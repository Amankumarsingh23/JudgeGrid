# Approved workspace validation

The trusted host command `repo-validate` validates one exact Session 18 selected version. Its own development authorization requires `--allow-development-validation`, `REPO_VALIDATION_ENABLED=true`, `APP_ENV=development`, local authentication with a fixed `DEV_PRINCIPAL`, and an explicit `REPO_DATABASE_URL`. Patch approval conveys no validation permission. A trusted operator can request a validation directly under this independent capability. Session 19 exposes no public route, model approval path or generic command.

From the repository root, after applying migration 0019 and provisioning the pinned images:

1. Use `--action request_validation --workspace <id> --version <hash> --profile go-unit`.
2. Use `--action run --id <validation-digest>` to execute or replay the request.
3. Use `--action inspect_validation --id <digest>` or `--action read_validation_log --id <digest>` for verified private state and logs.
4. Use `--action cancel_validation --id <digest>` to revoke authority and reconcile its exact resources. A cleanup error leaves attempt cleanup state visible.

Each invocation requires the opt-in flag. On request and materialization the existing workspace reader verifies owner, the selected inventory identity, paths, modes, bounds and all emitted file hashes. The workspace controller supplies logical file identities. A fresh labeled volume receives only verified regular-file contents. Reserved topology entries, symlinks and gitlinks have no materialized bytes. A fixed trusted staging command uses exclusive file creation and independently rehashes each written file before the workload container is created. Go sees the volume read-only. There is no host repository bind mount, source credential or Docker socket in either sandbox.

A PASS describes one observation under one exact profile, version, policy and image. It does not establish full repository coverage, behavioral security or readiness to merge. Source and logs are untrusted data. The private report stores bounded, terminal-safe displayed output with a digest, plus a separate digest of the complete report. It collects no arbitrary workload filesystem artifacts. Telemetry observes coarse operations without source or identity labels.
