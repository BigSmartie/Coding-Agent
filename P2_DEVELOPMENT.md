# P2 development closeout

P2 is complete for the Windows-first scope selected for the native sandbox.
The five milestones are merged on `main`. This record distinguishes implemented
behavior from checks that require a user's own service or platform setup.

| Milestone | Delivered behavior | Review |
| --- | --- | --- |
| P2.1 | Bounded Jupyter notebook reads and reviewed, digest-checked single-cell edits that clear stale code outputs | [PR #9](https://github.com/BigSmartie/Coding-Agent/pull/9) |
| P2.2 | Approved HTTPS fetch and optional SearXNG-backed search through the guarded egress client | [PR #10](https://github.com/BigSmartie/Coding-Agent/pull/10) |
| P2.3 | Optional VS Code extension for workspace launch and reviewable active-file context | [PR #11](https://github.com/BigSmartie/Coding-Agent/pull/11) |
| P2.4 | Opt-in Anthropic prompt caching and estimates from user-supplied token prices | [PR #12](https://github.com/BigSmartie/Coding-Agent/pull/12) |
| P2.5 | Explicit Windows WSL2/Bubblewrap execution backend for foreground commands, stdio MCP, and non-PTY jobs | [PR #13](https://github.com/BigSmartie/Coding-Agent/pull/13) |

Each milestone's contract and failure behavior are in its linked
[P2.1 RFC](docs/rfc/p2-1-notebook-editing.md),
[P2.2 RFC](docs/rfc/p2-2-web-tools.md),
[P2.3 RFC](docs/rfc/p2-3-vscode-integration.md),
[P2.4 RFC](docs/rfc/p2-4-cache-cost.md), and
[P2.5 RFC](docs/rfc/p2-5-wsl-sandbox.md). The user-facing setup is in the
[README](README.md), and the trust boundary is in [SECURITY.md](SECURITY.md).

## Acceptance evidence

- PR #13 passed the GitHub Actions Windows, macOS, and Linux race-test matrix,
  the actual Docker isolation job, and both egress and MCP fuzz jobs. Those
  checks also cover the earlier P2 milestones after their merge.
- On the Windows development host, the opt-in `TestWSLBackendIsolation` test
  passed against an actual WSL2 distribution. It checked non-root execution,
  filtered snapshot contents, inaccessible Windows drive mounts, denied network,
  temporary edits, retained artifact export, and cancellation.
- The local full `go test -race -count=1 -timeout=5m ./...`, `go vet ./...`,
  `go mod verify`, VS Code `node --test`, and a 15-second egress fuzz run passed
  after the final code change. An earlier CI fuzz run exposed a leading-zero
  default-port bug; PR #13 includes its fix and a deterministic regression
  test. The follow-up local fuzz run exercised the URL policy about 1.46
  million times without a failure.

## Operational limits

- The WSL backend is opt-in with `MY_CODE_SANDBOX_BACKEND=wsl` and requires
  WSL2 plus Bubblewrap, `prlimit`, and `wslpath` in the default distribution.
  Docker remains the default. Native Windows PTY jobs are unsupported; WSL
  resource limits are process limits rather than Docker cgroup limits.
- The VS Code extension is delivered as source and can be packaged as a VSIX;
  it is not published in the Marketplace.
- Live SearXNG search needs a user-configured endpoint. Live provider cache
  hits and actual billing need user-owned credentials and are not CI checks.
  Cost figures are estimates, not a spending cap or billing record.
- P2 adds optional product breadth to an early Alpha. The P0/P1 security and
  reliability boundaries in [SECURITY.md](SECURITY.md) still apply.
