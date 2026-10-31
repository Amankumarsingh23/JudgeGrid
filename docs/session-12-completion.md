# Session 12: commit-pinned private repository ingestion

Baseline: `56ea76e421eca19066362fdecedd4e9aae04521e`, verified Session 11. This session adds an independent repository-intelligence ingestion domain and retains the online judge. It implements no AST/symbol extraction, Tree-sitter, embeddings, BM25/vector retrieval, reranking, inference or autonomous agent execution.

## Architecture and authority

The explicitly enabled trusted `repo-ingest` CLI registers approved repositories, resolves exact commits, ingests/retries, inspects status/inventory, retrieves verified private files, compares snapshots and performs read-only storage audits. There is no public endpoint or Docker authority. Repository owner binding is authoritative in PostgreSQL; foreign scope is rejected before object retrieval. Normal services receive neither source credentials nor the CLI/Git controller.

The Git reader uses a clean environment and our temporary bare configuration, reads raw objects in one bounded batch process, ignores repository hooks/filters/includes/replacements/remotes and never reads working-tree files. SHA-1 and SHA-256 fixtures both pass. Controlled HTTPS transport uses exact origin allowlisting, pinned public DNS/TLS and a bounded loopback smart-HTTP broker, with no redirects, ambient proxies/credentials, SSH, submodule or LFS fetch. Previously published commits can seed bounded mirror negotiation; cached exact OIDs need no new remote fetch. Remote HTTP response-body bytes are reported separately from source Git blob bytes.

Forward migration 0014 introduces repositories, immutable policy identities, snapshots, verified source catalogs and immutable file manifests. Snapshot identity hashes schema/repository/object format/full commit/policy; the canonical manifest also records root tree and byte-sorted exact paths/modes/types/OIDs/sizes/languages/reasons/raw hashes where examined. File IDs are stable for that snapshot/path. Source contents are repository-scoped SHA-256 objects in separate private storage. S3 requires versioning/conditional publication/readback, has independent credentials/volume, and never falls back to local storage. A rooted local adapter is also provided.

Claims use independent database-clock leases, tokens and monotonic generations. Only an unexpired VERIFYING claim can atomically insert the complete inventory and publish READY. Retry/takeover preserves snapshot identity; stale owners cannot renew or publish. Unknown/staged objects are retained. Audits return opaque object references rather than storage keys. Published provenance remains immutable; reads fail closed on missing/corrupt bytes.

See [ADR 0012](adr/0012-commit-pinned-repository-snapshots.md), [operator workflow](repository-ingestion.md) and [schema/typed reader](repository-snapshot-schema.md) for exact contracts, cost limits and Session 13 use.

## Actual bounded evidence

Synthetic Git A contained five eligible files. B changed three blobs: **three fetched / two verified reused**, **48 newly fetched bytes / 35 reused bytes**, and the complete five-file manifest. Renaming `main.go` to `renamed.go` and changing only its mode to `100755` fetched **zero** blobs and reused **five**. Duplicate registration/ingestion retained identity; changing the docs policy created another identity. Another owner/repository with identical contents fetched its own five references instead of probing/reusing another tenant's catalog.

The deliberately killed helper exited after one verified upload/catalog write. No partial READY existed; generation two resumed with **four fetched / one reused**. S3 TCP-gate failure and poisoned current object bytes both refused READY; restored storage/content permitted safe retry. The exporter was absent throughout that fault test; publication still succeeded and bounded failure/byte counters were checked. Anonymous source-object HTTP access returned 403. A controlled real HTTPS smart-Git fixture verified fetch, subsequent commit/tree completeness, and missing-SHA refusal. Its tiny initial/following response-body samples were 519/668 bytes; this establishes no general bandwidth gain.

The real Session 11 JudgeGrid commit was ingested repeatedly through actual PostgreSQL and separate source MinIO storage, not the working tree. Its root tree is `2f2f17b6f2b3cb85d1988c0e3141f63e1a643930` and its inventory contains **264 entries: 207 included regular files, 48 directories and nine excluded regular files**. Included bytes: **977,091**. Exclusions: one generated/lock item and eight unsupported file types; directory rows are explicitly classified separately. The initial attempt fetched 207 file contents, and READY replay kept the same snapshot/manifest identity and verified stored bytes without new Git blob reads.

One native Windows integration observation measured 11.517 seconds initial / 1.519 seconds verified replay. A separate Linux race observation measured 15.843 / 3.329 seconds. These are different instrumented/host/filesystem populations, not comparable production capacity. The original immutable publication stats and current call's `attempt` stats are separate to prevent repeat calls reporting historical fetches as new work.

