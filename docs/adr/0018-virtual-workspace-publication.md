# ADR 0018 - Virtual workspaces with approval-bound publication

Use private database-backed content objects and complete immutable version inventories. This keeps platform-specific filesystem semantics outside agent editing. Only the source ingest subsystem reads registered Git repositories, using its existing verified snapshot boundary.

A workspace is a child of a completed Session 17 run. Reuse the authenticated operator and approval transition policy while pinning edit approvals separately to an exact immutable proposal and expected workspace version. An operator-visible unified preview is derived from the verified preimages and complete proposed postimages.

Separate durable blob staging from the fenced selection transaction. Keep selection, the publication receipt and the child journal event atomic. Make failed attempts recoverable and avoid automatic object deletion. The design trades host materialization and unrestricted file types for a narrow, inspectable and portable edit surface.
