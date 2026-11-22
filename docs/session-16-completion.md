# Session 16 — Repository retrieval evaluation

The private evaluator compares six stages of JudgeGrid's repository search over the same frozen questions and source. The retained Linux run completed 60 questions × six variants, plus 24 fresh process measurements. Replication commands and failure rules are in [the benchmark guide](retrieval-benchmark.md). Metric formulas, evidence matching and the risk register are in [the evaluation guide](retrieval-evaluation.md).

## Dataset and provenance

The authorized READY index contained 2,221 documents at source commit `56ea76e421eca19066362fdecedd4e9aae04521e` and snapshot `4dc8667343e1b42e7d57fe6ef03b4418087f71cc6fc38c3a4d3413416e8805d8`. The frozen v2 manifest digest is `df9caedd9a685675865a477cf668c7ec0c895f5f36dc2bae13f6163369c8f157`, the evaluator digest is `954aa7c7a6ca9b081f945a659ae76608cb49ea7baabf70770048883a27177678`, and the evaluation policy digest is `7a5582e6f77654b57f53454911aa30c94f0554d06c8220f9a4b6d196a281ae6f`. The numerical report digest is `0de908cd75a6e45abaf80c4f4c00b9ba491381827a8a231f4be48b023c47651b`.

The dataset has 224 source-span judgments and a 30/30 development/test split. It includes 10 exact-symbol, 12 implementation, 10 reliability, eight documentation, eight test-evidence, eight multi-hop and four negative questions. Five previously exposed questions stayed in development. Source anchors and hashes were mechanically audited for 14 stratified questions. Semantic judgments and absence labels are provisional and model-assisted. Independent human review was unavailable. The evaluation used frozen labels and unchanged retrieval policies.

## Held-out results

These are span-filtered results at K=5 for the 28 held-out questions with known positives. Binary relevance starts at grade 2. Unknown gains count as zero in the pooled nDCG calculation. Average top-five judgment coverage ranged from 8.0% to 11.3% across variants, so whole-corpus judgments could change the ordering. V6 uses evidence assembly order.

| Variant | Known Recall@5 | Hit@5 lower | MRR@5 lower | Pooled nDCG@5 | Paired nDCG delta vs hybrid (95% CI) |
| --- | ---: | ---: | ---: | ---: | --- |
| BM25 | 0.4107 | 0.4286 | 0.2887 | 0.3158 | −0.0201 [−0.1785, +0.1364] |
| Dense | 0.3869 | 0.4286 | 0.2589 | 0.2854 | −0.0505 [−0.1779, +0.0662] |
| Hybrid RRF | 0.4226 | 0.4643 | 0.3107 | 0.3358 | Reference |
| Hybrid + reranker | 0.4345 | 0.4643 | 0.3613 | 0.3715 | +0.0357 [−0.0960, +0.1639] |
| Reranker + graph | 0.5417 | 0.5714 | 0.4554 | 0.4695 | +0.1337 [−0.0265, +0.2798] |
| Full context pack | 0.5298 | 0.5714 | 0.4792 | 0.4829 | +0.1471 [−0.0078, +0.2919] |

Each paired interval used 2,000 query-level bootstrap resamples with seed 20261102. Every interval versus hybrid includes zero. The private JSON and CSV retain per-category scores and separate exact-chunk and span-filtered rankings. Candidate-union recall and required-fact coverage are measured separately.

## Evidence, failures and performance

Across both splits, the full pack covered all required groups for 20 queries, some for six, and none for 30. Four negative questions had no required groups to score. The provisional absence rubric recorded two correct abstentions and two false strong-support outcomes. Evidence coverage measures source support. Evaluating the groundedness of generated answers would require generated claims and claim-level judgments.

