# Retrieval evaluation contract

`internal/repoeval` and the opt-in `cmd/repo-eval` evaluate repository evidence. They do not generate answers or execute repository instructions. See [ADR 0016](adr/0016-private-retrieval-evaluation.md) and [benchmark construction](retrieval-benchmark.md).

## Frozen data and scope

A schema-1 benchmark binds owner/repository, full commit, source snapshot/manifest, analysis/index manifests, retrieval policy, supported context policy and tokenizer. Its digest also includes questions, source-span relevance grades 0–3, fact groups, split identity, matching/bootstrap policy and annotation provenance. A separate qrels digest excludes evaluator policy/version so a disclosed harness correction can retain unchanged labels under a distinct experiment identity. A source/corpus mismatch cannot silently reuse another snapshot's labels.

The supported annotation method is `model-assisted-source-inspection`, status `provisional`, with unavailable independent human review. Both benchmark and per-judgment provenance are validated. `human_reviewed=true` or a substituted human-review status is rejected. This validates the declared schema, not a cryptographic proof of a real annotator's identity. No independent human review or agreement statistic is invented.

Grades mean irrelevant (0), related but insufficient (1), supporting/substantially relevant (2), and direct evidence (3). Binary relevance defaults to grade >=2. Exact chunk judgments are mechanically projected from source spans onto declared documents with at least 50% gold-span coverage; projection and semantic labels remain provisional. Multiple annotations for one document use the maximum grade, not multiple positive counts.

## Metrics and unknown judgments

At each K=1,5,10,20, report exact chunk-ID metrics and a separately named source-span-filtered document-ranking proxy. The latter additionally requires at least 50% gold-span coverage and IoU >=0.01 for the actual returned span. It penalizes cropped windows that retain a chunk ID but lose its labeled evidence. These document-denominator metrics are distinct from union gold-byte and fact-group coverage.

| Metric | Definition/convention |
| --- | --- |
| Precision@K | Known positive distinct entities / K; unfilled slots earn nothing. Report lower/upper precision bounds, judged-only precision and judgment coverage. |
| Known-positive Recall@K | Distinct known positive retrieved documents / distinct known positive documents in the judged pool. This is not exhaustive corpus recall. |
| Hit@K | Any known positive in the prefix. |
| RR/MRR@K | Reciprocal position of the first known positive; macro-average across valid positive queries. |
| AP/MAP@K | Sum of precision at known-positive ranks, divided by **all** known positives, including those beyond K; macro-average across valid positive queries. |
| DCG@K | Sum `(2^grade-1)/log2(rank+1)`. |
| Pooled nDCG@K | DCG / ideal DCG from fixed known document judgments. Unknown results have zero gain **by disclosed convention**, not a grade-0 annotation. |

Unknown/unjudged is represented separately from grade 0. Zero-gain pooled nDCG and AP are **not guaranteed lower bounds** on full-corpus scores: undiscovered positives can change ideal-gain and relevance denominators. Precision bounds are conditional on the provisional labels being correct. Known-relevance RR/DCG have their corresponding lower-bound interpretation; no probability calibration is claimed.

No-positive queries have null recall/Hit/MRR/AP/nDCG and are excluded from those macro denominators. They are counted and evaluated as negative queries separately. Errors have no quality metrics and cannot earn correct abstention. Empty results on an answerable positive query contribute zero to its valid retrieval metrics. Repeated evidence IDs keep rank slots but cannot earn gain twice. Source-byte unions merge overlaps, never sum overlapping coverage. Exact chunk denominators may still contain distinct overlapping documents; byte coverage and redundancy reveal that difference.

## Evidence coverage, sufficiency and abstention

All interval arithmetic requires the same snapshot and file. IoU is intersection/union. Gold coverage is the union of retrieved intersections divided by gold length. Cross-file/snapshot comparisons are denied or contribute no union coverage. Actual source bytes, hashes and byte/line positions are verified through authorized readers.

