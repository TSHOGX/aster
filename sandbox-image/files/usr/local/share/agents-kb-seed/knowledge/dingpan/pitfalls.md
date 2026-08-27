---
id: dingpan/pitfalls
title: Dingpan MCP pitfalls
description: Known dingpan MCP failure modes — DIY HTTP/SSE clients, invented tool names, swapped descriptions, schema prefixes, missing table map
tags: [dingpan, mcp, pitfalls]
---

# Dingpan MCP pitfalls

## Do not reimplement the MCP client

If you see SSE (`event: message`) or a hostname for the host gateway, that is
**not** an invitation to `urllib`/`curl`, write `/workspace/mcp_client.py`,
probe `/`, `/mcp`, `/api`, `/v1`, or `find` tokens under `/agent-home`. Pi
already speaks streamable-http. Call `mcp_*_listTables` /
`mcp_*_tableSchemas` / `mcp_*_executeQuery` from the tool list. If they are
missing, stop — the session is miswired.

## Invented names are not tools

The live tools are `listTables`, `tableSchemas`, `executeQuery`. The strings
`listBusinessTables` and `getTableSchemas` appear only in **upstream
descriptions**. Calling them returns `Unknown tool` (fast). Do not invent other
names either.

## Descriptions are swapped

`tools/list` prose for the three tools is copy-pasted onto the wrong names
(`tableSchemas` text talks about listing tables; `executeQuery` text talks about
DDL). Ignore that prose. Use **name + inputSchema**:

- `listTables` — no args
- `tableSchemas` — `tableNames`
- `executeQuery` — `sql`

## Schema prefix is required

`tableSchemas` needs `schema.table` (example:
`digital_operation.dwd_e2e_trace_barcode_material`). Bare table names fail.
Batch with commas only after each name is fully qualified.

## Heavy calls are slow, not broken

`listTables` and large `executeQuery` can sit on upstream for a long time. Wait
for the result. If the host needs it, raise `settings.requestTimeoutMs` in
`mcp.json` — do not retry-spam. A *steady* ~60s on every call was the old
gateway SSE hang (fixed); do not treat that as a reason to skip `listTables`.

## Skip the filesystem scavenger hunt

Do not `find /` for `mcp.json`, credentials, or table maps. Do not
`cat ~/.pi/agent/auth.json`. Knowledge is the `dingpan-knowledge` MCP tools
(`kb_catalog` / `kb_get`). Live data is dingpan MCP. If tools are missing from
the session list, report a wiring failure.

## Missing table map

If `kb_get dingpan/tables` is a stub and `/agents/data/dingpan/README.md` is
absent, say so. Do not invent table names. `listTables` is the fallback for
discovery, then `tableSchemas` before any `executeQuery`.

## No silent fallback

If dingpan cannot answer, say so and ask. Do not silently open Doris SQL or
OBS Parquet unless the user explicitly orders that path.

## TODO (human)

- Record the exact error payloads you have seen (`Unknown tool`, authz denials,
  empty `data`, truncated `LIMIT`).
- Note tables that look queryable but are too large / wrong grain / PII.
