# Transactional outbox operations

Every new accepted submission now commits one initial dispatch intent in the existing PostgreSQL transaction. Replaying the same owner/key/body returns the original submission and creates no additional intent. Conflicting bodies still return 409. Source and hidden tests never enter outbox records or Redis. The API makes no Redis publish call.

The relay is off by default and belongs to Compose profile `outbox`. Running it publishes real references but does not enable judging. Production workers still consume no real jobs. The development receipt handler retains unsupported real jobs in the PEL without a receipt or ACK. Its stateful receipt path checks the authoritative submission is still QUEUED at state version zero. Schema 1 remains valid for the existing harness; schema 2 adds an event UUID to real references.

## Controlled local demonstration

```powershell
$env:APP_ENV='development'
$env:DEV_AUTH_ENABLED='true'
$env:DEV_PRINCIPAL='aman-local'
docker compose up --build -d --wait
docker compose run --rm migrate manage seed
$env:OUTBOX_RELAY_ENABLED='true'
$env:OUTBOX_STREAM='test:judgegrid:local-outbox'
docker compose --profile outbox up -d --wait outbox-relay
python scripts/outbox_smoke.py --stream test:judgegrid:local-outbox
docker compose exec -T outbox-relay wget -qO- http://127.0.0.1:8082/outboxz
docker compose logs --tail 30 outbox-relay
```

The smoke script sends 20 identical concurrent POSTs, verifies one submission/intent and nineteen replays, waits for publication, resolves the stored Stream ID in Redis and checks the submission remains QUEUED/version zero/null verdict. It uses trusted local database inspection, not a new public endpoint. Keep the receipt harness stream separate from the outbox demonstration stream.

On hosts with HTTPS inspection, use the README's public-CA build secret and then Compose without `--build`; do not disable certificate verification. A Go-enabled host can instead run `go run ./cmd/outbox-relay` with explicit database/Redis addresses and `OUTBOX_RELAY_ENABLED=true`. `judge-local` remains available for explicitly owned local evaluation, independent of the relay.

## Bounds and monitoring

| Variable | Default | Bound/purpose |
|---|---|---|
| OUTBOX_RELAY_ENABLED | false | Explicit startup opt-in |
| OUTBOX_STREAM | submissions:jobs | Stable destination; use isolated test stream for demonstrations |
| OUTBOX_BATCH | 4 | 1–16 simultaneous publications per relay |
| OUTBOX_CLAIM_TTL | 30s | 1s–5m, authoritative DB expiry |
| OUTBOX_PUBLISH_TIMEOUT | 3s | At most one third of TTL |
| OUTBOX_MAX_ATTEMPTS | 20 | 1–1000 attempts per retry budget |
| OUTBOX_POLL | 500ms | 10ms–1m, idle pacing |
| OUTBOX_BACKOFF_MIN / MAX | 1s / 1m | Capped exponential equal jitter, max 1h |
| OUTBOX_DRAIN | 10s | Bounded shutdown drain, max 1m |
| OUTBOX_BACKLOG_LIMIT | 10000 | API admission, 1–100000 outstanding intents |
| OUTBOX_HEALTH_ADDR | :8082 | Internal liveness/readiness/statistics listener |

Keep container stop grace above the drain plus bounded shutdown overhead (default 20s for a 10s drain). Compose leaves the health port unpublished. `/healthz` indicates liveness; `/readyz` checks PostgreSQL and Redis. An outage makes readiness unhealthy while the process continues retrying. `docker compose up --wait` can report failure during an outage even though the running relay can recover.

`/outboxz` and `outbox-relay --stats` expose Pending, Claimed, Published, Quarantined, RetryCount and OldestAgeSeconds. Each state samples at most 10001 oldest rows through its status/age index; `Capped=true` means counts/retries are lower bounds. Oldest age covers pending/claimed work. These are lightweight diagnostics rather than Session 11 metrics. Query quarantine rows directly for individual safe codes:

```sql
SELECT id, submission_id, attempts, claim_generation, last_error_code, created_at
FROM outbox_events WHERE status='QUARANTINED' ORDER BY created_at,id LIMIT 100;
```

At the admission threshold the API returns 503 `dispatch_backlog` with `Retry-After: 30`. Replays of accepted idempotency keys remain available. New acceptance is serialized briefly by a transaction advisory lock; there is no network call under that lock. Pending/claimed/quarantined rows count toward this threshold. Published but unconsumed Redis entries do not: publication is not execution, and operators must control relay use and stream capacity while real consumption is disabled.

## Retry, quarantine and historical reconciliation

Network failures and uncertain XADD outcomes are retryable. Retry delay is sampled from `[cap/2,cap]`, where `cap=min(max,min*2^(attempt-1))`. Redis WRONGTYPE/NOAUTH/WRONGPASS/NOPERM and malformed event versions are permanent. Retry-budget exhaustion quarantines an intent rather than deleting it. Safe codes contain no payloads, connection credentials or arbitrary Redis diagnostics.

After correcting the cause, release one quarantined or expired-claim event explicitly:

```powershell
docker compose --profile outbox run --rm --no-deps outbox-relay outbox-relay --retry EVENT_UUID
docker compose --profile outbox run --rm --no-deps outbox-relay outbox-relay --stats
```

`OUTBOX_RELAY_ENABLED=true` is required for these trusted commands. Retry never edits immutable payload identity, resets lifetime attempts, steals a live claim or reopens published history. It resets only the retry budget base. An unknown immutable payload version needs relay support deployed before retry; simply releasing it will quarantine it again. A bad stream type should be repaired by the operator after inspecting existing data, not by automatic key deletion. Keep the destination stable across relay instances/restarts; changing it can strand earlier deliveries in another stream.

Migration 0005 does not backfill historical submissions. To intentionally publish one existing QUEUED record, use:

```powershell
docker compose --profile outbox run --rm --no-deps outbox-relay outbox-relay --backfill SUBMISSION_UUID
```

This locks and checks that submission, inserts the missing generation-one intent idempotently and never scans all old rows. Terminal submissions are rejected. Backfill is a privileged reconciliation action, exempt from new HTTP admission pacing; use it deliberately. No HTTP mutation endpoint exists.

There is no retention cleanup. Outbox deletion and published updates are rejected. The relay never XDELs, XTRIMs or ACKs queue entries. A later retention policy must distinguish published, processed and acknowledged records.

Stop the optional service and restore default authentication after demonstrations:

```powershell
docker compose --profile outbox stop outbox-relay
Remove-Item Env:OUTBOX_RELAY_ENABLED,Env:APP_ENV,Env:DEV_AUTH_ENABLED,Env:DEV_PRINCIPAL,Env:DEV_DISPATCH_ENABLED -ErrorAction SilentlyContinue
docker compose up -d --wait
```

## Correctness boundary

One PostgreSQL transaction guarantees durable intent with accepted creation. It cannot atomically commit Redis XADD and PostgreSQL publication status. A crash after XADD or a lost response therefore permits repeated stream entries with one logical event UUID. Stable identity makes idempotent effects possible; it does not itself implement an execution consumer. Current tests prove zero duplicated judging effects while that consumer is disabled, and preserve the existing unique development receipt and evaluation-result invariants. Distributed execution ownership/fencing remains for Session 7.
