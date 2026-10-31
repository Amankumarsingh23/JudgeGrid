# Local embedding policy

The authoritative artifact versions and SHA-256 digests are in [`model.lock.json`](../internal/retrieval/helper/model.lock.json). The model is `sentence-transformers/all-MiniLM-L6-v2` at revision `1110a243fdf4706b3f48f1d95db1a4f5529b4d41`. Its model card declares Apache-2.0. We use the 90,405,214-byte ONNX export with SHA-256 `6fd5d72fe4589f189f8ebc006442dbb529bb7ce38f8082112682524616046452`, not pickle weights or repository-provided Python. The model card, configuration and tokenizer are also pinned. Artifact provenance is trust in that upstream publication and the reviewed operator setup, not a cryptographic attestation of the model's training data.

Runtime pins include CPython 3.13, ONNX Runtime CPU 1.23.2, tokenizers 0.22.1 and NumPy 2.3.4; all transitive inference dependencies are explicitly pinned with wheel hashes for Windows AMD64/Linux x86-64. Install with `--no-deps --only-binary=:all: --require-hashes`; tokenizers' unused Hub download dependency is deliberately absent. The helper checks installed versions and artifact bytes before inference. Ordinary services ship none of this toolchain.

Tokenization uses the pinned tokenizer JSON, special tokens, right truncation at **256 tokens**, dynamic batch padding and its attention mask. A single CPU thread runs the actual transformer ONNX graph. Attention-weighted mean pooling excludes padding, then L2-normalizes a finite float32 vector of exactly **384 components**. Both query and document use this same vector space. No task prefix is required by this symmetric model. Documents use exact UTF-8:

```text
language: <language>
path: <snapshot path>
symbol: <qualified name or empty>
code:
<exact raw chunk bytes>
```

Queries use the exact supplied query text. Document context consumes part of the 256-token allowance; long code tails can be truncated. No claim of complete chunk semantic coverage is made. No hidden judge tests or execution workspaces are embedding input. Only owner-authorized source snapshot chunks reach the model.

Canonical input identity hashes `context-content-v1` and the entire exact input, including language, path, symbol and raw bytes. The cache key additionally contains immutable repository UUID and model-policy digest; identical bytes under a different context cannot collide. Model identity hashes the helper and artifact/runtime lock. Lexical tokenizer and retrieval/scoring policies independently version the immutable retrieval index. New ranking weights may reuse compatible model/input vectors; changing model/tokenizer/encoding identity cannot. There is no cross-owner presence/cache-hit API.

The helper accepts at most 32 documents, 20,000 UTF-8 bytes each, over stdin JSON. The parent caps output at 1 MiB and kills it at 120 seconds or parent cancellation. Linux enforces a separate 2 GiB virtual-address/120 CPU-second envelope; Windows attaches the suspended process to a 2 GiB kill-on-close job. Original Git/syntax helper limits stay 512 MiB/60 CPU seconds. CPU threading is fixed to one. These are limits, not measured peak memory/CPU. Inference uses isolated Python, a trusted temporary helper, no repository imports, no custom ONNX operators, and CPUExecutionProvider only.

The typed embedding backend also declares its immutable model-policy identity. Builds and dense queries reject a backend whose identity differs from the index policy before inference, even if it could return a vector with the expected dimension.

Public model provisioning:

```sh
python -m pip install --no-deps --only-binary=:all: --require-hashes -r internal/retrieval/helper/requirements.lock
python scripts/setup-retrieval-model.py --directory /absolute/private/model-root
```

The setup script downloads only the fixed pinned public artifact list and verifies exact sizes/hashes before replacing local files. `--ca-file` optionally adds a trusted local CA without disabling TLS. This provisioning step sends no private code. Actual inference does not call Hugging Face, a hosted embedding API or any other network service. CI provisions the same real weights and uses actual inference; it does not substitute hash vectors or placeholder model output.

Primary references: [upstream model card](https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2), [ONNX Runtime Python inference](https://onnxruntime.ai/docs/api/python/api_summary.html).
