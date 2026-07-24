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
  facts/INDEX.md        # navigation entry — the agent reads this first
  facts/<fact>.md       # small, agentic-searchable knowledge notes
  skills/<name>/SKILL.md # curated procedures; frontmatter `status:` key
  data/<domain>/...      # bulky reference material
```

Skills carry a lifecycle status in their frontmatter:

- `observing` — newly promoted, not yet trusted; present but flagged as provisional.
- `in-use` — stable, well-supported, the default an agent should prefer.
- `deprecated` — kept for provenance, should not be followed.

The Pi system prompt points the agent at `/agents/facts/INDEX.md` and tells it to
search the KB before starting recurring tasks.

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

A background goroutine (`internal/memory`) runs once a day. It reads durable
session data — `messages` + `message_traces` + `message_feedback`, NOT the
`events` table (which is pruned to a 24h window every ~30s and is
infrastructure-centric) — and:

1. **Normalize** each grown session into `trajectory_events` (the sess-cli
   7-primitive schema: user / assistant / thinking / tool_call / tool_result /
   system / usage), indexed by `trajectory_snapshots` with a fingerprint so only
   sessions that changed are reprocessed.
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

- **Rebuildable view** — `trajectory_snapshots`, `trajectory_events`. Derived
  mechanically; a re-snapshot replaces a session's rows wholesale.
- **Non-rebuildable output** — `episode_memories`, `semantic_memories`,
  `skill_candidates`. Expensive LLM output; a re-snapshot must never cascade into
  these. They join the view only by soft keys (session id, evidence refs).

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
