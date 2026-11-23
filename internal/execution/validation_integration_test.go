//go:build sandboxintegration

package execution

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestValidationDockerProfiles(t *testing.T) {
	d := sandbox(t, 1)
	ctx := context.Background()
	runtime, e := d.ValidationRuntime(ctx)
	if e != nil {
		t.Fatal(e)
	}
	text := map[string]string{
		"go.mod":   "module example.test/validation\n\ngo 1.26.0\n",
		"value.go": "package fixture\nfunc Value() int { return 1 }\n",
		"value_test.go": `package fixture
import("net";"os";"strings";"testing";"time")
func TestBoundaries(t *testing.T){
 if os.Getuid()!=10001 {t.Fatal("unexpected uid")}
 state,e:=os.ReadFile("/proc/self/status"); if e!=nil || !strings.Contains(string(state),"CapEff:\t0000000000000000") || !strings.Contains(string(state),"NoNewPrivs:\t1") || !strings.Contains(string(state),"Seccomp:\t2"){t.Fatal("privilege boundary")}
 for key,want:=range map[string]string{"memory.max":"805306368","pids.max":"64","cpu.max":"100000 100000"} {b,e:=os.ReadFile("/sys/fs/cgroup/"+key);if e!=nil || strings.TrimSpace(string(b))!=want {t.Fatal("cgroup boundary",key,string(b),e)}}
 for _,name:=range []string{"/var/run/docker.sock","/run/secrets","/host","/root/.ssh"} {if _,e:=os.Stat(name);e==nil{t.Fatal("host path exposed")}}
 if os.Getenv("SENTINEL_PRIVATE")!=""{t.Fatal("host environment exposed")}
 if e:=os.WriteFile("/work/probe",[]byte("x"),0600);e==nil{t.Fatal("source write allowed")}
 c,e:=net.DialTimeout("tcp","192.0.2.1:80",200*time.Millisecond);if e==nil{c.Close();t.Fatal("network available")}
 t.Log("network, source and privilege restrictions observed")
}
`,
	}
	t.Setenv("SENTINEL_PRIVATE", "sentinel-not-forwarded")
	files := []ValidationFile{}
	for name, body := range text {
		files = append(files, ValidationFile{Path: name, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(body))), Bytes: []byte(body)})
	}
	name := "jg-v19-" + strings.Repeat("b", 32)
	out, e := d.ExecuteValidation(ctx, name, "go-unit", runtime, files, func(_ context.Context, action func() error) error { return action() })
	if e != nil || out.Run == nil || out.Run.ExitCode != 0 || !out.Cleaned {
		t.Fatalf("sandbox failed: %v stage=%+v run=%+v", e, out.Stage, out.Run)
	}
	if !strings.Contains(out.Run.Stdout, "network, source and privilege restrictions observed") {
		t.Fatal("runtime assertions missing", out.Run.Stdout)
	}
	t.Logf("sandbox policy observed: image=%s stage=%s run=%s resources=%v", runtime.Image, out.Stage.WallTime, out.Run.WallTime, out.Run.Limits)
	corrupt := append([]ValidationFile{}, files...)
	corrupt[len(corrupt)-1].SHA256 = strings.Repeat("0", 64)
	bad, e := d.ExecuteValidation(ctx, name, "go-unit", runtime, corrupt, func(_ context.Context, action func() error) error { return action() })
	if e == nil || bad.Run != nil || !bad.Cleaned {
		t.Fatal("bad staged hash entered the runtime", e, bad)
	}
	for _, tc := range []struct {
		kind, body string
		want       Outcome
	}{
		{"output", `package fixture
import("fmt";"strings";"testing")
func TestLimit(t *testing.T){fmt.Println(strings.Repeat("a",100000))}
`, OutputExceeded},
		{"wall", `package fixture
import("time";"testing")
func TestLimit(t *testing.T){time.Sleep(2*time.Minute)}
`, WallTimeout},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			work := append([]ValidationFile{}, files...)
			for i := range work {
				if work[i].Path == "value_test.go" {
					work[i].Bytes = []byte(tc.body)
					work[i].SHA256 = fmt.Sprintf("%x", sha256.Sum256(work[i].Bytes))
				}
			}
			result, e := d.ExecuteValidation(ctx, name, "go-unit", runtime, work, func(_ context.Context, action func() error) error { return action() })
			if e != nil || result.Run == nil || result.Run.Outcome != tc.want || !result.Cleaned {
				t.Fatalf("limit not observed: %v %+v", e, result.Run)
			}
			t.Logf("outcome=%s duration=%s exit=%d captured_bytes=%d", result.Run.Outcome, result.Run.WallTime, result.Run.ExitCode, len(result.Run.Stdout)+len(result.Run.Stderr))
		})
	}
	t.Run("cancel", func(t *testing.T) {
		work, stop := context.WithCancel(ctx)
		defer stop()
		type observed struct {
			phase ValidationPhases
			err   error
		}
		done := make(chan observed, 1)
		go func() {
			p, e := d.ExecuteValidation(work, name, "go-unit", runtime, files, func(_ context.Context, action func() error) error { return action() })
			done <- observed{p, e}
		}()
		deadline := time.Now().Add(15 * time.Second)
		running := false
		for time.Now().Before(deadline) {
			r, e := InspectResource(ctx, "container", name+"-run")
			if e == nil && r.Running {
				running = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		stop()
		result := <-done
		if !running || result.err != nil || result.phase.Run == nil || result.phase.Run.Outcome != Canceled || !result.phase.Cleaned {
			t.Fatalf("running cancellation not observed: %v %+v", result.err, result.phase.Run)
		}
		t.Logf("running container cancelled and exact resources removed")
	})
}
