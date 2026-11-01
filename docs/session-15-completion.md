# Session 15 completion

Session 15 adds a private, opt-in local cross-encoder, scoped graph proposals, span-aware MMR and verified token-budgeted context packs. It consumes the existing READY retrieval index without ingestion, corpus re-embedding or a new migration. No LLM answering, repository-driven actions, public QA endpoint or evaluation harness was added.

## Model and policy

The reranker is `cross-encoder/ms-marco-TinyBERT-L2-v2`, revision `81d1926f67cb8eee2c2be17ca9f793c7c3bd20cc`, Apache-2.0. The 17,604,619-byte ONNX graph has SHA-256 `0eac39ee56a3edf98d0beee17fa1bb368a5a1d8c8671b5afd6dd71677f8d8496`. Actual CPU paired inference uses the pinned Python 3.13.15 / ONNX Runtime 1.23.2 / tokenizers 0.22.1 environment. Windows and Linux execute genuine model and tokenizer operations. No hosted model receives private text.

A bi-encoder independently embeds query/documents for candidate recall; the cross-encoder jointly scores each query/source-window pair. Reranking changes order within bounded retrieved and scoped graph candidates. Raw logits, sigmoid scores and heuristic weights are not calibrated relevance probabilities.

Intent has exact/implementation/conceptual/tests/documentation/mixed modes and explicit overrides. Declared relevance is `.7*sigmoid(logit)+.2*exact_symbol_hint+.1*source_preference`. Implementation intent reserves its first slot for the strongest available named implementation, while tests/docs remain eligible. Graph membership adds no ranking bonus.

MMR uses `.75*relevance-.25*maximum_span_overlap`; overlap is intersection divided by shorter selected span in the same file. The independent fixture checks `.75*.8-.25*.5=.475`, changes selection when lambda changes, collapses identical ranges and suppresses overlaps of at least .8. Shared identifiers across unrelated files are not duplicate evidence.

Policy defaults: hybrid 24 candidates, channel depth 100, four 320-token source windows/document with target overlap 48, global 128 pairs; graph depth two, eight seeds, fan-out three, twelve additions, 128 visited nodes and 30,000 examined edges; 512 KiB candidate/full-file source planning per builder read phase; six blocks, 4,096 WordPiece tokens, 180 seconds. The existing reader retains its own limits. Model pairs are checked at 512 tokens including query, metadata and special tokens. Tail-aligned sampling, uncovered bytes, query/metadata clipping and final cropping are reported.

## Actual retained repository evidence

Source commit: `56ea76e421eca19066362fdecedd4e9aae04521e`. Source snapshot: `4dc8667343e1b42e7d57fe6ef03b4418087f71cc6fc38c3a4d3413416e8805d8`. Analysis index: `92609f260e21f8d08329ffbd8ee2d86635536abd266666d29fae036f22b5cc3c`. Retrieval index: `754720b7e2731b6b4321754b75a1f99b5cfd694b459ebca858781068af1dd338`; manifest: `e2b66586925035b05cb1fb18a929ef6b9003cbd6c38e5ffbc29786a550295cd7`. Its 2,221 documents and 1,699 repository/model/input cache rows were retained unchanged. CI explicitly transfers its already verified Session 14 fixture to Session 15 and cleans up that isolated schema afterward.

Context policy: `51b94e65085edddbaa382607e308bd5ea6aac6588a7a95a4165174e26545fd9d`. Model identity: `550fc35e075acfe4c71e3c75c1081e9a6d61a5a40cf9978de146b3e4831b3195`. Tokenizer identity: `15554009ff59ee4b67ac5a8140ff660172271a0246f358b0c2017e879d12d331`.

