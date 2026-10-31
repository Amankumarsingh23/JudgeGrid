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
	return runTrusted(ctx, program, args, input, max, 512<<20)
}

// RunTrustedModel is reserved for the compiled-in local embedding helper. It has
// a separate 2 GiB envelope; Git and syntax helpers retain their original bound.
func RunTrustedModel(ctx context.Context, program string, args []string, input []byte, max int) ([]byte, error) {
	return runTrusted(ctx, program, args, input, max, 2<<30)
}
func runTrusted(ctx context.Context, program string, args []string, input []byte, max int, memory uintptr) ([]byte, error) {
	if !filepath.IsAbs(program) || max < 1 || max > 8<<20 || len(input) > 2<<20 {
		return nil, ErrBudget
	}
	c := boundedProgram(ctx, program, args, memory)
	c.Dir = os.TempDir()
	c.WaitDelay = time.Second
	for _, k := range []string{"PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "TMPDIR"} {
		if v := os.Getenv(k); v != "" {
			c.Env = append(c.Env, k+"="+v)
		}
	}
	c.Env = append(c.Env, "OMP_NUM_THREADS=1", "OPENBLAS_NUM_THREADS=1", "MKL_NUM_THREADS=1", "TOKENIZERS_PARALLELISM=false", "HF_HUB_OFFLINE=1")
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
