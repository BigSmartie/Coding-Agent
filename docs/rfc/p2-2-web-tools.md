# P2.2 RFC: reviewed web search and fetch

## Contract

`web_fetch` accepts one absolute HTTPS URL and returns up to 20 KiB of readable
UTF-8 text from an HTML, plain-text, JSON, or XML response. HTML scripts,
styles, templates, SVG, and MathML are excluded from the extracted text. The
result includes the URL, a truncation indicator, and an untrusted-content
warning. Unsupported and empty responses fail closed.

`web_search` is available only when the user settings file contains a
`webSearchEndpoint` such as `https://search.example/search`. Project settings
cannot set or override it. The endpoint must be an absolute, query-free HTTPS
URL ending in `/search`; the tool constructs the query with `format=json` and
`safesearch=1`. The model controls only the query, limited to 256 UTF-8 bytes.
It returns at most eight public HTTPS result URLs with bounded titles and
snippets. The configured SearXNG instance must enable JSON responses; a 403 or
non-JSON response is reported as a tool error. The search instance may forward
the query to upstream search engines according to its own configuration.

## Trust and failure behavior

Every request requires fresh interactive approval showing its URL, including
path and query. URLs containing recognized credentials are rejected before the
prompt, including percent-encoded credentials. The existing
origin permission is checked separately by the shared egress client and can be
persisted for that exact origin. In a
headless session, URL approval fails closed even when the origin has a grant.
The shared egress client enforces public DNS/IP resolution, TLS, no proxy, no
redirects, 20-second timeout, a 4 MiB response cap, and a 32 MiB session cap.
Its requested/completed/failed audit events record the origin, method, status,
and byte count.

The tool never treats returned text, snippets, or result URLs as instructions.
Search result URLs are displayed to the model but are not fetched without a
separate `web_fetch` request and approval. The request path and query are
transmitted to the approved site, so the approval prompt explicitly shows
them. No new persisted session schema is introduced. If the endpoint is absent,
search is omitted from the tool registry; fetch remains available under the
same per-URL approval gate.

Deterministic tests cover endpoint ownership and validation, fresh URL review,
headless denial, HTML sanitization, response bounds, unsafe result filtering,
and fixed search endpoint construction. The existing egress tests cover DNS,
redirect, TLS, and traffic enforcement. A live search test requires a
user-configured SearXNG instance and is not part of CI. The opt-in
`TestLiveSearXNGSearch` exercises the configured endpoint through the normal
reviewed-tool and egress path with one fixed public query.
