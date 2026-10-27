# Development dispatch harness

This tests transport and durable receipts. It does not execute submitted code, produce verdicts, or advance submission state. Default production submission consumption is disabled.

Session 6 adds an independent [transactional outbox relay](outbox.md). Schema-2 real references carry a logical event UUID; schema-1 development messages remain supported. Keep relay and receipt demonstration streams separate. Unsupported real jobs remain unacknowledged/recoverable, and development receipt writes now check current QUEUED/state-version-zero admission. Relay claim generations fence publication metadata only; they are not execution leases.

## Run locally

From PowerShell, build/start the application with explicit development settings:

```powershell
$env:APP_ENV = 'development'
$env:DEV_AUTH_ENABLED = 'true'
$env:DEV_PRINCIPAL = 'aman-local'
$env:DEV_DISPATCH_ENABLED = 'true'
$env:QUEUE_STREAM = 'dev:judgegrid:dispatch'
docker compose up --build -d --wait
docker compose run --rm migrate manage seed
python scripts/smoke.py
python scripts/dispatch_smoke.py
```

The API smoke script creates a durable local submission. The dispatch smoke script publishes two references with one logical attempt through the CLI, waits for one receipt and an empty PEL, and verifies the original submission remains QUEUED with state_version 0 and no verdict.

To publish a chosen existing submission explicitly:

```powershell
docker compose exec -T worker dispatch YOUR_SUBMISSION_UUID 1
docker compose logs --tail 20 worker
docker compose exec -T redis redis-cli XPENDING dev:judgegrid:dispatch judges
docker compose exec -T redis redis-cli XPENDING dev:judgegrid:dispatch judges - + 20
docker compose exec -T redis redis-cli XRANGE dev:judgegrid:dispatch - + COUNT 20
docker compose exec -T worker wget -qO- http://127.0.0.1:8081/readyz
```

Publish again with the same UUID/attempt to test duplicate delivery: the receipt stays unique. A new positive attempt records a separate harness receipt, not a real execution attempt. The publisher verifies the submission exists. No source code enters Redis.

Add more worker processes with `docker compose up -d --scale worker=2 --wait`; worker ports are internal, and instance/slot consumer names are unique. Return to one process afterward with `--scale worker=1`. Changing WORKER_CONCURRENCY changes slots per process, not process count.

Set DEV_DISPATCH_ENABLED=false and DEV_AUTH_ENABLED=false and run Compose again to restore defaults. Never enable the receipt handler on the production submissions:jobs stream; configuration rejects it. APP_ENV must also be development. An unsupported real job arriving on the harness stream stays pending, without a receipt or ACK.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| DEV_DISPATCH_ENABLED | false | Explicit development receipt consumption |
| QUEUE_STREAM | submissions:jobs | Enabled harness requires dev: or test: prefix |
| QUEUE_GROUP | judges | Consumer-group identity |
| WORKER_CONCURRENCY | 4 | Slots per process, allowed 1–64 |
| WORKER_HEALTH_ADDR | :8081 | Internal health server |
| WORKER_BLOCK | 1s | Bounded Redis read block, 1ms–5s |
| WORKER_MIN_IDLE | 1m | Eligibility threshold for harness redelivery |
| WORKER_RECOVERY_INTERVAL | 2s | Per-slot PEL scan interval |
| WORKER_HANDLER_TIMEOUT | 5s | Cooperative handler deadline |
| WORKER_COMMAND_TIMEOUT | 3s | Nonblocking Redis command/ACK budget |
| WORKER_SHUTDOWN_GRACE | 10s | Separate in-flight drain budget |
| WORKER_BACKOFF_MIN / MAX | 100ms / 5s | Exponential reconnect bounds |
| WORKER_CONTAINER_STOP_GRACE | 20s | Docker stop grace; keep above drain plus bounded command/read time |

On a locally HTTPS-inspected network, use the README's optional CA-secret build command, then Compose with --no-build. No temporary trust certificate belongs in Git or the application image.

## Failure policy and inspection

Receipt insert failure: no ACK, message stays pending. Commit succeeds but ACK is lost: reclaim/replay returns AlreadyHandled and retries ACK. Malformed or unsupported messages: structured logs and PEL retention, without silent discard. Use XPENDING and XRANGE to inspect them. Repair/re-publish deliberately, verify the durable receipt, and only then acknowledge the old bad reference. There is no automated poison-message quarantine yet.

XAUTOCLAIM carries a scan cursor. An empty page with a nonzero cursor is not the end of the scan. Idle claiming does not revoke a live old worker; only the receipt's primary key protects this harness from duplicate effects. Do not reuse that assumption for sandbox execution before fencing exists.

Redis AOF and its named volume preserve normal restarts, subject to everysec fsync limitations and storage failure. ACK does not delete stream history. No trimming is configured; storage usage needs future retention/monitoring. Never delete a consumer merely because its process exits: it may still own pending work.

## Integration checks

```powershell
docker run --rm --network judgegrid_default -v "${PWD}:/src" -v judgegrid-go-cache:/go -w /src -e TEST_DATABASE_URL=postgres://judgegrid:judgegrid@postgres:5432/judgegrid?sslmode=disable -e TEST_REDIS_ADDR=redis:6379 golang:1.26 go test -race -tags=integration -v ./tests
```

The tests use isolated database schemas and unique streams. A helper consumer is an actual child OS process killed after delivery or commit. The outage regression interrupts a TCP proxy to real Redis, measures reconnect attempts and restores connectivity. Separate live checks can stop/restart the Compose Redis service. Shutdown tests cover successful draining and deadline expiry; all cooperative handler/pool goroutines are awaited.
