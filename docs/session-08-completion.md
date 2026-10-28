# Session 8 completion: sandbox hardening and safe orphan reconciliation

Date: 2026-10-28. Baseline: Session 7 `be9e58d687dcfc5f09175a0c685736f7f726d894`.

## Security changes

Both compiler/preparation and runtime containers now validate retained image identity, fixed command/watchdog, exact private artifact mount, tmpfs, ulimits, init/logging, device/capability restrictions and the exact pinned seccomp JSON before execution. Existing UID, namespace, network, cgroup, root-read-only and environment checks remain mandatory. Docker control output is bounded. Invalid policy configuration is typed infrastructure failure. C++ artifacts are checked as regular, non-symlink, size-bounded executables with restrictive permissions; Python source uses exclusive no-follow creation at a fixed path. Runtime artifact mounts remain read-only.

Pinned C++ image: `sha256:b8b2c5dcdf1884533d7123f61f7263d417fd78cfab861cda0e554e8c74dd1169`. Pinned Python image: `sha256:240510229e41a1b9ea7fee1843fb3b6e80d6d91ad339b023bf61db21fb1e7b6d`. Seccomp SHA-256 remains `6416b47770785a41ac59073cdc77d9fe98517df2799dc83ef207e622de3053f6`.

See the [control/evidence matrix](sandbox-security-controls.md). Configured restrictions do not prove all hostile programs are contained. The new syscall fixture separately observes filter mode 2, NNP 1 and zero effective capabilities. A harmless unshare call returns EPERM; attribution specifically to seccomp remains uncertain. CPU quota is bandwidth, not total CPU time. CPU time and peak memory remain unavailable/null.

## Ownership and lifecycle

Migration 0007 adds immutable sandbox provenance linked to the existing evaluation run/submission/generation. Every phase container and artifact volume carries matching structured labels with namespace, controller, owner hash, timestamp and resource role. The trusted judge registers provenance before creating resources. Creation and reaping serialize using a per-execution database advisory lock. Active current ownership is preserved, and stale cleanup avoids locking a newer owner's heartbeat row.

The separate host-only reconciler defaults to dry-run. Explicit opt-in plus apply rechecks database authority and re-enumerates matching resources under the lock. It removes containers by exact immutable ID, then volumes after identity/creation/labels revalidation. Unknown or inconsistent provenance is preserved; unavailable database authority prevents deletion. Inventory is bounded and retries are idempotent after partial physical cleanup. REAPED is committed only after successful cleanup. Ordinary API, worker and relay images retain no daemon access or controller binaries.

