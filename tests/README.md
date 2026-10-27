Unit tests live alongside their packages. `sh scripts/check.sh` checks formatting, vet, normal and race-enabled tests, and all executable builds.

PostgreSQL/Redis integration tests are explicitly selected with `-tags=integration`. They require TEST_DATABASE_URL and TEST_REDIS_ADDR and fail if either is absent. They create fresh isolated schemas and unique test streams, dropping only those resources afterward. Use a development/test database role allowed to create schemas, never production credentials.

```powershell
docker compose up -d --wait
docker run --rm --network judgegrid_default -v "${PWD}:/src" -v judgegrid-go-cache:/go -w /src -e TEST_DATABASE_URL=postgres://judgegrid:judgegrid@postgres:5432/judgegrid?sslmode=disable -e TEST_REDIS_ADDR=redis:6379 golang:1.26 go test -race -tags=integration -v ./tests
```

These tests cover migration checksums, idempotent seeds, database constraints, rollback after injected mapping failure, immutable versions, ownership, pagination including tied timestamps, 20 concurrent HTTP creates sharing a key, and a 20-way state CAS race. The HTTP integration server uses real PostgreSQL, while the separate live smoke script targets the running application container.

Session 3 adds 50 dispatch jobs over four slots in two independent pool instances, actual child-consumer process kills before ACK and after commit, duplicate receipt effects, cursor-driven claims (including empty pages), live old/new consumer overlap, poison/unsupported/transient-failure PEL retention, successful and expired shutdown drain, failed ACK recovery, group initialization at 0, blocking-read deadlines and interrupted real Redis TCP connections with bounded reconnect counts. The helper test function is a subprocess entrypoint, not a separate behavior assertion. Unit-test fakes are not presented as integration evidence.

Run `python scripts/smoke.py` after explicitly enabling local development identity and seeding as described in README. It verifies 20 concurrent requests against the live API as well as reads, errors and bounds. `python scripts/smoke.py --unauthenticated` checks the default disabled-auth configuration. Python uses only its standard library. On Linux use python3.

CI runs unit/race/build checks, Compose migrations, migration check, seeds, PostgreSQL/Redis race-enabled integration tests and both live smoke scripts. See docs/dispatch.md for enabling the isolated development worker and running `python scripts/dispatch_smoke.py`. Normal `go test ./...` does not include integration-tagged tests; that is selection, not integration evidence. No integration tests silently skip. A local HTTPS-inspection CA may be needed for first-time Go dependency downloads; see README.

Session 4 sandbox tests have a separate `sandboxintegration` tag and explicit `JUDGEGRID_SANDBOX_TESTS=1` opt-in. Run them on the trusted local controller host with Docker CLI/daemon access, never by mounting a daemon socket into the ordinary API/worker or Go regression-test container. See docs/sandbox.md for native Windows cross-build commands and a Linux race-enabled command. Tests cover compile/runtime results, resource/output limits, actual policy observations, cleanup/cancellation, slot bounds, failed image/startup handling, controlled compiler crash and the independent container watchdog. Missing prerequisites fail explicitly rather than silently skipping.

The dedicated hosted sandbox job runs trusted runtime setup separately and executes these tests with the Linux race detector. Local native Windows sandbox runs are reported separately from Linux unit/race and PostgreSQL/Redis integration results.
# Session 6 outbox checks

The existing `integration` suite now includes atomic intent creation, backlog admission, historical backfill policy, process-crash recovery, expired/stale claims, two-relay distribution, quarantine, bounded shutdown and a real Redis XADD response-loss proxy. It preserves the prior tests. Named subprocess helpers are test infrastructure, not additional behavioral scenarios.

Session 7 adds `TestExecution*` database ownership/fencing/budget tests. On the trusted sandbox host, `JUDGEGRID_AGENT_TESTS=1 TEST_DATABASE_URL=... TEST_REDIS_ADDR=... go test -race -tags=agentintegration -v ./internal/judge` exercises real automatic judging, four process-crash boundaries, actual database/Redis TCP outages and graceful/expired execution drains. Run it separately from sandbox and judge suites on the same Docker daemon. The optional local overlay exposes PostgreSQL on 15432 and Redis on 16379 only on loopback.

After enabling development identity and the optional isolated relay, run `python scripts/outbox_smoke.py --stream test:judgegrid:local-outbox`. See [outbox operations](../docs/outbox.md) for the exact setup. Real references remain QUEUED; these checks do not enable distributed execution. Run the sandbox and judge integration suites sequentially on the same Docker daemon because the existing sandbox cancellation test polls daemon-wide container labels.
