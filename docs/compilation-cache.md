# Verified successful-compilation cache

Caching is opt-in on trusted host controllers: `COMPILATION_CACHE_ENABLED=true` plus explicit S3 configuration. Python is not compiled/cached. Failed C++ compilations never become READY. Ordinary service images have no cache controller authority or credentials.

The canonical schema-one key records exact source hash, compiler path and pinned image/toolchain, `-std=c++17 -O2 -pipe`, fixed source-date policy, amd64 ELF target/ABI, image-contained dependencies, sandbox policy/seccomp and compile budgets. Problem/test version is excluded because this offline compilation receives source only. Changing any key field misses. Arbitrary caller-supplied compiler flags are not supported.

One database BUILDING claim coordinates identical requests across processes. Owner, generation and DB-time expiry fence READY publication. Normal build lease is two minutes; compilation and object operations are bounded, and an expired builder cannot publish. Controller slots bound local waiters. Only trusted code exports and checks a regular size-bounded ELF, publishes immutable bytes and verifies read-back. Every READY read checks length, hash and format before preparing a new private workspace. Each case still starts a fresh restricted runtime. No shared cache volume is mounted in workloads.

Cache hit preparation evidence describes verified artifact transfer, not a new compiler execution. `Report.cache_hit`, cache counts and actual Docker compilation counters distinguish them. The completion report contains measured counts. Compilation resource-policy failures remain infrastructure failures.

```sh
# Explicit trusted metadata eviction after investigating a corrupt/unavailable entry:
./judge-admin --allow-development-administration --action cache-evict --key SHA256_CACHE_KEY
```

Eviction removes reuse eligibility and leaves object bytes intact. A later request claims a new generation and recompiles. It is safe if another evaluation already copied verified bytes. No automatic deletion of potentially referenced objects or automatic garbage collection is implemented. Storage operators can deny access or corrupt bytes, but a digest mismatch cannot be executed as a hit. The database, trusted compiler/controller and pinned images remain trusted. See [ADR 0009](adr/0009-immutable-objects-cache-and-rejudge-jobs.md).

Cold and cached C++ workspaces both contain the submission's own main.cpp (0400, fixed timestamp) and program (0500). Cache-hit preparation transfers a bounded exact source prefix and verified ELF into its fresh private volume, preserving source-readable program behavior. The ten-request integration fixture exercises that compatibility on cold and hit paths.
