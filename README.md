# JudgeGrid

Session 1 foundation for a distributed online judge and future AI code-intelligence platform.

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

This stops services and preserves the PostgreSQL volume. Local database credentials are for development. Only the API port is published, bound to localhost. Production configuration and deployment are outside Session 1.

## Configuration

| Variable | Default |
| --- | --- |
| HTTP_ADDR | :8080 |
| DATABASE_URL | postgres://judgegrid:judgegrid@localhost:5432/judgegrid?sslmode=disable |
| REDIS_ADDR | localhost:6379 |
| REDIS_PASSWORD | empty |
| CHECK_TIMEOUT | 3s, must be positive |

Compose supplies container hostnames automatically. For native Go execution, provide reachable PostgreSQL and Redis addresses through these variables, then run `go run ./cmd/api` and `go run ./cmd/worker` separately. Compose does not publish the database or Redis ports.

## Verify

With Go 1.26 and a C compiler for the race detector:

```sh
sh scripts/check.sh
```

On PowerShell without Go installed, use Docker:

```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.26 sh scripts/check.sh
```

Checks cover formatting, vet, race-enabled unit tests and both executable builds. GitHub Actions also starts Compose and checks both HTTP endpoints. Dependency failure can be checked by stopping Redis, requesting `/readyz` (503) and `/healthz` (200), then restarting Redis and checking readiness recovers.

## Boundaries

`cmd/` contains executable wiring. `internal/api`, `config`, `storage`, `queue` and `worker` implement the foundation. `internal/domain` reserves future core rules. `migrations`, `infra`, `tests`, `scripts` and `docs/adr` hold their respective supporting assets.

The worker verifies connectivity and waits for a shutdown signal. Submission endpoints, database schemas, queue consumers, scheduling, sandbox execution, verdicts, authentication and AI features belong to later sessions.

See [the architecture decision](docs/adr/0001-service-boundaries.md).
