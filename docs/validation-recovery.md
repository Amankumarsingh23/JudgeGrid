# Validation recovery

Migration 0019 binds a logical request to owner, repository, workspace, inventory-derived version, profile, policy and pinned image. Two active validations and twelve admissions per owner per hour are allowed. A workspace version must be selected to request or dispatch validation. Older completed reports remain readable to their owner.

PENDING has no physical attempt. A claim reserves one opaque sandbox identity, gives the worker a database-clock lease and monotonic generation, and enters MATERIALIZING. RUNNING covers bounded staging and execution phases. COMPLETED holds an immutable, digest-verified result. Operational failure or expiration after retries leaves FAILED or EXPIRED. Cancellation invalidates the current claim immediately and remains distinct from physical cleanup. A completed historical test failure still has state COMPLETED.

A retry can claim an expired generation at most twice. It verifies the local runtime, removes only its previous labeled validation resources and proves cleanup before dispatch. The original worker cannot update the result. A partial stage has no runnable Go container. Crash recovery uses a new disposable stage; staged files, caches and process outputs do not survive into an authoritative result. The local daemon is a trusted dependency, and all attempts for one database scope must use that same daemon. Cross-host orphan reconciliation is outside this implementation.

A report completed after sandbox cleanup is written in one fenced database transaction. Results with cleanup failures report infrastructure failure and retain cleanup-required attempt state. A lost response replays the committed report and digest. A known container crash before publication creates no authoritative result. Cleanup re-inspects exact labeled resources and uses immutable Docker container IDs for removal. It never sweeps unrelated resources.

Session 20 can consume the verified version identity, structured outcome and bounded log as evidence for a new approved proposal. The validation controller performs no repair or retry of repository changes.
