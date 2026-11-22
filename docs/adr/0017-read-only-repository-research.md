# ADR 0017 - Separate read-only repository research control plane

Status: accepted for trusted development tooling.

Use a separate PostgreSQL run journal and a software-enforced read-only capability registry over existing owner-scoped repository readers. Keep the shipped planner deterministic. The model boundary is a versioned strict decision schema; a future model adapter must establish its own private-data transport and versioned runtime identity.

Commit decisions before reading, reserve physical attempts under a fenced generation, and commit each result with its logical step. Require explicit authenticated action-bound approval, with no approval path capable of registering new tools. Keep the research binary out of service images and expose no API route.

The resulting citations demonstrate source integrity. Retrieval relevance claims remain provisional, and task sufficiency needs separate evaluation. Future phases own edit and execution permissions.
