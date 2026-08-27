# `knowledge/` — progressive-disclosure articles

Plain-text Markdown knowledge base, modeled on the DeepSeek Harness
`dsh-knowledge` example: **catalog of summaries in context, full body loaded
on demand**. No embeddings, no FTS index, no encryption.

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

After editing the seed, either boot a fresh KB (`snapshots/seed`) or
`orchestrator kb reseed` so live sandboxes pick up the files. No image rebuild
is required for the Markdown; the `kb` CLI itself **does** need a rootfs rebuild
the first time it is added.

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

## Recall

Inside a sandbox:

```bash
kb catalog                 # id + description + tags; no bodies
kb get dingpan/tables      # one full article
kb get dingpan/tools --json
```

Default root is `/agents/knowledge`. Override with `--root` or `KNOWLEDGE_ROOT`.

The agent should **not** `cat` every file under `knowledge/`. Match the user
question to a catalog description, then `kb get` that id.