- **q01, reranking** — Hybrid's first citation was in [storage limits](https://github.com/Amankumarsingh23/JudgeGrid/blob/56ea76e421eca19066362fdecedd4e9aae04521e/internal/storage/limits.go#L80). Reranking moved the [lease-renewal implementation](https://github.com/Amankumarsingh23/JudgeGrid/blob/56ea76e421eca19066362fdecedd4e9aae04521e/internal/storage/ownership.go#L39) to the top. The final 3,642-token pack preserved the required fact.
- **q03, exact identifier and graph recovery** — BM25 put [`completeEvaluation`](https://github.com/Amankumarsingh23/JudgeGrid/blob/56ea76e421eca19066362fdecedd4e9aae04521e/internal/storage/evaluations.go#L153) first, while dense and reranking returned fragments missing the required fact. Graph expansion restored the fact. Full packing preserved it and reduced the measured duplicate-byte fraction from 1.95% to zero.
- **q11, conceptual retrieval** — Dense reached both required groups and ranked the [lease renewal code](https://github.com/Amankumarsingh23/JudgeGrid/blob/56ea76e421eca19066362fdecedd4e9aae04521e/internal/storage/ownership.go#L39) first. BM25 led with the [lease and fencing ADR](https://github.com/Amankumarsingh23/JudgeGrid/blob/56ea76e421eca19066362fdecedd4e9aae04521e/docs/adr/0007-execution-leases-and-fencing.md#L1) and missed both groups. Graph and packing each recovered one group.
- **q06, cropped evidence** — Hybrid scored 1.0 on pooled nDCG@5. Reranking scored zero. The full pack included the beginning of the [cache function](https://github.com/Amankumarsingh23/JudgeGrid/blob/56ea76e421eca19066362fdecedd4e9aae04521e/internal/buildcache/cache.go#L63), ending at line 83 before the judged evidence starting at line 85. Fact coverage was zero.
- **q52, documentation and packing** — BM25 led with [rejudge documentation](https://github.com/Amankumarsingh23/JudgeGrid/blob/56ea76e421eca19066362fdecedd4e9aae04521e/docs/rejudging.md#L1). Dense led with the [rejudge implementation](https://github.com/Amankumarsingh23/JudgeGrid/blob/56ea76e421eca19066362fdecedd4e9aae04521e/internal/storage/rejudge.go#L22). Each covered two groups. Hybrid through graph covered three. The final pack hit 4,096 tokens and retained one of three. The recorded failure stage groups MMR and the token budget together.
- **q58, false strong support** — The full pack returned READY and led with a [sandbox storage span](https://github.com/Amankumarsingh23/JudgeGrid/blob/56ea76e421eca19066362fdecedd4e9aae04521e/internal/storage/sandbox.go#L51). The provisional absence label classifies this as false strong support.
- **Fresh latency** — Two queries ran twice through all six variants with separate helper processes. BM25 took about 1.03–1.06 seconds and complete packs 6.93–7.52 seconds. Full-pack verification took 1.99–2.10 seconds, plus 0.84–0.88 seconds verifying scope. Cross-encoder model load and inference together took 0.35–0.61 seconds. Shared verified-source capture and batched embeddings are accounted for separately from fresh latency. These Linux-container timings are specific to the two sampled queries and differ in environment from the earlier 28–29 second Windows observations.

The private v2 output contains the JSON, CSV, Markdown and checkpoint, with per-query verified citations, omissions, model calls, token counts and failure classifications. An earlier interrupted run resumed; the v2 report records zero prior failed attempts under its own manifest. The public fixture contains synthetic data.

## Verification and handoff

The retained tagged evaluator race run passed with genuine source storage, PostgreSQL and model paths. It covered owner denial and corrupt/unavailable source, interruption and recovery, ablation parity, citation checks and a two-query smoke run against the 2,221-document corpus. Metric and manifest tests exercise worked ranking examples, tie/zero-positive handling, overlap and cropped evidence, unjudged results, provenance, reproducible bootstrap/reporting and frozen labels. The 60-query run provides the full benchmark evidence. CI also exercises the evaluator against its authorized test fixture.

Initial hosted verification exposed a legacy memory-verdict failure (RE instead of MLE) in [run 45020691305](https://github.com/Amankumarsingh23/JudgeGrid/actions/runs/45020691305). The sandbox job passed on [run 45020778589](https://github.com/Amankumarsingh23/JudgeGrid/actions/runs/45020778589), whose check job hit Go's ten-minute package timeout in the Session 14 retained search. CI now gives the tagged retrieval and context suites 25 minutes each and the evaluator suite 15 minutes. Production request, sandbox and benchmark limits are unchanged. The native Windows combined unit invocation encountered a parser failure and a symlink-privilege failure. The parser suite passed in isolation.

The native Windows agent failures documented in Session 15 remain open. Keep provisional judgment coverage and the two false-support cases visible when using Session 16 to inform Session 17. The handoff is scoped evidence, sufficiency status, citation verification, failure classifications and measured cost. Session 17 owns planning, tool permissions, durable state and approval.
