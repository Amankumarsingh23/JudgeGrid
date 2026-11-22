# Repository research controller

The Session 17 controller runs a bounded research plan over a single immutable READY repository index. Unlike a one-shot context pack, a run can choose a next read, wait for an explicit approval, and recover from a process crash. PostgreSQL owns the authoritative journal. The controller reuses the existing retrieval, source, analysis and context-pack readers.

The shipped planner is a deterministic fixture. It searches, reads a verified chunk, verifies available symbol metadata, inspects bounded graph neighbors and independently verifies a context pack. Planner proposals enter a versioned JSON interface. The fixture ignores source instructions and uses metadata only. The distribution contains neither an LLM planner nor an external model provider. The embedding and cross-encoder stages still use the existing real pinned local models.

Final results carry the source commit and index scope, plus exact file ID, path, byte and line coordinates and SHA-256 for each published citation. Graph proposals are separately identified and remain structural hints. Search candidates are not citations. Pack diagnostics carry the retrieval heuristic's status. VERIFIED_REFERENCES means the pack verifier accepted bytes and provenance; the final status always reports that task sufficiency is unassessed. An absent or weak pack yields NO_EVIDENCE or INSUFFICIENT_EVIDENCE.

Session 16's provisional benchmark found all required evidence on 20 of 56 answerable questions and false strong support on two of four provisional negatives. The pooled full-pack nDCG@5 of 0.4829 versus hybrid's 0.3358 has a paired interval including zero. These limits apply to the same retrieval pipeline here.

Session 18 can reuse the durable journal and trusted approval pattern. Workspace preparation and any mutation capability need a separate action vocabulary, threat model, authority boundary and validation.
