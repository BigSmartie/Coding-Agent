# P1.4 checkpoint — 2026-10-08

Status: **work in progress**. This commit is a recovery checkpoint, not a P1.4 completion claim. P1.5 has not started.

## Completed before this checkpoint

- P1.1 event journal and recovery: PR #4 (`codex/p1-event-journal`).
- P1.2 context budget and reviewed memory: PR #5 (`codex/p1-context-budget`).
- P1.3 durable tasks, bounded jobs, and artifact export: PR #6 (`codex/p1-durable-jobs`), CI green and ready for review. Native Windows PTY remains unsupported; ordinary Windows jobs work.
- P1.4 work on this branch: exact HTTPS origin permissions, guarded egress transport with DNS/IP and traffic bounds, typed network audit events, remote MCP Streamable HTTP client, remote MCP configuration/CLI, and session wiring. The full `go test ./...` suite passes at this checkpoint.

## Resume P1.4

1. Exercise remote MCP end to end through the **production guarded transport**. Existing remote tests inject an HTTPS client and do not prove that SDK headers, streaming, session IDs, and cleanup work under the real guard.
2. Review transport and journal method coverage, including MCP GET/POST/DELETE fallback and shutdown. Keep network audit records valid for every allowed method. Enforce numeric port parsing and review request-body accounting and response truncation.
3. Give remote MCP calls a truthful per-operation approval prompt. The shared `authorizeCall` path still uses command-oriented wording and an isolated-sandbox claim that does not describe a remote server.
4. Complete MCP capability, pagination, notification-refresh, cancellation, and schema-limit behavior for both remote and stdio clients. Verify failures and reconnects do not bypass permissions or lose audit data.
5. Add conformance and adversarial tests, document the threat model and configuration, run full race/vet/CI validation, then prepare the P1.4 PR for review.

After P1.4 is complete, proceed to P1.5 (bounded subagents, evaluation, and release engineering) according to the project roadmap. Keep the P1.1–P1.3 PR stack intact until review/merge; do not redo those milestones.
