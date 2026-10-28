//go:build integration

package tests

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/buildcache"
	"github.com/Amankumarsingh23/JudgeGrid/internal/objects"
	"os"
	"sync"
	"testing"
	"time"
)

type artifactMemory struct {
	mu            sync.Mutex
	data          map[string][]byte
	down, corrupt bool
}

func (m *artifactMemory) Get(ctx context.Context, key string, max int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if m.down {
		return nil, errors.New("injected outage")
	}
	if m.corrupt {
		return []byte("corrupt"), nil
	}
	b, ok := m.data[key]
	if !ok || int64(len(b)) > max {
		return nil, objects.ErrIntegrity
	}
	return append([]byte(nil), b...), nil
}
func (m *artifactMemory) Put(ctx context.Context, key string, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	if m.down {
		return errors.New("injected outage")
	}
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	if old, ok := m.data[key]; ok && string(old) != string(b) {
		return objects.ErrIntegrity
	}
	m.data[key] = append([]byte(nil), b...)
	return nil
}
func TestCacheClaimCancellationTakeoverAndEviction(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	bytes, e := os.ReadFile("/bin/true")
	if e != nil {
		t.Fatal("requires Linux fixture ELF", e)
	}
	s := &artifactMemory{}
	c := &buildcache.Cache{DB: db, Objects: s, Lease: time.Second}
	k := buildcache.Key{Schema: 1, SourceSHA: objects.Digest([]byte("fixture")), Compiler: "fixture", Toolchain: "pinned", Flags: "flags", Target: "linux/amd64", Dependencies: "pinned", Policy: "test", Seccomp: "test"}
	old, e := c.Acquire(ctx, k, "old")
	if e != nil {
		t.Fatal(e)
	}
	waiting, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, e = c.Acquire(waiting, k, "waiter"); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("canceled waiter modified authority", e)
	}
	time.Sleep(1100 * time.Millisecond)
	newer, e := c.Acquire(ctx, k, "new")
	if e != nil || newer.Generation != 2 {
		t.Fatal(newer, e)
	}
	if e = c.Publish(ctx, old, bytes); e == nil {
		t.Fatal("stale cache builder published")
	}
	if e = c.Publish(ctx, newer, bytes); e != nil {
		t.Fatal(e)
	}
	hit, e := c.Acquire(ctx, k, "reader")
	if e != nil || !hit.Hit {
		t.Fatal(e)
	}
	s.corrupt = true
	if _, e = c.Acquire(ctx, k, "reader"); e == nil {
		t.Fatal("corruption accepted")
	}
	s.corrupt = false
	s.down = true
	if _, e = c.Acquire(ctx, k, "reader"); e == nil {
		t.Fatal("outage accepted")
	}
	s.down = false
	if e = c.Evict(ctx, k.ID()); e != nil {
		t.Fatal(e)
	}
	if len(s.data) != 1 {
		t.Fatal("eviction deleted potentially referenced bytes")
	}
	third, e := c.Acquire(ctx, k, "third")
	if e != nil || third.Generation != 3 || third.Hit {
		t.Fatal(third, e)
	}
	if e = c.Release(ctx, third); e != nil {
		t.Fatal(e)
	}
}
