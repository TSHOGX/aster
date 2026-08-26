# Knowledge base index

Navigation entry for the shared knowledge base mounted read-only at `/agents`.
For who you are and how you are expected to work, read `/agents/CONSTITUTION.md`
first — this file is just the map.

## Layout

- `CONSTITUTION.md` — identity, capabilities, operating principles. Read first.
- `skills/<name>/SKILL.md` — reusable, task-oriented procedures. Frontmatter
  `status:` key marks trust:
  - `in-use` — validated and recommended. Prefer these.
  - `observing` — a candidate distilled from recent trajectories, not yet proven.
    Use with judgment; prefer it over reinventing, but verify.
  - `deprecated` — superseded or found harmful. Do not follow.
- `data/<domain>/...` — supporting datasets, references, and artifacts that skills
  point at (schemas, lookup tables, example outputs).
- `facts/*.md` — long-lived, searchable knowledge notes grouped by scope.

## Which data source (read before querying anything)

1. **MCP tools are the default for all data questions.** Statistics, reports,
   trends, aggregations, charging analysis, signal lookups — start with MCP every
   time. See `skills/mcp-data-query`. Discover tools in-session, then call them.
2. **Doris direct SQL is opt-in only.** Open `skills/doris-query` only when the
   user explicitly asks for Doris / direct warehouse SQL.
3. **OBS Parquet is opt-in only.** Open `skills/obs-parquet-duckdb` only when the
   user explicitly mentions OBS, object storage, Parquet, or raw signal files.

If MCP cannot answer, say so and ask — do **not** silently fall back to Doris or
scan object storage.

## Skills

- `skills/mcp-data-query` (`in-use`, **default data source**) — query live data
  through session MCP tools (host gateway → upstream). Use this for everyday
  data work.
- `skills/doris-query` (**opt-in only** — not the default) — direct read-only
  Doris SQL. Use only if the user explicitly requests Doris / raw SQL against the
  warehouse.
- `skills/obs-parquet-duckdb` (**opt-in only** — not the default) — raw
  vehicle-signal Parquet on Huawei OBS via DuckDB. Only when the user explicitly
  says OBS / object storage / Parquet / raw signal files.
- `skills/matplotlib-cjk-fonts` (`in-use`) — render Chinese text in matplotlib /
  Pillow charts without tofu boxes.
- `skills/battery-domain-reference` (`in-use`) — engineering specs / DTC / CAN
  decode; for measured occurrences afterward, use `skills/mcp-data-query`.

## How to search

Before starting a recurring task:

1. Read `CONSTITUTION.md`, then this index.
2. Agentically search `skills/` for a match; read its `SKILL.md`. Prefer an
   `in-use` skill; consider `observing` ones but verify.
3. Search `data/` for supporting material the skill references.
4. Then begin, following the skill's guidance.

New skills and facts are distilled from session trajectories by an out-of-band
curator and appear in later snapshots.
