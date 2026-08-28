---
name: dingpan-knowledge
status: in-use
support_count: 999
confidence: 1.0
source: human-seeded (dingpan MCP knowledge catalog, 2026-08-27)
---

# Dingpan MCP knowledge (catalog + kb_get)

## When To Use

Before any dingpan / fleet / warehouse question, load the matching knowledge
article(s) from the progressive-disclosure catalog. This is how you learn
tool names, table meanings, and query recipes **without** dumping every
Markdown file into context.

Pair with `skills/mcp-data-query` for the live dingpan MCP calls.

## Procedure

1. **Catalog (summaries only):** call `mcp_dingpan_knowledge_kb_catalog`
   (server `dingpan-knowledge`). You should see ids such as `dingpan/overview`,
   `dingpan/tools`, `dingpan/tables`, `dingpan/query-cookbook`,
   `dingpan/pitfalls`.
2. **Match** the user question to one or more catalog **descriptions**. Do not
   invent ids. Do not `cat` the whole `knowledge/` tree. Do not bash `kb` /
   `kb get` / `echo | kb mcp`.
3. **Load:** call `mcp_dingpan_knowledge_kb_get` with that exact `id`. Answer
   from the loaded `<knowledge_body>`, then call dingpan MCP tools as
   `skills/mcp-data-query` describes.
4. Typical loads:
   - first contact / wiring → `dingpan/overview` + `dingpan/tools`
   - pick a table → `dingpan/tables`
   - write SQL → `dingpan/query-cookbook` after schemas
   - errors / hangs → `dingpan/pitfalls`

## Hard rules

- Catalog is summaries only. Never pretend you have read a body you did not
  `kb_get`.
- If `mcp_dingpan_knowledge_kb_catalog` is missing from the tool list, say so
  (mcp.json not wired). Fall back to `skills/mcp-data-query` and native MCP
  `mcp_dp_dingpan_mcp_server_listTables`; do not invent tables, do not HTTP
  the gateway, do not bash `kb`.
- Human-authored articles live in the seed tree
  `sandbox-image/files/usr/local/share/agents-kb-seed/knowledge/dingpan/`
  (runtime: `/agents/knowledge/dingpan/`).
