# ADR 0004: Trusted local controller and restricted Docker execution

Status: Accepted — Session 4

## Boundary

An opt-in local fixture CLI invokes execution.Executor through worker.ProgramProcessor. That processor has no database, stream ACK or verdict-writing authority. Existing Redis handlers never invoke it. The controller is not included in API/worker runtime images and exposes no network endpoint.

The controller uses the developer's local Docker CLI authority. It can create only generated sandbox names, private named volumes and fixed commands through its implementation. This code boundary is narrow, but the developer account/daemon itself remains powerful and trusted. We do not present unrestricted daemon authority as a reduced-privilege daemon service. No Docker socket is mounted into API or Redis worker containers. A future hosted controller should run on dedicated execution hosts with a separately reviewed, authenticated/allowlisted runtime API, rootless/user-namespace mappings or a stronger runtime as appropriate.

## Contract

Request contains a supported language, bounded source/stdin, compile limits and run limits. Executor returns separate preparation/compilation/runtime results with image identity, policy version, captured output, observed exit code, controller elapsed time and Docker OOM evidence. Program compilation errors/nonzero exits/timeouts/output breaches are results. Engine/image/policy/startup/cleanup failures are typed InfrastructureError values. CPU time and peak memory remain null because this backend does not reliably measure them. No judge verdict is inferred.

Images are built/prefetched in a trusted setup phase, pinned by immutable local sha256 identity and used with --pull never. C++17 compilation uses fixed compiler flags in a separate container. Python uses a bounded, nonexecuting preparation phase, then isolated interpreter execution. A unique Docker volume carries controlled main.cpp/program or main.py filenames. No source is interpolated into host or shell commands, no archive is extracted, no host bind directory is exposed, and the runtime mounts the workspace read-only.

## Defense in depth

Compilation, preparation and runtime all use non-root UID/GID 10001, cap-drop ALL, no-new-privileges, the verified vendored Moby seccomp allowlist, private PID/IPC/cgroup views, network none, a read-only rootfs, bounded writable /tmp tmpfs, one CPU's allocation, memory with no extra swap, 16 tasks, no core dumps, bounded file size and file descriptors. Docker logging is disabled; combined stdout/stderr/diagnostics are captured incrementally up to their phase budget. The controller verifies retained engine settings and rejects unapproved environment variables before starting a phase. Runtime permissions and syscall denials still require functional tests; configuration inspection alone is not enforcement evidence.

Host-side timers and cancellation actively kill the whole sandbox container, not just the Docker client or a single workload PID. An independent coreutils timeout inside the container limits computation if the controller disappears abruptly. Controller-owned slot acquisition spans preparation, execution and cleanup. Deferred cleanup has an independent context, removes generated containers and workspace volume, and reports failures rather than claiming cleanup succeeded when the daemon is unreachable. A hard controller/host crash can leave stopped container/volume metadata requiring operator reconciliation; it must not be mistaken for fully durable lifecycle management.

## Limits and tradeoffs

Default compile budget is 15s/256 MiB, runtime is 3s/64 MiB, and each phase captures at most 64 KiB of combined output. Source/stdin are each capped at 64 KiB. Limits are bounded in the contract; max local slots is four and the demo uses two. Wall time includes Docker attach/start overhead, not pure program CPU time. OOMKilled is observed daemon evidence; exit 137 alone is not called memory exhaustion. General syscall-denial attribution is not available, so a nonzero exit is not automatically labeled a policy violation.

The GCC image is deliberately reused for C++ runtime compatibility in this first backend. This adds tools/attack surface compared with a future minimal ABI-compatible runner image. Named-volume storage is private but not an aggregate disk-quota implementation; fixed compiler paths, per-file limits, bounded source and bounded tmpfs reduce that exposure without proving resistance to compiler/runtime exploits.

Ordinary containers share the Linux kernel. Passing resource/isolation tests does not prove safety against determined hostile code. Docker Desktop/WSL2 is a local development environment, not a reviewed public execution service. Session 5 can consume typed program outcomes for test-case evaluation; automatic real job consumption, outbox, fencing, leases and production identity remain later work.
