package repoeval

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type Summary struct {
	Split    string   `json:"split"`
	Category string   `json:"category"`
	Variant  string   `json:"variant"`
	K        int      `json:"k"`
	Metric   string   `json:"metric"`
	Interval Interval `json:"query_bootstrap_95"`
}
type Comparison struct {
	Split    string   `json:"split"`
	Category string   `json:"category"`
	Baseline string   `json:"baseline"`
	Variant  string   `json:"variant"`
	K        int      `json:"k"`
	Metric   string   `json:"metric"`
	Delta    Interval `json:"paired_query_delta_95"`
}
type Report struct {
	Schema           int            `json:"schema"`
	State            string         `json:"state"`
	Benchmark        string         `json:"benchmark_sha256"`
	Qrels            string         `json:"qrels_sha256"`
	Policy           string         `json:"policy_sha256"`
	Evaluator        string         `json:"evaluator_sha256"`
	Scope            Scope          `json:"scope"`
	Queries          int            `json:"queries"`
	Observations     int            `json:"observations"`
	Counts           map[string]int `json:"counts"`
	Summary          []Summary      `json:"macro_metrics"`
	Comparisons      []Comparison   `json:"paired_comparisons"`
	ExactSummary     []Summary      `json:"exact_chunk_macro_metrics"`
	ExactComparisons []Comparison   `json:"exact_chunk_paired_comparisons"`
	Rows             []Observation  `json:"per_query"`
	Attempts         []Observation  `json:"prior_failed_attempts"`
	Fresh            []Fresh        `json:"fresh_end_to_end"`
	CaptureSeconds   float64        `json:"verified_capture_seconds"`
	EmbeddingSeconds float64        `json:"shared_query_embedding_seconds"`
	EmbeddingCalls   int            `json:"shared_embedding_calls"`
	MetricsDigest    string         `json:"metrics_sha256"`
	Limits           []string       `json:"limitations"`
	Environment      map[string]any `json:"experiment_conditions"`
}

