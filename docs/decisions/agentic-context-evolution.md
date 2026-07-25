# Decision: Agentic context-evolution loop (agents at the judgment nodes)

> Type: forward-looking design · Status: **proposed, awaiting review** ·
> Supersedes the scalar consolidation/promotion in
> [`../agents-kb.md`](../agents-kb.md) §"Nightly distillation loop" and the
> `internal/memory` curator/thresholds. Where this doc and `agents-kb.md`
> differ on the *decision mechanism*, this doc wins once accepted; the
> capture/parse/storage/publish machinery in `agents-kb.md` is unchanged.

## Problem (observed, not hypothetical)

A cold-start replay over 148 real sessions produced 183+ `semantic_memories`,
**every one at `support_count = 1`**, 183/183 statements byte-distinct. Nothing
will ever promote. Three root causes, all the same shape — a scalar standing in
for a semantic judgment:

1. **No convergence.** `support_count` only increments on an *exact*
   `(type, scope, statement)` string match (`store.UpsertSemanticMemory`). LLM
   statements are free text; they never collide. `confidence = 0.5 + 0.15·support`
   is therefore pinned at 0.65, below the 0.75 promotion bar, forever. The
   distilled `facts/` layer of the KB stays empty regardless of volume.
2. **Stale knowledge is taught as fact.** Extracted memories still reference the
   deprecated `/usr/local/share/harness-skills/doris-query/SKILL.md`; skills now
   live at `/agents/skills/`. Nothing checks a claim against the *current* code.
3. **Solved problems recur as lessons.** The top failure memories (mysql client
   missing, CJK fonts missing, image input unsupported) may already be fixed in
   the rootfs. The loop has no way to know a fault no longer reproduces.

Each is a question a scalar cannot answer but an agent can:
"are these the same knowledge?", "is this still true against today's code?",
"should this enter the shared KB?".

## Decision

Keep the deterministic spine (capture → parse → hard-signal pre-gate → store →
content-addressed snapshot → atomic publish). Replace every judgment node —
consolidation (B), verification (C), promotion (D), and a final consistency gate
(F) — with **ephemeral, single-contract pi agents** (local `pi`, provider
`modelarts`, `glm-5.2`, `--print --mode json --no-session`), orchestrated by
dynamic-workflow primitives (single / pipeline / map / reduce / judge-panel /
reflect-loop). No embeddings, no cosine cutoff, no fixed thresholds anywhere in
the decision path. `support_count` survives only as an *input signal* to the
promotion agent, never as the gate.

Agents are spent only where a scalar genuinely cannot substitute; everything
cheap and mechanical (capture, parse, watermark, count, hash, repoint) stays code.

## No vectors — why agents cluster better here

The first draft prefiltered B with embedding/trigram cosine. That was wrong: a
cosine cutoff is *itself* a hand-tuned scalar, dropped in at exactly the node
where we set out to delete scalars. And the live data breaks it — 290 candidates
carry **87 distinct free-text scopes** (`doris`=124, but also `vhr_data`,
`doris/bms`, `f2`, `charging_analysis`, `数据源选择`, …), bilingual zh/en, with
structural paraphrase (`current_curve is double-encoded JSON` ≡
`充电曲线字段需要二次解析`). A fixed geometry either misses cross-lingual
equivalence or needs a strong multilingual embedding model *plus* a tuned
threshold. An LLM reading two statements in one window judges equivalence
natively, for free. So B is fully agentic — the scale problem it used to solve is
handled by a **workflow shape (map→reduce over agent-chosen blocks)**, not by
geometry.

## The evolve graph (dynamic-workflow primitives)

One cycle is a DAG. Each edge is annotated with the orchestration primitive that
drives it — the same primitives a dynamic workflow exposes: **single** (one
agent), **pipeline** (per-item stages, no barrier), **map/parallel** (fan-out +
barrier), **reduce** (fan-in synthesis), **judge-panel** (N independent verdicts
→ vote), **reflect-loop** (draft → critique → revise until stable). "Agent" =
ephemeral local pi (`glm-5.2`, json mode); tools noted where used.