A required evidence group names one or more alternative critical source facts. All groups are required, but any alternative can satisfy its group. Default fact coverage is **100% of the annotated critical bytes**; dropping a guard operator cannot silently count as complete. States are ALL, SOME, NONE and CANNOT_ASSESS for explicit unknown annotations. This measures support for the authored facts, not independently established completeness of an answer.

Keep citation integrity, source relevance and sufficiency separate. Candidate-union recall uses the raw bounded BM25/dense pools before fusion/cutoff; final-pack coverage uses actual E1–En bytes after windows, MMR and cropping. Diagnostics distinguish candidate misses, cutoff/graph misses, scored-window loss, MMR/budget loss, annotation uncertainty and infrastructure.

For provisionally absent implementation queries, V6 READY with evidence is false **strong support under the declared rubric**; NO_CANDIDATES or INSUFFICIENT_EVIDENCE is semantic abstention. An outage, denial or deadline is unscorable infrastructure, not correct abstention. Ranking-only variants do not make a semantic abstention claim. A useful rejection/unsupported-language passage can be related to a negative query; a later answering system must evaluate its generated claim rather than treating this rubric as answer truth.

## Ablations, uncertainty and performance

V1 BM25; V2 dense; V3 hybrid RRF; V4 hybrid/cross-encoder with graph disabled; V5 hybrid/cross-encoder/resolved graph, stopping before MMR; V6 full production graph/MMR/token packing. V1–V5 are document rankings. V6's optional rank metrics use declared evidence assembly order E1–En and are not a new document-ranker claim. Prefer its fact coverage, redundancy and bytes/token for selection comparisons.

Ranks and model input policies reuse Sessions 14/15. The evaluator records original channel/fused scores, actual logits/windows, graph reasons, omissions, full versus selected spans, policy identities and physical model calls/cache hits. A scoped verified capture and batched query embeddings are shared setup costs. Cached stage times include explicit shared-channel preparation separately; they are **not fresh end-to-end latencies**. Fresh first/repeated measurements use production readers and actual model calls for each variant. Each helper is a new process; filesystem-cache effects are not persistent-model warm inference. Embedding wall time is measured; a load/inference split is unavailable from the existing embedding interface, while the cross-encoder exposes both.

The default 2,000-resample, seeded, query-level bootstrap uses linearly interpolated 2.5/97.5 percentiles. Paired deltas include only queries valid for both variants, in stable query-ID order. Report N, macro/category/split results and small-sample flags (N<8). These descriptive intervals ignore residual within-topic dependence and do not prove causal production improvement. No held-out ranking/model tuning is performed.

Checkpoint/report writes use private modes, atomic same-directory replacement and bounded JSON. Frozen manifests are exclusive new files. Checkpoints retain failed attempts and the fresh-query plan; COMPLETE requires all six variants for every query, every planned fresh attempt, and final backing-source/index verification. Metrics/bootstrap are recomputed on resume. Checkpoint hashes detect alteration but do not cryptographically attest historical model scores; the trusted operator/OS remains a boundary.

## Open risk register and Session 17 boundary

| Risk | Status |
| --- | --- |
| Provisional model-assisted qrels, incomplete pooling, author/topic exposure | Open; independent semantic review unavailable. |
| Sparse judgments can make pooled scores misleading without coverage | Open; bounds/coverage and source fact coverage accompany scores. |
| Small categories and correlated questions | Open; descriptive query intervals and N are explicit. |
| Native Windows agent recovery/control/timing failures seen in Session 15 | Open; Linux CI success is a separate observation, not a native fix. |
| Compact English reranker and heuristic intent/abstention | Open; evaluate errors rather than assume improvement. |
| Verified evidence does not authorize an action or prove an LLM answer | Enforced; no answer generator or agent/tool/PR implementation exists here. |

Session 17 can consume scoped verified context, support/uncertainty states, cost measurements and failure taxonomy. It must introduce its own bounded planning, typed-tool permissions, durable execution state and approval boundary; none of those authorities are supplied by this evaluator.
