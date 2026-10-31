//go:build windows

package ingest

import (
	"context"
	"golang.org/x/sys/windows"
	"os/exec"
	"syscall"
	"unsafe"
)

func limitedGit(ctx context.Context, args []string) *gitProcess {
	c := exec.CommandContext(ctx, "git", args...)
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	return &gitProcess{Cmd: c, context: ctx}
}

// Attach the suspended process before it can parse input. Closing the job kills
// descendants too; its 512 MiB job limit covers Git transport/index-pack helpers.
func limitStartedGit(c *exec.Cmd) (func(), error) {
	job, e := windows.CreateJobObject(nil, nil)
	if e != nil {
		return nil, e
	}
	closeJob := func() { windows.CloseHandle(job) }
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_JOB_MEMORY
	info.JobMemoryLimit = 512 << 20
	if _, e = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); e != nil {
		closeJob()
		return nil, e
	}
	proc, e := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(c.Process.Pid))
	if e != nil {
		closeJob()
		return nil, e
	}
	defer windows.CloseHandle(proc)
	if e = windows.AssignProcessToJobObject(job, proc); e != nil {
		closeJob()
		return nil, e
	}
	snapshot, e := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if e != nil {
		closeJob()
		return nil, e
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for e = windows.Thread32First(snapshot, &entry); e == nil; e = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID == uint32(c.Process.Pid) {
			thread, x := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if x != nil {
				closeJob()
				return nil, x
			}
			_, x = windows.ResumeThread(thread)
			windows.CloseHandle(thread)
			if x != nil {
				closeJob()
				return nil, x
			}
			return closeJob, nil
		}
	}
	closeJob()
	return nil, ErrBudget
}
