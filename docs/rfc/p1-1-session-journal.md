# P1.1 RFC: session event journal and model capabilities

Status: implemented for local CLI and TUI sessions. Job lifecycle events are
added with the P1.3 job runtime; remote MCP and sub-agent events follow their
own P1 milestones.

## Problem and contract

P0 saved a complete JSON transcript only after a turn ended. A process crash
between a tool side effect and that save could leave the transcript behind the
workspace. Blindly replaying the turn could execute the same tool twice.

Each CLI session now holds an exclusive OS file lock for its lifetime and writes
`<id>.events.jsonl` beside `<id>.json`. Event schema version 1 has a contiguous
sequence, UTC timestamp, kind, optional turn/call metadata, and a SHA-256 hash
chained to the previous event. The event kinds are turn/model/tool start and
completion/failure, approval request/decision, and checkpoint. Tool input and
output are never copied into execution events. A checkpoint contains the full
sanitized transcript and its sequence. The snapshot schema is version 2 and
stores the checkpoint sequence.

All event appends are synchronous and call `Sync`. A failed model-start event
prevents the provider call; a failed tool-start event prevents tool execution.
The checkpoint event is synced before the atomic JSON snapshot replacement.
The supported durability claim is process-crash recovery, not power-loss
survival on every filesystem.

## Recovery and rollback

| Disk state | Recovery behavior |
| --- | --- |
| Checkpoint event is newer than the snapshot | Replay its transcript in memory; the next save updates the snapshot. No model/tool call is replayed. |
| Interrupted turn has no tool start or approval decision | `--resume` appends a turn-abandoned event and a new checkpoint from the last complete transcript. No model/tool call is replayed. |
| Interrupted turn includes a tool start or approval decision | `--resume` refuses. After reviewing the workspace, `--resume <id> --recover-interrupted` explicitly abandons the turn and checkpoints the previous transcript. No tool is re-executed. |
| Final JSONL line was interrupted | Discard that incomplete line while holding the session lock. A tool cannot have started after an unacknowledged start event. |
| Complete event is corrupt, sequence/hash is wrong, or a checkpoint's journal is missing | Fail closed rather than guess the transcript. |
| Another process owns the session lock | Refuse a second active session. |

Schema-1 snapshots remain readable. Their next save upgrades them to schema 2;
the original JSON is replaced atomically. The older P0 binary rejects schema 2,
so rollback requires restoring a pre-upgrade session backup or starting a new
session. No automatic downgrade rewrites the journal.

After a successful snapshot save, a journal above 32 MiB is atomically reduced
to its latest checkpoint. The replacement root has an explicit `compacted`
marker, retains the checkpoint sequence and predecessor, and receives a new
hash; later events continue from that hash. Readers reject an unmarked missing
prefix and validate all links after the compacted root.
An interrupted replacement leaves either the old complete journal or the new
complete checkpoint. Compaction discards older event details, so this journal
is a recovery mechanism, not a permanent audit archive.

## Model capability contract

`model.CapabilitiesFor` returns schema-1 adapter metadata: wire protocol,
configured context/output limits, tool encoding, parsed usage and cache usage,
reasoning controls, and opaque reasoning-state handling. It describes the
adapter, not a guarantee that a selected model or compatible gateway supports
each feature. Context window defaults to unknown (`0`); users may set
`contextWindowTokens` in their private settings or
`MY_CODE_CONTEXT_WINDOW_TOKENS` in their process environment. Project settings
cannot raise this trusted limit. `TokenUsage` normalizes Anthropic cached read
and write tokens into the input total, and records the corresponding OpenAI
usage breakdowns. OpenAI usage fields follow the [Chat Completions](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
and [Responses](https://developers.openai.com/api/reference/resources/responses/methods/retrieve)
schemas; Anthropic's [usage accounting](https://platform.claude.com/docs/en/about-claude/pricing)
counts uncached input, cache creation, and cache reads separately.

## Threat model and limits

The journal and lock live in the same private per-user session directory as
P0 snapshots. State-file open rejects non-regular entries and swapped symlinks.
Checkpoint sanitization uses the existing exact-secret and opaque-provider-state
rules. A signed/opaque continuation that must be redacted remains visible in
history but disables resume. Event metadata is redacted too. The hash chain
detects accidental corruption and unsophisticated edits; it is not an
authentication mechanism against a process with the same user account.

An interrupted turn is always abandoned rather than replayed. A recovered
session resumes from the last complete checkpoint, so the next user prompt
must account for any external effects from the interrupted tool. Approval
events are emitted by the TUI prompt; other approval surfaces must use the
same journal hook when introduced.

## Verification

Deterministic tests cover checkpoint replay after a simulated crash, interrupted
tool-turn refusal, torn-tail repair, complete-event corruption, secret redaction,
schema-1 migration, compacted checkpoint replay, automatic side-effect-free
recovery, explicit recovery after tool start, and a second process failing to
take the lock. The agent
test proves a tool is not called when its durable intent write fails. CI runs
the Go suite on Windows, Linux, and macOS; platform-specific native lock
behavior is exercised by the same test on each runner.
