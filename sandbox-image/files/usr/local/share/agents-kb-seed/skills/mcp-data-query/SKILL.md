---
name: mcp-data-query
status: in-use
support_count: 999
confidence: 1.0
source: human-seeded (MCP-first data path, 2026-08-26)
---

# MCP data query (default)

## When To Use

**This is the default data source for every measured-data / fleet / warehouse
question.** Statistics, reports, trends, aggregations, signal lookups, charging
analysis, and similar work must go through **MCP tools** exposed to this session
(via the host MCP gateway → upstream data platform). Do **not** open
`skills/doris-query` or `skills/obs-parquet-duckdb` unless the user explicitly
orders a direct Doris SQL or OBS/Parquet path.

Specification / DTC / CAN decode questions still use
`skills/battery-domain-reference`; after you decode a code or signal, come back
here and query occurrences via MCP tools.

## Context

- MCP servers are configured for the session (typically streamable-http to
  `http://harness-mcp.internal:8103/`). Real tokens stay on the host gateway;
  you never invent or hard-code upstream credentials.
- Tool names and argument schemas come from the live MCP server — **discover
  them each session**; do not assume a fixed tool list from memory.
- Prefer calling an MCP tool over writing `mysql` / `pymysql` / DuckDB-against-OBS
  clients. Those paths are legacy escape hatches, not the default.

## Procedure

1. **List tools.** Use the session’s MCP tooling (e.g. list tools / inspect
   schemas) before the first call. Note each tool’s name, required args, and
   what it returns.
2. **Pick the smallest fitting tool.** Prefer a purpose-built query/search tool
   over a generic “run arbitrary SQL” tool when both exist. Prefer filtered /
   paginated calls over unbounded dumps.
3. **Call the tool** with explicit arguments. If a call fails, report the exact
   MCP / upstream error; do not silently retry via Doris or OBS.
4. **Ground the answer** in tool results. Cite which tool and key filters you
   used. Turn tabular results into concise tables/charts in `/workspace` when
   useful (matplotlib CJK: `skills/matplotlib-cjk-fonts`).
5. **If MCP cannot answer**, say so clearly and ask the user how to proceed.
   Only if they explicitly request Doris direct access or OBS/Parquet should you
   open those skills.

## Hard rules

- **No Doris by default.** Do not connect with `mysql`/`pymysql` to Doris FE,
  and do not follow `skills/doris-query`, unless the user explicitly asks for
  direct Doris SQL.
- **No OBS by default.** Do not open `skills/obs-parquet-duckdb` unless the user
  explicitly names OBS / object storage / Parquet / raw signal files.
- **No credential inventing.** Never put real tokens in workspace files or in
  shell history for the upstream MCP; the gateway injects auth.
- **Read-only posture.** Do not call MCP tools that mutate external systems
  unless the user explicitly asks.
- **Be economical.** Prefer narrow filters and limits; stream or page large
  results; do not materialize huge payloads in RAM.

## Known Pitfalls

- Long MCP `tools/call` latencies are common; wait for the tool result rather
  than abandoning MCP for Doris mid-turn.
- If tools are missing or the MCP server is unreachable, report that as an
  environment/config issue (`harness-mcp.internal` / gateway / session
  `mcp.json`) — do not treat it as “fall back to Doris”.
- Tool schemas change; rediscover after reconnect rather than reusing stale
  argument shapes from a previous session.