func measure(m Metric, name string) *float64 {
	switch name {
	case "precision_lower":
		return ptr(m.PrecisionLower)
	case "precision_upper":
		return ptr(m.PrecisionUpper)
	case "judgment_coverage":
		return ptr(m.JudgmentCoverage)
	case "known_recall":
		return m.Recall
	case "hit_lower":
		return m.Hit
	case "judged_precision":
		return m.JudgedPrecision
	case "dcg_lower":
		return ptr(m.DCG)
	case "mrr_lower":
		return m.MRR
	case "map_known_zero_gain":
		return m.AP
	case "ndcg_pooled_zero_gain":
		return m.NDCG
	}
	return nil
}
func BuildReport(b Benchmark, c Checkpoint, s *Session) Report { return buildReport(b, c, s, true) }
func buildReport(b Benchmark, c Checkpoint, s *Session, includeExact bool) Report {
	if len(c.Rows) != len(b.Queries)*len(Variants) || len(c.Fresh) != len(c.FreshPlan)*12 {
		c.State = "INCOMPLETE"
	}
	r := Report{Schema: 1, State: c.State, Benchmark: b.ID(), Policy: b.Policy.ID(), Evaluator: ImplementationID(), Scope: b.Scope, Queries: len(b.Queries), Observations: len(c.Rows), Counts: map[string]int{}, Rows: sortedRows(c.Rows), Fresh: c.Fresh, Limits: []string{"Model-assisted provisional labels; independent human review unavailable.", "Unjudged results remain unknown; pooled scores explicitly assume zero gain, with precision bounds and judgment coverage reported.", "Known-positive recall/MAP denominators describe the judged pool, not exhaustive corpus recall.", "No-positive queries are excluded from recall/MRR/MAP/nDCG macro denominators; errors are not abstentions.", "V6 order is evidence assembly order, not a document reranker; compare its coverage separately.", "Offline scoped verified-corpus reuse is not fresh end-to-end latency; each helper process remains cold.", "Bootstrap intervals describe this query sample, not causal production improvement or actual LLM-answer groundedness.", "Native Windows Session 15 agent failures remain unresolved; hosted Linux results are recorded separately."}}
	if s != nil {
		r.CaptureSeconds = s.SourceCaptureSeconds
		r.EmbeddingSeconds = s.EmbeddingSeconds
		r.EmbeddingCalls = s.EmbeddingCalls
	}
	r.Qrels = b.QrelsID()
	r.Limits = append(r.Limits, "Only precision bounds and known-relevance MRR/DCG have the stated lower-bound interpretation conditional on labels. Pooled nDCG and AP are not lower bounds on whole-corpus quality: unjudged positives can change ideal-gain and relevance denominators.")
	r.Environment = map[string]any{"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "logical_cpus": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0), "evaluation_concurrency": 1, "mode": "owner-scoped-frozen-source-authorized-each-read-reverified-at-checkpoints-and-publication", "model_process_persistent": false, "source_byte_cap": 32 << 20, "run_seconds": b.Policy.Seconds, "resamples": b.Policy.Resamples, "seed": b.Policy.Seed, "no_policy_tuning": true}
	r.Attempts = c.Attempts
	r.Counts["prior_failed_attempts"] = len(c.Attempts)
	byQuery := map[string]Query{}
	for _, q := range b.Queries {
		byQuery[q.ID] = q
		r.Counts["category/"+q.Category]++
		r.Counts["split/"+q.Split]++
		if q.Exposed {
			r.Counts["previously_exposed"]++
		}
		if !q.Answerable {
			r.Counts["negative"]++
		}
		r.Counts["judgments"] += len(q.Judgments)
	}
	for _, o := range r.Rows {
		if o.Error != "" {
			r.Counts["error/"+o.Error]++
		}
		if o.Variant == Variants[5] && o.Error == "" {
			r.Counts["support/"+o.Support.State]++
			if o.Negative != "" {
				r.Counts["negative/"+o.Negative]++
			}
		}
	}
	names := []string{"precision_lower", "precision_upper", "judged_precision", "judgment_coverage", "known_recall", "hit_lower", "mrr_lower", "map_known_zero_gain", "dcg_lower", "ndcg_pooled_zero_gain"}
	categories := append([]string{"all"}, Categories(b)...)
	for _, split := range []string{"all", "dev", "test", "train"} {
		for _, category := range categories {
			for _, k := range b.Policy.K {
				for _, name := range names {
					values := map[string]map[string]float64{}
					for _, v := range Variants {
						values[v] = map[string]float64{}
					}
					for _, o := range r.Rows {
						q := byQuery[o.Query]
						if o.Error != "" || (split != "all" && q.Split != split) || (category != "all" && q.Category != category) {
							continue
						}
						for _, m := range o.SpanMetrics {
							if m.K == k {
								if v := measure(m, name); v != nil {
									values[o.Variant][o.Query] = *v
								}
							}
						}
					}
					for _, variant := range Variants {
						vs := []float64{}
						ids := []string{}
						for id := range values[variant] {
							ids = append(ids, id)
						}
						sort.Strings(ids)
						for _, id := range ids {
							vs = append(vs, values[variant][id])
						}
						if len(vs) > 0 {
							r.Summary = append(r.Summary, Summary{split, category, variant, k, name, Bootstrap(vs, b.Policy.Seed, b.Policy.Resamples)})
						}
						if variant == Variants[2] {
							continue
						}
						delta := []float64{}
						for _, id := range ids {
							if base, ok := values[Variants[2]][id]; ok {
								delta = append(delta, values[variant][id]-base)
							}
						}
						if len(delta) > 0 {
							r.Comparisons = append(r.Comparisons, Comparison{split, category, Variants[2], variant, k, name, Bootstrap(delta, b.Policy.Seed, b.Policy.Resamples)})
						}
					}
				}
			}
		}
	}
	if includeExact {
		exact := c
		exact.Rows = append([]Observation(nil), c.Rows...)
		for i := range exact.Rows {
			exact.Rows[i].SpanMetrics = exact.Rows[i].Metrics
		}
		other := buildReport(b, exact, s, false)
		r.ExactSummary = other.Summary
		r.ExactComparisons = other.Comparisons
	}
	r.MetricsDigest = digest([]any{r.Benchmark, r.Qrels, r.Policy, r.Summary, r.Comparisons, r.ExactSummary, r.ExactComparisons})
	return r
}
func WriteReport(dir string, r Report) error {
	if r.State != "COMPLETE" && r.State != "COMPLETE_WITH_ERRORS" {
		return ErrIncomplete
	}
	// Reports are derived artifacts; the immutable benchmark itself is exclusive.
	if e := atomicJSON(filepath.Join(dir, "report.json"), r); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".report-*.csv")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_ = f.Chmod(0600)
	w := csv.NewWriter(f)
	_ = w.Write([]string{"matching", "split", "category", "variant", "k", "metric", "n", "mean", "low95", "high95", "small_sample"})
	for _, kind := range []string{"span-filtered", "exact-chunk"} {
		rows := r.Summary
		if kind == "exact-chunk" {
			rows = r.ExactSummary
		}
		for _, s := range rows {
			_ = w.Write([]string{kind, s.Split, s.Category, s.Variant, fmt.Sprint(s.K), s.Metric, fmt.Sprint(s.Interval.N), fmt.Sprintf("%.9f", s.Interval.Mean), fmt.Sprintf("%.9f", s.Interval.Low), fmt.Sprintf("%.9f", s.Interval.High), fmt.Sprint(s.Interval.Unstable)})
		}
	}
	w.Flush()
	e = w.Error()
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(tmp, filepath.Join(dir, "report.csv")); e != nil {
		return e
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# Repository retrieval evaluation\n\nState: %s. Benchmark `%s`; evaluator `%s`; policy `%s`.\n\n%d queries, %d recorded observations. Labels are **model-assisted/provisional**, not independent human ground truth.\n\n", r.State, r.Benchmark, r.Evaluator, r.Policy, r.Queries, r.Observations)
	md.WriteString("## Held-out span-filtered ranking proxy\n\nV1–V5 are document rankings; V6 is E1–En evidence assembly order. Unknown gains are zero **only in these explicitly pooled zero-gain metrics**; precision bounds and judged coverage remain separate. No-positive queries and infrastructure failures are excluded from quality denominators.\n\n| Variant | Metric@5 | N | Mean | 95% query bootstrap |\n| --- | --- | --- | --- | --- |\n")
	for _, s := range r.Summary {
		if s.Split == "test" && s.Category == "all" && s.K == 5 {
			fmt.Fprintf(&md, "| %s | %s | %d | %.4f | [%.4f, %.4f] |\n", s.Variant, s.Metric, s.Interval.N, s.Interval.Mean, s.Interval.Low, s.Interval.High)
		}
	}
	md.WriteString("\n## Paired held-out differences from hybrid\n\n| Variant | Metric@5 | Paired N | Mean delta | 95% interval |\n| --- | --- | --- | --- | --- |\n")
	for _, c := range r.Comparisons {
		if c.Split == "test" && c.Category == "all" && c.K == 5 && (c.Metric == "ndcg_pooled_zero_gain" || c.Metric == "known_recall") {
			fmt.Fprintf(&md, "| %s | %s | %d | %+.4f | [%+.4f, %+.4f] |\n", c.Variant, c.Metric, c.Delta.N, c.Delta.Mean, c.Delta.Low, c.Delta.High)
		}
	}
	md.WriteString("\n## Per-query evidence and failures\n\n| Query | Variant | Status/error | Groups | Tokens | Offline seconds | Stage | Verified first citation |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, o := range r.Rows {
		cite := ""
		if len(o.Items) > 0 {
			i := o.Items[0]
			cite = fmt.Sprintf("%s:%d bytes [%d,%d) sha256 %s", i.Ref.Path, i.Ref.Span.StartLine, i.Ref.Span.Start, i.Ref.Span.End, i.Ref.SHA256)
		}
		fmt.Fprintf(&md, "| %s | %s | %s/%s | %d/%d (%s) | %d | %.4f | %s | %s |\n", o.Query, o.Variant, o.Status, o.Error, o.Support.Covered, o.Support.Total, o.Support.State, o.Tokens, o.Seconds, o.FailureStage, cite)
	}
	md.WriteString("\n## Fresh process end-to-end observations\n\nEach attempt uses the production readers and real model calls. Repeated processes may benefit from filesystem caches; no persistent model-loaded warm process is claimed.\n\n| Query | Variant | Attempt | Status | Seconds |\n| --- | --- | --- | --- | --- |\n")
	for _, f := range r.Fresh {
		fmt.Fprintf(&md, "| %s | %s | %s | %s | %.3f |\n", f.Query, f.Variant, f.Attempt, f.Status, f.Seconds)
	}
	md.WriteString("\n## Limits\n\n")
	for _, s := range r.Limits {
		fmt.Fprintf(&md, "- %s\n", s)
	}
	return atomicBytes(filepath.Join(dir, "report.md"), []byte(md.String()))
}
func atomicJSON(path string, v any) error { return atomicBytes(path, canonical(v)) }
func atomicBytes(path string, b []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".report-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_ = f.Chmod(0600)
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(tmp, path)
}
