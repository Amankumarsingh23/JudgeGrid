// Package ingest reads authorized Git objects as data. It has no judge or Docker authority.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

var (
	ErrDenied    = errors.New("repository scope unavailable")
	ErrSource    = errors.New("source object unavailable or unsafe")
	ErrBudget    = errors.New("ingestion resource budget exceeded")
	ErrIntegrity = errors.New("snapshot content integrity failure")
	ErrStorage   = errors.New("repository storage unavailable")
	ErrFence     = errors.New("ingestion claim expired or replaced")
	ErrBusy      = errors.New("snapshot ingestion busy or retry not eligible")
)

func Digest(b []byte) string   { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func canonical(v any) []byte   { b, _ := json.Marshal(v); return b }
func identity(v any) string    { return Digest(canonical(v)) }
func stringInt(n int64) string { return strconv.FormatInt(n, 10) }

type Repository struct {
	ID       string `json:"repository_id"`
	Owner    string `json:"-"`
	Name     string `json:"name"`
	Location string `json:"-"`
	Origin   string `json:"origin,omitempty"`
}
type Entry struct {
	ID       string `json:"file_id"`
	Path     string `json:"path"`
	Mode     string `json:"mode"`
	Type     string `json:"type"`
	OID      string `json:"git_oid"`
	Size     int64  `json:"size"`
	SHA256   string `json:"raw_sha256,omitempty"`
	Language string `json:"language"`
	Reason   string `json:"reason"`
}
type Manifest struct {
	Schema     int     `json:"schema"`
	Repository string  `json:"repository_id"`
	Snapshot   string  `json:"snapshot_id"`
	Format     string  `json:"object_format"`
	Commit     string  `json:"commit_oid"`
	Tree       string  `json:"tree_oid"`
	Policy     string  `json:"policy_id"`
	Entries    []Entry `json:"entries"`
}
type Stats struct {
	Discovered     int            `json:"discovered"`
	Included       int            `json:"included"`
	Excluded       int            `json:"excluded"`
	Bytes          int64          `json:"included_bytes"`
	Fetched        int            `json:"git_blobs_fetched"`
	FetchedBytes   int64          `json:"git_bytes_fetched"`
	RemoteBytes    int64          `json:"remote_download_bytes"`
	Reused         int            `json:"verified_blobs_reused"`
	ReusedBytes    int64          `json:"verified_bytes_reused"`
	SourceSeconds  float64        `json:"source_seconds"`
	StorageSeconds float64        `json:"storage_seconds"`
	DBSeconds      float64        `json:"db_seconds"`
	TotalSeconds   float64        `json:"total_seconds"`
	Reasons        map[string]int `json:"reasons"`
}
type Snapshot struct {
	ID         string `json:"snapshot_id"`
	Repository string `json:"repository_id"`
	Format     string `json:"object_format"`
	Commit     string `json:"commit_oid"`
	Tree       string `json:"tree_oid"`
	Policy     string `json:"policy_id"`
	State      string `json:"state"`
	Generation int64  `json:"generation"`
	Manifest   string `json:"manifest_sha256,omitempty"`
	Failure    string `json:"failure_code,omitempty"`
	Stats      Stats  `json:"stats"`
	Replayed   bool   `json:"replayed"`
	Attempt    *Stats `json:"attempt,omitempty"` // current call; Stats remains immutable publication evidence
}
type Claim struct {
	Snapshot Snapshot
	Token    string
	TTL      time.Duration
}
type File struct {
	Snapshot Snapshot
	Entry    Entry
	Bytes    []byte
}

// Reader is the Session 13 boundary. Implementations authorize before fetching bytes,
// return READY provenance only, and verify content on every read.
type Reader interface {
	Ready(context.Context, string, string) (Snapshot, Manifest, error)
	ReadFile(context.Context, string, string, string) (File, error)
}
type BlobStore interface {
	Get(context.Context, string, int64) ([]byte, error)
	Put(context.Context, string, []byte) error
	Inventory(context.Context, string, int) ([]string, error)
}

func snapshotID(repo, format, commit, policy string) string {
	return identity(struct {
		Schema                             int
		Repository, Format, Commit, Policy string
	}{1, repo, format, commit, policy})
}
func fileID(snapshot, path string) string { return identity([]string{"file-v1", snapshot, path}) }
