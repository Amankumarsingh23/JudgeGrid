# Coding model provider boundary

The available pinned MiniLM and TinyBERT artifacts implement embeddings and reranking. The workspace did not provide a trusted instruction/code model, pinned tokenizer/template and supported local serving runtime. Genuine coding-model inference was therefore NOT RUN. The CLI rejects an unavailable provider and never substitutes a fixture implicitly.

`ModelClient` is a versioned bounded adapter. Its input labels the authenticated task, current state, selected workspace version, allowed decisions, remaining budget, verified citation and source data, and untrusted sanitized logs. A provider supplies typed JSON and input/output token usage. The controller admits only a fixed decision enum, rejects unknown or duplicate fields and validates patch JSON through the existing strict workspace decoder. Fixture decisions report zero model tokens because they perform no model inference.

The parent planner supports prepare, finish, and abstain in planning and typed patch or abstain during repair. Fixed Session 17 typed reads remain in that controller. Approval, validation dispatch and workspace publication remain trusted transitions. The proposer has no shell, network, Docker, approval, test-edit or configuration-edit capability. Previous proposals and approvals stay in the durable journals. Current workspace reads come from the verified repair path after every edition.

A future real adapter needs an explicitly authorized provider, exact model/runtime revision, tokenizer/template, trustworthy resource accounting, privacy contract and separate evaluation. No remote transport, provider token handling, model download or repository-upload path is installed by this session.
