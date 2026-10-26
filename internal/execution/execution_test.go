package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRequestAndImagePolicy(t *testing.T) {
	valid, _ := Fixture("python-hello")
	if valid.Validate() != nil {
		t.Fatal("fixture invalid")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Request)
	}{
		{"language", func(r *Request) { r.Language = "bash" }}, {"empty", func(r *Request) { r.Source = nil }},
		{"source size", func(r *Request) { r.Source = []byte(strings.Repeat("x", 65537)) }}, {"NUL", func(r *Request) { r.Source = []byte("x\x00") }},
		{"input size", func(r *Request) { r.Stdin = make([]byte, 65537) }}, {"wall", func(r *Request) { r.RunLimits.WallTime = 0 }},
		{"memory", func(r *Request) { r.RunLimits.MemoryBytes = 1 << 30 }}, {"output", func(r *Request) { r.CompileLimits.OutputBytes = 1 << 30 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			tc.mutate(&r)
			if !errors.Is(r.Validate(), ErrInvalid) {
				t.Fatal("invalid request accepted")
			}
		})
	}
	if (Images{CPP: "gcc:latest", Python: "python:latest"}).Validate() == nil {
		t.Fatal("mutable tags accepted")
	}
	if _, err := Fixture("arbitrary-shell"); err == nil {
		t.Fatal("arbitrary fixture accepted")
	}
}
func TestClassification(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		exit                         int
		oom, timeout, output, cancel bool
		want                         Outcome
	}{
		{"normal", 0, false, false, false, false, Exited}, {"program crash", 139, false, false, false, false, NonzeroExit},
		{"SIGKILL alone is not OOM", 137, false, false, false, false, NonzeroExit},
		{"confirmed OOM", 137, true, false, false, false, MemoryExceeded}, {"deadline", 137, false, true, false, false, WallTimeout},
		{"output", 137, false, false, true, false, OutputExceeded}, {"cancel", 137, false, false, false, true, Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if classify(tc.exit, tc.oom, tc.timeout, tc.output, tc.cancel) != tc.want {
				t.Fatal("incorrect outcome")
			}
		})
	}
}
func TestBoundedConcurrentOutput(t *testing.T) {
	c := newCapture(1024)
	var wg sync.WaitGroup
	for _, stderr := range []bool{false, true} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if n, err := c.writer(stderr).Write([]byte(strings.Repeat("x", 100))); n != 100 || err != nil {
					t.Error("writer failed")
				}
			}
		}()
	}
	wg.Wait()
	out, errout := c.text()
	if len(out)+len(errout) != 1024 {
		t.Fatal("output not bounded")
	}
	select {
	case <-c.exceeded:
	default:
		t.Fatal("limit breach not signaled")
	}
}
func TestIsolationArguments(t *testing.T) {
	d := &Docker{seccomp: "verified-profile.json"}
	args := strings.Join(d.createArgs("controlled", "sha256:abc", "private-volume", DefaultRunLimits(), true, []string{"/work/program"}), " ")
	for _, required := range []string{"--network none", "--read-only", "--user 10001:10001", "--cap-drop ALL", "no-new-privileges=true", "--pids-limit 16", "--cpus 1", "--memory-swap 67108864", "--log-driver none", "--pull never", "dst=/work,readonly"} {
		if !strings.Contains(args, required) {
			t.Fatal(required)
		}
	}
	for _, forbidden := range []string{"--privileged", "docker.sock", "type=bind", "--pid host", "--network host"} {
		if strings.Contains(args, forbidden) {
			t.Fatal(forbidden)
		}
	}
}
func TestCancellationWhileWaitingForSlot(t *testing.T) {
	d := &Docker{slots: make(chan struct{}, 1)}
	d.slots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, _ := Fixture("python-hello")
	if _, err := d.Execute(ctx, r); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestInfrastructureErrorAndMissingObject(t *testing.T) {
	e := &InfrastructureError{Phase: "run", Operation: "start", Cause: context.DeadlineExceeded}
	if !errors.Is(e, context.DeadlineExceeded) || !strings.Contains(e.Error(), "run/start") {
		t.Fatal(e)
	}
	if !missingObject(errors.New("No such container")) || missingObject(errors.New("daemon unavailable")) {
		t.Fatal("cleanup error mapping")
	}
	if _, err := NewDocker(Images{}, "missing", 2); err == nil {
		t.Fatal("invalid images accepted")
	}
	var _ Executor = (*Docker)(nil)
}

func TestSeccompTamperingRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"defaultAction":"SCMP_ACT_ALLOW"}`), 0600); err != nil {
		t.Fatal(err)
	}
	images := Images{CPP: "sha256:" + strings.Repeat("a", 64), Python: "sha256:" + strings.Repeat("b", 64)}
	if _, err := NewDocker(images, path, 1); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatal("modified policy accepted")
	}
}
