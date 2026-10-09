# MythosCode

MythosCode is a Go-first terminal coding assistant with Anthropic Messages, OpenAI
Chat Completions and Responses, reviewed file edits, streaming output, sessions,
Skills and stdio MCP. It is an early Alpha. The P0 security baseline uses OS
credential storage, explicit trust and isolated command execution.

Read [SECURITY.md](SECURITY.md) for the execution boundaries and
[P0_DEVELOPMENT.md](P0_DEVELOPMENT.md) for the implementation and validation status.

## What Changed

- CLI command: `mythoscode`
- Go module: `github.com/BigSmartie/Coding-Agent`
- Config directory: `~/.mythos-code`
- Config override: `MYTHOS_CODE_HOME`
- App env vars: `MYTHOS_CODE_PROVIDER`, `MYTHOS_CODE_MODEL`,
  `MYTHOS_CODE_MODEL_MODE`, `MYTHOS_CODE_MAX_OUTPUT_TOKENS`
- Local skills path: `.mythos-code/skills` and `~/.mythos-code/skills`
- Go entrypoint: `cmd/mythoscode`
- Local launcher: `bin/mythoscode`

Provider-specific variables are still intentionally compatible with existing
API conventions:

- Anthropic-compatible: `ANTHROPIC_MODEL`, `ANTHROPIC_BASE_URL`,
  `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`
- OpenAI-compatible: `OPENAI_MODEL`, `OPENAI_BASE_URL`,
  `OPENAI_AUTH_TOKEN`, `OPENAI_API_KEY`

## Quick Start

Run in mock mode:

```powershell
$env:MYTHOS_CODE_MODEL_MODE = "mock"
go run ./cmd/mythoscode
```

Configure a provider and enter its key in the hidden terminal prompt:

```powershell
go run ./cmd/mythoscode auth login
go run ./cmd/mythoscode
```

The login command accepts Anthropic/OpenAI, your model name and an HTTPS base URL.
The key is stored in Windows Credential Manager, macOS Keychain, or Linux Secret
Service (`secret-tool`). Settings contain a reference and the permitted origin.
There is no plaintext fallback and keys cannot be passed as command arguments.

Process environment BYOK remains available for trusted automation. Custom
endpoints also require an explicit credential origin binding. For example, with
the key already supplied by your secret manager:

```powershell
$env:MYTHOS_CODE_PROVIDER = "openai"
$env:OPENAI_MODEL = "your-model"
$env:OPENAI_BASE_URL = "https://your-provider.example/v1"
$env:OPENAI_CREDENTIAL_ORIGIN = "https://your-provider.example"
go run ./cmd/mythoscode
```

## Install Locally

Interactive install:

```powershell
go run ./cmd/mythoscode install-local
```

Non-interactive install:

```powershell
go run ./cmd/mythoscode install-local `
  --provider openai `
  --model your-model
go run ./cmd/mythoscode auth login
```

The installer builds `mythoscode-go` under `~/.mythos-code/bin` and writes a `mythoscode`
launcher under `~/.local/bin`.

## Upgrade From Project-Local Credentials

User state must be outside the workspace. Launchers now respect `MYTHOS_CODE_HOME`
when explicitly set and otherwise use `~/.mythos-code`. Remove an old override that
points at the repository before running the new version.

Use `mythoscode auth migrate` to migrate legacy **user** settings into the OS store.
Use `mythoscode auth migrate --from-project` to explicitly migrate a legacy
`.mythos-code/settings.json` in this repository. Migration first saves the key in the
OS store and persists user settings; only then does it clear project secrets.
It never prints the key. An unavailable credential store leaves the source intact.
Migration does not rotate credentials at the provider.

Project settings may contain `model`, `maxOutputTokens` and MCP definitions.
Project `env`, `provider`, credential references and endpoint overrides are rejected.
Set `contextWindowTokens` in private user settings (or
`MYTHOS_CODE_CONTEXT_WINDOW_TOKENS` in the process environment) when the chosen
model's context limit is known; the app does not guess gateway limits. Project
settings cannot override this value. `/status` shows the configured limit or
`unknown`.
When configured, the agent checks a request budget before each model call and
compacts only at complete conversation/tool boundaries. If the current request
cannot fit without losing part of it, the call stops with a budget error.
Reviewed project memory may use `MEMORY.md` or `.mythos-code/MEMORY.md`; global
memory may use `~/.mythos-code/MEMORY.md`. These files and the existing
`AGENTS.md`/`CLAUDE.md` rules can include nested Markdown files with
`@include relative/path.md`. Project includes are part of the workspace trust
fingerprint, so changes require review again.

An interrupted session can be reopened with `mythoscode --resume <id|latest>` if
no tool or approval decision was reached. If a tool may have changed external
state, inspect the workspace first, then use
`mythoscode --resume <id|latest> --recover-interrupted` to abandon the unfinished
turn at its last checkpoint. The tool is never executed again automatically.

