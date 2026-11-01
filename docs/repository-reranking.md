# Local repository reranking

`internal/contextpack.Builder` calls the existing owner-scoped retrieval reader. It retains the exact repository, snapshot/commit, analysis ID/manifest and retrieval ID/policy/manifest. It never calls source ingestion or corpus embedding. Context policy hashes all selection/window/graph/MMR/budget settings, helper/model/tokenizer locks and compiled-in builder/ranking/graph sources, with canonical LF source identity. Hybrid-only comparison has its own policy identity. Existing migrations 0001–0016 remain unchanged.

The trusted model is [cross-encoder/ms-marco-TinyBERT-L2-v2](https://huggingface.co/cross-encoder/ms-marco-TinyBERT-L2-v2) revision `81d1926f67cb8eee2c2be17ca9f793c7c3bd20cc`. The ONNX graph is **17,604,619 bytes**, SHA-256 `0eac39ee56a3edf98d0beee17fa1bb368a5a1d8c8671b5afd6dd71677f8d8496`. [`model.lock.json`](../internal/contextpack/helper/model.lock.json) pins its tokenizer/config/model card, runtime versions and license declaration. It uses genuine paired sequence classification logits, CPUExecutionProvider, one inference thread, batches of eight pairs, no custom operators, no remote inference and no dynamic model code. Model/runtime files and privileged operator configuration are trusted; upstream training-data provenance is not cryptographically attested.

Explicit setup reuses the Session 14 pinned inference environment:

```sh
python -m pip install --no-deps --only-binary=:all: --require-hashes -r internal/contextpack/helper/requirements.lock
python scripts/setup-context-model.py --directory /absolute/private/reranker
docker build -f infra/context/Dockerfile -t judgegrid-context-tools .
```

Build `judgegrid-analysis-tools` and `judgegrid-retrieval-tools` first. The setup script downloads only fixed public pinned artifacts and verifies sizes/digests. An optional operator CA preserves TLS verification. Ordinary services neither download nor ship model weights or `repo-context`. Runtime checks CPython 3.13.15, exact dependency versions and model artifacts before each helper operation. Helpers run in the existing separate 2 GiB model process envelope, with Linux 120 CPU seconds, a 120-second wall deadline/cancellation and Windows suspended-start kill-on-close jobs. No measured peak RSS claim is made.

The model uses at most 512 paired tokens: query up to 96, metadata up to 92 and a default 320-token source window, including three special tokens. Metadata is language/path/qualified symbol plus a code delimiter. Query/metadata clipping is reported. Long chunks use 48-token target overlap; the last tail-aligned window can overlap more. Uniformly sample at most four windows/document (fairly reduced by the global pair allowance), including the tail. Aggregate by maximum raw logit, tie by earlier span. This does not claim whole-method coverage when windows omit bytes; long documents can benefit from max-window aggregation, a known ranking limitation.

Defaults: hybrid 24 candidates/channel depth 100; depth two, eight graph seeds, fan-out three, twelve additions, 128 visited nodes and 30,000 examined edges; 512 KiB candidate/full-file verification planning per builder read phase; 128 scored pairs; six evidence blocks; 4,096 target tokens; 180 seconds total. Hard policy ceilings are validated. The underlying Session 14 search retains its own 15-second/32 MiB source-read limits. Thus source transfer is bounded by the existing reader plus the explicitly planned builder verification phases, rather than treating chunk length as full-file I/O. Metadata/manifest verification costs remain separate.

Intent is a conservative heuristic (`exact`, `implementation`, `conceptual`, `tests`, `documentation`, `mixed`) with explicit override. It is not a trained classifier. Relevance is `0.7*sigmoid(logit) + 0.2*exact_symbol_hint + 0.1*source_type_preference`; exact hints are case-sensitive qualified names or terminal names. These declared weights are initial policy settings, not measured optima. Documentation/tests are not removed. Original baseline and per-channel ranks remain in diagnostics; graph connectivity adds no relevance bonus by itself.

MMR is `lambda*relevance - (1-lambda)*max_redundancy`, default lambda .75. Redundancy is overlap divided by shorter span length for the actual selected scored subspans in the same file; unrelated spans/files sharing words get zero. Exact file/ranges collapse and overlaps at least .8 are suppressed. Ties use chunk ID. Distinct nonoverlapping pieces can complement each other. Graph additions receive the same query reranking, and remain labeled supporting syntactic evidence.

Primary reference: [Sentence Transformers ONNX cross-encoder inference](https://sbert.net/docs/cross_encoder/usage/efficiency.html).
