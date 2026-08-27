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
`mcp_dp_dingpan_listBusinessTables`).

Specification / DTC / CAN decode questions still use
`skills/battery-domain-reference`; after you decode a code or signal, come back
here and query occurrences via **dingpan MCP tools**.

## Context

- Transport: streamable-http → `http://harness-mcp.internal:8103/`. Real tokens
  stay on the host gateway; never invent credentials.
- **Table map:** read **once** `/agents/data/dingpan/README.md` for known
  GaussDB / `digital_operation` tables. Prefer it over a full remote table list
  when the README already names the table.
- **Supported dingpan tools only** (do not invent other names):

  | Tool | Purpose |
  |---|---|
  | `listBusinessTables` | Tables this token can access (name, biz desc, source) |
  | `getTableSchemas` | DDL for one or more tables; names need `schema.table`; comma-separated for batch |
  | `executeQuery` | Read-only `SELECT`; server may auto `LIMIT 10000`; JSON with `rowCount` + `data` |

  **Not supported / do not call:** `listTables`, `tableSchemas`, or any other
  guessed name. Those are wrong tools and often hang until client timeout.

## Procedure (short path)

1. **Read once:** `/agents/data/dingpan/README.md`. No `find /`, no hunting
   `mcp.json`.
2. **Pick tables** from the README when possible. Only call
   `listBusinessTables` if the README does not cover the question.
3. **`getTableSchemas`** with `schema.table` (e.g.
   `digital_operation.dwd_e2e_trace_barcode_material`). Batch with commas if
   needed.
4. **`executeQuery`** with a narrow `SELECT` (filters + explicit columns). Do
   not `SELECT *` on huge tables.
5. If dingpan tools are missing from the tool list, stop and report wiring
   failure — do not bash/`curl` the gateway, do not fall back to Doris/OBS
   unless the user asks.
6. Ground the answer in tool results; charts via `skills/matplotlib-cjk-fonts`.

## Hard rules

- **Only the three tools above.** Never call `listTables` / `tableSchemas`.
- **Dingpan MCP first.** Native MCP tool calls only.
- **No filesystem scavenger hunt** for data-path setup.
- **No credential inventing.** Gateway injects auth.
- **Read-only.** No mutating tools unless the user asks.
- **Be economical.** Prefer README → `getTableSchemas` → `executeQuery`. Skip
  `listBusinessTables` when the target table is already known.

## Known Pitfalls

- Calling `listTables` (wrong name) wastes a full client timeout (30s/300s) and
  returns `-32001`. Use `listBusinessTables` instead — or skip it.
- `listBusinessTables` / heavy `executeQuery` can be slow on upstream; wait for
  the result; raise `settings.requestTimeoutMs` in mcp.json if needed.
- Table names for `getTableSchemas` must include schema prefix.
- If `/agents/data/dingpan/README.md` is missing, say so; do not invent tables.
