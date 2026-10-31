package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"sort"
	"sync"
	"time"
)

type Service struct {
	DB      Database
	Objects BlobStore
	Trust   Trust
	Lease   time.Duration
	// Deliberate fault seams are package-private and never accepted by the CLI.
	fault func(stage string, n int)
}
type Options struct {
	Retry       bool
	AllowRemote bool
}

func (s *Service) stage(stage string, n int) {
	if s.fault != nil {
		s.fault(stage, n)
	}
}
func (s *Service) readObject(ctx context.Context, key string, max int64) (b []byte, err error) {
	ctx, end := telemetry.Begin(ctx, "ingest.verify", "")
	defer func() {
		end(err)
		if err != nil {
			telemetry.Count("ingest.fetch_error", "error", "")
			if errors.Is(err, ErrStorage) {
				telemetry.Count("ingest.storage_error", "unavailable", "")
			}
		}
	}()
	return s.Objects.Get(ctx, key, max)
}
func (s *Service) putObject(ctx context.Context, key string, b []byte) (err error) {
	ctx, end := telemetry.Begin(ctx, "ingest.storage", "")
	defer func() {
		end(err)
		if errors.Is(err, ErrStorage) {
			telemetry.Count("ingest.storage_error", "unavailable", "")
		}
	}()
	return s.Objects.Put(ctx, key, b)
}
func failureCode(e error) string {
	switch {
	case errors.Is(e, ErrSource):
		return "source"
	case errors.Is(e, ErrBudget):
		return "budget"
	case errors.Is(e, ErrIntegrity):
		return "integrity"
	case errors.Is(e, ErrStorage):
		return "storage"
	case errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded):
		return "canceled"
	default:
		return "internal"
	}
}
func (s *Service) Ingest(ctx context.Context, owner, repository, ref string, p Policy, opts Options) (result Snapshot, err error) {
	if e := p.Validate(); e != nil {
		return result, e
	}
	if s.Objects == nil {
		return result, ErrStorage
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.MaxSeconds)*time.Second)
	defer cancel()
	ctx, end := telemetry.Begin(ctx, "ingest.total", "")
	defer func() { end(err) }()
	started := time.Now()
	stats := Stats{Reasons: map[string]int{}}
	tick := time.Now()
	r, e := s.DB.Repository(ctx, owner, repository)
	stats.DBSeconds += time.Since(tick).Seconds()
	if e != nil {
		return result, e
	}
	if r.Origin != "" && (!opts.AllowRemote || !s.Trust.Remote(r.Origin)) {
		return result, ErrDenied
	}
	tick = time.Now()
	g, e := OpenGit(ctx, s.Trust, r.Location)
	if e != nil {
		return result, e
	}
	defer g.Close()
	if r.Origin != "" {
		cached := false
		if validOID(ref, g.Format) {
			typ, _, x := g.Info(ref)
			cached = x == nil && (typ == "commit" || typ == "tag")
		}
		if !cached {
			ids, x := s.DB.KnownCommits(ctx, r.ID, g.Format)
			if x != nil {
				return result, x
			}
			if x = g.seedHaves(ids); x != nil {
				return result, x
			}
			ref, e = g.Fetch(ctx, s.Trust, r.Origin, ref)
			if e != nil {
				return result, e
			}
		}
		stats.RemoteBytes = g.RemoteBytes
	}
	commit, tree, e := g.Resolve(ref)
	stats.SourceSeconds += time.Since(tick).Seconds()
	if e != nil {
		return result, e
	}
	s.stage("resolved", 0)
	tick = time.Now()
	snap, created, e := s.DB.Request(ctx, r, g.Format, commit, tree, p)
	stats.DBSeconds += time.Since(tick).Seconds()
	if e != nil {
		return result, e
	}
	if created {
		telemetry.Count("ingest.accepted", "ok", "")
	}
	if snap.State == "READY" {
		ready, m, e := s.Ready(ctx, owner, snap.ID)
		if e == nil {
			e = s.verifyFiles(ctx, m)
		}
		ready.Replayed = true
		stats.Discovered = ready.Stats.Discovered
		stats.Included = ready.Stats.Included
		stats.Excluded = ready.Stats.Excluded
		stats.Bytes = ready.Stats.Bytes
		stats.Reused = ready.Stats.Included
		stats.ReusedBytes = ready.Stats.Bytes
		stats.Reasons = ready.Stats.Reasons
		stats.TotalSeconds = time.Since(started).Seconds()
		ready.Attempt = &stats
		return ready, e
	}
	lease := s.Lease
	if lease == 0 {
		lease = 15 * time.Second
	}
	tick = time.Now()
	claim, e := s.DB.Claim(ctx, owner, snap.ID, lease, opts.Retry)
	stats.DBSeconds += time.Since(tick).Seconds()
	if e != nil {
		return result, e
	}
	if claim.Snapshot.Generation > 1 {
		telemetry.Count("ingest.retry", "ok", "")
	}
	run, stop := context.WithCancelCause(ctx)
	heartbeat, stopHeartbeat := context.WithCancel(run)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		timer := time.NewTicker(lease / 3)
		defer timer.Stop()
		for {
			select {
			case <-heartbeat.Done():
				return
			case <-timer.C:
				check, done := context.WithTimeout(run, lease/3)
				e := s.DB.Renew(check, claim)
				done()
				if e != nil {
					g.cancel()
					stop(ErrFence)
					return
				}
			}
		}
	}()
	defer func() {
		stopHeartbeat()
		wg.Wait()
		if err != nil {
			if errors.Is(context.Cause(run), ErrFence) {
				err = ErrFence
			}
			if errors.Is(err, ErrFence) {
				telemetry.Count("ingest.fenced", "fenced", "")
			}
			cleanup, done := context.WithTimeout(context.Background(), time.Second)
			defer done()
			if s.DB.Fail(cleanup, claim, failureCode(err)) == nil {
				telemetry.Count("ingest.failed", "error", "")
			}
		}
		stop(nil)
	}()
	s.stage("claimed", 0)
	tick = time.Now()
	entries, e := g.Inventory(run, tree, p)
	stats.SourceSeconds += time.Since(tick).Seconds()
	if e != nil {
		return result, e
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	candidates := 0
	var scanned int64
	for i := range entries {
		if run.Err() != nil {
			return result, context.Cause(run)
		}
		f := &entries[i]
		f.ID = fileID(snap.ID, f.Path)
		stats.Discovered++
		if f.Reason == "included" {
			f.Language, f.Reason = selectPath(f.Path, p.Docs)
		}
		if f.Reason == "included" {
			candidates++
			if candidates > p.MaxFiles {
				return result, ErrBudget
			}
			if f.Size > p.MaxFileBytes {
				f.Reason = "oversize"
			} else {
				scanned += f.Size
				if scanned > p.MaxTotalBytes {
					return result, ErrBudget
				}
				tick = time.Now()
				digest, n, reused, e := s.DB.Blob(run, snap, f.OID)
				stats.DBSeconds += time.Since(tick).Seconds()
				if e != nil {
					return result, e
				}
				var body []byte
				if reused {
					if n != f.Size {
						return result, ErrIntegrity
					}
					tick = time.Now()
					body, e = s.readObject(run, blobKey(r.ID, digest), n)
					stats.StorageSeconds += time.Since(tick).Seconds()
					if e != nil {
						return result, e
					}
					if objectOID(g.Format, "blob", body) != f.OID {
						return result, ErrIntegrity
					}
					stats.Reused++
					stats.ReusedBytes += n
				} else {
					tick = time.Now()
					body, e = g.Read(f.OID, "blob", p.MaxFileBytes)
					stats.SourceSeconds += time.Since(tick).Seconds()
					if e != nil {
						return result, e
					}
					stats.Fetched++
					stats.FetchedBytes += int64(len(body))
					digest = Digest(body)
				}
				f.SHA256 = digest
				f.Reason = classify(body, f.Language)
				if reused && f.Reason != "included" {
					return result, ErrIntegrity
				}
				if f.Reason == "included" && !reused {
					tick = time.Now()
					e = s.putObject(run, blobKey(r.ID, digest), body)
					stats.StorageSeconds += time.Since(tick).Seconds()
					if e != nil {
						return result, e
					}
					// Conditional upload and verified readback precede the fenced reuse catalog.
					tick = time.Now()
					e = s.DB.RecordBlob(run, claim, f.OID, digest, int64(len(body)))
					stats.DBSeconds += time.Since(tick).Seconds()
					if e != nil {
						return result, e
					}
					s.stage("uploaded", stats.Fetched)
				}
			}
		}
		stats.Reasons[f.Reason]++
		if f.Reason == "included" {
			stats.Included++
			stats.Bytes += f.Size
		} else {
			stats.Excluded++
		}
	}
	m := Manifest{1, r.ID, snap.ID, g.Format, commit, tree, p.ID(), entries}
	if len(canonical(m)) > MaxManifestBytes {
		return result, ErrBudget
	}
	if e = validateManifest(snap, m); e != nil {
		return result, e
	}
	tick = time.Now()
	if e = s.DB.Verifying(run, claim); e != nil {
		return result, e
	}
	stats.DBSeconds += time.Since(tick).Seconds()
	s.stage("verifying", 0)
	tick = time.Now()
	if e = s.verifyFiles(run, m); e != nil {
		return result, e
	}
	manifest := canonical(m)
	if e = s.putObject(run, manifestKey(r.ID, Digest(manifest)), manifest); e != nil {
		return result, e
	}
	stats.StorageSeconds += time.Since(tick).Seconds()
	stats.TotalSeconds = time.Since(started).Seconds()
	s.stage("publish", 0)
	stopHeartbeat()
	wg.Wait()
	if run.Err() != nil {
		return result, context.Cause(run)
	}
	tick = time.Now()
	if e = s.DB.Publish(run, claim, m, stats); e != nil {
		return result, e
	}
	stats.DBSeconds += time.Since(tick).Seconds()
	telemetry.Count("ingest.completed", "ok", "")
	telemetry.CountN("ingest.files", "discovered", float64(stats.Discovered))
	telemetry.CountN("ingest.files", "included", float64(stats.Included))
	telemetry.CountN("ingest.files", "excluded", float64(stats.Excluded))
	telemetry.IngestionBytes("processed", float64(scanned))
	telemetry.IngestionBytes("fetched", float64(stats.FetchedBytes))
	telemetry.IngestionBytes("reused", float64(stats.ReusedBytes))
	telemetry.Observe("ingest.git", "ok", "", stats.SourceSeconds)
	telemetry.Observe("ingest.storage", "ok", "", stats.StorageSeconds)
	telemetry.Observe("ingest.db", "ok", "", stats.DBSeconds)
	tick = time.Now()
	result, e = s.DB.Status(run, owner, snap.ID)
	stats.DBSeconds += time.Since(tick).Seconds()
	stats.TotalSeconds = time.Since(started).Seconds()
	result.Attempt = &stats
	return result, e
}

