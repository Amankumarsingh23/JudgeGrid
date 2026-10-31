# Session 13: immutable syntax analysis and verified chunks

The Session 12 baseline is `4b8e3c99d04d504599b0fbb8c07f2e529ad2dec6`. This session implements actual Tree-sitter syntax parsing, scoped symbols/relationships, deterministic structural chunks and an owner-authorized internal reader. No BM25, embeddings, vector retrieval, reranking, LLM calls or autonomous agents are implemented.

See [ADR 0013](adr/0013-bounded-tree-sitter-analysis.md), [operator and language contracts](repository-symbol-analysis.md) and [chunk schema](code-chunk-schema.md).

## Architecture and trust

The Go 1.26 controller uses a separately resource-bounded trusted CPython 3.13 process, pinned Tree-sitter 0.26.0 and compiled grammar wheels (Go/Python/JavaScript 0.25.0, TypeScript/TSX 0.23.2). Package/wheel/module digests are checked; source arrives only over stdin from the Session 12 verified READY reader. Linux uses kernel address-space/CPU limits; Windows assigns the suspended child to a kill-on-close memory-limited job. Real wall deadlines kill the native process. No source imports, scripts, working-tree reads, repository grammar/plugin loading or execution authority is introduced.

Migration 0015 preserves all earlier SQL/source/judge history. Index identity includes immutable snapshot and parser/extractor/chunking policy. Every file is accounted for, with generation-scoped private progress, short claim/cache transactions, database-clock renewals and stale-token fencing. The complete manifest and domain rows publish atomically. Owner joins precede public metadata/source reads; chunk and symbol retrieval reverify immutable source bytes and exact hash/range/line provenance. Telemetry has fixed operation labels and no private content identifiers.

Supported syntax is Go, Python, JavaScript/JSX, TypeScript and TSX. Other included text is UNSUPPORTED and receives bounded file regions; excluded entries receive metadata only. CST errors are explicit PARTIAL files; process death/time/AST/source-integrity failures are FAILED indexes, not empty successes. Static binding is conservative and does not claim compiler or runtime type checking.

## Actual retained snapshot

The authorized retained local snapshot was read directly, without fresh Git ingestion:

| Provenance | Observed value |
| --- | --- |
| Owner | `aman-s12` |
| Source snapshot | `4dc8667343e1b42e7d57fe6ef03b4418087f71cc6fc38c3a4d3413416e8805d8` |
| Source manifest | `132315c415dca198405a231ff1297787a825fdfb6eda378a337d82abb253f97a` |
| Exact commit | `56ea76e421eca19066362fdecedd4e9aae04521e` |
| Analysis index | `92609f260e21f8d08329ffbd8ee2d86635536abd266666d29fae036f22b5cc3c` |
| Analysis policy | `70a099d9d4b3fd5c1282fbf13ee2234b851b84614112cfd7f5f0eed59d7cbc93` |
| Analysis manifest | `fdf208982a13678b419c97eb1e716c2e0bf39201035acf9295ceb8d5588002a4` |

Inventory remained **264 entries / 207 included regular files / 977,091 included bytes**, with 48 directory entries and nine excluded regular files. Analysis accounted for **128 PARSED, zero PARTIAL, zero FAILED, 79 UNSUPPORTED, 57 EXCLUDED**. Supported files: 118 Go, nine Python, one JavaScript. Unsupported included files: 42 Markdown, 13 SQL, 11 YAML, five JSON, two Dockerfiles, two shell, two text, one Go module and one C++ source. These are observed status counts, not a source-code coverage or semantic-accuracy score.

Symbols total **1,185**: 370 functions, 252 methods, 117 structs, 24 interfaces, eight named types/aliases, two classes, 32 constants and 380 variables. There are **1,032 imports** (259 resolved local module imports, 773 external) and **7,583 calls** (1,498 resolved, 25 candidates, 4,402 unresolved, 1,658 external). The graph has **12,289 edges**: 1,185 containment, 252 receiver ownership, 2,356 import-to-file/module, 7,583 call and 913 candidate type-reference edges. An import can target multiple package files, so import-edge count differs from import count.

