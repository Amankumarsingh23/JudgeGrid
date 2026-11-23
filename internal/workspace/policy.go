package workspace

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
)

const (
	Policy         = "object-workspace-v1"
	ApprovalPolicy = "exact-owner-patch-v1"
	maxFiles       = 512
	maxTreeBytes   = 16 << 20
	maxFileBytes   = 4 << 20
	maxChange      = 64 << 10
	maxPatch       = 128 << 10
	maxInput       = 384 << 10
	maxDiff        = 384 << 10
	maxOps         = 8
)

var (
	ErrDenied    = errors.New("workspace authority unavailable")
	ErrInvalid   = errors.New("workspace policy rejected input")
	ErrConflict  = errors.New("workspace version or preimage conflict")
	ErrBusy      = errors.New("workspace has an active claim")
	ErrIntegrity = errors.New("workspace integrity check failed")
)

type File struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Hash string `json:"sha256,omitempty"`
	Size int64  `json:"size"`
}
type Change struct {
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Mode       string `json:"mode"`
	Before     string `json:"before_sha256,omitempty"`
	BeforeSize int    `json:"before_size"`
	After      string `json:"after_sha256"`
	AfterSize  int    `json:"after_size"`
	Text       string `json:"text"`
}
type Patch struct {
	Base    string   `json:"base_version"`
	Changes []Change `json:"changes"`
}
type Proposal struct {
	Workspace string `json:"workspace"`
	Digest    string `json:"digest"`
	Patch     Patch  `json:"patch"`
	Diff      string `json:"diff"`
}

var segment = regexp.MustCompile(`^[A-Za-z0-9._@+ -]{1,120}$`)

func portable(path string) bool {
	if len(path) > 512 {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if !segment.MatchString(part) || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.TrimSpace(part) != part {
			return false
		}
		p := strings.ToLower(part)
		stem, _, _ := strings.Cut(p, ".")
		switch stem {
		case "con", "prn", "aux", "nul", "com0", "com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9", "lpt0", "lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9":
			return false
		}
		if p == ".git" || strings.HasPrefix(p, "git~") {
			return false
		}
	}
	return true
}

func protected(path string) bool {
	for _, part := range strings.Split(strings.ToLower(path), "/") {
		switch part {
		case ".github", ".gitmodules", ".gitattributes", ".gitconfig", ".git-credentials", ".hg", ".svn", ".ssh", ".aws", ".azure", ".config", ".docker", ".netrc", ".npmrc", ".pypirc", ".bashrc", ".profile", "secrets", "credentials", "kubeconfig", "id_rsa", "id_ed25519":
			return true
		}
		if strings.HasPrefix(part, ".env") || strings.Contains(part, "credential") || strings.Contains(part, "secret") || strings.HasSuffix(part, ".pem") || strings.HasSuffix(part, ".key") || strings.HasSuffix(part, ".pfx") || strings.HasSuffix(part, ".p12") {
			return true
		}
	}
	return false
}

