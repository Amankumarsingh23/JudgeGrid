# Session 10 completion — fair scheduling, sessions and live progress

Implemented on the verified Session 9 baseline `f1134e1b5ecd38193f6de9466ead481365b82007`. This session adds shared bounded scheduling, backpressure, owner-authorized local sessions, durable public progress, resumable SSE and an embedded local frontend. It preserves immutable versions/results, cache/artifact verification, database fencing, private test delivery and Docker authority separation. Session 11 telemetry, production/public deployment, RAG and Kubernetes are outside this change.

## Delivered behavior

The explicitly enabled PostgreSQL scheduler chooses the least recently admitted eligible enrolled principal and its oldest pending logical scope. It covers initial submissions and independent rejudge jobs. One short locked transaction grants a fair turn and existing fenced ownership; sandbox execution runs after commit. Limits are shared across independent agents. Redis notifications are validated and ACKed separately from admission, and bounded DB polling recovers work after lost notifications. Expired attempts spend a fair turn, preserve queue position and cannot accept stale results.

At most 128 explicitly enrolled principals, 16 global valid active leases and a 17-row per-principal head search bound selection. A principal cannot occupy every slot in a multi-slot pool. The observed skew workload had A=100, B=1: two independent OS processes admitted B by turn 2, active peak global=2/principal=1. Twenty additional concurrent claims while full were refused. Fairness is about available admission turns, with working database/agents, covered scope and eligible published heads; it does not promise equal completion time or a bound on physical expired containers. Retry backoff can hold a principal's head. See [ADR 0010](adr/0010-fair-admission-and-public-progress.md).

Database token buckets enforce per-principal HTTP request rate across API instances. Replays consume tokens but create no new work. Per-principal pending quotas cover initial/rejudge logical jobs including active jobs until terminal state. Rate, pending and SSE capacity return structured 429 with Retry-After; scheduler deferral/backlog/unavailability use 503. Existing source/body/outbox limits remain. Default policy is disabled; enabled defaults are global=4, principal=1, pending=128, request burst=120/refill=2 per second, SSE global=32/principal=2.

The local web mode is explicitly enabled only in development on an exact loopback origin. Offline-provisioned users have salted PBKDF2 hashes and 30-minute opaque, database-revocable sessions. HttpOnly SameSite=Strict cookies, Secure for HTTPS, and Host/Origin/CSRF checks protect mutations. JavaScript receives no privileged dev key. All submission reads/lists, history, cursors and SSE retain SQL owner authorization; forged identity headers grant nothing. Production defaults reject private access. Administrative rejudge/publication remains an offline trusted command.

Migration 0012 records immutable, allowlisted, small events in the same transaction as business transitions. Per-submission counter row locks prevent commit-order cursor gaps. Actual Docker StartedAt observations produce fenced compiling/running events; verified cache restoration produces preparation. SSE captures state/cursor in repeatable read, replays after Last-Event-ID through that boundary, then streams subsequent committed rows. Snapshot boundary IDs may repeat; they replace state. Poll=500 ms, page=32, replay/lag=256, heartbeat=10 seconds, write deadline=5 seconds, stream rotation=2 minutes. Shared expiring connection leases bound clients across APIs. Old/lagged history resets to authoritative state. Session revocation/database failure closes existing streams.

The small embedded frontend selects published problems, supplies C++17/Python starters, accepts source, uses idempotency keys, shows actual event phases/verdicts and selected/original history, and reconnects after refresh. It stores only principal-scoped IDs/cursors locally, caps its timeline at 60 DOM rows and uses textContent for dynamic content. Source stays out of browser localStorage and public events. See [local web operations](local-web.md) and [API contracts](api.md).

## Verification and fault cases

| Cases | Actual evidence |
| --- | --- |
| A–C | PostgreSQL/Redis A100/B1 stress across two independent test-agent processes; B turn 2, global peak 2, principal peak 1; 20 additional claims refused while full |
| D–F | Concurrent same-job claims yield one owner; expired A cannot bypass B; generation-two recovery rejects stale results; terminal duplicate adds no run; retained outbox/uncertain XADD regressions |
| G | Genuine WA/CE/TLE complete and leave zero valid scheduled leases before the next matrix case |
| H–J | 20 HTTP creations across two API servers yield one submission/outbox intent; shared replay-rate 429, pending 429 and SSE quota cannot be bypassed by the second server |
| K–L | Cookie sessions ignore forged identity headers; foreign GET/history/SSE return 404 before cursor parsing; lists contain no foreign source/IDs; another owner's list cursor returns 400; logout revokes across servers |
| M | Real fair-agent C++ AC/WA/CE and Python AC/RE/TLE matrix emits actual phase and final-result SSE; private diagnostics absent |
| N | HTTP disconnect releases SSE lease; Last-Event-ID replays a missed event then snapshot; live/reconnect paths use bounded authoritative state |
| O | A allocates sequence 2 and delays commit; B blocks, reader stays at 1; committed replay then contains contiguous 2,3; rollback consumes no sequence |
| P | Stalled writer reaches its five-second deadline; more than 256 lagged events reset/close the stream; shared connection quotas and bounded DOM/page sizes |
| Q | Actual fair-agent rejudge preserves original AC, publishes requested/running/completed/selected events and selects new WA; original two-run history remains |
| R | Existing real Redis outage/backoff/ACK-loss and DB heartbeat-outage tests rerun; authority uncertainty cancels execution and stale results fail closed |
| S | All prior sandbox/judge/agent/orphan and six Session 9 object/cache/rejudge fault functions rerun, including private objects, corruption, cache single-flight, process death and generation fencing |
| T | Existing default-auth and controller opt-in checks preserved; default API restoration and live unauthorized checks are part of delivery |

