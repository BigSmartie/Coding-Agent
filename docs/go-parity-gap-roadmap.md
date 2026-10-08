# Go MiniCode Parity Gap Roadmap

> Historical parity checklist. Current P0 evidence and future priorities are in
> [../P0_DEVELOPMENT.md](../P0_DEVELOPMENT.md) and [../ROADMAP.md](../ROADMAP.md).

This document tracks the remaining gaps between the TypeScript MiniCode implementation and the Go implementation.

## Phase 1: CLI Runtime Parity

- [x] Interactive slash commands: `/status`, `/model`, `/model <name>`, `/skills`, `/mcp`, `/permissions`.
- [x] History persistence to `~/.mini-code/history.json`.
- [x] MCP management flags: `--env`, `--protocol`, `--project`.
- [x] Better reviewed-file unified diff output.
- [x] Runtime metadata wiring from config, skills, and MCP summaries into session output.
- [x] Permission summary parity for extra allowed dirs, dangerous command allowlist, and trusted edit targets.
- [x] Missing runtime config reports request failure instead of implicitly entering mock mode.

## Phase 2: MCP Runtime Parity

- [x] Launch stdio MCP servers.
- [x] Support JSON-RPC request/response IDs and pending request timeouts.
- [x] Support both `Content-Length` and newline-json framing.
- [x] Wrap remote MCP tools as local tools named `mcp__server__tool`.
- [x] Add generic MCP resources and prompts tools.
- [x] Dispose MCP child processes on exit.
- [x] Harden concurrent request handling and long-running MCP failure recovery.
- [ ] Add integration smoke tests against a real public MCP server command.

## Phase 3: Terminal Experience Parity

- [x] Basic full-screen alternate-screen terminal UI.
- [x] Basic scrollable transcript with user, assistant, progress, and tool entries.
- [x] Slash menu rendering, selection, and tab completion.
- [x] Raw-mode input editing for text, return, escape, arrows, backspace, and page scroll.
- [x] History navigation in raw TUI mode.
- [x] Approval panel for path, command, and edit decisions.
- [x] Basic approval detail expansion/collapse and scroll controls.
- [x] Syntax-highlighted diff review and word-level changed-span emphasis.
- [x] Tool result auto-collapse and edit aggregation.
- [x] Tool result collapse phases and bounded transcript previews for large outputs.
- [x] Terminal-size detection instead of fixed fallback dimensions.
- [x] Agent progress/tool/assistant events rendered as structured transcript entries.
- [x] Markdown-ish assistant/progress/tool transcript rendering.
- [x] Card-style Unicode panel borders, colored transcript headers, highlighted slash selection, and wide-character display width handling.

## Phase 4: Hardening and Installer

- [x] Standard library JSON schema subset validation for tool inputs, including common string, number, array, enum, object, and additional-property constraints.
- [x] More exact Anthropic edge-case handling and diagnostics.
- [x] Agent continuation behavior for progress/final markers, plain status after tool results, empty responses, and clarifying questions.
- [x] Stop before tool execution when a tool-call response contains a clarifying question.
- [x] Provider abstraction beyond Anthropic-compatible endpoints.
- [x] Local installer equivalent to `npm run install-local`.
- [x] Session persistence/resume once the runtime is stable.
- [x] Tool panic recovery returns explicit errors.
- [x] Dangerous git overwrite commands are classified for approval.
- [x] Non-interactive installer settings flags.
- [x] Interactive installer prompts for provider, model, base URL, and secret.
