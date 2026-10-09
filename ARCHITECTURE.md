# MythosCode Architecture

MythosCode is a Go-first terminal coding assistant. Its core loop is intentionally
small:

```text
user input -> model -> tool calls -> tool results -> model -> final answer
```

The goal is not to become a large IDE platform. The goal is to keep a compact,
testable runtime that can inspect a workspace, execute controlled tools, review
file changes, and keep the conversation moving inside a terminal.

## Design Principles

1. Keep the agent loop understandable and easy to test.
2. Centralize tool registration, validation, and execution.
3. Make path, command, and edit permissions explicit.
4. Review file changes before writing them.
5. Keep the terminal UI useful without depending on a heavy frontend stack.
6. Keep app identity centralized in `internal/brand`.

## Runtime Packages

- `cmd/mythoscode`: Parses startup arguments, loads runtime config, wires skills,
  MCP tools, permissions, model adapters, and session state.
- `internal/agent`: Drives the model/tool/model loop, including progress
  continuation, clarification stops, empty-response recovery, and tool errors.
- `internal/model`: Adapts Anthropic-compatible, OpenAI-compatible, mock, and
  error model behavior to one internal interface.
- `internal/tools`: Defines tools, validates tool input with a practical JSON
  schema subset, and executes built-ins.
- `internal/permissions`: Enforces path, command, and edit decisions with
  persistent allow/deny state.
- `internal/filereview`: Builds unified diffs and routes write operations
  through edit approval.
- `internal/session`: Handles line-mode interaction, TUI interaction, session
  records, and command history.
- `internal/tui`: Renders panels, transcript entries, slash menus, input, and
  highlighted diffs.
- `internal/config`: Loads `~/.mythos-code/settings.json`, `~/.mythos-code/mcp.json`,
  project `.mcp.json`, Claude-compatible fallbacks, and process environment.
- `internal/credentials`: Stores keys in native OS credential services; settings
  hold only random references bound to an HTTPS origin.
- `internal/trust`: Requires reviewed fingerprints for project rules and MCP.
- `internal/sandbox`: Runs commands/MCP only in a no-network disposable Docker
  snapshot, with no host execution or automatic file writeback.
- `internal/safety`: Escapes terminal controls, redacts secrets and atomically
  replaces private state files.
- `internal/skills`: Discovers and manages local `SKILL.md` workflows under
  `.mythos-code/skills`, `~/.mythos-code/skills`, and Claude-compatible skill folders.
- `internal/mcp`: Starts stdio MCP servers, handles JSON-RPC framing, wraps
  remote tools, and exposes resource/prompt helpers.
- `internal/install`: Builds `mythoscode-go` and writes the local `mythoscode` launcher.
- `internal/manage`: Implements management commands such as `install-local`,
  `sessions list`, `mcp`, and `skills`.
- `internal/brand`: Owns product constants such as `MythosCode`, `mythoscode`,
  `.mythos-code`, and `MYTHOS_CODE`.

## Configuration Model

MythosCode uses its own config namespace:

```text
~/.mythos-code/settings.json
~/.mythos-code/mcp.json
~/.mythos-code/permissions.json
~/.mythos-code/history.json
~/.mythos-code/sessions/
```

`MYTHOS_CODE_HOME` can override the app state directory. This is useful for tests,
CI, and portable development, but it must be outside the current workspace.

Project settings can only select a model/output limit and define MCP servers.
Project environment/provider/credential overrides are rejected. MCP definitions
replace previous definitions as a whole, preventing credentials from being
silently inherited across a changed command. Each resolved server configuration
needs its own trust grant and runs in the same isolated backend as commands.

Model adapters expose an optional streaming interface. The agent emits live text
deltas, then reconciles the final/progress event without duplicate transcripts.
Opaque Responses/Anthropic state is retained alongside semantic transcript
messages so multi-tool requests can replay provider-specific state correctly.
Turn lifecycle hooks clear temporary permission grants even after cancellation.

Provider-specific env vars intentionally remain compatible with common API
names such as `ANTHROPIC_AUTH_TOKEN` and `OPENAI_API_KEY`.

## Verification

The repository should stay green with:

```powershell
go test ./...
go vet ./...
go test -race ./...
```

Windows-specific behavior is covered by tests that avoid Unix-only executable
mode assumptions and isolate app state with `MYTHOS_CODE_HOME`.
