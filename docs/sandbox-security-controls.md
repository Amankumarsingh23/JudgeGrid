# Sandbox control and evidence matrix

Configured settings, retained Docker inspection, and workload behavior are separate evidence. The completion report records executed outcomes. Unsafe-policy fixtures are created but never started.

| Control | Implementation/configuration | Observation and regression | Limit |
| --- | --- | --- | --- |
| Non-root, capabilities, NNP | `docker.go`: UID/GID 10001, cap-drop ALL, no-new-privileges; retained validation | Existing policy tests; `/proc/self/status` CapEff zero and NoNewPrivs 1 | Shared kernel; no user namespace claim |
| Seccomp | Verified SHA-256, private read-only policy copy, inspect exact compact JSON | Filter mode 2, harmless unshare EPERM; unsafe retained policy rejected | EPERM attribution ambiguous between filter/capabilities/kernel checks |
| Pinned images/commands | Manifest IDs, fixed watchdog and commands inspected | Existing C++/Python matrix, invalid identity constructor test | Trusted image supply chain |
| Network/namespaces | Network none, private IPC/cgroup, non-host PID, no devices/extra hosts | Existing network probe; bridge rejected before start | Not kernel exploit isolation |
| Memory/tasks | Memory=swap limit, PID limit 16 | Existing bounded OOM/fork tests; cgroup files observed | Only OOMKilled evidence supports MLE |
| CPU bandwidth | NanoCPUs=one CPU, retained inspection | cpu.max quota equals period | Not accumulated CPU time; CPU metric null |
| Deadlines/descendants | Controller deadline plus in-container timeout --signal=KILL | Timeout, child, cancellation tests; orphan C watchdog survives controller death | Requires functioning kernel/daemon |
| Output | Incremental combined stdout/stderr budget, bounded compiler/control output | Existing output overflow tests | Detection/termination have scheduling latency |
| Descriptors | nofile 64:64 | EMFILE fixture | Per-process limit |
| Files/temp | fsize 16 MiB, core 0; tmpfs /tmp 64 MiB noexec/nosuid/nodev, shm 8 MiB | EFBIG and ENOSPC fixtures | No aggregate artifact-volume quota |
| Mounts/artifacts | Exact private volume at /work; writable only during preparation | Wrong mount rejected, concurrent source and read-only checks | Trusted compiler, bounded individual files |
| Fixed paths | Python O_EXCL/O_NOFOLLOW 0400; regular bounded C++ executable 0500 | Existing path contracts and concurrent source identity assertions | Base filesystem/compiler trusted |
| Secrets/hidden tests | Explicit environment; one selected stdin; trusted comparison | Host-secret sentinel, separate streams; judge API privacy tests | Agent and immutable test store trusted |
| Provenance/cleanup | Structured labels, immutable DB registry, exact ID/name/time match | Units, PG authority tests, orphan A–J | Unknown/legacy resources need verified operator review |
| Active work preserved | DB-time leases, generation comparison, per-execution lock | Orphan E/F current execution runs and renews after old cleanup | Fencing and process termination are distinct |
| Retry/concurrency | Re-list under lock; containers before volumes; REAPED after success | Two reconcilers, interruption/retry, repeat cleanup | Docker deletion is not transactional |
| DB outage | Authority required before deletion callback | Closed real PG pool preserves resources | Cleanup cannot progress during outage |
| Service separation | Dockerfile/Compose omit controller authority | `scripts/check-service-isolation.py` includes reconciler binary | Host tools intentionally have daemon authority |

Focused tests: `internal/execution/hardening_integration_test.go`, `internal/judge/orphan_integration_test.go`, `tests/sandbox_registry_integration_test.go`, provenance and reconciliation unit tests. Compilation and runtime share the retained-policy validator.
