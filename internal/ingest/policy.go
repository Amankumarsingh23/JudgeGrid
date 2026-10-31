package ingest

import (
	"bytes"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Policy struct {
	Version       string `json:"version"`
	Docs          bool   `json:"docs"`
	MaxEntries    int    `json:"max_entries"`
	MaxDepth      int    `json:"max_depth"`
	MaxFiles      int    `json:"max_candidate_files"`
	MaxFileBytes  int64  `json:"max_file_bytes"`
	MaxTotalBytes int64  `json:"max_candidate_bytes"`
	MaxPathBytes  int    `json:"max_total_path_bytes"`
	MaxSeconds    int    `json:"max_seconds"`
	ParallelReads int    `json:"parallel_blob_reads"`
}

func DefaultPolicy() Policy {
	return Policy{"source-v1", true, 10000, 32, 2000, 1 << 20, 16 << 20, 1 << 20, 120, 1}
}
func (p Policy) Validate() error {
	if p.Version != "source-v1" || p.MaxEntries < 1 || p.MaxEntries > 20000 || p.MaxDepth < 1 || p.MaxDepth > 64 || p.MaxFiles < 1 || p.MaxFiles > 5000 || p.MaxFileBytes < 1 || p.MaxFileBytes > 2<<20 || p.MaxTotalBytes < 1 || p.MaxTotalBytes > 32<<20 || p.MaxPathBytes < 1 || p.MaxPathBytes > 2<<20 || p.MaxSeconds < 1 || p.MaxSeconds > 300 || p.ParallelReads != 1 {
		return ErrBudget
	}
	return nil
}
func (p Policy) ID() string { return identity(p) }

var validHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,79}$`)
var windowsDevice = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(?:\.|$)`)

func validPath(v string) bool {
	if len(v) == 0 || len(v) > 512 || !utf8.ValidString(v) || !norm.NFC.IsNormalString(v) || strings.ContainsAny(v, "\\:<>\"|?*") || strings.HasPrefix(v, "/") {
		return false
	}
	for _, c := range v {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
			return false
		}
	}
	for _, c := range strings.Split(v, "/") {
		if c == "" || c == "." || c == ".." || strings.EqualFold(c, ".git") || strings.TrimRight(c, " .") != c || windowsDevice.MatchString(c) {
			return false
		}
	}
	return true
}
func foldPath(p string) string { return cases.Fold().String(p) }
func selectPath(p string, docs bool) (string, string) {
	lower := strings.ToLower(p)
	base := path.Base(lower)
	for _, part := range strings.Split(lower, "/") {
		switch part {
		case "vendor", "node_modules", "dist", "build", "target", "coverage", ".venv", "venv", "__pycache__", ".next", ".cache":
			return "", "dependency_build"
		}
		if strings.HasPrefix(part, ".env") || part == ".ssh" || part == ".aws" || part == ".npmrc" || part == ".netrc" || part == ".git-credentials" || strings.HasPrefix(part, "credentials") || strings.HasPrefix(part, "secrets.") || strings.HasPrefix(part, "id_rsa") || strings.HasPrefix(part, "id_ed25519") {
			return "", "sensitive_path"
		}
	}
	ext := path.Ext(lower)
	switch ext {
	case ".pem", ".key", ".p12", ".pfx", ".keystore":
		return "", "sensitive_path"
	}
	if strings.Contains(base, ".min.") || strings.HasSuffix(base, ".map") || base == "package-lock.json" || base == "yarn.lock" || base == "go.sum" || strings.HasSuffix(base, ".pb.go") {
		return "", "generated_minified"
	}
	langs := map[string]string{".go": "go", ".py": "python", ".java": "java", ".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".hpp": "cpp", ".js": "javascript", ".jsx": "javascript", ".ts": "typescript", ".tsx": "typescript", ".json": "json", ".yaml": "yaml", ".yml": "yaml", ".toml": "toml", ".sql": "sql", ".proto": "protobuf", ".sh": "shell", ".md": "markdown", ".txt": "text", ".mod": "gomod"}
	lang := langs[ext]
	if base == "dockerfile" {
		lang = "dockerfile"
	}
	if base == ".gitignore" || base == ".gitattributes" {
		lang = "text"
	}
	if lang == "" {
		return "", "unsupported_type"
	}
	if !docs && (lang == "markdown" || lang == "text") {
		return lang, "docs_disabled"
	}
	return lang, "included"
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})`),
	regexp.MustCompile(`(?i)(?:password|secret|access_token|api_key)\s*[:=]\s*["'][A-Za-z0-9_+/=-]{12,}["']`),
	regexp.MustCompile(`https?://[^\s/@:]+:[^\s/@]+@`),
}

func classify(b []byte, lang string) string {
	if bytes.IndexByte(b, 0) >= 0 {
		return "binary"
	}
	if !utf8.Valid(b) {
		return "encoding"
	}
	if len(b) <= 4096 && (bytes.HasPrefix(b, []byte("version https://git-lfs.github.com/spec/v1\n")) || bytes.HasPrefix(b, []byte("version https://git-lfs.github.com/spec/v1\r\n"))) {
		return "lfs_pointer"
	}
	for _, r := range secretPatterns {
		if r.Match(b) {
			return "secret_evidence"
		}
	}
	head := b
	if len(head) > 8192 {
		head = head[:8192]
	}
	if bytes.Contains(bytes.ToLower(head), []byte("code generated")) && bytes.Contains(bytes.ToLower(head), []byte("do not edit")) {
		return "generated_minified"
	}
	if lang == "javascript" || lang == "typescript" {
		for _, line := range bytes.Split(b, []byte{'\n'}) {
			if len(line) > 8192 {
				return "generated_minified"
			}
		}
	}
	return "included"
}
