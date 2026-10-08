# MyCode Roadmap

MyCode is an early Alpha. The P0 implementation baseline is complete; its exact
validation evidence and remaining environment checks are recorded in
[P0_DEVELOPMENT.md](P0_DEVELOPMENT.md). [SECURITY.md](SECURITY.md) defines the
current trust boundary.

## P0: Safety and reliability baseline

P0 covers buildable terminal interaction, OS-backed credential references,
credential-origin binding, explicit workspace and MCP trust, reviewed file
edits, fail-closed Docker command execution, bounded provider streaming,
retry/cancellation, multi-tool continuity, and resumable session snapshots.

P0 deliberately keeps command and MCP workspaces temporary and offline. Live
provider checks and native credential-store checks are opt-in because they need
user-owned credentials or platform services.

## P1: Long-running agent runtime

P1 is developed in dependency order. Each milestone requires an RFC and threat
model, versioned data contracts, migration and rollback behavior, deterministic
fixtures, race/fault/adversarial tests, platform integration, and updated docs.

### P1.1 Event journal, checkpoints, and model capabilities

The first journal slice is implemented (see
[RFC](docs/rfc/p1-1-session-journal.md)): durable execution events, an atomic
replayable checkpoint, conservative interrupted-turn handling, schema-1 to
schema-2 migration, and a cross-process session lock. P1.1 is **not complete**;
model capability metadata, richer interrupted-turn recovery, and journal
compaction remain.

- append-only typed events for turns, approvals, model calls, tool calls, and jobs
- atomic checkpoints and crash replay without duplicate tool execution
- session schema migration and cross-process locking
- provider capability metadata for context windows, tools, reasoning, usage, and caching

### P1.2 Context budgeting, compaction, and layered memory

- request-time token budgeting with provider-reported usage
- deterministic compaction that preserves complete tool-call/result groups
- protection for opaque Responses state and Anthropic signatures
- trusted global, project, nested, and included memory with cycle and size limits

### P1.3 Durable tasks, PTY, and background jobs

- structured task state that survives compaction and restart
- bounded PTY/job start, attach, read, write, poll, cancel, and process-tree cleanup
- explicit reviewed export of selected sandbox artifacts to the host workspace

### P1.4 Controlled network, provider conformance, and remote MCP

- deny-by-default egress broker with exact origin grants and audit events
- SSRF, redirect, DNS-rebinding, credential-scope, traffic, and response limits
- one conformance suite for Anthropic, OpenAI, gateways, and compatible providers
- MCP capability negotiation, pagination, notifications, cancellation, and Streamable HTTP

### P1.5 Sub-agents, evaluation, and release engineering

- bounded sub-agents with narrower tools, paths, network, concurrency, and token budgets
- sanitized local traces and reproducible repository-task evaluation
- fuzz/fault suites, SBOM, checksums, reproducible archives, and signed releases

## P2: Optional product breadth

- notebook editing
- built-in web search/fetch beyond MCP and controlled network profiles
- richer IDE integrations
- advanced prompt caching and cost optimization
- native sandbox backends where Docker is unavailable

## Contribution gate

Prefer focused changes with explicit validation. A feature that changes files,
processes, credentials, network access, persisted state, or model-visible trust
must include its failure behavior and security tests in the same change.
