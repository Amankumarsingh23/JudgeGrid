//go:build linux

package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGitKernelLimitsAndCancellation(t *testing.T) {
	f := newFixture(t, "sha1")
	f.write("main.go", "package fixture\n")
	f.commit()
	ctx, cancel := context.WithCancel(context.Background())
	g, e := OpenGit(ctx, Trust{Roots: []string{f.dir}}, f.dir)
	if e != nil {
		t.Fatal(e)
	}
	commit := f.git(nil, "rev-parse", "HEAD")
	if _, _, e = g.Info(commit); e != nil {
		g.Close()
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join("/proc", strconv.Itoa(g.cmd.Process.Pid), "limits"))
	if e != nil {
		g.Close()
		t.Fatal(e)
	}
	for _, want := range []string{"Max address space         536870912", "Max cpu time              60"} {
		if !strings.Contains(string(b), want) {
			g.Close()
			t.Fatal("kernel resource limit missing", string(b))
		}
	}
	start := time.Now()
	cancel()
	_, _, e = g.Resolve("HEAD")
	g.Close()
	if e == nil || time.Since(start) > 2*time.Second {
		t.Fatal("canceled Git process did not terminate", e)
	}
}
