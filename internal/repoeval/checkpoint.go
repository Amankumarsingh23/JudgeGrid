package repoeval

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

type Checkpoint struct {
	Schema    int           `json:"schema"`
	Benchmark string        `json:"benchmark_sha256"`
	Policy    string        `json:"policy_sha256"`
	Owner     string        `json:"owner"`
	State     string        `json:"state"`
	Rows      []Observation `json:"rows"`
	Attempts  []Observation `json:"prior_failed_attempts"`
	Fresh     []Fresh       `json:"fresh"`
	FreshPlan []string      `json:"fresh_query_plan"`
	Digest    string        `json:"payload_sha256"`
}

func (c Checkpoint) ID() string { c.Digest = ""; return digest(c) }
func ReadJSON(path string, max int64, v any) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() > max {
		return ErrInvalid
	}
	d := json.NewDecoder(io.LimitReader(f, max+1))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func WriteExclusive(path string, v any) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	e = json.NewEncoder(f).Encode(v)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	return closeErr
}
func LoadCheckpoint(dir string, b Benchmark) (Checkpoint, error) {
	c := Checkpoint{Schema: 1, Benchmark: b.ID(), Policy: b.Policy.ID(), Owner: b.Scope.Owner, State: "INCOMPLETE", Rows: []Observation{}, Fresh: []Fresh{}}
	e := ReadJSON(filepath.Join(dir, "checkpoint.json"), 64<<20, &c)
	if os.IsNotExist(e) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	if c.Schema != 1 || c.Benchmark != b.ID() || c.Policy != b.Policy.ID() || c.Owner != b.Scope.Owner || c.Digest != c.ID() || len(c.Rows) > len(b.Queries)*len(Variants) || len(c.Fresh) > 36 {
		return c, ErrIntegrity
	}
	seen := map[string]bool{}
	queries := map[string]bool{}
	variants := map[string]bool{}
	for _, q := range b.Queries {
		queries[q.ID] = true
	}
	for _, v := range Variants {
		variants[v] = true
	}
	for _, r := range c.Rows {
		key := r.Query + "/" + r.Variant
		if !queries[r.Query] || !variants[r.Variant] || seen[key] || len(r.Items) > 50 || r.Attempt < 1 || r.Attempt > 3 {
			return c, ErrIntegrity
		}
		seen[key] = true
	}
	if len(c.Attempts) > len(b.Queries)*len(Variants)*2 {
		return c, ErrIntegrity
	}
	attemptSeen := map[string]bool{}
	for _, r := range c.Attempts {
		key := r.Query + "/" + r.Variant + "/" + string(rune(r.Attempt+'0'))
		if !queries[r.Query] || !variants[r.Variant] || r.Attempt < 1 || r.Attempt > 2 || r.Error == "" || attemptSeen[key] {
			return c, ErrIntegrity
		}
		attemptSeen[key] = true
	}
	if (c.State == "COMPLETE" || c.State == "COMPLETE_WITH_ERRORS") && len(c.Rows) != len(b.Queries)*len(Variants) {
		return c, ErrIntegrity
	}
	if len(c.FreshPlan) > 3 {
		return c, ErrIntegrity
	}
	plans := map[string]bool{}
	for _, id := range c.FreshPlan {
		if !queries[id] || plans[id] {
			return c, ErrIntegrity
		}
		plans[id] = true
	}
	freshSeen := map[string]bool{}
	for _, f := range c.Fresh {
		key := f.Query + "/" + f.Variant + "/" + f.Attempt
		if !plans[f.Query] || !variants[f.Variant] || freshSeen[key] || (f.Attempt != "first-process-cold" && f.Attempt != "repeat-process-cold") {
			return c, ErrIntegrity
		}
		freshSeen[key] = true
	}
	if (c.State == "COMPLETE" || c.State == "COMPLETE_WITH_ERRORS") && len(c.Fresh) != len(c.FreshPlan)*12 {
		return c, ErrIntegrity
	}
	c.State = "INCOMPLETE"
	return c, nil
}
func SaveCheckpoint(dir string, c Checkpoint) error {
	if !filepath.IsAbs(dir) {
		return ErrInvalid
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	c.Digest = c.ID()
	f, e := os.CreateTemp(dir, ".evaluation-*.json")
	if e != nil {
		return e
	}
	path := f.Name()
	defer os.Remove(path)
	if e = f.Chmod(0600); e == nil {
		e = json.NewEncoder(f).Encode(c)
	}
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
	return os.Rename(path, filepath.Join(dir, "checkpoint.json"))
}
