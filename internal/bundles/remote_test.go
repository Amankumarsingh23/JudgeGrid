package bundles

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/objects"
	"testing"
)

type memoryObjects struct {
	data map[string][]byte
	down bool
}

func (m *memoryObjects) Get(_ context.Context, k string, n int64) ([]byte, error) {
	if m.down {
		return nil, errors.New("unavailable")
	}
	b, ok := m.data[k]
	if !ok || int64(len(b)) > n {
		return nil, objects.ErrIntegrity
	}
	return append([]byte(nil), b...), nil
}
func (m *memoryObjects) Put(_ context.Context, k string, b []byte) error {
	if m.down {
		return errors.New("unavailable")
	}
	if old, ok := m.data[k]; ok && string(old) != string(b) {
		return objects.ErrIntegrity
	}
	m.data[k] = append([]byte(nil), b...)
	return nil
}
func TestRemoteCanonicalIntegrityAndFailure(t *testing.T) {
	m := &memoryObjects{data: map[string][]byte{}}
	s := &Remote{m}
	b, _ := Demo("two-sum")
	ref, e := s.Put(b)
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Load(ref.Hash)
	if e != nil || got.Verify(ref) != nil {
		t.Fatal(e)
	}
	if _, e = s.Put(b); e != nil {
		t.Fatal("identical publish", e)
	}
	m.data["bundles/"+ref.Hash+".json"] = []byte("corrupt")
	if _, e = s.Load(ref.Hash); !errors.Is(e, ErrIntegrity) {
		t.Fatal("corruption accepted", e)
	}
	m.down = true
	if _, e = s.Load(ref.Hash); e == nil {
		t.Fatal("outage downgraded")
	}
}
func TestConfiguredBackendNeverDowngrades(t *testing.T) {
	t.Setenv("BUNDLE_BACKEND", "invalid")
	if _, e := Configured(t.TempDir()); e == nil {
		t.Fatal("unknown backend")
	}
	t.Setenv("BUNDLE_BACKEND", "s3")
	t.Setenv("S3_ENDPOINT", "")
	if _, e := Configured(t.TempDir()); e == nil {
		t.Fatal("silently opened local store")
	}
}
