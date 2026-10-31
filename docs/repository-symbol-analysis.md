# Private repository symbol analysis

Session 13 consumes `ingest.Reader.Ready` and `ReadFile`, after authoritative repository-owner authorization. It never reads Git, a moving branch, the working tree, external dependencies or source instructions for analysis input. The retained Session 12 local snapshot and source manifest remain the input identities. Source credentials and source storage are separate from judge bundles/cache.

## Trusted setup

The Go controller builds with `CGO_ENABLED=0`; native grammar compilation is unnecessary on supported platforms. Install CPython **3.13**, create an operator-controlled virtual environment, and install:

```sh
python -m pip install --only-binary=:all: --require-hashes -r internal/analysis/helper/requirements.lock
```

Runtime pins: Tree-sitter **0.26.0**, Go/Python/JavaScript grammars **0.25.0**, TypeScript/TSX grammar **0.23.2**. Windows AMD64 and Linux x86_64 have pinned wheel/module hashes; other platforms fail closed. Tests require `REPO_ANALYSIS_PYTHON` pointing to the absolute environment interpreter; missing prerequisites are failures, not skips. For Linux tooling, build `docker build -f infra/analysis/Dockerfile -t judgegrid-analysis-tools .`. An optional trusted `local_ca` build secret supports a local certificate authority without disabling TLS.

| Source language | Real grammar | Extracted structure |
| --- | --- | --- |
| Go | Go | package, functions, pointer/value receiver methods, struct/interface/named type/alias, constants/variables, imports, calls, candidate type references |
| Python | Python | classes, methods, nested functions, explicit imports, calls, lexical bindings |
| JavaScript / JSX | JavaScript | named functions/classes/methods, named function-valued variables, static imports, direct exports, calls |
| TypeScript | TypeScript | JS structure plus interfaces, named types/enums |
| `.tsx` | TSX | TypeScript declarations with JSX syntax |
| C/C++/Java/config/docs/other eligible text | unsupported | bounded verified file-region chunks only |
| symlink/gitlink/LFS/dependency/generated/sensitive/other excluded entry | excluded | metadata only; no content read, parsing or chunks |

## Syntax and resolution contract

A CST preserves concrete syntax, punctuation and error nodes. The symbol table records selected named declarations, lexical parent, receiver/class owner, visibility evidence and exact spans. Anonymous expressions are not given fake named symbols. Go interface member signatures are retained within the interface span, not emitted as implemented receiver methods. Python exports are naming conventions, and direct JS exports are syntax evidence; this is not a complete language export table.

Symbol IDs hash index/file identity, ordered local declaration, kind, qualified name and offsets. Short names can repeat. Qualified names show lexical/receiver context; file and symbol IDs supply uniqueness. A declaration also retains its lexical scope span; block-local declarations cannot resolve calls outside that span. Parent and receiver relationships are separate: a Go method can belong to a type declared in another file of the same exact package/directory. Calls bound to variables or type conversions remain candidates, since callable value inference is outside scope.

Calls preserve the exact syntactic target and byte span. `resolved` means one declaration bound under the documented syntactic assumptions, not verified runtime behavior. `candidate` retains ambiguity/partial-syntax or dynamic-dispatch evidence. `unresolved` retains unknown, shadowed or unsupported syntax. `external` requires explicit nonlocal import evidence. A resolved module import can still have an unresolved call.

Go resolution uses the exact directory plus package and the module path read from verified `go.mod`. Imports match only that module's exact directory prefix. Aliases and parameter/variable shadowing are respected conservatively; dot/blank imports are unresolved. Simple local struct receiver type syntax can bind a method; unknown/chained receivers, constructors and interface dispatch are not inferred. Build tags/platform selection are not evaluated; duplicate declarations remain candidates. Qualified external type names are not connected to local types with the same short name. Go local type references are candidate evidence, because generics and full type checking are outside scope.

Python resolves lexical named declarations and explicit relative imports to exact `.py`/`__init__.py` inventory paths. Class bodies are not treated as method closure scopes. `self`/`cls` calls are candidates, and arbitrary attributes remain unresolved. JS/TS supports static relative imports with explicit bounded extension/index candidates and direct exported declarations; multiple possible files remain candidates. Reexports, star expansion, path aliases, package manifests, inference of variable value types, monkey patching, decorators and dynamic import behavior are not followed. Matching a name in an unrelated directory never creates a resolved edge.

Relations are `CONTAINS`, `RECEIVER_OF`, `IMPORTS`, `CALLS` and candidate Go `REFERENCES`. Every row retains source positions. Source text, paths, symbol names, IDs and hashes are never telemetry labels or span attributes. Only fixed operation labels (`analysis.total`, `analysis.parse`, `analysis.completed`) are emitted; exporter availability is not a publication dependency.

## Bounds and lifecycle

