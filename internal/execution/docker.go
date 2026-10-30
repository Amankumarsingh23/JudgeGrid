package execution

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/buildcache"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Docker is a trusted LOCAL controller. Never put daemon access in an API/Redis worker container.
type Docker struct {
	cache                                     *buildcache.Cache
	images                                    Images
	seccomp                                   string
	controller                                string
	slots                                     chan struct{}
	active, peak                              atomic.Int32
	preparations, compilations, runtimeStarts atomic.Int64
}

func NewDocker(images Images, profile string, slots int) (*Docker, error) {
	if err := images.Validate(); err != nil {
		return nil, &InfrastructureError{"setup", "policy", err}
	}
	if slots < 1 || slots > 4 {
		return nil, &InfrastructureError{"setup", "policy", fmt.Errorf("sandbox slots must be 1..4")}
	}
	content, err := os.ReadFile(profile)
	if err != nil {
		return nil, &InfrastructureError{"setup", "policy", err}
	}
	if fmt.Sprintf("%x", sha256.Sum256(content)) != SeccompSHA256 {
		return nil, &InfrastructureError{"setup", "policy", fmt.Errorf("seccomp checksum mismatch")}
	}
	// Copy verified policy to a controller-owned temporary file; the workload cannot access this path.
	file, err := os.CreateTemp("", "judgegrid-seccomp-*.json")
	if err != nil {
		return nil, err
	}
	if _, err = file.Write(content); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	if err = file.Close(); err != nil {
		os.Remove(file.Name())
		return nil, err
	}
	if err = os.Chmod(file.Name(), 0400); err != nil {
		os.Remove(file.Name())
		return nil, err
	}
	return &Docker{images: images, seccomp: file.Name(), slots: make(chan struct{}, slots), controller: strings.ToLower(rand.Text())}, nil
}

// Close must be called after all Execute calls have returned.
func (d *Docker) Close() error {
	if err := os.Chmod(d.seccomp, 0600); err != nil {
		return err
	}
	return os.Remove(d.seccomp)
}
func (d *Docker) PeakActive() int32 { return d.peak.Load() }

// CheckImages verifies both configured capabilities before an automatic controller accepts work.
func (d *Docker) CheckImages(ctx context.Context) error {
	for _, id := range []string{d.images.CPP, d.images.Python} {
		if _, err := control(ctx, "image", "inspect", id); err != nil {
			return &InfrastructureError{"setup", "image", err}
		}
	}
	return nil
}

type Environment struct {
	ServerVersion   string   `json:"ServerVersion"`
	KernelVersion   string   `json:"KernelVersion"`
	OperatingSystem string   `json:"OperatingSystem"`
	OSType          string   `json:"OSType"`
	CgroupVersion   string   `json:"CgroupVersion"`
	MemoryLimit     bool     `json:"MemoryLimit"`
	SwapLimit       bool     `json:"SwapLimit"`
	CpuCfsQuota     bool     `json:"CpuCfsQuota"`
	PidsLimit       bool     `json:"PidsLimit"`
	SecurityOptions []string `json:"SecurityOptions"`
}

func (d *Docker) Environment(ctx context.Context) (Environment, error) {
	var e Environment
	out, err := control(ctx, "info", "--format", "{{json .}}")
	if err == nil {
		err = json.Unmarshal(out, &e)
	}
	if err != nil {
		return e, &InfrastructureError{"setup", "capabilities", err}
	}
	seccomp := false
	for _, option := range e.SecurityOptions {
		if strings.Contains(option, "seccomp") {
			seccomp = true
		}
	}
	if e.OSType != "linux" || e.CgroupVersion != "2" || !e.MemoryLimit || !e.SwapLimit || !e.CpuCfsQuota || !e.PidsLimit || !seccomp {
		return e, &InfrastructureError{"setup", "capabilities", fmt.Errorf("Linux cgroup v2, CPU/memory/swap/PID controllers and seccomp required")}
	}
	return e, nil
}
func control(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.WaitDelay = 5 * time.Second
	output := newCapture(64 << 10)
	cmd.Stdout = output.writer(false)
	cmd.Stderr = output.writer(true)
	err := cmd.Run()
	select {
	case <-output.exceeded:
		return nil, fmt.Errorf("bounded Docker control output exceeded")
	default:
	}
	stdout, stderr := output.text()
	if err != nil {
		return nil, fmt.Errorf("docker %s: %w (%s)", args[0], err, strings.TrimSpace(stderr))
	}
	return []byte(stdout), nil
}

