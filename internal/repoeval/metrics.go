package repoeval

import (
	"math"
	"math/rand"
	"sort"
)

type Metric struct {
	K                int      `json:"k"`
	Returned         int      `json:"returned"`
	Judged           int      `json:"judged"`
	KnownPositives   int      `json:"known_positives"`
	PrecisionLower   float64  `json:"precision_lower"`
	PrecisionUpper   float64  `json:"precision_upper"`
	JudgedPrecision  *float64 `json:"judged_precision"`
	JudgmentCoverage float64  `json:"judgment_coverage"`
	Recall           *float64 `json:"known_positive_recall"`
	Hit              *float64 `json:"hit_lower"`
	MRR              *float64 `json:"mrr_lower"`
	AP               *float64 `json:"ap_known_zero_gain"`
	DCG              float64  `json:"dcg_lower"`
	NDCG             *float64 `json:"ndcg_pooled_zero_gain"`
}

func ptr(v float64) *float64 { return &v }

// grades use -1 for unjudged. Missing results occupy empty slots in P@K.
// Repeated evidence entities keep a rank slot but cannot earn gain twice.
func Metrics(grades []int, entities []string, gold map[string]int, k, threshold int) Metric {
	m := Metric{K: k}
	positive := 0
	ideal := []int{}
	for _, g := range gold {
		if g >= threshold {
			positive++
		}
		ideal = append(ideal, g)
	}
	m.KnownPositives = positive
	seen := map[string]bool{}
	hits, unjudged := 0, 0
	rr, ap := 0., 0.
	for i := 0; i < min(k, len(grades)); i++ {
		m.Returned++
		g := grades[i]
		if g < 0 {
			unjudged++
			continue
		}
		m.Judged++
		id := entities[i]
		if seen[id] {
			continue
		}
		seen[id] = true
		m.DCG += (math.Exp2(float64(g)) - 1) / math.Log2(float64(i+2))
		if g >= threshold {
			hits++
			if rr == 0 {
				rr = 1 / float64(i+1)
			}
			ap += float64(hits) / float64(i+1)
		}
	}
	m.PrecisionLower = float64(hits) / float64(k)
	m.PrecisionUpper = float64(hits+unjudged) / float64(k)
	if m.Judged > 0 {
		m.JudgedPrecision = ptr(float64(hits) / float64(m.Judged))
	}
	if m.Returned > 0 {
		m.JudgmentCoverage = float64(m.Judged) / float64(m.Returned)
	}
	if positive > 0 {
		m.Recall = ptr(float64(hits) / float64(positive))
		m.Hit = ptr(0)
		if hits > 0 {
			m.Hit = ptr(1)
		}
		m.MRR = ptr(rr)
		m.AP = ptr(ap / float64(positive))
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ideal)))
	idcg := 0.
	for i := 0; i < min(k, len(ideal)); i++ {
		idcg += (math.Exp2(float64(ideal[i])) - 1) / math.Log2(float64(i+2))
	}
	if positive > 0 && idcg > 0 {
		m.NDCG = ptr(m.DCG / idcg)
	}
	return m
}

type Interval struct {
	N        int     `json:"n"`
	Mean     float64 `json:"mean"`
	Low      float64 `json:"low_95"`
	High     float64 `json:"high_95"`
	Unstable bool    `json:"small_sample_unstable"`
}

func Bootstrap(values []float64, seed int64, repeats int) Interval {
	out := Interval{N: len(values), Unstable: len(values) < 8}
	if len(values) == 0 {
		return out
	}
	for _, v := range values {
		out.Mean += v
	}
	out.Mean /= float64(len(values))
	rng := rand.New(rand.NewSource(seed))
	samples := make([]float64, repeats)
	for i := range samples {
		for range values {
			samples[i] += values[rng.Intn(len(values))]
		}
		samples[i] /= float64(len(values))
	}
	sort.Float64s(samples)
	quantile := func(p float64) float64 {
		at := p * float64(len(samples)-1)
		left := int(at)
		right := min(left+1, len(samples)-1)
		return samples[left] + (samples[right]-samples[left])*(at-float64(left))
	}
	out.Low, out.High = quantile(.025), quantile(.975)
	return out
}
