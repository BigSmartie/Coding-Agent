# P2.5 RFC: Windows Docker-free WSL2 sandbox

## Contract

`MY_CODE_SANDBOX_BACKEND=wsl` explicitly selects a Docker-free execution
backend on Windows. The default remains Docker. The selected default WSL2
distribution must already contain `/usr/bin/bwrap`, `/usr/bin/prlimit`, and
`/usr/bin/wslpath`; MyCode neither installs them nor falls back to the Windows
host if preflight fails. Only Linux executables installed in that distribution
can run. A WSL1 distribution is refused. The backend works for foreground
commands, stdio MCP, and non-PTY background jobs with reviewed artifact export.
Native Windows PTY jobs remain unsupported.

## Isolation and resource limits

MyCode makes the same filtered Windows workspace snapshot used by Docker and
converts only that private snapshot path into a WSL path. Bubblewrap creates
user, PID, mount, IPC, UTS, cgroup, and network namespaces with `--unshare-all`,
drops capabilities, disables nested user namespaces, and runs as UID/GID 65534.
It mounts a minimal WSL runtime read-only, the snapshot read-only at `/input`,
and a 512 MiB temporary `/workspace`. The Windows drive mounts normally
visible inside WSL are not mounted inside Bubblewrap. A fixed bootstrap copies
the snapshot into `/workspace` before launching the requested executable; no
model text is interpolated into shell source. `/tmp` is a 256 MiB tmpfs.

`prlimit` caps virtual address space at 1 GiB, CPU time at 120 seconds, file
size at 512 MiB, and process count at 128. Background jobs use a session-private
Windows temporary scratch directory mounted only at `/workspace`, with the
existing file-count and aggregate-size scan. The same path-contained reviewed
export flow applies. Cancellation terminates the `wsl.exe` command; Bubblewrap
uses `--die-with-parent` for its child. No Windows workspace path, credentials,
proxy settings, or Windows executable directory is forwarded into the guest.

The default WSL distribution and its runtime binaries are trusted components;
the backend is not a defense against a compromised Windows account or WSL
distribution. Resource limits here are process limits rather than Docker's
cgroup limits, so they are not identical. Networked dependency installation
inside the sandbox remains unavailable.

## Validation and recovery

Deterministic tests inspect mount, namespace, environment, command and path
policy. An opt-in test on a Windows host with WSL2 executes actual commands to
verify non-root identity, filtered files, no host-drive mount, denied outbound
socket connection, temporary edits, retained artifact export, and cancellation:

```powershell
$env:MY_CODE_WSL_INTEGRATION = "1"
go test -run '^TestWSLBackendIsolation$' -count=1 ./internal/sandbox
```

On a crash, `--die-with-parent` ends the isolated process and session recovery
removes leftover scratch directories. A failed preflight or missing required
binary returns an explicit error without starting a host command. The default
Docker backend and its CI integration tests are unchanged.
