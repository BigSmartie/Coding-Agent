# SearXNG endpoint operations

MyCode's configured search endpoint is
`https://search.bigsmartie.cn/search`. The Windows user setting
`webSearchEndpoint` enables it; no search credential or server secret belongs
in this repository. A live run of `TestLiveSearXNGSearch` returned seven
results on 2026-10-09.

The user's OpenCloudOS server runs a separate, digest-pinned SearXNG and
Valkey Compose project at `/opt/mycode-searxng/compose.yml`. SearXNG binds
only to `127.0.0.1:18081`; Valkey has no published port. The server secret is
in a root-only `.env` beside the Compose file. The SearXNG settings enable
JSON results, its limiter, and search engines reachable from that server.
The new Nginx virtual host is
`/www/server/panel/vhost/nginx/search.bigsmartie.cn.conf`. It terminates
HTTPS, rate-limits `/search`, and restricts access to the current client
network. It does not replace the existing virtual hosts. If the client's
outbound network changes, update both the Nginx allowlist and SearXNG's
`config/limiter.toml` passlist, then test Nginx and reload it and restart
SearXNG. Do not remove the allowlist merely to make a failed request pass.

Let's Encrypt certificate files live under
`/etc/letsencrypt/live/search.bigsmartie.cn/`. The server's
`certbot-renew.timer` is enabled; a deploy hook tests and reloads the existing
Nginx after renewal. `certbot renew --dry-run --cert-name
search.bigsmartie.cn` succeeded when the endpoint was installed. The
challenge webroot is `/opt/mycode-searxng/acme`.

For a read-only service check on the server, run `docker compose -f
/opt/mycode-searxng/compose.yml ps` and `systemctl is-active
certbot-renew.timer`. From an allowed client network, request
`https://search.bigsmartie.cn/search?q=OpenAI&format=json`; it should return
HTTP 200, `application/json`, and a nonempty `results` array. The opt-in Go
acceptance test uses the normal MyCode egress path:

```powershell
$env:MY_CODE_LIVE_WEB_SEARCH = "1"
$env:MY_CODE_WEB_SEARCH_ENDPOINT = "https://search.bigsmartie.cn/search"
go test -run '^TestLiveSearXNGSearch$' -count=1 -v ./internal/tools
```
