//go:build !windows

package ingest

import (
	"context"
	"os/exec"
	"strconv"
)

// prlimit execs Git with inherited address-space/CPU limits. Missing enforcement
// fails closed; there is no unbounded fallback and no elevated/cgroup authority.
func limitedGit(ctx context.Context, args []string) *gitProcess {
	return limitedProgram(ctx, "git", args)
}
func limitedProgram(ctx context.Context, program string, args []string) *gitProcess {
	return boundedProgram(ctx, program, args, 512<<20)
}
func boundedProgram(ctx context.Context, program string, args []string, memory uintptr) *gitProcess {
	cpu := "60"
	if memory > 512<<20 {
		cpu = "120"
	}
	return &gitProcess{Cmd: exec.CommandContext(ctx, "prlimit", append([]string{"--as=" + strconv.FormatUint(uint64(memory), 10), "--cpu=" + cpu, "--", program}, args...)...), context: ctx, memory: memory}
}
func limitStartedProgram(*exec.Cmd, uintptr) (func(), error) { return func() {}, nil }
