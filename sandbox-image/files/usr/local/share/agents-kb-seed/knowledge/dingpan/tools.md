---
id: dingpan/tools
title: Dingpan MCP tools
description: The three dingpan MCP tools as registered on the wire — listTables, tableSchemas, executeQuery — and how to call them
tags: [dingpan, mcp, tools]
---

# Dingpan MCP tools

**Trust `tools/list` names and `inputSchema`, not the upstream prose.** The
server's own descriptions still mention `listBusinessTables` / `getTableSchemas`
and have the three write-ups swapped. Those two names are **not registered**.
Calling them returns `Unknown tool`.

Confirmed 2026-08-27 against `tools/list`:

| Wire name | Arguments | What it actually does |
|---|---|---|
| `listTables` | none | List tables this token can access (name, biz desc, source) |
| `tableSchemas` | `tableNames` (string): `schema.table`, comma-separated for batch | DDL / CREATE TABLE for those tables |
| `executeQuery` | `sql` (string): read-only `SELECT` | Run the query; server may auto `LIMIT 10000` |

Pi-facing names are prefixed: `mcp_<server>_listTables` (example
`mcp_dp_dingpan_listTables`). Use the names from the live tool list.

## Call order

1. Pick tables from MCP `kb_get dingpan/tables` when the article already names them.
2. Call `listTables` **only** if the table map does not cover the question.
3. `tableSchemas` with `tableNames` = `schema.table` (example:
   `digital_operation.dwd_e2e_trace_barcode_material`). Batch with commas.
4. `executeQuery` with `sql` a narrow `SELECT` (filters + explicit columns). Do
   not `SELECT *` on huge tables.

## Hard rules

- Native MCP tool calls only. No bash/`curl`/`urllib` against the gateway. No
  `/workspace/mcp_client.py`. No probing `/mcp`, `/api`, `/v1`. No parsing
  `event: message` yourself.
- Read-only. No mutating SQL.
- If dingpan tools are missing from the tool list, stop and report wiring
  failure. Do not hunt `/agent-home` for tokens.
- Do not call `listBusinessTables` or `getTableSchemas`. They are not tools.

## TODO (human)

- Paste a successful `executeQuery` JSON shape (`rowCount` / `data` vs wrapped
  `result` string).
