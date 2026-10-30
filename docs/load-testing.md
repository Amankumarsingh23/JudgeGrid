# Bounded development load experiments

Use Python **3.13.15** (`.python-version`), only its standard library, the repository's pinned Go 1.26 toolchain and explicitly pinned Compose images. `scripts/load.py` restricts destinations to loopback, rejects unbounded concurrency/work/duration and prints no password, cookie, source or hidden tests. No external load generator/account is required. Environment proxies are disabled. For HTTP localhost, the client connects to IPv4 loopback while keeping the configured Host/Origin/cookie identity unchanged; HTTPS retains ordinary certificate verification.

## Isolate the laboratory

Use a fresh schema named `judgegrid_s11_lab_<timestamp>` in the owned local PostgreSQL instance, a unique `test:judgegrid:s11:<schema>` Redis stream, separate private bundle/cache buckets, offline-provisioned local users and ignored password files under `bin/`. PostgreSQL URL `options=-csearch_path%3D<schema>` selects the schema for migrations, seed, API, relay and native controllers. Validate the schema identifier against a fixed prefix and digits before using it in operator SQL. Set OBS_DATABASE_URL for the observability overlay's API/relay. Do not fault shared/public services or silently run against the application's public schema.

Apply migrations/seed to that schema, initialize its private stores, seed suites, provision accounts and configure fair admission before starting agents. Initial API-only experiments can use accepted legacy Go identifiers: they exercise creation/replay/backpressure but the trusted judge rejects that unsupported runtime without launching a container. Label these as API admission experiments, never genuine C++/Python execution. Published fixture workloads with real/cache profiles use supported runtimes and actual sandbox execution.

The delivered laboratory used global active=2, per-principal active=1, pending=8, request burst=120/refill=2 per second and SSE global=8/per-account=2. An A100/B1 stress test uses its separate isolated test schema and pending budget 128, two independent subprocesses and lightweight database report fixtures. It proves scheduling order/caps, not real compiler capacity.

## Profiles

```sh
python scripts/load.py --profile read --username s11-a --password-file bin/s11-a.password --total 30 --concurrency 2 --duration 30 --seed 11 --output bin/load-read.json
python scripts/load.py --profile create --username s11-a --password-file bin/s11-a.password --total 6 --concurrency 2 --duration 30 --seed 11 --output bin/load-create.json
python scripts/load.py --profile replay --username s11-a --password-file bin/s11-a.password --total 10 --concurrency 2 --duration 30 --seed 11 --output bin/load-replay.json
python scripts/load.py --profile burst --username s11-a --password-file bin/s11-a.password --total 12 --concurrency 3 --duration 30 --seed 11 --output bin/load-burst.json
python scripts/load.py --profile real --username s11-b --password-file bin/s11-b.password --total 2 --concurrency 2 --duration 90 --seed 11 --output bin/load-real.json
python scripts/load.py --profile cache --username s11-a --password-file bin/s11-a.password --total 3 --concurrency 1 --duration 120 --seed 11 --output bin/load-cache.json
python scripts/load.py --profile sse --username s11-b --password-file bin/s11-b.password --total 4 --concurrency 1 --duration 30 --seed 11 --output bin/load-sse.json
```

Maximums are 200 requests, concurrency 16 and 180 seconds; real/cache cap at six submissions. Each socket operation has a bounded remaining deadline. Login/problem lookup and optional replay/SSE precondition creation are setup, not measured HTTP samples; the latter is reported separately. SSE samples are connect+reconnect pairs, not individual requests. Cache trials are serial with identical source and distinct idempotency keys: first cold, later warm. A fresh source comment/run identity prevents an earlier experiment from being mistaken for the cold trial. Use durable cache_hit/compiler counters to verify the assumption.

For pending-quota experiments leave agents stopped until the bounded queue is filled, then start them and observe drain. For request-rate experiments use an explicitly small burst/refill and a fresh account/bucket; reset only the named lab bucket through trusted local SQL if repeating the experiment. Restore policy afterward. Do not use a valid replay key to bypass rate limiting. No script disables server admission/authentication.

## Measurement rules

Each output records sample count, seed, actual Python version, concurrency/time caps, status counts, throughput and nearest-rank p50/p95/p99. Rejected, accepted, replayed and completed populations remain separate. Cache HTTP throughput divides by summed serial request time; job throughput includes the drain window. Completed verdicts come from owner-authorized GET, never simulated responses.

`terminal_duration_seconds` uses PostgreSQL updated_at−created_at for original laboratory submissions, while `poll_wait_seconds` describes only the client's observation wait. Sequentially polling an already completed second job can yield a tiny poll wait; it is not tiny end-to-end judging latency. Rejudge duration uses its own logical intent timestamp in telemetry, not the original submission's age. Server `sse.delivery` ends at flush; client snapshot/replay workflow latency and the tagged SSE fixture's DB-journal-to-client interval are distinct evidence.

Read-only baseline, POST creation, identical-key replay, burst/pending/rate rejection, A100/B1 fairness, real Python/C++, cold/warm cache and SSE reconnects are all bounded profiles. These are short load/burst tests. A stress test explores controlled degradation, a soak test requires sustained observation, and chaos tests inject a particular owned fault. This session does not claim a long-duration soak or production saturation benchmark.

An initial Windows urllib localhost run measured approximately two seconds per request. A direct comparison observed localhost=2.0809s versus IPv4 loopback=0.0173s. The corrected 30-read sample measured p50=2.82ms, p95=24.21ms and 418.52 requests/s over only 0.0717s. This exposes a client connection-path effect; it does not prove the judge can sustain that arrival rate. Prefer server histograms plus controlled client transport when diagnosing application latency.

The initial genuine Python sample accepted two requests and completed both AC in a 20.90s observation window (0.0957 completed/s), constrained to one active execution for that account. Three initial C++ trials completed AC in 32.78s: cold observed wait 11.07s, warm waits 10.78s/10.84s. The small cache saving is consistent with repeated fresh-container/policy/cleanup overhead remaining after compilation is avoided; it does not establish a universal cache speedup. Final counts, SQL durations, rate-limit decisions, active peaks and subsequent verified trials are recorded in the Session 11 report.

Preserve the environment record: Docker Desktop Linux engine/kernel/cgroup version, pinned image/seccomp identities, visible CPU/RAM, service memory/CPU limits, agent/global/per-account caps and workload/tool versions. No new per-submission CPU or peak-memory measurement is available. Do not add overlapping phase histograms to invent an end-to-end total. Do not infer a p99 reliability target from two or three jobs.

After experiments drain accepted work, stop only owned agents/relay, check valid-active=0, pending=0, registry cleanup and SSE lease release, restore dependencies/default Compose and retain evidence. Preserve unknown older resources; use the existing authority-based reconciler for known stale ones. Passwords, manifests and evidence containing local identifiers remain ignored. A bounded lab schema/stream can be retained for audit or removed later by an explicit operator workflow; no broad prune is part of these scripts.
