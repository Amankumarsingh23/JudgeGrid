# ADR 0001: Separate control and execution processes

Status: Accepted (Session 1)

The API accepts HTTP traffic; an independent worker will coordinate execution. Untrusted source code must eventually run in an isolated execution backend, never in the API process. Session 1 does not execute submitted code.

PostgreSQL will be the authoritative store of submissions and verdicts. Redis Streams will distribute work; a queue message is not authoritative submission state. Session 1 only establishes connectivity and creates no streams or tables.

Writing to PostgreSQL and Redis independently creates a dual-write failure window. Session 6 will add a transactional outbox. No atomic delivery or recovery guarantee is claimed today.

Use a single Go module with separate cmd/api and cmd/worker entrypoints. Keep domain rules, persistence, messaging, HTTP transport and worker orchestration in internal packages. Introduce interfaces when an implemented boundary needs them; reserve sandbox backends and job types for later sessions.

Liveness checks process availability. Readiness checks PostgreSQL and Redis under a bounded context. The API can remain live while dependencies are unavailable. The foundation worker checks dependencies at startup and waits for cancellation; it does not advertise ongoing processing readiness.

Both processes handle termination signals and close clients. The API drains requests with a ten-second shutdown budget. Context cancellation is cooperative and will not replace sandbox termination enforcement.
