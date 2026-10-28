# Session 9: immutable object storage, compilation cache and safe rejudging

Date: 2026-10-28. Baseline: `01a6bae9c4cbeba1374c28763d8322ac8645aecf`.

## Implementation

Private canonical bundle bytes can now use an explicit S3-compatible adapter while preserving local operation. minio-go v7.4.1 performs bounded conditional publication, enabled-versioning checks and read-back verification. The evaluator still checks the complete sealed version reference before execution. Separate private artifact storage backs successful C++ caching. No S3 credential or trusted controller binary was added to ordinary service images.

The optional MinIO development overlay uses a persistent named volume, loopback-only endpoint and generated ignored operator credentials. Both attempted packaged-image pulls failed; a reproducible source Dockerfile builds upstream release commit `07c3a429bfed433e49018cb0f78a52145d4bedeb`. The upstream community server is archived, so this is an explicit development compatibility backend, not a maintained production-storage recommendation. SDK and server status were checked against [upstream SDK release](https://github.com/minio/minio-go/releases/tag/v7.4.1) and [server repository](https://github.com/minio/minio).

Migration 0008 adds durable STAGED/ACTIVE publication identities and fenced BUILDING/READY/EVICTED compilation metadata. Publication stages before uploading; verification precedes transactional version creation, suite sealing and activation. Retry resumes the same operator/key/file. Database/object storage dual writes are recoverable rather than atomic. Read-only staged/object inventories retain unknown and potentially referenced bytes; no automatic object GC is claimed.

Cache identity includes schema, exact source SHA-256, compiler path, pinned toolchain/image, flags, target/ABI, dependencies, build/security policy, seccomp digest and compile budgets. Fixed source timestamps and SOURCE_DATE_EPOCH remove date-dependent compiler inputs. Problem version is excluded because compilation receives source only. The supported cached artifact target is image-verified Linux/amd64 ELF. Local slots bound waiters and database claims provide single-flight across processes. Only an unexpired matching generation can publish READY after bounded artifact export, ELF validation and remote read-back. Every hit re-verifies bytes, prepares a new private workspace and uses fresh restricted runtimes. Compilation errors do not publish READY. Explicit metadata eviction permits safe rebuild without deleting bytes.

Migration 0009 adds logical evaluation jobs, independent job ownership/attempt generations, schema-three outbox references and guarded selected results. Existing initial workflow, ownership, event uniqueness and historical rows remain intact. New runs use the existing immutable result/evidence tables with a job link. The agent, heartbeat, finalizer, recovery scanner and sandbox registry resolve the correct scope. Original source/version/status/verdict never reset. Requests serialize to increasing ordinals, and only the latest requested job can select its committed result. An older finishing job cannot replace the selection. Migration 0010 persists cache-hit observations so historical preparation evidence is distinguishable from actual compilation.

The separate `judge-admin` development-only CLI publishes versions, requests owner-scoped rejudges, reads sanitized history, inventories staged/remote objects and explicitly evicts cache metadata. There is no public administrative endpoint. Public GET/list keep original version fields and expose only safe selected run/version/original-verdict metadata with the current displayed verdict.

## Required demonstrations A–R

| Case | Executed evidence |
| --- | --- |
| A | Real private MinIO canonical bundle publication and SHA-256/read-back verification |
| B | Missing object and deliberately changed latest object bytes rejected |
| C | Actual owned MinIO container restart; same immutable bundle remained readable and verified |
| D | Genuine C++ and Python AC evaluations using S3-backed tests |
| E | Equivalent C++ evaluations reused verified artifacts with no new compiler invocation |
| F | Changed flags/image identities yielded distinct keys and non-hit claims; arbitrary alternate compiler flags were not executed |
| G | Deliberately corrupted known cache object rejected before runtime; explicit eviction rebuilt safely |
| H | Ten concurrent identical real C++ requests: one compiler invocation, one cache build/miss, nine hits |
| I | Real child controller killed after actual compilation before cache publication; no partial READY; expired claim taken over |
| J | Original real AC source failed a newly published seventh edge case and produced WA |
| K | Old AC run/verdict/evidence remained in immutable history |
| L | Selected current result changed to committed WA while original source/version/verdict stayed sealed |
| M | Duplicate rejudge key returned one job; injected lost XADD reply triggered republishing; duplicate delivery had one set of effects |
| N | Real PostgreSQL stale rejudge-attempt finalization rejected; stale cache-builder publication rejected after takeover |
| O | Actual MinIO stops during bundle fetch and during cache publication produced null-verdict infrastructure failures; restart recovered genuine results |
| P | Anonymous private-object GET returned 403; public result serialization and service isolation checks exposed no hidden test data or credentials |
| Q | New executions closed their registry records; killed-controller resources were reconciled with DB authority; unknown resources were preserved |
| R | Previous sandbox, local-judge, agent, orphan, outbox, race and distribution regressions rerun |

The six new behavioral runtime functions are in `session9_integration_test.go` and `session9_fault_integration_test.go` under explicit `agentintegration,session9integration` tags. The child helper is excluded from behavioral counts. Direct database-contract tests use fixture reports/ELF bytes without pretending they are execution demonstrations; all verdict demonstrations above ran actual C++/Python under Docker.

## Observed correctness and performance

The final six-function fault run observed ten concurrent C++ evaluations in 26.86 seconds on the trusted Windows controller/Docker Desktop Linux engine, using two execution slots: **one actual compiler invocation, one cache build/miss and nine verified hits**. The earlier equivalent run took 20.56 seconds. These are small environment-dependent end-to-end observations, not an isolated cache latency or throughput benchmark. Each request retained private preparation, fresh runtime and cleanup overhead. Changing key identities was tested through real claims, not through arbitrary untrusted compiler configuration.

Cache poisoning was rejected without any increase in runtime starts. Restoring bytes and explicitly evicting metadata caused a new verified compilation. Killing the builder left BUILDING; the replacement reached generation two READY, and the original publisher could not finalize. A cache-store outage performed two actual compiler invocations across failure/recovery and exposed no user verdict during failure. Bundle-store outage recovered real Python AC after restart. No CPU/peak-memory measurements were invented.

The object-backed rejudge fixture used an otherwise correct addition program with an intentional `42,0` edge defect. Six original cases yielded AC. A new immutable suite added the edge case; rejudge yielded WA using the cached artifact, with zero additional compiler calls and old AC history retained. An additional run during concurrent image builds failed its Python AC assertion; final dedicated verification and the live demonstration are recorded with the delivered results rather than counting that failed run as passed.

## Verification and operational evidence

Local counts are 55 unit test functions, 38 PostgreSQL/Redis behavioral functions (34 prior plus publication/rejudge/cache contract tests), 30 sandbox scenarios, 11 durable judge scenarios, 16 baseline agent scenarios, ten orphan cases, and six new real object/cache/rejudge fault functions. Formatting, vet, normal/race unit tests, tagged PostgreSQL/Redis race tests and executable builds passed. Native runtime controllers have no race instrumentation; hosted Linux CI runs tagged runtime suites with `-race`. No missing prerequisite is silently skipped or counted as passed.

The previous 20-way submission and ownership races, 50-job/four-slot/two-instance distribution, outbox crash/uncertain XADD, verdict matrix and authority separation remain covered. API/worker/relay Compose configuration contains no Docker socket or S3 credentials, and their images omit judge-admin and all other trusted controllers. Runtime images and seccomp identity remain pinned. The MinIO source image and updated ordinary application images built successfully with TLS verification enabled.

Live CLI/API evidence, final cleanup/default restoration, pushed commit and observed hosted CI outcome are supplied with the final delivery. Learning notes stay ignored at `learning-notes/09-session-nine-learning-notes.md`.

## Remaining limits and Session 10 handoff

The local MinIO backend is an archived source release; deployment needs maintained storage, separate least-privilege reader/builder/operator credentials and suitable TLS/access controls. The demonstration uses generated trusted operator credentials. Versioning/conditional creation and hash verification do not implement privileged-administrator-proof object retention. Automatic object garbage collection and retention policy are deferred; unknown/staged/evicted objects are deliberately retained. Object auditing lists bounded latest keys and does not delete historical versions.

Cache builds use bounded leases rather than continuous build-lease renewal; an overrun fails fenced publication and can be retried. ELF validation is not a malware scanner; the trusted compiler/image/controller/database remain authoritative. The cache currently supports amd64 only. Public history pagination and remote administration are not added. Current GET/list selection enrichment uses bounded per-item reads; larger-scale optimization is future work.

Session 8 shared-kernel/Docker trust, aggregate artifact-volume quota gaps, temporary policy-file crash leftovers and null CPU/peak-memory measurements remain. The older three-resource unknown execution remains preserved because its original registry is unavailable. No broad prune or unknown-object deletion was used. Session 10 can build on immutable job/run/version/object identities without reopening sealed submissions or weakening these boundaries; no Session 10 feature was begun.

## Live CLI/API evidence and delivered local state

The fresh trusted principal was `aman-s9-live` on isolated stream `test:judgegrid:s9-live`. Actual API/outbox/Redis/host-agent execution with S3 bundles produced C++ AC/WA/CE, Python AC/RE/TLE and unsupported-Go FAILED/null verdict. The live API/dispatch/outbox regressions also passed, including one HTTP creation plus 19 replays and one durable dispatch receipt for duplicate deliveries.

The administrative CLI published problem version ID **33** (version number 2), then created rejudge job `7e852bb6-0072-478e-be47-7ffa31837de4` for submission `83d4dc20-0dab-40ef-b066-a0c07a1b1130`. Original run `566cfe25-7763-4d6c-9130-f4922e45b680` remains **AC**, original version ID 1, six of six cases passed. New run `17a45ba6-189e-4f86-a91c-731198cd79d1` committed **WA**, six of seven cases passed, with durable `cache_hit=true`. HTTP GET displayed WA while retaining original AC/version/source metadata. Repeating the administrative request returned the same job. Repeating publication returned the same version. A publication containing an unknown runtime-image field was rejected by the strict decoder before activation.

The dedicated follow-up runtime test passed in 26.33 seconds, including persisted cache-hit history and uncertain XADD recovery. The earlier failed supplemental assertion was not counted as passed. The CLI staged inventory found the one ACTIVE publication, and the read-only object audit retained all three referenced bundles and three referenced compilation artifacts.

Redis pending count reached zero. Both initial and job ownership had zero ACTIVE rows; all 14 public-schema sandbox records (including six retained from Session 8) were CLOSED. Host agent, relay and MinIO were stopped after the demonstration; persistent object data was retained. Final default-auth restoration and hosted CI are checked during delivery. Credentials, publication files, runtime manifests and learning notes remain ignored.

A final compatibility check also made cached C++ preparation restore its own main.cpp with the same read-only mode and fixed timestamp as a cold build. The real ten-request fixture reads that source file, so both the one cold execution and all verified hits must observe the same private workspace. Cached executable bytes are never mounted through a shared writable volume.
