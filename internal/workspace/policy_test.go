package workspace

import (
	"strings"
	"testing"

	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
)

func change(kind, path, before, after string) Change {
	c := Change{Kind: kind, Path: path, Mode: "100644", BeforeSize: len(before), AfterSize: len(after), After: ingest.Digest([]byte(after)), Text: after}
	if kind == "MODIFY" {
		c.Before = ingest.Digest([]byte(before))
	}
	return c
}

func TestPortablePathsAndPatchInput(t *testing.T) {
	base := strings.Repeat("a", 64)
	for _, path := range []string{"../hello.go", "/absolute", "C:/Absolute", `folder\file.go`, "folder/..", "folder/./file", ".git/config", "gIt~1/index", "folder/aux.go", "folder/CON", "folder./file", " x/file", "a/.env", "a/credentials.txt", "a/id_rsa", "a/private.key", ".github/workflows/ci.yml", "folder/caf\u00e9.go"} {
		// The last case is passed as its UTF-8 form below.
		if _, e := normalize(Patch{Base: base, Changes: []Change{change("ADD", path, "", "test\n")}}); e == nil {
			t.Errorf("accepted %q", path)
		}
	}
	for _, body := range []string{
		`{"base_version":"` + base + `","changes":[],"command":"touch PWNED"}`,
		`{"base_version":"` + base + `","base_version":"` + base + `","changes":[]}`,
		`{"base_version":"` + base + `","changes":[[[[[[]]]]]]}`,
		strings.Repeat(" ", maxInput+1),
	} {
		if _, e := Decode(strings.NewReader(body)); e == nil {
			t.Errorf("accepted malformed input %.80s", body)
		}
	}
	for _, after := range []string{"x\x00", strings.Repeat("x", maxChange+1), "fake\roverwrite", "false\u202eorder"} {
		if _, e := normalize(Patch{Base: base, Changes: []Change{change("ADD", "safe.go", "", after)}}); e == nil {
			t.Errorf("accepted unsafe bytes")
		}
	}
	for _, kind := range []string{"DELETE", "execute_command", "RENAME"} {
		if _, e := normalize(Patch{Base: base, Changes: []Change{change(kind, "safe.go", "", "trusted data")}}); e == nil {
			t.Errorf("accepted %s", kind)
		}
	}
	c := change("ADD", "ok.go", "", "package fixture\n")
	c.Mode = "100755"
	if _, e := normalize(Patch{Base: base, Changes: []Change{c}}); e == nil {
		t.Error("accepted executable write")
	}
}

func TestInventoryAndExactPreview(t *testing.T) {
	if err := validateInventory([]File{{Path: "dir", Mode: "DIRECTORY"}, {Path: "dir/file", Mode: "RESERVED"}}); err != nil {
		t.Fatal("ordinary tracked directory rejected", err)
	}
	for _, files := range [][]File{
		{{Path: "Dir/a", Mode: "RESERVED"}, {Path: "dir/b", Mode: "RESERVED"}},
		{{Path: "dir", Mode: "RESERVED"}, {Path: "dir/file", Mode: "RESERVED"}},
	} {
		if validateInventory(files) == nil {
			t.Fatal("accepted directory alias")
		}
	}
	var w strings.Builder
	unified(&w, change("MODIFY", "demo.go", "old\n", "new"), "old\n")
	const want = "--- a/demo.go\n+++ b/demo.go\n@@ -1,1 +1,1 @@\n-old\n+new\n\\ No newline at end of file\n"
	if got := w.String(); got != want {
		t.Fatalf("preview %q", got)
	}
}
