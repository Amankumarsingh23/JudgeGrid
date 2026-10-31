# Trusted repository ingestion

Session 12 adds `cmd/repo-ingest` and `internal/ingest`. It reads approved repositories as data, with no public endpoint, shell command construction, code execution, Docker socket, checkout or dependency installation. All judge behavior and defaults remain independent. See [ADR 0012](adr/0012-commit-pinned-repository-snapshots.md) and the [snapshot schema](repository-snapshot-schema.md).

A Git ref is a movable name. A commit names an immutable root tree; a tree names paths/modes and child trees or blobs. Resolve a requested ref once and read those exact objects. Working-tree edits and later branch movement cannot alter the pinned inventory. Git OIDs include object headers and use the repository's SHA-1 or SHA-256 format; raw SHA-256 hashes only file bytes. Neither is authorization. Source documentation, including prompt-injection text, is data and never an executable instruction.

## Local operator setup

Build with Go 1.26; Linux needs Git and util-linux `prlimit`, Windows needs Git and job-object enforcement. Unsupported enforcement fails closed. `repo-ingest` is intentionally excluded from ordinary service images. The CLI requires all of:

```sh
docker compose -f docker-compose.yml -f infra/local-judge.compose.yml up --no-deps -d --wait postgres
export APP_ENV=development DEV_AUTH_ENABLED=true DEV_PRINCIPAL=repository-owner
export REPO_INGEST_ENABLED=true
export REPO_DATABASE_URL='postgres://judgegrid:judgegrid@127.0.0.1:15432/judgegrid?sslmode=disable&search_path=owned_repository_lab'
go build -o bin/repo-ingest ./cmd/repo-ingest
bin/repo-ingest --allow-development-ingestion --action migrate
```

Create the explicitly named isolated schema beforehand with the trusted database operator. Keep URLs, trust/policy JSON, credentials and evidence in ignored `bin/`. The CLI never silently migrates an ingestion request or chooses a default database. Status/migration do not require object storage. All other source/storage actions require a private trust file such as:

```json
{"local_roots":["/absolute/approved/repositories"],"remote_origins":["https://github.com/Amankumarsingh23/JudgeGrid.git"]}
```

Use absolute Windows paths in Windows JSON. Roots/origins are explicit operator approvals, not content supplied by repositories or HTTP callers. Do not grant broad unrelated roots. Locations outside the approved roots, linked-worktree `.git` files, object alternates/promisors/symlinked object files and unsupported ref storage are refused. The controlled filesystem and Git installation remain trusted; another privileged OS process replacing files concurrently is outside this boundary.

Choose one backend explicitly. For rooted local storage:

```sh
export REPO_STORAGE_BACKEND=local REPO_LOCAL_ROOT=/absolute/private/source-blobs
```

For S3 use **separate source credentials/bucket**, never hidden-test/cache credentials. The optional `infra/repository-ingestion.compose.yml` profile creates a separate MinIO instance, loopback port 19100 and independent `repository-source-data` volume. Use ignored `bin/repository-store.env` containing distinct generated `REPO_MINIO_ROOT_USER` and `REPO_MINIO_ROOT_PASSWORD`:

```sh
docker compose --env-file bin/repository-store.env -f docker-compose.yml -f infra/repository-ingestion.compose.yml --profile repository-storage up --build --no-deps -d --wait repository-store
export REPO_STORAGE_BACKEND=s3 REPO_S3_ENDPOINT=127.0.0.1:19100 REPO_S3_BUCKET=private-repository-sources
export REPO_S3_ACCESS_KEY=your-separate-source-key REPO_S3_SECRET_KEY=your-separate-source-secret
export REPO_S3_TLS=false # explicit isolated loopback development only; TLS is otherwise enabled
bin/repo-ingest --allow-development-ingestion --trust bin/repository-trust.json --action init-storage
```

The bucket must be private and versioned. An unavailable/uninitialized S3 backend never falls back to a local directory. The existing archived MinIO development-image limitation still applies; this is not a production storage recommendation.

## Command contract

Every command below also needs the explicit opt-in flag and trust file. The principal comes from the trusted development identity; a supplied snapshot/repository hash alone never grants access.

```sh
bin/repo-ingest --allow-development-ingestion --trust bin/repository-trust.json --action register --name JudgeGrid --location /absolute/approved/repositories/JudgeGrid
# Use the returned repository UUID:
bin/repo-ingest --allow-development-ingestion --trust bin/repository-trust.json --action ingest --repository UUID --ref 56ea76e421eca19066362fdecedd4e9aae04521e
bin/repo-ingest --allow-development-ingestion --trust bin/repository-trust.json --action status --snapshot SNAPSHOT
bin/repo-ingest --allow-development-ingestion --trust bin/repository-trust.json --action inventory --snapshot SNAPSHOT
bin/repo-ingest --allow-development-ingestion --trust bin/repository-trust.json --action file --snapshot SNAPSHOT --path internal/storage/ownership.go --output bin/verified-ownership.go.txt
bin/repo-ingest --allow-development-ingestion --trust bin/repository-trust.json --action compare --snapshot OLD --other NEW
bin/repo-ingest --allow-development-ingestion --trust bin/repository-trust.json --action retry --repository UUID --ref EXACT_REF
bin/repo-ingest --allow-development-ingestion --trust bin/repository-trust.json --action audit --repository UUID
```

Output files are newly created private files, never overwritten. File/inventory/status output contains authorized provenance, not object keys or credentials. Source bytes are written only to the explicitly requested output file. Errors use bounded codes; untrusted Git stderr is discarded. Status can show a non-READY attempt, but READY inventory/file reads require published authority. Retry waits for the one-second failure backoff or an expired claim and retains the same identity; five attempts is the maximum. Unknown audit objects are always retained.