func validateManifest(s Snapshot, m Manifest) error {
	if m.Schema != 1 || m.Snapshot != s.ID || m.Repository != s.Repository || m.Format != s.Format || m.Commit != s.Commit || m.Tree != s.Tree || m.Policy != s.Policy || snapshotID(m.Repository, m.Format, m.Commit, m.Policy) != m.Snapshot || len(m.Entries) > 20000 {
		return ErrIntegrity
	}
	last := ""
	seen := map[string]bool{}
	for _, f := range m.Entries {
		if !validPath(f.Path) || f.Path <= last || seen[foldPath(f.Path)] || !validOID(f.OID, m.Format) || f.ID != fileID(m.Snapshot, f.Path) || f.Size < 0 {
			return ErrIntegrity
		}
		last = f.Path
		seen[foldPath(f.Path)] = true
		if f.Reason == "included" && (!validHash.MatchString(f.SHA256) || (f.Mode != "100644" && f.Mode != "100755") || f.Type != "blob") {
			return ErrIntegrity
		}
	}
	return nil
}
func (s *Service) verifyFiles(ctx context.Context, m Manifest) error {
	for _, f := range m.Entries {
		if f.Reason != "included" {
			continue
		}
		b, e := s.readObject(ctx, blobKey(m.Repository, f.SHA256), f.Size)
		if e != nil {
			return e
		}
		if int64(len(b)) != f.Size || objectOID(m.Format, "blob", b) != f.OID {
			return ErrIntegrity
		}
	}
	return nil
}
func (s *Service) Ready(ctx context.Context, owner, id string) (Snapshot, Manifest, error) {
	snap, e := s.DB.Status(ctx, owner, id)
	if e != nil {
		return snap, Manifest{}, e
	}
	if snap.State != "READY" {
		return snap, Manifest{}, ErrBusy
	}
	b, e := s.readObject(ctx, manifestKey(snap.Repository, snap.Manifest), MaxManifestBytes)
	if e != nil {
		return snap, Manifest{}, e
	}
	var m Manifest
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&m); e != nil || !bytes.Equal(b, canonical(m)) {
		return snap, m, ErrIntegrity
	}
	if e = validateManifest(snap, m); e != nil {
		return snap, m, e
	}
	entries, e := s.DB.Files(ctx, owner, id)
	if e != nil {
		return snap, m, e
	}
	if !bytes.Equal(canonical(entries), canonical(m.Entries)) {
		return snap, m, ErrIntegrity
	}
	return snap, m, nil
}
func (s *Service) ReadFile(ctx context.Context, owner, id, path string) (File, error) {
	snap, e := s.DB.Status(ctx, owner, id)
	if e != nil {
		return File{}, e
	}
	if snap.State != "READY" {
		return File{}, ErrBusy
	}
	if !validPath(path) {
		return File{}, ErrDenied
	}
	var f Entry
	e = s.DB.Pool.QueryRow(ctx, `SELECT file_id,path,git_mode,git_type,git_oid,byte_size,raw_sha256,language,reason FROM source_files WHERE snapshot_id=$1 AND path=$2 AND reason='included'`, id, path).Scan(&f.ID, &f.Path, &f.Mode, &f.Type, &f.OID, &f.Size, &f.SHA256, &f.Language, &f.Reason)
	if e != nil {
		return File{}, ErrDenied
	}
	if f.ID != fileID(id, path) {
		return File{}, ErrIntegrity
	}
	b, e := s.readObject(ctx, blobKey(snap.Repository, f.SHA256), f.Size)
	if e != nil {
		return File{}, e
	}
	if int64(len(b)) != f.Size || objectOID(snap.Format, "blob", b) != f.OID {
		return File{}, ErrIntegrity
	}
	return File{snap, f, b}, nil
}