Local verification contains **60 unit test functions**, **43 PostgreSQL/Redis behavioral functions** (three child helpers excluded), the prior **30 sandbox scenarios**, **11 local judging scenarios**, **16 baseline agent scenarios**, **10 orphan cases**, **six Session 9 fault functions**, and one new Session 10 real-runtime function with six language/verdict subcases plus rejudge. Formatting, vet, normal/race Go tests, tagged PostgreSQL/Redis race tests and executable builds are required. Windows native runtime suites are not race-instrumented; hosted Linux CI applies `-race` to tagged runtime suites. CI now explicitly includes the Session 10 matrix.

The dedicated new real-runtime run passed in 46.96 seconds. Its observed journal-timestamp-to-client intervals were about 64–504 ms, including commit and local transport/poll overhead. They are small end-to-end observations, not a capacity or isolated database benchmark. A missed-event HTTP replay plus snapshot is measured separately by the DB/API suite. No CPU/peak-memory values are fabricated.

An initial supplemental rejudge returned TLE while an image build ran concurrently. That failed attempt is not counted as passing. Runtime verification was separated from image builds, and the new rejudge fixture received its own five-second wall budget; the dedicated rerun selected actual WA. The inherited cache-crash test also assumed that cache-lease expiry implied execution-lease expiry. It now explicitly waits for both independent leases before attempting takeover; the ownership refusal that exposed this assumption was safe behavior, not grounds to weaken fencing.

The real browser automation provider was unavailable and the native fallback stopped because it could not verify Brave's current URL. No visual-browser pass is claimed. Live HTML/CSS/JS asset delivery, cookie authentication, API, SSE and automatic judging are checked through HTTP; manual visual/responsive inspection remains a limitation. No browser safety barrier was bypassed.

## Operational limits and handoff

Migration files 0001–0010 remain byte-for-byte unchanged. New migrations are forward-only, with exact checksums retained by `.gitattributes`. Ordinary API/worker/relay images have no Docker socket, S3 secrets or trusted administrative/controller binaries. Private credentials, runtime manifests, bundles and `learning-notes/10-session-ten-learning-notes.md` remain ignored.

Existing Docker/shared-kernel trust, physical orphan reconciliation, aggregate artifact-volume quota gaps, archived MinIO, privileged operator trust and unavailable CPU/peak-memory measurements remain. New event/auth metadata has no automatic retention pruning; replay windows bound clients, not total database storage. No production identity/TLS/reverse proxy validation, fairness throughput guarantee or event broker is claimed. Policy scope changes require trusted agent restart. Pending POST keys survive ambiguous retry within the current page but are not persisted across a page reload. Session 11 can add queue/admission/phase latency and low-cardinality metrics using existing run/job identities without recording private source/tests.

Final live identifiers, cleanup, pushed commit and observed hosted CI outcome are recorded at delivery; only observed results are reported.

## Final local delivery evidence

The final complete Go/DB verification passed, including all 43 behavioral functions under `-race`; missed-event replay plus authoritative snapshot was observed in 21.84 ms in that run. The corrected six-function Session 9 fault suite passed. Its ten-request C++ cache experiment observed one compiler/build and nine hits in 33.98 seconds; the object-backed rejudge retained original AC and selected WA with a cache hit and zero additional compiler invocations.

The final ordinary application image built with TLS verification enabled. Live HTTP served the embedded HTML/CSS/JS with security headers and authenticated two local accounts. Independent native host agents admitted different principals concurrently: `aman-s10` submission `b13d67aa-6d5b-4d76-9883-0721320e1ee0`, run `24a9ae5c-e79c-4a4b-b80b-760f259834d9`, and `peer-s10` submission `9a688ca5-18b2-4a5e-9bc9-465ceeb813c5`, run `fd05b2de-f17b-43c0-b3f6-61ec45f24a0b`. Both reached genuine Python AC through private S3-backed suites. The owner streams replayed accepted/claimed/preparing/running/completed through cursor 11 and reconnected to an authoritative snapshot. A forged identity header on the other account still returned 404.

All 12 staged SQL files were checked against exact disk and applied database hashes; migrations 0001–0010 also matched the original commit bytes. Learning notes were confirmed ignored. Demo agents are stopped only after zero valid active leases, scheduler policy is disabled, the owned relay/MinIO are stopped, and default production-auth-disabled Compose services are restored at delivery. Persistent data and unknown older sandbox resources are preserved. Final push/hosted CI results accompany the delivered commit.
