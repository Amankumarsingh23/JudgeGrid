# ADR 0002: Durable, owner-scoped submissions and versioned problems

Status: Accepted — Session 2

## Decision

HTTP handlers parse bounded JSON and call a submission service. The service validates request intent and the trusted principal, then calls a PostgreSQL repository using a bounded context. Redis is not part of this write path.

Submissions begin in QUEUED. Legal progression is QUEUED → RUNNING → EVALUATING → COMPLETED. Each nonterminal state can instead transition to FAILED with an infrastructure failure reason. COMPLETED requires a verdict from AC, WA, TLE, MLE, RE, CE or OLE; user-code failure is still a completed evaluation. FAILED has no verdict. There is no public status mutation endpoint.

Internal transitions validate domain rules and atomically UPDATE only when both the expected status and state_version match. Exactly one concurrent claim can succeed. Every successful transition increments the version. A database trigger additionally rejects illegal transitions and changes to original submission intent; CHECK constraints enforce verdict/failure consistency. Missing rows and conflicts are distinct errors. This does not provide future attempt fencing or retry recovery.

Problems point to their currently published version. Version rows contain immutable statement and judging limits; updates and deletes are rejected. A composite foreign key ensures a version belongs to the problem. Creation locks the problem row for sharing while reading its current pointer, then stores that exact version in the submission. Later publication cannot rewrite history.

Idempotency-Key is required, scoped by principal, and retained without expiry in this milestone. The fingerprint is SHA-256 of a deterministic JSON encoding of problem_id, normalized language and exact source bytes. Language is trimmed and lowercased; source whitespace is preserved. The fingerprint deliberately excludes the currently published version, so retrying the original intent after publication returns the original submission/version.

Within one transaction, a PostgreSQL transaction advisory lock serializes a principal/key pair, an existing mapping is checked, or the submission and its mapping are inserted together. Database uniqueness remains a second guard. Lock hash collisions cause additional serialization, not identity collisions. Cancelled/failed transactions roll back; uncertain commit outcomes are resolved by client retries with the same key. We do not blindly retry an ambiguous write as a new submission. Same-key replay returns HTTP 200 with the existing submission's current state; a new record returns 201. Different intent returns 409.

Private reads and cursor listings always include principal_id in their SQL predicates. The HTTP layer never accepts an arbitrary user ID as authentication. Development identity is a configured static principal, explicitly enabled only when APP_ENV=development; every caller then shares that identity. It is disabled by default. A production identity provider is not implemented, so protected endpoints return 401 by default. Public problem metadata contains no hidden test data.

Cursor pagination uses descending (created_at, UUID) order, a default page size of 20, and a maximum of 100. Cursors are opaque encodings of positions, not authentication tokens. Tampering cannot remove owner predicates. Pagination is not a snapshot across requests.

## Consequences

Accepted submissions are durable but remain QUEUED. No queue messages are published, and no work is executed. Session 3 can add Redis Streams/worker-pool infrastructure. Reliable database-to-queue publication remains a later transactional-outbox milestone; adding a naive dual write would break failure consistency.

Migrations use embedded ordered SQL, checksum tracking, an advisory lock and a single transaction. Seeds are idempotent development CLI operations, with no public write endpoint. Operational role separation, production authentication, rate limiting, idempotency retention and attempt fencing remain future work.
