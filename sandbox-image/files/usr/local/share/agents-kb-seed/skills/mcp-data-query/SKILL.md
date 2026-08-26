---
name: mcp-data-query
status: in-use
support_count: 999
confidence: 1.0
source: human-seeded (dingpan MCP-first data path, 2026-08-26)
---

# Dingpan MCP data query (default)

## When To Use

**This is the default data source for every measured-data / fleet / warehouse
question.** Statistics, reports, trends, aggregations, signal lookups, charging
analysis, and similar work must go through the **dingpan MCP server** tools
exposed in this session — server name **`dp-dingpan-mcp-server`** (via the host
MCP gateway at `http://harness-mcp.internal:8103/`).

Do **not** open `skills/doris-query` or `skills/obs-parquet-duckdb` unless the
user explicitly orders a direct Doris SQL or OBS/Parquet path.

Specification / DTC / CAN decode questions still use
`skills/battery-domain-reference`; after you decode a code or signal, come back
here and query occurrences via **dingpan MCP tools**.

## Context

- Preferred MCP server: **`dp-dingpan-mcp-server`** (dingpan / 云龙数据平台).
  Transport is typically streamable-http to `http://harness-mcp.internal:8103/`.
  Real tokens stay on the host gateway; never invent or hard-code upstream
  credentials.
- If other MCP servers appear in the session, still **prefer dingpan** for data
  queries unless the user names a different server.
- Tool names and argument schemas come from the live dingpan server —
  **discover them each session**; do not assume a fixed tool list from memory.
- Prefer calling a dingpan MCP tool over writing `mysql` / `pymysql` /
  DuckDB-against-OBS clients. Those paths are legacy escape hatches, not the
  default.

## Procedure

1. **Target dingpan.** Confirm `dp-dingpan-mcp-server` (or the dingpan-named
   server) is available in this session’s MCP config.
2. **List dingpan tools.** Inspect tool schemas from that server before the
   first call. Note each tool’s name, required args, and what it returns.
3. **Pick the smallest fitting dingpan tool.** Prefer a purpose-built
   query/search tool over a generic “run arbitrary SQL” tool when both exist.
   Prefer filtered / paginated calls over unbounded dumps.
4. **Call the dingpan tool** with explicit arguments. If a call fails, report
   the exact MCP / upstream error; do not silently retry via Doris or OBS.
5. **Ground the answer** in dingpan tool results. Cite which tool and key
   filters you used. Turn tabular results into concise tables/charts in
   `/workspace` when useful (matplotlib CJK: `skills/matplotlib-cjk-fonts`).
6. **If dingpan MCP cannot answer**, say so clearly and ask the user how to
   proceed. Only if they explicitly request Doris direct access or OBS/Parquet
   should you open those skills.

## Hard rules

- **Dingpan MCP first.** Start every data question with
  `dp-dingpan-mcp-server` tools. Do not skip MCP to “try Doris first”.
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

- Long dingpan `tools/call` latencies are common; wait for the tool result
  rather than abandoning MCP for Doris mid-turn.
- If dingpan tools are missing or the server is unreachable, report that as an
  environment/config issue (`dp-dingpan-mcp-server` /
  `harness-mcp.internal:8103` / gateway / session `mcp.json`) — do not treat it
  as “fall back to Doris”.
- Tool schemas change; rediscover after reconnect rather than reusing stale
  argument shapes from a previous session.
