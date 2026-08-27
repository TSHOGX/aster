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
analysis, and similar work must go through the **dingpan MCP tools already in
this session's tool list**.

Look for names like `mcp_dp_dingpan_listTables` (prefix comes from the mcp.json
server id, often `dp-dingpan` or `dp-dingpan-mcp-server`). **Call those tools.**
Do not open a URL. Do not write a client.

Specification / DTC / CAN decode questions still use
`skills/battery-domain-reference`; after you decode a code or signal, come back
here and query occurrences via **dingpan MCP tools**.

## Context

- Pi is already connected to dingpan over streamable-http. Real tokens stay on
  the **host** gateway. Never invent credentials. Never `curl` / `urllib` the
  gateway. Never put secrets in files.
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
5. If dingpan tools are missing from the tool list, **stop and report wiring
   failure**. Do not bash/`curl`/`urllib` the gateway, do not write
   `/workspace/mcp_client.py`, do not probe `/mcp` `/api` `/v1`, do not parse
   SSE, do not `find` tokens under `/agent-home`. Do not fall back to Doris/OBS
   unless the user asks.
6. Ground the answer in tool results; charts via `skills/matplotlib-cjk-fonts`.

## Hard rules

- **Only the three tools above, via native MCP calls.** Never invent
  `listBusinessTables` / `getTableSchemas`.
- **No DIY MCP client.** Seeing SSE (`event: message`) or a gateway hostname
  is not a prompt to reimplement JSON-RPC.
- **No filesystem scavenger hunt** for data-path setup or auth.
- **No credential inventing.** The host injects auth.
- **Read-only.** No mutating tools unless the user asks.
- **Be economical.** Prefer MCP `kb_get dingpan/tables` → `tableSchemas` →
  `executeQuery`. Skip `listTables` when the target table is already known.

## Known Pitfalls

- Upstream tool *descriptions* are copy-pasted wrong (list vs schema vs SQL).
  Follow **name + inputSchema**, not the prose.
- Calling `listBusinessTables` / `getTableSchemas` returns `Unknown tool`
  immediately. Use `listTables` / `tableSchemas`.
- Heavy `listTables` / `executeQuery` can be slow on upstream; wait for the
  result; raise `settings.requestTimeoutMs` in mcp.json if needed (host
  operator, not you).
- `tableSchemas.tableNames` must include schema prefix.
- If `kb_get dingpan/tables` is still a stub and `/agents/data/dingpan/README.md`
  is missing, say so; do not invent tables. Use `listTables` to discover.
- A long bash session that "finally figures out tools/call" is a failed
  session: you should have called `mcp_*_listTables` on the first turn.
