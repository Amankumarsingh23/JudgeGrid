# Local execution setup and fixture tests

The sandbox controller is an explicitly enabled local CLI. It is not an API endpoint or Redis consumer. It does not write submissions, receipts or verdicts. API/worker containers never receive Docker daemon access, and the controller is deliberately absent from their runtime image.

Session 7 reuses this restricted backend through a separate opt-in [trusted development execution agent](execution-agent.md). That host agent consumes authorized references and persists fenced results; the fixture CLI remains unchanged. No ordinary API/worker/relay container gains controller or Docker authority.

## Requirements

Use Docker Desktop with its Linux engine on Windows, or a dedicated local Linux development host with Docker. The controller requires Linux cgroup v2, working CPU/memory/swap/PID controls and seccomp; missing capabilities fail closed. Docker Desktop's Linux VM is supported for these development fixtures, but this is not a production security assurance.

The developer account running the controller must already have Docker authority. A Go toolchain is useful on the controller host; the Windows commands below can cross-build trusted Go binaries through the existing Go build container without installing Go locally. No daemon socket is mounted into that build container. Do not attempt to run Docker directly inside the internal docker-desktop WSL distribution; a separately installed/integrated Ubuntu WSL distribution is the supported alternative if you want a WSL controller.

## Trusted runtime preparation

On a Linux controller host:

```sh
sh scripts/setup-sandbox.sh
```

On Windows, review and run scripts/setup-sandbox.ps1 if your existing PowerShell policy permits scripts. If scripts are disabled, do not change the machine's security policy for this project; the equivalent trusted setup commands are:

```powershell
docker build -f infra/sandbox/Dockerfile.cpp -t judgegrid-sandbox-cpp:session4 infra/sandbox
docker build -f infra/sandbox/Dockerfile.python -t judgegrid-sandbox-python:session4 infra/sandbox
$images = @{
  cpp17 = (docker image inspect --format '{{.Id}}' judgegrid-sandbox-cpp:session4)
  python3 = (docker image inspect --format '{{.Id}}' judgegrid-sandbox-python:session4)
  cpp_base = 'gcc@sha256:5e927c284bf55a7dc796262e311a0703344f62f41f5621eb56843111b1d37e15'
  python_base = 'python@sha256:e32be0e55814a08977501ca863271d9bd02443710db8f2afaf8372e2c4508530'
}
$images | ConvertTo-Json | Set-Content -Encoding ascii .sandbox-images.local.json
```

These are trusted dependency/image builds, using pinned base digests. They may download image layers. Untrusted execution subsequently uses only the recorded immutable local image identities with --pull never, network none and no dependency installation. The local manifest and bin/ are ignored by Git; do not commit generated binaries or machine-local image identities.

## Run an approved fixture

With Go on the local controller host:

```sh
go run ./cmd/sandbox-demo --allow-local-fixtures --fixture cpp-hello
go run ./cmd/sandbox-demo --allow-local-fixtures --fixture python-hello
```

Without native Go on Windows:

```powershell
docker run --rm -v "${PWD}:/src" -v judgegrid-go-cache:/go -v judgegrid-go-build-cache:/root/.cache/go-build -w /src golang:1.26 sh -c 'GOOS=windows GOARCH=amd64 go build -buildvcs=false -o bin/sandbox-demo.exe ./cmd/sandbox-demo'
& '.\bin\sandbox-demo.exe' '--allow-local-fixtures' '--fixture' 'cpp-hello'
& '.\bin\sandbox-demo.exe' '--allow-local-fixtures' '--fixture' 'python-hello'
```

Fresh local executables can be subject to existing Windows security scanning. Do not disable that protection. The fixture flag is required. The CLI accepts approved fixture names, not raw arbitrary host commands or a public source payload. It outputs JSON with actual environment metadata, separate phase outcomes, runtime image identity, limits and cleanup status.

Other fixtures: cpp-syntax, cpp-loop, cpp-crash, python-sleep, python-memory, python-processes, python-stdout, python-stderr, python-network, python-files, python-policy and python-crash. These are small, controlled workloads inside bounded disposable containers. Do not replace them with host resource-exhaustion experiments.

## Limits and outcomes

Compilation and Python preparation default to 15 seconds/256 MiB. Runtime defaults to 3 seconds/64 MiB. Each phase has a combined stdout/stderr budget of 64 KiB. Source and stdin are each capped at 64 KiB. Contract limits reject invalid or excessive values. The demo uses two whole-execution slots; the library permits at most four per controller instance. Multiple controller instances do not form a global distributed slot allocator.

