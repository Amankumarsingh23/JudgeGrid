# Session 14 completion: private hybrid repository retrieval

Baseline: `f0d6e3f91ecf61131c28f33b871702c695746885`. This session adds genuine local lexical/dense/hybrid search over the retained READY source analysis. It adds no reranker, context-pack assembly, LLM answering, evaluation harness or autonomous agent. See [ADR 0014](adr/0014-private-hybrid-retrieval.md), [operations](repository-retrieval.md), [embedding policy](embedding-policy.md) and [ranking contract](retrieval-ranking.md).

## Identity and actual corpus

Owner `aman-s12`; exact source commit `56ea76e421eca19066362fdecedd4e9aae04521e`. No fresh HEAD ingestion was used for this evidence. The source snapshot and Session 13 analysis remain unchanged.

| Record | Observed value |
| --- | --- |
| Source snapshot | `4dc8667343e1b42e7d57fe6ef03b4418087f71cc6fc38c3a4d3413416e8805d8` |
| READY analysis | `92609f260e21f8d08329ffbd8ee2d86635536abd266666d29fae036f22b5cc3c` |
| Analysis manifest | `fdf208982a13678b419c97eb1e716c2e0bf39201035acf9295ceb8d5588002a4` |
| Retrieval index | `754720b7e2731b6b4321754b75a1f99b5cfd694b459ebca858781068af1dd338` |
| Retrieval policy | `93033f1a092f4adcd60324a30dfb7bcf319d255b1ab3dc45252858a478eb9ef1` |
| Retrieval manifest | `e2b66586925035b05cb1fb18a929ef6b9003cbd6c38e5ffbc29786a550295cd7` |
| Embedding policy | `9ff21d7f2d9781187aec4d79ab01f4e5c00dd1c5b3f1872e6a09c64151a8bbfe` |

The 2,253 original analysis chunks yield **2,221 indexed documents**, with **32 exact duplicate file/ranges explicitly excluded**. All remaining chunk classes, including eligible unsupported text and file regions, are indexed; source exclusions have no chunks. The corpus has **442,177 lexical term occurrences across four fields**, not transformer-token or unique-source-byte counts. Vector coverage is **2,221/2,221, dimension 384**, with finite, normalized vectors and exact context/input/vector provenance hashes. Parent/nested overlap is retained in statistics and grouped conservatively in returned rankings.

Four weighted BM25 fields use symbol/exact word/path/body weights 4/2/1.5/1, k1=1.2 and b=.75. Tokenization preserves exact case, lowercased names, camel/snake/acronym components and path segments. Corpus df/count/field lengths are persisted and independently verified. Hybrid uses pure weighted RRF, equal channel weights and k=60; no extra exact-identifier/path boost was promoted. Stable ties use chunk ID.

## Local model and vector storage

Actual inference used the pinned local ONNX `sentence-transformers/all-MiniLM-L6-v2` revision `1110a243fdf4706b3f48f1d95db1a4f5529b4d41`, CPython 3.13.15, ONNX Runtime CPU 1.23.2 and tokenizers 0.22.1. The 90,405,214-byte ONNX graph and tokenizer/config artifacts are SHA-256 verified. Attention-mask mean pooling, 256-token right truncation and L2 normalization produce genuine 384-component float32 vectors. Document inputs include exact language/path/symbol/body; query text uses the same symmetric model space. Source never goes to a public inference API. Windows and Linux actual inference tests passed; cross-platform bit equality is not claimed.

Migration 0016 adds owner-scoped immutable retrieval policies/indexes, lexical documents/corpus statistics, vectors and verified repository/model/input cache. pgvector 0.8.2 is compiled from pinned source onto the exact existing PostgreSQL 17 Alpine base digest. Existing data/major version/base distribution are retained. Per-index LIST partitions have independent HNSW graphs. Exact cosine remains the default; ANN uses explicit bounds and exact fallback for underfill or filters selecting less than one quarter of the corpus. SQL joins enforce owner/index/snapshot/analysis scope. READY publication and all vectors/manifest rows commit atomically under database-clock token/generation fencing.

## Actual query observations

