---
name: mcp-data-query
status: in-use
support_count: 999
confidence: 1.0
source: human-seeded (dingpan MCP-first + executeQuery output=file, 2026-09-02)
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
| `mcp_dp_dingpan_mcp_server_executeQuery` | `executeQuery` | `sql` (required); `output` (optional): `file:///workspace/data/<name>.csv` |

If your tool list shows a different `mcp_*_listTables`, use **that** name. Do
not reconstruct names from mcp.json. Do not try both prefixes "to see which
works".

Specification / DTC / CAN decode still uses
`skills/battery-domain-reference`; then come back here for occurrences.

## Context budget (read this before every query)

**Without `output`, the `executeQuery` payload is injected into the model
context in full.** UI hints like `[Old tool result hard-truncated]` do **not**
undo that cost. Pass `output` on the **same** call so the bridge lands CSV
before the model ever sees the rows.

### Result landing — `output` on executeQuery (required for non-peeks)

For any result that is **more than a tiny peek** (rule of thumb: **> 20 rows**,
or any detail/series pull you will reuse):

```
executeQuery({
  sql: "SELECT …",
  output: "file:///workspace/data/<short_name>.csv"
})
```

The tool-bridge writes the CSV under `/workspace` and returns only **path**,
**rowCount**, **cols**, and ≤3 **sample** rows. Do not echo the upstream JSON.
Do not re-write the file in Python after the fact — that is too late; the
rows would already be in context.

Later analysis / LTTB / HTML must `pandas.read_csv(path)` (or equivalent) and
print **summaries** (`shape`, `head`, dtypes, quantiles) — never `cat` the
whole file back into context.

If you omit `output` and the reply is still oversized, the bridge **auto-lands**
to `/workspace/data/query_<timestamp>.csv` and returns the same summary.
Still pass `output` yourself so the path is predictable.

### Hard limits on what you fetch

1. **Discovery is optional.** If the user (or `dingpan/tables`) already names
   the table, **skip** `listTables`. Call `tableSchemas` **once** for that
   single `schema.table`. Never re-fetch the same DDL.
2. **Do not load extra knowledge bodies.** Prefer `kb_catalog` summaries; only
   `kb_get` the one id you need. Skip `overview` / `tools` / `pitfalls` unless
   stuck.
3. **Peek ≤ 20 rows** for schema/shape checks (`LIMIT 20` + explicit columns).
   Never rely on the server's ~10000 default for a wide fact table.
4. **Shape first:** `COUNT(*)` / time bounds → `GROUP BY` distributions → peeks
   → only then a bounded detail pull **with `output`**.
5. **Prefer SQL-side aggregates / buckets** for charts. If you still need a
   series, keep filters tight (e.g. one month × one factory × one code × one
   process) and pass `output`.
6. **Never paste giant JSON into assistant text.**

Peeks (`COUNT`, `GROUP BY`, `LIMIT ≤ 20`) may omit `output` so you can read
the numbers inline. Everything else uses `output`.

## Context

- Pi is already connected. Real tokens stay on the host. Never invent
  credentials. Never `curl` / `urllib` the gateway.
- **Table map:** MCP `mcp_dingpan_knowledge_kb_catalog` then
  `mcp_dingpan_knowledge_kb_get` with id `dingpan/tables` (see
  `skills/dingpan-knowledge`). Prefer the article over a full remote list when
  it already names the table.
- Upstream *descriptions* still say `listBusinessTables` / `getTableSchemas`
  and are swapped; **those names are not tools** (`Unknown tool`).
- `output` is a **Pi-side** argument. The bridge strips it before the upstream
  `tools/call`. Paths must stay under `/workspace`.

## Procedure (short path)

1. If the target table is already known → jump to step 3. Else
   `kb_catalog` + **one** `kb_get` (`dingpan/tables`).
2. Only call `listTables` if the article does not cover the table.
3. `tableSchemas` once with `tableNames` = one `schema.table`.
4. `executeQuery`: `COUNT` / distribution / `LIMIT 20` peek (no `output`).
5. For any reusable or >20-row result: `executeQuery` with **`output`** set to
   `file:///workspace/data/<name>.csv`; reply with the summary fields only;
   analyze from the file.
6. Charts via `skills/matplotlib-cjk-fonts` when needed. If tools are missing,
   **stop** (no DIY MCP client).

## Hard rules

- **Native MCP tool calls only.** Never invent `listBusinessTables` /
  `getTableSchemas`.
- **No DIY MCP client** (`/workspace/mcp_client.py`, HTTP, SSE) and **no kb CLI**.
- **No filesystem scavenger hunt.**
- **Read-only.** No mutating SQL unless the user asks.
- **Be economical.** Skip `listTables` when the target table is already known.
- **Land non-trivial results with `output`** (CSV under `/workspace`).
- **No 10k-row chat dumps.** Wide tables → aggregate, small peeks, or bounded
  pulls with `output`.

## Known Pitfalls

- A session that `find`s mcp.json then `kb get` then pipes JSON-RPC to `kb mcp`
  has already failed: the first turn should have been
  `mcp_dingpan_knowledge_kb_catalog`.
- `mcp_dp_dingpan_listTables` is **not** the name on this host. Use
  `mcp_dp_dingpan_mcp_server_listTables` (or whatever is actually listed).
- Upstream descriptions are swapped. Follow name + inputSchema.
- `tableSchemas.tableNames` must include the schema prefix.
- Calling `executeQuery` without `output` and then writing CSV in Python does
  **not** undo the tokens already spent on that tool result. Pass `output` on
  the query call.
