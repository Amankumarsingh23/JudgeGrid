package ingest

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

var originPath = regexp.MustCompile(`^/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:\.git)?$`)

func approvedURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Port() != "" || !strings.Contains(u.Hostname(), ".") || strings.ContainsAny(u.Hostname(), "%:") || !originPath.MatchString(u.Path) || strings.Contains(u.Path, "..") || strings.ToLower(u.Hostname()) != u.Hostname() {
		return nil, ErrDenied
	}
	if _, e = netip.ParseAddr(u.Hostname()); e == nil {
		return nil, ErrDenied
	}
	return u, nil
}
func (t Trust) Remote(raw string) bool {
	if _, e := approvedURL(raw); e != nil {
		return false
	}
	for _, v := range t.Origins {
		if v == raw {
			return true
		}
	}
	return false
}

// InitializeMirror only creates an empty bare mirror. Existing nonempty directories
// are never rewritten; OpenGit subsequently validates their object layout.
func InitializeMirror(dir, format string, t Trust) error {
	p, e := t.Local(dir)
	if e != nil {
		return e
	}
	if format != "sha1" && format != "sha256" {
		return ErrSource
	}
	entries, e := os.ReadDir(p)
	if e != nil {
		return ErrSource
	}
	if len(entries) > 0 {
		return nil
	}
	for _, n := range []string{"objects", "refs"} {
		if e = os.Mkdir(filepath.Join(p, n), 0700); e != nil {
			return ErrSource
		}
	}
	config := "[core]\nrepositoryformatversion = 0\nbare = true\n"
	if format == "sha256" {
		config = "[core]\nrepositoryformatversion = 1\nbare = true\n[extensions]\nobjectformat = sha256\n"
	}
	if e = os.WriteFile(filepath.Join(p, "config"), []byte(config), 0600); e != nil {
		return ErrSource
	}
	if e = os.WriteFile(filepath.Join(p, "HEAD"), []byte("ref: refs/heads/unused\n"), 0600); e != nil {
		return ErrSource
	}
	return nil
}
func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48", "fec0::/10"} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return false
		}
	}
	return true
}
func remoteClient(ctx context.Context, u *url.URL) (*http.Client, error) {
	ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if e != nil || len(ips) == 0 {
		return nil, ErrSource
	}
	for _, ip := range ips {
		if !publicAddress(ip) {
			return nil, ErrDenied
		}
	}
	// Pin the verified destination for the lifetime of this fetch; TLS still verifies
	// the original DNS name. No proxy, redirect, credentials or secondary destination.
	ip := ips[0].String()
	for _, candidate := range ips {
		if candidate.Is4() {
			ip = candidate.String()
			break
		}
	}
	tr := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 << 10, DisableCompression: true, MaxConnsPerHost: 2, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip, "443"))
	}}
	return &http.Client{Transport: tr, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

type fetchBudget struct{ remaining atomic.Int64 }
type budgetReader struct {
	r io.Reader
	b *fetchBudget
}

func (r budgetReader) Read(p []byte) (int, error) {
	left := r.b.remaining.Load()
	if left <= 0 {
		return 0, ErrBudget
	}
	if int64(len(p)) > left {
		p = p[:left]
	}
	n, e := r.r.Read(p)
	r.b.remaining.Add(-int64(n))
	return n, e
}
func broker(ctx context.Context, origin *url.URL, client *http.Client, budget *fetchBudget) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		valid := (q.Method == "GET" && q.URL.Path == "/info/refs" && q.URL.RawQuery == "service=git-upload-pack") || (q.Method == "POST" && q.URL.Path == "/git-upload-pack" && q.URL.RawQuery == "")
		if !valid {
			http.Error(w, "unsupported Git transport", 400)
			return
		}
		target := *origin
		target.Path = strings.TrimSuffix(target.Path, "/") + q.URL.Path
		target.RawQuery = q.URL.RawQuery
		var body io.Reader
		if q.Method == "POST" {
			b, e := io.ReadAll(http.MaxBytesReader(w, q.Body, 1<<20))
			if e != nil {
				http.Error(w, "request budget exceeded", 413)
				return
			}
			body = bytes.NewReader(b)
		}
		req, e := http.NewRequestWithContext(ctx, q.Method, target.String(), body)
		if e != nil {
			http.Error(w, "fetch failed", 502)
			return
		}
		for _, h := range []string{"Content-Type", "Accept", "Git-Protocol"} {
			if v := q.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
		req.Header.Set("User-Agent", "JudgeGrid-ingest-v1")
		resp, e := client.Do(req)
		if e != nil {
			http.Error(w, "fetch unavailable", 502)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			http.Error(w, "fetch refused", 502)
			return
		}
		if resp.ContentLength > budget.remaining.Load() {
			http.Error(w, "fetch budget exceeded", 413)
			return
		}
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		_, _ = io.Copy(w, budgetReader{resp.Body, budget})
	})
}
func (g *Git) Fetch(ctx context.Context, t Trust, origin, ref string) (string, error) {
	if !t.Remote(origin) || (!validOID(ref, g.Format) && (!validRef(ref) || ref == "HEAD")) {
		return "", ErrDenied
	}
	u, _ := approvedURL(origin)
	client, e := remoteClient(ctx, u)
	if e != nil {
		return "", e
	}
	defer client.CloseIdleConnections()
	return g.fetchUsing(ctx, u, ref, client)
}
func (g *Git) seedHaves(ids []string) error {
	if len(ids) > 16 {
		return ErrBudget
	}
	shallow := []string{}
	if e := os.MkdirAll(filepath.Join(g.View, "refs", "judgegrid"), 0700); e != nil {
		return ErrSource
	}
	for i, id := range ids {
		if !validOID(id, g.Format) {
			return ErrIntegrity
		}
		typ, _, e := g.Info(id)
		if e != nil {
			continue
		}
		if typ != "commit" {
			return ErrIntegrity
		}
		name := filepath.Join(g.View, "refs", "judgegrid", fmt.Sprintf("have-%d", i))
		if e = os.WriteFile(name, []byte(id+"\n"), 0600); e != nil {
			return ErrSource
		}
		shallow = append(shallow, id)
	}
	if len(shallow) > 0 {
		if e := os.WriteFile(filepath.Join(g.View, "shallow"), []byte(strings.Join(shallow, "\n")+"\n"), 0600); e != nil {
			return ErrSource
		}
	}
	return nil
}

