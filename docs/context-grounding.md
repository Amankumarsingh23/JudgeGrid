# Grounded evidence and operator boundary

Repository README, comments, tests and code are untrusted data. They may contain apparent instructions, JSON actions or hostile text. The context pipeline stores such material only as quoted `source` string values. It has no execution/tool-dispatch interface, does not follow repository instructions and does not generate answers. A future answering component must preserve this trust boundary; structured quoting alone is not a guarantee against future LLM prompt injection.

Every build starts with authoritative PostgreSQL owner authorization through the Session 14 reader. It checks repository/snapshot/analysis/retrieval identity before candidate disclosure. It reads the actual authorized Session 13 manifest for structural proposals and verifies its digest/scope. Only resolved local CALLS, CONTAINS, RECEIVER_OF and local IMPORTS can be traversed, bidirectionally with cycle/depth/fan-out/node/edge/global-addition limits. Candidate, unresolved and external relationships are never upgraded to proven runtime calls. Every reason retains its seed, exact edge ID/type/class/span and depth.

Graph neighbors must map to documents already present in the exact retrieval index. Duplicate ranges and source budget are checked before bounded batch reads. Actual source bytes are loaded through the authorized immutable readers; UTF-8, raw spans, file/chunk hashes and final cropped spans are rechecked. Integrity failure yields an explicit empty failure outcome rather than a source summary. No model outage substitutes another model. Telemetry availability is independent of source authority.

Build `go build -o repo-context ./cmd/repo-context`. Require all of: `--allow-development-context`, `REPO_CONTEXT_ENABLED=true`, development app/auth mode, fixed DEV_PRINCIPAL, explicit REPO_DATABASE_URL and the existing independent source-storage configuration. Set absolute REPO_EMBEDDING_PYTHON/REPO_EMBEDDING_MODEL for candidate queries and REPO_CONTEXT_PYTHON/REPO_CONTEXT_MODEL for reranking/tokenization. Root, bucket and credentials remain operator controlled. No public repository question-answering endpoint is added.

```sh
repo-context --allow-development-context --action policy
repo-context --allow-development-context --action pack --index <ready-index> --query 'where is the execution lease renewed?' --output private-pack.json
repo-context --allow-development-context --action rerank --index <ready-index> --query Repository.renewExecution
repo-context --allow-development-context --action graph --index <ready-index> --query 'where is the outbox event published?'
repo-context --allow-development-context --action tokens --index <ready-index> --query lease --tokens 2048
repo-context --allow-development-context --action citations --index <ready-index> --query cache --intent implementation
repo-context --allow-development-context --action compare --index <ready-index> --query 'where is the compilation cache populated?'
repo-context --allow-development-context --action verify --input private-pack.json
```

Exports create a new `.json` file exclusively with private permissions; they never overwrite files. Comparison runs hybrid-only packing and the reranked/expanded policy on the same exact index. Each action exposes status; `rerank`, `graph` and `citations` wrap their diagnostics with status/scope. Private query/evidence exports, weights and learning notes remain ignored. CI explicitly hands the verified Session 14 fixture to Session 15 so it does not re-ingest/re-embed merely to build context, then cleans up that isolated fixture. The real local demonstration uses the retained index unchanged.

Session 16 should evaluate candidate recall, rank quality and evidence coverage on a controlled query set. Five requested observations and toy arithmetic tests are not such a benchmark. Session 15 implements no cross-owner semantic cache, answer generation, context-driven tools or autonomous code changes.