## Retained private CLI snapshot

The actual trusted native CLI also ingested the exact baseline into an owned persistent `judgegrid_s12_lab_*` schema with independent private source storage. Local owner scope is `aman-s12`; ignored `bin/s12-lab.json`, trust file and source-store environment file retain the operator setup for Session 13. Credentials, storage keys and raw source copies are not committed.

| Evidence | Observed value |
| --- | --- |
| Local snapshot | `4dc8667343e1b42e7d57fe6ef03b4418087f71cc6fc38c3a4d3413416e8805d8` |
| Local manifest digest | `132315c415dca198405a231ff1297787a825fdfb6eda378a337d82abb253f97a` |
| Policy | `e3019f4451cd8d563498728b65db30e358123d4bec51e50480c748e985ba5e1f` (default source-v1) |
| Local initial attempt | 7.284s, 207 Git blob reads / 977091 bytes; source 0.735s, storage 4.119s, DB including commit/status 2.195s |
| Verified READY replay | 0.983s, zero new Git blob reads, 207 verified reused references / 977091 bytes |
| Actual approved GitHub HTTPS snapshot | `8a41cccc69ac0282886c5ac565db50a537619b2011f2a72a1be898e4ca6e49d0` |
| HTTPS manifest digest | `782774cbc18d2a6603f3ccae10249a4ebd17b5b5d2c56703ff5ae24b7d51b33e` |
| HTTPS initial attempt | 10.552s, 384524 HTTP response-body bytes; same exact commit/tree and source inventory |

Comparing local versus HTTPS inventories returned an empty difference list. Their different repository scopes correctly produce different snapshot/manifest/file identities. An independent Python check reproduced the local canonical manifest/snapshot/file hashes, sorted inventory counts and raw byte total. Authorized extraction of `internal/storage/ownership.go` matched both Git's exact baseline bytes and raw SHA-256 `851b2052667713d6b0c2faed407a1d0771b7114f21be903049b8ca6920bc0791`. Its standalone verification copy uses `.txt` so it cannot accidentally become a Go package.

The retained schema had two READY snapshots, zero live ingestion claims, 528 manifest rows and 414 verified catalog references. The local read-only audit reported 208 opaque references with no exposed object keys. No remote bandwidth/performance gain is inferred from comparing these tiny initial samples. The existing mirrors and object volume are retained; no unknown bytes are deleted.

Default source limits are 10000 entries/depth 32/2000 candidates/1 MiB per candidate/16 MiB total/one reader/120 seconds; policy ceilings and metadata/path/manifest limits are explicit. Linux tests inspect the running Git process's actual 512 MiB address-space/60 CPU-second limits and verify cancellation. Windows starts Git suspended, attaches a 512 MiB kill-on-close job and resumes it; cancellation closes the job to terminate descendants. The Go CLI's 64 MiB heap target is soft. Peak RSS/CPU usage is not measured or invented.

## Fault/security matrix

| Case | Observed assertion |
| --- | --- |
| A | Same commit/policy gives the same snapshot and manifest; READY replay performs zero new Git blob reads |
| B | Branch movement after resolution and dirty working files cannot change the pinned commit/tree |
| C | Complete B inventory, three changed blob fetches, two verified reuses; no diff-only reconstruction |
| D | Rename/executable mode preserved with zero new content fetches and correct comparison |
| E | Actual helper death after upload: no READY, expired takeover, generation two and verified reuse |
| F | Two real ingesters race; second active claim refused, one authoritative READY publication |
| G | Expired generation cannot renew or publish after takeover |
| H | Real source-S3 TCP outage refuses READY, then restores and retries safely |
| I | Privileged fixture corruption before verification is detected; matching restored bytes permit retry |
| J | Symlink target is never followed or returned as source bytes |
| K | Gitlink recorded as excluded metadata; no recursive fetch |
| L | LFS pointers classified; no external materialization |
| M | Oversize, binary, invalid encoding, generated/minified, dependency, sensitive-path and secret-evidence fixtures classified |
| N | Traversal, malformed UTF-8, non-NFC/case aliases and unsafe path forms fail closed |
| O | Foreign metadata/file/audit denied before object access; anonymous source objects forbidden; repository-scoped reuse |
| P | Duplicate registration/ingestion stable; rebinding denied; distinct policy identity |
| Q | Actual synthetic entry/depth/candidate/byte/path/blob and cancellation boundaries, plus kernel Git memory/CPU limits |
| R | Unknown object survives the bounded read-only reconciliation inventory |
| S | Existing judge regression and secure-default checks run separately; final results below |
| T | Exact real JudgeGrid baseline: actual counts/bytes/root tree, repeated manifest reproducibility |

