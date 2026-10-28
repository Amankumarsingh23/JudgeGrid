package execution

import (
	"context"
	"errors"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/buildcache"
	"strings"
	"sync"
	"time"
)

type preparedDocker struct {
	claim                 *buildcache.Claim
	mu                    sync.Mutex
	docker                *Docker
	request               Request
	result                Result
	image, volume         string
	containers            []string
	volumeCreated, closed bool
	closeErr              error
	runs                  int
	provenance            Provenance
	registry              Registry
	registered            bool
}

func (p *preparedDocker) protect(ctx context.Context, action func() error) error {
	if p.registry != nil {
		return p.registry.ProtectSandbox(ctx, p.result.ExecutionID, action)
	}
	return action()
}

func (p *preparedDocker) phase(ctx context.Context, name string, limits Limits, input []byte, readonly bool, command []string) (*PhaseResult, error) {
	container := p.result.ExecutionID + "-" + name
	p.containers = append(p.containers, container)
	if err := p.protect(ctx, func() error {
		if _, e := control(ctx, p.docker.createArgs(container, p.image, p.volume, limits, readonly, command, p.provenance.Labels(name))...); e != nil {
			return e
		}
		if e := p.docker.verify(ctx, container, limits, readonly, policyExpectation{p.image, p.volume, command}); e != nil {
			return e
		}
		r, e := InspectResource(ctx, "container", container)
		if e != nil {
			return e
		}
		if !labelsEqual(r.Labels, p.provenance.Labels(name)) || r.Name != container {
			return ErrInvalid
		}
		return nil
	}); err != nil {
		return nil, &InfrastructureError{name, "create/policy", err}
	}
	return p.docker.runPhase(ctx, container, p.image, name, limits, input)
}
func (p *preparedDocker) Run(ctx context.Context, input []byte, limits Limits) (result *PhaseResult, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.runs >= 16 {
		return nil, fmt.Errorf("prepared program closed or run bound exceeded")
	}
	validation := p.request
	validation.Stdin = input
	validation.RunLimits = limits
	if err = validation.Validate(); err != nil {
		return nil, err
	}
	p.runs++
	name := "run"
	if p.runs > 1 {
		name = fmt.Sprintf("run-%d", p.runs)
	}
	command := []string{"python3", "-I", "-B", "/work/main.py"}
	if p.request.Language == "cpp17" {
		command = []string{"/work/program"}
	}
	p.docker.runtimeStarts.Add(1)
	result, err = p.phase(ctx, name, limits, input, true, command)
	// Fresh runtime containers are removed between cases; the only reused object is a read-only artifact.
	container := p.result.ExecutionID + "-" + name
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if removeErr := RemoveOwned(cleanup, "container", container, p.provenance, name); removeErr != nil && !missingObject(removeErr) {
		err = errors.Join(err, &InfrastructureError{"cleanup", "runtime", removeErr})
	} else {
		p.containers = p.containers[:len(p.containers)-1]
	}
	return result, err
}
func (p *preparedDocker) Summary() Result { p.mu.Lock(); defer p.mu.Unlock(); return p.result }
func (p *preparedDocker) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed = true
	defer p.releaseBuild()
	defer func() { p.docker.active.Add(-1); <-p.docker.slots }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var failures []error
	for _, name := range p.containers {
		_, role, parseErr := ParseLabels(p.provenance.Labels(strings.TrimPrefix(name, p.provenance.ExecutionID+"-")))
		if parseErr != nil {
			failures = append(failures, parseErr)
			continue
		}
		if err := RemoveOwned(ctx, "container", name, p.provenance, role); err != nil && !missingObject(err) {
			failures = append(failures, err)
		}
	}
	if p.volumeCreated {
		if err := RemoveOwned(ctx, "volume", p.volume, p.provenance, "artifact"); err != nil && !missingObject(err) {
			failures = append(failures, err)
		}
	}
	if len(failures) == 0 && p.registered {
		if err := p.registry.FinishSandbox(ctx, p.result.ExecutionID); err != nil {
			failures = append(failures, err)
		}
	}
	p.result.Cleaned = len(failures) == 0
	if len(failures) > 0 {
		p.closeErr = &InfrastructureError{"cleanup", "remove", errors.Join(failures...)}
	}
	return p.closeErr
}

type OperationCounts struct {
	Preparations  int64
	Compilations  int64
	RuntimeStarts int64
}

func (d *Docker) Counts() OperationCounts {
	return OperationCounts{d.preparations.Load(), d.compilations.Load(), d.runtimeStarts.Load()}
}
