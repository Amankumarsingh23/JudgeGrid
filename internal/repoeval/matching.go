package repoeval

import (
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"sort"
)

func Intersection(a, b analysis.Span) int { return max(0, min(a.End, b.End)-max(a.Start, b.Start)) }
func IoU(a, b Ref) (float64, error) {
	if a.Snapshot != b.Snapshot || a.File != b.File {
		return 0, ErrDenied
	}
	if a.Span.End <= a.Span.Start || b.Span.End <= b.Span.Start {
		return 0, ErrInvalid
	}
	n := Intersection(a.Span, b.Span)
	return float64(n) / float64(a.Span.End-a.Span.Start+b.Span.End-b.Span.Start-n), nil
}
func UnionLength(spans []analysis.Span) int {
	v := append([]analysis.Span(nil), spans...)
	sort.Slice(v, func(i, j int) bool { return v[i].Start < v[j].Start })
	n, end := 0, -1
	for _, r := range v {
		if r.End <= r.Start {
			continue
		}
		n += max(0, r.End-max(r.Start, end))
		end = max(end, r.End)
	}
	return n
}
func Coverage(g Ref, results []Ref) float64 {
	spans := []analysis.Span{}
	for _, r := range results {
		if r.Snapshot != g.Snapshot || r.File != g.File {
			continue
		}
		a, z := max(g.Span.Start, r.Span.Start), min(g.Span.End, r.Span.End)
		if z > a {
			spans = append(spans, analysis.Span{Start: a, End: z})
		}
	}
	if g.Span.End <= g.Span.Start {
		return 0
	}
	return float64(UnionLength(spans)) / float64(g.Span.End-g.Span.Start)
}
func MatchGrade(r Ref, q Query, p Policy, exact bool, chunk string) (int, bool) {
	grade, judged := -1, false
	for _, j := range q.Judgments {
		match := false
		if exact {
			for _, id := range j.Ref.ChunkIDs {
				if id == chunk {
					match = true
				}
			}
		} else if r.Snapshot == j.Ref.Snapshot && r.File == j.Ref.File {
			iou, e := IoU(j.Ref, r)
			match = e == nil && Coverage(j.Ref, []Ref{r}) >= p.GoldCoverage && iou >= p.MinIoU
		}
		if match {
			judged = true
			grade = max(grade, j.Grade)
		}
	}
	return grade, judged
}

type Support struct {
	State                string             `json:"state"`
	Covered              int                `json:"covered_groups"`
	Total                int                `json:"required_groups"`
	GroupCoverage        map[string]float64 `json:"group_coverage"`
	RelevantByteFraction float64            `json:"relevant_byte_fraction"`
	GoldCoverage         float64            `json:"known_gold_byte_coverage"`
	Redundancy           float64            `json:"duplicate_byte_fraction"`
}

func EvidenceSupport(q Query, refs []Ref, p Policy) Support {
	s := Support{State: "NONE", Total: len(q.Groups), GroupCoverage: map[string]float64{}}
	byID := map[string]Judgment{}
	for _, j := range q.Judgments {
		byID[j.ID] = j
	}
	unknown := false
	for _, g := range q.Groups {
		if g.Unknown {
			unknown = true
			continue
		}
		best := 0.
		for _, id := range g.Alternatives {
			best = max(best, Coverage(byID[id].Ref, refs))
		}
		s.GroupCoverage[g.ID] = best
		if best >= p.GroupCoverage {
			s.Covered++
		}
	}
	if unknown || len(q.Groups) == 0 {
		s.State = "CANNOT_ASSESS"
	} else if s.Covered == s.Total {
		s.State = "ALL"
	} else if s.Covered > 0 {
		s.State = "SOME"
	}
	selected, gold, intersections := map[string][]analysis.Span{}, map[string][]analysis.Span{}, map[string][]analysis.Span{}
	raw := 0
	for _, r := range refs {
		key := r.Snapshot + "/" + r.File
		selected[key] = append(selected[key], r.Span)
		raw += r.Span.End - r.Span.Start
	}
	for _, j := range q.Judgments {
		if j.Grade < p.Relevant {
			continue
		}
		key := j.Ref.Snapshot + "/" + j.Ref.File
		gold[key] = append(gold[key], j.Ref.Span)
		for _, r := range refs {
			if key != r.Snapshot+"/"+r.File {
				continue
			}
			a, z := max(r.Span.Start, j.Ref.Span.Start), min(r.Span.End, j.Ref.Span.End)
			if z > a {
				intersections[key] = append(intersections[key], analysis.Span{Start: a, End: z})
			}
		}
	}
	unique, goldN, hit := 0, 0, 0
	for _, v := range selected {
		unique += UnionLength(v)
	}
	for _, v := range gold {
		goldN += UnionLength(v)
	}
	for _, v := range intersections {
		hit += UnionLength(v)
	}
	if unique > 0 {
		s.RelevantByteFraction = float64(hit) / float64(unique)
	}
	if goldN > 0 {
		s.GoldCoverage = float64(hit) / float64(goldN)
	}
	if raw > 0 {
		s.Redundancy = 1 - float64(unique)/float64(raw)
	}
	return s
}
