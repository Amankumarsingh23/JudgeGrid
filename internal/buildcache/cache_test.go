package buildcache

import (
	"reflect"
	"testing"
)

func TestCanonicalCompilationIdentity(t *testing.T) {
	k := Key{Schema: 1, SourceSHA: "source", Compiler: "g++", Toolchain: "image", Flags: "-O2", Target: "linux/amd64", Dependencies: "headers", Policy: "policy", Seccomp: "filter", Memory: 1, Wall: 2, Output: 3}
	if k.ID() != k.ID() {
		t.Fatal("non-deterministic key")
	}
	v := reflect.ValueOf(k)
	for i := 0; i < v.NumField(); i++ {
		copy := k
		f := reflect.ValueOf(&copy).Elem().Field(i)
		if f.Kind() == reflect.String {
			f.SetString(f.String() + "changed")
		} else {
			f.SetInt(f.Int() + 1)
		}
		if copy.ID() == k.ID() {
			t.Fatalf("identity omitted %s", v.Type().Field(i).Name)
		}
	}
}
func TestRejectInvalidArtifactFormat(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("not executable"), make([]byte, 100), make([]byte, (16<<20)+1)} {
		if Validate(b) == nil {
			t.Fatal("invalid executable accepted")
		}
	}
}
