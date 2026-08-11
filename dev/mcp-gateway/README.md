# Host-side MCP gateway (MVP)

Gateway between the sandbox `pi-mcp` client and a real upstream MCP SSE
server so the sandbox never holds the upstream token. Pattern mirrors
`dev/model-proxy/` (HTTP reverse proxy + credential injection).

## What it does

1. Accepts legacy MCP SSE from the sandbox (`GET …/sse`).
2. Forwards to `UPSTREAM_SSE_URL`, injecting `Authorization` / `X-Auth-Token`
   from the environment. Client-supplied auth headers are dropped.
3. Rewrites SSE `event: endpoint` payloads so the client's message POSTs hit
   **this gateway**, not the real upstream host.
4. Forwards `POST` JSON-RPC (`initialize`, `tools/list`, `tools/call`, …).
5. Optionally (`REQUIRE_AUTHZ=1`) denies `tools/call` unless the sandbox
   source IP has an active turn via
   `POST /internal/dataplane/requests/authorize`.

Out of scope for this MVP: streamable-http upstreams, multi-upstream routing,
locked-egress allow-list changes, projecting a read-only `mcp.json`.

## Layout

| File | Role |
|---|---|
| `gateway.py` | The gateway |
| `.env.example` | Env template (copy to `.env`, gitignored) |

## Run against a real upstream

```bash
cd dev/mcp-gateway
cp .env.example .env   # edit tokens + UPSTREAM_SSE_URL + PUBLIC_BASE_URL
set -a && . ./.env && set +a
python3 gateway.py
```

Sandbox `mcp.json` (no real token):

```json
{
  "mcpServers": {
    "data-governance-mcp-server": {
      "transport": "sse",
      "url": "http://harness-mcp.internal:8103/api/v1/sse",
      "lifecycle": "eager"
    }
  }
}
```

`PUBLIC_BASE_URL` must match what the sandbox actually dials.

## Use `http://harness-mcp.internal:8103`

The alias only works after the control plane projects it into the sandbox
`/etc/hosts` (same mechanism as Doris/OBS). Steps:

1. **Enable the config entry** in `config/harness.yaml`:

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
           kind: mcp_sse
           base_url: http://{real-host}:8003/data-governance-service/api/v1/sse
   ```

2. **Restart the orchestrator** so it reloads `data_proxies` and passes
   `harness-mcp.internal` into `SandboxDataProxyAliases`.

3. **Start a new Agent session** (hosts are written at generation start; an
   already-running sandbox will not pick up the new alias).

4. **On the same Linux host as orchestrator**, run the gateway with
   `PUBLIC_BASE_URL=http://harness-mcp.internal:8103`.

5. **Verify inside the sandbox:**

   ```bash
   grep harness-mcp /etc/hosts
   curl -sS http://harness-mcp.internal:8103/healthz
   ```

With `network.egress.allow_all: true` (checked-in default), port 8103 on the
host gateway IP is reachable without further egress changes.

## Design notes

- Authorize **`tools/call` only**, not SSE connect / `tools/list` — otherwise
  pi-mcp's eager `session_start` connect fails before any turn exists.
- On authz denial return a JSON-RPC error body; do **not** drop the SSE
  connection (that trips the client's reconnect backoff).
- Absolute upstream endpoint URLs must be rewritten; relative paths are OK.
- This hides the **credential**, not the **data**. Lock egress separately if
  query results must not leave the box.
- Upstream SSE reads use `resp.fp.read1()` — `http.client`/`BufferedReader.read()`
  blocks trying to fill its buffer on a long-lived stream and will stall the
  proxy.
