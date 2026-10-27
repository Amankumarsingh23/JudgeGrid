# Trusted development execution agent

This host-side controller is explicitly authorized to execute the configured development principal's C++17/Python submissions. It is absent from ordinary service images. No public execution endpoint, Docker socket mount, privileged workload or production automatic consumer is added.

Use a trusted host with the existing pinned runtime manifest, verified seccomp profile and Docker Linux/cgroup-v2 support. The agent checks both image capabilities before startup. Choose a fresh principal and isolated stream for demonstrations; a published historical QUEUED intent for that same principal is eligible for recovery. Legacy pre-lease RUNNING jobs and one-shot local-mode abandoned jobs require operator investigation and are not silently taken over.

```powershell
$env:APP_ENV='development'
$env:DEV_AUTH_ENABLED='true'
$env:DEV_PRINCIPAL='aman-s7-auto'
$env:OUTBOX_RELAY_ENABLED='true'
$env:OUTBOX_STREAM='test:judgegrid:s7-live'
docker compose -f docker-compose.yml -f infra/local-judge.compose.yml --profile outbox up --build -d --wait
docker compose -f docker-compose.yml -f infra/local-judge.compose.yml run --rm migrate manage seed
$env:DATABASE_URL='postgres://judgegrid:judgegrid@127.0.0.1:15432/judgegrid?sslmode=disable'
$env:REDIS_ADDR='127.0.0.1:16379'
go build -o bin/judge-local.exe ./cmd/judge-local
bin/judge-local.exe --allow-local-judging --seed-tests
go build -o bin/judge-agent.exe ./cmd/judge-agent
$env:JUDGE_AGENT_ENABLED='true'
$env:AGENT_STREAM='test:judgegrid:s7-live'
bin/judge-agent.exe --allow-development-execution
```

In another terminal run `python scripts/agent_smoke.py`. It creates real C++ AC/WA/CE, Python AC/RE/TLE and unsupported Go fixtures through the API and waits for automatic results. Use the README's optional public CA build secret on HTTPS-inspected hosts; never disable TLS verification. The optional overlay exposes PostgreSQL/Redis only on loopback; default Compose exposes neither. Native Go binaries can be cross-built in a Go container without granting that container Docker authority.

| Variable | Default | Bounds/policy |
|---|---|---|
| JUDGE_AGENT_ENABLED | absent | Must equal true, plus explicit CLI flag |
| AGENT_STREAM | test:judgegrid:agent | dev:/test: stream only; match relay destination |
| AGENT_GROUP | judge-agents | Bounded nonempty group |
| AGENT_LEASE | 30s | 1s–5m, DB-issued expiry |
| AGENT_HEARTBEAT | 10s | 50ms–lease/3 |
| AGENT_RECOVERY | 2s | 50ms–1m, DB scan and PEL recovery pacing |
| AGENT_ATTEMPTS | 3 | 1–5, fixed when first attempt is granted |
| AGENT_CONCURRENCY | 2 | 1–4 shared pool/scanner execution slots |
| AGENT_DRAIN | 20s | 1s–3m, then cancellation and bounded sandbox cleanup |

The maximum evaluation duration remains 120s, with separate compiler/runtime budgets. Heartbeats use independent database connections and bounded deadlines. A single failed renewal fails closed: cancel, discard unfinished evidence and wait for expiry/takeover. Restoring connectivity after expiry cannot revive the old worker. Local fixture/local-judge CLIs remain available; new one-shot evaluation results use a 180s fence and retain the existing advisory lock.

The current protocol accepts only initial intent generation one. Outbox relay claim generations are not execution generations. Runtime admission validates the event, its immutable submission version and principal in PostgreSQL. Fresh valid claims advance execution generation; duplicates do not. A valid different owner causes a retry without ACK. Completed replay verifies terminal run/submission provenance rather than trusting a Redis message or status alone. Exact persistence replay compares the report fingerprint; a different report is a conflict.

## Recovery and inspection

The database scanner can recover without Redis entries or PEL ownership. It queries published, unowned QUEUED work and due/expired agent ownership for the configured principal, then calls the same atomic claim as Redis delivery. An old ACTIVE attempt becomes immutable FAILED/lease_expired before a new run is granted. Retryable infrastructure failures retain RUNNING/EVALUATING workflow state with ownership RETRY and a one-second minimum recovery delay. User verdicts are terminal. Exhausted attempts or permanent configuration/bundle failures become visible FAILED/null-verdict outcomes. Unsupported languages create `execution_rejections`, without a fake CE/RE or evaluation.

```sql
SELECT o.submission_id,o.run_id,o.generation,o.owner_id,o.state,o.lease_expires_at,
       o.heartbeat_at,o.next_attempt_at,o.max_attempts,o.failure_code
FROM execution_ownership o ORDER BY o.started_at DESC LIMIT 100;
SELECT submission_id,generation,owner_id,status,failure_code,final_verdict,started_at,finished_at
FROM evaluation_runs ORDER BY started_at DESC LIMIT 100;
SELECT submission_id,event_id,reason,created_at FROM execution_rejections ORDER BY created_at DESC LIMIT 100;
```

There is no automatic rejudge/reopen operation or lease override command. Do not manually extend an expired worker lease to reuse its output. Investigate exhausted/legacy work deliberately; a later protocol can add controlled operational actions without erasing history.

SIGTERM/Ctrl+C stops new work and drains. Forced process death stops heartbeats; in-container watchdogs bound running computation and PostgreSQL expiry enables takeover. Abrupt host/controller death can leave stopped containers, private volumes or temporary policy files; fault tests remove only their identified artifacts after confirming watchdog termination. There is no automatic orphan-artifact reaper. Session 8 should harden that lifecycle. Do not delete arbitrary shared volumes as cleanup.

After all demo work is terminal, stop the host agent, stop the optional relay and run default Compose with development variables removed. This restores development-auth-off and removes the temporary database/Redis loopback ports. Preserve database, Redis and private bundle data.

## Interview explanations

A lease is time-bounded permission; a heartbeat is a request to extend that permission under the same identity. XAUTOCLAIM moves delivery ownership, while a PostgreSQL transaction grants execution authority. Fencing rejects a paused zombie worker's old generation even if cancellation never reached it. Physical execution may repeat after crashes; exactly-once authoritative effects come from fenced atomic result transactions and verified replay. Crash before commit leaves recoverable work; crash after commit/before ACK reads the terminal result and ACKs without rerunning. A database outage is uncertainty, not evidence that a submitted program is wrong.
