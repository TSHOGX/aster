# Shared Knowledge Base

This is the shared, self-evolving knowledge base mounted read-only at `/agents`
in every session. It is the first thing to consult for any recurring task:
distilled skills, supporting data, and long-lived facts accumulated across
prior sessions. Read this file first, then search before you start work.

## Layout

- `skills/` — reusable, task-oriented skills. Each skill lives in its own
  directory with a `SKILL.md` that carries YAML frontmatter, including a
  `status:` key:
  - `observing`  — a candidate distilled from recent trajectories, not yet
    proven. Use with judgment; prefer it over reinventing, but verify.
  - `in-use`     — validated and recommended. Prefer these.
  - `deprecated` — superseded or found harmful. Do not follow; read only to
    understand why an approach was retired.
- `data/`  — supporting datasets, references, and artifacts that skills point
  at (schemas, lookup tables, example outputs).
- `facts/` — long-lived facts and navigation docs. Start here.

## How to search it

Before starting a recurring task:

1. Read this INDEX.
2. Agentically search `/agents/skills` for a skill matching the task. Read the
   matching `SKILL.md` frontmatter and body. Prefer an existing `in-use` skill
   over reinventing an approach; consider `observing` skills but verify them.
3. Search `/agents/data` for supporting material the skill references.
4. Only then begin the task, following the skill's guidance.

The knowledge base is read-only from inside the session. New skills and facts
are distilled from session trajectories by an out-of-band curator and appear in
later snapshots.
