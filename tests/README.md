Unit tests live alongside their packages. `sh scripts/check.sh` checks formatting, vet, normal and race-enabled tests, and all executable builds.

PostgreSQL integration tests are explicitly selected with `-tags=integration`. They require TEST_DATABASE_URL and fail if it is absent. They create a fresh isolated schema per test and drop only those schemas afterward. Use a development/test database role allowed to create schemas, never production credentials.

```powershell
docker compose up -d --wait
docker run --rm --network judgegrid_default -v "${PWD}:/src" -v judgegrid-go-cache:/go -w /src -e TEST_DATABASE_URL=postgres://judgegrid:judgegrid@postgres:5432/judgegrid?sslmode=disable golang:1.26 go test -race -tags=integration -v ./tests
```

These tests cover migration checksums, idempotent seeds, database constraints, rollback after injected mapping failure, immutable versions, ownership, pagination including tied timestamps, 20 concurrent HTTP creates sharing a key, and a 20-way state CAS race. The HTTP integration server uses real PostgreSQL, while the separate live smoke script targets the running application container.

Run `python scripts/smoke.py` after explicitly enabling local development identity and seeding as described in README. It verifies 20 concurrent requests against the live API as well as reads, errors and bounds. `python scripts/smoke.py --unauthenticated` checks the default disabled-auth configuration. Python uses only its standard library. On Linux use python3.

CI runs unit/race/build checks, Compose migrations, migration check, seeds, PostgreSQL race-enabled integration tests and live smoke checks. Normal `go test ./...` does not include integration-tagged tests; that is selection, not integration evidence. No integration tests silently skip. A local HTTPS-inspection CA may be needed for first-time Go dependency downloads; see README.
