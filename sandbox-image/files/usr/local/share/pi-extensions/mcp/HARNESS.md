# MCP for Pi (harness lab)

Vendored from open-source [`pi-mcp-extension`](https://www.npmjs.com/package/pi-mcp-extension) (MIT).

On sandbox start, `harness-agent-entrypoint` links this directory into
`/agent-home/.pi/agent/extensions/mcp` and seeds `/agent-home/.pi/agent/mcp.json`
if missing. Without that link, Pi only exposes read/bash/edit/write — `mcp.json`
on disk is not enough.

Seeded servers: `demo`, `dp-dingpan-mcp-server` (host gateway), `dingpan-knowledge`
(`kb mcp`). Edit `mcp.json` to add more (stdio / streamable-http / sse).
