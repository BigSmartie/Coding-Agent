# P1.1 RFC: session event journal, first slice

Status: implemented for the local CLI; the rest of P1.1 remains open.

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
| Turn has events after the last checkpoint | Refuse automatic resume and retain the last complete transcript. Tool effects may exist and require workspace review. |
| Final JSONL line was interrupted | Discard that incomplete line while holding the session lock. A tool cannot have started after an unacknowledged start event. |
| Complete event is corrupt, sequence/hash is wrong, or a checkpoint's journal is missing | Fail closed rather than guess the transcript. |
| Another process owns the session lock | Refuse a second active session. |

Schema-1 snapshots remain readable. Their next save upgrades them to schema 2;
the original JSON is replaced atomically. The older P0 binary rejects schema 2,
so rollback requires restoring a pre-upgrade session backup or starting a new
session. No automatic downgrade rewrites the journal.

## Threat model and limits

The journal and lock live in the same private per-user session directory as
P0 snapshots. State-file open rejects non-regular entries and swapped symlinks.
Checkpoint sanitization uses the existing exact-secret and opaque-provider-state
rules. A signed/opaque continuation that must be redacted remains visible in
history but disables resume. Event metadata is redacted too. The hash chain
detects accidental corruption and unsophisticated edits; it is not an
authentication mechanism against a process with the same user account.

This slice intentionally never replays an incomplete turn. It also does not
yet compact the bounded 128 MiB journal, recover a partially completed tool
batch into a new resumable turn, or add model capability metadata. Approval
events are emitted by the TUI prompt; future permission plumbing must cover
other approval surfaces. Those are follow-up P1.1 changes before the milestone
is called complete.

## Verification

Deterministic tests cover checkpoint replay after a simulated crash, interrupted
tool-turn refusal, torn-tail repair, complete-event corruption, secret redaction,
schema-1 migration, and a second process failing to take the lock. The agent
test proves a tool is not called when its durable intent write fails. CI runs
the Go suite on Windows, Linux, and macOS; platform-specific native lock
behavior is exercised by the same test on each runner.
