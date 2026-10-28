//go:build sandboxintegration

package execution

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSandboxHardeningLimitsAndSyscalls(t *testing.T) {
	d := sandbox(t, 1)
	for _, tc := range []struct{ name, source string }{{"descriptor-limit", `import os,errno
f=[]
try:
 for i in range(80): f.append(os.open('/dev/null',os.O_RDONLY))
except OSError as e: assert e.errno==errno.EMFILE
assert len(f)<64
print('bounded')`}, {"per-file-limit", `import os,errno
try:
 with open('/tmp/file','wb',buffering=0) as f:
  for i in range(20): f.write(b'x'*1048576)
except OSError as e: assert e.errno==errno.EFBIG
else: raise AssertionError('missing fsize')
print('bounded')`}, {"tmpfs-limit", `import errno
try:
 for i in range(9):
  with open('/tmp/f'+str(i),'wb',buffering=0) as f:
   for j in range(8): f.write(b'x'*1048576)
except OSError as e: assert e.errno==errno.ENOSPC
else: raise AssertionError('missing tmpfs bound')
print('bounded')`}, {"syscall-and-cgroup-evidence", `import ctypes,os
s=open('/proc/self/status').read()
assert 'NoNewPrivs:\t1' in s and 'Seccomp:\t2' in s and 'CapEff:\t0000000000000000' in s
lib=ctypes.CDLL(None,use_errno=True)
assert lib.unshare(0x20000)==-1 and ctypes.get_errno()==1
assert open('/sys/fs/cgroup/pids.max').read().strip()=='16'
a,b=map(int,open('/sys/fs/cgroup/cpu.max').read().split());assert a==b and a>0
assert open('/sys/fs/cgroup/memory.max').read().strip()==str(256*1024*1024)
print('bounded')`}} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := Fixture("python-hello")
			r.Source = []byte(tc.source)
			r.RunLimits.MemoryBytes = 256 << 20
			r.RunLimits.WallTime = 5 * time.Second
			v, e := d.Execute(context.Background(), r)
			if e != nil || v.Run == nil || v.Run.Outcome != Exited || strings.TrimSpace(v.Run.Stdout) != "bounded" {
				t.Fatalf("%+v %v", v, e)
			}
			noLeftovers(t, v)
			t.Log("observed bounded fixture; EPERM attribution is seccomp/capability ambiguous")
		})
	}
}
func TestSandboxConcurrentPrivateWorkspaces(t *testing.T) {
	d := sandbox(t, 2)
	t.Setenv("DATABASE_URL", "host-secret-sentinel")
	var wg sync.WaitGroup
	fail := make(chan error, 2)
	for _, tag := range []string{"private-alpha", "private-beta"} {
		wg.Add(1)
		go func(tag string) {
			defer wg.Done()
			r, _ := Fixture("python-hello")
			r.Source = []byte(fmt.Sprintf("#%s\nimport os,sys,time\nassert 'DATABASE_URL' not in os.environ\nassert open('/work/main.py').readline().strip()=='#%s'\nassert os.listdir('/work')==['main.py']\nassert not os.path.exists('/var/lib/docker/volumes')\nopen('/tmp/shared-name','w').write('%s')\ntime.sleep(.3)\nassert open('/tmp/shared-name').read()=='%s'\ntry:\n open('/work/forbidden','w')\nexcept OSError: pass\nelse: raise AssertionError('writable artifact')\nprint('%s');print('%s',file=sys.stderr)", tag, tag, tag, tag, tag, tag))
			v, e := d.Execute(context.Background(), r)
			if e != nil || v.Run == nil || v.Run.Outcome != Exited || strings.TrimSpace(v.Run.Stdout) != tag || strings.TrimSpace(v.Run.Stderr) != tag || !v.Cleaned {
				fail <- fmt.Errorf("cross-submission fixture failure: %v", e)
			}
		}(tag)
	}
	wg.Wait()
	close(fail)
	for e := range fail {
		t.Fatal(e)
	}
}

func TestSandboxRejectsUnsafeRetainedPolicy(t *testing.T) {
	d := sandbox(t, 1)
	r, _ := Fixture("python-hello")
	artifact, _, e := d.Prepare(context.Background(), r)
	if e != nil || artifact == nil {
		t.Fatal(e)
	}
	defer artifact.Close()
	p := artifact.(*preparedDocker)
	for _, tc := range []struct{ name, flag, value string }{{"network", "--network", "bridge"}, {"seccomp", "--security-opt", "seccomp=unconfined"}, {"mount", "--mount", "type=volume,src=" + p.volume + ",dst=/wrong,readonly"}} {
		t.Run(tc.name, func(t *testing.T) {
			name := p.provenance.ExecutionID + "-run-16"
			command := []string{"python3", "-I", "-B", "/work/main.py"}
			args := d.createArgs(name, p.image, p.volume, r.RunLimits, true, command, p.provenance.Labels("run-16"))
			for i := 0; i < len(args)-1; i++ {
				if args[i] == tc.flag {
					if tc.name == "seccomp" && !strings.HasPrefix(args[i+1], "seccomp=") {
						continue
					}
					args[i+1] = tc.value
					break
				}
			}
			if _, e := control(context.Background(), args...); e != nil {
				t.Fatal(e)
			}
			defer RemoveOwned(context.Background(), "container", name, p.provenance, "run-16")
			if e := d.verify(context.Background(), name, r.RunLimits, true, policyExpectation{p.image, p.volume, command}); e == nil {
				t.Fatal("unsafe retained policy accepted")
			}
			state, e := containerState(context.Background(), name)
			if e != nil || state.Running || state.Status != "created" {
				t.Fatal("rejected policy executed", state, e)
			}
		})
	}
}