| Query | Hybrid seconds / tokens / blocks | Context seconds / tokens / blocks | Scored pairs |
| --- | --- | --- | --- |
| Repository.renewExecution | 27.509 / 4093 / 5 | 28.388 / 3642 / 3 | 37 |
| where is the execution lease renewed? | 27.363 / 4094 / 4 | 29.045 / 3994 / 5 | 63 |
| how are stale workers prevented from finalizing an old result? | 27.219 / 3690 / 3 | 28.845 / 3860 / 4 | 68 |
| where does the compilation cache get populated? | 26.984 / 3903 / 5 | 28.870 / 3958 / 3 | 46 |
| where is the outbox event published? | 27.154 / 3970 / 6 | 28.803 / 4096 / 3 | 43 |

All requested runs used the same retained source/index on this Windows Docker Desktop machine. Both paths verify source and pack actual evidence; hybrid-only skips cross-encoder/graph ranking and carries a distinct policy. Each helper is a new process, so these are cold-process measurements, with normal filesystem-cache variation. No persistent model-loaded warm process exists. Timings are observations, not throughput or quality benchmarks.

| Query | Scope verify | Retrieval | Graph/analysis | Source/pack verify | Rerank wall | Model load | Inference | Tokenize | Assembly | Total |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | 6.042 | 8.954 | 1.614 | 10.422 | 0.829 | 0.145 | 0.227 | 0.015 | 0.515 | 28.388 |
| 2 | 6.000 | 9.096 | 1.712 | 10.666 | 1.096 | 0.138 | 0.410 | 0.066 | 0.415 | 29.045 |
| 3 | 5.938 | 9.030 | 1.686 | 10.561 | 1.130 | 0.141 | 0.452 | 0.066 | 0.455 | 28.845 |
| 4 | 5.947 | 9.228 | 1.614 | 10.573 | 0.954 | 0.141 | 0.319 | 0.035 | 0.531 | 28.870 |
| 5 | 6.153 | 9.144 | 1.664 | 10.379 | 0.855 | 0.133 | 0.260 | 0.027 | 0.588 | 28.803 |

All phase values are seconds. Model load/inference/tokenize are nested within rerank wall time; do not add those columns twice. Source/pack verification includes both candidate reads and final scope/source/citation/token re-verification. Small residual overhead is included in total. Repeated manifest and source integrity verification dominates these runs; the small ONNX forward pass is not the primary bottleneck. Peak RSS was not measured. The existing separate 2 GiB helper envelope, CPU/wall limits, cancellation, source/pair/window/graph/token limits remain enforced.

### Verbatim citation examples

- `Repository.renewExecution`: `internal/storage/ownership.go` / `Repository.renewExecution`, bytes [1466, 2312), lines 39–55, selected-source SHA-256 `d0e63f16b5c0fe198344c73729c78beb4cf8324968d7fca59eebb8d35e838482`, partial=true. The private pack contains the actual verbatim bytes and E-numbered citation, not a generated summary.
- `where is the execution lease renewed?`: `internal/storage/ownership.go` / `Repository.renewExecution`, bytes [1569, 2407), lines 40–62, selected-source SHA-256 `f825eae0fa96b8076b438d0d39e9db4c46a9df98441acf0bc0692b616020c3ad`, partial=true. The private pack contains the actual verbatim bytes and E-numbered citation, not a generated summary.
- `how are stale workers prevented from finalizing an old result?`: `internal/storage/sandbox.go` / ``, bytes [2211, 2384), lines 65–69, selected-source SHA-256 `46baed1396e101d0294e63d44f1d1ad6f22f5a6e99cf939ef9b01e355f43b506`, partial=false. The private pack contains the actual verbatim bytes and E-numbered citation, not a generated summary.
- `where does the compilation cache get populated?`: `internal/execution/cache_config.go` / `Docker.ConfigureCache`, bytes [198, 795), lines 11–24, selected-source SHA-256 `989510e92023b9abee9afcf029450e9ac840f001b12769f55cd95e9dbabafcf7`, partial=false. The private pack contains the actual verbatim bytes and E-numbered citation, not a generated summary.
- `where is the outbox event published?`: `internal/storage/outbox.go` / `Repository.Published`, bytes [2020, 2611), lines 37–46, selected-source SHA-256 `d5c877dee64219a22d1b60c73f2adf6723ed3b5bdbaee28977b42e4c14f07170`, partial=false. The private pack contains the actual verbatim bytes and E-numbered citation, not a generated summary.

