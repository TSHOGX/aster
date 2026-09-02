---
name: dingpan-knowledge
status: in-use
support_count: 999
confidence: 1.0
source: human-seeded (dingpan MCP knowledge catalog, 2026-09-02 context-budget)
---

# Dingpan MCP knowledge (catalog + kb_get)

## When To Use

Before dingpan / fleet / warehouse work, load **at most one** matching
knowledge article from the progressive-disclosure catalog. This avoids dumping
every Markdown file into context.

Pair with `skills/mcp-data-query` for live dingpan MCP calls (and its
**context budget** rules).

## Procedure

1. **If the user already named the table** and you know the MCP tool names →
   skip catalog/`kb_get` and go straight to `skills/mcp-data-query`
   (`tableSchemas` once → small SQL).
2. Else **catalog (summaries only):** `mcp_dingpan_knowledge_kb_catalog`.
3. **Load one id only** via `mcp_dingpan_knowledge_kb_get`:
   - pick a table → `dingpan/tables`
   - SQL / downsample patterns → `dingpan/query-cookbook`
   - errors / hangs → `dingpan/pitfalls`
   - wiring confusion → `dingpan/tools` (avoid overview+tools+tables together)
4. Do not invent ids. Do not `cat` the whole `knowledge/` tree. Do not bash
   `kb` / `kb get` / `echo | kb mcp`.

## Hard rules

- Catalog is summaries only. Never pretend you have read a body you did not
  `kb_get`.
- Prefer **one** `kb_get` per task phase. Do not preload overview+tools+tables
  "just in case".
- If `mcp_dingpan_knowledge_kb_catalog` is missing from the tool list, say so
  (mcp.json not wired). Fall back to `skills/mcp-data-query` and native MCP
  `mcp_dp_dingpan_mcp_server_listTables`; do not invent tables, do not HTTP
  the gateway, do not bash `kb`.
- Human-authored articles live in the seed tree
  `sandbox-image/files/usr/local/share/agents-kb-seed/knowledge/dingpan/`
  (runtime: `/agents/knowledge/dingpan/`).
