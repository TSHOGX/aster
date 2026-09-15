# `knowledge/` — progressive-disclosure articles

Plain-text Markdown knowledge base, modeled on the DeepSeek Harness
`dsh-knowledge` example: **catalog of summaries, full body loaded on demand**.
No embeddings, no FTS index, no encryption.

Recall is an **MCP server** (`kb_catalog` / `kb_get`), the same kind of tool
surface as dingpan. Orchestrator is not involved.

## Where to put documents

**Edit and add articles here in the repo:**

```
sandbox-image/files/usr/local/share/agents-kb-seed/knowledge/
```

At runtime this tree is mounted read-only at **`/agents/knowledge/`**.

| Put this… | Here |
|---|---|
| Dingpan MCP how-to, tool semantics, table meanings, query recipes | `knowledge/dingpan/*.md` (this tree; committed) |
| Bulky private dumps (full DDL exports, sample extracts) | `data/dingpan/` (gitignored; provision on the host snapshot) |

Nested directories become ids: `knowledge/dingpan/tables.md` → id `dingpan/tables`.

After editing Markdown, reseed the host KB snapshot (existing `orchestrator kb
reseed`) so new sessions see it. No orchestrator code change, no image rebuild
for article text.

## Article format

Every `.md` file that should be recallable needs YAML frontmatter. `description`
is required — it is what the model sees in the catalog and uses to decide
whether to load the article.

```markdown
---
id: dingpan/tables
title: Dingpan table map
description: GaussDB digital_operation tables used via dingpan MCP
tags: [dingpan, tables, schema]
---

# Dingpan table map
…
```

| Field | Required | Meaning |
|---|---|---|
| `description` | yes | Catalog summary. Keep it specific so the model knows when to load. |
| `id` | no | Loader id. Default: path under this root without `.md`. |
| `title` | no | Shown after load, inside `<knowledge_content>`. |
| `tags` | no | Listed next to the catalog description. |

Ids are lowercase path-style (`a-z`, `0-9`, `/`, `-`). Files without usable
frontmatter (including this README) are skipped.

## Recall: MCP server (preferred)

Add this next to dingpan in the sandbox `mcp.json`
(`/agent-home/.pi/agent/mcp.json`):

```json
{
  "mcpServers": {
    "dp-dingpan-mcp-server": {
      "transport": "streamable-http",
      "url": "http://harness-mcp.internal:8103/",
      "lifecycle": "eager"
    },
    "dingpan-knowledge": {
      "transport": "stdio",
      "command": "/usr/local/bin/kb",
      "args": ["mcp"],
      "lifecycle": "eager"
    }
  }
}
```

Copy-paste snippet:
`/usr/local/share/harness/dingpan-knowledge-mcp.example.json`.

Pi then exposes:

- `mcp_dingpan_knowledge_kb_catalog` — id + description catalog (no bodies)
- `mcp_dingpan_knowledge_kb_get` — load one article by exact id

The stdio server reads `/agents/knowledge` inside the sandbox. No extra host
process, no extra data-proxy, no orchestrator change.

The `kb` binary needs to be in the sandbox image (one rootfs rebuild the first
time). After that, only `mcp.json` wiring is required.

Operator debug (optional, **not** what the agent should use). The `kb`
catalog/get/path subcommands refuse unless `KB_ALLOW_CLI=1`:

```bash
KB_ALLOW_CLI=1 kb catalog
KB_ALLOW_CLI=1 kb get dingpan/tables
kb mcp    # stdio MCP on stdin/stdout (Pi uses this; agents must not)
```