`/tasks` shows structured task state saved outside the compacted transcript.
The agent can update tasks with `task_update` and list them with `task_list`.
`/jobs` shows background sandbox jobs. The agent can use `job_start`,
`job_attach`/`job_read`, `job_poll`, `job_write`, `job_cancel`, and `job_list`.
Background jobs have bounded runtime, concurrency, input and output. They are
canceled when the session exits. PTY mode uses a Unix host PTY, or a guest PTY
inside Docker or WSL2/Bubblewrap on Windows. A successfully completed job can export one
UTF-8 text file (up to 1 MiB) through `job_export`; the destination change
requires a reviewed diff and edit approval. See the
[P1.3 jobs RFC](docs/rfc/p1-3-durable-tasks-jobs.md).

## Commands and MCP in an Isolated Sandbox

Install/start Docker with Linux containers and explicitly build a trusted image:

```powershell
docker build -t mythoscode-sandbox -f sandbox/Dockerfile sandbox
$env:MYTHOS_CODE_SANDBOX_IMAGE = "mythoscode-sandbox"
go run ./cmd/mythoscode
```

Runtime verifies that the image already exists; it never pulls an image and
never falls back to running a model command on the host. Images should contain
the offline tools, package caches and MCP server binaries you need, plus
`/bin/sh` and `cp` for the fixed snapshot bootstrap. Windows Docker PTY jobs
also require `python3` in the trusted image; the supplied Dockerfile includes it.

On Windows, a Docker-free backend is available when the default WSL2 Linux
distribution already has Bubblewrap and `prlimit` installed. Interactive
PTY jobs also require `/usr/bin/python3` in that distribution:

```powershell
wsl --exec sh -lc 'command -v bwrap && command -v prlimit && command -v python3'
$env:MYTHOS_CODE_SANDBOX_BACKEND = "wsl"
go run ./cmd/mythoscode
```

The WSL backend runs Linux executables inside Bubblewrap, not Windows `.exe`
files. It fails closed if WSL2 or its required binaries are unavailable. It
uses the same filtered snapshot, reviewed edits, offline network namespace,
bounded scratch, and artifact review as Docker. A PTY job allocates its terminal
inside the isolated WSL guest; command and arguments are passed as separate
arguments to a fixed helper, without shell interpolation. The default remains
Docker; `MYTHOS_CODE_SANDBOX_IMAGE` is not needed for the WSL backend. See the
[P2.5 RFC](docs/rfc/p2-5-wsl-sandbox.md).

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
`~/.mythos-code/settings.json` (use your actual endpoint):

```json
{"webSearchEndpoint":"https://search.example/search"}
```

Each full request URL requires one-time interactive approval, followed by the
existing exact-origin network approval. Redirects, private addresses, binary
responses, and unattended web requests fail closed. Search is unavailable until
the endpoint is configured; the endpoint must support SearXNG's JSON format.
Page text and search snippets are untrusted. See the
[P2.2 RFC](docs/rfc/p2-2-web-tools.md).

The optional [VS Code extension](editors/vscode/README.md) opens MythosCode in a
trusted workspace terminal and prepares reviewed active-file context without
submitting it. See the [P2.3 RFC](docs/rfc/p2-3-vscode-integration.md).

For Anthropic Messages, user settings may opt into provider prompt caching and
show a cost estimate using rates you supply for your model or gateway:

```json
{
  "promptCaching": true,
  "pricing": {
    "inputPerMillion": 10,
    "outputPerMillion": 20,
    "cacheReadPerMillion": 1,
    "cacheWritePerMillion": 12
  }
}
```

These numbers are examples, not current prices. MythosCode does not ship a price
table. Plain sessions show estimated cost per turn; the TUI shows an estimated
total for the current run. Existing `maxOutputTokens` and
`contextWindowTokens` settings can cap output and context growth. See the
[P2.4 RFC](docs/rfc/p2-4-cache-cost.md).

Review project instructions and each MCP configuration before enabling them:

```text
mythoscode trust workspace
mythoscode trust workspace --accept <displayed-fingerprint>
mythoscode trust mcp <server-name>
mythoscode trust mcp <server-name> --accept <displayed-fingerprint>
```

Open and review the listed instruction files or MCP command/arguments before
accepting. Fingerprints are bound to the canonical workspace and configuration.
Changed rules, server configuration or sandbox image selection require review
again. Append `--revoke` to either capability command to revoke its trust.
MCP tool/resource/prompt calls also pass through operation approvals.

## Commands

Management commands:

```powershell
go run ./cmd/mythoscode help
go run ./cmd/mythoscode install-local
go run ./cmd/mythoscode auth login
go run ./cmd/mythoscode trust workspace
go run ./cmd/mythoscode sessions list
go run ./cmd/mythoscode mcp list
go run ./cmd/mythoscode mcp add fs -- npx server
go run ./cmd/mythoscode mcp add-url remote https://example.com/mcp
go run ./cmd/mythoscode trust mcp remote

# Evaluate a pinned repository task in a disposable snapshot (uses your configured model)
go run ./cmd/mythoscode-eval -manifest task.json
go run ./cmd/mythoscode skills list
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

- `cmd/mythoscode`: CLI entrypoint and startup flow
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
- `internal/sandbox`: isolated, disposable Docker or Windows WSL2 execution
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
$env:MYTHOS_CODE_SANDBOX_IMAGE = "mythoscode-sandbox"
$env:MYTHOS_CODE_SANDBOX_INTEGRATION = "1"
go test -count=1 ./internal/sandbox
```

The TypeScript code and older tutorial documents are retained as historical
learning references. The P0 security guarantees apply to the Go runtime only.
