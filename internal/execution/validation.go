package execution

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

const ValidationGoRef = "golang@sha256:672c944cfd07599b5f1bb179869270cdc94c6254e41db4d431259693cb286b82"
const normalTmpfs = "rw,noexec,nosuid,nodev,size=67108864,mode=1777"
const workspaceTmpfs = "rw,exec,nosuid,nodev,size=402653184,mode=1777"

var offlineGo = []string{"CGO_ENABLED=0", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off", "GOVCS=*:off", "GOCACHE=/tmp/go-cache", "GOPATH=/tmp/go", "GOMAXPROCS=2", "GOFLAGS=-p=1 -mod=readonly", "GOTELEMETRY=off"}
var validationName = regexp.MustCompile(`^jg-v19-[a-f0-9]{32}$`)

func ValidationContract() string {
	b, _ := json.Marshal([]any{ValidationGoRef, SeccompSHA256, workspaceTmpfs, offlineGo, stageValidation,
		"limits:65s/768MiB/64pids/64KiB; go test -json -count=1 .; gofmt -l verified .go file paths"})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

type ValidationFile struct {
	Path   string
	SHA256 string
	Bytes  []byte
}

type ValidationRuntime struct {
	Image       string      `json:"image"`
	StageImage  string      `json:"stage_image"`
	Environment Environment `json:"environment"`
}

type ValidationPhases struct {
	Bytes     int           `json:"bytes"`
	Stage     *PhaseResult  `json:"stage,omitempty"`
	Run       *PhaseResult  `json:"run,omitempty"`
	SetupTime time.Duration `json:"setup_ns"`
	Cleaned   bool          `json:"cleaned"`
}

func (d *Docker) ValidationRuntime(ctx context.Context) (ValidationRuntime, error) {
	env, e := d.Environment(ctx)
	if e != nil {
		return ValidationRuntime{}, e
	}
	body, e := control(ctx, "image", "inspect", "--format", "{{.Id}}", ValidationGoRef)
	if e != nil {
		return ValidationRuntime{}, e
	}
	id := strings.TrimSpace(string(body))
	if !imageID.MatchString(id) {
		return ValidationRuntime{}, ErrInvalid
	}
	return ValidationRuntime{Image: id, StageImage: d.images.Python, Environment: env}, nil
}

func ValidationProfile(profile string) bool {
	return profile == "go-unit" || profile == "go-format-check"
}

type stageFile struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
	Body string `json:"body"`
}

const stageValidation = `
import base64, hashlib, json, os, re, sys
items=json.loads(sys.stdin.buffer.read(25<<20))
assert 0 < len(items) <= 512
assert os.listdir('/work') == []
os.umask(0o077)
total=0
for item in items:
    n=item['name']
    assert len(n)<=512 and all(re.fullmatch(r'[A-Za-z0-9._@+ -]{1,120}',p) and p not in ('.','..') for p in n.split('/'))
    body=base64.b64decode(item['body'],validate=True)
    total+=len(body)
    assert total<=16777216 and len(body)<=4194304
    name='/work/'+n
    os.makedirs(os.path.dirname(name),exist_ok=True)
    fd=os.open(name,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o400)
    with os.fdopen(fd,'wb') as f: f.write(body)
    with open(name,'rb') as f: assert hashlib.sha256(f.read()).hexdigest()==item['hash']
`

func validationCommand(profile string, files []ValidationFile) ([]string, error) {
	switch profile {
	case "go-unit":
		// A single root package, offline and independent of the host module state.
		return []string{"/usr/local/go/bin/go", "test", "-json", "-count=1", "."}, nil
	case "go-format-check":
		// Fixed argv with paths from the verified inventory; no shell or options.
		args := []string{"/usr/local/go/bin/gofmt", "-l"}
		var argBytes int
		for _, f := range files {
			if path.Ext(f.Path) == ".go" {
				args = append(args, "/work/"+f.Path)
				argBytes += len(f.Path) + 7
			}
		}
		if len(args) == 2 || argBytes > 24<<10 {
			return nil, ErrInvalid
		}
		return args, nil
	default:
		return nil, ErrInvalid
	}
}

// ExecuteValidation only accepts the compiled profile registry and verified
// regular files. Names are opaque attempt identities supplied by the controller.
func (d *Docker) ExecuteValidation(ctx context.Context, name, profile string, runtime ValidationRuntime, files []ValidationFile, guard func(context.Context, func() error) error) (out ValidationPhases, err error) {
	if !validationName.MatchString(name) || !imageID.MatchString(runtime.Image) || runtime.StageImage != d.images.Python || len(files) < 1 || len(files) > 512 || guard == nil {
		return out, ErrInvalid
	}
	actual, e := d.ValidationRuntime(ctx)
	if e != nil || actual.Image != runtime.Image {
		return out, errors.Join(ErrInvalid, e)
	}
	command, e := validationCommand(profile, files)
	if e != nil {
		return out, e
	}
	payload := make([]stageFile, 0, len(files))
	for _, f := range files {
		out.Bytes += len(f.Bytes)
		payload = append(payload, stageFile{f.Path, f.SHA256, base64.StdEncoding.EncodeToString(f.Bytes)})
	}
	if out.Bytes > 16<<20 {
		return out, ErrInvalid
	}
	input, e := json.Marshal(payload)
	if e != nil {
		return out, e
	}
	select {
	case d.slots <- struct{}{}:
	case <-ctx.Done():
		return out, ctx.Err()
	}
	defer func() { <-d.slots }()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		cleanupErr := d.CleanupValidation(cleanup, name)
		out.Cleaned = cleanupErr == nil
		err = errors.Join(err, cleanupErr)
	}()
	volume := name + "-work"
	if e = guard(ctx, func() error {
		_, e := control(ctx, "volume", "create", "--label", "judgegrid.validation="+name, volume)
		return e
	}); e != nil {
		return out, e
	}
	t := time.Now()
	stageLimits := Limits{WallTime: 20 * time.Second, MemoryBytes: 256 << 20, OutputBytes: 16 << 10}
	stage := []string{"python3", "-I", "-c", stageValidation}
	out.Stage, e = d.validationPhase(ctx, name, "stage", runtime.StageImage, volume, stageLimits, false, false, stage, input, guard)
	if e != nil || out.Stage == nil || out.Stage.Outcome != Exited {
		return out, errors.Join(ErrInvalid, e)
	}
	limits := Limits{WallTime: 65 * time.Second, MemoryBytes: 768 << 20, OutputBytes: 64 << 10}
	out.SetupTime = time.Since(t)
	out.Run, e = d.validationPhase(ctx, name, "run", runtime.Image, volume, limits, true, true, command, nil, guard)
	return out, e
}

