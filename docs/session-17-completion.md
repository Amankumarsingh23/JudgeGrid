# Session 17 completion report

Session 17 adds a private repository research controller. See [architecture](agent-architecture.md), [permissions and operator commands](agent-permissions.md), [run schema](agent-run-schema.md) and [ADR 0017](adr/0017-read-only-repository-research.md).

Migration 0017 adds isolated research runs, logical decisions and observations, physical attempts and expiring action-bound approvals. The read-only CLI uses the existing authorized READY repository chain for search, verified chunk and symbol reads, bounded graph proposals and independently verified context packs.

## Verification

Focused policy tests, Go vet, command builds, Compose configuration and trusted controller image isolation passed locally. The native Windows combined unit invocation failed the existing ingestion symlink test for lack of Windows symlink privileges. The research package passed in that invocation. The local Linux container applied and checked migration 0017 on the existing retained development schema.

The tagged suite runs the lease, outbox and cache tasks in order against the 2,221-document index pinned to commit `56ea76e421eca19066362fdecedd4e9aae04521e`. Its test log records each decision, physical attempts, bounded source bytes and context tokens, elapsed time, citation coordinates and hash, final outcome and uncertainty. It also tests owner denial, exact grant and replay matching, expiration, denial and cancellation, competing claims and crash after a tool read but before the observation commits. Fixture planning exercises the real orchestration and real retrieval models.

The first retained Linux run, using fixture policy v1, observed the following results. Each task journaled a search, chunk read, symbol lookup, graph inspection, verified pack and finish. All three completed with VERIFIED_REFERENCES and task sufficiency unassessed. The shipped v2 fixture adds a targeted `authorizeWork` lookup to the lease task and charges graph-anchor verification bytes as inspected payload.

| Task | Physical reads | Accounted payload bytes | Pack WordPieces | Elapsed | Verified example |
| --- | ---: | ---: | ---: | ---: | --- |
| Lease renewal | 5 | 11,195 | 4,095 | 98.8 s | [ownership.go lines 39–62](https://github.com/Amankumarsingh23/JudgeGrid/blob/56ea76e421eca19066362fdecedd4e9aae04521e/internal/storage/ownership.go#L39-L62), bytes [1466, 2407), hash `5c22e50da7ccd8f84ad9c0f02d0362d85e07d74f0d60accd339456d0ba5f0d73` |
| Outbox publication | 5 | 10,241 | 3,814 | 99.0 s | [relay.go lines 126–165](https://github.com/Amankumarsingh23/JudgeGrid/blob/56ea76e421eca19066362fdecedd4e9aae04521e/internal/outbox/relay.go#L126-L165), bytes [3967, 5304), hash `8421a2e53ea9426b39b1205aca7d158b5602c9743135684b72082a1542ff914d` |
| Compilation-cache creation and reuse | 5 | 7,996 | 3,884 | 97.2 s | [execution cache lines 27–45](https://github.com/Amankumarsingh23/JudgeGrid/blob/56ea76e421eca19066362fdecedd4e9aae04521e/internal/execution/cache.go#L27-L45), pack span hash `476ad1816e659f8fd9eb5937e7ee0730916b02635d95aad587d318717068238e` |

The tagged Linux race run passed in 390.9 seconds. In the simulated process-loss case, two workers competed for one claim. Generation 1 completed a real read and lost its lease before recording the observation. Generation 2 re-authorized the read, committed one logical observation with two physical attempts, and finished. The stale generation could not record or finalize. Foreign-owner view and approval requests, wrong digests and reversed decisions were rejected. Replaying the same grant left the approved action at one physical attempt.

## Limits and handoff

The Session 16 labels remain provisional and model-assisted. Its pooled full-pack nDCG@5 of 0.4829 versus hybrid's 0.3358 had a paired interval including zero. Only 20 of 56 answerable questions had all required groups. Two of four provisional negatives received strong support. The controller reports task sufficiency as unassessed even when it can verify references. Native Windows agent regressions from Session 15 remain open.

Session 18 can consume verified references and the fenced journal. Its workspace, mutation and test authorities require additional design and controls.
