# MyCode

MyCode is a Go-first terminal coding assistant with Anthropic Messages, OpenAI
Chat Completions and Responses, reviewed file edits, streaming output, sessions,
Skills and stdio MCP. It is an early Alpha. The P0 security baseline uses OS
credential storage, explicit trust and isolated command execution.

Read [SECURITY.md](SECURITY.md) for the execution boundaries and
[P0_DEVELOPMENT.md](P0_DEVELOPMENT.md) for the implementation and validation status.

## What Changed

- CLI command: `mycode`
- Go module: `github.com/BigSmartie/Coding-Agent`
- Config directory: `~/.my-code`
- Config override: `MY_CODE_HOME`
- App env vars: `MY_CODE_PROVIDER`, `MY_CODE_MODEL`,
  `MY_CODE_MODEL_MODE`, `MY_CODE_MAX_OUTPUT_TOKENS`
- Local skills path: `.my-code/skills` and `~/.my-code/skills`
- Go entrypoint: `cmd/mycode`
- Local launcher: `bin/mycode`

Provider-specific variables are still intentionally compatible with existing
API conventions:

- Anthropic-compatible: `ANTHROPIC_MODEL`, `ANTHROPIC_BASE_URL`,
  `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`
- OpenAI-compatible: `OPENAI_MODEL`, `OPENAI_BASE_URL`,
  `OPENAI_AUTH_TOKEN`, `OPENAI_API_KEY`

## Quick Start

Run in mock mode:

```powershell
$env:MY_CODE_MODEL_MODE = "mock"
go run ./cmd/mycode
```

Configure a provider and enter its key in the hidden terminal prompt:

```powershell
go run ./cmd/mycode auth login
go run ./cmd/mycode
```

The login command accepts Anthropic/OpenAI, your model name and an HTTPS base URL.
The key is stored in Windows Credential Manager, macOS Keychain, or Linux Secret
Service (`secret-tool`). Settings contain a reference and the permitted origin.
There is no plaintext fallback and keys cannot be passed as command arguments.

Process environment BYOK remains available for trusted automation. Custom
endpoints also require an explicit credential origin binding. For example, with
the key already supplied by your secret manager:

```powershell
$env:MY_CODE_PROVIDER = "openai"
$env:OPENAI_MODEL = "your-model"
$env:OPENAI_BASE_URL = "https://your-provider.example/v1"
$env:OPENAI_CREDENTIAL_ORIGIN = "https://your-provider.example"
go run ./cmd/mycode
```

## Install Locally

Interactive install:

```powershell
go run ./cmd/mycode install-local
```

Non-interactive install:

```powershell
go run ./cmd/mycode install-local `
  --provider openai `
  --model your-model
go run ./cmd/mycode auth login
```

The installer builds `mycode-go` under `~/.my-code/bin` and writes a `mycode`
launcher under `~/.local/bin`.

## Upgrade From Project-Local Credentials

User state must be outside the workspace. Launchers now respect `MY_CODE_HOME`
when explicitly set and otherwise use `~/.my-code`. Remove an old override that
points at the repository before running the new version.

Use `mycode auth migrate` to migrate legacy **user** settings into the OS store.
Use `mycode auth migrate --from-project` to explicitly migrate a legacy
`.my-code/settings.json` in this repository. Migration first saves the key in the
OS store and persists user settings; only then does it clear project secrets.
It never prints the key. An unavailable credential store leaves the source intact.
Migration does not rotate credentials at the provider.

Project settings may contain `model`, `maxOutputTokens` and MCP definitions.
Project `env`, `provider`, credential references and endpoint overrides are rejected.
Set `contextWindowTokens` in private user settings (or
`MY_CODE_CONTEXT_WINDOW_TOKENS` in the process environment) when the chosen
model's context limit is known; the app does not guess gateway limits. Project
settings cannot override this value. `/status` shows the configured limit or
`unknown`.
When configured, the agent checks a request budget before each model call and
compacts only at complete conversation/tool boundaries. If the current request
cannot fit without losing part of it, the call stops with a budget error.
Reviewed project memory may use `MEMORY.md` or `.my-code/MEMORY.md`; global
memory may use `~/.my-code/MEMORY.md`. These files and the existing
`AGENTS.md`/`CLAUDE.md` rules can include nested Markdown files with
`@include relative/path.md`. Project includes are part of the workspace trust
fingerprint, so changes require review again.

An interrupted session can be reopened with `mycode --resume <id|latest>` if
no tool or approval decision was reached. If a tool may have changed external
state, inspect the workspace first, then use
`mycode --resume <id|latest> --recover-interrupted` to abandon the unfinished
turn at its last checkpoint. The tool is never executed again automatically.

`/tasks` shows structured task state saved outside the compacted transcript.
The agent can update tasks with `task_update` and list them with `task_list`.
`/jobs` shows background sandbox jobs. The agent can use `job_start`,
`job_attach`/`job_read`, `job_poll`, `job_write`, `job_cancel`, and `job_list`.
Background jobs have bounded runtime, concurrency, input and output. They are
canceled when the session exits. PTY mode requires a Unix host (or WSL on
Windows); native Windows supports non-PTY jobs. A successfully completed job can export one
UTF-8 text file (up to 1 MiB) through `job_export`; the destination change
requires a reviewed diff and edit approval. See the
[P1.3 jobs RFC](docs/rfc/p1-3-durable-tasks-jobs.md).

## Commands and MCP in an Isolated Sandbox

Install/start Docker with Linux containers and explicitly build a trusted image:

```powershell
docker build -t mycode-sandbox -f sandbox/Dockerfile sandbox
$env:MY_CODE_SANDBOX_IMAGE = "mycode-sandbox"
go run ./cmd/mycode
```

