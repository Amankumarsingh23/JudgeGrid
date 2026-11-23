# Session 19 completion report

Migration 0019 and the opt-in `repo-validate` controller add pinned offline Go validation of selected immutable workspace versions. The original repository has no mount or write path in this workflow. The trusted controller materializes verified regular files into a private attempt volume, inspects the sandbox before execution and keeps the selected source read-only. See [validation](agent-validation.md), [profiles](validation-profiles.md), [recovery](validation-recovery.md), and [ADR 0019](adr/0019-fixed-offline-workspace-validation.md).

## Live evidence

The local end-to-end suite used a real disposable Git repository, PostgreSQL, private local immutable source storage and a disposable Linux Docker sandbox. The test seeds a completed research parent for the small fixture. Source ingestion, Session 18 patch approval, workspace publication, materialization and test execution use their real paths. The retained demo uses the actual archived Session 18 workspace.

| Demonstration | Result |
| --- | --- |
| V1 root-package test | TEST_FAILURE, exit 1, real assertion `got 1, want 2`. The fixture also printed a misleading success phrase, which did not grant a PASS. The run took 18.0 s. Log digest `3577ac8e6066d48be6a80983972ffb189f186eebd73eb22159d684868d24c36e`. |
| V2 after exact approved patch | PASS, exit 0, 19.8 s. V1's result remained separate. Log digest `f44473ec4b377a6dbc264b813180db4cfdcd32378f2c40a22db71a300ead6cde`. |
| Recovery | Injected loss after completed execution and before report publication. Recreated an actual running, labeled orphan as a controlled fixture. Generation 2 removed that exact resource and reran; generation 1 could not write. The final digest replayed with two physical attempts and one report. |
| V3 invalid Go | BUILD_FAILURE, compiler exit 1, 18.5 s. The original checkout HEAD, index, status and file bytes remained equal to the initial capture after all three versions. |
| Retained JudgeGrid | Approved a private 62-byte documentation addition, then ran only the bounded `go-format-check` profile at the retained source commit `56ea76e421eca19066362fdecedd4e9aae04521e`. PASS, 971,521 staged bytes, 3.33 s overall. This establishes formatting for the selected version. |

The Go runtime also exercised a read-only source write denial, absence of a Docker socket and host secret, non-root identity, dropped capabilities, no-new-privileges, seccomp and observed cgroup limits, and a forbidden connection to a TEST-NET address. A deliberately corrupted staged hash blocked the workload container. The output test hit its exact 65,536-byte cap even though the process managed to exit zero. A limit outcome takes precedence over a zero exit.

## Security matrix and limits

- A through D and AE have live fail/fix/pass, compiler failure, explicit approval and original-checkout evidence. E through G use the runtime wall and output checks plus classifier coverage for daemon-reported OOM. The workspace-specific peak memory and an actual workspace OOM event were not measured.
- H through L cover source/platform error separation in controller logic and runtime observations of network, host-path, credential and process isolation. PostgreSQL failover is outside the local demonstration.
- M through S use owner, expected version, typed profile, fixed image/argv, inherited portable inventory validation and a real corrupted stage. The Session 18 virtual source policy rejects path aliases and never emits symlink/gitlink contents.
- T through Z cover failed staging, a real labeled orphan, generation fencing, report replay and pending cancellation. A lost worker is injected at an explicit failure point; the test does not kill the Windows controller process.
- AA through AD use immutable digests, bounded safe log display, real overflow behavior and distinct version/profile identities. The test does not request unsafe image substitutions. The pinned image and execution contract are part of the identity.
- AF and AG use the established CI regression workflow, default-disabled CLI and controller image isolation. Older migrations are unchanged. Native Windows baseline issues remain separate from successful Linux checks.

The local test compilation/run phase is reported as a combined Go invocation. No arbitrary workload file artifacts are collected. Session 20 can consume the scoped result and bounded log, then propose a separately approved edit.