See [ADR 0008](adr/0008-sandbox-provenance-and-reconciliation.md), [operations](sandbox.md#session-8-trusted-orphan-reconciliation), and the updated [threat model](sandbox-threat-model.md).

## Crash matrix observed on the real Linux Docker engine

| Case | Executed evidence |
| --- | --- |
| A: killed after creation | Child controller forcibly terminated after Docker create/verify; resources reaped after lease expiry |
| B: killed during runtime | Real running orphan confirmed after DB lease expiry, then terminated and removed |
| C: watchdog after controller death | Runtime stopped independently before reconciliation |
| D: volume only | Container removed, remaining artifact volume reconciled |
| E: stale generation | Old generation resources removed after takeover |
| F: active current generation | Current resources preserved; program ran and lease renewed |
| G: unavailable authority | Closed real PostgreSQL pool refused cleanup; resources unchanged; restored authority cleaned up |
| H: reused/inconsistent identity | Same-name volume recreated with inconsistent labels; stale snapshot deletion and automatic cleanup refused |
| I: concurrent reconcilers | Two real PostgreSQL/Docker reconciliations completed without double-delete failure |
| J: interrupted cleanup | Failure after first actual deletion; retry removed remaining resources; repeat harmless |

A/B/C/D/I/J forcibly terminate real child controller processes. On Windows this is Process.Kill; hosted Linux CI uses the same Go call, which sends SIGKILL. J injects interruption after a successful physical deletion; it does not claim an additional OS process kill. H exercises inconsistent-label volume-name reuse, not an impossible same immutable container-ID reuse. All ten cases passed in the final dedicated orphan run. Six measured apply scan/cleanup durations ranged from 1.48 to 3.12 seconds with the bounded fixture inventory.

The actual host reconciler CLI also ran against a registered orphan left by an interrupted test: default dry-run reported three eligible resources, explicit apply reaped them, and unknown resources were preserved. One older test execution whose registry schema was already removed remains intentionally unknown (three stopped/created resources); no automatic deletion authority was inferred from its name. Standalone fixtures remain deliberately unregistered.

## Validation

Executed local results and performance observations follow. The final delivery message records the commit and observed hosted CI result.

The local environment was Docker Desktop 29.9.2, Linux WSL2 kernel 6.18.54.1-microsoft-standard-WSL2, cgroup v2; Go 1.26 builds/checks ran in Docker, while native Windows test controllers exercised the Linux daemon. One initial orphan run was interrupted by an accidental Compose PostgreSQL recreation; the affected cases were rerun after restoring the test overlay. This infrastructure interruption is not counted as a successful test.

### Completed regression checks

- `gofmt`, `go vet ./...`, `go test ./...`, `go test -race ./...`, application builds: passed with Go 1.26.
- PostgreSQL/Redis tagged vet and race tests: 34 behavioral test functions passed (32 previous plus two registry tests), including outbox fault injection, 20-way submission/ownership races and the 50-job/two-instance/four-slot worker matrix.
- Trusted agent baseline: all 16 previous automatic/crash/outage/drain scenarios passed before the unrelated database recreation; the separate final ten-case orphan run passed in full.
- Compose config, migration check/seed, health and service isolation audit: passed. Controller binaries, including the new reconciler, remain absent from ordinary service images.
- Live API smoke: health/readiness, bounds, conflicts and 20-way HTTP idempotency race passed (one create, 19 replays). Dispatch CLI/worker: two deliveries, one receipt, empty PEL, submission still QUEUED. Outbox smoke: one committed intent and publication, unchanged submission state.
- Unit suite count: 50 test functions (44 previous plus six provenance/reconciliation tests).
- Restricted sandbox suite: 30 scenarios passed (22 previous plus eight hardening scenarios). Descriptor EMFILE, file-size EFBIG, tmpfs ENOSPC, cgroup/syscall observations, concurrent private workspaces and three unsafe retained policies were exercised. Both concurrent sources saw only their own source/tmp file, no injected database secret, read-only artifacts and correctly separated stdout/stderr.
- Durable local judge suite: all 11 previous scenarios passed, including C++/Python AC, WA, CE, RE, TLE, OOM-evidenced MLE, stdout/stderr OLE and empty/token/exact-output isolation. AC compiled C++ once and started six fresh runtime containers.
- Native runtime suites were sequential on one daemon. They are not race-instrumented; hosted Linux CI runs the tagged suites with `-race`. The Go unit and PostgreSQL/Redis suites were race-instrumented locally.
- Reconciler CLI guards rejected production mode and missing opt-in. No skipped fixture is counted as passed.

### Performance observations

Identical tiny cpp-hello/python-hello requests, three samples each, measured by the same host-controller harness before/after hardening. Milliseconds below are medians. Preparation includes capability checks, creation and compile/source preparation; run includes creation/verification, execution and per-case removal; cleanup is Prepared.Close. These are end-to-end phase durations, not isolated Docker startup or CPU-time measurements.

| Workload | Prepare before / after | Run before / after | Cleanup before / after |
| --- | --- | --- | --- |
| C++ hello | 2008 / 1696 | 997 / 1038 | 252 / 543 |
| Python hello | 1515 / 1464 | 898 / 1090 | 256 / 471 |

The final after samples ran without another runtime suite. Baseline samples were collected before the interruption/restart, so cache/host load and Docker state are not controlled. Three observations are illustrative, not a throughput claim or causal speedup estimate. Extra retained-policy/provenance inspections add work, particularly during cleanup. Correct authorization takes priority over speed. The standalone harness does not measure database registry overhead; the real agent/crash suites exercise that path separately.

### Real host-agent demonstration and final state

Fresh development principal `aman-s8-auto`, isolated stream `test:judgegrid:s8-auto`, real API/outbox/Redis and native trusted host agent:

| Fixture | Submission | Observed terminal result |
| --- | --- | --- |
| C++ correct | 4efe559a-0085-4238-9856-c1b54fe0af85 | COMPLETED / AC |
| C++ wrong | ece659c4-81cf-463f-a75d-d0f6f331989a | COMPLETED / WA |
| C++ syntax | 10af5066-7cfb-48b2-b6f1-153d1b25a4ed | COMPLETED / CE |
| Python correct | 97db53f4-6251-4754-8493-ad6a03962d28 | COMPLETED / AC |
| Python exception | 9bb5763d-a5bf-414d-a6ea-cb8e491fe759 | COMPLETED / RE |
| Python loop | fbc68f9d-882c-4dbe-9849-26b81ab0976f | COMPLETED / TLE |
| Unsupported Go | 5e2aa1f0-1a54-431d-ad7a-e9cdc15c0820 | FAILED / null verdict |

All six executed submissions used generation 1 / ownership DONE. Redis pending count reached zero; all six public-schema sandbox registry records were CLOSED and no active demo ownership remained. Host agent and relay were stopped. Final dry-run preserved only the three-resource older unknown execution described above. No new demo sandbox resource remained. Default production/auth-disabled configuration is restored after the smoke check; learning notes, runtime manifest, build certificates and private test bundles stay ignored.

## Remaining limits and Session 9 handoff

Shared kernel, trusted daemon/controller/image supply chain, no VM isolation or user-namespace claim, no verified aggregate named-volume quota, and no reliable CPU/peak-memory accounting remain. Per-file bounds do not establish aggregate disk quotas. A future bounded compile workspace needs quota-backed storage or bounded tmpfs and a verified artifact handoff. Docker lacks atomic compare-and-delete for volumes against a malicious daemon administrator. Crashed controllers can leave host temporary seccomp copies; this reconciler never accepts label-selected host paths. Unknown legacy resources require separate verified operator review.

Session 9 may build immutable problem version/object-storage, compilation caching and explicit rejudging work on these run/version/generation boundaries. None of those new features was added here. Keep cache identity tied to trusted compiler/runtime policy and preserve authoritative fencing when designing new attempts. Local learning notes remain ignored at `learning-notes/08-session-eight-learning-notes.md`.
