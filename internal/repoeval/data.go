// Package repoeval evaluates frozen, source-grounded retrieval experiments.
// It has no answer generator, action dispatcher or public endpoint.
package repoeval

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/contextpack"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid frozen evaluation data")
var ErrIncomplete = errors.New("evaluation incomplete")
var ErrIntegrity = ingest.ErrIntegrity
var ErrDenied = ingest.ErrDenied

func canonical(v any) []byte { b, _ := json.Marshal(v); return b }
func digest(v any) string    { return ingest.Digest(canonical(v)) }

//go:embed data.go metrics.go matching.go snapshot.go runner.go report.go checkpoint.go
var implementation embed.FS

func ImplementationID() string {
	names, _ := implementation.ReadDir(".")
	parts := []string{"repo-eval-v1", contextpack.EvaluationIdentity()}
	for _, n := range names {
		b, _ := implementation.ReadFile(n.Name())
		parts = append(parts, ingest.Digest(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	return digest(parts)
}

type Policy struct {
	Version        string  `json:"version"`
	Implementation string  `json:"implementation"`
	K              []int   `json:"k"`
	Relevant       int     `json:"relevant_grade"`
	GoldCoverage   float64 `json:"gold_match_coverage"`
	MinIoU         float64 `json:"min_iou"`
	GroupCoverage  float64 `json:"required_group_coverage"`
	Seed           int64   `json:"seed"`
	Resamples      int     `json:"bootstrap_resamples"`
	MaxQueries     int     `json:"max_queries"`
	Seconds        int     `json:"run_seconds"`
	ReverifyEvery  int     `json:"reverify_every_queries"`
	Convention     string  `json:"unjudged_convention"`
}

func DefaultPolicy() Policy {
	return Policy{"repo-eval-v1", ImplementationID(), []int{1, 5, 10, 20}, 2, .5, .01, 1, 20261102, 2000, 80, 1800, 15, "precision-bounds-pooled-zero-gain-with-coverage"}
}
func (p Policy) ID() string { return digest(p) }
func (p Policy) Validate() error {
	if p.Version != "repo-eval-v1" || p.Implementation != ImplementationID() || p.Relevant < 1 || p.Relevant > 3 || p.Resamples < 2000 || p.Resamples > 10000 || p.MaxQueries < 1 || p.MaxQueries > 100 || p.Seconds < 1 || p.Seconds > 3600 || p.ReverifyEvery < 1 || p.ReverifyEvery > 20 || p.Convention != "precision-bounds-pooled-zero-gain-with-coverage" || len(p.K) < 1 || len(p.K) > 8 {
		return ErrInvalid
	}
	for _, v := range []float64{p.GoldCoverage, p.MinIoU, p.GroupCoverage} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > 1 {
			return ErrInvalid
		}
	}
	last := 0
	for _, k := range p.K {
		if k <= last || k > 50 {
			return ErrInvalid
		}
		last = k
	}
	return nil
}

type Scope struct {
	Owner             string `json:"owner"`
	Repository        string `json:"repository"`
	Snapshot          string `json:"snapshot"`
	Commit            string `json:"commit"`
	SourceManifest    string `json:"source_manifest"`
	Analysis          string `json:"analysis"`
	AnalysisManifest  string `json:"analysis_manifest"`
	Retrieval         string `json:"retrieval"`
	RetrievalManifest string `json:"retrieval_manifest"`
	RetrievalPolicy   string `json:"retrieval_policy"`
	ContextPolicy     string `json:"context_policy"`
	Tokenizer         string `json:"tokenizer"`
}
type Ref struct {
	Snapshot string        `json:"snapshot"`
	File     string        `json:"file_id"`
	Path     string        `json:"path"`
	Span     analysis.Span `json:"span"`
	SHA256   string        `json:"sha256"`
	ChunkIDs []string      `json:"chunk_ids"`
}
type Annotation struct {
	Method        string `json:"method"`
	Author        string `json:"author"`
	Status        string `json:"status"`
	HumanReviewed bool   `json:"human_reviewed"`
	Reviewer      string `json:"reviewer"`
	Rationale     string `json:"rationale"`
}
type Judgment struct {
	ID         string     `json:"id"`
	Ref        Ref        `json:"source"`
	Grade      int        `json:"grade"`
	Annotation Annotation `json:"annotation"`
}
type Group struct {
	ID           string   `json:"id"`
	Role         string   `json:"role"`
	Alternatives []string `json:"alternatives"`
	Unknown      bool     `json:"unknown"`
	Uncertainty  string   `json:"uncertainty"`
}
type Query struct {
	ID                string     `json:"id"`
	Question          string     `json:"question"`
	Category          string     `json:"category"`
	Intent            string     `json:"intent"`
	Topic             string     `json:"topic"`
	Split             string     `json:"split"`
	Exposed           bool       `json:"previously_exposed"`
	Answerable        bool       `json:"answerable"`
	NegativeRationale string     `json:"negative_rationale"`
	Judgments         []Judgment `json:"judgments"`
	Groups            []Group    `json:"required_evidence_groups"`
	Uncertainties     []string   `json:"uncertainties"`
}
type Benchmark struct {
	Schema      int        `json:"schema"`
	Name        string     `json:"name"`
	Version     int        `json:"version"`
	Frozen      bool       `json:"frozen"`
	SafeFixture bool       `json:"safe_synthetic_fixture"`
	Scope       Scope      `json:"scope"`
	Policy      Policy     `json:"policy"`
	Annotation  Annotation `json:"annotation"`
	SplitDigest string     `json:"split_digest"`
	Queries     []Query    `json:"queries"`
}

func (b Benchmark) ID() string { return digest(b) }
func (b Benchmark) QrelsID() string {
	return digest(struct {
		Scope      Scope
		Queries    []Query
		Annotation Annotation
	}{b.Scope, b.Queries, b.Annotation})
}
func (b Benchmark) SplitsID() string {
	rows := [][3]string{}
	for _, q := range b.Queries {
		rows = append(rows, [3]string{q.ID, q.Split, q.Topic})
	}
	return digest(rows)
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)
var sha = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validAnnotation(a Annotation) bool {
	return a.Method == "model-assisted-source-inspection" && a.Author != "" && a.Status == "provisional" && !a.HumanReviewed && a.Reviewer == "unavailable" && a.Rationale != ""
}
func (b Benchmark) Validate() error {
	if b.Schema != 1 || !identifier.MatchString(b.Name) || b.Version < 1 || !b.Frozen || b.Policy.Validate() != nil || len(b.Queries) < 1 || len(b.Queries) > b.Policy.MaxQueries || !validAnnotation(b.Annotation) || b.SplitDigest != b.SplitsID() {
		return ErrInvalid
	}
	if b.Scope.Owner == "" || b.Scope.Repository == "" || !regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`).MatchString(b.Scope.Commit) {
		return ErrInvalid
	}
	for _, v := range []string{b.Scope.Snapshot, b.Scope.SourceManifest, b.Scope.Analysis, b.Scope.AnalysisManifest, b.Scope.Retrieval, b.Scope.RetrievalManifest, b.Scope.RetrievalPolicy, b.Scope.ContextPolicy, b.Scope.Tokenizer} {
		if !sha.MatchString(v) {
			return ErrInvalid
		}
	}
	ids, questions := map[string]bool{}, map[string]bool{}
	for _, q := range b.Queries {
		if !identifier.MatchString(q.ID) || ids[q.ID] || questions[q.Question] || strings.TrimSpace(q.Question) == "" || !utf8.ValidString(q.Question) || strings.ContainsRune(q.Question, 0) || len(q.Question) > 1024 || q.Category == "" || q.Topic == "" || len(q.Judgments) > 32 || len(q.Groups) > 12 {
			return ErrInvalid
		}
		ids[q.ID] = true
		questions[q.Question] = true
		if _, e := contextpack.Intent(q.Question, q.Intent); e != nil {
			return ErrInvalid
		}
		if q.Split != "dev" && q.Split != "test" && q.Split != "train" {
			return ErrInvalid
		}
		if q.Exposed && q.Split == "test" {
			return ErrInvalid
		}
		js := map[string]Judgment{}
		positive := 0
		for _, j := range q.Judgments {
			if !identifier.MatchString(j.ID) || js[j.ID].ID != "" || j.Grade < 0 || j.Grade > 3 || !validAnnotation(j.Annotation) || j.Ref.Snapshot != b.Scope.Snapshot || j.Ref.File == "" || j.Ref.Path == "" || !sha.MatchString(j.Ref.SHA256) || j.Ref.Span.Start < 0 || j.Ref.Span.End <= j.Ref.Span.Start || len(j.Ref.ChunkIDs) == 0 {
				return ErrInvalid
			}
			js[j.ID] = j
			if j.Grade >= b.Policy.Relevant {
				positive++
			}
		}
		if q.Answerable && (positive == 0 || len(q.Groups) == 0) {
			return ErrInvalid
		}
		if !q.Answerable && (positive > 0 || q.NegativeRationale == "") {
			return ErrInvalid
		}
		groups := map[string]bool{}
		for _, g := range q.Groups {
			if g.ID == "" || groups[g.ID] || g.Role == "" {
				return ErrInvalid
			}
			groups[g.ID] = true
			if g.Unknown {
				if g.Uncertainty == "" || len(g.Alternatives) > 0 {
					return ErrInvalid
				}
				continue
			}
			if len(g.Alternatives) == 0 {
				return ErrInvalid
			}
			for _, id := range g.Alternatives {
				j, ok := js[id]
				if !ok || j.Grade < b.Policy.Relevant {
					return ErrInvalid
				}
			}
		}
	}
	return nil
}
func Categories(b Benchmark) []string {
	m := map[string]bool{}
	for _, q := range b.Queries {
		m[q.Category] = true
	}
	s := []string{}
	for c := range m {
		s = append(s, c)
	}
	sort.Strings(s)
	return s
}
