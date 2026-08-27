---
id: dingpan/tools
title: Dingpan MCP tools
description: The three supported dingpan MCP tools (listBusinessTables, getTableSchemas, executeQuery) and how to call them
tags: [dingpan, mcp, tools]
---

# Dingpan MCP tools

**Only these three tools exist.** Do not invent other names. Wrong names
(`listTables`, `tableSchemas`) often hang until the client timeout.

| Tool | Purpose |
|---|---|
| `listBusinessTables` | Tables this token can access (name, biz desc, source) |
| `getTableSchemas` | DDL for one or more tables; names need `schema.table`; comma-separated for batch |
| `executeQuery` | Read-only `SELECT`; server may auto `LIMIT 10000`; JSON with `rowCount` + `data` |

Pi-facing names are prefixed with the MCP server id, e.g.
`mcp_dp_dingpan_listBusinessTables`. Discover the live names from the session
tool list; do not hard-code a prefix if the list already shows them.

## Call order

1. Pick tables from `kb get dingpan/tables` when the article already names them.
2. Call `listBusinessTables` **only** if the table map does not cover the question.
3. `getTableSchemas` with `schema.table` (example:
   `digital_operation.dwd_e2e_trace_barcode_material`). Batch with commas.
4. `executeQuery` with a narrow `SELECT` (filters + explicit columns). Do not
   `SELECT *` on huge tables.

## Hard rules

- Native MCP tool calls only. No bash/`curl` against the gateway.
- Read-only. No mutating SQL.
- If dingpan tools are missing from the tool list, stop and report wiring
  failure.

## TODO (human)

- Paste real example arguments / JSON shapes from a successful session.
- Note any extra tools this deployment actually exposes (only if confirmed).
