# ADR 0008: Verified policy and database-authorized orphan cleanup

Date: 2026-10-28. Status: accepted for explicitly enabled local development.

A controller may die between creating a resource and removing it. Go `defer` cannot run after process termination. PostgreSQL result fencing prevents stale writes but cannot terminate an existing Linux process. These require separate controls.

Each preparation receives random execution and controller IDs. Every artifact volume and phase container carries the exact namespace, execution, controller, run, submission, generation, owner hash, creation timestamp and role. Before creation, the trusted judge registers immutable provenance in `sandbox_executions`, tied to the existing evaluation attempt. Standalone unregistered fixtures remain supported but cannot be automatically reaped.

Creation and reconciliation serialize with a transaction-scoped advisory lock per execution. Creation checks an OPEN registry entry and current unexpired ownership. The lock covers Docker creation and retained-policy verification, not compilation or execution. Heartbeats remain independent. Reconciliation compares every provenance field with PostgreSQL and checks current generation and lease using database time. It preserves active work and refuses unknown or inconsistent ownership. For an expired current generation, a row lock serializes authorization with renewal/takeover. Removing an older generation does not lock the newer owner's row.

`sandbox-reconcile` is a separate trusted host command, absent from ordinary service images. Development mode and explicit opt-in are required; dry-run is default. Explicit `--apply` re-enumerates under the execution lock, validates relationships, removes containers before volumes, and records REAPED after successful cleanup. Inventory is bounded to 128 resources and a command to 60 seconds. Reports contain execution IDs, counts and decisions, never source or hidden output. Database failure authorizes no deletion. Interrupted physical cleanup rolls back metadata; retries handle remaining resources. Missing resources are harmless.

Containers are deleted by immutable Docker ID. Volumes expose names: deletion rechecks name, creation time, labels and role. Docker has no conditional volume-delete operation. A malicious Docker administrator can race those checks and is outside the trust boundary. Trusted controllers never reuse generated names, and REAPED registry entries cannot authorize creation.

Both phases inspect retained image, command/watchdog, fixed mount, UID, namespaces, cgroups, read-only filesystem, tmpfs, ulimits, capabilities, environment and exact pinned seccomp JSON before start. Policy errors remain infrastructure errors. Source preparation uses fixed paths, exclusive no-follow Python creation and restricted source/executable permissions. Runtime artifact mounts are read-only. Pinned images and seccomp checksum remain unchanged.

Rejected alternatives: broad prefix sweepers, age-only deletion, Redis-authorized deletion, and cleanup inside ordinary workers cannot establish both resource ownership and execution authority.

Limitations: shared kernel and trusted daemon, no aggregate named-volume quota, no total CPU-time accounting or reliable peak-memory measurement, no automatic deletion of legacy/unregistered resources or host temporary policy files. A future bounded compile workspace needs quota-backed storage or bounded tmpfs and verified size-limited artifact handoff. This is not proof of production hostile-code safety.
