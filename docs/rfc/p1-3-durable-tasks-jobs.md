# P1.3 RFC: durable tasks and bounded background jobs

Status: implemented for local CLI/TUI sessions with the Docker sandbox.

## Data contracts and recovery

The session record carries up to 32 structured tasks (`id`, `title`, optional
`details`, `status`, `updatedAt`). Task IDs are stable, updates are validated,
secret-redacted and synced as `task_updated` events before the in-memory state
changes. A checkpoint carries the current task set independently of the model
transcript, so context compaction cannot remove tasks. On load, valid task
events after the last checkpoint are replayed. An interrupted turn still needs
P1.1 recovery; task state is kept when the turn is abandoned.

Job events have a random 16-hex-character job ID and one of `job_started`,
`job_input`, `job_cancel_requested`, `job_completed`, `job_failed`, or
`job_canceled`. They record lifecycle boundaries without input, output, or
artifacts. A terminal event arriving after a checkpoint does not make a
completed turn appear interrupted. Jobs are process-local: after a crash,
running commands are **not** resumed or replayed. The next start for that
session removes its labelled orphan containers; this requires the exclusive
session lock. A prior binary cannot parse the new event kinds. Roll back by
restoring a pre-P1.3 session backup or starting a new session.

## Execution and export

`job_start` uses the same explicit path and command approvals as foreground
execution, and the same offline, non-root, read-only Docker sandbox. At most
four jobs run concurrently, eight may exist in a session, and each is limited
to ten minutes. Docker gets a session label and a retained container for
successful artifact export. Retained jobs use an isolated private-parent host
scratch mount because Docker drops tmpfs contents when a container exits; an
fsize limit and periodic 512 MiB / 25,000-entry scan cancel runaway writes.
The scan is a soft aggregate quota, so rapid writes can briefly exceed it.
`job_attach`/`job_read` return offsets in a 1 MiB
output tail; `job_poll`, `job_list`, `job_write` and `job_cancel` expose bounded
status and control. Input writes are limited to 4096 bytes. Optional `tty`
allocates a container PTY. Session exit and cancellation stop the container
process tree and remove its snapshot and container. No job is left running as
an unattended host process.

`job_export` accepts one relative sandbox path after successful completion.
The scratch reader accepts exactly one regular file no larger than 1 MiB,
refusing symlinks, hard links and paths outside the scratch mount.
The file must be UTF-8 text without NUL bytes. The host destination is
resolved with workspace protection, and a unified diff requires explicit edit
approval before the file is written. A refused export leaves the host unchanged.
The underlying `CopyArtifact` API never writes the host workspace.

## Threat model and verification

Untrusted commands can attempt path traversal, symlink export, oversized
output, long-lived child processes, and hostile terminal bytes. Docker remains
the execution boundary; no network access or host credentials are supplied.
Artifact path normalization, secure handle/size checks, output and input
bounds, terminal escaping, path approvals and reviewed writes limit the data
crossing back to the host. The cleanup path refuses a Docker executable found
inside the untrusted workspace.

Unit tests cover task replay across a crash and transcript compaction, journal
failure without state mutation, async completion after checkpoint, output
offsets, approval denial, reviewed export, binary rejection and retained Docker
arguments. The opt-in Docker CI test starts a job, reads output, exports an
artifact, drives a PTY and cancels a background process tree. Cross-platform
CI runs the full Go suite with the race detector.
