# Session 11 — optional observability, bounded load and controlled faults

Built on verified Session 10 commit `b5dc00250996670ee16202523a211f21ed822a06`. This change instruments the existing judge; it does not add judging capabilities, production identity, public telemetry or Session 12 repository/RAG ingestion.

## Architecture and correctness boundary

The API, relay, worker, trusted agent and reconciler use one optional process-local telemetry runtime. OpenTelemetry Go/SDK/OTLP HTTP 1.46.0 emits bounded traces directly to Tempo 3.1.1. client_golang 1.23.2 supplies one Prometheus metrics path; Prometheus 3.15.1 scrapes explicit API/relay/two-agent targets, and Grafana 13.3.0 provisions two data sources/four dashboard groupings. A Collector is unnecessary in this local topology. Versions/configuration are pinned and actual backend queries are checked separately from browser rendering.

Telemetry is disabled by default and development-only when explicitly enabled. Listeners are loopback or an explicitly private container interface. The ordinary API has no public metrics route, Docker socket, S3 secrets or trusted controller binaries. Unknown label values collapse to fixed categories; no account/submission ID, source hash, object key, URL, trace ID or raw error text becomes a metric label. Traces omit source, test material and credentials. Selected lifecycle logs contain trace/span IDs for correlation.

Migration 0013 stores immutable validated producer trace context with the existing outbox transaction. Wire schemas 4/5 add optional correlation to initial/rejudge references while preserving old schemas and authoritative payload identities. Public caller trace headers are ignored. Physical publication/processing/execution attempts link to the durable producer. Owner authorization, fair turns, leases/generations, immutable results and SSE counter ordering remain PostgreSQL decisions.

The exporter uses a nonblocking 128-span queue, 16-span batches, a 250 ms timer and a 500 ms deadline with retries disabled. Queue/export losses are observable; they never repeat a committed verdict. Metrics bind failure is optional diagnostics failure. The API caches bounded DB inventories every five seconds with a one-second query deadline and truncation/freshness indicators; scrapes have no live SQL dependency. Timing contracts distinguish local monotonic intervals, timestamp estimates, overlapping preparation stages, cached restoration and actual compilation. CPU/peak memory remain unavailable/null.

See [ADR 0011](adr/0011-optional-observability.md), [operations/SLIs/runbooks](observability.md), [load profiles](load-testing.md) and [fault catalog](chaos-experiments.md).

## Actual bounded workload evidence

The owned laboratory uses its own `judgegrid_s11_lab_*` schema, isolated test stream, private suite/cache buckets and local cookie accounts. Configured caps are global active=2, principal active=1, pending=8, SSE global=8/principal=2. Ordinary request burst/refill is 120/2s; a separate short rate trial used burst=2/refill=0.01s and then restored the policy. Tooling: Python 3.13.15 standard library, pinned Go 1.26 and Docker Desktop Linux/cgroup-v2 runtimes. Credentials/evidence/manifests stay ignored.

| Profile | Actual measured samples/results | p50 / p95 / p99 HTTP or workflow latency | Observed short-window rate |
| --- | --- | --- | --- |
| Read, IPv4 loopback | 30 requests, concurrency 2, all 200 | 2.82 / 24.21 / 24.27 ms | 418.52 requests/s over 0.0717s |
| Distinct creation | 6 requests, concurrency 2, six 201 | 52.04 / 205.63 / 205.63 ms | 21.36 accepted/s over 0.281s |
| Identical-key replay | 10 samples plus one setup creation, all replay 200 | 14.26 / 35.72 / 35.72 ms | 63.21 replays/s over 0.158s |
| Pending-cap burst | 12 samples, one 201 and eleven 429 | 27.94 / 62.60 / 62.60 ms | 6.68 accepted/s and 73.47 rejected/s over 0.150s |
| Request-rate burst | 5 samples, two 201 and three 429 | 14.65 / 34.20 / 34.20 ms | 36.59 accepted/s and 54.89 rejected/s over 0.0547s |
| Genuine Python | two accepted, two completed AC | 32.47 / 49.10 / 49.10 ms POST | 0.0957 completed/s over 20.90s including drain |
| Cold/warm C++ | three accepted, three AC, durable hit flags false/true/true | 33.83 / 35.86 / 35.86 ms POST | 0.0915 completed/s over 32.78s including drain |
| SSE reconnect | four connect+reconnect pairs, all 200 | 15.21 / 65.66 / 65.66 ms per pair | 25.09 pairs/s over 0.159s |

These are small workload observations, not production capacity or a statistically reliable p99. Login/problem setup and replay/SSE setup creation are excluded from sample counts; setup is reported separately by the tool. API-only creation/burst fixtures use unsupported legacy Go work and do not claim container execution. Those intents were drained to safe FAILED; the supported Python/C++ cohorts ran real private tests.