func (d *Docker) Execute(ctx context.Context, req Request) (result Result, err error) {
	artifact, result, err := d.Prepare(ctx, req)
	if err != nil || artifact == nil {
		return result, err
	}
	result.Run, err = artifact.Run(ctx, req.Stdin, req.RunLimits)
	closeErr := artifact.Close()
	result.Cleaned = artifact.Summary().Cleaned
	return result, errors.Join(err, closeErr)
}

// Prepare compiles/writes source once. The returned artifact is local to this evaluation, not a global cache.
func (d *Docker) Prepare(ctx context.Context, req Request) (artifact Prepared, result Result, err error) {
	ctx, end := telemetry.Begin(ctx, "sandbox.workspace", req.Language)
	defer func() { end(err) }()
	if err = req.Validate(); err != nil {
		return nil, result, err
	}
	select {
	case d.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, result, ctx.Err()
	}
	n := d.active.Add(1)
	for old := d.peak.Load(); n > old; old = d.peak.Load() {
		if d.peak.CompareAndSwap(old, n) {
			break
		}
	}
	p := &preparedDocker{docker: d, request: req}
	p.result = Result{ExecutionID: "judgegrid-sandbox-" + strings.ToLower(rand.Text()), Policy: PolicyVersion}
	p.volume = p.result.ExecutionID + "-work"
	p.provenance = Provenance{ExecutionID: p.result.ExecutionID, ControllerID: d.controller, RunID: randomUUID(), SubmissionID: randomUUID(), OwnerHash: OwnerHash("fixture"), Generation: 1, Created: time.Now().UTC().Format(time.RFC3339Nano)}
	if identity, ok := ctx.Value(identityKey{}).(binding); ok {
		p.provenance.RunID = identity.run
		p.provenance.SubmissionID = identity.sub
		p.provenance.OwnerHash = OwnerHash(identity.owner)
		p.provenance.Generation = identity.generation
		p.registry, _ = ctx.Value(registryKey{}).(Registry)
	}
	defer func() {
		if artifact == nil {
			err = errors.Join(err, p.Close())
			result.Cleaned = p.Summary().Cleaned
		}
	}()
	result = p.result
	if p.provenance.Validate() != nil {
		return nil, result, &InfrastructureError{"setup", "provenance", ErrInvalid}
	}
	if p.registry != nil {
		if err = p.registry.RegisterSandbox(ctx, p.provenance); err != nil {
			return nil, result, &InfrastructureError{"setup", "registry", err}
		}
		p.registered = true
	}
	setup, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = d.Environment(setup)
	cancel()
	if err != nil {
		return nil, result, err
	}
	p.image = d.images.Python
	if req.Language == "cpp17" {
		p.image = d.images.CPP
	}
	if _, err = control(ctx, "image", "inspect", p.image); err != nil {
		return nil, result, &InfrastructureError{"setup", "image", err}
	}
	p.volumeCreated = true
	if err = p.protect(ctx, func() error {
		args := []string{"volume", "create"}
		args = append(args, labelArgs(p.provenance.Labels("artifact"))...)
		args = append(args, p.volume)
		_, e := control(ctx, args...)
		if e != nil {
			return e
		}
		r, e := InspectResource(ctx, "volume", p.volume)
		if e != nil {
			return e
		}
		if !OwnedResource(r, p.provenance, "artifact") {
			return ErrInvalid
		}
		return nil
	}); err != nil {
		return nil, result, &InfrastructureError{"setup", "volume", err}
	}
	if req.Language == "cpp17" {
		hit, cacheErr := p.cachedCompile(ctx)
		if cacheErr != nil {
			return nil, p.result, &InfrastructureError{"compile", "cache", cacheErr}
		}
		if !hit {
			d.compilations.Add(1)
			if d.cache != nil {
				d.cache.RecordBuild()
			}
			p.result.Compile, err = p.phase(ctx, "compile", req.CompileLimits, req.Source, false, []string{"/bin/sh", "-c", "umask 077; set -e; cat > /work/main.cpp; touch -d @0 /work/main.cpp; SOURCE_DATE_EPOCH=0 g++ -std=c++17 -O2 -pipe /work/main.cpp -o /work/program; test -f /work/program; test ! -L /work/program; test $(wc -c < /work/program) -le 16777216; chmod 0500 /work/program; chmod 0400 /work/main.cpp"})
			result = p.result
			if err != nil || p.result.Compile.Outcome != Exited {
				return nil, result, err
			}
			if err = p.publishCompile(ctx); err != nil {
				return nil, p.result, &InfrastructureError{"compile", "cache-publish", err}
			}
		}
	} else {
		p.result.Prepare, err = p.phase(ctx, "prepare", req.CompileLimits, req.Source, false, []string{"python3", "-I", "-c", "import os,sys;fd=os.open('/work/main.py',os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o400);f=os.fdopen(fd,'wb');f.write(sys.stdin.buffer.read(65537));f.close()"})
		result = p.result
		if err != nil {
			return nil, result, err
		}
		if p.result.Prepare.Outcome != Exited {
			return nil, result, &InfrastructureError{"prepare", "source", fmt.Errorf("source preparation failed: %s", p.result.Prepare.Outcome)}
		}
	}
	d.preparations.Add(1)
	return p, p.result, nil
}

