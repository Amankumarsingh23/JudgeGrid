package ingest

import (
	"context"
	"os/exec"
	"sync"
)

type gitProcess struct {
	*exec.Cmd
	release func()
	finish  func()
	context context.Context
	memory  uintptr
}

func (p *gitProcess) Start() error {
	if e := p.Cmd.Start(); e != nil {
		return e
	}
	release, e := limitStartedProgram(p.Cmd, p.memory)
	if e != nil {
		p.Cmd.Process.Kill()
		p.Cmd.Wait()
		return ErrBudget
	}
	p.release = sync.OnceFunc(release)
	done := make(chan struct{})
	p.finish = sync.OnceFunc(func() { close(done); p.release() })
	if p.context != nil {
		go func() {
			select {
			case <-p.context.Done():
				p.release()
			case <-done:
			}
		}()
	}
	return nil
}
func (p *gitProcess) Wait() error {
	e := p.Cmd.Wait()
	if p.finish != nil {
		p.finish()
	}
	return e
}
func (p *gitProcess) Run() error {
	if e := p.Start(); e != nil {
		return e
	}
	return p.Wait()
}
