---
id: dingpan/overview
title: Dingpan MCP overview
description: What the dingpan MCP server is, how the sandbox reaches it, and server names to use
tags: [dingpan, mcp, overview]
---

# Dingpan MCP overview

Dingpan is the **default live-data path** on this platform. Statistics, reports,
trends, aggregations, charging analysis, and signal lookups go through the
dingpan MCP tools — not Doris SQL and not OBS Parquet, unless the user
explicitly asks for those.

## How the sandbox reaches it

- Transport: streamable-http → `http://harness-mcp.internal:8103/`.
- Real tokens stay on the **host MCP gateway**. Never invent credentials, never
  `curl` the gateway, never put secrets in `mcp.json`.
- Server name in `mcp.json` is typically `dp-dingpan` or `dp-dingpan-mcp-server`.
- Pi tool names are prefixed: `mcp_<server>_<tool>` (example:
  `mcp_dp_dingpan_listTables`).

## What this knowledge base covers

| id | Load when… |
|---|---|
| `dingpan/tools` | You need the exact tool names and parameters |
| `dingpan/tables` | You need to pick a GaussDB / `digital_operation` table |
| `dingpan/query-cookbook` | You need a known-good SELECT pattern |
| `dingpan/pitfalls` | A call hung, timed out, or returned an unexpected error |

Load those with MCP `kb_get <id>`. Then call dingpan MCP tools. Do not fall back to
Doris or OBS because dingpan looked empty — say so and ask.

## TODO (human)

- Record the exact `mcp.json` server name used in this deployment.
- Record any non-default timeout (`settings.requestTimeoutMs`) this host needs.
- Note which environments (prod / staging) this gateway points at.
