# Controlled local judging

Use a trusted development host with Docker, Go 1.26, the Session 4 runtime manifest and seccomp profile. Ordinary API/worker containers never receive Docker authority. There is no public judge endpoint. Creation commits an outbox intent; a separately enabled relay publishes it. Session 7's optional [trusted development agent](execution-agent.md) can judge authorized work automatically under renewable leases. The one-shot local CLI below now receives a bounded 180-second database fence in addition to its advisory lock. Default production execution remains disabled.

PowerShell setup:

```powershell
$env:APP_ENV='development'
$env:DEV_AUTH_ENABLED='true'
$env:DEV_PRINCIPAL='aman-local'
docker compose -f docker-compose.yml -f infra/local-judge.compose.yml up --build -d --wait
docker compose -f docker-compose.yml -f infra/local-judge.compose.yml run --rm migrate manage seed
$env:DATABASE_URL='postgres://judgegrid:judgegrid@127.0.0.1:15432/judgegrid?sslmode=disable'
go build -o bin/judge-local.exe ./cmd/judge-local
bin/judge-local.exe --allow-local-judging --seed-tests
bin/judge-local.exe --allow-local-judging --submission EXISTING_OWNED_UUID
python scripts/judge_smoke.py --judge-binary bin/judge-local.exe
```

Prefetch runtime images using `scripts/setup-sandbox.ps1` (or `.sh` on Linux) as documented in [sandbox.md](sandbox.md). The database overlay publishes only loopback port 15432 for the host controller. Keep the same Compose file arguments for management commands while using it; the default file intentionally has no database port. `--seed-tests` binds the two version-one fixtures idempotently, without changing newer versions. The private `.judgegrid-test-bundles/` directory is ignored by Git and Docker. Preserve it with the database; deleting it produces an infrastructure failure, not a verdict.

CLI output contains an aggregate evaluation summary and controller operation counts. Existing owner-scoped GET submission exposes final status/verdict, with no hidden-test detail. A repeated terminal request returns the same run with `replayed=true` and no execution. Another principal receives not-found. Go submissions remain accepted by the existing API but are not supported by this C++/Python controller and fail as configuration errors.

Restore defaults after local verification:

```powershell
Remove-Item Env:APP_ENV,Env:DEV_AUTH_ENABLED,Env:DEV_PRINCIPAL -ErrorAction SilentlyContinue
docker compose up -d --wait
```

The database port disappears and development identity is disabled. Do not delete persistent volumes to perform this reset.

Verification on a Go-enabled trusted host:

```sh
sh scripts/check.sh
JUDGEGRID_SANDBOX_TESTS=1 go test -race -tags=sandboxintegration -v ./internal/execution
JUDGEGRID_JUDGE_TESTS=1 TEST_DATABASE_URL='postgres://judgegrid:judgegrid@127.0.0.1:15432/judgegrid?sslmode=disable' go test -race -tags=judgeintegration -v ./internal/judge
```

PostgreSQL/Redis regression instructions remain in [tests/README.md](../tests/README.md). Tagged tests fail when their explicit configuration is missing rather than silently skipping.

For interviews: status describes workflow progress; verdict describes program correctness/resource behavior. FAILED means the system could not judge reliably and has no verdict. Compile-once/run-many saves repeated compiler work while fresh containers prevent mutable state sharing. Version and bundle hashes make historical decisions reproducible. Keeping expected answers and the checker in the trusted controller prevents contestants from reading them. Atomic finalization prevents a visible final verdict from disagreeing with durable per-case evidence. Stable run IDs make persistence retry safe; they do not provide distributed worker fencing.

## Session 9 immutable remote storage and rejudging

The local publisher/load adapter remains available. Explicit S3 configuration uses the same canonical bundle identity and sealed version metadata with remote read-back verification. Successful C++ artifacts may be reused only through the opt-in verified cache; cache hits still create fresh private runtime containers. Cached preparation evidence is marked with cache_hit and is not a compiler invocation. Original submission/source/version and all historical evidence remain immutable. New rejudge jobs use separate attempt ownership and guarded result selection; they do not reopen the original submission. See [storage](object-storage.md), [cache](compilation-cache.md) and [rejudge operations](rejudging.md).