Use a `.txt` suffix for standalone extracted Go snippets inside the workspace: `go test/vet ./...` traverses packages independently of `.gitignore`, so an isolated `.go` copy can accidentally create an incomplete package. The suffix does not change its verified raw bytes.

Refs are `HEAD`, fully qualified ASCII `refs/heads/...` / `refs/tags/...`, or a full object-format-appropriate commit/tag OID. Arbitrary revision expressions/options are unsupported. Missing requested objects are errors, never substituted with current HEAD. Packed refs and bounded annotated-tag peeling are supported. SHA-256 repositories use 64-character OIDs throughout.

## Approved remote workflow

Register an empty approved mirror directory with the exact allowlisted HTTPS origin. `--object-format sha256` can initialize a SHA-256 mirror when required; otherwise SHA-1 is explicit. Existing nonempty locations are validated and never overwritten by initialization.

```sh
bin/repo-ingest --allow-development-ingestion --trust bin/repository-trust.json --action register --name approved-remote --location /absolute/approved/repositories/remote-mirror --origin https://github.com/Amankumarsingh23/JudgeGrid.git
bin/repo-ingest --allow-development-ingestion --trust bin/repository-trust.json --action fetch-ingest --repository REMOTE_UUID --ref refs/heads/main
# Interrupted remote work must explicitly opt into fetching again:
bin/repo-ingest --allow-development-ingestion --trust bin/repository-trust.json --action retry --remote --repository REMOTE_UUID --ref FULL_COMMIT
```

Only public HTTPS Git origins without URL credentials, explicit ports, query/fragment or encoded path tricks are accepted. Exact URL allowlisting, public-IP checks and pinned TLS dialing prevent destination substitution; redirects and environment proxies are disabled. Discovery and upload-pack are the only allowed paths. No SSH, local transport, external helpers from repository config, submodule or LFS fetch is available. The transport allows 1 MiB request bodies, 32 MiB combined download and a 45-second fetch deadline. Existing mirror objects support Git negotiation; initial remote pack download and reused published source contents are distinct measurements. No remote bandwidth savings are claimed without measuring them.

For private remotes, a trusted operator may prepare an authorized local mirror separately using their own approved credential workflow; register it as local. The ingester itself never loads ambient Git credentials or private SSH configuration.

## Selection, budgets and privacy

`source-v1` supports Go/Python/Java/C/C++/JS/TS, JSON/YAML/TOML/SQL/protobuf/shell, go.mod, Dockerfile and optional bounded Markdown/text. It excludes dependency/build paths, common generated/minified/lock content and sensitive paths such as `.env*`, keys and credentials. Bounded content checks classify NUL/binary, invalid encoding, LFS pointers, generated headers, long minified JS/TS lines, private-key/token/credential evidence. These conservative rules can miss secrets or exclude useful examples; they do not prove source is secret-free. Review approved private content before any later external-model export.

Symlinks are metadata only and never followed; gitlinks are metadata and never recursively fetched. Oversized/disallowed blobs are not read. Invalid UTF-8/non-NFC, traversal, control/bidi characters, Windows device/punctuation aliases and case-fold collisions fail the snapshot closed rather than silently renaming paths. Gitignore is not a secret policy. Raw byte hashes for unread entries are explicitly absent.

Default limits: 10000 tree entries, depth 32, 2000 candidate files, 1 MiB/file, 16 MiB candidate bytes, 1 MiB combined path bytes, one blob reader, 8 MiB manifest and 120 seconds. Hard configurable ceilings: 20000 entries, depth 64, 5000 candidates, 2 MiB/file, 32 MiB total candidate bytes, 2 MiB path bytes and 300 seconds; metadata trees are additionally limited to 2 MiB/commit-tag objects to 256 KiB. The CLI has a 330-second outer deadline and bounded JSON operator inputs. Use `--policy` with the complete versioned fields to select tighter limits; all options enter policy identity. Runtime memory protection is described in ADR 0012; configured limits are not measured peak RSS.

Optional Session 11 telemetry uses service `repository-ingest`, bounded ingestion operations/durations and `judgegrid_ingestion_bytes_total{stage=processed|fetched|reused}`. Source paths, owners, commits, object hashes and contents are not labels/attributes. One-shot CLI metrics exist only during that process; scrape its explicitly chosen loopback `TELEMETRY_ADDR` while it runs. Trace export is bounded and independent of publication. Offline exporter, full queue and missing metrics listener never authorize or invalidate a snapshot. Use durable SQL/manifest evidence for logical accounting across restarts.

## Verification and handoff

Normal/race tests use actual SHA-1/SHA-256 Git fixtures and controlled HTTPS smart Git. Tagged ingestion tests require PostgreSQL, separately credentialed `REPO_TEST_S3_ENDPOINT/ACCESS_KEY/SECRET_KEY`, and `REPO_REAL_PATH` for the exact JudgeGrid baseline. They deliberately crash a helper, fence a takeover, gate real S3 connections, poison an owned object and restore it, test cross-owner denial and verify unknown-object retention. No missing prerequisite is counted as a pass. CI includes full baseline history so the requested Session 11 commit is actually available.

Session 13 should use `ingest.Reader` with the same authorized owner and immutable READY snapshot/file identities, retain raw/Git hashes and exact paths, and treat source text as untrusted. This session implements no Tree-sitter, ASTs, embeddings, BM25, reranking, inference or agent code execution.
