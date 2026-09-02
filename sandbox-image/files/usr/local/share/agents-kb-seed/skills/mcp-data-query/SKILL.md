---
name: mcp-data-query
status: in-use
support_count: 999
confidence: 1.0
source: human-seeded (dingpan MCP-first + CSV land, 2026-09-02)
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

## Context budget (read this before every query)

**Every MCP `executeQuery` payload is injected into the model context in full.**
UI hints like `[Old tool result hard-truncated]` do **not** undo that cost for
the turn that received the rows. Tens of thousands of JSON objects will blow a
1M window instantly. Treat every tool result as expensive.

### Result landing — CSV in `/workspace` (required)

For any result that is **more than a tiny peek** (rule of thumb: **> 20 rows**,
or any detail/series pull you will reuse):

1. **Immediately** write the rows to a **CSV** under `/workspace` (prefer
   `/workspace/data/<short_name>.csv`). **CSV only** — not JSON object arrays,
   not pretty-printed tool dumps.
2. In the assistant reply, keep only: **path**, **column names**, **row count**,
   and at most **3 sample rows**. Never echo the full tool JSON.
3. Later analysis / LTTB / HTML must `pandas.read_csv(path)` (or equivalent) and
   print **summaries** (`shape`, `head`, dtypes, quantiles) — never `cat` the
   whole file back into context.

Minimal land pattern (adapt to the actual `executeQuery` JSON shape):

```python
import csv, json, pathlib
# `rows` = list[dict] parsed from the tool result (do not keep the raw string)
path = pathlib.Path("/workspace/data/query_slice.csv")
path.parent.mkdir(parents=True, exist_ok=True)
cols = list(rows[0].keys()) if rows else []
with path.open("w", newline="", encoding="utf-8") as f:
    w = csv.DictWriter(f, fieldnames=cols, extrasaction="ignore")
    w.writeheader()
    w.writerows(rows)
print({"path": str(path), "rows": len(rows), "cols": cols})
```

**Honesty:** writing CSV does **not** erase tokens already spent on that MCP
reply. It preserves data on disk so later turns / truncation / compaction can
drop the fat tool payload while you still have the file. Therefore you must
**also** keep each MCP reply small (below).

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
   → only then a bounded detail pull, **landed as CSV**.
5. **Prefer SQL-side aggregates / buckets** for charts. If you still need a
   series, keep filters tight (e.g. one month × one factory × one code × one
   process) and land CSV immediately.
6. **Never paste giant JSON into assistant text.**

Rough guide: aim for each `executeQuery` tool result well under a few thousand
tokens. If a result looks huge, tighten SQL next — do not issue sibling wide
queries.

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

1. If the target table is already known → jump to step 3. Else
   `kb_catalog` + **one** `kb_get` (`dingpan/tables`).
2. Only call `listTables` if the article does not cover the table.
3. `tableSchemas` once with `tableNames` = one `schema.table`.
4. `executeQuery`: `COUNT` / distribution / `LIMIT 20` peek.
5. For any reusable or >20-row result: **land CSV under `/workspace/data/`**,
   reply with path + rowCount + cols only; analyze from the file.
6. Charts via `skills/matplotlib-cjk-fonts` when needed. If tools are missing,
   **stop** (no DIY MCP client).

## Hard rules

- **Native MCP tool calls only.** Never invent `listBusinessTables` /
  `getTableSchemas`.
- **No DIY MCP client** and **no kb CLI**.
- **No filesystem scavenger hunt.**
- **Read-only.** No mutating SQL unless the user asks.
- **Be economical.** Skip `listTables` when the target table is already known.
- **Land non-trivial results as CSV** in `/workspace` (not JSON dumps).
- **No 10k-row chat dumps.** Wide tables → aggregate, small peeks, or bounded
  pulls landed to CSV.

## Known Pitfalls

- A session that `find`s mcp.json then `kb get` then pipes JSON-RPC to `kb mcp`
  has already failed: the first turn should have been
  `mcp_dingpan_knowledge_kb_catalog`.
- `mcp_dp_dingpan_listTables` is **not** the name on this host. Use
  `mcp_dp_dingpan_mcp_server_listTables` (or whatever is actually listed).
- Upstream descriptions are swapped. Follow name + inputSchema.
- `tableSchemas.tableNames` must include the schema prefix.
- Leaving rows only as MCP JSON object arrays in the transcript is how a short
  chat exceeds 1M tokens. CSV on disk + tiny chat summary is the durable path;
  it does not make an oversized MCP reply free.
