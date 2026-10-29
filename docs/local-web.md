# Trusted local web workspace

The embedded, dependency-free frontend supports published problem selection, C++17/Python starters, source editing, idempotent submission, genuine SSE phases/verdicts, refresh/reconnect and owner-only evaluation history. Source is never stored in browser localStorage; only principal-scoped last submission IDs and event cursors are retained. Timeline DOM history is capped at 60 entries. Keep this development demo on loopback with trusted operators and submitted programs.

First follow [local judging](local-judging.md) and [execution agent](execution-agent.md) for pinned runtimes, seccomp, private suites and trusted host prerequisites. Build controller binaries from the repository; they deliberately remain absent from ordinary service images. Use the existing optional private S3/cache configuration when those suites were published remotely.

Apply migrations and seed published suites before enabling admission. All examples below run from the repository root. Native host tools need the database/Redis loopback ports from `infra/local-judge.compose.yml`; ordinary service images do not need them.

```sh
docker compose -f docker-compose.yml -f infra/local-judge.compose.yml up --build -d --wait
docker compose -f docker-compose.yml -f infra/local-judge.compose.yml run --rm migrate manage seed
```

Set APP_ENV=development, DEV_AUTH_ENABLED=true and DEV_PRINCIPAL to your trusted operator identity for the *host administrative commands*. Create a private password file under ignored `bin/` (12–128 bytes, optional trailing newline). Do not pass passwords in CLI arguments or commit them.

For a fresh local suite store, run `go run ./cmd/judge-local --allow-local-judging --seed-tests` with those trusted host settings before enabling the scheduler. This binds version-one demo suites; it does not replace any newer remotely published suite. Keep the selected bundle backend consistent with the published versions.

```sh
go run ./cmd/judge-admin --allow-development-administration --action local-user --user alice --password-file bin/alice.password
go run ./cmd/judge-admin --allow-development-administration --action local-user --user bob --password-file bin/bob.password
```

Use this trusted policy file (`bin/local-policy.json`):

```json
{"enabled":true,"global_active":2,"principal_active":1,"pending_limit":128,"rate_burst":120,"rate_per_second":2,"sse_global":32,"sse_principal":2,"principals":["alice","bob"]}
```

```sh
go run ./cmd/judge-admin --allow-development-administration --action scheduler-config --file bin/local-policy.json
docker compose -f docker-compose.yml -f infra/local-judge.compose.yml -f infra/local-web.compose.yml up --no-deps -d --wait api
```

Start the explicitly enabled outbox relay on an isolated stream, for example `OUTBOX_RELAY_ENABLED=true`, `OUTBOX_STREAM=test:judgegrid:local-web`. Start two independent trusted host agents with `JUDGE_AGENT_ENABLED=true`, `AGENT_FAIR_ENABLED=true`, `AGENT_PRINCIPALS=alice,bob`, `AGENT_STREAM=test:judgegrid:local-web`, and AGENT_CONCURRENCY=1. Their existing required development identity/flag and pinned-image/bundle configuration still apply:

```sh
go run ./cmd/judge-agent --allow-development-execution
```

The CLI requires every enrolled principal in its allowlist so a missing head cannot silently starve the pool. A scheduler-policy scope change requires agent restart. Open `http://localhost:8080`, sign in with the operator-provisioned account, choose a problem and submit. The browser uses its own account, not DEV_PRINCIPAL. Rejudge remains an offline owner-scoped administrative command, never a browser admin route. Switching language/problem loads a starter; ambiguous submission failures can be retried with the same in-memory idempotency key. Reloading before an ambiguous POST is resolved does not persist that pending key; inspect your list before retrying as a new intent.

The default policy is disabled. Configuration rejects lowering execution limits below current valid active work or lowering an enrolled principal's pending budget below its existing count. Pending includes active work. Rate tokens still apply to successful replays. See [ADR 0010](adr/0010-fair-admission-and-public-progress.md) for guarantees and limits.

For shutdown, stop the owned agents and relay, configure a policy with `enabled:false` and then restore the default Compose API. Do not disable admission while fair agents are still running. Default Compose omits the web overlay, keeps browser authentication/routes disabled, publishes only the loopback API port and removes host database/Redis ports. Preserve volumes and unknown execution resources. Credentials, runtime manifests, bundles and learning notes stay ignored.

Production authentication, internet exposure, a public contest service and visual-browser validation are not implied by the local demo. Session 10's browser automation was unavailable; its live API/assets/SSE/judge checks are recorded separately in the completion report.
