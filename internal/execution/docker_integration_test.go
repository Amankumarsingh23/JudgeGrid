//go:build sandboxintegration

package execution

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func sandbox(t *testing.T, slots int) *Docker {
	t.Helper()
	if os.Getenv("JUDGEGRID_SANDBOX_TESTS") != "1" {
		t.Fatal("sandbox tests require explicit JUDGEGRID_SANDBOX_TESTS=1 on a trusted local controller host")
	}
	root := os.Getenv("JUDGEGRID_SANDBOX_ROOT")
	if root == "" {
		root = "."
		if _, err := os.Stat(filepath.Join(root, "infra/sandbox/seccomp.json")); err != nil {
			root = "../.."
		}
	}
	images, err := LoadImages(filepath.Join(root, ".sandbox-images.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDocker(images, filepath.Join(root, "infra/sandbox/seccomp.json"), slots)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	environment, err := d.Environment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("backend=%s kernel=%s cgroup=%s docker=%s", environment.OperatingSystem, environment.KernelVersion, environment.CgroupVersion, environment.ServerVersion)
	return d
}
func noLeftovers(t *testing.T, result Result) {
	t.Helper()
	if !result.Cleaned {
		t.Fatal("cleanup not confirmed")
	}
	if result.ExecutionID == "" {
		return
	}
	out, err := control(context.Background(), "ps", "--all", "--quiet", "--filter", "label=judgegrid.sandbox=true", "--filter", "name="+result.ExecutionID)
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("leftover sandbox: %s %v", out, err)
	}
	out, err = control(context.Background(), "volume", "ls", "--quiet", "--filter", "name="+result.ExecutionID+"-work")
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("leftover workspace: %s %v", out, err)
	}
}
func TestSandboxFixtureMatrix(t *testing.T) {
	d := sandbox(t, 2)
	t.Setenv("JUDGEGRID_HOST_SECRET", "not-forwarded")
	for _, tc := range []struct {
		name     string
		want     Outcome
		contains string
		compile  bool
	}{
		{"cpp-hello", Exited, "5\n", false}, {"python-hello", Exited, "5\n", false},
		{"cpp-syntax", NonzeroExit, "error:", true}, {"cpp-loop", WallTimeout, "", false},
		{"python-sleep", WallTimeout, "", false}, {"python-memory", MemoryExceeded, "", false},
		{"python-processes", Exited, "PID_LIMIT_ENFORCED", false},
		{"python-stdout", OutputExceeded, "", false}, {"python-stderr", OutputExceeded, "", false},
		{"python-network", Exited, "NETWORK_DENIED", false}, {"python-files", Exited, "SECRET_ENV False", false},
		{"cpp-crash", NonzeroExit, "", false}, {"python-crash", NonzeroExit, "controlled fixture crash", false},
		{"python-policy", Exited, "NAMESPACE_CHANGE_DENIED", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := Fixture(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			result, err := d.Execute(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			noLeftovers(t, result)
			phase := result.Run
			if tc.compile {
				phase = result.Compile
				if result.Run != nil {
					t.Fatal("syntax error reached execution")
				}
			}
			if phase == nil || phase.Outcome != tc.want {
				t.Fatalf("want %s: %+v", tc.want, result)
			}
			if tc.contains != "" && !strings.Contains(phase.Stdout+phase.Stderr, tc.contains) {
				t.Fatalf("missing %q: %+v", tc.contains, phase)
			}
			if len(phase.Stdout)+len(phase.Stderr) > int(req.RunLimits.OutputBytes) {
				t.Fatal("unbounded capture")
			}
			if strings.Contains(phase.Stdout, "UNEXPECTED_") {
				t.Fatal(phase.Stdout)
			}
			if tc.name == "python-policy" {
				for _, value := range []string{"UID 10001", "NoNewPrivs:\t1", "Seccomp:\t2", "CapEff:\t0000000000000000", "CPU_MAX 100000 100000", "MEMORY_MAX 67108864", "PIDS_MAX 16"} {
					if !strings.Contains(phase.Stdout, value) {
						t.Fatalf("missing observed control %q: %s", value, phase.Stdout)
					}
				}
			}
			if tc.name == "python-memory" && !phase.OOMKilled {
				t.Fatal("OOM attribution lacks daemon evidence")
			}
			if phase.CPUTimeNS != nil || phase.PeakMemoryBytes != nil {
				t.Fatal("unexpected fabricated measurement")
			}
			t.Logf("phase=%s outcome=%s exit=%d wall=%s output=%d OOMKilled=%v cleaned=%v", phase.Phase, phase.Outcome, phase.ExitCode, phase.WallTime, len(phase.Stdout)+len(phase.Stderr), phase.OOMKilled, result.Cleaned)
		})
	}
}
func TestSandboxCompileBudgets(t *testing.T) {
	d := sandbox(t, 1)
	t.Run("timeout", func(t *testing.T) {
		req, _ := Fixture("cpp-hello")
		req.CompileLimits.WallTime = 50 * time.Millisecond
		result, err := d.Execute(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		noLeftovers(t, result)
		if result.Compile == nil || result.Compile.Outcome != WallTimeout || result.Run != nil {
			t.Fatalf("compile budget not enforced: %+v", result)
		}
	})
	t.Run("diagnostics", func(t *testing.T) {
		req, _ := Fixture("cpp-syntax")
		req.Source = []byte(strings.Repeat("invalid syntax here;\n", 300))
		req.CompileLimits.OutputBytes = 128
		result, err := d.Execute(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		noLeftovers(t, result)
		if result.Compile == nil || result.Compile.Outcome != OutputExceeded || len(result.Compile.Stdout)+len(result.Compile.Stderr) > 128 || result.Run != nil {
			t.Fatalf("diagnostic bound not enforced: %+v", result)
		}
	})
}
func TestSandboxCancellation(t *testing.T) {
	d := sandbox(t, 1)
	req, _ := Fixture("python-sleep")
	req.RunLimits.WallTime = 20 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type completed struct {
		result Result
		err    error
	}
	done := make(chan completed, 1)
	go func() { result, err := d.Execute(ctx, req); done <- completed{result, err} }()
	deadline := time.Now().Add(15 * time.Second)
	running := false
	for time.Now().Before(deadline) {
		out, err := control(context.Background(), "ps", "--format", "{{.Names}}", "--filter", "label=judgegrid.sandbox=true")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "-run") {
			running = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !running {
		cancel()
		<-done
		t.Fatal("runtime did not start")
	}
	cancel()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		noLeftovers(t, got.result)
		if got.result.Run == nil || got.result.Run.Outcome != Canceled {
			t.Fatalf("cancel did not terminate run: %+v", got.result)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("sandbox cancellation did not finish")
	}
}
func TestSandboxConcurrencyBound(t *testing.T) {
	d := sandbox(t, 2)
	req, _ := Fixture("python-hello")
	req.Source = []byte("import time;time.sleep(0.3);print('done')")
	var wg sync.WaitGroup
	results := make(chan Result, 4)
	failures := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := d.Execute(context.Background(), req)
			if err != nil {
				failures <- err
			} else {
				results <- result
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	for result := range results {
		noLeftovers(t, result)
		if result.Run == nil || result.Run.Outcome != Exited {
			t.Errorf("execution failed %+v", result)
		}
	}
	if d.PeakActive() != 2 {
		t.Fatalf("slot limit not observed: %d", d.PeakActive())
	}
	t.Log("four concurrent requests; peak active execution slots = 2")
}
func TestSandboxMissingImageIsInfrastructure(t *testing.T) {
	d := sandbox(t, 1)
	d.images.Python = "sha256:" + strings.Repeat("0", 64)
	req, _ := Fixture("python-hello")
	result, err := d.Execute(context.Background(), req)
	var infra *InfrastructureError
	if !errors.As(err, &infra) || infra.Operation != "image" || result.Run != nil {
		t.Fatalf("missing image misclassified: %+v %v", result, err)
	}
	noLeftovers(t, result)
}
func TestSandboxFailedProgramStartupCleanup(t *testing.T) {
	d := sandbox(t, 1)
	// Trusted test image with a deliberately absent entrypoint: create succeeds, start must fail.
	directory := t.TempDir()
	if d.images.PythonBase == "" {
		t.Fatal("trusted base digest required for bad-start fixture")
	}
	if err := os.WriteFile(filepath.Join(directory, "Dockerfile"), []byte("FROM "+d.images.PythonBase+"\nENTRYPOINT [\"/missing-trusted-test-entrypoint\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tag := "judgegrid-badstart:" + strings.ReplaceAll(strconv.FormatInt(time.Now().UnixNano(), 10), "-", "")
	if _, err := control(context.Background(), "build", "--tag", tag, directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := control(context.Background(), "image", "rm", "--force", tag); err != nil {
			t.Error(err)
		}
	})
	out, err := control(context.Background(), "image", "inspect", "--format", "{{.Id}}", tag)
	if err != nil {
		t.Fatal(err)
	}
	d.images.Python = strings.TrimSpace(string(out))
	req, _ := Fixture("python-hello")
	result, err := d.Execute(context.Background(), req)
	var infra *InfrastructureError
	if !errors.As(err, &infra) {
		t.Fatalf("expected actual startup infrastructure failure, got %v", err)
	}
	noLeftovers(t, result)
}

func TestSandboxCompilerCrashCleanup(t *testing.T) {
	d := sandbox(t, 1)
	if d.images.CPPBase == "" {
		t.Fatal("trusted compiler base required")
	}
	// Fault injection in a trusted setup image: the compiler process crashes itself, not the host.
	directory := t.TempDir()
	dockerfile := "FROM " + d.images.CPPBase + "\nRUN printf '#!/bin/sh\\nkill -SEGV $$\\n' > /usr/local/bin/g++ && chmod 755 /usr/local/bin/g++ && mkdir -p /work && chown 10001:10001 /work\n"
	if err := os.WriteFile(filepath.Join(directory, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
		t.Fatal(err)
	}
	tag := "judgegrid-compiler-crash:" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := control(context.Background(), "build", "--tag", tag, directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := control(context.Background(), "image", "rm", "--force", tag); err != nil {
			t.Error(err)
		}
	})
	out, err := control(context.Background(), "image", "inspect", "--format", "{{.Id}}", tag)
	if err != nil {
		t.Fatal(err)
	}
	d.images.CPP = strings.TrimSpace(string(out))
	req, _ := Fixture("cpp-hello")
	result, err := d.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	noLeftovers(t, result)
	if result.Compile == nil || result.Compile.Outcome != NonzeroExit || result.Compile.ExitCode != 139 || result.Run != nil {
		t.Fatalf("compiler crash misclassified: %+v", result)
	}
}

func TestSandboxIndependentWatchdog(t *testing.T) {
	d := sandbox(t, 1)
	id := "judgegrid-sandbox-watchdog-" + strings.ToLower(rand.Text())
	volume := id + "-work"
	container := id + "-run"
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = control(context.Background(), "rm", "--force", container)
		_, _ = control(context.Background(), "volume", "rm", volume)
	})
	if _, err := control(ctx, "volume", "create", "--label", "judgegrid.sandbox=true", volume); err != nil {
		t.Fatal(err)
	}
	limits := DefaultRunLimits()
	limits.WallTime = 200 * time.Millisecond
	if _, err := control(ctx, d.createArgs(container, d.images.Python, volume, limits, true, []string{"python3", "-c", "import time;time.sleep(20)"})...); err != nil {
		t.Fatal(err)
	}
	if err := d.verify(ctx, container, limits, true); err != nil {
		t.Fatal(err)
	}
	// Deliberately bypass runPhase's host timer: termination must come from the container watchdog.
	start := time.Now()
	_, err := control(ctx, "start", "--attach", container)
	if err == nil {
		t.Fatal("sleeping fixture unexpectedly exited zero")
	}
	state, err := containerState(ctx, container)
	if err != nil || state.Running || state.ExitCode != 137 || time.Since(start) > 3*time.Second {
		t.Fatalf("independent watchdog failed: %+v %v", state, err)
	}
	if _, err := control(ctx, "rm", "--force", container); err != nil {
		t.Fatal(err)
	}
	if _, err := control(ctx, "volume", "rm", volume); err != nil {
		t.Fatal(err)
	}
	noLeftovers(t, Result{ExecutionID: id, Cleaned: true})
	t.Log("container watchdog stopped a 20-second sleeper without a host execution timer")
}
