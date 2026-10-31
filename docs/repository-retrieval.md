# Private repository retrieval

Session 14 adds `internal/retrieval` and the trusted opt-in `repo-search` CLI. It consumes an existing authorized READY Session 13 analysis index; it does not ingest HEAD, browse arbitrary repositories or introduce a public API. See [ADR 0014](adr/0014-private-hybrid-retrieval.md), [embedding policy](embedding-policy.md) and [ranking contract](retrieval-ranking.md).

Migration 0016 adds immutable retrieval policies/index manifests, field postings and corpus statistics (inside document/manifest JSON), 384-dimensional pgvector document records and repository-scoped verified embedding cache. The partitioned document table has one HNSW partition per immutable retrieval index. Index identity hashes repository, snapshot, analysis ID/manifest and full tokenizer/model/input/scoring/ranking/budget policy. Every vector carries its document's chunk/range/source hash, canonical embedding input hash and vector hash through the immutable manifest. Migration checksums 0001..0015 remain unchanged.

The PostgreSQL image extends the **exact existing PostgreSQL 17 Alpine base digest**, compiling pgvector **0.8.2** from pinned source commit `cab9da72c04353f143bb06b42ab70a403daac64a` and archive SHA-256. It preserves the database major version, libc/base distribution and volume layout. Build with `docker compose build postgres`; back up existing private data before replacing its container. `docker compose up --build` provisions the extension-capable image. Migration fails if the installed extension differs from the supported pin. The extension is shared in `public`; application tables remain in their isolated schema. No judge table/schema behavior is replaced.

Lifecycle is `PENDING -> BUILDING -> VERIFYING -> READY`, with explicit FAILED/one-second retry delay and at most five attempts. Claims have database-clock expiry, random tokens, monotonic generations and renewals. Cache writes require the active generation; every cached vector is finite, normalized, correctly dimensioned and hash verified. A crashed builder can reuse verified vectors, but no partial index is searchable. The final freshly renewed transaction stages bounded batches, creates the isolated HNSW partition/index, validates source-analysis identity/chunk accounting/corpus/vector/manifest integrity and publishes READY atomically. READY and cache rows cannot be mutated by ordinary code. Privileged backend corruption is detected by verified reads; automatic repair is not claimed.

All authoritative status, manifest and similarity queries join the immutable repository owner. Search requires repository UUID, snapshot ID and retrieval index ID to agree. Denied searches cannot fetch foreign filenames, scores, vector presence, cache statistics or source bytes. Fixed telemetry operation labels contain none of the owner/path/query/hash/content/model inputs. Collector availability does not affect correct ranking. Ordinary API/worker/relay images have no parser/model/search binary or source credentials.

Build the trusted tool using `go build -o repo-search ./cmd/repo-search`. Require `--allow-development-retrieval`, `REPO_RETRIEVAL_ENABLED=true`, `APP_ENV=development`, `DEV_AUTH_ENABLED=true`, fixed `DEV_PRINCIPAL`, explicit `REPO_DATABASE_URL` and independent `REPO_STORAGE_BACKEND` configuration. For build/dense/hybrid/compare set absolute `REPO_EMBEDDING_PYTHON` and `REPO_EMBEDDING_MODEL`; BM25, metadata and verified reads do not require model inference. Local and source-S3 storage follow Session 12's independent credential boundary.

```sh
repo-search --allow-development-retrieval --action policy
repo-search --allow-development-retrieval --action migrate
repo-search --allow-development-retrieval --action build --analysis <authorized-ready-analysis>
repo-search --allow-development-retrieval --action retry --analysis <interrupted-analysis>
repo-search --allow-development-retrieval --action status --index <retrieval-index>
repo-search --allow-development-retrieval --action manifest --index <retrieval-index>
repo-search --allow-development-retrieval --action verify --index <retrieval-index>
repo-search --allow-development-retrieval --action bm25 --index <retrieval-index> --query Repository.renewExecution
repo-search --allow-development-retrieval --action dense --index <retrieval-index> --query 'where is the execution lease renewed?'
repo-search --allow-development-retrieval --action hybrid --index <retrieval-index> --query 'how are stale workers fenced?'
repo-search --allow-development-retrieval --action compare --index <retrieval-index> --query 'where is the outbox event published?'
repo-search --allow-development-retrieval --action dense --ann --index <retrieval-index> --query lease --file internal/storage/ownership.go
repo-search --allow-development-retrieval --action chunk --index <retrieval-index> --chunk <ranked-id> --output private-source.txt
```

Policy JSON is complete/bounded and can change allowed weights/ranking/budgets under a new index identity. Unsupported model/tokenizer/input substitutions are rejected, with no silent fallback. Source export exclusively creates a new private `.txt` file; it never overwrites files. The typed `retrieval.Reader` accepts explicit owner and scoped `Request`, returns ranked provenance/channel scores, and exposes verified chunk reads. `Verify` additionally rebuilds token fields/input identities from verified source. Session 15 may consume these candidates for reranking and compact context assembly; neither is implemented here.

Lexical maps intentionally distinguish case-sensitive keys such as `=AND` and `=and`. Use a case-sensitive JSON reader (Go, Python, JavaScript or jq) for full manifests/results; Windows PowerShell 5 `ConvertFrom-Json` cannot represent these maps. CLI stdout is ordinary UTF-8 JSON; PowerShell 5 redirection can write UTF-16, so native subprocess capture is preferable for automation.
