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

## Skills

- `skills/doris-query` (`in-use`) — read-only Doris/`vhr_data` access: discover
  the live schema, safe SQL, and streaming large exports.
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
