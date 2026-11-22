# Private benchmark construction and replication

The actual Session 16 benchmark is private/ignored. The only committed dataset is [`public-fixture.json`](../internal/repoeval/testdata/public-fixture.json): newly authored synthetic text with an explicit publication-safety statement. Its metric results are not presented as real repository retrieval results. The integration smoke fixture derives known, previously exposed public method evidence at runtime and uses real PostgreSQL/source storage/models; it is not a held-out quality benchmark.

## Actual dataset protocol

Capture the existing authorized READY source/index at commit `56ea76e421eca19066362fdecedd4e9aae04521e`, never HEAD. The retained local snapshot has 207 source files, 2,221 retrieval documents and 1,699 repository/model/input embedding-cache rows. Source/analysis/retrieval identity and the Session 15 context policy `51b94e65085edddbaa382607e308bd5ea6aac6588a7a95a4165174e26545fd9d` remain unchanged. No migration or corpus embedding is required.

Author 60 varied questions from exact verified source: 10 exact symbols, 12 natural implementation, 10 reliability, 8 architecture/docs, 8 test evidence, 8 multi-hop and 4 provisionally absent implementation questions. Inspect declarations, caller/guard/failure statements, documented decisions and actual test assertions before any ranking run. Label 0–3 spans with exact hashes/coordinates and critical required-fact alternatives. Include independently source-selected controlled negatives; never declare a current top result correct just because a model ranks it first.

This session's draft contains 224 source judgments, a 30/30 development/test split and 14 mechanically audited questions stratified across seven categories. The audit rechecks source anchors and hashes; it is **not independent semantic review**. Five exposed Session 14/15 demo questions are explicitly marked and assigned to development. Topic-family splitting reduces some leakage, but shared source/functions, author familiarity and model-assisted semantics remain limitations. There is no training or policy tuning; the held-out set is frozen before measurement.

Candidate pools from raw BM25/dense, hybrid, reranker and graph stages are recorded after freezing to audit judgment coverage and future annotation needs. Unknown returned passages remain unjudged. They do not trigger retroactive relabeling of the held-out experiment. A later reviewed pool must be a new manifest, retaining original provenance and reporting disagreements rather than rewriting history.

Absence judgments record bounded file/symbol inventory and source-text inspection at this commit. They are provisional, not universal proof of absence or independent human truth. Their raw questions, rationales, source excerpts and audit records stay private. Public completion documentation contains sanitized aggregate results and source-linked case IDs, not a dump of unreviewed qrels.

## Trusted operator CLI

Build `go build -o repo-eval ./cmd/repo-eval`. Require all of `--allow-development-evaluation`, `REPO_EVALUATION_ENABLED=true`, `APP_ENV=development`, `DEV_AUTH_ENABLED=true`, fixed `DEV_PRINCIPAL`, explicit private `REPO_DATABASE_URL` and the independent source-storage configuration. Use the existing absolute `REPO_EMBEDDING_PYTHON/MODEL` and `REPO_CONTEXT_PYTHON/MODEL` paths. The binary is omitted from normal images and exposes no HTTP endpoint.

```sh
repo-eval --allow-development-evaluation --action policy
repo-eval --allow-development-evaluation --action catalog --index <ready-index> --output /private/catalog.json
# Author/audit a private draft from those verified bytes, with frozen=false.
repo-eval --allow-development-evaluation --action freeze --index <ready-index> --benchmark /private/draft.json --output /private/benchmark.json
repo-eval --allow-development-evaluation --action validate --index <ready-index> --benchmark /private/benchmark.json
repo-eval --allow-development-evaluation --action run --index <ready-index> --benchmark /private/benchmark.json --directory /private/run --fresh q01,q52
# The same run command resumes; keep its benchmark, directory and fresh plan.
repo-eval --allow-development-evaluation --action report --index <ready-index> --benchmark /private/benchmark.json --directory /private/run
```

Use ignored private paths (locally `bin/s16-*`), not tracked source files. Catalogs include verified source bytes and remain sensitive even for a public upstream repository. Models, credentials, unreviewed labels and local learning notes are excluded from Git. JSON is UTF-8; use Go/Python rather than PowerShell 5's case-insensitive JSON conversion for lexical maps.

## Bounded execution and outputs

The default cap is 80 questions, 30 minutes, one evaluation at a time, 32 MiB captured source, top 24 documents at depth 100/channel, existing bounded graph/windows, 4,096 WordPiece context tokens, and full re-verification every 15 questions and before complete publication. At most three query IDs may request first/repeated fresh measurements. Physical paired scores may be reused only for identical source/query/model/tokenizer/window inputs within that owner-scoped session. Shared setup/channel cost is recorded separately. Never run native agent/sandbox regressions alongside model benchmarks.

An actual interruption should leave `checkpoint.json` INCOMPLETE, with no new complete report. Failed observations may retry at most twice; previous attempts remain in the checkpoint/report. Complete rows, fresh plan, owner, policy and benchmark digests are checked on resume. A deadline/partial error is not semantic abstention and is not silently dropped from the experiment account.

Outputs are `checkpoint.json`, machine-readable `report.json`, tabular `report.csv`, and private `report.md`. JSON includes source citations, exact/span-filtered metrics, support groups, traces, paired intervals, errors, model calls and fresh timings. CSV separates exact-chunk and source-span-filtered summaries. Markdown includes per-query citations and fresh measurements. A metrics digest excludes wall-clock timings and retry history, enabling same-seed/input numerical reproduction while preserving different operational outcomes separately.

Fresh samples use production readers for all six variants, first and repeated processes. They are not called model-loaded warm samples. If the runtime cap interrupts those samples, the experiment remains incomplete. Source, model, scope or policy substitutions require an explicit compatible new experiment; they cannot silently reuse another run's checkpoints. A harness reporting correction can retain unchanged qrels under a new version/digest, with the change disclosed and no ranking-policy tuning.

See [Session 16 completion](session-16-completion.md) for actual results, intervals, errors, source-linked case studies, latency tradeoffs and the separate platform regression/hosted-CI observations.
