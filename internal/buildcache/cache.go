// Package buildcache coordinates trusted immutable artifact publication. No shared cache is mounted in a workload.
package buildcache

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/objects"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync/atomic"
	"time"
)

var ErrBusy = errors.New("compilation build claim unavailable")

type Key struct {
	Schema                                                                       int
	SourceSHA, Compiler, Toolchain, Flags, Target, Dependencies, Policy, Seccomp string
	Memory, Wall, Output                                                         int64
}

func (k Key) ID() string { b, _ := json.Marshal(k); return objects.Digest(b) }

type Claim struct {
	Key, Owner string
	Generation int
	Artifact   []byte
	Hit        bool
}
type Cache struct {
	Lease                time.Duration // zero selects the normal two-minute bounded build lease
	DB                   *pgxpool.Pool
	Objects              objects.Store
	hits, misses, builds atomic.Int64
}
type Counts struct{ Hits, Misses, Builds int64 }

func (c *Cache) Counts() Counts { return Counts{c.hits.Load(), c.misses.Load(), c.builds.Load()} }

// RecordBuild is called only when the trusted executor actually invokes the compiler.
func (c *Cache) RecordBuild() { c.builds.Add(1) }
func Validate(b []byte) error {
	if len(b) < 64 || len(b) > objects.MaxBytes {
		return objects.ErrIntegrity
	}
	f, e := elf.NewFile(bytes.NewReader(b))
	if e != nil {
		return objects.ErrIntegrity
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 || f.Machine != elf.EM_X86_64 || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) {
		return objects.ErrIntegrity
	}
	return nil
}

// Acquire bounds wait time by caller context, and gives BUILDING claims a fixed two-minute lease.
// READY rows are immutable. Eviction removes metadata eligibility, never a potentially referenced object.
func (c *Cache) Acquire(ctx context.Context, k Key, owner string) (Claim, error) {
	id := k.ID()
	data, _ := json.Marshal(k)
	cl := Claim{Key: id, Owner: owner}
	lease := c.Lease
	if lease == 0 {
		lease = 2 * time.Minute
	}
	if lease < time.Second || lease > 2*time.Minute || len(owner) < 1 || len(owner) > 200 {
		return cl, ErrBusy
	}
	for {
		if e := ctx.Err(); e != nil {
			return cl, e
		}
		var state, digest string
		var generation int
		var size int64
		e := c.DB.QueryRow(ctx, `INSERT INTO compilation_cache(cache_key,key_json,state,generation,owner,lease_expires_at) VALUES($1,$2,'BUILDING',1,$3,clock_timestamp()+$4::bigint*interval '1 millisecond') ON CONFLICT(cache_key) DO UPDATE SET state='BUILDING',generation=compilation_cache.generation+1,owner=EXCLUDED.owner,lease_expires_at=EXCLUDED.lease_expires_at,artifact_sha=NULL,artifact_bytes=NULL,ready_at=NULL WHERE compilation_cache.key_json=EXCLUDED.key_json AND(compilation_cache.state='EVICTED' OR(compilation_cache.state='BUILDING' AND compilation_cache.lease_expires_at<=clock_timestamp())) RETURNING generation`, id, data, owner, lease.Milliseconds()).Scan(&cl.Generation)
		if e == nil {
			c.misses.Add(1)
			return cl, nil
		}
		e = c.DB.QueryRow(ctx, `SELECT state,generation,COALESCE(artifact_sha,''),COALESCE(artifact_bytes,0) FROM compilation_cache WHERE cache_key=$1 AND key_json=$2::jsonb`, id, data).Scan(&state, &generation, &digest, &size)
		if e != nil {
			return cl, e
		}
		if state == "READY" {
			b, e := c.Objects.Get(ctx, "artifacts/"+digest+".bin", objects.MaxBytes)
			if e != nil {
				return cl, e
			}
			if int64(len(b)) != size || objects.Digest(b) != digest || Validate(b) != nil {
				return cl, objects.ErrIntegrity
			}
			cl.Artifact = b
			cl.Generation = generation
			cl.Hit = true
			c.hits.Add(1)
			return cl, nil
		}
		select {
		case <-ctx.Done():
			return cl, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func (c *Cache) Publish(ctx context.Context, cl Claim, b []byte) error {
	if cl.Hit || Validate(b) != nil {
		return objects.ErrIntegrity
	}
	digest := objects.Digest(b)
	if e := c.Objects.Put(ctx, "artifacts/"+digest+".bin", b); e != nil {
		return e
	}
	got, e := c.Objects.Get(ctx, "artifacts/"+digest+".bin", objects.MaxBytes)
	if e != nil {
		return e
	}
	if !bytes.Equal(got, b) {
		return objects.ErrIntegrity
	}
	tag, e := c.DB.Exec(ctx, `UPDATE compilation_cache SET state='READY',artifact_sha=$4,artifact_bytes=$5,ready_at=clock_timestamp() WHERE cache_key=$1 AND owner=$2 AND generation=$3 AND state='BUILDING' AND lease_expires_at>clock_timestamp()`, cl.Key, cl.Owner, cl.Generation, digest, len(b))
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrBusy
	}
	return nil
}
func (c *Cache) Release(ctx context.Context, cl Claim) error {
	if cl.Hit {
		return nil
	}
	_, e := c.DB.Exec(ctx, `UPDATE compilation_cache SET state='EVICTED' WHERE cache_key=$1 AND owner=$2 AND generation=$3 AND state='BUILDING'`, cl.Key, cl.Owner, cl.Generation)
	return e
}
func (c *Cache) Evict(ctx context.Context, key string) error {
	tag, e := c.DB.Exec(ctx, `UPDATE compilation_cache SET state='EVICTED' WHERE cache_key=$1 AND state='READY'`, key)
	if e == nil && tag.RowsAffected() != 1 {
		return fmt.Errorf("cache entry not READY")
	}
	return e
}