Runtime verifies that the image already exists; it never pulls an image and
never falls back to running a model command on the host. Images should contain
the offline tools, package caches and MCP server binaries you need, plus
`/bin/sh` and `cp` for the fixed snapshot bootstrap.

Commands run with no network, a non-root user, a read-only container root,
limited resources and a filtered workspace snapshot mounted read-only at `/input`.
The snapshot is copied into a 512 MiB memory-backed `/workspace` for foreground
commands. Background jobs use a session-private temporary scratch mount so an
artifact can be reviewed after the command exits; a per-file limit and periodic
aggregate size scan cancel excessive writes. `/tmp` is limited to 256 MiB,
container memory to 1 GiB, and process count to 128. Container log
files are disabled, so output cannot fill Docker's host log storage. **Changes made by commands
or MCP are temporary.** Use approved `write_file`, `edit_file`, `modify_file` and
`patch_file` calls to persist source edits. Git metadata, app state, common secret
files and links are excluded; Git history/remote operations are unavailable in
this P0 snapshot mode. Installing dependencies over the network is unavailable.

Jupyter notebooks can be inspected with `read_notebook` and changed one source
cell at a time with `edit_notebook_cell`. An edit requires the cell's SHA-256
from the read result and a reviewed file diff; changed code cells lose their
saved outputs and execution count. See the [P2.1 RFC](docs/rfc/p2-1-notebook-editing.md).

`web_fetch` reads public HTTPS pages as bounded text. To enable `web_search`,
set a trusted SearXNG JSON endpoint in your user settings at
`~/.my-code/settings.json` (use your actual endpoint):

```json
{"webSearchEndpoint":"https://search.example/search"}
```

Each full request URL requires one-time interactive approval, followed by the
existing exact-origin network approval. Redirects, private addresses, binary
responses, and unattended web requests fail closed. Search is unavailable until
the endpoint is configured; the endpoint must support SearXNG's JSON format.
Page text and search snippets are untrusted. See the
[P2.2 RFC](docs/rfc/p2-2-web-tools.md).

Review project instructions and each MCP configuration before enabling them:

```text
mycode trust workspace
mycode trust workspace --accept <displayed-fingerprint>
mycode trust mcp <server-name>
mycode trust mcp <server-name> --accept <displayed-fingerprint>
```

Open and review the listed instruction files or MCP command/arguments before
accepting. Fingerprints are bound to the canonical workspace and configuration.
Changed rules, server configuration or sandbox image selection require review
again. Append `--revoke` to either capability command to revoke its trust.
MCP tool/resource/prompt calls also pass through operation approvals.

## Commands

Management commands:

```powershell
go run ./cmd/mycode help
go run ./cmd/mycode install-local
go run ./cmd/mycode auth login
go run ./cmd/mycode trust workspace
go run ./cmd/mycode sessions list
go run ./cmd/mycode mcp list
go run ./cmd/mycode mcp add fs -- npx server
go run ./cmd/mycode mcp add-url remote https://example.com/mcp
go run ./cmd/mycode trust mcp remote

# Evaluate a pinned repository task in a disposable snapshot (uses your configured model)
go run ./cmd/mycode-eval -manifest task.json
go run ./cmd/mycode skills list
```

Sessions use a private append-only event journal and atomic checkpoints.
After a crash, `--resume <id>` can replay a completed checkpoint without
rerunning tools. If a turn stopped before its checkpoint, resume is refused
because a tool may already have changed the workspace; review it and start a
new session. See the [P1.1 journal RFC](docs/rfc/p1-1-session-journal.md).

Interactive slash commands:

```text
/help
/tools
/status
/model
/model <name>
/config-paths
/skills
/mcp
/permissions
/exit
```

Local tool shortcuts:

```text
/ls [path]
/grep <pattern>::[path]
/read <path>
/write <path>::<content>
/modify <path>::<content>
/edit <path>::<search>::<replace>
/patch <path>::<search1>::<replace1>::...
/cmd [cwd::]<command> [args...]
```

## Architecture

The Go runtime is organized around small internal packages:

- `cmd/mycode`: CLI entrypoint and startup flow
- `internal/agent`: multi-step model/tool loop
- `internal/model`: Anthropic, OpenAI, mock, and error model adapters
- `internal/tools`: built-in tool registry, JSON schema validation, execution
- `internal/permissions`: path, command, and edit approval policy
- `internal/filereview`: diff review before file writes
- `internal/session`: line mode, TUI mode, sessions, history
- `internal/tui`: terminal rendering, transcript, input, diff highlighting
- `internal/config`: separate user/project settings and credential origin binding
- `internal/credentials`: OS credential store backends
- `internal/trust`: workspace and MCP configuration fingerprints
- `internal/sandbox`: isolated, disposable Docker execution
- `internal/safety`: secret redaction, terminal escaping and atomic state writes
- `internal/skills`: local `SKILL.md` discovery and installation
- `internal/mcp`: stdio MCP client and tool wrapping
- `internal/install`: local binary and launcher installer
- `internal/manage`: management subcommands
- `internal/brand`: product naming constants

## Verify

```powershell
go test ./...
go vet ./...
go test -race ./...
```

Tests isolate their own app state and do not use real model credentials. CI
checks Windows, Linux and macOS, plus actual container isolation on Linux.
For a local opt-in sandbox check against an already-installed image:

```powershell
$env:MY_CODE_SANDBOX_IMAGE = "mycode-sandbox"
$env:MY_CODE_SANDBOX_INTEGRATION = "1"
go test -count=1 ./internal/sandbox
```

The TypeScript code and older tutorial documents are retained as historical
learning references. The P0 security guarantees apply to the Go runtime only.
