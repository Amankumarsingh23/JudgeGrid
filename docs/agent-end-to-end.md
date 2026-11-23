# Unified private coding task

Session 21 joins the existing research, workspace, validation and repair controllers. A task starts from an authenticated owner and an explicit READY retrieval index. Its scope pins the repository, source snapshot, analysis and commit. A task with no edit target can end after citation-verified research. An editable task requires a bounded existing Go target and explicit operator review.

The read-only Session 17 fixture dispatches real hybrid search and verified source reads. The task planner receives bounded citation metadata with explicit uncertainty. If the decision permits preparation, Session 18 constructs the private virtual workspace. A real Session 19 root-package Go validation establishes the initial outcome. Session 20 handles actionable failures, current-version diagnosis, typed proposal, separate approval, publication and same-profile revalidation. A baseline pass requires no patch. Other failed outcomes do not authorize a speculative edit.

A proposal pauses the task in WAITING_APPROVAL. The review includes the actual unified diff, preimage and postimage hashes, expected version, required profile and image and approval expiry. The authenticated operator supplies the exact digest and a separate approval opt-in. A first incorrect patch can lead to the next bounded proposal, which requires a fresh grant. The task report links the source scope, verified citations, child and result identities, selected version and uncertainty. A root-package PASS has that profile's coverage only.

Build `go build ./cmd/repo-task` at the repository root. Apply migration 0021 and use the existing trusted retrieval, source storage and pinned sandbox configuration. Each call requires `--allow-development-task`, `REPO_TASK_ENABLED=true`, development mode, local authentication with fixed `DEV_PRINCIPAL`, and explicit `REPO_DATABASE_URL`.

- Start with `--action start --provider fixture --index <ready-index> --goal <task>`. Include `--target value.go` for the fixture edit path. Omit the target for research only. An optional UUID via `--id` makes intake replayable.
- Resume with `--action resume --provider fixture --id <task>`. Use `fixture-two` consistently for the two-iteration demonstration.
- Use `inspect`, `research`, `workspace`, `inspect_patch`, `validation`, `repair` or `report` as the action to read private evidence.
- Use `--action grant --allow-development-approval --id <task> --digest <patch>` for the exact human grant. `deny` uses the same authentication and digest. Use `cancel` to revoke the task.

The normal runtime images contain no task controller or mutation route.
