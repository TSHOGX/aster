# MCP for Pi (harness lab)

Vendored from open-source [`pi-mcp-extension`](https://www.npmjs.com/package/pi-mcp-extension) (MIT).

On sandbox start, `harness-agent-entrypoint` links this directory into
`/agent-home/.pi/agent/extensions/mcp` and seeds `/agent-home/.pi/agent/mcp.json`
if missing. Without that link, Pi only exposes read/bash/edit/write — `mcp.json`
on disk is not enough.

Seeded servers: `demo`, `dp-dingpan-mcp-server` (host gateway), `dingpan-knowledge`
(`kb mcp`). Edit `mcp.json` to add more (stdio / streamable-http / sse).

## executeQuery `output` (harness overlay)

The vendored extension is patched here so Pi-facing `executeQuery` accepts an
optional `output` argument the upstream Huawei server does not know about:

```
executeQuery({
  sql: "SELECT …",
  output: "file:///workspace/data/result.csv"
})
```

The bridge strips `output` before `tools/call`, writes CSV under `/workspace`
(or `$SESSION_WORKSPACE`), and returns `{path, rowCount, cols, sample}` to the
model. Paths must stay under the workspace (`file://` or `/workspace/...`).
Replies over ~4KB or >20 rows are auto-landed to `/workspace/data/query_<ts>.csv`
even when `output` is omitted. See `query-output.ts`.

