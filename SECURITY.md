# P0 security boundary

This Alpha protects a local user's provider credential from automatic project
configuration, file tools and child process environments. It does not provide a
safe way to distribute a shared provider key in a client binary. A distributed
service needs a backend broker, authenticated users and short-lived tokens.

## Credentials and configuration

- Provider credentials belong to the current user's OS credential store.
  Settings retain a random reference, credential type and HTTPS origin.
- Environment BYOK is accepted for the provider's official origin. A custom
  origin requires an explicit `OPENAI_CREDENTIAL_ORIGIN` or
  `ANTHROPIC_CREDENTIAL_ORIGIN`. HTTP, URL userinfo, query strings and fragments
  are rejected. Provider requests never follow redirects.
- Project settings cannot choose a provider, inject environment variables,
  redirect credentials or supply credential references. User state inside the
  workspace is refused. Never commit or distribute a live key.
- Secret entry is hidden. `--api-key` and `--auth-token` are rejected. Explicit
  migration preserves the old file until OS storage and new settings succeed.

## Trust and approvals

- Project instructions/skills are off until their workspace fingerprint is
  accepted. MCP servers are off until their configuration fingerprint is
  accepted; no process starts merely because `.mcp.json` exists.
- MCP server trust is independent of tool-call approval. Tool/resource/prompt
  invocations require an active permission manager.
- `allow once` applies to one request; turn grants are reset in the agent loop.
  Enter defaults to denial. Persistent command decisions include structured
  arguments and the working directory instead of a broad executable allowlist.
- Trust, permission files and configuration directories are protected from
  model tools. A model-visible instruction is never an authorization to grant
  trust or bypass the executor.

## Files and processes

- File operations use canonical containment and Go `os.Root` handles, with
  protection for secret/state paths and links. Reviewed edits compare the file
  again before an atomic replacement. Large files, search and diffs are bounded.
- Commands and stdio MCP run in the default preinstalled Linux Docker image,
  pinned to its locally inspected image ID for each launch, or in the explicitly
  selected Windows WSL2/Bubblewrap backend. Direct host execution is disabled.
- Both backends deny network access and host credential environments, run as a
  non-root user with a read-only runtime, and copy a filtered disposable
  snapshot from `/input` into a bounded `/workspace`. Docker also disables
  disk logging; WSL2 uses process resource limits rather than Docker cgroups.
- A command cannot persist its edits. The model must use the reviewed file tools
  for durable changes. Package downloads and Git metadata access are unavailable
  inside either sandbox; guarded HTTPS and remote MCP use a separate reviewed
  network path.
- The Docker daemon and image, or the selected WSL2 distribution and its
  Bubblewrap binaries, plus the OS and current user's profile are trusted
  components. Keep them patched. The sandbox is not a defense against an
  attacker who already controls these components.

## Transport and output

- Model requests have bounded connection, header and total deadlines, request
  and response sizes, limited transient retries, and `Retry-After` handling.
  Stream data is not silently replayed after partial output.
- Responses output items, including opaque reasoning state, are retained across
  tool turns. Provider errors and incomplete responses stop with an explicit
  error instead of silently succeeding.
- Encrypted continuation bytes and provider signatures remain intact in saved
  state. If signed plaintext reasoning requires secret redaction, its redacted
  history is saved with an explicit non-resumable marker instead of replaying
  a signature against modified text.
- ANSI/OSC, carriage returns and invisible control characters in external text
  are quoted before terminal rendering. Diff styling is added after escaping.
- Runtime credentials and common secret patterns are redacted from tool output,
  terminal output, history and saved sessions. Redaction is a best-effort last
  boundary; it cannot discover every arbitrary secret in source content.

## Persistence and remaining work

State writes use temporary files, fsync and replacement. Sessions have random
IDs and a schema version, preserve input/tool history on cancellation, and check
the workspace on resume. Current system instructions are rebuilt on resume.
Unix state uses 0700 directories and 0600 files. Windows state inherits the user
profile's ACL; keep a custom `MYTHOS_CODE_HOME` private to that user.

Session/history content remains plaintext after redaction. Event journaling,
cross-process locking, checkpoints, context compaction, bounded background jobs,
network grants, and a Windows WSL2 sandbox backend are implemented. Windows
WSL2 PTY jobs allocate the terminal inside the isolated Linux guest and require
Python 3 there. Windows Docker PTY jobs remain unsupported. Actual release
signing requires a tag and GitHub's attestation service.

Automated checks use temporary data, a fake credential store and local HTTP/SSE
fixtures. Live-provider, actual Docker, and WSL2 integration tests are explicit
opt-in checks. Native credential tests require `MYTHOS_CODE_CREDENTIAL_INTEGRATION=1` and
create/read/delete only their own random synthetic entry. Passing these checks
does not establish production security.
