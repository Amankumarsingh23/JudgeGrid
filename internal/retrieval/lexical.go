package retrieval

import (
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Tokens preserve identifiers and case-folded forms, then split acronym/case/
// underscore boundaries. No substring matching, stop-word list or stemming.
func Tokens(text string) map[string]int {
	out := map[string]int{}
	for _, word := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }) {
		if len(word) > 256 {
			continue
		}
		out["="+word]++
		out[strings.ToLower(word)]++
		rs := []rune(word)
		start := 0
		emit := func(end int) {
			if end > start {
				s := strings.ToLower(string(rs[start:end]))
				if s != strings.ToLower(word) {
					out[s]++
				}
			}
		}
		for i, r := range rs {
			if r == '_' {
				emit(i)
				start = i + 1
				continue
			}
			if i > start && ((unicode.IsUpper(r) && unicode.IsLower(rs[i-1])) || (unicode.IsUpper(rs[i-1]) && unicode.IsUpper(r) && i+1 < len(rs) && unicode.IsLower(rs[i+1]))) {
				emit(i)
				start = i
			}
		}
		emit(len(rs))
	}
	return out
}
func makeDocument(c analysis.Chunk, f analysis.File, symbol string, b []byte) (Document, string, error) {
	if len(b) > 16384 || !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
		return Document{}, "", ErrIntegrity
	}
	input := "language: " + c.Language + "\npath: " + f.Path + "\nsymbol: " + symbol + "\ncode:\n" + string(b)
	if len(input) > 20000 {
		return Document{}, "", ErrBudget
	}
	d := Document{Chunk: c, Path: f.Path, Symbol: symbol, State: f.State, InputHash: hash([]string{"context-content-v1", input})}
	// symbol, exact identifier channel, path, complete raw body (comments included).
	d.Fields = [4]map[string]int{Tokens(symbol), {}, Tokens(f.Path), Tokens(string(b))}
	for term, n := range d.Fields[3] {
		if strings.HasPrefix(term, "=") {
			d.Fields[1][term] = n
		}
	}
	for i, m := range d.Fields {
		for _, n := range m {
			d.Lengths[i] += n
		}
	}
	return d, input, nil
}
func corpus(docs []Document) Corpus {
	c := Corpus{Count: len(docs)}
	for i := range c.DF {
		c.DF[i] = map[string]int{}
	}
	for _, d := range docs {
		for i, f := range d.Fields {
			c.Totals[i] += d.Lengths[i]
			for t := range f {
				c.DF[i][t]++
			}
		}
	}
	return c
}
func BM25(docs []Document, c Corpus, q string, p Policy) []Candidate {
	terms := Tokens(q)
	keys := make([]string, 0, len(terms))
	for t := range terms {
		keys = append(keys, t)
	}
	sort.Strings(keys)
	out := []Candidate{}
	for _, d := range docs {
		score := 0.0
		for field, w := range p.Weights {
			if c.Totals[field] == 0 {
				continue
			}
			avg := float64(c.Totals[field]) / float64(c.Count)
			for _, t := range keys {
				tf := float64(d.Fields[field][t])
				if tf == 0 {
					continue
				}
				df := float64(c.DF[field][t])
				idf := math.Log(1 + (float64(c.Count)-df+.5)/(df+.5))
				score += w * idf * (tf * (p.K1 + 1)) / (tf + p.K1*(1-p.B+p.B*float64(d.Lengths[field])/avg))
			}
		}
		if score > 0 {
			out = append(out, Candidate{Document: d, LexicalScore: score, Score: score})
		}
	}
	sortCandidates(out)
	for i := range out {
		out[i].LexicalRank = i + 1
	}
	return out
}
func sortCandidates(v []Candidate) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].Score != v[j].Score {
			return v[i].Score > v[j].Score
		}
		return v[i].Document.Chunk.ID < v[j].Document.Chunk.ID
	})
}
func Fuse(lex, dense []Candidate, p Policy) []Candidate {
	byID := map[string]Candidate{}
	for _, v := range lex {
		v.Score = p.ChannelWeights[0] / float64(p.RRF+v.LexicalRank)
		byID[v.Document.Chunk.ID] = v
	}
	for _, v := range dense {
		a, ok := byID[v.Document.Chunk.ID]
		if !ok {
			a = v
			a.Score = 0
		}
		a.DenseRank = v.DenseRank
		a.Distance = v.Distance
		a.Score += p.ChannelWeights[1] / float64(p.RRF+v.DenseRank)
		byID[v.Document.Chunk.ID] = a
	}
	out := make([]Candidate, 0, len(byID))
	for _, v := range byID {
		out = append(out, v)
	}
	sortCandidates(out)
	return out
}
func matches(d Document, f Filter) bool {
	return (f.Path == "" || d.Path == f.Path) && (f.Language == "" || d.Chunk.Language == f.Language) && (f.State == "" || d.State == f.State)
}

// Exact same ranges collapse at build time. Near duplicates are grouped only in
// returned rankings, after scoring with complete immutable corpus statistics.
func diverse(in []Candidate, k int) []Candidate {
	out := []Candidate{}
	for _, v := range in {
		duplicate := false
		for _, a := range out {
			if a.Document.Chunk.File != v.Document.Chunk.File {
				continue
			}
			x, y := a.Document.Chunk.Span, v.Document.Chunk.Span
			overlap := min(x.End, y.End) - max(x.Start, y.Start)
			if overlap > 0 && float64(overlap)/float64(min(x.End-x.Start, y.End-y.Start)) >= .8 {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, v)
			if len(out) == k {
				break
			}
		}
	}
	return out
}
