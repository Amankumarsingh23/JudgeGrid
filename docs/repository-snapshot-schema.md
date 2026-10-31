# Repository snapshot identities and publication

Migration `0014_repository_snapshots.sql` adds an independent schema domain; migrations 0001–0013 retain their exact bytes.

| Table | Authority / immutability |
| --- | --- |
| `source_repositories` | UUID, exact owner/name, approved local location and optional HTTPS origin; registration is idempotent, rebinding/update/delete refused |
| `ingestion_policies` | SHA-256 of canonical typed policy; immutable selection/scanner version, docs option and all budgets |
| `source_snapshots` | Logical identity, full commit/root-tree OIDs, object format, state, generation/token/lease, attempts/retry/failure, immutable READY manifest digest and publication statistics |
| `source_blobs` | Verified repository+policy+object-format+Git-OID → raw SHA-256/size catalog; immutable, no cross-repository/tenant reuse |
| `source_files` | Complete published inventory, snapshot/path primary key and stable file ID; mode/type/OID/size/language/reason/optional raw digest; insert only under live VERIFYING authority, immutable thereafter |

State progression is PENDING → INGESTING → VERIFYING → READY. An attempt can fail to FAILED with a bounded reason and a one-second retry delay; explicit retry is required. INGESTING/VERIFYING can be reclaimed only after database-clock lease expiry. Each claim increments generation and attempt count, assigns a new UUID token and has a maximum of five attempts. The default lease is 15 seconds with renewal every third of the lease. Authority uncertainty cancels local work. An expired/stale owner cannot renew, add verified catalog records or publish.

Publication locks the current claim, checks database time/generation/token, inserts every manifest entry and transitions to READY in one transaction. SQL guards check complete row/included/excluded/byte counts and matching verified blob references. A failed transaction rolls back the whole inventory. Publication references, repository bindings, policies and blob mappings cannot subsequently change. A privileged database administrator remains trusted, as elsewhere in JudgeGrid.

Identity definitions are versioned canonical JSON, serialized by fixed Go struct field order and compact `encoding/json` escaping:

```text
policy_id = SHA256(canonical Policy)
snapshot_id = SHA256({Schema:1, Repository:UUID, Format, Commit, Policy:policy_id})
file_id = SHA256(["file-v1", snapshot_id, exact_git_path])
manifest_sha256 = SHA256(canonical Manifest)
raw_sha256 = SHA256(exact raw file bytes)
Git OID = repository hash algorithm(type + " " + decimal_size + NUL + raw bytes)
```

The manifest contains schema, repository UUID, snapshot ID, object format, commit OID, root tree OID, policy ID and entries sorted by exact UTF-8 bytes. Every entry includes file ID, path, raw Git mode/type/OID, byte size, language and reason. A raw SHA-256 is present for examined content; it is absent for entries deliberately not read (directories, gitlinks, symlinks, disallowed paths/types and oversized blobs). Absence is unknown/unread, not a claimed digest. Directory rows are explicit `directory` exclusions; `discovered` counts all tree entries, not just regular files.

READY statistics describe the successful publication attempt. CLI `attempt` describes the current invocation, including a READY replay's zero new Git blob reads and verified reused file references. Reuse/fetch counts count file-content references, so two equal-content paths can contribute a first fetch and a reuse. Fetch byte counts include bounded candidates later excluded by content scanning. Replayed publication `stats` are intentionally historical. Identity/manifest bytes contain no timestamps, timings, retry counters or reuse counts.

Service phase timings use monotonic intervals. Persisted publication stats are captured before the final commit; current-call attempt totals/DB timings include commit/status return. READY replay performs mixed DB/storage verification, so its phase fields are partial and its full total/caller wall time is the reliable comparison. Linux `-race` and native Windows measurements are separate populations. No peak RSS/CPU usage or production throughput is inferred from configured limits.

`ingest.Reader` offers `Ready(ctx, owner, snapshotID)` and `ReadFile(ctx, owner, snapshotID, exactPath)`. READY loads verify canonical manifest digest/header and equality with the immutable DB inventory. File reads authorize against the READY owner scope, fetch the selected file only, and verify raw digest, byte size and Git blob OID. Metadata remains available when source Git is offline; storage failures/corruption fail reads closed. READY is immutable publication provenance, not a promise that a storage administrator can never remove an object.

Objects are conditionally created under private repository-scoped blob/manifest namespaces. The local adapter publishes verified temporary files with a non-overwriting hard link; the S3 adapter requires versioning, conditional create and verified readback. Staged bytes and catalog rows can survive interruption and support recovery. Audit decisions are verified-reference-retain or unknown-retain; CLI JSON emits an opaque hash of the key, never an S3 key. No automatic cleanup or cross-tenant hash-existence probe is provided.
