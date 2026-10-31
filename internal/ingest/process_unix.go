//go:build !windows

package ingest

import (
	"context"
	"os/exec"
)

// prlimit execs Git with inherited address-space/CPU limits. Missing enforcement
// fails closed; there is no unbounded fallback and no elevated/cgroup authority.
func limitedGit(ctx context.Context, args []string) *gitProcess {
	return &gitProcess{Cmd: exec.CommandContext(ctx, "prlimit", append([]string{"--as=536870912", "--cpu=60", "--", "git"}, args...)...), context: ctx}
}
func limitStartedGit(*exec.Cmd) (func(), error) { return func() {}, nil }