Safe graph proposals use only resolved local CALLS, CONTAINS, RECEIVER_OF and IMPORTS edges, with exact edge ID/type/class/span/depth and seed retained. Candidate/unresolved/external edges are not runtime-call proof. Graph neighbors must already belong to this retrieval index and pass the same authorized source checks.

Actual proposal: `internal/storage/ownership.go` / `Repository.renewExecution`, edge `0534ad5f51b21107881847d0ae4526c06e382b776b1062ec9eb3ce366c53aac4`, CALLS / resolved, depth 1, seed `ef00364afa119344de5f6dd0ee7040334fd4c7fb064a021d5f6da9c5da9a9453`. This is supporting syntax evidence, not an inferred runtime guarantee.

## A–AB verification

| Cases | Actual verification |
| --- | --- |
| A–D | Retained READY source: exact renewal method, natural-language lease ownership source, actual build-cache code and outbox implementation present; all five packs independently verified |
| E–G | Intent overrides/policy identities; real model scores finite/repeated within 1e-5; model/tokenizer/pair contract mismatch rejected |
| H–I | Long UTF-8 source fixture: true tail-scored evidence wins, covered-byte/truncation/window/token metadata checked; exact cropped citations |
| J–M | Resolved-neighbor fixture and actual retained graph; candidate relationship excluded; cycles, depth zero, edge cap, cancellation and foreign snapshot rejection |
| N–Q | Exact range collapse, same-file overlap suppression, independent MMR arithmetic, lambda-dependent diversity and unrelated-file preservation |
| R–T | Actual tokenizer counts complete canonical scope/evidence JSON; byte/line/SHA checks for cropped UTF-8 source; every delivered block independently reread and reverified; forged commit/source rejected |
| U–V | Actual private S3 corruption and deletion fail explicitly; original bytes restored; foreign principal/build/verify and mismatched snapshot denied without disclosure |
| W–Y | Empty BM25 status, documented weak heuristic, 128-token header abstention; hostile README action strings remain verbatim inert data; model failure and absent telemetry collector preserve authoritative READY state and private metric labels |
| Z | Actual canceled local inference and graph traversal, explicit timeout outcome, policy/source/pair/window/graph/token caps; no measured peak-memory claim |
| AA | Five requested retained-index comparisons above, with real candidate/window/model/graph/source evidence and unchanged manifest/cache count |
| AB | Legacy and runtime results recorded below |

The first native test invocation exposed Windows default-encoding failure for non-ASCII helper JSON. Explicit UTF-8 stdout fixed it; the native model/tokenizer and full owned-fixture checks then passed. A separate PowerShell test-launch argument issue was resolved in the private harness by passing the executable argument list through Python. Neither workaround weakens model, source or execution checks.

One overlapping CLI/normal/race invocation hit the existing retrieval deadline: the retained comparison reported TIME_BUDGET_EXCEEDED and that command exited nonzero. It is not counted as a passing matrix. The quiet full context rerun passed all nine functions in 389.864 seconds, including the five retained comparisons in 339.34 seconds, with unchanged per-operation limits. A superseded run was deliberately interrupted for saved-pack verifier hardening and is also not counted as a final pass. Re-verification now checks block/full-file byte limits before reads and the actual bounded resolved seed-to-citation graph path.

## Regression and delivery

Formatting, vet, uncached normal/race Go suites and all command builds passed. The 45 PostgreSQL/Redis behavioral tests, six ingestion and six AST-analysis behavioral functions passed against real services. Five context unit functions and four context behavioral functions cover the matrix; native Windows unit/owned/fault fixtures also passed. The existing full Session 14 race-enabled retrieval suite passed in 397.079 seconds, including crash recovery, HNSW/exact ranks, storage/vector faults and all five retained-query comparisons. Native execution results are recorded below.

