package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type preparedDocker struct {
	mu                    sync.Mutex
	docker                *Docker
	request               Request
	result                Result
	image, volume         string
	containers            []string
	volumeCreated, closed bool
	closeErr              error
	runs                  int
}

func (p *preparedDocker) phase(ctx context.Context, name string, limits Limits, input []byte, readonly bool, command []string) (*PhaseResult, error) {
	container := p.result.ExecutionID + "-" + name
	p.containers = append(p.containers, container)
	if _, err := control(ctx, p.docker.createArgs(container, p.image, p.volume, limits, readonly, command)...); err != nil {
		return nil, &InfrastructureError{name, "create", err}
	}
	if err := p.docker.verify(ctx, container, limits, readonly); err != nil {
		return nil, &InfrastructureError{name, "policy", err}
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
	if _, removeErr := control(cleanup, "rm", "--force", container); removeErr != nil && !missingObject(removeErr) {
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
	defer func() { p.docker.active.Add(-1); <-p.docker.slots }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var failures []error
	for _, name := range p.containers {
		if _, err := control(ctx, "rm", "--force", name); err != nil && !missingObject(err) {
			failures = append(failures, err)
		}
	}
	if p.volumeCreated {
		if _, err := control(ctx, "volume", "rm", p.volume); err != nil && !missingObject(err) {
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
