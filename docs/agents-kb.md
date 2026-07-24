# Shared knowledge base (`/agents`) + nightly trajectory distillation

> The self-evolving knowledge base every sandbox mounts read-only, and the
> nightly loop that grows it from real session trajectories. Implements (and
> supersedes the "system-skills mount" placeholder in)
> [`decisions/trajectory-pipeline.md`](./decisions/trajectory-pipeline.md).

## What it is

Every sandbox gets a read-only `/agents` directory bind-mounted in, holding a
`~/.agents`-style tree:

```
/agents/
  CONSTITUTION.md       # who the agent is, what it can do, how it works — read first
  facts/INDEX.md        # navigation entry / map of the KB
  facts/<fact>.md       # small, agentic-searchable knowledge notes
  skills/<name>/SKILL.md # curated procedures; frontmatter `status:` key
  data/<domain>/...      # bulky reference material
```

Skills carry a lifecycle status in their frontmatter:

- `observing` — newly promoted, not yet trusted; present but flagged as provisional.
- `in-use` — stable, well-supported, the default an agent should prefer.
- `deprecated` — kept for provenance, should not be followed.

### Prompt architecture: slim bootstrap + evolvable constitution

The identity/capabilities/where-to-look content that used to be frozen in the
baked system prompt is split by change-rate and owner into two layers:

- **Bootstrap prompt** (`sandbox-image/.../pi-system-prompt.md`, baked, injected
  via `--append-system-prompt`): a minimal, rarely-changing string — the identity
  one-liner plus "read `/agents/CONSTITUTION.md` first, consult the KB before
  recurring work", and safe defaults for when `/agents` is absent. No domain rules.
- **Constitution** (`/agents/CONSTITUTION.md`): the governing document — who the
  agent is, what it can do, who it helps, operating principles, and the KB search
  protocol. It lives in the KB, so it evolves without an image rebuild and can
  later become a curator candidate (behind human review). Modeled on the local
  `~/.agents` spec style.

Domain skills (Doris, matplotlib-CJK) are no longer inlined in the prompt or the
legacy `harness-skills/` dir — they are seeded skills in the KB
(`agents-kb-seed/skills/`, `status: in-use`) and follow the same
snapshot/promotion lifecycle as distilled ones.

The constitution and human-seeded skills are **carried forward** on every
republish (`kb.go` `carryForward`): the distillation publisher only regenerates
`facts/INDEX.md`, per-scope facts, and distilled skills, and overlays them on top
of the preserved seed — so a nightly snapshot never drops the constitution or a
seeded skill.

## Delivery (no image rebuild, no restart)

The KB lives on the host at `AgentsKBRoot` (default `/var/lib/harness/agents-kb`,
env `HARNESS_AGENTS_KB_PATH`):

```
agents-kb/
  current -> snapshots/<digest>/   # atomically re-pointed symlink
  snapshots/<digest>/...           # content-addressed immutable snapshots
  reports/YYYY-MM-DD.md            # nightly run reports
```

At sandbox spec-build time the runtime resolves `current` → the concrete
`snapshots/<digest>/` dir and bind-mounts THAT (stable, digest-pinned) at
`/agents`, read-only (`runtime/mount_plan.go`, `runtime_spec.go`). Because the
mount source is the resolved snapshot, a running generation stays pinned to the
snapshot it started with; a **new** session picks up whatever `current` points at
then. Publishing a new KB = write a fresh `snapshots/<digest>/` tree, then
atomically re-point `current`. No rootfs rebuild, no orchestrator/front-end
restart. Old sessions refresh on their next generation (restart).

The mount is disabled (absent) when `AgentsKBRoot` is empty or no `current`
snapshot exists yet. A baked seed KB
(`sandbox-image/files/usr/local/share/agents-kb-seed/`) is copied into
`snapshots/seed/` and pointed to on first boot so the mount source always exists.

## Nightly distillation loop

