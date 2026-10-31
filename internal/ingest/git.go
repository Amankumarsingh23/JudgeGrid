package ingest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Trust is operator configuration, never derived from repository content or HTTP input.
type Trust struct {
	Roots   []string `json:"local_roots"`
	Origins []string `json:"remote_origins"`
}

func (t Trust) Local(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", ErrDenied
	}
	abs, e := filepath.EvalSymlinks(p)
	if e != nil {
		return "", ErrDenied
	}
	abs = filepath.Clean(abs)
	for _, root := range t.Roots {
		if !filepath.IsAbs(root) {
			continue
		}
		r, e := filepath.EvalSymlinks(root)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(r, abs)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel) {
			return abs, nil
		}
	}
	return "", ErrDenied
}
func gitEnv() []string {
	env := []string{}
	for _, k := range []string{"PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "TMPDIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_COUNT=0", "LC_ALL=C")
}
func gitCommand(ctx context.Context, dir string, args ...string) *gitProcess {
	base := []string{"--no-replace-objects", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "core.packedGitWindowSize=1m", "-c", "core.packedGitLimit=16m", "-c", "core.deltaBaseCacheLimit=8m", "-c", "protocol.allow=never"}
	if dir != "" {
		base = append(base, "--git-dir="+dir)
	}
	c := limitedGit(ctx, append(base, args...))
	c.WaitDelay = time.Second
	c.Env = gitEnv()
	c.Stderr = io.Discard
	return c
}

type limitedOutput struct {
	bytes.Buffer
	max int
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > w.max {
		return 0, ErrBudget
	}
	return w.Buffer.Write(p)
}
func output(c *gitProcess, max int) ([]byte, error) {
	w := &limitedOutput{max: max}
	c.Stdout = w
	if e := c.Run(); e != nil {
		return nil, ErrSource
	}
	return w.Bytes(), nil
}
func readSafe(root *os.Root, name string, max int64) ([]byte, error) {
	cur := ""
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == "" || part == ".." {
			return nil, ErrSource
		}
		cur = filepath.Join(cur, part)
		i, e := root.Lstat(cur)
		if e != nil || i.Mode()&os.ModeSymlink != 0 {
			return nil, ErrSource
		}
	}
	f, e := root.Open(name)
	if e != nil {
		return nil, ErrSource
	}
	defer f.Close()
	i, e := f.Stat()
	if e != nil || !i.Mode().IsRegular() || i.Size() > max {
		return nil, ErrBudget
	}
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, ErrSource
	}
	return b, nil
}
func removeView(dir string) {
	abs, e := filepath.Abs(dir)
	tmp, x := filepath.Abs(os.TempDir())
	if e == nil && x == nil && filepath.Dir(abs) == tmp && strings.HasPrefix(filepath.Base(abs), "judgegrid-ingest-view-") {
		_ = os.RemoveAll(abs)
	}
}

type Git struct {
	Root              *os.Root
	Dir, View, Format string
	cmd               *gitProcess
	in                io.WriteCloser
	out               *bufio.Reader
	cancel            context.CancelFunc
	context           context.Context
	diagnostic        io.Writer // test-only; never configured by the CLI
	RemoteBytes       int64
}