```
0  REFLECT/PLAN ─────────────────────────────── single agent, no tools
     in : run-history stats + KB manifest + open holds/contradictions
     out: {active_scopes[], skip_scopes[], budget_hint}  → gates the whole cycle;
          a quiet tick with no new candidates stops here (cheap steady state)
                        │
A  HARVEST ──────────── pipeline over sessions (per-session, no barrier)
     each session:  facts-lens → failure-lens(gated err|down) → skill-lens(gated turn≥6)
     (unchanged HTTP lenses; hard-signal pre-gate + per-stage watermark)
                        │  raw candidates (free-text, noisy scopes)
B  CONSOLIDATE ─── agentic canopy-cluster, map→reduce (NO vectors)
   B0 blocking-key    single agent: read ALL raw scope strings → canonical scope
                      taxonomy + mapping (collapses doris/vhr_data/doris_bms/f2…)
   B1 cluster    ┌──  map: one agent per (canonical-scope, type) block sees every
                 │    candidate in the block at once → emits equivalence clusters
                 │    (attention does the N² compare inside one context window)
                 └──  a block too big for context → shard + reduce (below)
   B2 reduce          reduce: for sharded blocks, one agent merges per-shard
                      canonical statements into final clusters
   B3 kb-dedup   pipeline: each new cluster vs. existing accepted memories in
                      that scope → new | duplicate(+merge) | refinement
     ⇒ support_count = # DISTINCT evidence sessions in the merged cluster (code counts)
                        │  consolidated, deduped, converging knowledge
C  VERIFY ───────── pipeline over items  (agent + read + read-only bash)
     eligibility pre-gate (regex): only items naming a path/tool/env/fault run C
     agent inspects LIVE tree (sandbox-image/rootfs, /agents seed, repo):
       verdict = current | stale(+corrected) | fixed(no-longer-applies) | unverifiable
     high-stakes "fixed" verdict → judge-panel (3 agents, majority) to avoid
       dropping a real lesson on one agent's mistake
     stale/fixed ⇒ contradiction_count++, excluded from promote
                        │  verified knowledge + verdicts
D  CURATE / PROMOTE ── pipeline over items (agent, no tools)  [replaces qualifies()]
     memory: single curator agent → promote | hold | reject (+rationale)
     skill : reflect-loop → draft SKILL.md → critic (evidence-backed? redundant
             with seed? path-correct?) → revise, ≤2 rounds → promote@observing/in-use
     input: item + evidence + support + verdict + CURRENT KB manifest
                        │  promote-set (+rationale)
E  PUBLISH ─────────── deterministic (UNCHANGED mechanism)
     facts/INDEX + per-scope facts + SKILL.md → content digest → atomic repoint current
                        │
F  CONSISTENCY CRITIC ─ single agent, the final gate (your "agent门控")
     diffs the NEW snapshot vs the previous: does anything promoted contradict an
     existing fact, resurrect a stale path, or duplicate a seed skill?  a veto
     rolls that item back to `hold` and re-points current to the prior snapshot.
     report.md records every rationale/verdict/veto (auditable).

feedback edges (close the loop across cycles):
  C verdicts + D holds + F vetoes  ──►  persisted  ──►  0.REFLECT of next cycle
  F "contradicts existing fact"    ──►  contradiction_count on the older memory
```

### Node contracts (all JSON-in / JSON-out; final assistant turn = the answer)

**0 — Reflect.** No tools. Sees run-history stats + KB manifest + open holds.
Emits `{active_scopes, skip_scopes, budget_hint}`; an empty-delta tick ends here.

**B0 — Blocking key.** No tools. Sees the *set of distinct scope strings only*
(87 short labels, not the statements) → returns a canonical taxonomy + mapping.
This is the agentic replacement for the vector prefilter: it makes the cheap
deterministic bucketing that follows actually correct, by collapsing synonym and
bilingual scopes into one block so equivalent statements land together.