Durable SQL measured the two Python accepted-to-terminal durations as **10.657s and 20.757s**, with admission waits **0.481s and 10.666s**. The second waited behind the same account's active cap. Three C++ durations were **10.962s cold, 10.698s warm and 10.677s warm**, with waits **0.392s, 0.184s and 0.475s**; all ran six tests. Fresh per-case container/policy/cleanup overhead remains after cache reuse, so a dramatic total speedup is not established. The separate ten-request regression verifies one build/nine hits.

The original localhost client sample was about two seconds per request; direct comparison measured localhost 2.0809s versus IPv4 loopback 0.0173s. The bounded load client now avoids that connection-path delay without changing Host/Origin/cookie security. Sequential polling wait is also reported separately from DB terminal duration, preventing an already completed second job's tiny polling wait from being called its end-to-end latency.

The independent A100/B1 test uses two actual subprocesses with lightweight DB result fixtures. B is admitted by turn 2, global active peak=2/principal peak=1, with extra claims refused while full. That proves fairness/caps, while separate sandbox matrices prove genuine verdicts. The lab's pre-cleanup durable check observed five supported AC jobs, zero pending/valid-active/open-registry/SSE leases, and nine safely rejected unsupported-runtime fixtures.

## Fault and telemetry evidence

Cases A–L are defined with exact injection points and assertions in [the fault catalog](chaos-experiments.md). Real Redis TCP gates, lost-XADD replies, killed relay/agent helpers, DB heartbeat outages, artifact poisoning, local MinIO outages, concurrent fair recovery, SSE reconnects and authority-based orphan cleanup are retained. The new I/J test first verifies a healthy owned OTLP receiver, stops it and gates the agent's Redis connection while real judging runs, fills the export queue, then verifies one genuine AC/run, recovered acknowledgement and zero valid-active ownership. Existing crash helpers run with telemetry enabled when requested.

Early development checks found and corrected two observability-specific issues: traced crash fixtures needed to include the optional durable field while preserving strict delivery equality; and the pinned exporter sends a pathless URL to `/`, requiring explicit normalization to `/v1/traces`. The latter now has an actual HTTP path test and backend storage/query verification. An early ACK wait also underestimated the existing 30-second reclaim policy; its test budget was corrected without relaxing ownership. Supplemental Windows runs, both with telemetry enabled and disabled, exposed timing sensitivity in inherited fixtures with a 250 ms heartbeat command deadline and two-second sandbox budgets. Safe physical retries, a runtime-start wait timeout and a recovered C++ TLE failed strict assertions in separate attempts. These failed attempts are not counted as passing, and neither leases nor sandbox limits were weakened.

## Verification, final state and limitations

No skipped/unavailable check is counted as passing. The local Docker host reports 16 CPUs, 8,130,551,808 bytes of memory and kernel 6.18.54.1-microsoft-standard-WSL2 (Docker Desktop 29.9.2). Prometheus/Grafana each have 256 MiB/0.5 CPU caps; Tempo has 512 MiB/0.5 CPU and a 256 MiB temporary trace store.

Formatting, vet, normal/race unit tests and executable builds passed. The code now contains **69 unit test functions** (60 prior plus eight telemetry and one queue-context function). The complete uncached PostgreSQL/Redis run passed **45 behavioral functions** under `-race`, including the prior 43, 20-way races, 50-job distribution, A100/B1 fairness by turn two, and two new observability functions. The terminal-metric test verifies that retry-budget exhaustion with a retryable failure counts committed FAILED once and refused replay adds nothing. The original **30 sandbox scenarios** and **11 local judging scenarios** passed. Native Windows runtime binaries are not race-instrumented; Linux CI runs tagged runtime tests with `-race`.

The **16 baseline agent scenarios**, **ten orphan cases**, **six Session 9 functions**, Session 10 matrix and new Session 11 function were all executed. Successful local evidence exists for every required fault case below, but the supplemental full Windows agent invocations were not wholly green: their short-deadline failures are disclosed above. Do not interpret individual successful reruns as a single complete local pass. Hosted CI separately runs the full original agent/orphan suite, Session 10, and the instrumented Session 9/11 matrix; its observed result and pushed revision are reported at delivery.

Actual configuration checks passed: Compose validation, deterministic generation of four dashboards, Prometheus `promtool check config` and all seven alert rules, Grafana database health, both provisioned data-source health checks, successful PromQL queries and Tempo trace retrieval. After the final demonstration the API, relay and both native-agent targets were simultaneously UP. No graphical dashboard validation was performed.

## Final linked-job and scrape-outage demonstration

On 2026-10-30 the final current-binary Python cohort accepted two requests, returned two genuine AC outcomes through six private tests each, and drained in 27.098 seconds (0.0738 completed/s). HTTP p50/p95/p99 were 35.88/58.19/58.19 ms; durable terminal times were 13.667 and 26.945 seconds, with admission waits 0.635 and 13.687 seconds. The same-account active cap explains serialization.

