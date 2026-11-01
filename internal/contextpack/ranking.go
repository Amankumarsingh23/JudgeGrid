package contextpack

import (
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"math"
	"regexp"
	"sort"
	"strings"
)

var exactPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

func Intent(query, override string) (string, error) {
	if override != "" {
		switch override {
		case "exact", "implementation", "conceptual", "tests", "documentation", "mixed":
			return override, nil
		}
		return "", ErrBudget
	}
	q := strings.TrimSpace(query)
	lower := strings.ToLower(q)
	if exactPattern.MatchString(q) {
		return "exact", nil
	}
	if strings.Contains(lower, "documentation") || strings.Contains(lower, "readme") || strings.HasPrefix(lower, "docs ") {
		return "documentation", nil
	}
	if strings.Contains(lower, "test") || strings.Contains(lower, "evidence") {
		return "tests", nil
	}
	if strings.HasPrefix(lower, "where ") || strings.Contains(lower, "implementation") || strings.Contains(lower, "source code") {
		return "implementation", nil
	}
	if strings.HasPrefix(lower, "how ") || strings.HasPrefix(lower, "why ") || strings.Contains(lower, "explain") {
		return "conceptual", nil
	}
	return "mixed", nil
}
func sourceType(path string) string {
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, "_test.go") || strings.Contains(lower, "/tests/") || strings.HasPrefix(lower, "tests/") || strings.HasPrefix(lower, "test_") {
		return "tests"
	}
	if strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".rst") || strings.HasPrefix(lower, "docs/") {
		return "documentation"
	}
	return "implementation"
}
func preference(kind, intent string) float64 {
	if kind == intent || (intent == "exact" && kind == "implementation") || (intent == "conceptual" && kind == "implementation") {
		return 1
	}
	if intent == "mixed" {
		return .5
	}
	return .2
}
func normalize(logit float64) float64 {
	if logit >= 0 {
		return 1 / (1 + math.Exp(-logit))
	}
	e := math.Exp(logit)
	return e / (1 + e)
}
func rank(candidates []Candidate, query, intent string) {
	for i := range candidates {
		c := &candidates[i]
		exact := 0.0
		if intent == "exact" && (c.Symbol == strings.TrimSpace(query) || (!strings.Contains(query, ".") && strings.HasSuffix(c.Symbol, "."+strings.TrimSpace(query)))) {
			exact = 1
		}
		// A bounded, explicit type/exact-name preference supplements the actual model
		// logit. Sigmoid is a score mapping, never a calibrated probability.
		c.Relevance = .7*normalize(c.RerankerScore) + .2*exact + .1*preference(c.SourceType, intent)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Relevance != candidates[j].Relevance {
			return candidates[i].Relevance > candidates[j].Relevance
		}
		return candidates[i].Chunk.ID < candidates[j].Chunk.ID
	})
	for i := range candidates {
		candidates[i].Rerank = i + 1
	}
}
func Redundancy(a, b analysis.Span, sameFile bool) float64 {
	if !sameFile {
		return 0
	}
	n := min(a.End, b.End) - max(a.Start, b.Start)
	if n <= 0 {
		return 0
	}
	return float64(n) / float64(min(a.End-a.Start, b.End-b.Start))
}
func MMRScore(relevance, redundancy, lambda float64) float64 {
	return lambda*relevance - (1-lambda)*redundancy
}
func Select(candidates []Candidate, p Policy, intent string) ([]Candidate, []Omission) {
	left := append([]Candidate(nil), candidates...)
	selected := []Candidate{}
	omitted := []Omission{}
	// Implementation-oriented requests reserve one genuine source candidate;
	// documentation/tests remain eligible rather than being discarded.
	if intent == "implementation" || intent == "exact" {
		for i, c := range left {
			if c.SourceType == "implementation" && c.Symbol != "" {
				selected = append(selected, c)
				left = append(left[:i], left[i+1:]...)
				break
			}
		}
	}
	for len(left) > 0 && len(selected) < p.MaxBlocks {
		best := -1
		bestScore := math.Inf(-1)
		for i, c := range left {
			redundancy := 0.0
			for _, s := range selected {
				redundancy = math.Max(redundancy, Redundancy(c.WindowSpan, s.WindowSpan, c.Chunk.File == s.Chunk.File))
			}
			if redundancy >= .8 {
				continue
			}
			score := MMRScore(c.Relevance, redundancy, p.Lambda)
			if score > bestScore || (score == bestScore && (best < 0 || c.Chunk.ID < left[best].Chunk.ID)) {
				best = i
				bestScore = score
			}
		}
		if best < 0 {
			break
		}
		selected = append(selected, left[best])
		left = append(left[:best], left[best+1:]...)
	}
	for _, c := range left {
		reason := "block_limit"
		for _, s := range selected {
			if Redundancy(c.WindowSpan, s.WindowSpan, c.Chunk.File == s.Chunk.File) >= .8 {
				reason = "overlap"
			}
		}
		omitted = append(omitted, Omission{c.Chunk.ID, reason})
	}
	return selected, omitted
}
func weakEvidence(query string, c []Candidate) bool {
	terms := retrieval.Tokens(query)
	for _, v := range c {
		if v.RerankerScore >= 0 {
			return false
		}
		for term := range retrieval.Tokens(v.Path + " " + v.Symbol) {
			if terms[term] > 0 {
				return false
			}
		}
	}
	return true
}