func (d *Docker) createArgs(name, image, volume string, limits Limits, readonly bool, command []string, labels ...map[string]string) []string {
	mount := "type=volume,src=" + volume + ",dst=/work"
	if readonly {
		mount += ",readonly"
	}
	args := []string{"create", "--name", name, "--label", "judgegrid.sandbox=true", "--pull", "never", "--interactive", "--init", "--network", "none", "--ipc", "private", "--cgroupns", "private", "--hostname", "sandbox", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=" + d.seccomp, "--read-only", "--cpus", "1", "--memory", strconv.FormatInt(limits.MemoryBytes, 10), "--memory-swap", strconv.FormatInt(limits.MemoryBytes, 10), "--pids-limit", "16", "--ulimit", "nofile=64:64", "--ulimit", "core=0:0", "--ulimit", "fsize=16777216:16777216", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=67108864,mode=1777", "--shm-size", "8m", "--mount", mount, "--workdir", "/work", "--log-driver", "none", "--env", "HOME=/tmp", "--env", "LANG=C.UTF-8", "--env", "PATH=/usr/local/bin:/usr/bin:/bin", "--env", "PYTHONDONTWRITEBYTECODE=1", "--env", "PYTHONHASHSEED=0", image}
	if len(labels) > 0 {
		args = args[:len(args)-1]
		args = append(args, labelArgs(labels[0])...)
		args = append(args, image)
	}
	// Independent in-container watchdog also stops computation if the local controller is abruptly lost.
	args = append(args, "/usr/bin/timeout", "--signal=KILL", strconv.FormatFloat(limits.WallTime.Seconds(), 'f', 3, 64)+"s")
	return append(args, command...)
}

type policyExpectation struct {
	image, volume string
	command       []string
}

