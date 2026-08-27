---
id: dingpan/pitfalls
title: Dingpan MCP pitfalls
description: Known dingpan MCP failure modes — wrong tool names that hang, schema prefixes, timeouts, missing README
tags: [dingpan, mcp, pitfalls]
---

# Dingpan MCP pitfalls

## Wrong tool names hang

Calling `listTables` or `tableSchemas` (wrong names) wastes a full client
timeout (30s / 300s) and returns `-32001`. Use `listBusinessTables` and
`getTableSchemas`. If a name is not in `dingpan/tools`, do not guess.

## Schema prefix is required

`getTableSchemas` needs `schema.table` (example:
`digital_operation.dwd_e2e_trace_barcode_material`). Bare table names fail or
hang. Batch with commas only after each name is fully qualified.

## Heavy calls are slow, not broken

`listBusinessTables` and large `executeQuery` can sit on upstream for a long
time. Wait for the result. If the host needs it, raise
`settings.requestTimeoutMs` in `mcp.json` — do not retry-spam.

## Skip the filesystem scavenger hunt

Do not `find /` for `mcp.json`, credentials, or table maps. Knowledge is the
`dingpan-knowledge` MCP tools (`kb_catalog` / `kb_get`). Live data is dingpan
MCP. If tools are missing from the session list, report a wiring failure.

## Missing table map

If `kb_get dingpan/tables` is a stub and `/agents/data/dingpan/README.md` is
absent, say so. Do not invent table names. `listBusinessTables` is the
fallback for discovery, then `getTableSchemas` before any `executeQuery`.

## No silent fallback

If dingpan cannot answer, say so and ask. Do not silently open Doris SQL or
OBS Parquet unless the user explicitly orders that path.

## TODO (human)

- Record the exact error payloads you have seen (`-32001`, authz denials, empty
  `data`, truncated `LIMIT`).
- Note tables that look queryable but are too large / wrong grain / PII.