The following predeclared five queries use topK=5, per-channel depth=100. Each candidate retains immutable commit/path/line/byte/hash provenance and verified source spans. These first-place results are actual index output, not relevance labels or proof that hybrid improves semantic quality.

| Query | BM25 first | Dense first | Hybrid first | ANN/exact dense overlap@5 |
| --- | --- | --- | --- | --- |
| Repository.renewExecution | `internal/storage/observed.go:74` | `internal/storage/limits.go:80` | `internal/storage/limits.go:80` | 5/5 |
| where is the execution lease renewed? | `docs/adr/0007-execution-leases-and-fencing.md:1` | `internal/storage/ownership.go:39` | `docs/adr/0007-execution-leases-and-fencing.md:1` | 5/5 |
| how are stale workers prevented from finalizing an old result? | `internal/agent/fair.go:30` | `internal/storage/evaluations.go:83` | `internal/agent/fair.go:13` | 5/5 |
| where does the compilation cache get populated? | `docs/compilation-cache.md:1` | `internal/buildcache/cache.go:81` | `docs/compilation-cache.md:1` | 5/5 |
| where is the outbox event published? | `docs/outbox.md:89` | `internal/storage/outbox.go:29` | `internal/outbox/relay_test.go:62` | 5/5 |

Dense retrieval found the actual lease renewal and compilation-cache code for their natural-language queries, while lexical/hybrid often prioritized explanatory documents. The exact private `Repository.renewExecution` declaration is retained in the lexical top five; its public wrapper ranks first. Some hybrid results favor tests or higher-level control code. These are known ranking gaps, not hidden failures or a semantic-accuracy score. The exact query lists, full channel ranks/scores and verified ranges are preserved in ignored private `bin/s14-retained-evidence.json` for later Session 16 assessment.

## Measurements

The first real model build observed **519 cache hits / 1702 document misses**, with **259.540 seconds construction** before publication. Identical canonical inputs at different ranges can share a repository-scoped embedding; unique cache rows therefore differ from document-miss count. That exploratory index remains immutable. A later explicit ranking-implementation identity change reused the compatible model/input cache: the delivered index measured **9.547 seconds full build**, 2221 hits/0 misses, followed by **7.109 seconds verified READY replay**. The failed earlier query run did not preserve a standalone full cold-build timer; its construction timing is labeled accordingly.

Five first-pass and five repeated-query samples were collected per BM25/dense/hybrid mode, plus five ANN requests: **35 searches total**. Every dense request starts a bounded helper; the repeated pass is a repeated-query/OS-cache observation, not a persistent-model service or cold-machine experiment. Durations include integrity/source verification and local inference as applicable.

| Mode | First-pass mean seconds (n=5) | Repeat mean seconds (n=5) |
| --- | --- | --- |
| BM25 | 7.057 | 7.256 |
| Dense | 8.778 | 8.849 |
| Hybrid | 8.759 | 8.808 |

The five full-corpus HNSW requests averaged **8.938 seconds**, with observed top-five overlap **25/25** against exact dense results after the same display grouping. The small independent fixture also observed recall@5=5/5 and an actual HNSW access plan; its database call was approximately 1.8 ms. These small measurements do not establish ANN superiority, general recall or production capacity. Peak RSS/CPU were not instrumented; process memory/CPU/deadline/thread limits are documented as bounds.

## A–AA verification

| Cases | Actual checks |
| --- | --- |
| A–F | Real known symbol/path/phrase/generic queries; exact/camel/snake/acronym tokens; independently calculated BM25/IDF/length normalization; field weights; exact-range collapse and overlap grouping |
| G–J | Genuine pinned CPU transformer inference on Windows/Linux; query/document bytes differ; context/model policy identity; finite/dimension/norm/NaN/Inf checks; failed model cannot silently fall back |
| K–N | PostgreSQL cosine ranks match independent float calculations; real HNSW index scan and exact comparison; selective filters use exact fallback |
| O–S | Foreign status/search/source/build denied; no foreign data/count leakage; different commits/ranking policies remain separate; independent toy RRF; actual BM25+dense hybrid; stable repeated ranks |
| T–V | Real child exits 37 after partial embeddings; no partial READY; verified cache reuse under generation two; one competing builder wins; stale renew/cache/publication rejected |
| W–Y | Actual S3 corruption/unreachable endpoint rejected and restored; stored-vector corruption detected; private metric labels excluded; absent telemetry collector leaves results unchanged |
| Z | Actual retained READY index and all five predeclared queries with exact verified citations; CI explicitly reconstructs the same pinned source commit in an isolated authorized fixture |
| AA | Full legacy database/ingestion/analysis checks passed; final runtime/default/CI observations recorded at delivery |

