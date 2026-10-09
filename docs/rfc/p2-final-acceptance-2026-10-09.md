# P2 follow-up acceptance — 2026-10-09

The P2 milestones were merged before this follow-up. This record covers the
remaining checks that can run with the services available on the Windows host.

| Scenario | Result | Evidence |
| --- | --- | --- |
| DeepSeek `deepseek-v4-pro` streaming tool call and continuation | Passed | Opt-in `TestLiveDeepSeekToolContinuation`, using the OS credential store; no key in source or logs. |
| DeepSeek automatic context-cache hit | Passed | Opt-in `TestLiveDeepSeekCache`: a fresh repeated prefix produced 0 cached input tokens on the first request and 9,984 on the second, using the OS credential store. |
| Windows Docker Linux-container PTY, isolation, cancellation, artifact | Passed | `TestIntegrationBackgroundJobPTYCancelAndArtifact` and sandbox integration suite on Docker Desktop 29.5.2, with a locally available trusted Python image. |
| WSL2 guest PTY, sustained job, artifact, cancellation | Passed | `TestIntegrationWSLGuestPTY` on the local Windows/WSL2 host. |
| Checkpoint, crash replay, interrupted tool recovery, durable task | Passed | Focused `internal/session` journal and task tests. |
| Go and VS Code unit checks | Passed | `go vet ./...`, `go test -count=1 -timeout=5m ./...`, `node --test editors/vscode/context.test.js`. |
| VS Code installable package | Prepared | `mycode-vscode-0.1.0.vsix` built with `@vscode/vsce`; package is a local ignored artifact. |
| SearXNG HTTPS JSON search | Passed | `TestLiveSearXNGSearch` returned seven bounded public results from the configured endpoint. |

Windows Docker PTY is allocated inside the container by a fixed Python helper.
The Docker CLI uses pipes on the Windows side, preserving the existing resource
limits, filtered snapshot, explicit local-image policy, and reviewed artifact
flow. The trusted image must include `python3`; both supplied Dockerfiles now
do. The local Docker Hub registry connection was unavailable when attempting
to build the Alpine test image, so the Windows integration used the locally
installed `python:3.12-slim` image. CI builds and tests the supplied Alpine
image on Ubuntu.

The SearXNG endpoint is now `https://search.bigsmartie.cn/search` in the
Windows user settings. It runs on the user's server behind HTTPS and an
address allowlist; see the [operations note](../ops/searxng.md). The endpoint
was exercised through the normal MyCode egress client. The remaining live
acceptance needs external prerequisites:

* Anthropic cache: no Anthropic credential is configured. The optional
  `TestLiveAnthropicCache` in `internal/model` remains available when one is.
  DeepSeek's separately verified automatic prefix cache does not exercise the
  Anthropic `cache_control` request or its cache-write usage field.
* VS Code Marketplace: the package is ready for publisher identity review,
  but there is no Marketplace publisher account or publishing credential on
  this host. The VSIX can be installed locally now; Marketplace publication
  requires the account owner to create the publisher and configure publishing
  authentication first.

No credential value is stored in this repository or in the VSIX.
