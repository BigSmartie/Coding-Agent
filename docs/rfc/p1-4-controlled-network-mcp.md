# P1.4 RFC: controlled egress, provider conformance, and remote MCP

## Scope and trust boundary

Remote MCP is configured with `mythoscode mcp add-url <name> <https-url>` and must first be trusted with `mythoscode trust mcp <name>`. A URL cannot contain credentials, query parameters, fragments, command fields, or environment variables. Trusting the server configuration permits discovery, not its tools' external effects: each model-triggered remote tool, resource read, or prompt retrieval asks for a fresh operation approval. Headless operation fails closed. Stdio MCP keeps its sandbox command approval flow.

Remote MCP uses the official Go SDK Streamable HTTP client over the guarded egress transport. Every request, including retries, GET subscriptions, and DELETE cleanup, is bound to the configured HTTPS origin. The transport rejects redirects, host proxies, private/non-public DNS answers, URL userinfo, and headers outside the MCP allowlist. DNS addresses are checked before use and pinned for each dial, while TLS still authenticates the configured hostname. The connection has 1 MiB request, 4 MiB response, and 32 MiB cumulative traffic limits. Network phases are appended to the session journal; an interrupted request requires explicit effect review on recovery. Startup discovery events are buffered until the journal is available. The transport does not forward provider credentials or arbitrary Authorization headers to MCP servers.

The endpoint is an origin grant, not a path or subdomain grant. Project settings and MCP descriptions are untrusted input. Tool schemas are bounded by depth, array/property count, and string length; external schema references are blocked. Discovery caps each list at 32 pages and 512 entries and rejects repeated cursors. Capability negotiation avoids unsupported lists when advertised; legacy stdio servers without capability metadata retain compatibility. List-change notifications trigger revalidation before subsequent calls. Cancellation sends an MCP notification for stdio and then terminates the isolated server, because it may ignore cancellation. Remote calls use the SDK's request cancellation and bounded transport.

## Provider conformance

The local fixture matrix asserts the same normalized tool-call ID, name, arguments, token usage, and incomplete-output rejection for Anthropic Messages, Anthropic-compatible gateways, OpenAI Chat Completions, OpenAI-compatible gateways, and OpenAI Responses. Live provider smoke tests remain opt-in because they require billable user credentials. Compatible providers that omit `total_tokens` receive a sum of their reported input/output tokens.

## Recovery and rollback

Network journal events are additive to the P1.1 event format. Older binaries cannot interpret them; use a pre-P1.4 session backup or start a new session when rolling back. Exact origin grants are stored outside the workspace alongside existing permission state. Removing a remote MCP server from configuration stops future startup; revoking its trust fingerprint or stored origin approval blocks subsequent use. No remote secrets are stored in project configuration.

## Validation

Local tests cover exact-origin approval, SSRF and redirect denial, response bounds, SDK traffic through the production guarded transport, remote operation approval, stdio pagination and notifications, journal DELETE/recovery, and the provider conformance matrix. CI runs formatting, module verification, vet, race tests on Windows/Linux/macOS, and real Docker sandbox integration on Linux.