func (d *Docker) verify(ctx context.Context, name string, limits Limits, readonly bool, expected ...policyExpectation) error {
	out, err := control(ctx, "inspect", "--format", "{{json .}}", name)
	if err != nil {
		return err
	}
	return d.validateInspection(out, limits, readonly, expected...)
}
func (d *Docker) validateInspection(out []byte, limits Limits, readonly bool, expected ...policyExpectation) error {
	var v struct {
		Image  string
		Config struct {
			User            string
			Env             []string
			Image           string
			Cmd, Entrypoint []string
		}
		HostConfig struct {
			Init                                        *bool
			LogConfig                                   struct{ Type string }
			Tmpfs                                       map[string]string
			ShmSize                                     int64
			CapAdd, Devices, DeviceRequests, ExtraHosts []json.RawMessage
			Ulimits                                     []struct {
				Name       string
				Soft, Hard int64
			}
			Privileged                              bool
			ReadonlyRootfs                          bool
			NetworkMode                             string
			PidMode                                 string
			IpcMode                                 string
			CgroupnsMode                            string
			Memory, MemorySwap, NanoCpus, PidsLimit int64
			CapDrop, SecurityOpt, Binds             []string
		}
		Mounts []struct {
			Type, Destination string
			Name              string
			RW                bool
		}
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return err
	}
	h := v.HostConfig
	if h.Init == nil || !*h.Init || h.LogConfig.Type != "none" || h.ShmSize != 8<<20 || len(h.Tmpfs) != 1 || h.Tmpfs["/tmp"] != "rw,noexec,nosuid,nodev,size=67108864,mode=1777" || len(h.CapAdd)+len(h.Devices)+len(h.DeviceRequests)+len(h.ExtraHosts) > 0 || len(v.Config.Entrypoint) != 0 {
		return fmt.Errorf("mandatory bounded storage/init/privilege policy missing")
	}
	ulimits := map[string][2]int64{}
	for _, u := range h.Ulimits {
		ulimits[u.Name] = [2]int64{u.Soft, u.Hard}
	}
	if len(ulimits) != 3 || ulimits["nofile"] != [2]int64{64, 64} || ulimits["core"] != [2]int64{0, 0} || ulimits["fsize"] != [2]int64{16777216, 16777216} {
		return fmt.Errorf("required ulimits missing")
	}
	if len(expected) > 0 {
		e := expected[0]
		if v.Image != e.image || v.Config.Image != e.image || len(v.Mounts) != 1 || v.Mounts[0].Name != e.volume {
			return fmt.Errorf("artifact/image identity mismatch")
		}
		cmd := append([]string{"/usr/bin/timeout", "--signal=KILL", strconv.FormatFloat(limits.WallTime.Seconds(), 'f', 3, 64) + "s"}, e.command...)
		a, _ := json.Marshal(cmd)
		b, _ := json.Marshal(v.Config.Cmd)
		if !bytes.Equal(a, b) {
			return fmt.Errorf("fixed watchdog/command mismatch")
		}
	}
	if v.Config.User != "10001:10001" || h.Privileged || !h.ReadonlyRootfs || h.NetworkMode != "none" || h.PidMode != "" || h.IpcMode != "private" || h.CgroupnsMode != "private" || h.Memory != limits.MemoryBytes || h.MemorySwap != limits.MemoryBytes || h.NanoCpus != 1e9 || h.PidsLimit != 16 || len(h.Binds) != 0 || len(v.Mounts) != 1 {
		return fmt.Errorf("runtime did not retain required isolation/resource settings")
	}
	allowedEnv := map[string]bool{"PATH": true, "HOME": true, "LANG": true, "PYTHONDONTWRITEBYTECODE": true, "PYTHONHASHSEED": true, "PYTHON_VERSION": true, "PYTHON_SHA256": true, "GPG_KEY": true, "GPG_KEYS": true, "GCC_VERSION": true, "GCC_MIRRORS": true}
	for _, env := range v.Config.Env {
		key, _, _ := strings.Cut(env, "=")
		if !allowedEnv[key] {
			return fmt.Errorf("unapproved workload environment variable")
		}
	}
	caps := false
	nnp := false
	seccomp := false
	for _, s := range h.CapDrop {
		if strings.EqualFold(s, "ALL") {
			caps = true
		}
	}
	for _, s := range h.SecurityOpt {
		if s == "no-new-privileges=true" || s == "no-new-privileges" {
			nnp = true
		}
		if strings.HasPrefix(s, "seccomp=") && !strings.Contains(s, "unconfined") {
			content, e := os.ReadFile(d.seccomp)
			if e != nil {
				return e
			}
			var actual, want bytes.Buffer
			if json.Compact(&actual, []byte(strings.TrimPrefix(s, "seccomp="))) != nil || json.Compact(&want, content) != nil || !bytes.Equal(actual.Bytes(), want.Bytes()) {
				return fmt.Errorf("retained seccomp policy differs")
			}
			seccomp = true
		}
	}
	if !caps || len(h.CapDrop) != 1 || len(h.SecurityOpt) != 2 || !nnp || !seccomp {
		return fmt.Errorf("runtime security options missing")
	}
	for _, m := range v.Mounts {
		if m.Type != "volume" || m.Destination != "/work" || m.RW == readonly {
			return fmt.Errorf("unexpected workload mount")
		}
	}
	return nil
}

