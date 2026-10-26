# ADR 0003: Redis Streams and bounded development dispatch

Status: Accepted — Session 3

## Decision

Redis Streams provides transport using XADD, XREADGROUP, XACK, XPENDING and cursor-based XAUTOCLAIM. Consumer-group initialization uses MKSTREAM at ID 0, so entries published before startup are eligible. BUSYGROUP is the sole ignored initialization error. Streams and group names are configurable; production defaults are submissions:jobs and judges.

Production submission consumption remains disabled. A development-only worker can consume dispatch_test references from an isolated dev: or test: stream. The publisher CLI also requires APP_ENV=development and an isolated stream. HTTP submission creation does not publish anything. No submission is marked RUNNING, COMPLETED or FAILED by this harness, and there is no compiler or executor.

Messages contain exactly schema_version=1, kind, submission_id UUID and positive attempt. They contain no source, principal credentials, limits or verdict. Transport recognizes submission and dispatch_test kinds; the development handler refuses submission jobs. Attempt is a logical receipt generation, not a fencing token or authorization proof.

The pool defaults to four slots. Every slot has a unique consumer name composed of a random process-instance identifier and slot number, and reads/claims COUNT 1. It processes synchronously before reading again, so there is no local prefetch queue and at most one locally active delivery per slot. The group distributes across processes; slots bound goroutine concurrency inside each process. Failed work retained in Redis's PEL can accumulate independently of local active capacity.

Idle PEL scanning is periodic, uses XAUTOCLAIM COUNT 1 and carries the returned cursor across scans, including empty pages. Failed calls preserve the previous cursor. Idle time is eligibility for redelivery, not evidence of worker death. Normal default min-idle is one minute, above the test handler's five-second deadline; shorter values used in tests deliberately demonstrate concurrent old/new handlers. Automatic recovery is enabled only for the idempotent development harness, never real execution.

The handler commits one isolated development_dispatch_receipts row per (submission_id, attempt). A primary key makes concurrent duplicates harmless. A duplicate receipt is ACK-safe. SQL never changes submission status or verdict. Exactly one receipt insertion is proven; exactly-once execution and arbitrary side effects are not claimed.

ACK occurs only after the handler returns Handled or AlreadyHandled with no error. Retryable failures, unknown outcomes, malformed messages and unsupported real workloads remain pending. ACK failures after commit also remain recoverable; replay finds the receipt. ACK removes an entry from a group's pending list, not from the stream. No application path trims or deletes stream entries.

Malformed messages emit structured error logs containing stream-entry identity and remain visible through XPENDING. They are retried at the controlled idle/recovery cadence; there is no automatic discard or dead-letter ACK. Operators must inspect and repair/re-publish deliberately, verify the corrected durable effect, and only then acknowledge the bad reference. Persistent poison entries consume Redis storage; monitoring and an audited quarantine workflow are future work.

Transport failures use cancelable exponential backoff, from 100ms to 5s by default, per slot. Built-in command retries are disabled so the pool owns reconnect pacing. Each command has a deadline; bounded blocking reads may take their socket deadline to notice cancellation. Missing groups can be recreated at 0 without deleting retained entries, which can cause redelivery and is safe only for this receipt workload.

On shutdown, acquisition stops via its polling context. In-flight work uses a distinct processing context, retaining a bounded drain window. Successfully committed outcomes can still ACK during drain. Grace expiry cancels cooperative handlers, waits for owned goroutines to exit, returns an explicit error, and leaves unfinished messages pending. Arbitrary uncooperative code cannot be forcibly terminated by a Go context; future sandboxes need external enforcement. Container stop grace must exceed handler-drain and bounded transport shutdown time.

Worker health is served internally on :8081. Liveness means the health server responds. Readiness checks PostgreSQL/Redis and, in development mode, initialized pool/group availability. Disabled production mode reports infrastructure readiness, not execution capability. During drain the enabled pool is unready.

## Persistence and remaining risks

Compose enables Redis AOF with appendfsync everysec and a named volume. This improves restart behavior but permits recent-write loss on host/storage failure and provides no HA guarantee. PostgreSQL stays authoritative. Never trim a live stream without a retention design that accounts for all pending consumers/groups. Consumers and old entries currently require operational retention planning; consumer identities are not deleted during shutdown because they can still own pending entries.

Reliable database-to-Redis dispatch requires the planned outbox in Session 6. Safe real execution recovery requires attempt IDs, renewable leases and fencing in Sessions 6–7. Session 4 must introduce a verified sandbox behind a separate handler/executor boundary; the development receipt handler is not a judge result implementation.
