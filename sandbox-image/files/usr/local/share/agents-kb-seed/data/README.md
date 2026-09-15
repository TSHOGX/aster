# `data/` — reference corpora

`data/<domain>/` holds bulky reference material that skills point at: schemas,
lookup tables, specification exports, extracted document text. It is mounted
read-only at `/agents/data/` in every sandbox.

## Nothing under `data/<domain>/` is committed

Each deployment's corpora are its own private domain knowledge, not part of this
runtime. `.gitignore` excludes `data/*/` wholesale — including manifests and file
listings, because those leak source filenames, and filenames are often themselves
confidential. Only this README and `.gitkeep` are tracked.

Provision a dataset on the host instead, writing it into the KB snapshot:

```
$HARNESS_AGENTS_KB_PATH/snapshots/<digest>/data/<domain>/
```

Install into `snapshots/seed/` as well, since that is the first-boot fallback when
no `current` symlink exists yet. No image rebuild and no restart is needed: the
runtime resolves `current` to a concrete snapshot at per-generation spec-build
time, so new sessions pick up new content and running generations refresh on
restart.

## Making a dataset usable by an agent

Bulk files alone are close to useless — an agent needs to find them and know what
they mean. Provide three things:

1. **`data/<domain>/README.md`** — the entry point. What the corpus is, its
   layout, what each subtree contains, and how the parts join to each other
   (shared keys, IDs, naming conventions). This is the first thing an agent reads.
2. **`skills/<name>/SKILL.md`** — the procedure. When to reach for this corpus,
   how to locate before loading, and the grounding rules (cite the file you used;
   don't generalize a value across contexts it isn't valid for). Give it
   `status: in-use` frontmatter once validated.
3. **`facts/<name>.md`** — a short pointer so the corpus is discoverable from
   `facts/INDEX.md`.

Note that `facts/` is fully regenerated on every KB republish (see
`internal/memory/kb.go` `carryForward`), so treat a hand-written `facts/` entry as
disposable and keep the durable copy inside `data/<domain>/` or the skill.
`CONSTITUTION.md`, `skills/`, `knowledge/` and `data/` are carried forward.

## Making it searchable

Agents search text. Convert what you can and keep the conversion honest:

- Prefer CSV/Markdown over binary office formats so the corpus is greppable.
  Mirror the source directory tree so a path identifies its origin.
- Ship a manifest (source file → its converted artifacts) and a per-sheet or
  per-section index. These let an agent answer "does this exist" without opening
  anything large. Keep them host-side with the payload.
- Record fidelity caveats in the README: what the conversion normalized, where
  headers are irregular, whether values are strings, and whether anything was
  deduplicated. An agent that assumes row 0 is the header will be wrong often.
- **Enumerate what is missing.** If content is image-only, or a source file is
  corrupt, or a parse needed relaxed rules, list it explicitly. Otherwise an agent
  reports "not in the corpus" when the truth is "present but unreadable in this
  form" — a materially different answer.
- Match the build to the model. If the deployment's model is text-only, shipping
  binary originals costs disk and buys nothing; but check what else those
  binaries carry before dropping them (spreadsheet comments and formulas are
  often load-bearing and can be extracted to text).

## Confidential material

Corpora frequently carry confidentiality markings. Say so in the README and in
the skill, so answers stay inside the platform and raw excerpts are not forwarded
to external services. There is no tenant isolation yet: everything in `data/` is
visible to every sandbox on the host.