Native CLI policy, pack, rerank, graph, tokens, citations, compare and saved-pack verify passed on the retained index; foreign saved-pack verification failed without stdout. Both ordinary application images built; Compose and trusted-controller isolation audits passed. Private evidence, model weights, credentials and local learning notes remain ignored.

Two initial complete native judge invocations failed the memory case; an isolated diagnostic confirmed TLE under the original 256 MiB / two-second fixture. The test now appends a separate immutable version with a stricter 32 MiB memory cap, retains the same two-second wall guard, and restores the original current-version pointer afterward. This isolates the intended OOM assertion without changing production limits, verdict precedence, sealed history or timeout semantics. The isolated MLE and subsequent full eleven-case judge rerun passed. Failure diagnostics now print the actual verdict instead of its pointer.

The complete native sandbox suite passed. The final native judge run passed all eleven real verdict/isolation cases. The combined native agent invocation exited nonzero: process crash recovery, drain, volume-only orphan/active-generation checks, bundle-store outage recovery and cache concurrency had failures. Automatic verdicts, repeated-crash budget, Redis/database heartbeat outages, the other orphan cases, Session 10 fairness/SSE/rejudge, Session 11 telemetry/Redis chaos, cache-publication crash recovery, cache-store outage recovery, immutable object storage and object-backed historical rejudge passed. This command is not represented as a fully passing regression suite.

A separate targeted invocation with global telemetry disabled passed claim/after-commit recovery, graceful drain, volume-only orphan reconciliation and the real cache-concurrency/poisoning proof: ten C++ requests, one compiler invocation, one cache miss and nine hits. Before-commit recovery, forced drain and active-generation checks still failed in that native rerun; it also exited nonzero. The bundle-store outage's long retry/durable-failure error was not labeled a pass. Native lease/control/timing failures remain an explicit local limitation; no production execution/lease limits or verdict rules were relaxed to conceal them. Hosted Linux workflow results are separate observations and accompany the authorized commit/push delivery.

Production defaults were restored and audited: API/worker healthy, health/ready 200, unauthenticated submissions 401, root/session/metrics/repository/context routes 404. PostgreSQL/Redis have no host port bindings; development auth/dispatch/telemetry are off. Both optional object stores are stopped with volumes preserved. Source/analysis/retrieval have no active retained claims; both source manifests, the retained retrieval manifest and all 1,699 embedding-cache rows are unchanged. Management migration check passes; all sixteen disk/staged/public/retained migration hashes match the Session 14 baseline. Final formatting and tagged judge vet checks passed after the stricter MLE fixture change.

## Limits and Session 16 handoff

TinyBERT is a compact English passage model, not a code-trained correctness oracle. Max-window aggregation can favor long documents; sampled windows can omit useful bytes. Graph resolution is conservative syntax, intent and weak-evidence checks are heuristics, ranking weights are initial choices, span MMR does not identify semantic duplicates in separate files, and suffix fitting is bounded rather than optimal. WordPiece counts apply to this declared wire/tokenizer, not an unspecified future LLM. Outer diagnostics and any future prompts/message overhead need separate budgets. Saved-pack verification attests citation/scope/token integrity, not historical model-score correctness. Five selected queries do not establish quality improvement.

Session 16 can evaluate recall, ranking and evidence coverage using preserved candidate/channel ranks, actual logits/windows, graph reasons, omission/uncertainty records and re-verifiable packs. A controlled evaluation set, broader languages, calibrated abstention and latency/peak-memory benchmarking remain future work. No Session 16 implementation started.

See [reranker details](repository-reranking.md), [pack schema](context-pack-schema.md), [grounding/operator guide](context-grounding.md) and [ADR 0015](adr/0015-private-evidence-context.md).
