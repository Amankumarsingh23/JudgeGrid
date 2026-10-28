package execution

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/buildcache"
	"github.com/Amankumarsingh23/JudgeGrid/internal/objects"
	"io"
	"os/exec"
	"time"
)

func (d *Docker) SetCache(c *buildcache.Cache) { d.cache = c }
func (d *Docker) cacheKey(r Request) buildcache.Key {
	return buildcache.Key{Schema: 1, SourceSHA: objects.Digest(r.Source), Compiler: "/usr/local/bin/g++", Toolchain: d.images.CPP, Flags: "-std=c++17 -O2 -pipe SOURCE_DATE_EPOCH=0 source-mtime=0", Target: "linux/amd64 ELF64 SYSV dynamic", Dependencies: d.images.CPP, Policy: PolicyVersion + "/artifact-v2", Seccomp: SeccompSHA256, Memory: r.CompileLimits.MemoryBytes, Wall: int64(r.CompileLimits.WallTime), Output: r.CompileLimits.OutputBytes}
}
func (p *preparedDocker) cachedCompile(ctx context.Context) (bool, error) {
	if p.docker.cache == nil {
		return false, nil
	}
	platform, e := control(ctx, "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", p.image)
	if e != nil || string(bytes.TrimSpace(platform)) != "linux/amd64" {
		return false, fmt.Errorf("unsupported compilation cache platform")
	}
	cl, e := p.docker.cache.Acquire(ctx, p.docker.cacheKey(p.request), p.result.ExecutionID)
	if e != nil {
		return false, e
	}
	p.claim = &cl
	if !cl.Hit {
		return false, nil
	}
	// Restore the same readable source/artifact layout as a cold compilation. Only
	// the trusted byte count enters the shell; source and ELF bytes arrive on stdin.
	input := bytes.Join([][]byte{p.request.Source, cl.Artifact}, nil)
	command := fmt.Sprintf("umask 077; set -e; dd bs=1 count=%d status=none of=/work/main.cpp; touch -d @0 /work/main.cpp; cat > /work/program; chmod 0400 /work/main.cpp; chmod 0500 /work/program", len(p.request.Source))
	p.result.Compile, e = p.phase(ctx, "compile", p.request.CompileLimits, input, false, []string{"/bin/sh", "-c", command})
	if e == nil && (p.result.Compile == nil || p.result.Compile.Outcome != Exited) {
		e = fmt.Errorf("verified artifact preparation failed")
	}
	p.result.CacheHit = true
	return true, e
}

type archiveBuffer struct{ bytes.Buffer }

func (b *archiveBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > objects.MaxBytes+65536 {
		return 0, fmt.Errorf("artifact transfer exceeds limit")
	}
	return b.Buffer.Write(p)
}
func (p *preparedDocker) publishCompile(ctx context.Context) error {
	if p.claim == nil || p.claim.Hit {
		return nil
	}
	var out archiveBuffer
	cmd := exec.CommandContext(ctx, "docker", "cp", p.result.ExecutionID+"-compile:/work/program", "-")
	cmd.Stdout = &out
	if e := cmd.Run(); e != nil {
		return e
	}
	tr := tar.NewReader(bytes.NewReader(out.Bytes()))
	h, e := tr.Next()
	if e != nil || h.Typeflag != tar.TypeReg || h.Name != "program" || h.Size < 1 || h.Size > objects.MaxBytes {
		return objects.ErrIntegrity
	}
	data, e := io.ReadAll(io.LimitReader(tr, objects.MaxBytes+1))
	if e != nil || int64(len(data)) != h.Size {
		return objects.ErrIntegrity
	}
	if _, e = tr.Next(); e != io.EOF {
		return objects.ErrIntegrity
	}
	return p.docker.cache.Publish(ctx, *p.claim, data)
}
func (p *preparedDocker) releaseBuild() {
	if p.claim != nil && !p.claim.Hit {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = p.docker.cache.Release(ctx, *p.claim)
	}
}
