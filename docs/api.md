# JudgeGrid API contract

Application responses are JSON except the explicitly requested SSE event stream and local web assets. Errors use:

```json
{"error":{"code":"invalid_request","message":"invalid request"}}
```

## Identity

Submission routes require a trusted principal. No production identity provider exists yet. By default these routes return 401. Only explicit development configuration enables a fixed local identity; `X-User-ID` is ignored. Public problem metadata and health checks need no identity.

## Create a submission

`POST /api/v1/submissions`, with `Content-Type: application/json` and required `Idempotency-Key` (1–200 printable non-space ASCII characters).

```json
{"problem_id":1,"language":"cpp17","source_code":"int main(){return 0;}"}
```

Supported API identifiers: `cpp17`, `python3`, `go`; the trusted judge currently executes C++17/Python only and safely rejects unsupported Go execution. Language is trimmed and lowercased. Source is preserved, must be nonblank, contain no NUL, and be at most 65,536 UTF-8 bytes. The total JSON body limit is 524,288 bytes, applied before decoding. Unknown fields, trailing JSON and malformed JSON are rejected. Use the problem endpoint's returned numeric ID rather than assuming fixture IDs.

New records return 201 and a Location header:

```json
{
  "submission_id":"550e8400-e29b-41d4-a716-446655440000",
  "problem_id":1,
  "problem_version_id":1,
  "problem_version":1,
  "language":"cpp17",
  "source_code":"int main(){return 0;}",
  "status":"QUEUED",
  "verdict":null,
  "state_version":0,
  "created_at":"2026-10-26T08:00:00Z",
  "updated_at":"2026-10-26T08:00:00Z"
}
```

IDs and timestamps above are illustrative. Equivalent same-key requests return the same submission ID with HTTP 200 and `Idempotency-Replayed: true`. The response reflects its current state, not a byte-for-byte cached original response. A changed request with the same principal/key returns 409. Different principals have independent key namespaces. Keys have no expiry yet. Retry ambiguous transport errors using the same key and intent.

## Retrieve and list

- `GET /api/v1/submissions/{uuid}` returns the same submission representation to its owner. Missing, malformed or inaccessible IDs return 404.
- `GET /api/v1/submissions?limit=20&cursor=...` returns `{"items":[...],"next_cursor":"..."}`. Items include source code and are private to the principal. Limit is 1–100, default 20. Follow next_cursor unchanged; its absence means no next page. Ordering is newest first by (created_at, UUID). New arrivals do not shift later cursor positions, but pages do not provide a transaction-wide snapshot.
- `GET /api/v1/problems/{slug}` returns published public metadata, for example `{"problem_id":1,"slug":"two-sum","title":"Two Sum","problem_version_id":1,"problem_version":1,"statement":"Read two integers and print their sum.","time_limit_ms":2000,"memory_limit_kb":262144}`. Missing or unpublished problems return 404. No hidden tests are stored or exposed by these fixtures.

## Status codes

| Code | Meaning |
| --- | --- |
| 200 | Read/list or idempotent replay |
| 201 | New durable submission |
| 400 | Invalid body, language, key, limit or cursor |
| 401 | Authentication unavailable/missing |
| 404 | Missing/inaccessible submission or unpublished/missing problem |
| 409 | Idempotency intent conflict or problem has no published version for submission |
| 413 | Request body or source exceeds the limit |
| 415 | Creation request is not application/json |
| 503 | Database/operation unavailable; internal connection details are not returned |

## Data flow

HTTP parsing → trusted principal + service validation → PostgreSQL transaction → QUEUED response. The transaction checks idempotency, selects the current immutable problem version, and saves submission plus mapping together. No Redis dispatch or execution occurs in Session 2.
# Session 5 local judging

The separate `judge-local` CLI can evaluate an owned durable submission in an explicitly opted-in development environment. Existing submission reads reflect its final status and verdict. No judge endpoint or hidden-case/evidence endpoint is added. See [local judging](local-judging.md).

## Session 6 dispatch intents

