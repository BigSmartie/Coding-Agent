# P1.5 RFC: bounded subagents, repository evaluation, and signed releases

## Subagent boundary

`delegate_readonly` runs a short repository investigation under a selected workspace subdirectory. The child receives only `list_files`, `grep_files`, and `read_file`. It cannot execute commands, edit files, call MCP, use network tools, or start another child. The path resolver and file access layer enforce the selected directory even through links. One session can start at most eight children, at most two concurrently. Each child has eight model steps, at most 16 tool calls, a 90-second deadline, an 8,192-token context window, a 1,024-token output setting, and a 12,000-token cumulative reported-use cutoff. If a provider does not report usage, the step and time limits still apply. Child results are capped at 8 KiB and redacted before returning to the parent.

The trace file is stored outside the repository beside session state as `<session-id>.subagents.json`. Each event has `schemaVersion: 1` and records only a child ID, scope digest, status, tool names, token count, and elapsed time. It contains no prompts, tool arguments, file contents, model output, or credentials. Both start and completion/failure records use private atomic writes and typed session journal events. A failed journal or trace write fails the child call. The trace file is additive, but older binaries cannot replay the new journal events; restore a pre-P1.5 session backup or start a new session when rolling back.

## Reproducible repository-task evaluation

`go run ./cmd/mythoscode-eval -manifest task.json` evaluates a pinned Git commit in a disposable snapshot. The manifest supplies an ID, repository path, full commit SHA, task prompt, and expected file SHA-256 digests (or `absent`). The runner exports that commit with `git archive`, rejects links, special files, protected paths, traversal, and oversized snapshots, and runs the selected model with a restricted set of file-read and file-edit tools. Command, MCP, network, and delegation tools are absent. Automatic edit approval applies only inside the disposable snapshot. The report includes pass/fail, checked paths, token count, tool names, and duration; it excludes the prompt and file contents. Source files and the user's workspace remain untouched.

Live evaluation is opt-in and may use billable provider calls. The deterministic test fixture uses a scripted model and a temporary Git repository, verifying that the same task mutates only the snapshot and that expected file hashes match. A task manifest should pin a commit from a repository whose contents the evaluator is allowed to read. The manifest accepts `schemaVersion: 1` or an omitted version for compatibility and rejects unknown versions. Reports always carry `schemaVersion: 1`; new fields may be added without changing existing meanings.

Example manifest (replace the repository, commit, and digest with your own):

```json
{
  "schemaVersion": 1,
  "id": "fix-small-bug",
  "repository": ".",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "prompt": "Fix the parser edge case in internal/parser.go.",
  "expectedFiles": {"internal/parser.go": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
}
```

The evaluator reads provider credentials from the user configuration in an empty scope, so repository settings cannot change the selected provider or credential source.

## Release artifacts and trust

`releasepack` produces stable tar.gz/ZIP archives for Linux, macOS, and Windows on amd64/arm64 from binaries built with `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, and an empty Go build ID. Archive file order, member mode, and timestamps derive from the source commit epoch. A sorted `SHA256SUMS` file and SPDX 2.3 JSON module SBOM accompany the archives. Tests compare independently produced archives byte-for-byte.

The tag-triggered release workflow verifies source, builds all targets, generates checksums and SBOM, then uses GitHub artifact attestations to sign build provenance and the SBOM with short-lived Sigstore identities before publishing the release. `actions/attest` is pinned to an immutable commit. Release signing requires GitHub's OIDC/attestation service and is exercised only when a `v*` tag is pushed; no release is published by merging this PR. To roll back a release, remove or supersede the affected GitHub Release and tag according to repository policy; the previous source commits and artifacts remain independently verifiable.

## Fault and platform checks

The regular CI suite runs race tests on Windows, Linux, and macOS plus a real Docker sandbox test on Linux. Linux CI also fuzzes URL origin parsing and MCP frame bounds. The evaluation fixture, subagent trace failure, scoped file access, call limit, deterministic archives, and SBOM format have local deterministic tests. Provider calls and signed tag publishing require opt-in external services and are not claimed as locally exercised.
