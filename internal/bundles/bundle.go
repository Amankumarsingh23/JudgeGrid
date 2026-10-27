// Package bundles provides a trusted local, content-addressed development test store.
package bundles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const MaxCases = 16
const MaxBytes = 2 << 20

var ErrIntegrity = errors.New("test bundle integrity/configuration failure")
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var casePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type Case struct {
	ID       string `json:"id"`
	Order    int    `json:"order"`
	Sample   bool   `json:"sample"`
	Input    []byte `json:"input"`
	Expected []byte `json:"expected"`
}
type Bundle struct {
	Schema     int    `json:"schema"`
	Comparator string `json:"comparator"`
	Tests      []Case `json:"tests"`
}
type CaseMeta struct {
	ID           string
	Order        int
	Sample       bool
	InputHash    string
	ExpectedHash string
}
type Reference struct {
	Hash       string
	Comparator string
	Cases      []CaseMeta
}

func Hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (b Bundle) Validate() error {
	if b.Schema != 1 || (b.Comparator != "exact" && b.Comparator != "tokens") || len(b.Tests) < 1 || len(b.Tests) > MaxCases {
		return ErrIntegrity
	}
	ids := map[string]bool{}
	total := 0
	for i, c := range b.Tests {
		if !casePattern.MatchString(c.ID) || ids[c.ID] || c.Order != i+1 || len(c.Input) > 64<<10 || len(c.Expected) > 64<<10 {
			return ErrIntegrity
		}
		ids[c.ID] = true
		total += len(c.Input) + len(c.Expected)
	}
	if total > 1<<20 {
		return ErrIntegrity
	}
	return nil
}
func (b Bundle) Reference() (Reference, error) {
	if err := b.Validate(); err != nil {
		return Reference{}, err
	}
	data, _ := json.Marshal(b)
	ref := Reference{Hash: Hash(data), Comparator: b.Comparator}
	for _, c := range b.Tests {
		ref.Cases = append(ref.Cases, CaseMeta{c.ID, c.Order, c.Sample, Hash(c.Input), Hash(c.Expected)})
	}
	return ref, nil
}
func (b Bundle) Verify(ref Reference) error {
	actual, err := b.Reference()
	if err != nil {
		return err
	}
	if actual.Hash != ref.Hash || actual.Comparator != ref.Comparator || len(actual.Cases) != len(ref.Cases) {
		return ErrIntegrity
	}
	for i, c := range actual.Cases {
		if c != ref.Cases[i] {
			return ErrIntegrity
		}
	}
	return nil
}

type Store interface{ Load(string) (Bundle, error) }
type Local struct{ root string }

func Open(root string) (*Local, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrIntegrity
	}
	return &Local{absolute}, nil
}
func (s *Local) Put(bundle Bundle) (Reference, error) {
	ref, err := bundle.Reference()
	if err != nil {
		return ref, err
	}
	data, _ := json.Marshal(bundle)
	if len(data) > MaxBytes {
		return ref, ErrIntegrity
	}
	path := filepath.Join(s.root, ref.Hash+".json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if errors.Is(err, os.ErrExist) {
		stored, e := s.Load(ref.Hash)
		if e != nil {
			return ref, e
		}
		return ref, stored.Verify(ref)
	}
	if err != nil {
		return ref, err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(path)
		return ref, errors.Join(err, closeErr)
	}
	return ref, nil
}
func (s *Local) Load(hash string) (Bundle, error) {
	var bundle Bundle
	if !hashPattern.MatchString(hash) {
		return bundle, ErrIntegrity
	}
	path := filepath.Join(s.root, hash+".json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > MaxBytes {
		return bundle, ErrIntegrity
	}
	file, err := os.Open(path)
	if err != nil {
		return bundle, ErrIntegrity
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil || len(data) > MaxBytes || Hash(data) != hash {
		return bundle, ErrIntegrity
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&bundle) != nil {
		return bundle, ErrIntegrity
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return bundle, ErrIntegrity
	}
	if err = bundle.Validate(); err != nil {
		return bundle, fmt.Errorf("%w", ErrIntegrity)
	}
	return bundle, nil
}
