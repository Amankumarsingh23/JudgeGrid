# ADR 0015: local reranking and verified evidence contexts

Status: accepted for Session 15 trusted operator tooling.

Layer an ephemeral context domain over the immutable Session 14 retrieval reader and Session 13 authorized analysis reader. Do not change source/analysis/retrieval identities, re-embed the corpus, create a public endpoint or introduce execution/answer-generation authority. No database migration is required. Optional exported packs are private immutable files created exclusively, not shared semantic caches.

Use the pinned ONNX `cross-encoder/ms-marco-TinyBERT-L2-v2` at revision `81d1926f67cb8eee2c2be17ca9f793c7c3bd20cc`. It is a compact Apache-2.0, MS MARCO passage reranker, not a code compiler. Genuine query/document pairs attend together through the transformer and yield one raw classification logit. This differs from Session 14's independently encoded 384-dimensional vectors. Pin model/config/tokenizer bytes and the existing CPython 3.13.15/ONNX Runtime CPU 1.23.2 dependency wheels; never load repository-selected model code or download implicitly.

Heuristic intent chooses transparent source-type/exact-name preferences. Apply the actual reranker to bounded candidates and safe structural proposals; preserve baseline lexical/dense/RRF provenance. Sigmoid normalizes logits to a ranking scale, not calibrated probabilities. Implement span-based MMR: redundancy is actual same-file overlap rather than sharing identifier words, so complementary callers/helpers are not penalized just for vocabulary. An implementation/exact request reserves its first selected slot for the strongest implementation candidate, while documents and tests remain eligible.

Graph expansion traverses only resolved local CALLS, RECEIVER_OF, CONTAINS and local IMPORTS, in either direction with explicit reasons. Candidate/unresolved/external edges are excluded. Depth, fan-out, global additions, visited nodes, examined edges, verified input bytes and overall time are bounded. Syntactic resolution is never described as a proven runtime relationship.

Score token-aware, overlapping windows sampled deterministically across long chunks, always retaining the tail. Record scored/possible window counts, actual byte coverage, metadata/query truncation and the selected subspan. Use the same pinned WordPiece tokenizer as the declared target context tokenizer. Future LLMs require their own tokenizer policy; these counts are not universal LLM tokens.

The actual budgeted context is canonical structured JSON containing scope and complete evidence blocks. Count its entire encoded text, including hashes/headers/separators/source. Crop only exact UTF-8 suffixes of scored spans when necessary, recompute hash/coordinates, and verify the final wire text again. Source is a string value, never a tool directive. Foreign requests fail before candidate/model disclosure. Source/model/time failures return explicit outcomes without fabricated evidence or incompatible fallbacks.

The tradeoff is repeated immutable integrity verification and short-lived helpers rather than an inter-owner persistent model/cache. Measure this cost honestly. A–AB tests and five fixed query observations establish functionality and provenance, not a full quality benchmark. Reranking evaluation belongs to Session 16; LLM answers and agents remain later work.