Default limits: 1 MiB/file, 32 MiB/index, 3 seconds/parser invocation, 200,000 CST nodes/depth 256, 2,000 symbols, 5,000 calls, 1,024 imports, 5,000 type references, 2,000 chunks/file, 150,000 persisted rows, 20,000 inventory entries, 300 seconds/index, one file at a time, database batches of at most 128. Chunk size is 8,192 bytes with up to 128 bytes overlap. Policy validation caps every configurable limit. File-local bindings/facts also count toward a conservative index memory/row budget. Helper output is capped at 8 MiB. Graph construction checks cancellation and the row budget; no unbounded global name search is used.

`PENDING → ANALYZING → VERIFYING → READY` uses database-clock leases (normally 30 seconds), renewals and generation/token fencing on progress/cache/publication writes. Failed work is `FAILED` with a bounded code and a one-second retry delay, at most five claims. A dead process leaves no public partial index; expired takeover reconstructs it and can reuse already verified facts. In-memory construction and validation precede a single atomic bounded publication transaction. `VERIFYING` is a transactional publication phase, so another client sees the old analyzing state until commit. File-attempt rows are diagnostic and never a READY reader source.

Publication rechecks the source inventory, every file's status, valid references and ranges, full eligible-byte chunk coverage, symbol coverage and canonical manifest hash. Immutable tables retain policies, indexes, file statuses, symbols, imports, calls, edges, chunks and repository-scoped cache facts. Cache rows contain syntax metadata only. Cache identity includes raw SHA-256, grammar variant, policy and extractor identity; cached spans/name/target substrings are checked against freshly verified bytes. Cross-file relationships are always recomputed for a new snapshot. A READY repeat verifies the immutable source and analysis manifest and returns the same index; it does not reparse.

PARTIAL is a successful bounded parse with error/missing spans, not a parser process failure. The corpus can contain both PARTIAL and PARSED files while becoming READY, with honest statuses and conservative calls. Oversize/AST/depth/row/time/child failure or corrupt source prevents publication. Trusted administrators can still damage backend data; verified readers detect corruption. No garbage collection, cross-owner reuse or public analysis API is introduced.

## Operator CLI and typed reader

Build `go build -o repo-analyze ./cmd/repo-analyze` on a trusted host. Require all of: `--allow-development-analysis`, `REPO_ANALYSIS_ENABLED=true`, `APP_ENV=development`, `DEV_AUTH_ENABLED=true`, `DEV_PRINCIPAL=<owner>`, `REPO_DATABASE_URL`, and the existing independent `REPO_STORAGE_BACKEND` plus local-root or source-S3 configuration. For parsing also set `REPO_ANALYSIS_PYTHON`. Credentials remain private environment/operator files. The CLI does not execute repository code or accept repository instructions. It is absent from ordinary Docker images.

```sh
repo-analyze --allow-development-analysis --action migrate
repo-analyze --allow-development-analysis --action policy
repo-analyze --allow-development-analysis --action analyze --snapshot <ready-source-id>
repo-analyze --allow-development-analysis --action retry --snapshot <interrupted-source-id>
repo-analyze --allow-development-analysis --action status --index <index-id>
repo-analyze --allow-development-analysis --action files --index <index-id>
repo-analyze --allow-development-analysis --action symbols --index <index-id> --file internal/storage/ownership.go --kind function
repo-analyze --allow-development-analysis --action symbol --index <index-id> --id <symbol-id> --output private-symbol.txt
repo-analyze --allow-development-analysis --action imports --index <index-id>
repo-analyze --allow-development-analysis --action calls --index <index-id>
repo-analyze --allow-development-analysis --action edges --index <index-id>
repo-analyze --allow-development-analysis --action unresolved --index <index-id>
repo-analyze --allow-development-analysis --action chunks --index <index-id>
repo-analyze --allow-development-analysis --action chunk --index <index-id> --id <chunk-id> --output private-chunk.txt
repo-analyze --allow-development-analysis --action compare --index <index-id> --other <index-id>
```

`--policy` accepts bounded complete policy JSON from `--action policy`. `compare` authorizes both READY indexes and reports file additions/removals/content-status changes alongside each index's structural counts. Enumeration supports exact file filtering; symbols also support kind filtering. Source extraction uses a new `.txt` file, created exclusively with private permissions; omit `--output` to verify without emitting bytes. Keep source copies outside tracked code and do not give verification files a `.go` extension.

`analysis.Reader` exposes owner-scoped `Ready`, `ReadSymbol` and `ReadChunk`. READY manifests enumerate all symbols/chunks and exact index/snapshot/repository/source-manifest/commit/policy provenance. Span retrieval reuses `ingest.ReadFile`, checks source file identity/hash and line/byte span, then verifies content SHA-256. Session 14 should build its separate retrieval indexes from these verified READY records and retain owner scope. It must not infer semantics from the existence of a chunk or silently search another owner's index.