The first fixture invocation used an invalid repository display name; it was corrected without weakening authorization. A large-corpus query initially exhausted its deadline while repeatedly verifying the full analysis manifest for individual results. Search now uses the bounded bulk reader, preserving every source-span check and the original 15-second query bound. The configured full Linux race matrix passed after those corrections. Helper-only child-test parent invocations are excluded from behavioral totals.

## Regression and delivery

Formatting/vet, uncached normal/race suites and executable builds passed. The 45 prior PostgreSQL/Redis behavioral functions, six ingestion behavioral functions and six Session 13 analysis behavioral functions passed with actual database and separate source storage. Six new unit functions and five new retrieval behavioral functions exercise the new domain; the isolated crash helper is not an extra behavioral case. Private source/weights/credentials/evidence/backups and numbered learning notes remain ignored.

The native Windows retrieval fixture/unit suite passed, using genuine local inference. Native CLI migrate/policy/status/manifest/verify/build replay, BM25/dense/hybrid/compare, selective ANN fallback and foreign-owner rejection passed. An exported chunk independently matched its source SHA-256, and native replay preserved the Linux retrieval/policy identity. The first CLI harness used PowerShell 5's case-insensitive JSON conversion; Python corrected that harness limitation without changing stored maps or ranking. All sixteen disk/staged/public/retained migration checksums match; original migrations 0001–0015 match the verified baseline byte-for-byte. Application images built and Compose/controller-isolation audits passed.

The complete native sandbox suite (30 scenarios) and local judge matrix (11 scenarios) passed. The combined agent invocation passed the 16 baseline agent scenarios, ten orphan cases, Session 11 telemetry/Redis fault and all six Session 9 storage/cache/rejudge fault functions, including one-build/nine-hit concurrency, poisoning, interrupted publication and historical rejudge. Its Session 10 C++ AC case unexpectedly committed TLE, so that combined invocation exited nonzero; it is not reported as a fully passing command. A quiet isolated Session 10 rerun passed in 51.09 seconds with the same execution limits, including C++ AC/WA/CE, Python AC/RE/TLE, durable SSE and AC-to-WA rejudge history. No timeout, verdict rule or execution authority was weakened, and no skipped case is counted as a pass.

Final model-policy contract checks additionally passed all six retrieval unit functions under race instrumentation and the actual publication/owner/commit fixture. An incompatible backend is rejected before inference, even with a plausible vector dimension. The rebuilt native CLI successfully queried the retained index with that contract.

Production defaults were restored and verified: API/worker healthy, health/ready routes 200, unauthenticated submissions 401, and root/session/metrics/repository routes 404. PostgreSQL/Redis have no host port bindings; development auth/dispatch/telemetry are off. Both optional private object stores are stopped with their volumes preserved. No analysis/retrieval claim is active, both retained source manifests are unchanged, management migration check passes, and pgvector reports 0.8.2. The authorized commit/push and actual hosted workflow result are reported with delivery.

## Limits and Session 15 handoff

MiniLM is a compact general-English model, not a code compiler. Long contexts consume the 256-token allowance and source tails may be truncated. All eligible nonduplicate chunk classes, including short file regions, can contribute path/context-only signals. Exact match is token-based, not ordered phrase matching. Conservative overlap grouping can underfill requested diverse topK at bounded candidate depth. Small ANN recall examples and five queries are not a quality evaluation. Full integrity verification dominates some query latencies; there is no production-throughput claim. Historical implementation/model policies require their matching supported tooling, rather than being silently reinterpreted.

Session 15 can consume `retrieval.Reader` ranked candidates, per-channel ranks/scores, parse status and verified source provenance for reranking/context assembly. Those features, LLM answers and coding agents have not been started.
