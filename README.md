# JudgeGrid

Distributed online judge and future AI code-intelligence platform. Sessions 1–2 provide runnable services and durable, owner-scoped submission management. Judging is not implemented yet.

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

This stops services and preserves the PostgreSQL volume. Local database credentials are for development. Only the API port is published, bound to localhost. Production deployment and an authenticated identity provider are not implemented.

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

Checks cover formatting, vet, normal/race-enabled unit tests and all three executable builds. PostgreSQL integration tests are a separate required check:

```powershell
docker run --rm --network judgegrid_default -v "${PWD}:/src" -v judgegrid-go-cache:/go -w /src -e TEST_DATABASE_URL=postgres://judgegrid:judgegrid@postgres:5432/judgegrid?sslmode=disable golang:1.26 go test -race -tags=integration -v ./tests
```

GitHub Actions runs these checks plus migrations, seeds and live API smoke tests. See [test instructions](tests/README.md). Dependency failure can be checked by stopping Redis, requesting `/readyz` (503) and `/healthz` (200), then restarting Redis and checking readiness recovers.

## Boundaries

`cmd/` contains API, worker and management executable wiring. HTTP handlers call `internal/submission`, which validates use cases and calls its repository interface. `internal/storage` implements PostgreSQL persistence. `internal/domain` contains entities, validation, verdicts and state rules. `internal/config`, `queue` and `worker` retain the foundation infrastructure. Supporting assets live in `migrations`, `infra`, `tests`, `scripts` and `docs/adr`.

The worker verifies connectivity and waits for a shutdown signal. New submissions remain QUEUED: there is no Redis publication or execution. Queue consumers, attempt fencing, scheduling, sandbox execution, actual verdict generation, production authentication and AI features belong to later sessions. A transactional outbox is planned to make future queue dispatch recoverable.

See [the architecture decision](docs/adr/0001-service-boundaries.md).
