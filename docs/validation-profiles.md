# Validation profiles

Both profiles use `golang@sha256:672c944cfd07599b5f1bb179869270cdc94c6254e41db4d431259693cb286b82` (Go 1.26.10 in the observed Linux image). Provision it on the trusted controller with `docker pull` before using validation. The existing trusted sandbox setup provisions the pinned Python staging image and seccomp profile. Execution never pulls images. The request identity includes the resolved immutable Go image ID and a policy digest incorporating staging image, fixed commands, offline settings and execution contract.

| Profile | Input | Fixed command | Outcome |
| --- | --- | --- | --- |
| `go-unit` | Root Go module and root test source | `/usr/local/go/bin/go test -json -count=1 .` | Requires actual zero exit and named test completion evidence for PASS. Structured Go build failure and test failure are classified separately. |
| `go-format-check` | Verified, case-sensitive `.go` paths | `/usr/local/go/bin/gofmt -l` plus bounded absolute in-container paths | Zero exit and empty listing is PASS. Listed files mean formatting failure. |

Go runs offline with proxy, checksum lookup, toolchain updates, workspaces, VCS resolution and CGO disabled. Module edits are disabled. There is no warmed dependency cache. Missing dependencies are build failures. The root-package profile does not imply `./...`, and formatting checks are not tests. Compiling and running the root package are one measured Go phase.

The runner uses the pinned seccomp rules with network none, non-root UID/GID 10001, no capabilities and no-new-privileges, private namespaces, a read-only root and read-only source mount. Its limits are one CPU, 768 MiB memory without swap, 64 PIDs, 65 seconds of wall time, 64 KiB combined captured output and 384 MiB of disposable scratch. The scratch mount is executable so generated Go test binaries can run and has nosuid and nodev. This is a separate workspace runtime policy. The judge sandbox retains its existing noexec scratch and resource limits. Stage execution is bounded independently at 20 seconds, 256 MiB of memory and 16 KiB of combined output. No resource peak is claimed where it was not sampled.
