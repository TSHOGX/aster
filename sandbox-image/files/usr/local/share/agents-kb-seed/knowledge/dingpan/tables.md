---
id: dingpan/tables
title: Dingpan table map
description: GaussDB digital_operation tables used via dingpan MCP — names, meaning, grain, key columns, and when to use each
tags: [dingpan, tables, schema]
---

# Dingpan table map

This is the **first place** to pick a table. Prefer it over a full remote
`listBusinessTables` when the question already matches a row below.

Schema prefix is required for `getTableSchemas` (`schema.table`). The warehouse
schema used here is `digital_operation` unless a row says otherwise.

Bulky extras (full DDL dumps, sample extracts) belong in
`/agents/data/dingpan/` — gitignored, provisioned on the host. Point at them
from this article; do not paste megabytes of DDL here.

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

<!-- TODO: replace the example and add the real map. -->

## digital_operation.dwd_e2e_trace_barcode_material

- **Meaning (业务):** end-to-end barcode / material trace (example name from the
  skill; confirm against live `getTableSchemas` before relying on columns).
- **Grain:** _TODO_
- **Key columns:** _TODO_
- **Useful filters:** _TODO_
- **When to use:** _TODO_
- **Caveats:** confirm column names with `getTableSchemas` before `executeQuery`.

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