A–R and T have successful actual fixture/integration evidence. Final whole-suite results classify delivery; no helper-only invocation or unavailable prerequisite is counted as a behavioral pass. Early fixtures mistakenly removed index-only special entries with `git add` and lost the crash helper's isolated schema when serializing its DSN. Both fixture bugs were corrected. The initial kernel-limit test observed `prlimit` before it execed Git; an object-protocol handshake now establishes that the inspected process is actually Git. Neither fix weakens limits or authority. A smart-HTTP POST framing failure was corrected with a bounded body and explicit content length. A temporary shallow fetch ref is intentionally replaced only in our private view, never via force push.

## Regression, restoration and delivery

Formatting, vet, normal/race unit tests and executable builds passed. Linux now has **78 unit-test functions**: the prior 69, seven Git/policy/storage/transport functions, one Linux kernel-limit/cancellation function and one ingestion metric-cardinality function. All prior **45 PostgreSQL/Redis behavioral functions** passed in an uncached race run; six new ingestion behavioral functions passed with actual PostgreSQL and separate source MinIO. Child-helper-only functions are excluded from these counts. The new tests also completed natively on Windows; native Windows runtime binaries are not race-instrumented.

The complete sequential native platform run passed: **30 sandbox scenarios, 11 local judging scenarios, 16 baseline agent scenarios, ten orphan cases, six Session 9 object/cache/rejudge fault functions, the Session 10 fair scheduler/SSE/rejudge matrix and Session 11 combined exporter/Redis fault**. The automatic judging pipeline, DB heartbeat outage, every orphan group and both C++/Python rejudge paths passed in this whole invocation. The new ingestion matrix A–T is classified PASS with the retained CLI proof plus these regressions. Earlier Session 11 Windows timing failures remain historical qualifications; they are not counted as current passes or failures. Initial Session 12 fixture/verification failures are disclosed above, not silently counted as successes.

An extracted `.go` verification artifact initially created an unintended package under ignored `bin/` and failed vet. Its exact bytes were moved to a `.txt` file, the operator example was corrected, and the final full Go/DB verification passed. Git ignore rules do not control Go package discovery. No authority, lease or sandbox limit was weakened to fix a test.

The current delivery CLI reverified the retained READY snapshot with zero new Git blob reads after its final rebuild. The Docker host reported 16 CPUs, 8,130,551,808 bytes memory, Docker 29.9.2, kernel 6.18.54.1-microsoft-standard-WSL2/cgroup v2. The independent source MinIO profile is capped at 768 MiB/one CPU. These limits and small samples do not establish a capacity benchmark or measured peak RSS.

Both owned demo object stores are stopped; their volumes/private bytes and the two READY source snapshots are retained for authorized Session 13 use. No ingester/controller remains running and no live ingestion claim remains. Unknown older resources are preserved. Normal API/worker/PostgreSQL/Redis were restored healthy with production defaults, development auth/dispatch/telemetry off and database/Redis host ports removed. Live checks returned submissions=401 and local session/root UI/metrics/repository routes=404. Service-isolation/controller-exclusion and Compose configuration audits passed. No graphical dashboard inspection is claimed.

Migration 0014 was applied and checked in the normal database as well as the retained lab. All fourteen disk/index/applied SQL checksums are verified at delivery; original migrations 0001–0013 must match the baseline bytes exactly. Source credentials, mirrors, evidence, raw verification copies, runtime manifests and learning notes remain ignored/unpushed. The authorized pushed revision and observed hosted workflow result accompany delivery; no pending/skipped hosted run is described as passed.

## Remaining limits and Session 13 handoff

The operator, controlled filesystem, Git executable and privileged database/storage administrators remain trusted. Git process limits are resource controls, not an arbitrary-hostile-parser isolation claim. Secret scanning is conservative and cannot prove absence; excluded raw contents may remain in the approved original/private mirror Git database, but are not published as source blobs. Published object references cannot prevent privileged backend deletion/corruption; every file read verifies bytes and fails closed. No cross-tenant deduplication, private remote credential flow, ref-storage reftable/worktree-gitfile support, unbounded repository scan, automatic retention/GC, public browsing, production identity/deployment or peak resource/throughput claim is provided. The archived MinIO development-image qualification remains.

Session 13 should consume the READY typed reader with exact owner, snapshot/file IDs, raw bytes, language and commit/tree/OID/policy provenance. Analysis should retain those identities and keep repository text untrusted. It must not re-read a mutable branch or the working tree. This session does not start symbol extraction or retrieval features.
