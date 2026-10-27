# JudgeGrid

Distributed online judge and future AI code-intelligence platform. Sessions 1–4 provide durable submissions, recoverable development dispatch and a restricted local C++17/Python execution subsystem. Full judging is not implemented yet.

## Run

Install Docker Desktop with Linux containers, then run from the repository root:

```sh
docker compose up --build -d --wait
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
docker compose logs worker
```

PowerShell users can use `Invoke-RestMethod http://localhost:8080/readyz`.
Liveness returns HTTP 200 with `{"status":"ok"}`. Readiness returns HTTP 200 when PostgreSQL and Redis respond, otherwise HTTP 503 with per-dependency availability. Connection details are never included in health responses.

If a local HTTPS inspection tool causes Go dependency downloads to fail with an unknown certificate authority, export its trusted public CA certificate to a PEM file outside this repository. The Dockerfile supports an optional BuildKit secret without embedding that local certificate in the image:

```powershell
docker build --secret "id=local_ca,src=C:\path\local-ca.pem" -t judgegrid-api -t judgegrid-worker .
docker compose up -d --wait --no-build
```

For the Docker-based Go checks, mount that PEM and set `SSL_CERT_FILE` to its container path. Keep TLS verification enabled.

```sh
docker compose down
```

This stops services and preserves PostgreSQL and Redis volumes. Redis uses AOF with every-second fsync; this is not a guarantee against all host/storage failures. Local database credentials are for development. Only the API port is published, bound to localhost. Production deployment and an authenticated identity provider are not implemented.

## Try the Submission API

Protected routes return 401 by default. For local development only, explicitly enable a fixed principal. Every caller then shares that principal; arbitrary `X-User-ID` headers have no effect. Configuration rejects this identity mechanism outside APP_ENV=development.

PowerShell:

```powershell
$env:APP_ENV = 'development'
$env:DEV_AUTH_ENABLED = 'true'
$env:DEV_PRINCIPAL = 'aman-local'
docker compose up -d --wait
docker compose run --rm migrate manage check
docker compose run --rm migrate manage seed
$problem = Invoke-RestMethod http://localhost:8080/api/v1/problems/two-sum
$body = @{ problem_id = $problem.problem_id; language = 'cpp17'; source_code = 'int main(){return 0;}' } | ConvertTo-Json
$submission = Invoke-RestMethod -Method Post -Uri http://localhost:8080/api/v1/submissions -ContentType application/json -Headers @{ 'Idempotency-Key' = 'my-first-submission' } -Body $body
Invoke-RestMethod "http://localhost:8080/api/v1/submissions/$($submission.submission_id)"
Invoke-RestMethod 'http://localhost:8080/api/v1/submissions?limit=20'
python scripts/smoke.py
```

On POSIX shells, export the same three variables before running Compose. You can also create an ignored local `.env` with those explicit settings. Disable development identity again with `DEV_AUTH_ENABLED=false` and recreate the API using Compose. The repository defaults stay disabled.

Migrations run as a separate, one-shot Compose service before API startup. Seeds are explicit and idempotent, create two public demonstration problems, and have no HTTP mutation endpoint. The current problem version is selected by the server. A retry with the same principal/key and normalized request returns the original submission, even after a new problem version is published. Reusing a key for different intent returns 409.

See [API contract and response examples](docs/api.md), [migration operations](migrations/README.md) and [durable-submission decisions](docs/adr/0002-durable-submissions.md).

## Development dispatch (Session 3)

Production consumption is disabled. The optional harness handles only dispatch_test references on an isolated stream, records idempotent development receipts in PostgreSQL, and leaves real submissions QUEUED with no verdict. The HTTP create path still publishes no Redis messages.

After enabling the local API identity above:

```powershell
$env:DEV_DISPATCH_ENABLED = 'true'
$env:QUEUE_STREAM = 'dev:judgegrid:dispatch'
docker compose up -d --wait
python scripts/smoke.py
python scripts/dispatch_smoke.py
docker compose exec -T redis redis-cli XPENDING dev:judgegrid:dispatch judges
docker compose exec -T worker wget -qO- http://127.0.0.1:8081/readyz
```

The pool defaults to four slots, each with a unique consumer and one active message at most. Redis coordinates consumers across processes; goroutines bound work within each process. ACK follows a durable receipt or known duplicate. Failures and malformed/unsupported messages stay pending for explicit recovery. No stream trimming is configured.