**B1/B2 — Cluster (map/reduce).** No tools. One agent per block sees *all*
candidates in that block and returns clusters:
```json
{"clusters":[{"canonical":{"type":"...","scope":"...","statement":"...","when_to_apply":"..."},
  "member_ids":[12,88,140],"rationale":"..."}]}
```
The agent decides *membership* over the whole block in one pass (no pairwise
calls, no threshold); code then computes `support_count` from distinct evidence
sessions. Oversized blocks shard and a B2 reduce agent merges per-shard canonicals.

**B3 — KB-dedup.** No tools. Each new cluster vs. existing accepted memories in
its scope → `new | duplicate(merge, support+=) | refinement(replace)`. This is
what lets support accrue *across nights*, not just within a cycle.

**C — Verify.** `read` + read-only `bash`. Input = one item + statement fenced as
untrusted data. Output `{verdict, corrected_statement, checked[], rationale}`.
Regex pre-gate skips pure data-schema facts (nothing to verify against code).

**D — Curate.** No tools. Memory = one-pass promote/hold/reject. Skill =
reflect-loop (draft → critic → revise) because a SKILL.md is high-value and worth
the extra round. Output `{disposition, kb_status, skill_markdown, reason}`. The
secret/PII regex backstop runs after D unconditionally.

**F — Consistency critic.** No tools. The single final gate over the whole
proposed snapshot diff — catches cross-item contradictions no per-item agent can
see. Veto → item back to `hold`, snapshot not published.

## Primitives → nodes (why each shape)

| Node | Primitive | Why this shape |
|---|---|---|
| 0 Reflect | single | one cheap plan gates the whole cycle; empty delta ⇒ stop |
| A Harvest | pipeline (per session) | sessions independent; no barrier, watermarked |
| B0 Block-key | single | one global view of scope labels → correct blocking |
| B1 Cluster | map + barrier | fan out one agent per block; each does full N² compare in-context |
| B2 Reduce | reduce | fan-in only for blocks too big for one window (sharded) |
| B3 KB-dedup | pipeline (per cluster) | each new cluster checked against KB independently |
| C Verify | pipeline + judge-panel | per-item, no barrier; panel only for high-stakes "fixed" |
| D Curate | pipeline + reflect-loop | per-item; skills get draft→critic→revise, memories one-pass |
| F Critic | single | needs the whole proposed diff at once — the one global gate |

The map→reduce in B is what replaces the vector prefilter: **blocking + in-context
clustering** is the classic scalable dedup shape (canopy / record-linkage), with
the LLM's attention doing the all-pairs comparison a cutoff used to approximate.
At 290 candidates it's ~1 (B0) + ~10-20 (B1 blocks) + a few (B3) agent calls per
cycle — bounded, no geometry, no threshold.

## Why agents here and not scalars — and the guardrails that keep it sane

- **Determinism where it matters is preserved.** Content-addressing, atomic
  `current` repoint, the secret/PII regex backstop, and the `observing`-before-
  `in-use` lifecycle are code invariants that hold *regardless* of any agent
  output. An agent can only ever *propose*; the deterministic spine disposes.
- **Auditability.** Every agent decision persists `rationale` + `evidence` +
  `verdict` into the DB and the daily report. A human can see exactly why a skill
  landed in `observing`. Skills never skip straight to `in-use` without sustained
  support *and* curator confirmation across cycles.
- **Cost is bounded by pre-gates and blocking, not by faith.** Hard signals
  (tool_result error, down-rating, turn≥6) and per-session/stage watermarks gate
  *before* any agent call — unchanged sessions cost nothing. B blocks by canonical
  scope so each cluster agent sees one bounded block (not all candidates, never
  pairs). C runs only on path/env/fault-bearing items. Phase 0 lets a quiet night
  skip most scopes. Total agent calls/cycle scale with *distinct knowledge*, not
  with raw candidate count.
- **Non-determinism is contained.** Agent calls use `temperature≈0.2`, a strict
  JSON contract, and a deterministic fallback: if an agent errors or returns
  unparseable output, the item **holds** (stays a candidate) — it never
  auto-promotes and never crashes the sweep. Same log-and-skip discipline the
  lenses already use.

