package bundles

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestImmutableRoundTrip(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Demo("two-sum")
	ref, err := s.Put(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Put(b); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(ref.Hash)
	if err != nil || loaded.Verify(ref) != nil {
		t.Fatal(err)
	}
	ref.Cases[0].ExpectedHash = Hash([]byte("changed"))
	if loaded.Verify(ref) == nil {
		t.Fatal("metadata tampering accepted")
	}
	path := filepath.Join(root, ref.Hash+".json")
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(ref.Hash); !errors.Is(err, ErrIntegrity) {
		t.Fatal("corruption accepted", err)
	}
	if _, err = s.Put(b); !errors.Is(err, ErrIntegrity) {
		t.Fatal("corrupt immutable object overwritten", err)
	}
	if _, err = s.Load("../private"); !errors.Is(err, ErrIntegrity) {
		t.Fatal("path accepted")
	}
}

func TestBundleValidation(t *testing.T) {
	for _, mutate := range []func(*Bundle){
		func(b *Bundle) { b.Schema = 2 }, func(b *Bundle) { b.Comparator = "numeric" }, func(b *Bundle) { b.Tests = nil },
		func(b *Bundle) { b.Tests[1].ID = b.Tests[0].ID }, func(b *Bundle) { b.Tests[0].Order = 2 },
		func(b *Bundle) { b.Tests[0].Input = make([]byte, 65537) }, func(b *Bundle) { b.Tests[0].Expected = make([]byte, 65537) },
	} {
		b, _ := Demo("maximum")
		mutate(&b)
		if b.Validate() == nil {
			t.Fatal("invalid bundle accepted")
		}
	}
	b := Bundle{Schema: 1, Comparator: "exact", Tests: []Case{{ID: "empty", Order: 1}}}
	if b.Validate() != nil {
		t.Fatal("empty input/output must be valid")
	}
}