The default policy produced **2,253 chunks / 989,290 chunk bytes**, including deliberate parent/nested-symbol overlap. With 4,096-byte chunk policy, the separate immutable index produced **2,312 chunks / 996,842 bytes**. Its symbols/calls were unchanged and all 128 supported files were new cache misses because policy identity changed. Empty source files need no chunks; all nonempty eligible bytes are covered.

Exact observed `internal/storage/ownership.go` examples at the pinned commit:

| Structure | Exact byte span | Lines / evidence |
| --- | --- | --- |
| `storage.authorizeWork` function | `[702,1465)` | 21–38; raw span SHA-256 `2597838514c9c32d1619ef6fe527c51903fa1372532e47eca438932b5691ccd6` |
| `Repository.renewExecution` pointer-receiver method | `[1466,2407)` | 39–62; owner receiver `Repository`, variable `r` |
| `Repository.RecoveryJobs` method | `[11475,13288)` | 246–278 |
| `authorizeWork → setFence` | call target `[1443,1451)` | line 37 columns 8–16; exact same-package function `[332,641)` at lines 16–19, resolved syntactic binding |

The typed reader verified a real ownership function and chunk against stored source. Synthetic real-parser fixtures separately verify value/pointer receiver ownership, duplicate short names, lexical nesting, Unicode byte columns, aliases, class dispatch, parameter shadowing, unsupported languages and partial errors. All unresolved syntax is retained; no global-name equality is used as a resolved edge.

## Reuse and performance observations

The final-policy initial Linux race analysis had **zero cache hits / 128 misses** and measured **27.130 seconds for the full call**, including **19.191 seconds for construction/validation before atomic publication**. Its full verified READY repeat measured **1.391 seconds**. Publication uses bounded PostgreSQL COPY batches with all guards active. CLI `attempt.seconds` measures full call duration; immutable `stats.seconds` describes construction before publication. READY replay has no parser calls. Memory/CPU peaks were not instrumented; kernel/helper limits and sequential concurrency are documented, not claimed as measured peak RSS.

The final native Windows CLI analyzed the second actual retained HTTPS snapshot `8a41cccc69ac0282886c5ac565db50a537619b2011f2a72a1be898e4ca6e49d0`, with the same exact source commit and final policy. Its separate repository scope produced index `a49a46db7a77b42c35394891ab4d2f9934db6a91a7463381413f9b467261a1d9` and manifest `89eb7dbfba2069da8ce3ae92a4f971475d0dd884a5ba5334824406a424f47b7c`. Counts matched the local snapshot. This cold call had zero hits/128 misses, **55.693 seconds construction / 59.498 seconds full duration**, followed by **0.313 seconds full verified replay**. Native ambiguity/Unicode tests, cross-platform policy identity and independently hashed symbol/chunk extraction passed. The native and Linux samples differ in platform/instrumentation and do not establish a comparative capacity benchmark.

A real Git→private-S3→READY fixture changed only an imported Go file, renaming `Target` to `Renamed`. The unchanged caller reused file-local facts; the new snapshot observed **four hits / one miss**, reconstructed all IDs and changed its old `b.Target` resolved edge to unresolved with zero stale targets. READY repeat retained exact symbols/chunks/manifest. A changed chunking policy produced a distinct index and cold syntax-cache scope. An analyzer child really exited after partial progress; no partial READY was readable, and generation two recovered with verified cache reuse. An expired old claim could neither renew, cache-write nor publish after takeover.

## Required A–Y matrix

| Cases | Actual assertion |
| --- | --- |
| A–B | Real Go function/struct/interface/type extraction and both pointer/value receiver associations |
| C–D | Real Python class/method/nested function; JavaScript, TypeScript and TSX grammar cases |
| E–F | Duplicate identities remain distinct; exact UTF-8 substring/range and line positions verified |
| G–J | Aliased/local/external imports, correct static calls, dynamic/ambiguous candidates, shadowing, no unrelated global-name match |
| K | Malformed Go grammar yields PARTIAL plus error spans; no parser crash disguised as success |
| L | Source-byte, CST node/depth, symbol/call/import/reference, helper deadline, chunk and graph row limits fail boundedly |
| M | Large Unicode function splits into capped valid UTF-8 chunks, retains symbol and covers verified bytes |
| N–Q | Four reused facts/one changed import file, policy separation, rebuilt edges and deterministic repeated manifests |
| R–S | Real analyzer process death; no partial READY; generation takeover and stale renew/cache/publication rejection |
| T | Privileged fixture source-S3 corruption prevents READY; exact source bytes restored afterward |
| U | Foreign status/index/symbol/chunk/analyze denied; denied readers reach no source-store access |
| V | Unreachable OTLP exporter cannot prevent READY; labels contain no private source identifiers |
| W | Actual Git symlink/gitlink/LFS/dependency exclusions produce no parser chunks |
| X | Actual retained baseline counts/locations/graph above, source manifest unchanged; CI reconstructs the same exact commit in its own private fixture |
| Y | Existing full regressions and delivery evidence recorded below |