func (g *Git) fetchUsing(ctx context.Context, u *url.URL, ref string, client *http.Client) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return "", ErrSource
	}
	budget := &fetchBudget{}
	budget.remaining.Store(32 << 20)
	server := &http.Server{Handler: broker(ctx, u, client, budget), ReadHeaderTimeout: time.Second, WriteTimeout: 45 * time.Second, MaxHeaderBytes: 16384}
	go server.Serve(ln)
	defer server.Close()
	c := gitCommand(ctx, g.View, "-c", "protocol.http.allow=always", "-c", "protocol.version=2", "-c", "credential.helper=", "-c", "http.proxy=", "-c", "http.followRedirects=false", "-c", "fetch.unpackLimit=0", "fetch", "--force", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--depth=1", "http://"+ln.Addr().String(), ref+":refs/judgegrid/input")
	c.Env = append(c.Env, "GIT_OBJECT_DIRECTORY="+filepath.Join(g.Dir, "objects"))
	if g.diagnostic != nil {
		c.Stderr = g.diagnostic
	}
	if _, e = output(c, 4096); e != nil {
		return "", e
	}
	g.RemoteBytes = (32 << 20) - budget.remaining.Load()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	view, e := os.OpenRoot(g.View)
	if e != nil {
		return "", ErrSource
	}
	defer view.Close()
	b, e := readSafe(view, "refs/judgegrid/input", 256)
	if e != nil {
		return "", e
	}
	oid := strings.TrimSpace(string(b))
	if !validOID(oid, g.Format) {
		return "", ErrSource
	}
	// Post-fetch audit catches unsupported layout/size before ingestion. Objects are
	// pack-only and network bytes are bounded, not an unbounded recursive clone.
	var bytes int64
	count := 0
	e = boundedWalk(ctx, g.Root, "objects", 8, 50000, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return ErrSource
		}
		count++
		if count > 50000 {
			return ErrBudget
		}
		if d.Type()&os.ModeSymlink != 0 {
			return ErrSource
		}
		if !d.IsDir() {
			i, e := d.Info()
			if e != nil {
				return e
			}
			bytes += i.Size()
			if bytes > 256<<20 {
				return ErrBudget
			}
		}
		return nil
	})
	if e != nil {
		return "", e
	}
	return oid, nil
}