Creation atomically commits one initial outbox intent with the submission and idempotency mapping. Owner/key/body replay returns the same ID without another event; body conflict remains 409. Redis publication is asynchronous through an explicitly enabled relay, never an HTTP dual write. Excess pending/claimed/quarantined backlog rejects new acceptance with 503 `dispatch_backlog` and `Retry-After: 30`; existing idempotent replays still succeed. Public responses contain no outbox payload or hidden tests. Historical submissions are not automatically backfilled. Automatic real execution stays disabled; see [outbox operations](outbox.md).

## Session 7 controlled execution

The explicitly enabled trusted development agent can automatically evaluate the configured principal's published submissions. Owner-scoped GET returns the genuine final verdict. Unsupported runtime languages become durable FAILED with no fabricated verdict and an internal rejection reason. No public execution endpoint, lease override, hidden-evidence endpoint or automatic production consumer is added. See [agent operations](execution-agent.md).

## Session 10 local sessions, backpressure and events

The optional [local web overlay](local-web.md) replaces fixed development identity with offline-provisioned local user sessions. `POST /api/v1/session` accepts one username/password JSON object, at most 2 KiB, from the exact configured loopback Origin/Host. It returns principal, short-lived CSRF token and expiry, and sets a 30-minute HttpOnly SameSite=Strict cookie (Secure for HTTPS). `GET /api/v1/session` checks that cookie; `DELETE` requires same Origin plus `X-CSRF-Token` and revokes it. Submission POST also requires those CSRF checks in web mode. Passwords and cookie session tokens must not be logged. No identity header grants rights. Default non-web production routes still return 401. Session endpoints/assets are absent unless explicitly enabled.

`GET /api/v1/problems` returns at most 100 published suite-backed public problem descriptions. `GET /api/v1/submissions/{id}/evaluations` returns at most 100 sanitized owner-only historical summaries; missing/foreign submissions both return 404. GET/list continue to display the selected committed verdict alongside immutable original version/verdict metadata. A rejudge's pending progress is available in the event snapshot without replacing the previous selected verdict prematurely. List cursors are bound to the principal; pass them unchanged. They are not credentials and SQL owner authorization remains mandatory.

When the trusted scheduler is enabled, each submission POST spends a shared database token before validation/replay. Rate exhaustion returns 429 `request_rate` and Retry-After seconds. A new initial/rejudge logical scope beyond the principal pending budget returns 429 `pending_capacity`; an existing valid idempotent replay does not add pending work. Capacity deferral is 503 `scheduler_capacity`; outbox backlog remains 503 `dispatch_backlog`. Scheduler defaults: global active 4, principal active 1, pending 128, request burst 120/refill 2 per second; enabling requires explicit enrollment. These are shared database limits, not per-process estimates.

`GET /api/v1/submissions/{id}/events` requires the owner session (or existing trusted server-side identity outside web mode). It returns `text/event-stream; charset=utf-8`, no-cache/no-transform, disabled proxy buffering, named events, flushed frames and comment heartbeats. Example:

```text
id: 550e8400-e29b-41d4-a716-446655440000:7
event: running
data: {"status":"RUNNING","evaluation_run_id":"...","generation":1}

```

The first stream emits an authoritative `snapshot` with an ID covering its repeatable-read database boundary. Reconnect with `Last-Event-ID: <submission UUID>:<sequence>` (or `?cursor=` for a new browser EventSource after refresh). Committed events after that sequence are replayed through a captured boundary, followed by the authoritative snapshot, then new committed events. Snapshot may repeat the boundary ID; consumers must treat it as state replacement. Cursor syntax errors return 400; a future own cursor returns 409; a foreign/missing resource returns 404 before cursor parsing. No source, hidden tests, private object keys or diagnostics appear in events.

The page size is 32 and replay/lag window is 256 events. Too-old/missing history yields `reset` with current state; excessive live lag resets and closes so the client can reconnect. No historical pruning is implemented yet. Five-second transport write deadlines bound stalled clients; connections rotate after two minutes and EventSource reconnects. Polling is 500 ms, heartbeats ten seconds. Session expiry/revocation or unavailable database ends an existing stream. SSE connections share database leases (30 seconds, renewed every ten), default global 32/principal 2; exhaustion returns 429 `sse_capacity` with Retry-After. The browser caps its displayed timeline at 60 entries and retains only principal-scoped IDs/cursors locally.