The Linux race analysis matrix passed after fixture corrections. The first crash-helper attempt lost its isolated schema when serializing a connection configuration; the child now receives that schema explicitly. A second test selected an incompatible overlap for its small chunk policy; its fixture overlap was corrected. Neither changed a lease, safety limit or publication guard. A native cold analysis attempt during the application build hit the parser deadline and correctly recorded a FAILED file/index; it is separate from successful matrix/retry evidence. Later scope checks keep block-local declarations private to their spans and classify variable/conversion calls as candidates. The policy now hashes the helper/grammar lock and compiled-in resolver/chunker sources; the resulting new READY index preserves prior experimental-policy records. Helper-only no-op parent invocations do not count as behavioral cases.

## Regression and delivery

Formatting, vet, executable builds, uncached normal and race unit suites passed with the final implementation. All 45 previous PostgreSQL/Redis behavioral functions passed. The six existing ingestion behavioral functions passed with actual PostgreSQL and separate source MinIO, including exact pinned baseline ingestion; the initial combined invocation omitted its required real-path environment and is not counted as a pass. The configured rerun passed. The six new analysis behavioral functions and six new unit functions passed under Linux race instrumentation; the child-helper-only function is excluded from behavioral counts. The final native Windows analysis matrix also passed. Native binaries are not race-instrumented.

All fifteen SQL files match disk/staged/public/lab checksums, and migrations 0001–0014 match the prior baseline exactly. Compose application build/health, optional profile validation, migration checks and service/controller-isolation audits passed. A management check temporarily recreated PostgreSQL without its loopback port during native verification; the port was restored and that native verification was rerun sequentially. The interrupted invocation is retained separately and is not counted as a pass.

The complete sequential native runtime invocation passed: **30 sandbox scenarios, 11 local judge scenarios, 16 baseline agent scenarios, ten orphan cases, six Session 9 fault functions, Session 10 fairness/SSE/rejudge and the Session 11 combined telemetry/Redis fault**. All three runtime binaries exited zero. AC/WA/CE/RE/TLE/MLE/OLE, execution fencing, database heartbeat outage, interrupted cache publication, object/cache outages, concurrent compilation deduplication/cache poisoning and object-backed rejudge all retained actual passing evidence. No authority or timing limit was weakened. Earlier source credentials, private evidence, parser environment and numbered learning notes remain ignored and unpushed. Final restoration and authorized push/hosted workflow observations accompany delivery.

## Limits and Session 14 handoff

Final restoration passed: API/worker healthy under production defaults; `/healthz` and `/readyz` returned 200, unauthenticated submissions returned 401, and root UI/session/metrics/repository routes returned 404. PostgreSQL/Redis have no host port bindings. Both optional private object stores are stopped with volumes preserved; no analysis claim is active. Both retained source snapshots remain READY with their original manifests. Management migration check and service/controller-isolation audits passed again. The authorized commit, push and actual hosted workflow result are reported with delivery.

Syntax resolution omits compiler/build-tag evaluation, full generic/type inference, dynamic dispatch, package/dependency execution, reexports and configured JS path aliases. The grammar/helper/interpreter and privileged operator remain trusted. Whole-file facts are reused; Tree-sitter incremental edit trees are not stored. The current version is sequential and bounded; neither performance samples nor raw counts establish production capacity or resolution accuracy. Source/backend corruption is detected, not repaired automatically.

Session 14 can use `analysis.Reader.Ready` for immutable chunk/symbol enumeration and `ReadChunk`/`ReadSymbol` for verified source spans, always preserving repository owner, snapshot, policy and index scope. BM25/vector/hybrid indexes belong to that later session; none have been started here.