The final C++ cohort accepted three requests and returned three AC outcomes, with durable cache flags false/true/true. Total drain was 41.935 seconds (0.0715 completed/s); HTTP p50/p95/p99 were 42.44/55.96/55.96 ms. Terminal durations were 14.943/13.562/13.000 seconds and admission waits 1.408/0.540/0.403 seconds. Seeds were `11-final-scrape-outage` and `11-final-causal-trace`; total work was capped at two/three and each tool deadline at 90 seconds. The owned Prometheus container was stopped during execution; the last two C++ commits completed during its 21:32:33–21:33:03 IST outage. Restart restored all four targets. Metrics collection was not an execution prerequisite.

For actual cold C++ submission `1caa0871-ab12-478e-9d68-11e6b57abe8c`, Tempo returned producer trace `6d2517829f9057b6924fe51a259f77b6` and execution trace `de79c1794de8548bd32e7b697807c589` with a matching causal link. Its 16 stored spans include bundle retrieval/verification, cache lookup, actual compilation/publication, workspace preparation, six test runs, heartbeat, evaluation and persistence. The two warm execution traces contained `cache.restore` and no `sandbox.compile`; Python traces contained staging and no compiler spans. All five current jobs had stored linked traces, checked for source/test/credential field leakage. Trace IDs are diagnostic references, never labels or authorization. Enter the execution trace in Grafana Explore/Tempo and follow its producer link while the ephemeral backend is running.

## Observed fault classifications and restoration

| Case | Observed outcome and final invariant | Classification |
| --- | --- | --- |
| A | Redis outage retained durable retryable outbox work; restoration and multiple relays published it safely | PASS |
| B | Lost successful XADD reply caused transport retry; one logical effect survived duplicate delivery | PASS |
| C | Killed relay claim expired; successor recorded publication; killed child's last export is not required evidence | PASS |
| D | Owned DB gate outage canceled uncertain execution, wrote no test results during outage and recovered generation two; separate timing-sensitive attempts failed before injection | PASS in successful dedicated execution; intermittent local reruns disclosed |
| E | Actual process death during sandbox work recovered genuine completion under the next generation; old resources were authority-checked | PASS |
| F | Death after commit before ACK replayed committed work without another authoritative result | PASS |
| G | Poisoned artifact was refused before runtime, then restored/evicted and rebuilt; ten requests also proved one compiler/nine hits | PASS |
| H | Actual named MinIO stop preserved null verdict/no partial READY; restart recovered genuine AC and rebuilt cache safely; one later recovered C++ attempt hit its wall limit | PASS in successful dedicated execution; failed attempt disclosed |
| I | Healthy receiver was stopped, 1000 spans filled the bounded queue, errors/drops were observed, real AC committed once | PASS |
| J | Two concurrent fair agents continued through the combined receiver/Redis fault; one run, recovered ACK and zero valid-active leases | PASS |
| K | Durable SSE replay/reset/caps tests passed; actual Session 10 stream retained original AC and selected new WA with contiguous progress/reconnect behavior | PASS |
| L | Ten authority/identity/provenance cases checked stale cleanup, preserved active/unknown resources and failed closed during DB outage; one default-off watchdog startup attempt missed its short lease | PASS in complete successful instrumented orphan run; failed attempt disclosed |

The fault fixtures close gates/helpers, restore the owned MinIO and drain their resources through cleanup. A killed process can lose spans; SQL/Redis/resource assertions remain the source of fault evidence. The new I/J run passed in 31.14 seconds with no extra evaluation run and zero valid-active ownership. These classifications do not erase failed supplemental invocations.

The final isolated lab contained **ten supported AC submissions and nineteen safely failed unsupported fixtures**, with **zero pending jobs, valid-active leases, open sandbox registry records and live SSE leases**. Agents were stopped only after that drain; the lab policy was disabled. The owned relay, MinIO, Prometheus, Grafana and Tempo were stopped. Default API/worker/PostgreSQL/Redis services were restored healthy, with telemetry/development auth/dispatch disabled and no host database/Redis ports. Live checks returned 401 for submissions and 404 for local login, root UI and `/metrics`. Service-isolation/controller-exclusion audits passed. Persistent data and unknown older resources were preserved.

Migration 0013 was applied to the ordinary database. All thirteen staged/disk/applied SQL hashes are checked at delivery; migrations 0001–0012 must remain identical to the baseline. Learning notes, credentials, runtime manifests, private buckets and raw evidence remain local/ignored. The authorized commit/push and hosted workflow result accompany the delivered revision.

Remaining limits: local single-host measurements are not production SLOs/SLAs; counters/export can reset/drop while SQL remains authoritative; capped/stale inventories need their health indicators; detailed per-case traces are bounded by existing case limits; no long soak or production saturation claim is made. Graphical dashboard/browser rendering is not claimed. Existing Docker/shared-kernel, aggregate artifact-volume quota, archived MinIO, trusted-operator and nullable CPU/peak-memory limitations remain. Development trace storage is ephemeral. The implementation provides stable private diagnostics and test evidence for Session 12's future commit-pinned repository ingestion without implementing it.
