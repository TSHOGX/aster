# Host-side MCP gateway

Gateway between the sandbox `pi-mcp` client and a real upstream MCP server so
the sandbox never holds the upstream token. Pattern mirrors `dev/model-proxy/`.

## Transports

| Upstream | Env | Sandbox `mcp.json` |
|---|---|---|
| Legacy SSE | `UPSTREAM_SSE_URL` | `"transport": "sse"`, url `…/api/v1/sse` |
| Streamable HTTP | `UPSTREAM_HTTP_URL` | `"transport": "streamable-http"`, url `http://harness-mcp.internal:8103/` |

Both can be enabled in one process. `/sse` routes to SSE; other GET/POST/DELETE
go to streamable-http (except legacy SSE message POSTs when both are set).

## What it does

1. Strips client `Authorization` / `X-Auth-Token` / `x-api-key`.
2. Injects real credentials from the environment.
3. **SSE:** rewrites `event: endpoint` so POSTs stay on this gateway.
4. **Streamable HTTP:** proxies POST/GET/DELETE to `UPSTREAM_HTTP_URL`,
   forwarding `Mcp-Session-Id` / `Mcp-Protocol-Version` and streaming SSE
   responses when the upstream returns `text/event-stream`.
5. Optional (`REQUIRE_AUTHZ=1`) turn authz on `tools/call` only.

## Run (streamable-http example)

```bash
cd dev/mcp-gateway
cp .env.example .env
# set UPSTREAM_HTTP_URL + UPSTREAM_AUTHORIZATION + PUBLIC_BASE_URL
set -a && . ./.env && set +a
python3 gateway.py
```

Sandbox `mcp.json` (no real token):

```json
{
  "mcpServers": {
    "dp-dingpan-mcp-server": {
      "transport": "streamable-http",
      "url": "http://harness-mcp.internal:8103/",
      "lifecycle": "eager"
    },
    "dingpan-knowledge": {
      "transport": "stdio",
      "command": "/usr/local/bin/kb",
      "args": ["mcp"],
      "lifecycle": "eager"
    }
  }
}
```

`dp-dingpan-mcp-server` is the live warehouse (this gateway). `dingpan-knowledge`
is a local stdio server that reads `/agents/knowledge` and exposes `kb_catalog` /
`kb_get`. It does not go through this gateway and does not need a data-proxy.

The gateway ignores the client path and always forwards to `UPSTREAM_HTTP_URL`,
so `/` or `/mcp` on the gateway both work.

A second, tokenless MCP (`trajectory-evolve` for on-demand KB evolution) does
**not** go through this gateway. It binds `:8104` itself and uses a separate
`data_proxies` alias. See [`docs/self-evolve-mcp.md`](../../docs/self-evolve-mcp.md).

## Use `http://harness-mcp.internal:8103`

1. Enable in `config/harness.yaml`:

   ```yaml
   harness:
     data_proxies:
       mcp:
         enabled: true
         bind_addr: 0.0.0.0:8103
         sandbox_alias: harness-mcp.internal
         sandbox_port: 8103
         sandbox_user: sandbox
         upstream:
           kind: mcp_streamable_http   # or mcp_sse
           base_url: http://mcpgateway.example/mcp/…
   ```

2. Rebuild/restart orchestrator, then **new** Agent session.
3. Run `gateway.py` on the same Linux host as orchestrator.
4. Verify in sandbox: `curl -sS http://harness-mcp.internal:8103/healthz`

## Design notes

- Authorize **`tools/call` only**, not connect / `tools/list`.
- On authz denial return JSON-RPC error; do not drop the connection.
- Hides the **credential**, not the **data**.
- Upstream stream reads use `resp.fp.read1()` (plain `read()` stalls on SSE).
