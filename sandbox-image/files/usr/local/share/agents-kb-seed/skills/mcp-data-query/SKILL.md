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

Specification / DTC / CAN decode questions still use
`skills/battery-domain-reference`; after you decode a code or signal, come back
here and query occurrences via **dingpan MCP tools**.

## Context

- Preferred MCP server: **`dp-dingpan-mcp-server`** (dingpan / 云龙数据平台).
  Transport is typically streamable-http to `http://harness-mcp.internal:8103/`.
  Real tokens stay on the host gateway; never invent or hard-code upstream
  credentials.
- **Table / schema knowledge:** read **only**
  `/agents/data/dingpan/README.md` (and this skill). Do not browse
  `/agents/skills`, `/agents/data`, or the whole filesystem to “find” data
  sources. The README documents GaussDB (Gauss100) tables under schema
  `digital_operation`.
- Dingpan tools appear as **native session tools** (MCP-backed). You call them
  the same way you call `bash` / `read` — by name from your tool list. There is
  **no** shell command that lists or invokes MCP tools.

## Procedure (keep it short — avoid exploration loops)

1. **Read once:** `/agents/data/dingpan/README.md` (and this skill if needed).
   Do **not** `find /`, `ls` trees, or hunt for `mcp.json`.
2. **Use native MCP tools immediately.** From your available tools, pick those
   belonging to `dp-dingpan-mcp-server` (names often include dingpan / dp_ /
   query-like verbs). Call a tool directly with args aligned to the README.
3. **If no dingpan/MCP tools appear in your tool list**, stop. Tell the user
   MCP is not wired into this session (`mcp.json` / pi-mcp / gateway /
   `harness-mcp.internal:8103`). Do **not** simulate discovery with
   `echo`, `curl`, or bash. Do **not** fall back to Doris/OBS unless the user
   explicitly asks.
4. **Ground the answer** in tool results. Cite tool name + key filters. Charts
   in `/workspace` if useful (`skills/matplotlib-cjk-fonts`).

## Hard rules

- **Dingpan MCP first.** Native MCP tool calls only — never “discover MCP”
  via bash.
- **No filesystem scavenger hunt.** Forbidden for data-path setup:
  `find /`, recursive `ls` of `/agents`, searching for `mcp.json`, grepping
  env for MCP. Config is already loaded by the runtime.
- **No fake discovery.** `echo "Discovering MCP…"` is not a tool call.
- **No credential inventing.** Gateway injects auth.
- **Read-only posture.** Do not call mutating MCP tools unless asked.
- **Be economical.** One README read + MCP calls. Prefer filtered/paginated
  queries.

## Known Pitfalls

- Long dingpan `tools/call` latencies are common; wait for the result rather
  than abandoning MCP mid-turn.
- Many bash/`find` steps before any MCP call usually mean tools are missing
  from the session — report wiring failure instead of exploring more.
- Tool schemas change; use the live tool schema from your tool list, not
  memorized shapes.
- If `/agents/data/dingpan/README.md` is missing, say so; do not invent table
  names from memory.
