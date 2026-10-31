package ingest

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"time"
)

// RunTrustedHelper shares the existing OS-enforced process boundary. The caller
// supplies an operator-approved executable and compiled-in helper, never repo code.
func RunTrustedHelper(ctx context.Context, program string, args []string, input []byte, max int) ([]byte, error) {
	if !filepath.IsAbs(program) || max < 1 || max > 8<<20 || len(input) > 2<<20 {
		return nil, ErrBudget
	}
	c := limitedProgram(ctx, program, args)
	c.Dir = os.TempDir()
	c.WaitDelay = time.Second
	for _, k := range []string{"PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "TMPDIR"} {
		if v := os.Getenv(k); v != "" {
			c.Env = append(c.Env, k+"="+v)
		}
	}
	c.Stdin = bytes.NewReader(input)
	c.Stderr = io.Discard
	w := &limitedOutput{max: max}
	c.Stdout = w
	if e := c.Run(); e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrSource
	}
	return w.Bytes(), nil
}
