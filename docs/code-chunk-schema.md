# Code chunk and analysis provenance

Migration 0015 adds `analysis_policies`, `analysis_indexes`, `analysis_file_attempts`, `analysis_files`, `analysis_symbols`, `analysis_imports`, `analysis_calls`, `analysis_edges`, `analysis_chunks` and `analysis_cache`. All source tables and migrations 0001–0014 are preserved. Domain rows store typed JSON metadata with relational index/file foreign keys; the canonical manifest is the authoritative immutable ordered reader representation. Raw source bodies are never duplicated in analysis storage.

| Record | Identity / provenance |
| --- | --- |
| Policy | canonical SHA-256 over version, parser/extractor digest and all analysis/chunking/budget settings |
| Index | hash of `analysis-v1`, exact source snapshot and policy |
| File | hash of index and immutable Session 12 file ID; exact source path, bytes/hash, grammar, parse status, exclusion reason, package |
| Symbol | index/file, local declaration position, kind, qualified name, lexical scope and exact offsets; parent/receiver/export/parser state |
| Import/call/edge | index, typed evidence payload, classification, source span and exact candidate target IDs |
| Chunk | index/file/symbol/kind/range/content SHA-256; symbol parent and language retained |
| Parse cache | repository + raw source digest + grammar variant + policy/extractor; no source body or snapshot-specific symbol IDs |

The manifest holds `schema`, `index`, `repository`, `snapshot`, `source_manifest`, pinned `commit`, `policy`, and ordered file/symbol/import/call/edge/chunk inventories. Its SHA-256 is computed over the exact canonical UTF-8 JSON bytes before PostgreSQL publication. Stats, cache counts, timings, generation and lease tokens are outside content identity. Typed JSON struct field ordering is part of schema 1. A new schema/extractor version is required for semantic changes; do not silently reinterpret old manifests.

Every `Span` uses **half-open raw-byte offsets `[start,end)`**, **one-based lines**, and **zero-based UTF-8 byte columns**. `end_line/end_column` point immediately after the span. A trailing newline can put the endpoint at column zero on the next line. Unicode character/grapheme counts differ from byte columns. Tree-sitter's zero-based row positions are not exposed directly. Source byte ranges, names and target strings are checked against the original verified UTF-8 bytes.

Small named symbols receive complete `symbol` chunks. Large symbols receive `symbol_part` chunks of at most the policy byte cap. The splitter prefers a newline in the latter half of the window, otherwise uses a UTF-8 boundary. The next part begins up to the configured overlap before the prior end, advanced to a rune boundary. Every part retains its symbol and lexical parent. Classes/types and nested declarations can intentionally overlap; chunk bytes can exceed unique source bytes. Non-symbol gaps receive bounded `file_region` chunks. Unsupported eligible text receives only file regions. Empty files have metadata and no chunks; excluded files have no chunks or content reads.

The publisher validates complete eligible-byte coverage and complete symbol coverage, valid same-file parents and spans, reference targets, per-file chunk counts and total row caps. Every retrieval first authorizes the authoritative repository owner, revalidates the READY source manifest, retrieves the immutable source file through Session 12 and checks range/hash/line provenance. A mutable path is never the authority for returned bytes. IDs are snapshot-local; equal content in another snapshot can share verified file-local syntax facts but receives reconstructed symbol/chunk identities and a freshly resolved graph.

READY rows/policies/cache are immutable under database triggers. Active writes require a database-clock-valid token and generation. File-attempt diagnostics are isolated by generation, and partial analysis is unavailable to the reader. The index manifest and all published domain rows commit together. This is the provenance boundary for future retrieval, not a semantic compiler graph or embedding schema.