type Change struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

func (s *Service) Compare(ctx context.Context, owner, a, b string) ([]Change, error) {
	_, x, e := s.Ready(ctx, owner, a)
	if e != nil {
		return nil, e
	}
	_, y, e := s.Ready(ctx, owner, b)
	if e != nil {
		return nil, e
	}
	old := map[string]Entry{}
	for _, f := range x.Entries {
		old[f.Path] = f
	}
	changes := []Change{}
	for _, f := range y.Entries {
		v, exists := old[f.Path]
		if !exists {
			changes = append(changes, Change{f.Path, "added"})
		} else {
			v.ID = f.ID
			if !bytes.Equal(canonical(v), canonical(f)) {
				changes = append(changes, Change{f.Path, "modified"})
			}
		}
		delete(old, f.Path)
	}
	for p := range old {
		changes = append(changes, Change{p, "removed"})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

type AuditEntry struct {
	Key       string `json:"-"`
	Reference string `json:"object_reference"`
	Decision  string `json:"decision"`
}

func (s *Service) Audit(ctx context.Context, owner, repo string, max int) ([]AuditEntry, error) {
	if _, e := s.DB.Repository(ctx, owner, repo); e != nil {
		return nil, e
	}
	keys, e := s.Objects.Inventory(ctx, repo, max)
	if e != nil {
		return nil, e
	}
	known := map[string]bool{}
	rows, e := s.DB.Pool.Query(ctx, `SELECT 'repositories/'||repository_id||'/blobs/'||raw_sha256 FROM source_blobs WHERE repository_id=$1 UNION SELECT 'repositories/'||repository_id||'/manifests/'||manifest_sha256||'.json' FROM source_snapshots WHERE repository_id=$1 AND state='READY' LIMIT 20001`, repo)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if e = rows.Scan(&key); e != nil {
			return nil, e
		}
		known[key] = true
		if len(known) > 20000 {
			return nil, ErrBudget
		}
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	result := []AuditEntry{}
	for _, k := range keys {
		decision := "unknown-retain"
		if known[k] {
			decision = "verified-reference-retain"
		}
		result = append(result, AuditEntry{k, Digest([]byte(k)), decision})
	}
	return result, nil
}
