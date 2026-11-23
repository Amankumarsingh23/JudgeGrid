# Exact patch review and approval

Build the trusted command with `go build ./cmd/repo-workspace`. It requires `--allow-development-editing`, `REPO_WORKSPACE_ENABLED=true`, `APP_ENV=development`, `DEV_AUTH_ENABLED=true`, the authenticated operator's fixed `DEV_PRINCIPAL` and explicit `REPO_DATABASE_URL`. Preparation also takes the existing private repository storage configuration. There is no service endpoint. The same trusted development identity decides requests out of band.

With the opt-in flag present, the operations are:

1. `--action prepare_workspace --run <completed-research-id>` returns the workspace ID and selected base version.
2. `--action inspect_workspace_version --workspace <id>` returns the selected inventory. `--action read --workspace <id> --path <virtual-path>` returns verified file metadata and base64-encoded bytes.
3. Send one bounded typed JSON patch on standard input with `--action propose_patch --workspace <id>`. Use `base_version` and a `changes` array. Each operation supplies `kind` (ADD or MODIFY), `path`, `mode` (100644), `before_size`, `after_size`, `after_sha256`, and complete intended UTF-8 `text`. MODIFY requires `before_sha256`; ADD has zero before size and an omitted or empty before hash.
4. `--action inspect_patch --workspace <id> --digest <sha256>` returns typed hashes and the exact full-file unified preview. Add `--diff` for the plain preview.
5. Request review with `--action request_patch_approval --workspace <id> --digest <sha256>`. The authenticated owner explicitly calls `grant` or `deny` with the same workspace and digest. `cancel` can withdraw an unpublished request or grant.
6. `--action apply_approved_patch --workspace <id> --digest <sha256>` applies only the exact permitted proposal.

The canonical identity hashes the sorted typed operations, complete before/after declarations, expected version, immutable workspace and run scope, action kind and approval-policy version. The owner is bound by the immutable workspace and the separately recorded requester and decision-maker. The shared Session 17 transition policy handles exact replay, denial and out-of-band authority. Child edit approvals have a separate typed identity so a read approval never becomes an edit approval. The five-minute expiry is calculated by the database and checked again immediately before selection.

The policy permits eight changed files, 64 KiB per changed preimage or postimage, and 128 KiB combined changed bytes. The full-file preview uses one hunk per change and rejects output above 384 KiB. Duplicate keys, unknown fields, unsafe controls, nonportable aliases, binary edits, deletion and mode changes fail closed. Deterministic fixture patches are trusted test inputs; their application is not evidence of model coding ability.
