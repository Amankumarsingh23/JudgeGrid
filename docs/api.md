# Session 2 API contract

All application responses are JSON. Errors use:

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

Supported identifiers: `cpp17`, `python3`, `go`. These identify future execution configurations, not currently implemented compilers. Language is trimmed and lowercased. Source is preserved, must be nonblank, contain no NUL, and be at most 65,536 UTF-8 bytes. The total JSON body limit is 524,288 bytes, applied before decoding. Unknown fields, trailing JSON and malformed JSON are rejected. Use the problem endpoint's returned numeric ID rather than assuming fixture IDs.

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

The separate `judge-local` CLI can evaluate an owned durable submission in an explicitly opted-in development environment. Existing submission reads reflect its final status and verdict. No judge endpoint or hidden-case/evidence endpoint is added; HTTP creation still only queues durable state without Redis publication. See [local judging](local-judging.md).
