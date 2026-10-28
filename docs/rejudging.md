# Authorized development rejudging

Build `cmd/judge-admin` on the trusted host. The command requires explicit development administration, development authentication and a configured principal. Requests target one submission owned by that principal and a published newer version of the same problem. The original submission must already be COMPLETED and use C++17/Python. There is no public administration endpoint.

```sh
./judge-admin --allow-development-administration --action rejudge --submission UUID --version VERSION_ID --key correction-1
./judge-admin --allow-development-administration --action history --submission UUID
```

The new logical job and schema-three outbox intent commit together. Start the existing opt-in outbox relay and trusted judge agent on an isolated stream. They resolve thin references against PostgreSQL and evaluate the target immutable version. PostgreSQL recovery also finds published jobs whose delivery reference was lost. Repeating a key returns the same logical job; changing its target conflicts. Each job has its own bounded lease/retry generations, and stale attempts cannot insert evidence or finalize.

The original source bytes, bound version, initial workflow and historical runs never reset. Results commit to the existing evaluation history. `selected_evaluations` may advance only to the latest requested job after committed completion. Request ordinals serialize on the submission; a later-finishing older request cannot overwrite a newer selection. If the newest job fails, the previous selected committed result remains visible. Until a rejudge succeeds, the original result remains displayed.

Owner-scoped GET/list responses preserve the original problem version fields, display the selected verdict, and add current evaluation run/version and original verdict metadata. History is available through the trusted CLI as sanitized summaries, capped at 100 entries. Hidden test content, object keys and evaluator output remain private. Initial schema-two deliveries continue to replay the initial result rather than selecting a rejudge attempt.

Sandbox provenance still names the actual run/submission/generation. Registry authority resolves that run to its logical job, so generation one of two different jobs cannot be confused. Result fencing and orphan termination remain separate. See [agent operations](execution-agent.md), [storage](object-storage.md) and [ADR 0009](adr/0009-immutable-objects-cache-and-rejudge-jobs.md).