A background goroutine (`internal/memory`) runs once a day. Its trajectory source
is pi's **native session JSONL** — the same complete record the local sess-cli
ingests — captured full-fidelity, NOT the lossy `messages`/`message_traces`
projection and NOT the `events` table (which is pruned to a 24h window every ~30s,
is infrastructure-centric, and even drops pi's `toolUse` turn boundaries). It:

1. **Capture** each session's native pi trace (`internal/trajectory`): locate
   `<session_driver_homes.host_path>/.pi/agent/sessions/<selected_session_relpath>`
   (from `session_driver_states`; glob fallback), archive a durable copy under
   `<TrajectoryRoot>/raw/<session>/`, record a `raw_sources` pointer, parse it via
   the sess-cli `pi.ts` field mapping into `trajectory_events` (7-primitive schema:
   user / assistant / thinking / tool_call / tool_result / system; pi emits no
   token usage). Text is stored **full and untruncated** — the 2000-char
   `message_traces` cap and synthesized "thinking" of the product path are gone.
   Indexed by `trajectory_snapshots` with a fingerprint (`records:lastEpoch`) so
   only changed sessions are reprocessed. Enumerated across **all users**. When no
   native file is available the loop falls back to the lossy DB projection at
   `fidelity=partial`.
2. **Extract** via three LLM lenses (adapted from Mem0 / WDM / Hermes /
   ReasoningBank / AWM), each gated by a cheap hard signal before spending an LLM
   call:
   - **facts** → `semantic_memories` candidates.
   - **failure/feedback** → episode + bug/correction memories. Gated on a
     material failure signal (a `tool_result` error, or a `down` feedback rating).
   - **skill** → `skill_candidates`. Gated on `turn_count >= 6`.
3. **Consolidate** candidate memories into `semantic_memories` with
   support/contradiction counts driving confidence.
4. **Curate & publish**: promote a memory/skill into a new KB snapshot when it is
   stable and well-supported (`support_count >= 3 && confidence >= 0.75 &&
   contradiction_count == 0`). Promoted skills first land as `status: observing`;
   sustained support flips them to `in-use`. A daily report is written to
   `agents-kb/reports/`.

### Storage lifecycle split (from sess-cli)

Two table groups share the DB but have opposite lifecycles:

- **Rebuildable view** — `raw_sources` (pointer to the archived native trace),
  `trajectory_snapshots`, `trajectory_events`. Derived mechanically from the
  native pi files; a re-capture replaces a session's rows wholesale.
- **Non-rebuildable output** — `episode_memories`, `semantic_memories`,
  `skill_candidates`. Expensive LLM output; a re-capture must never cascade into
  these. They join the view only by soft keys (session id, evidence refs).

This full research store is deliberately **separate from the `events` table**,
which remains the untouched user-facing product log (two businesses: product vs
research). Native traces persist on the host (agent homes are never reclaimed by
cleanup), so capture can run long after a session ends.

### Cold-start: backfill + replay

The orchestrator binary has two one-shot subcommands for bootstrapping the store
(e.g. on first ECS deploy):

- `orchestrator backfill [--since YYYY-MM-DD]` — capture every historical pi
  session's native trajectory (omit `--since` for all history). Idempotent.
- `orchestrator replay --date YYYY-MM-DD` — re-capture that day and run the
  distillation sweep scoped to it, as if it were that night — so historical days
  can be processed one at a time to grow the KB from cold.

Both need `HARNESS_TRAJECTORY_ROOT` (default `/var/lib/harness/trajectories`);
`replay` also needs `config/memory-llm.yaml`.

## LLM configuration

The extraction model is configured in a gitignored secret file
`config/memory-llm.yaml` (see `config/memory-llm.example.yaml`), same shape as
`title-llm.yaml`: `base_url` / `api_key` / `model` (glm-5.2). Empty api key or
absent file disables the nightly pipeline. The extractor calls the upstream API
directly (like `internal/titler`); the in-repo model proxy serves sandbox
traffic only and is not a server-side client.

## Multi-tenancy note

There is no tenant isolation yet: all users' trajectories feed one shared KB, and
promoted knowledge is visible to every sandbox. The promotion path must strip
user-specific secrets/PII before anything enters the shared KB (the lens prompts
are instructed to omit them, and the curator is the enforcement point). Revisit
when tenancy lands — per-tenant overlays over shared trajectory storage are the
natural extension (sess-cli's overlay-keyed-by-id model).
