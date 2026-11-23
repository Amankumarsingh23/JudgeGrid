# Session 20 completion report

Migration 0020 and the private `repo-repair` controller compose the real Session 18 patch and Session 19 validation boundaries. A repair pins an owner, selected workspace version, exact baseline report, profile, image, target and proposer policy. It records immutable iteration and event evidence. See [repair loop](agent-repair-loop.md), [approvals](repair-approval-and-safety.md), [state and recovery](repair-state-and-recovery.md), and [ADR 0020](adr/0020-bounded-reviewed-repair.md).

## Observed end-to-end results

The Linux Docker tests ran locally from the Windows trusted host with PostgreSQL and a disposable real Git repository. The shared fixture seeds its research parent. Source ingestion, private immutable object storage, exact operator grants, workspace selection, Go validation and repair receipts use the real implementations.

| Scenario | Result and evidence | Baseline-through-result wall time |
| --- | --- | ---: |
| One iteration | Real TEST_FAILURE, exit 1 and assertion `got 1, want 2`. Without approval the version stayed selected. The exact approved patch produced PASS under the same Go profile. A lost response after validation replayed one authoritative run and one physical child attempt. | 37.7 s |
| Two iterations | Approved patch A produced a different version but retained TEST_FAILURE. A's digest was refused as authorization for B. Patch B received its own grant and passed. A simulated loss after A's publication recovered the committed workspace receipt. | 57.2 s |
| Budget | Two genuinely failing approved fixes ended BUDGET_EXHAUSTED. A third proposal was rejected. | 58.6 s |
| Abstention | Invalid Go produced a real BUILD_FAILURE. The fixture declined to invent a fix and returned STOPPED_UNRESOLVED with no patch. | 21.6 s including fixture setup |
| Retained review | At source commit `56ea76e421eca19066362fdecedd4e9aae04521e`, a separate trusted setup patch created a synthetic failing root package inside the private workspace. The validator staged 971,687 bytes and returned TEST_FAILURE. The repair controller produced a review-only proposal, which stayed WAITING_APPROVAL. | 19.2 s including setup and review |

The one-iteration proof used baseline report `622991f2991a5fd6fd0cbdbb2327ffe1e21ef4175c10d8f1c2f98f75f0fd6f8a`, patch `188e4543ed60fbf9214c34f90642ec4a5a7a2055b48e91301c347c46f58b26e0` and final validation `e692bf982a839cf566b22128698699f6f05f93961fc1575961e91b4606af72f1`. The SHA-256 of the 47-byte Go preimage was `458cf65508504f3ea616c36cc572d0d08536178425505d179149585ab55c008a` and the postimage was `fbefbe4d1a9bb2b5e360e46f88cea05ef5a024febc7fb66f6c4739920c8668be`. The proposer changed only the return value. The original Git tree, index, status and file bytes were verified unchanged.

The first full local repair and retained review invocation passed in 200.4 seconds. Typical diagnosis read 47 source bytes and about 1.4 KiB of sanitized log in 28?36 ms. Combined diagnosis and proposal preflight took 57?85 ms. Scripted repeated grant interactions took 38?60 ms and are not human review latency. Subsequent sandbox validations took roughly 18?19 seconds, each with one physical child attempt in the measured fixtures. The parent journal separately records logical requests and grant/apply/validate timestamps. No token usage, peak memory or model accuracy was inferred.

## Controls and regression evidence

- A through F exercise live test and compiler failure, refusal to repair a PASS, exact report identity and current-source verification. The case with an infrastructure outcome is a proposer/policy refusal test. A live database failover was not exercised.
- G through O cover deterministic typed proposals, pending denial of application, wrong and old digests, expired or denied grants, owner scoping, protected tests and policy paths and inert malicious output. There is no command capability in the proposer contract.
- P through W cover exact Session 18 publication, unmodified original checkout, exact same-profile revalidation, first-fix failure, separate approval for the second fix and the two-iteration ceiling. The narrow fixture cannot establish general correctness. The controller rejects previously attempted postimages and earlier known failed preimages.
- X through AB cover duplicate logical grants, lost patch and validation receipts, generation-fenced child dispatch and cleanup after withdrawal. Controlled faults simulate interruption at precise boundaries; they do not terminate the host process.
- AC through AF cover bounded private evidence, failure-class policy, immutable provenance and optional telemetry. Proposer/reader dependency faults cannot create a grant. No independent live telemetry collector outage was measured specifically for the repair parent.
- AG through AI use existing source and controller regressions and the hosted workflow. The opt-in command is absent from the normal service image. Native Windows ingestion symlink and prior timing regressions remain open.

The retained demonstration validates synthetic fixture code on actual retained content. It does not establish a fix to JudgeGrid's production code. No genuine coding-model inference was used. Session 21 can compose the existing research and repair authorities without converting verified evidence into automatic permission.
