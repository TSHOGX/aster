---
id: dingpan/query-cookbook
title: Dingpan query cookbook
description: Known-good read-only SELECT patterns for dingpan MCP executeQuery — filters, date windows, aggregations
tags: [dingpan, sql, cookbook]
---

# Dingpan query cookbook

Patterns for `executeQuery`. Always:

- Read-only `SELECT`.
- Explicit columns (no `SELECT *` on large tables).
- Filters that bound time / VIN / id before aggregating.
- Confirm column names with `tableSchemas` (or `dingpan/tables`) first.

The server may auto-`LIMIT 10000`. If you need a true total, `COUNT(*)` with
the same filters rather than paging locally.

## Recipe template (human)

Copy a subsection:

- Heading: short name
- **Question it answers:**
- **Tables:** `digital_operation.<name>`
- **SQL:** a fenced `sql` block
- **Notes:** grain, LIMIT behavior, columns that lie

## Recipes

<!-- TODO: add real recipes from successful sessions. -->

### Count rows in a bounded window (placeholder)

- **Question it answers:** is this table usable, and roughly how big is the slice?
- **Tables:** _replace with a real table from `dingpan/tables`_
- **SQL:**

```sql
-- TODO: replace table and time column
SELECT COUNT(*) AS n
FROM digital_operation.<table>
WHERE <time_col> >= '<start>'
  AND <time_col> < '<end>'
```

- **Notes:** run this before a wide pull. If `n` is huge, tighten filters.

## TODO (human)

Paste recipes that already worked in production sessions, for example:

- Daily charging energy by VIN
- Fault / DTC occurrence counts in a date range
- Latest pack metric snapshot per vehicle
- Barcode / material trace for one SN