See [dispatch commands, configuration and failure policy](docs/dispatch.md) and [ADR 0003](docs/adr/0003-streams-and-bounded-dispatch.md). Disable DEV_DISPATCH_ENABLED and DEV_AUTH_ENABLED again and run Compose to restore defaults. Local sandboxed judging and recoverable outbox publication are available separately; distributed execution ownership remains future work.

## Local sandbox execution (Session 4)

An explicitly enabled fixture CLI can compile C++17 or execute Python 3 in restricted Linux containers. It runs as a trusted local controller using existing developer Docker authority. It is absent from API/worker runtime images, and no daemon socket is mounted into those services. No public execute endpoint or automatic Redis execution is enabled.

See [setup and fixture commands](docs/sandbox.md), [threat model](docs/sandbox-threat-model.md) and [ADR 0004](docs/adr/0004-local-sandbox-execution.md). Setup prefetches/pins runtime images; execution stays offline. The engine separates compile/run budgets, bounds captured output, terminates the container namespace on cancellation/deadline, and reports program outcomes separately from infrastructure errors. CPU and peak-memory measurements remain explicitly unavailable. Containers and functional tests do not prove arbitrary-hostile-code safety.

## Configuration

| Variable | Default |
| --- | --- |
| HTTP_ADDR | :8080 |
| DATABASE_URL | postgres://judgegrid:judgegrid@localhost:5432/judgegrid?sslmode=disable |
| REDIS_ADDR | localhost:6379 |
| REDIS_PASSWORD | empty |
| CHECK_TIMEOUT | 3s, must be positive |
| REQUEST_TIMEOUT | 5s, positive and at most 8s |
| APP_ENV | production |
| DEV_AUTH_ENABLED | false |
| DEV_PRINCIPAL | empty; required when development identity is enabled |

Compose supplies container hostnames automatically. For native Go execution, provide reachable PostgreSQL and Redis addresses through these variables, apply `go run ./cmd/manage migrate`, then run `go run ./cmd/api` and `go run ./cmd/worker` separately. Compose does not publish the database or Redis ports. Source is capped at 64 KiB and JSON bodies at 512 KiB; supported language identifiers are cpp17, python3 and go.

## Verify

With Go 1.26 and a C compiler for the race detector:

```sh
sh scripts/check.sh
```

On PowerShell without Go installed, use Docker:

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26 sh scripts/check.sh
```

Checks cover formatting, vet, normal/race-enabled unit tests and all executable builds. PostgreSQL/Redis integration tests are a separate required check:

```powershell
docker run --rm --network judgegrid_default -v "${PWD}:/src" -v judgegrid-go-cache:/go -w /src -e TEST_DATABASE_URL=postgres://judgegrid:judgegrid@postgres:5432/judgegrid?sslmode=disable -e TEST_REDIS_ADDR=redis:6379 golang:1.26 go test -race -tags=integration -v ./tests
```

GitHub Actions runs these checks plus migrations, seeds and live API smoke tests. See [test instructions](tests/README.md). Dependency failure can be checked by stopping Redis, requesting `/readyz` (503) and `/healthz` (200), then restarting Redis and checking readiness recovers.

## Boundaries

`cmd/` contains API, worker, management and development-publisher executable wiring. HTTP handlers call `internal/submission`, which validates use cases and calls its repository interface. `internal/storage` implements PostgreSQL persistence. `internal/domain` contains entities, validation, verdicts and state rules. `internal/queue` implements Streams transport; `internal/worker` separates the bounded pool from its handler contract. Supporting assets live in `migrations`, `infra`, `tests`, `scripts` and `docs/adr`.

The default worker verifies connectivity, serves internal health endpoints and waits for shutdown. Only explicit development configuration enables receipt processing. Session 6 atomically creates a dispatch intent with every new submission; an explicitly enabled outbox relay publishes it with recoverable at-least-once semantics. Automatic execution remains disabled. See [outbox operations](docs/outbox.md) and [ADR 0006](docs/adr/0006-transactional-outbox.md). Session 5's separate trusted local controller still supports durable C++/Python judging: see [local judging](docs/local-judging.md). Distributed execution ownership is Session 7 work.

See [the architecture decision](docs/adr/0001-service-boundaries.md).
