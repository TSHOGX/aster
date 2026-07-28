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

There are two data stores, and they are not interchangeable:

1. **Doris is the default for all data questions.** Statistics, reports, trends,
   aggregations, charging analysis, signal lookups — start here, every time. It is
   modeled, indexed, and answers in seconds. See `skills/doris-query`.
2. **OBS Parquet is the narrow exception.** Use it only when the user explicitly
   mentions OBS, object storage, or Parquet, or explicitly asks to read raw signal
   files. Do not use it for routine querying.

If the user's question does not name Parquet/OBS, the answer comes from Doris. If
Doris genuinely lacks the column, say so and ask — don't silently fall back to
scanning object storage.

## Skills

- `skills/doris-query` (`in-use`, **default data source**) — read-only Doris
  access across `vhr_data` / `vehicle_signal` / `watt`: discover the live schema,
  safe SQL, and streaming large exports. Use this for everyday data work.
- `skills/obs-parquet-duckdb` (`in-use`, **narrow trigger — do not reach for it
  by default**) — raw vehicle-signal Parquet on Huawei OBS, read in place with
  DuckDB httpfs. **Only open this skill when the user explicitly says OBS, object
  storage, Parquet, or asks for raw signal files.** Everyday data questions —
  statistics, reports, trends, aggregations — belong in `skills/doris-query`; do
  not go to Parquet for them. OBS is 1.13M+ bare objects where a single listing
  costs tens of seconds, so scanning it when Doris would have answered wastes
  minutes and returns worse data.
- `skills/matplotlib-cjk-fonts` (`in-use`) — render Chinese text in matplotlib /
  Pillow charts without tofu boxes.

## How to search

Before starting a recurring task:

1. Read `CONSTITUTION.md`, then this index.
2. Agentically search `skills/` for a match; read its `SKILL.md`. Prefer an
   `in-use` skill; consider `observing` ones but verify.
3. Search `data/` for supporting material the skill references.
4. Then begin, following the skill's guidance.

New skills and facts are distilled from session trajectories by an out-of-band
curator and appear in later snapshots.