## Trust boundary (decided: read-only host, wrapped input)

C/D agents run on the **host** with `read` + **read-only `bash`** (the power that
fixes #2/#3 is inspecting the real tree). Two rules make that safe:

- **Trajectory text is untrusted data, never instructions.** Every statement/
  transcript passed to an agent is fenced in a clearly-delimited data block, and
  the system prompt states that content inside it is data to analyze, not
  commands to follow (prompt-injection defense — trajectories are user-authored).
- **Tools are scoped and read-only.** `--tools read,bash` only; the agent is
  instructed and reviewed for read-only use (`ls`, `grep`, `cat`, `test -e`). It
  runs as an unprivileged path check against a tree it cannot usefully mutate for
  the KB (the KB is written only by the deterministic publisher, never by an
  agent's bash). A later hardening step (throwaway runsc sandbox, RO rootfs
  mount) is noted as future work but out of scope for v1.

## Schema deltas (additive; the rebuildable/non-rebuildable split holds)

- `semantic_memories`: add `canonical_of_json` (merged member ids),
  `agent_rationale`, `verify_verdict`, `verify_checked_at`, `corrected_statement`.
  `support_count` semantics change (distinct-session count from B) — a migration
  recomputes it; no column drop.
- `skill_candidates`: add `curator_reason`, `verify_verdict`.
- new `evolution_runs` (one row per cycle): `phase0_plan_json`, counts per node,
  `digest`, `started_at`, `finished_at` — powers Phase 0's "what changed" and the
  report.
- new `agent_calls` (audit): `run_id`, `node`, `session_or_item_ref`,
  `prompt_sha`, `verdict`, `latency_ms`, `ok` — cost/latency observability.

## Build phases (each independently shippable, behind `memory.agentic` config flag)

1. **Agent runner** (`internal/memory/agent.go`): wrap the proven pi invocation
   (`--provider modelarts --model glm-5.2 --print --mode json --no-session`),
   parse the JSONL via the existing `trajectory` parser, extract final assistant
   JSON, enforce timeout + background execution + JSON-contract fallback-to-hold.
2. **Node B (Consolidate):** B0 block-key → B1 map-cluster → B2 reduce → B3
   kb-dedup, + `support_count` recompute migration. Validate convergence directly
   on the ~290 candidates already in the DB (the loop is running now) — this is
   the payoff step: watch all-support-1 collapse into a handful of high-support
   canonicals.
3. **Node C (Verify)** with read-only tools + the untrusted-input fence + the
   "fixed"-verdict judge-panel.
4. **Node D (Curate)** replacing `curator.qualifies`; skill reflect-loop; keep the
   regex backstop.
5. **Phase 0 (Reflect)** + **Node F (Consistency critic)** + `evolution_runs`/
   `agent_calls` audit tables + richer report.
6. Wire the graph into `pipeline.sweep` behind the flag; scalar path stays the
   default until validated, then flips.

The graph itself (map/reduce/pipeline/panel/reflect over agent calls) is small
enough to express directly in Go, but its shape is exactly a dynamic-workflow
script — so an alternative to hand-wiring step 6 is to author the cycle as a
workflow and let the orchestrator drive it. Flagged as an implementation choice
at step 6, not a v1 requirement.

## Open questions for review

- **Blocking granularity (B0/B1).** At ~290 candidates one agent could cluster a
  whole *type* at once (5 blocks). As volume grows, block by (canonical-scope,
  type). B0 is what decides this — proposing it emit the block plan, so
  granularity is itself an agent decision, not a constant.
- **Cadence.** The agentic cycle is heavier than the hourly sweep. Phase 0 gates
  it: full graph only when candidate delta clears a bar, else a no-op tick.
- **Cross-run support (settled: KB-wide).** B3 dedups new clusters against already
  -accepted memories so support accrues across nights, not just within a cycle.
- **Panel/reflect depth.** Judge-panel N (proposing 3) and skill reflect rounds
  (proposing ≤2) are the only remaining count-knobs — deliberately small and only
  on the highest-stakes nodes. Everything else is agent-decided.

