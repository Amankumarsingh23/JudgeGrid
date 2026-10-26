# Session 2 completion report

## Implemented

- POST /api/v1/submissions: required owner-scoped Idempotency-Key, strict bounded JSON, validation, PostgreSQL transaction, server-selected immutable problem version; 201 for new records and 200 for replay.
- GET /api/v1/submissions/{id}: private owner-only retrieval; inaccessible and missing IDs both return 404.
- GET /api/v1/submissions: owner-scoped cursor pagination, default 20 and maximum 100, ordered by timestamp and UUID.
- GET /api/v1/problems/{slug}: public current-version metadata, without hidden tests.
- Domain entities, statuses, verdicts, request normalization/fingerprints, legal transition rules, service/repository boundary and safe JSON errors.
- PostgreSQL problems, immutable versions, submissions, idempotency mappings, indexes, CHECK/UNIQUE/composite foreign-key constraints and guarded updates.
- Embedded transactional/checksummed migrations with CLI migrate/check, Compose startup migration service, and an explicit development-only idempotent seed command for two problems.
- Development identity disabled by default and rejected outside development configuration. No arbitrary client-supplied user ID is trusted.
- Updated Docker build, CI, README, API contract, test instructions and ADR 0002. Local Session 2 learning notes remain excluded from Git and Docker build context.

## State and concurrency guarantees

The internal compare-and-set transition checks expected status and state_version in one SQL update, increments the version and rejects illegal transitions. Database triggers also preserve original submission intent and forbid illegal/terminal rewrites. COMPLETED requires a valid judge verdict, including WA; FAILED requires an infrastructure failure reason and no verdict.

Creation serializes each principal/key pair using a transaction advisory lock. The fingerprint covers numeric problem ID, normalized language and exact source. The version pointer is selected under a shared row lock and permanently stored on the submission. Submission and mapping commit or roll back together. Same-key replay continues to return the historical submission after new-version publication.

## Checks actually executed

All Go commands ran in Docker because the host does not have Go installed. The existing temporary trusted-CA mount/BuildKit secret was used for dependency-download compatibility; no TLS bypass or CA commit was introduced.

| Check | Result |
| --- | --- |
| gofmt check | Passed |
| go vet ./... and go vet -tags=integration ./... | Passed |
| go test ./... | Passed: 13 unit-test functions, including table-driven subtests |
| go test -race ./... | Passed: same 13 functions, including Session 1 checks |
| go build ./cmd/... | Passed: API, worker and manage |
| go test -race -tags=integration -v ./tests | Passed: 8 integration-test functions, including 10 constraint subtests |
| Docker application image build | Passed |
| docker compose config --quiet | Passed |
| Compose startup and migration/check | Passed |
| Explicit seed command twice | Passed; fixtures remained idempotent |
| git diff --check | Passed |

Integration tests use real PostgreSQL in isolated temporary schemas. No tests skipped. Untagged unit runs intentionally do not select the integration suite; that suite was run separately.

The 20-way HTTP integration race produced **1 creation + 19 replays, exactly 1 submission and 1 mapping**, all sharing one submission ID. The 20-way state CAS race produced **1 winner + 19 conflicts**. Tests additionally verified injected mapping-write rollback, idempotency mismatch, owner isolation, historical version association, immutable version rows, migrations/checksums, FK/CHECK/UNIQUE constraints, terminal protection, unpublished/missing problems, unavailable storage and pagination with tied timestamps.

## Live checks

The running API container passed the standard-library Python smoke script: public problems, creation, replay, conflict, get/list, required keys, unknown fields, source limits, pagination bounds and missing resources. Its independent 20-request race also returned **1 creation + 19 replays with one ID**. New submissions stayed QUEUED with no verdict.

Default disabled-auth configuration returned 401 on protected routes despite an X-User-ID header. Explicit local development identity was enabled temporarily for authenticated smoke checks.

Stopping PostgreSQL produced a safe 503 on submission reads; liveness remained 200. The final concurrent readiness implementation reported PostgreSQL unavailable and Redis healthy. Stopping Redis produced readiness 503 with PostgreSQL healthy, while liveness stayed 200. Readiness recovered after dependencies restarted. This fixes a Session 1 issue where sequential checks could exhaust the shared budget before checking a healthy dependency.

Both API and worker handled termination with exit code 0 and were restarted. Services are left running with repository-default development identity disabled; follow README to explicitly enable a local identity again.

## Files and operational limits

Primary implementation: internal/domain/submission.go, internal/submission/service.go, internal/storage/submissions.go and seed.go, internal/api/submissions.go, cmd/manage/main.go, migrations/0001_submissions.sql and migrate.go. See docs/api.md and docs/adr/0002-durable-submissions.md for the contract and rationale.

No live judging, Redis dispatch, sandbox, fabricated verdicts, production identity provider, attempt fencing or retry/reclaim workflow was added. Idempotency records have no expiry. Pagination is not a cross-request snapshot. The static development identity is shared by all callers and provides no production authentication assurance. Database role separation and production deployment are future work. Migrations are forward-only; integration checks require permission to create temporary schemas in a development database.

Session 3 can build Redis Streams and worker-pool infrastructure against these durable records and CAS rules. Reliable publication remains the planned transactional-outbox milestone; do not introduce an unsafe database/Redis dual write.

The verified work is committed/pushed in this execution; the exact Git result is provided in the chat summary. GitHub Actions is configured with integration and live smoke checks; a hosted run result has not been verified locally.