func digest(v any) string { b, _ := json.Marshal(v); return ingest.Digest(b) }
func validHash(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func safeText(s string) bool {
	if !utf8.ValidString(s) || len(s) > maxChange {
		return false
	}
	for i, r := range s {
		if (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') || (r == '\r' && (i+1 == len(s) || s[i+1] != '\n')) || unicode.Is(unicode.Cf, r) || r == 0xfffd {
			return false
		}
	}
	return true
}

func Decode(r io.Reader) (Patch, error) {
	var p Patch
	data, err := io.ReadAll(io.LimitReader(r, maxInput+1))
	if err != nil {
		return p, err
	}
	if len(data) > maxInput || !utf8.Valid(data) {
		return p, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if !uniqueKeys(d, 0) {
		return p, ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&p); err != nil {
		return p, ErrInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return p, ErrInvalid
	}
	return normalize(p)
}

func uniqueKeys(d *json.Decoder, depth int) bool {
	if depth > 4 {
		return false
	}
	token, err := d.Token()
	if err != nil {
		return false
	}
	if token != json.Delim('{') && token != json.Delim('[') {
		return true
	}
	seen := map[string]bool{}
	for d.More() {
		if token == json.Delim('{') {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] || strings.ToLower(name) != name {
				return false
			}
			seen[name] = true
		}
		if !uniqueKeys(d, depth+1) {
			return false
		}
	}
	_, err = d.Token()
	return err == nil
}

func normalize(p Patch) (Patch, error) {
	if !validHash(p.Base) || len(p.Changes) < 1 || len(p.Changes) > maxOps {
		return Patch{}, ErrInvalid
	}
	p.Changes = slices.Clone(p.Changes)
	slices.SortFunc(p.Changes, func(a, b Change) int { return strings.Compare(a.Path, b.Path) })
	var changed int
	seen := map[string]bool{}
	for _, c := range p.Changes {
		k := strings.ToLower(c.Path)
		if !portable(c.Path) || protected(c.Path) || c.Mode != "100644" || seen[k] || !safeText(c.Text) ||
			c.AfterSize != len(c.Text) || c.After != ingest.Digest([]byte(c.Text)) || c.BeforeSize < 0 || c.BeforeSize > maxChange {
			return Patch{}, ErrInvalid
		}
		seen[k] = true
		switch c.Kind {
		case "ADD":
			if c.Before != "" || c.BeforeSize != 0 {
				return Patch{}, ErrInvalid
			}
		case "MODIFY":
			if !validHash(c.Before) || c.Before == c.After {
				return Patch{}, ErrInvalid
			}
		default:
			return Patch{}, ErrInvalid
		}
		changed += c.BeforeSize + c.AfterSize
	}
	if changed > maxPatch {
		return Patch{}, ErrInvalid
	}
	return p, nil
}

// validateInventory rejects aliasing at every directory level as well as file/directory collisions.
func validateInventory(files []File) error {
	if len(files) > maxFiles {
		return ErrInvalid
	}
	nodes := map[string]string{}
	leaf := map[string]bool{}
	var total int64
	for i, f := range files {
		if !portable(f.Path) || (i > 0 && files[i-1].Path >= f.Path) || f.Size < 0 || f.Size > maxFileBytes {
			return ErrInvalid
		}
		if f.Hash != "" && (!validHash(f.Hash) || (f.Mode != "100644" && f.Mode != "100755")) {
			return ErrInvalid
		}
		if f.Hash == "" && (f.Size != 0 || (f.Mode != "RESERVED" && f.Mode != "DIRECTORY")) {
			return ErrInvalid
		}
		total += f.Size
		parts := strings.Split(f.Path, "/")
		for j := range parts {
			exact := strings.Join(parts[:j+1], "/")
			k := strings.ToLower(exact)
			existing, ok := nodes[k]
			if ok && (existing != exact || leaf[k] || j == len(parts)-1) {
				return ErrInvalid
			}
			nodes[k] = exact
			leaf[k] = j == len(parts)-1 && f.Mode != "DIRECTORY"
		}
	}
	if total > maxTreeBytes {
		return ErrInvalid
	}
	return nil
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	l := strings.SplitAfter(s, "\n")
	if l[len(l)-1] == "" {
		l = l[:len(l)-1]
	}
	return l
}

func unified(w *strings.Builder, c Change, old string) {
	before := "a/" + c.Path
	if c.Kind == "ADD" {
		before = "/dev/null"
	}
	a, b := lines(old), lines(c.Text)
	fmt.Fprintf(w, "--- %s\n+++ b/%s\n@@ -%d,%d +%d,%d @@\n", before, c.Path, min(1, len(a)), len(a), min(1, len(b)), len(b))
	for _, group := range []struct {
		prefix string
		lines  []string
	}{{"-", a}, {"+", b}} {
		for _, l := range group.lines {
			w.WriteString(group.prefix)
			w.WriteString(l)
			if !strings.HasSuffix(l, "\n") {
				w.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
}
