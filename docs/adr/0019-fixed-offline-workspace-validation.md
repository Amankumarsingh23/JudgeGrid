# ADR 0019 - Fixed offline workspace validation

Keep validation in a separately authorized host controller. Materialize the existing immutable selected workspace into a fresh private Docker volume using only regular files and verified hashes. Mount source read-only and use fixed pinned Go profiles with isolation inspected before execution. Do not grant Docker authority to the API, queue worker or planner.

Separate logical request results from physical attempts. Bind the request to the exact selected inventory, image and execution policy. Use database-clock generation fencing and exact labeled resource cleanup. Store bounded private reports and hash the sanitized log and complete report. The design keeps original Git state outside execution and leaves repair and integration to subsequent sessions.
