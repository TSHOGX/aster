---
name: mcp-data-query
status: in-use
support_count: 999
confidence: 1.0
source: human-seeded (dingpan MCP-first data path, 2026-08-27)
---

# Dingpan MCP data query (default)

## When To Use

**This is the default data source for every measured-data / fleet / warehouse
question.** Call the dingpan MCP tools **already in this session's tool list**.
Do not open a URL. Do not write a client. Do not bash `kb`.

Exact Pi names on this host (hyphens in the server id become underscores):

| Call this | Wire tool | Args |
|---|---|---|
| `mcp_dp_dingpan_mcp_server_listTables` | `listTables` | none |
| `mcp_dp_dingpan_mcp_server_tableSchemas` | `tableSchemas` | `tableNames`: `schema.table`, comma-separated |
| `mcp_dp_dingpan_mcp_server_executeQuery` | `executeQuery` | `sql`: read-only `SELECT` |

If your tool list shows a different `mcp_*_listTables`, use **that** name. Do
not reconstruct names from mcp.json. Do not try both prefixes "to see which
works".

Specification / DTC / CAN decode still uses
`skills/battery-domain-reference`; then come back here for occurrences.

## Context

- Pi is already connected. Real tokens stay on the host. Never invent
  credentials. Never `curl` / `urllib` the gateway.
- **Table map:** MCP `mcp_dingpan_knowledge_kb_catalog` then
  `mcp_dingpan_knowledge_kb_get` with id `dingpan/tables` (see
  `skills/dingpan-knowledge`). Prefer the article over a full remote list when
  it already names the table.
- Upstream *descriptions* still say `listBusinessTables` / `getTableSchemas`
  and are swapped; **those names are not tools** (`Unknown tool`).

## Procedure (short path)

1. Call `mcp_dingpan_knowledge_kb_catalog`, then `mcp_dingpan_knowledge_kb_get`
   for matching ids (`dingpan/tables`, `dingpan/tools` if needed). No `find /`,
   no `kb get`, no hunting mcp.json.
2. Pick tables from `dingpan/tables` when possible. Only call
   `mcp_dp_dingpan_mcp_server_listTables` if the article does not cover it.
3. `mcp_dp_dingpan_mcp_server_tableSchemas` with `tableNames` = `schema.table`.
4. `mcp_dp_dingpan_mcp_server_executeQuery` with a narrow `SELECT`.
5. If those tools are missing from the tool list, **stop**. Do not bash `kb`,
   do not `echo | kb mcp`, do not write `/workspace/mcp_client.py`.
6. Ground the answer in tool results; charts via `skills/matplotlib-cjk-fonts`.

## Hard rules

- **Native MCP tool calls only.** Never invent `listBusinessTables` /
  `getTableSchemas`.
- **No DIY MCP client** and **no kb CLI**.
- **No filesystem scavenger hunt.**
- **Read-only.** No mutating SQL unless the user asks.
- **Be economical.** Skip `listTables` when the target table is already known.

## Known Pitfalls

- A session that `find`s mcp.json then `kb get` then pipes JSON-RPC to `kb mcp`
  has already failed: the first turn should have been
  `mcp_dingpan_knowledge_kb_catalog`.
- `mcp_dp_dingpan_listTables` is **not** the name on this host. Use
  `mcp_dp_dingpan_mcp_server_listTables` (or whatever is actually listed).
- Upstream descriptions are swapped. Follow name + inputSchema.
- `tableSchemas.tableNames` must include the schema prefix.