func OpenGit(ctx context.Context, t Trust, location string) (g *Git, err error) {
	p, e := t.Local(location)
	if e != nil {
		return nil, e
	}
	d := filepath.Join(p, ".git")
	if i, e := os.Lstat(d); e == nil {
		if !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return nil, ErrSource
		}
	} else {
		d = p
	}
	root, e := os.OpenRoot(d)
	if e != nil {
		return nil, ErrSource
	}
	g = &Git{Root: root, Dir: d, Format: "sha1"}
	owned := g
	defer func() {
		if err != nil {
			owned.Close()
		}
	}()
	if _, e = readSafe(root, "config", 64<<10); e != nil {
		return nil, e
	}
	// Read only one harmless config value, with includes/global/system disabled.
	for key, target := range map[string]*string{"extensions.objectformat": &g.Format, "extensions.refstorage": nil} {
		cmd := gitCommand(ctx, "", "config", "--no-includes", "--file", filepath.Join(d, "config"), "--get", key)
		b, x := output(cmd, 256)
		if x != nil {
			if ee, ok := cmd.ProcessState, cmd.ProcessState != nil; !ok || ee.ExitCode() != 1 {
				return nil, ErrSource
			}
			continue
		}
		v := strings.TrimSpace(string(b))
		if target != nil {
			*target = v
		} else if v != "files" {
			return nil, ErrSource
		}
	}
	if g.Format != "sha1" && g.Format != "sha256" {
		return nil, ErrSource
	}
	count := 0
	var size int64
	e = boundedWalk(ctx, root, "objects", 8, 50000, func(name string, de fs.DirEntry, e error) error {
		if e != nil {
			return ErrSource
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		count++
		if count > 50000 {
			return ErrBudget
		}
		if de.Type()&os.ModeSymlink != 0 {
			return ErrSource
		}
		if strings.HasSuffix(name, "/alternates") || strings.HasSuffix(name, "/http-alternates") || strings.HasSuffix(name, ".promisor") {
			return ErrSource
		}
		if !de.IsDir() {
			i, e := de.Info()
			if e != nil || !i.Mode().IsRegular() {
				return ErrSource
			}
			size += i.Size()
			if i.Size() > 128<<20 || size > 256<<20 {
				return ErrBudget
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	g.View, e = os.MkdirTemp("", "judgegrid-ingest-view-")
	if e != nil {
		return nil, ErrSource
	}
	for _, n := range []string{"objects", "refs"} {
		if e = os.Mkdir(filepath.Join(g.View, n), 0700); e != nil {
			return nil, ErrSource
		}
	}
	config := "[core]\nrepositoryformatversion = 0\nbare = true\n"
	if g.Format == "sha256" {
		config = "[core]\nrepositoryformatversion = 1\nbare = true\n[extensions]\nobjectFormat = sha256\n"
	}
	if e = os.WriteFile(filepath.Join(g.View, "config"), []byte(config), 0600); e != nil {
		return nil, ErrSource
	}
	if e = os.WriteFile(filepath.Join(g.View, "HEAD"), []byte("ref: refs/heads/unused\n"), 0600); e != nil {
		return nil, ErrSource
	}
	run, cancel := context.WithCancel(ctx)
	g.cancel = cancel
	g.context = run
	g.cmd = gitCommand(run, g.View, "cat-file", "--batch-command")
	g.cmd.Env = append(g.cmd.Env, "GIT_OBJECT_DIRECTORY="+filepath.Join(d, "objects"))
	g.in, e = g.cmd.StdinPipe()
	if e != nil {
		return nil, ErrSource
	}
	pipe, e := g.cmd.StdoutPipe()
	if e != nil {
		return nil, ErrSource
	}
	g.out = bufio.NewReaderSize(pipe, 64<<10)
	if e = g.cmd.Start(); e != nil {
		return nil, ErrSource
	}
	return g, nil
}
func (g *Git) Close() {
	if g == nil {
		return
	}
	if g.in != nil {
		_ = g.in.Close()
	}
	if g.cancel != nil {
		g.cancel()
	}
	if g.cmd != nil && g.cmd.Process != nil {
		_ = g.cmd.Wait()
	}
	if g.Root != nil {
		_ = g.Root.Close()
	}
	if g.View != "" {
		removeView(g.View)
	}
}
func validOID(v, format string) bool {
	n := 40
	if format == "sha256" {
		n = 64
	}
	if len(v) != n || v != strings.ToLower(v) {
		return false
	}
	_, e := hex.DecodeString(v)
	return e == nil && strings.Trim(v, "0") != ""
}

var refPattern = regexp.MustCompile(`^(HEAD|refs/(heads|tags|judgegrid)/[A-Za-z0-9_./-]+)$`)

func validRef(v string) bool {
	return refPattern.MatchString(v) && !strings.Contains(v, "..") && !strings.Contains(v, "//") && !strings.HasSuffix(v, "/") && !strings.HasSuffix(v, ".") && !strings.HasSuffix(v, ".lock")
}
func (g *Git) ref(ref string, depth int) (string, error) {
	if validOID(ref, g.Format) {
		return ref, nil
	}
	if depth > 5 || !validRef(ref) {
		return "", ErrSource
	}
	b, e := readSafe(g.Root, ref, 4096)
	if e == nil {
		v := strings.TrimSpace(string(b))
		if strings.HasPrefix(v, "ref: ") {
			return g.ref(strings.TrimPrefix(v, "ref: "), depth+1)
		}
		if validOID(v, g.Format) {
			return v, nil
		}
		return "", ErrSource
	}
	if _, statErr := g.Root.Lstat(ref); !errors.Is(statErr, fs.ErrNotExist) {
		return "", ErrSource
	}
	b, e = readSafe(g.Root, "packed-refs", 4<<20)
	if e != nil {
		return "", ErrSource
	}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Split(line, " ")
		if len(parts) == 2 && parts[1] == ref && validOID(parts[0], g.Format) {
			return parts[0], nil
		}
	}
	return "", ErrSource
}
func (g *Git) command(action, oid string) (string, int64, error) {
	if g.context.Err() != nil {
		return "", 0, g.context.Err()
	}
	if !validOID(oid, g.Format) {
		return "", 0, ErrSource
	}
	if _, e := io.WriteString(g.in, action+" "+oid+"\n"); e != nil {
		return "", 0, ErrSource
	}
	line, e := g.out.ReadSlice('\n')
	if e != nil || len(line) > 160 {
		return "", 0, ErrSource
	}
	p := strings.Fields(string(line))
	if len(p) != 3 || p[0] != oid {
		return "", 0, ErrSource
	}
	n, e := strconv.ParseInt(p[2], 10, 64)
	if e != nil || n < 0 {
		return "", 0, ErrSource
	}
	return p[1], n, nil
}
func (g *Git) Info(oid string) (string, int64, error) { return g.command("info", oid) }
func (g *Git) Read(oid, typ string, max int64) ([]byte, error) {
	// Inspect size before asking Git for bytes; no filters, textconv or symlink following.
	t, n, e := g.Info(oid)
	if e != nil {
		return nil, e
	}
	if t != typ {
		return nil, ErrSource
	}
	if n > max {
		return nil, ErrBudget
	}
	t, m, e := g.command("contents", oid)
	if e != nil || t != typ || m != n {
		return nil, ErrSource
	}
	b := make([]byte, n+1)
	if _, e = io.ReadFull(g.out, b); e != nil || b[n] != '\n' {
		return nil, ErrSource
	}
	b = b[:n]
	if objectOID(g.Format, typ, b) != oid {
		return nil, ErrIntegrity
	}
	return b, nil
}
func objectOID(format, typ string, b []byte) string {
	var h hash.Hash = sha1.New()
	if format == "sha256" {
		h = sha256.New()
	}
	fmt.Fprintf(h, "%s %d\x00", typ, len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
func (g *Git) Resolve(ref string) (commit, tree string, err error) {
	oid, e := g.ref(ref, 0)
	if e != nil {
		return "", "", e
	}
	for range 6 {
		typ, _, e := g.Info(oid)
		if e != nil {
			return "", "", e
		}
		if typ == "commit" {
			b, e := g.Read(oid, "commit", 256<<10)
			if e != nil {
				return "", "", e
			}
			line := strings.SplitN(string(b), "\n", 2)[0]
			if !strings.HasPrefix(line, "tree ") || !validOID(line[5:], g.Format) {
				return "", "", ErrSource
			}
			return oid, line[5:], nil
		}
		if typ != "tag" {
			return "", "", ErrSource
		}
		b, e := g.Read(oid, "tag", 256<<10)
		if e != nil {
			return "", "", e
		}
		line := strings.SplitN(string(b), "\n", 2)[0]
		if !strings.HasPrefix(line, "object ") || !validOID(line[7:], g.Format) {
			return "", "", ErrSource
		}
		oid = line[7:]
	}
	return "", "", ErrSource
}
func (g *Git) Inventory(ctx context.Context, tree string, p Policy) ([]Entry, error) {
	entries := []Entry{}
	seen := map[string]bool{}
	pathBytes := 0
	var walk func(string, string, int) error
	walk = func(oid, prefix string, depth int) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if depth > p.MaxDepth {
			return ErrBudget
		}
		b, e := g.Read(oid, "tree", 2<<20)
		if e != nil {
			return e
		}
		oidBytes := 20
		if g.Format == "sha256" {
			oidBytes = 32
		}
		for len(b) > 0 {
			sp := bytes.IndexByte(b, ' ')
			nul := bytes.IndexByte(b, 0)
			if sp < 1 || nul <= sp+1 || len(b) < nul+1+oidBytes {
				return ErrSource
			}
			mode, name := string(b[:sp]), string(b[sp+1:nul])
			path := prefix + name
			child := hex.EncodeToString(b[nul+1 : nul+1+oidBytes])
			b = b[nul+1+oidBytes:]
			if strings.Contains(name, "/") || !validPath(path) || seen[foldPath(path)] {
				return ErrSource
			}
			seen[foldPath(path)] = true
			pathBytes += len(path)
			if len(entries) >= p.MaxEntries || pathBytes > p.MaxPathBytes {
				return ErrBudget
			}
			item := Entry{Path: path, Mode: mode, OID: child, Reason: "included"}
			switch mode {
			case "40000":
				item.Type = "tree"
				item.Reason = "directory"
			case "100644", "100755":
				item.Type = "blob"
			case "120000":
				item.Type = "blob"
				item.Reason = "symlink"
			case "160000":
				item.Type = "commit"
				item.Reason = "gitlink"
			default:
				return ErrSource
			}
			if item.Type == "blob" {
				typ, n, e := g.Info(child)
				if e != nil || typ != "blob" {
					return ErrSource
				}
				item.Size = n
			}
			entries = append(entries, item)
			if mode == "40000" {
				if e = walk(child, path+"/", depth+1); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e := walk(tree, "", 1); e != nil {
		return nil, e
	}
	return entries, nil
}
