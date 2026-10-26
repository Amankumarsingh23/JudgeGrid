package execution

import (
	"bytes"
	"io"
	"sync"
)

// capture bounds combined stdout+stderr incrementally and continues discarding until termination.
type capture struct {
	mu             sync.Mutex
	remaining      int64
	stdout, stderr bytes.Buffer
	exceeded       chan struct{}
	once           sync.Once
}

func newCapture(limit int64) *capture {
	return &capture{remaining: limit, exceeded: make(chan struct{})}
}

type streamWriter struct {
	target *capture
	stderr bool
}

func (c *capture) writer(stderr bool) io.Writer { return streamWriter{c, stderr} }
func (w streamWriter) Write(p []byte) (int, error) {
	c := w.target
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	keep := int64(n)
	if keep > c.remaining {
		keep = c.remaining
		c.once.Do(func() { close(c.exceeded) })
	}
	if w.stderr {
		c.stderr.Write(p[:int(keep)])
	} else {
		c.stdout.Write(p[:int(keep)])
	}
	c.remaining -= keep
	return n, nil
}
func (c *capture) text() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdout.String(), c.stderr.String()
}
