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
question.** Statistics, reports, trends, aggregations, signal lookups, charging
analysis, and similar work must go through the **dingpan MCP server** tools
exposed in this session (via the host MCP gateway at
`http://harness-mcp.internal:8103/`).

Server name in `mcp.json` is typically `dp-dingpan` or `dp-dingpan-mcp-server`.
Pi tool names are prefixed: `mcp_<server>_<tool>` (e.g.
`mcp_dp_dingpan_listTables`).

Specification / DTC / CAN decode questions still use
`skills/battery-domain-reference`; after you decode a code or signal, come back
here and query occurrences via **dingpan MCP tools**.

## Context

- Transport: streamable-http → `http://harness-mcp.internal:8103/`. Real tokens
  stay on the host gateway; never invent credentials.
- **Table map:** call MCP `kb_catalog` then `kb_get dingpan/tables` (see
  `skills/dingpan-knowledge`). Prefer the article over a full remote table list
  when it already names the table. Bulky dumps, if provisioned, are at
  `/agents/data/dingpan/`.
- **Supported dingpan tools only** — wire names from `tools/list` (2026-08-27).
  Upstream *descriptions* still say `listBusinessTables` / `getTableSchemas` and
  are swapped; **those names are not tools**.

  | Tool | Arguments | Purpose |
  |---|---|---|
  | `listTables` | none | Tables this token can access (name, biz desc, source) |
  | `tableSchemas` | `tableNames`: `schema.table`, comma-separated | DDL for those tables |
  | `executeQuery` | `sql`: read-only `SELECT` | Run query; server may auto `LIMIT 10000` |

  **Do not call:** `listBusinessTables`, `getTableSchemas`, or any other guessed
  name. They return `Unknown tool`.

## Procedure (short path)

1. **Catalog + load:** MCP `kb_catalog`, then `kb_get` matching dingpan ids
   (`dingpan/tables` for table pick, `dingpan/tools` if you need call shape).
   See `skills/dingpan-knowledge`. No `find /`, no hunting `mcp.json`.
2. **Pick tables** from `dingpan/tables` when possible. Only call
   `listTables` if the article does not cover the question.
3. **`tableSchemas`** with `tableNames` = `schema.table` (e.g.
   `digital_operation.dwd_e2e_trace_barcode_material`). Batch with commas if
   needed.
4. **`executeQuery`** with `sql` a narrow `SELECT` (filters + explicit columns).
   Do not `SELECT *` on huge tables.
5. If dingpan tools are missing from the tool list, stop and report wiring
   failure — do not bash/`curl` the gateway, do not fall back to Doris/OBS
   unless the user asks.
6. Ground the answer in tool results; charts via `skills/matplotlib-cjk-fonts`.

## Hard rules

- **Only the three tools above.** Never invent `listBusinessTables` /
  `getTableSchemas`.
- **Dingpan MCP first.** Native MCP tool calls only.
- **No filesystem scavenger hunt** for data-path setup.
- **No credential inventing.** Gateway injects auth.
- **Read-only.** No mutating tools unless the user asks.
- **Be economical.** Prefer MCP `kb_get dingpan/tables` → `tableSchemas` →
  `executeQuery`. Skip `listTables` when the target table is already known.

## Known Pitfalls

- Upstream tool *descriptions* are copy-pasted wrong (list vs schema vs SQL).
  Follow **name + inputSchema**, not the prose.
- Calling `listBusinessTables` / `getTableSchemas` returns `Unknown tool`
  immediately. Use `listTables` / `tableSchemas`.
- Heavy `listTables` / `executeQuery` can be slow on upstream; wait for the
  result; raise `settings.requestTimeoutMs` in mcp.json if needed.
- `tableSchemas.tableNames` must include schema prefix.
- If `kb_get dingpan/tables` is still a stub and `/agents/data/dingpan/README.md`
  is missing, say so; do not invent tables. Use `listTables` to discover.
