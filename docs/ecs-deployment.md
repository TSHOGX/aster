# ECS deployment handoff: knowledge base, trajectories, distillation

This is the operational guide for running the self-evolving `/agents` knowledge
base and the trajectory-distillation loop on an ECS host. It is the companion to
the design doc [agents-kb.md](./agents-kb.md).

The core property to preserve: **skills, facts, and the agent constitution update
without rebuilding the sandbox image or restarting the orchestrator.** Only a tiny
bootstrap prompt is baked into the image. Everything else is data under host
directories that new sessions pick up automatically.

## 1. Persistent host directories

Point these at durable, backed-up storage (they hold the accumulated research
store and the live knowledge base):

| Env var | Default | Holds |
|---|---|---|
| `HARNESS_AGENTS_KB_PATH` | `/var/lib/harness/agents-kb` | the `/agents` KB: `current -> snapshots/<digest>/`, plus `reports/` |
| `HARNESS_TRAJECTORY_ROOT` | `/var/lib/harness/trajectories` | archived native pi traces (`raw/<session>/…jsonl`) |
| `HARNESS_AGENT_HOMES_ROOT` | `/var/lib/harness/agent-homes` | per-session pi homes (native traces are captured from here) |
| `HARNESS_DB_PATH` | `/var/lib/harness/state/orchestrator.db` | the store (sessions, events, trajectory + memory tables) |

On first boot the orchestrator seeds `HARNESS_AGENTS_KB_PATH` from the baked
overlay (`ensureAgentsKB`) and points `current` at a `seed` snapshot, so `/agents`
is populated before the first distillation run.

## 2. Enable the distillation loop (LLM)

Create `config/memory-llm.yaml` (git-ignored secret; see
`config/memory-llm.example.yaml`):

```yaml
enabled: true
base_url: https://api.modelarts-maas.com/openai/v1
api_key: "<key>"
model: glm-5.2
```

Empty api key or absent file ⇒ the nightly loop is disabled and the KB only
changes when a human edits it. The extractor calls this endpoint directly (like
the titler); it does **not** go through the in-sandbox model proxy.

## 3. Provision the Doris credential

The `doris-query` skill embeds a live read-only credential, so its real
`SKILL.md` is git-ignored. Before building the rootfs (or into the KB seed on the
host), copy the redacted example and fill in real values:

```
sandbox-image/files/usr/local/share/agents-kb-seed/skills/doris-query/SKILL.md.example
  → .../doris-query/SKILL.md   (real host/port/user/password)
```

Keep sandbox egress locked to the Doris host (see [security.md](./security.md)).

## 4. Cold-start: backfill + replay

After deploying with existing session history, bootstrap the research store and
grow the KB from past sessions. Both are one-shot subcommands of the orchestrator
binary (no server, no goroutines):

```bash
# 1. Capture ALL historical sessions' native pi trajectories into the store.
#    Idempotent; unchanged sessions are skipped by fingerprint.
orchestrator backfill                       # or: --since 2026-05-01

# 2. Replay the distillation loop one day at a time, oldest first, so the KB
#    grows as it would have night by night. Each run re-captures that day and
#    runs the extraction lenses + curator, republishing the KB snapshot.
orchestrator replay --date 2026-05-01
orchestrator replay --date 2026-05-02
# … loop over the date range …
```

`backfill` needs `HARNESS_TRAJECTORY_ROOT`. `replay` also needs
`config/memory-llm.yaml`. A simple driver loop over dates is fine; runs are
independent and idempotent.

## 5. Steady state

- The orchestrator runs the distillation sweep on its own timer (no cron needed).
  Each sweep captures new/grown sessions, extracts, and republishes the KB if
  anything qualifies for promotion.
- To update a skill or the constitution **by hand**: edit the file under the
  current snapshot's source (or add a new snapshot dir and re-point `current`) —
  new sessions see it immediately; running sessions refresh on their next
  generation. No image rebuild, no restart.
- Rebuild the sandbox image only to change the baked bootstrap prompt (the
  identity fallback used when `/agents` is absent) or the OS/runtime layer.

## 6. What the ECS team needs to do

1. Mount durable storage for the four dirs in §1; set the env vars.
2. Drop in `config/memory-llm.yaml` and the real Doris `SKILL.md`.
3. Run the §4 backfill + replay sequence once to cold-start.
4. Nothing else recurring — the loop self-schedules. Back up
   `HARNESS_AGENTS_KB_PATH`, `HARNESS_TRAJECTORY_ROOT`, and the DB; the archived
   raw traces + the KB snapshots are the non-rebuildable assets.
