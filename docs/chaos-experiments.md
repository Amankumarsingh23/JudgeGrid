# Controlled chaos experiment catalog

Authority invariants remain more important than responsiveness during a fault. Every fixture uses an isolated PostgreSQL schema and Redis stream, exact owned helper processes, existing TCP gates or the named local MinIO test container. Unknown/active Docker resources are preserved. A fault never grants a new generation, revives an expired lease, relaxes seccomp/TLS or fabricates a verdict.

The optional `S11_TEST_TELEMETRY=1` test bootstrap initializes telemetry in instrumented agent tests **and their subprocess helpers**. Set APP_ENV=development, TELEMETRY_ENABLED=true, TELEMETRY_ADDR=127.0.0.1:0 and TELEMETRY_OTLP_URL to the owned local receiver. This observes the established crash windows while retaining their real assertions. Missing export data is expected when a process is killed. It is not evidence that the business transition did not occur.

| Case | Hypothesis and exact injection | Required invariant / assertion | Telemetry evidence |
| --- | --- | --- | --- |
| A | Gate Redis during outbox publication (`TestOutboxRedisOutageBacklogAndMultipleRelays`) | Accepted intent remains pending/retryable, then publishes; no duplicate authoritative effect | outbox publish/retry outcomes, bounded backlog snapshot/oldest age |
| B | Drop XADD response after Redis accepted entry (`TestOutboxLostXADDResponse`) | Republish may duplicate transport, one durable intent/effect remains | multiple physical publish spans linked to one producer, transient retry count |
| C | Kill relay helper after publish before recording PUBLISHED (`TestOutboxCommittedAPICrashAndRelayCrash`) | Claim expires, successor publishes/records safely | failed/incomplete physical span, later claim/publication linked to producer |
| D | Drop agent PostgreSQL heartbeat connectivity through an owned TCP gate (`TestAgentDatabaseHeartbeatOutage`) | Cancel on uncertainty, no result rows during outage, generation two recovers | lease renewal error, canceled/error execution, replacement claim/finalization |
| E | Kill agent helper during sandbox run (`TestAgentProcessCrashRecovery/during`) | In-container watchdog bounds work, expired generation replaced, genuine terminal outcome | truncated execution trace, recovery claim and committed terminal counter |
| F | Kill helper after database commit before XACK (`.../after_commit`) | Replay acknowledges committed result without another authoritative execution | duplicate delivery/processing spans and one committed outcome |
| G | Replace known cached artifact bytes with poison (`TestS9RealCacheConcurrencyAndPoisoning`) | Hash/ELF mismatch rejected before runtime start; metadata eviction permits rebuild | cache lookup failure, unchanged runtime-start evidence, later build |
| H | Stop the explicitly named local MinIO during bundle fetch (`TestS9ObjectStoreOutageRecovery`) | Infrastructure failure/null verdict, restart permits genuine AC | bundle fetch error followed by retry/recovery; no false user verdict |
| I | Stop an owned OTLP/HTTP receiver while real fair judging executes (`TestS11FairWorkSurvivesTelemetryAndRedisFault`) | AC commits exactly once despite unavailable exporter; 128-span queue drops rather than blocking | exporter error/drop counts and genuine terminal AC |
| J | Concurrent fair agents plus short Redis gate outage, with I's receiver failure | Fair DB scanner continues; no extra run/stale commit, valid-active returns zero | scheduler/claim/execute spans, one run, recovered ACK/zero active |
| K | Disconnect/reconnect SSE across committed progress (`TestSSEReplaySharedCapsAndLagReset`, Session 10 runtime matrix) | Contiguous durable replay or authoritative reset; original AC/new WA retained | reconnect/replay/reset counters, bounded server flush durations and client fixture timing |
| L | Known stale resource cleanup with active/unknown/mismatched resources present (ten orphan cases) | Exact authorized stale resources reaped; active/unknown preserved; DB outage fails closed | reconciliation scan/reap/preserve/error observations and concrete inventory assertions |

These rows name reproducible functions, not inferred successes from a graph. Actual run results/classification and final states belong in [Session 11 completion](session-11-completion.md). Case C's deliberately killed child may lose its final telemetry batch; durable Redis/SQL assertions provide the injection evidence. Cases D/F test fencing and exactly-once effects, not exactly-once delivery. Cases G/H use real artifact/object paths internally but never attach them to metrics or spans.

## Exporter and scrape failures

Unit tests exercise an absent collector at startup, an occupied metrics listener, 1000 completed spans against a blocked exporter, malformed/untrusted trace context, bounded drain and span linkage. A full queue can retain at most 128 spans plus a 16-span export batch. Export attempts have 500 ms timeouts and no retry queue; shutdown has a finite budget. A scrape is served from cached/local collectors even after its database pool is closed. A wrong explicit telemetry URL/bind configuration is a configuration error; a well-configured but unavailable receiver/port is an optional diagnostics failure.

The combined real-runtime fault first verifies a healthy receiver accepted exports, then closes that exact receiver and gates only the test agent's Redis connections. It deliberately generates 1000 spans to exercise capacity, waits for a real AC, restores Redis, verifies one run/zero valid-active leases and waits for the configured notification reclaim budget. An early supplemental run's 12-second ACK wait was shorter than the 30-second min-idle policy; the test now allows the existing 45-second recovery window instead of weakening leases or requeueing hotly.

## Reproduction and restoration

Run the ordinary race/DB suites, then sequentially run sandbox, local judge and instrumented agent/S9/S10/S11 suites on the same trusted daemon. See CI and [load testing](load-testing.md). Do not overlap heavy image builds/real runtime matrices: controller wall time includes Docker overhead and timing interference can produce real TLEs.

Record the hypothesis, injection boundary, expected SQL/Redis/container state, actual observations and metrics/trace evidence for each run. Keep the old six-function private object/cache/rejudge matrix and A100/B1/20-way races. Gate restoration and goroutine drain happen through fixture cleanup; the local MinIO is restarted after its owned outage. Reconciliation is still provenance- and authority-checked. Unknown resources are never deleted just to make a test inventory empty.

After the laboratory demonstration stop owned processes only after valid-active=0, stop the owned relay/diagnostics services, disable its scheduler policy and restore the normal API configuration. Keep evidence, private credentials and runtime manifests ignored. Production/public infrastructure and long-running uncontrolled floods are outside this harness.