func (d *Docker) validationPhase(ctx context.Context, name, phase, image, volume string, limits Limits, readonly, goMode bool, cmd []string, input []byte, guard func(context.Context, func() error) error) (*PhaseResult, error) {
	container := name + "-" + phase
	e := guard(ctx, func() error {
		args := d.createArgs(container, image, volume, limits, readonly, cmd)
		if goMode {
			for i := range args {
				switch args[i] {
				case "16":
					if i > 0 && args[i-1] == "--pids-limit" {
						args[i] = "64"
					}
				case "/tmp:" + normalTmpfs:
					args[i] = "/tmp:" + workspaceTmpfs
				}
			}
		}
		i := 0
		for args[i] != image {
			i++
		}
		opts := []string{"--label", "judgegrid.validation=" + name}
		if goMode {
			for _, env := range offlineGo {
				opts = append(opts, "--env", env)
			}
		}
		args = append(append(append([]string{}, args[:i]...), opts...), args[i:]...)
		if _, e := control(ctx, args...); e != nil {
			return e
		}
		b, e := control(ctx, "inspect", "--format", "{{json .}}", container)
		if e != nil {
			return e
		}
		return d.validateInspectionMode(b, limits, readonly, goMode, policyExpectation{image, volume, cmd})
	})
	if e != nil {
		return nil, e
	}
	// A second fence ensures a paused worker cannot start a reconciled container.
	if e = guard(ctx, func() error { return nil }); e != nil {
		return nil, e
	}
	return d.runPhase(ctx, container, image, phase, limits, input)
}

func (d *Docker) CleanupValidation(ctx context.Context, name string) error {
	if !validationName.MatchString(name) {
		return ErrInvalid
	}
	for _, item := range []struct{ kind, name string }{{"container", name + "-run"}, {"container", name + "-stage"}, {"volume", name + "-work"}} {
		r, e := InspectResource(ctx, item.kind, item.name)
		if e != nil {
			if missingObject(e) {
				continue
			}
			return e
		}
		if r.Name != item.name || r.Labels["judgegrid.validation"] != name {
			return fmt.Errorf("refused unrelated validation resource")
		}
		if item.kind == "container" {
			_, e = control(ctx, "rm", "--force", r.ID)
		} else {
			_, e = control(ctx, "volume", "rm", r.Name)
		}
		if e != nil && !missingObject(e) {
			return e
		}
	}
	return nil
}
