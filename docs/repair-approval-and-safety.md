# Repair approval and safety

Build `go build ./cmd/repo-repair` from the repository root. The private CLI requires `--allow-development-repair`, `REPO_REPAIR_ENABLED=true`, development mode, local authentication with fixed `DEV_PRINCIPAL`, and explicit `REPO_DATABASE_URL`. Apply migration 0020 first. Resuming validation also requires the existing Session 19 pinned runtime setup. Production images contain no repair controller or route.

- `--action start --baseline <validation-digest> --target <path.go> --requirement <goal>` creates a run. Add `--fixture-planner` only to exercise the deterministic built-in examples.
- Use `--action diagnose --id <run>` for authorized evidence, or `--action inspect` for the private durable journal.
- Run `--action propose --fixture-planner --id <run>` for a fixture run. A manual run takes a Session 18 typed JSON patch on standard input with `--action submit`.
- `--action inspect_patch --id <run>` returns the precise unified preview, expected bytes and hashes, approval expiry, expected profile and image.
- The authenticated operator calls `--action grant` or `--action deny` with `--allow-development-approval --id <run> --digest <exact-patch>`. A replay of the same grant does not create another logical decision. Earlier grants cannot authorize a later proposal.
- `--action resume --id <run>` applies and validates only an authorized iteration. `--action cancel` stops further repair authority. A prior selected version remains durable.

The repair accepts exactly one MODIFY against its nominated existing 100644 Go file. Its reported preimage must match the current verified bytes. Test files, common test/support directories, authorization, secret, migration, dependency, validation, workspace, sandbox and policy paths are protected. The underlying Session 18 policy still rejects special paths, unsafe controls and malformed patches. The immutable baseline and pinned preexisting tests remain available for review. These conservative path protections cannot establish that a proposed source change has no unintended semantic effects.

There is no model-side approval, generic command, arbitrary filesystem write, Git write, or validation-profile substitution. Separate trusted operators can still use the established workspace controller according to its own authority. Site administrators and the controller host remain within the trust boundary.
