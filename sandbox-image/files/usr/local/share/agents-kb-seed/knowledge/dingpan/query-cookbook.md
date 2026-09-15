---
id: dingpan/query-cookbook
title: Dingpan query cookbook
description: Known-good read-only SELECT patterns for dingpan MCP executeQuery — filters, date windows, aggregations, context-safe peeks
tags: [dingpan, sql, cookbook]
---

# Dingpan query cookbook

Patterns for `executeQuery`. Always:

- Read-only `SELECT`.
- Explicit columns (no `SELECT *` on large tables).
- Filters that bound time / factory / code / id before aggregating.
- Confirm column names with `tableSchemas` (or `dingpan/tables`) first.
- **Keep each tool result small.** Prefer `LIMIT 20` peeks. Do not lean on the
  server auto-`LIMIT 10000` for wide fact tables — that reply alone can burn
  most of a 1M context window.
- **Land reusable / >20-row results with `output`**
  (`file:///workspace/data/<name>.csv`) on the same `executeQuery` call. The
  bridge writes CSV; chat keeps only path + rowCount + columns + ≤3 sample
  rows. Do not keep JSON object-array dumps as the working copy.

If you need a true total, `COUNT(*)` with the same filters rather than paging
locally through MCP.

## Recipe: count before pull

```sql
SELECT COUNT(*) AS n
FROM digital_operation.ads_energy_storage_pack_monitor_process_item_result_full
WHERE <time_col> >= '<start>'
  AND <time_col> < '<end>';
```

If `n` is large (10^5+), **do not** pull the slice raw. Aggregate or sample.

## Recipe: distribution (factory / month / process)

```sql
SELECT <factory_col> AS factory, COUNT(*) AS n
FROM digital_operation.ads_energy_storage_pack_monitor_process_item_result_full
WHERE <time_col> >= '<start>' AND <time_col> < '<end>'
GROUP BY 1
ORDER BY n DESC
LIMIT 50;
```

## Recipe: schema peek only (≤ 20 rows)

```sql
SELECT <col1>, <col2>, <col3>, <time_col>
FROM digital_operation.ads_energy_storage_pack_monitor_process_item_result_full
WHERE <time_col> >= '<start>' AND <time_col> < '<end>'
ORDER BY <time_col>
LIMIT 20;
```

## Recipe: chart / LTTB without flooding context

Do **not** `executeQuery` hundreds of thousands of points into the chat and
then LTTB in Python on the tool payload.

Prefer one of:

1. **SQL buckets** (time_bucket / date_trunc + avg/min/max) so each series is
   hundreds of points, not hundreds of thousands.
2. **Tight multi-dimensional filters** (one month × one factory × one code ×
   one process) + explicit columns + a modest `LIMIT` if still dense.
3. If you must materialize more points, pass **`output`** on that
   `executeQuery` so rows never enter chat. Never issue parallel wide pulls
   without `output`. Summarize from the file (`rowCount`, min/max, head) —
   do not echo JSON.

## Recipe template (human)

Copy a subsection:

- Heading: short name
- **Question it answers:**
- **Tables:** `digital_operation.<name>`
- **SQL:** a fenced `sql` block
- **Notes:** grain, LIMIT behavior, columns that lie

## TODO (human)

Paste recipes that already worked in production sessions, for example:

- Daily charging energy by VIN
- Fault / DTC occurrence counts in a date range
- Latest pack metric snapshot per vehicle
- Barcode / material trace for one SN
- Pack process item: per-month factory counts (replace placeholder cols after
  one `tableSchemas` call)
