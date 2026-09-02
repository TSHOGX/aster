---
id: dingpan/tables
title: Dingpan table map
description: GaussDB digital_operation tables used via dingpan MCP — names, meaning, grain, key columns, and when to use each
tags: [dingpan, tables, schema]
---

# Dingpan table map

This is the **first place** to pick a table. Prefer it over a full remote
`listTables` when the question already matches a row below.

Schema prefix is required for `tableSchemas` (`schema.table`). The warehouse
schema used here is `digital_operation` unless a row says otherwise.

Bulky extras (full DDL dumps, sample extracts) belong in
`/agents/data/dingpan/` — gitignored, provisioned on the host. Point at them
from this article; do not paste megabytes of DDL here.

After you know the table name from this map (or the user), call
`tableSchemas` **once** for that table only, then follow
`skills/mcp-data-query` (`COUNT` / `GROUP BY` / `LIMIT ≤ 20` peeks; pass
`output` on any larger `executeQuery`). Do not re-list all tables.

## How to add a table (human)

Copy the template. One subsection per table. Keep the catalog `description`
above in sync if you add a whole new domain.

### Template

```markdown
## digital_operation.<table_name>

- **Meaning (业务):**
- **Grain:** one row per …
- **Key columns:**
- **Useful filters:**
- **When to use:**
- **Caveats:**
```

## Tables

## digital_operation.ads_energy_storage_pack_monitor_process_item_result_full

- **Meaning (业务):** 储能 pack 制程 / 工序监控明细结果（全量项次）。
- **Grain:** one row per process-item result (high volume — hundreds of
  thousands to millions of rows).
- **Key columns:** confirm with `tableSchemas` (expect time, factory, product /
  barcode / code, process / station, measurement fields). Do not assume names
  from this stub alone.
- **Useful filters:** month or time window × factory × code × process — always
  bound before any detail `SELECT`.
- **When to use:** pack manufacturing / process monitoring trends, per-factory
  comparisons, LTTB / HTML process charts.
- **Caveats:** **Context bomb.** Never `SELECT *` or rely on the server
  auto-`LIMIT 10000` for this table. Sequence: `COUNT(*)` → distribution
  `GROUP BY` → `LIMIT 20` peek → SQL-side aggregates, or a bounded pull with
  `output: file:///workspace/data/<name>.csv`. Pulling raw month×factory
  slices through MCP into chat (without `output`) will exceed a 1M context
  window quickly.

## digital_operation.dwd_e2e_trace_barcode_material

- **Meaning (业务):** end-to-end barcode / material trace (example name from the
  skill; confirm against live `tableSchemas` before relying on columns).
- **Grain:** _TODO_
- **Key columns:** _TODO_
- **Useful filters:** _TODO_
- **When to use:** material / barcode trace questions.
- **Caveats:** confirm column names with `tableSchemas` before `executeQuery`.

## TODO (human)

Add every table the agent should know about. Suggested groups:

- Fleet / vehicle / VIN identity
- Charging sessions and energy
- Battery / cell measurements
- Fault / DTC occurrences
- Material / barcode / manufacturing trace
- Time-series or snapshot fact tables vs dimensions

For each, fill meaning, grain, keys, filters, and caveats. If a table is
dangerous (huge, PII, misleading grain), say so here.
