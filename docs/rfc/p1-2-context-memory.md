# P1.2 RFC: context budget, deterministic compaction, and reviewed memory

Status: implemented for configured context windows and local CLI/TUI sessions.

## Request budget

`contextWindowTokens` is an explicit user setting. Unknown model/gateway limits
remain zero and disable automatic compaction. Before every model request, the
agent reserves configured output tokens (or a bounded default), ten percent of
the context window, and a fixed overhead. It estimates serialized transcript
and tool-schema tokens, then anchors growth to the previous provider-reported
input-token count in the same turn. The estimate is intentionally conservative
but cannot exactly predict every model or compatible gateway tokenizer. A
provider may still reject a request; that error is returned normally.

When the estimate exceeds the input budget, compaction chooses the earliest
complete boundary that makes the request fit. A boundary follows a final
assistant message or a fully matched tool-call/result batch. It never splits a
tool pair, an opaque Responses item from its mirrored projection, or an
Anthropic signed-thinking block from a retained group. Removed tool output is
not promoted into the summary. The summary preserves the entire current user
request if that request would otherwise be dropped. If no safe boundary fits,
the model is not called and the user receives a budget error. A compaction event
is synced to the session journal before the next model call.

Compaction changes the saved transcript at the next checkpoint. The original
complete transcript remains in the previous checkpoint until that save, and
ordinary session rollback follows the P1.1 journal contract. A P1.1 binary
does not recognize the new `context_compacted` event; restore a pre-P1.2
session backup or start a new session to roll back.

## Layered memory and trust

Global memory comes from `~/.my-code/MEMORY.md` and the existing
`~/.claude/CLAUDE.md` compatibility file. Project memory comes from reviewed
`MEMORY.md`, `.my-code/MEMORY.md`, `AGENTS.md`, and `CLAUDE.md`. A line of the
form `@include relative/path.md` expands a nested Markdown file relative to
its parent. Includes cannot use absolute paths, `..`, symlinks, special files,
or paths outside their source root. Global memory is limited to 128 files,
1 MiB total, and depth 16. Project memory is captured into the immutable
workspace trust snapshot, limited to 1024 files, 4 MiB total, 256 KiB per file,
and depth 32. Include cycles fail closed. The project fingerprint includes all
nested include bytes, so a change revokes trust; prompt building never rereads
project memory from the live workspace after approval.

## Threat model and verification

Repository text is untrusted until the workspace snapshot is approved. Tool
results are untrusted even after compaction and never become memory rules.
The byte-based budget estimate is not a provider token-count guarantee. The
checkpoint and secret-redaction limits from P1.1 still apply.

Deterministic tests cover complete tool groups, preserved opaque bytes,
oversized requests failing before provider calls, journal failure before
compaction, provider-usage anchoring, unchanged current requests, immutable
reviewed includes, changed-fingerprint revocation, include cycles/traversal,
and global nested includes. CI runs race tests on Windows, Linux, and macOS.
