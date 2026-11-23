# Session 18 completion report

Session 18 extends a completed Session 17 research run with an owner-scoped workspace, an immutable patch proposal, a separately authenticated exact approval and a fenced publication. Migration 0018 adds immutable private byte objects, inventories, logical receipts and physical attempts. The workspace has a virtual Git-relative namespace. There are no host worktrees, repository hooks or operating-system path writes in the controller.

See [workspace isolation](agent-workspaces.md), [operator commands and patch approval](agent-patch-approval.md), [recovery](agent-edit-recovery.md) and [ADR 0018](adr/0018-virtual-workspace-publication.md).

## Live verification

The Linux race suite used a disposable Git repository, PostgreSQL, and the existing private object-storage reader. It ran a pinned Git fixture through source, analysis, retrieval and a completed Session 17 journal. It also prepared a workspace from the existing retained JudgeGrid snapshot and a real completed research run. The suite passed in 11.5 seconds after a first development run exposed that ordinary directory metadata needed to be distinct from reserved leaves.

| Demo | Observed result |
| --- | --- |
| Single-file MODIFY | Fixture preparation 51 ms, proposal validation 11 ms. Before approval, application was denied. Exact grant and replay were accepted. The 47-byte `main.go` changed from `return 1` to `return 2` in 29 ms overall, with 7 ms staging and verification and 10 ms publication. |
| Two-file ADD and MODIFY | Injected failure after staging the first artifact. The selected inventory and file bytes remained unchanged. Generation 3 recovered generation 2's partial staging, applied both files once and committed one logical receipt across two physical attempts. A simulated lost publication response replayed that receipt. |
| Competing approvals | Both same-base proposals received separate grants. Exactly one selected a version. The losing proposal became a stale-version conflict. |
| Retained research to proposal | Prepared 264 inventory entries at `56ea76e421eca19066362fdecedd4e9aae04521e` in 1.74 s. Proposed a 64-byte documentation addition. Application without approval was denied and the base stayed selected. |

The successful fixture proposal digest was `f6fa92a8208e15db8baf6a95a6f39fe30094ceee19a44815401f79e3a36c8214`. Its preimage SHA-256 was `458cf65508504f3ea616c36cc572d0d08536178425505d179149585ab55c008a` and its postimage SHA-256 was `fbefbe4d1a9bb2b5e360e46f88cea05ef5a024febc7fb66f6c4739920c8668be`. The preview showed the complete before and after text. The original Git HEAD, index entries, status and original file bytes matched their captured values after the suite. The hostile README remained inert. The retained repository was read through its verified immutable snapshot.

## Security and regression evidence

- A through L exercise exact scope, immutable source, typed edits, denied/expired/cancelled and forged approval, digest/version mismatch, grant and apply replay, preimage checks and concurrent selection.
- M through R exercise path traversal, absolute names, a real tracked symlink, Windows aliases, protected paths, unsafe Unicode controls, malformed schemas, oversize limits and binary/executable writes. The virtual representation has no host junction or hardlink materializer. Windows path behavior is enforced by portable-name policy and topology checks.
- S through U exercise partial-staging failure, response loss after commit and expired generation rejection. V through Y exercise unsupported deletion and commands, inert repository text and foreign inspection rejection.
- Z and AA cover source-read failure, absence of partial publication, and the lack of a host cleanup surface. A real interruption is simulated at controlled execution points. PostgreSQL failover and filesystem-junction races were not separately measured.
- AB and AC cover migration checksums, the repository CI workflow and controller image isolation. The development CLI requires both explicit opt-ins, local authentication and an explicit database. Existing native Windows symlink privilege and Session 15 agent issues remain tracked separately from Linux verification.

The patch fixture is deterministic. The retained flow preserves Session 17's distinction between verified references and task sufficiency. The edits establish exact bytes, modes, tree identity and publication integrity. Behavioral validation belongs to Session 19, which can consume an explicitly selected version through its own authorized materializer and sandbox.