func (d *Docker) runPhase(parent context.Context, name, image, phase string, limits Limits, input []byte) (*PhaseResult, error) {
	result := &PhaseResult{Phase: phase, Container: name, Image: image, Limits: limits, ExitCode: -1, MeasurementNote: "CPU time and cgroup peak memory unavailable: not sampled reliably; controller wall time includes Docker attach/start overhead"}
	output := newCapture(limits.OutputBytes)
	attach, cancelAttach := context.WithCancel(context.Background())
	defer cancelAttach()
	cmd := exec.CommandContext(attach, "docker", "start", "--attach", "--interactive", name)
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = output.writer(false)
	cmd.Stderr = output.writer(true)
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return result, &InfrastructureError{phase, "start", err}
	}
	observation := startObservation(parent, name, limits.WallTime)
	defer observation.Close()
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	timer := time.NewTimer(limits.WallTime)
	defer timer.Stop()
	var commandErr error
	timedOut, tooMuch, canceled := false, false, false
	select {
	case commandErr = <-finished:
	case <-timer.C:
		timedOut = true
	case <-output.exceeded:
		tooMuch = true
	case <-parent.Done():
		canceled = true
	}
	if timedOut || tooMuch || canceled {
		// Kill the sandbox namespace, not just the Docker client or a guessed workload PID.
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, killErr := control(cleanup, "kill", "--signal", "KILL", name)
		cancel()
		cancelAttach()
		commandErr = <-finished
		if killErr != nil {
			// A concurrent natural exit is harmless; otherwise fail closed and let deferred forced removal run.
			state, e := containerState(context.Background(), name)
			if e != nil || state.Running {
				return result, &InfrastructureError{phase, "terminate", killErr}
			}
		}
	}
	result.WallTime = time.Since(start)
	result.Stdout, result.Stderr = output.text()
	select {
	case <-output.exceeded:
		tooMuch = true
	default:
	}
	state, err := containerState(context.Background(), name)
	if err != nil {
		return result, &InfrastructureError{phase, "inspect", err}
	}
	if state.Running || state.Error != "" || state.Status == "created" {
		return result, &InfrastructureError{phase, "runtime", fmt.Errorf("container did not complete normally: %s %s", state.Status, state.Error)}
	}
	result.ExitCode = state.ExitCode
	result.OOMKilled = state.OOMKilled
	result.Outcome = classify(state.ExitCode, state.OOMKilled, timedOut, tooMuch, canceled)
	if commandErr != nil && state.ExitCode == 0 && !timedOut && !tooMuch && !canceled {
		return result, &InfrastructureError{phase, "attach", commandErr}
	}
	if !canceled {
		if err := observation.Wait(); err != nil {
			return result, &InfrastructureError{phase, "progress", err}
		}
	}
	return result, nil
}

type state struct {
	StartedAt string
	Running   bool
	Status    string
	ExitCode  int
	OOMKilled bool
	Error     string
}

func containerState(ctx context.Context, name string) (state, error) {
	out, err := control(ctx, "inspect", "--format", "{{json .State}}", name)
	var s state
	if err == nil {
		err = json.Unmarshal(out, &s)
	}
	return s, err
}

func LoadImages(path string) (Images, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Images{}, err
	}
	var images Images
	if err = json.Unmarshal(data, &images); err != nil {
		return images, err
	}
	return images, images.Validate()
}
func missingObject(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such container") || strings.Contains(message, "no such object") || strings.Contains(message, "no such volume")
}
