# Session 3 completion report

## Implemented components

- internal/queue/streams.go: schema-validated thin references, XADD, idempotent XGROUP MKSTREAM at 0, bounded blocking XREADGROUP, XACK, XPENDING summary/details, group inspection and cursor-based XAUTOCLAIM.
- internal/worker/pool.go: configurable slots (default four), unique instance/slot consumers, COUNT 1 acquisition, no local prefetch queue, bounded command/handler deadlines, cancelable exponential reconnect backoff, periodic PEL scanning and separate shutdown/drain contexts.
- internal/worker/dispatch.go and internal/storage/dispatch.go: development-only handler with one durable receipt per (submission_id, attempt), without modifying submission state or verdict.
- migrations/0002_development_dispatch_receipts.sql: isolated harness receipt table. Existing migration 0001 is unchanged.
- cmd/dispatch/main.go: explicit development publisher for existing submissions, restricted to isolated dev: or test: streams.
- internal/config/worker.go and cmd/worker/main.go: guarded development configuration, internal worker liveness/readiness on :8081, pool/group startup checks and signal-driven shutdown.
- Compose: production consumption disabled by default, explicit optional development settings, worker health checks, startup migration dependency, configurable stop grace, Redis AOF/everysec fsync and a persistent Redis volume.
- Tests, live dispatch smoke script, expanded CI, README, docs/dispatch.md and ADR 0003. Session 3 learning notes remain locally ignored and outside Docker build context.

## Safety and ACK policy

HTTP submission creation still publishes no jobs. Production defaults use submissions:jobs / judges but do not consume them. The enabled development handler requires APP_ENV=development and an isolated dev: or test: stream, and only handles dispatch_test messages. A real submission workload on that stream is unsupported and stays pending.

ACK follows Handled or AlreadyHandled only when no error is returned. Transient failures, malformed payloads, unsupported workloads and uncertain handler outcomes are logged and retained in the PEL. ACK failure after commit is recoverable through replay. No application trimming/deletion occurs; ACK does not remove stream history. Poison entries require deliberate operator inspection/repair; there is no silent discard or automatic dead-letter ACK.

All slots use one synchronous delivery at a time; durable retry backlog in Redis may grow separately. Recovery carries XAUTOCLAIM cursors across empty pages and preserves the prior cursor on failed calls. Reclaim does not prove an old consumer died or fence it. Only receipt uniqueness makes this harness duplicate-safe; no exactly-once execution claim is made.

## Executed validation

Go was run in Docker because it is not installed on the host. Dependency trust uses the existing optional local CA mount/BuildKit secret, without disabling TLS verification or committing the certificate.

| Check | Observed result |
| --- | --- |
| gofmt and git diff --check | Passed |
| go vet ./... and go vet -tags=integration ./... | Passed |
| go test ./... | Passed: 21 unit-test functions with table-driven subtests |
| go test -race ./... | Passed: same 21 functions, including Session 1/2 tests |
| go build ./cmd/... | Passed: API, worker, manage and dispatch |
| go test -race -tags=integration -v ./tests | Passed: 17 behavioral integration-test functions plus one subprocess helper entrypoint (18 top-level names) |
| Docker image build and Compose config/startup/health | Passed |
| Migration apply/check and seed | Passed against the existing development database |
| Live API smoke | Passed, including 20 requests → 1 create + 19 replays |
| Live publisher/worker smoke | Passed before and after Redis restart: 2 references → 1 receipt, PEL=0, submission unchanged |
| Live worker/API SIGTERM | Both exited 0; restarted healthy |

No tests skipped. Unit fakes are not presented as integration evidence. Integration tests use real PostgreSQL/Redis with isolated schemas and unique streams; the helper is an actual separately launched OS process.

### 50-job and concurrency evidence

Fifty jobs were handled using two independent pool instances with two slots each: peak concurrency exactly four, four distinct consumer identities spanning both instances, exactly 50 unique durable receipts and PEL=0. All 50 submissions remained QUEUED with no verdict and state_version=0. XLEN stayed 50 after ACKs, proving entries were not deleted.

The previous owner-isolation, immutable-version, request-idempotency, rollback, constraint, cursor-pagination and state-CAS integration checks all passed. The 20-way submission race still produced one creation/19 replays; the 20-way CAS race still produced one winner/19 conflicts.

### Recovery and shutdown evidence

- A child consumer OS process was forcibly killed after delivery, before ACK. A survivor reclaimed it and produced one receipt, then ACKed.
- A second child was killed after its receipt committed, before ACK. Replay recognized AlreadyHandled; receipt count stayed one and PEL became empty.
- Twelve pending entries were scanned/reclaimed with cursor pagination, including an empty page with a nonzero cursor.
- A still-live old handler and its new claimant ran concurrently: one receipt insertion and one duplicate outcome, demonstrating why idle claiming is not fencing.
- Injected handler failure, malformed payload and unsupported real workload stayed pending with no receipt or submission mutation.
- Injected ACK failure after durable handling recovered through reclaim: duplicate recognized, one receipt retained, eventual ACK.
- Successful shutdown drained active durable work and ACKed; expired grace canceled cooperative work and retained its pending entry. Pool/handler goroutines were awaited.
- A real-Redis TCP proxy outage observed six connection attempts in 500ms with bounded backoff, then resumed processing and ACKed after connectivity returned.
- The outage test initially exposed a cursor-loss bug on failed XAUTOCLAIM; the implementation now preserves the previous cursor, and the regression and full suite passed afterward.
- Group creation twice succeeded; an entry published before initialization was consumed because the initial group ID is 0. A blocked five-second read respected a 50ms context deadline.

Live Redis stop/start made worker readiness return 503 while API liveness remained 200. Recovery restored worker readiness. Redis reported appendonly=yes and appendfsync=everysec; the stream retained its two entries and group with PEL=0 after restart. The publisher/worker smoke passed again. This demonstrates a normal graceful restart, not survival of every storage/host failure.

## Runtime state and limitations

Services are left running with development identity and dispatch disabled again. Protected API routes return 401 by default. The default worker reports infrastructure readiness and does not consume real submission jobs. Follow README/docs/dispatch.md to enable a local test harness explicitly. Learning notes are not committed or pushed.

No secure executor, actual verdict production, real submission failure fabrication, automatic API dispatch, outbox, lease renewal, attempt fencing, real RUNNING-job reassignment, HA guarantee, retention policy or automated poison quarantine was added. AOF/everysec can still lose recent writes under host/storage failures. Stream/consumer metadata requires future retention and monitoring. Go handlers must cooperate with cancellation; container stop grace must exceed drain plus bounded transport shutdown time.

Session 4 should add verified sandbox execution behind the handler/executor boundary, without treating the receipt handler as judging. Sessions 6–7 remain responsible for outbox, leases, attempt epochs and fencing before safe real execution recovery.

Commit/push hash and any accessible hosted GitHub Actions result are provided in the chat summary. Hosted CI is separate from the locally executed evidence above.