C++ compilation errors are compile-phase nonzero outcomes; run-phase nonzero exits are program failures. Timeouts, output breaches, cancellations and daemon-confirmed OOM are distinct outcomes. Image/engine/policy/startup/cleanup failures return typed infrastructure errors. A SIGKILL-like exit alone is not classified as OOM. No AC/WA/TLE/MLE judge verdicts are persisted.

Controller wall time includes attach/start overhead. CPU time and peak-memory values are explicitly null/unavailable, rather than estimated as reliable measurements. General syscall-denial attribution is unavailable; approved fixtures can demonstrate specific denied operations without proving arbitrary hostile-code safety.

The controller actively kills the sandbox namespace on timeout/cancellation. A separate in-container timeout bounds computation after abrupt controller loss. Deferred cleanup removes phase containers and their private volume, then releases the execution slot. Cleanup errors are infrastructure errors, not hidden successes. A hard host/controller crash can leave stopped metadata requiring operator reconciliation; inspect only resources belonging to the relevant generated execution ID.

## Run enforcement tests

On a supported Linux controller with Go and Docker available:

```sh
JUDGEGRID_SANDBOX_TESTS=1 go test -race -tags=sandboxintegration -v ./internal/execution
```

On Windows without native Go:

```powershell
docker run --rm -v "${PWD}:/src" -v judgegrid-go-cache:/go -v judgegrid-go-build-cache:/root/.cache/go-build -w /src golang:1.26 sh -c 'GOOS=windows GOARCH=amd64 go test -c -buildvcs=false -tags=sandboxintegration -o bin/sandbox-tests.exe ./internal/execution'
$env:JUDGEGRID_SANDBOX_TESTS = '1'
& '.\bin\sandbox-tests.exe' '-test.v' '-test.run=TestSandbox'
```

Quote dotted native test flags in PowerShell. These native Windows integration tests use Docker's Linux backend; they are not Windows race-detector runs. Linux unit/race checks run separately, and the hosted dedicated sandbox job uses race-enabled tests on its Linux controller host. Tagged tests fail when explicit opt-in or required runtime setup is missing; they do not silently skip.

`python scripts/check-service-isolation.py` verifies ordinary Compose services lack daemon socket/endpoint authority, privileged mode and host PID/network namespaces. See [the threat model](sandbox-threat-model.md), [ADR 0004](adr/0004-local-sandbox-execution.md) and [vendored profile origin](../infra/sandbox/README.md) for policy and limitations.

## Session 8: trusted orphan reconciliation

Build `go build -o sandbox-reconcile ./cmd/sandbox-reconcile` on the trusted controller host. Apply migration 0007 through the existing management command before running updated agents. Configure `APP_ENV=development` and `DATABASE_URL` for that host. Docker must point to the same trusted Linux daemon used by the agent. Keep development opt-in and production-disabled defaults.

```sh
# Read-only default: inspect decisions first.
./sandbox-reconcile --allow-local-reconciliation --max-resources=64
# Explicitly authorize bounded cleanup of database-proven orphans.
./sandbox-reconcile --allow-local-reconciliation --max-resources=64 --apply
```

PowerShell uses the same flags with `.\sandbox-reconcile.exe` and `$env:APP_ENV='development'`. Maximum inventory is 128 and total command deadline is 60 seconds. An oversized inventory fails without deletion; investigate rather than sweeping by prefix. Reports expose execution ID, decision and resource count only. Decisions include active, eligible, reaped, unknown, mismatch and interrupted/unavailable errors. Dry-run never deletes. Unknown/unregistered fixtures and legacy namespaces are not automatically deleted. Review their exact provenance separately; never use a broad Docker prune as recovery.

The registry is committed before resource creation. A per-execution PostgreSQL lock guards creation and cleanup; active current leases are preserved. Old-generation cleanup does not lock the newer owner. Apply re-enumerates and revalidates resources under that lock. Exact container IDs and volume name/creation/labels are rechecked before deletion. Docker provides no atomic conditional volume delete; the daemon administrator remains trusted. Database/schema failure prevents deletion. If cleanup is interrupted after removing a container, retry safely removes the remaining volume. Physical deletion cannot roll back with SQL.

Normal cleanup removes resources and closes the registry record. A process kill bypasses defer; the in-container watchdog limits computation and the reconciler later removes stopped or running expired resources. Fenced result writes and resource termination solve different problems. A crashed host may also leave controller seccomp temporary copies; this reconciler deliberately never accepts arbitrary host paths from labels.

See [security evidence](sandbox-security-controls.md), [threat model](sandbox-threat-model.md) and [ADR 0008](adr/0008-sandbox-provenance-and-reconciliation.md). Aggregate artifact-volume quotas, CPU-time and peak-memory measurement remain unavailable.
