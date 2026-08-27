---
name: dingpan-knowledge
status: in-use
support_count: 999
confidence: 1.0
source: human-seeded (dingpan MCP knowledge catalog, 2026-08-27)
---

# Dingpan MCP knowledge (catalog + kb get)

## When To Use

Before any dingpan / fleet / warehouse question, load the matching knowledge
article(s) from the progressive-disclosure catalog. This is how you learn
tool names, table meanings, and query recipes **without** dumping every
Markdown file into context.

Pair with `skills/mcp-data-query` for the live MCP calls.

## Procedure

1. **Catalog (summaries only):** `kb catalog`
   Default root is `/agents/knowledge`. You should see ids such as
   `dingpan/overview`, `dingpan/tools`, `dingpan/tables`,
   `dingpan/query-cookbook`, `dingpan/pitfalls`.
2. **Match** the user question to one or more catalog **descriptions**. Do not
   invent ids. Do not `cat` the whole `knowledge/` tree.
3. **Load:** `kb get <id>` for each matching id. Answer from the loaded
   `<knowledge_body>`, then call dingpan MCP tools as `skills/mcp-data-query`
   describes.
4. Typical loads:
   - first contact / wiring → `dingpan/overview` + `dingpan/tools`
   - pick a table → `dingpan/tables`
   - write SQL → `dingpan/query-cookbook` after schemas
   - errors / hangs → `dingpan/pitfalls`

## Hard rules

- Catalog is summaries only. Never pretend you have read a body you did not
  `kb get`.
- If `kb` is missing or `/agents/knowledge` is empty, say so; fall back to
  `skills/mcp-data-query` and `listBusinessTables`, do not invent tables.
- Human-authored articles live in the seed tree
  `sandbox-image/files/usr/local/share/agents-kb-seed/knowledge/dingpan/`
  (runtime: `/agents/knowledge/dingpan/`).
