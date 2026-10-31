package analysis

import (
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"sort"
	"unicode/utf8"
)

func span(b []byte, start, end int) Span {
	s := Span{Start: start, End: end, StartLine: 1, EndLine: 1}
	for i := 0; i < end; i++ {
		if i < start {
			if b[i] == '\n' {
				s.StartLine++
				s.StartColumn = 0
			} else {
				s.StartColumn++
			}
		}
		if b[i] == '\n' {
			s.EndLine++
			s.EndColumn = 0
		} else {
			s.EndColumn++
		}
	}
	return s
}
func makeChunks(index string, f File, b []byte, symbols []Symbol, p Policy) ([]Chunk, error) {
	out := []Chunk{}
	covered := [][2]int{}
	add := func(start, end int, kind, symbol, parent string) error {
		for start < end {
			stop := start + p.ChunkBytes
			if stop > end {
				stop = end
			}
			for stop > start && stop < len(b) && !utf8.RuneStart(b[stop]) {
				stop--
			}
			if stop < end {
				for k := stop; k > start+p.ChunkBytes/2; k-- {
					if b[k-1] == '\n' {
						stop = k
						break
					}
				}
			}
			c := Chunk{File: f.ID, Symbol: symbol, Parent: parent, Kind: kind, Language: f.Language, Span: span(b, start, stop), SHA256: ingest.Digest(b[start:stop])}
			c.ID = hash([]any{"chunk-v1", index, f.ID, symbol, kind, start, stop, c.SHA256})
			out = append(out, c)
			if len(out) > p.MaxChunks {
				return ErrBudget
			}
			if stop == end {
				break
			}
			next := stop - p.Overlap
			for next < stop && !utf8.RuneStart(b[next]) {
				next++
			}
			if next <= start {
				next = stop
			}
			start = next
		}
		return nil
	}
	for _, s := range symbols {
		kind := "symbol"
		if s.Span.End-s.Span.Start > p.ChunkBytes {
			kind = "symbol_part"
		}
		if e := add(s.Span.Start, s.Span.End, kind, s.ID, s.Parent); e != nil {
			return nil, e
		}
		covered = append(covered, [2]int{s.Span.Start, s.Span.End})
	}
	sort.Slice(covered, func(i, j int) bool { return covered[i][0] < covered[j][0] })
	pos := 0
	for _, r := range covered {
		if r[0] > pos {
			if e := add(pos, r[0], "file_region", "", ""); e != nil {
				return nil, e
			}
		}
		if r[1] > pos {
			pos = r[1]
		}
	}
	if pos < len(b) {
		if e := add(pos, len(b), "file_region", "", ""); e != nil {
			return nil, e
		}
	}
	return out, nil
}

type unit struct {
	file    File
	bytes   []byte
	facts   Facts
	symbols []Symbol
}

func materialize(index string, f File, b []byte, facts Facts, p Policy) (unit, []Chunk, error) {
	u := unit{file: f, bytes: b, facts: facts}
	for i, s := range facts.Symbols {
		parent, qualified := "", s.Name
		if s.Parent >= 0 {
			parent = u.symbols[s.Parent].ID
			qualified = u.symbols[s.Parent].Qualified + "." + s.Name
		} else if s.Receiver != "" {
			qualified = s.Receiver + "." + s.Name
		} else if facts.Package != "" {
			qualified = facts.Package + "." + s.Name
		}
		v := Symbol{File: f.ID, Kind: s.Kind, Name: s.Name, Qualified: qualified, Parent: parent, Receiver: s.Receiver, ReceiverVar: s.ReceiverVar, Export: s.Export, State: "PARSED", Span: span(b, s.Start, s.End), NameSpan: span(b, s.NameStart, s.NameEnd), SHA256: ingest.Digest(b[s.Start:s.End])}
		v.Scope = span(b, s.ScopeStart, s.ScopeEnd)
		if s.Partial {
			v.State = "PARTIAL"
		}
		v.ID = hash([]any{"symbol-v1", index, f.ID, i, v.Kind, v.Qualified, s.Start, s.End})
		u.symbols = append(u.symbols, v)
	}
	chunks, e := makeChunks(index, f, b, u.symbols, p)
	return u, chunks, e
}
