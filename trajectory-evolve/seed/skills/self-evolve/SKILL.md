---
name: self-evolve
status: in-use
support_count: 999
confidence: 1.0
source: human-seeded (trajectory-evolve MCP, 2026-08-27)
---

# Self-evolve from session trajectories

## When To Use

**Only when the user asks** to evolve / refresh / distill the shared knowledge
base from recent sessions, or to run the nightly evolution now.

Do **not** start a cycle unprompted. Do **not** start one because a data query
failed or a fact was missing — that is `skills/mcp-data-query`, not a reason to
rewrite `/agents`. Nightly cron already runs this on the host.

## Context

The evolution graph runs on the **host**, not in this sandbox. It is exposed as
MCP server `trajectory-evolve` (streamable-http at
`http://harness-evolve.internal:8104/mcp`). Pi prefixes tools
`mcp_trajectory_evolve_<name>`. A future agent that speaks MCP calls the same
five names without a prefix. This skill is the WHEN/HOW; the protocol is the
WHAT — so a later agent architecture change does not require rewriting the
graph.

**Tools that exist** (do not invent others):

| Tool | Purpose |
|---|---|
| `evolve_status` | Read-only picture: live/pending digest, quadrants, pains, gaps. Zero LLM. **Call this first.** |
| `evolve_run` | Start a host-side cycle. Returns a **job id immediately**. Result is **staged**, never published. |
| `evolve_job` | Poll that job. `queued` / `running` / `done` / `error` / `skipped`. |
| `kb_status` | Live vs pending digest. This session is pinned to live. |
| `kb_diff` | File-level diff of the pending snapshot. Empty if nothing is pending. |

**Tools that do not exist and must not be invented:** `kb_approve`, `kb_reject`,
`publish`, `ext_activate`. Publication is expert-CLI-only
(`harness-ctl kb approve <digest>` / `cli kb approve <digest>`).

## Procedure

1. **`evolve_status`.** If `pains` is empty **and** `quadrants.miss` is 0 (and
   the other quadrants are 0), tell the user a cycle would be a no-op and stop.
2. If `pending` is already set, show `kb_diff` and ask whether they still want a
   new cycle. A running nightly will make `evolve_run` return `skipped`.
3. **`evolve_run`.** Read `job_id` and `status`. If `skipped` / `reason` is
   `already_running`, tell the user and **stop — do not retry-spam**.
4. **Poll `evolve_job`** until `done`, `error`, or `skipped`. A cycle takes
   minutes. If the client times out, raise `settings.requestTimeoutMs` in
   `mcp.json` rather than launching a second cycle.
5. On `done`: the snapshot is **staged, not live**. This session stays on the
   current `/agents` mount. Tell the user an expert must
   `harness-ctl kb diff` then `harness-ctl kb approve <digest>` before **new**
   sessions see it.

## Hard rules

- Host-side only. Never write `/agents`. Never `curl` the MCP URL as a
  substitute for the tool. Never invent credentials.
- Stage ≠ publish. There is no approve tool on this server.
- Don't retry skipped jobs.
- Don't start a cycle as part of ordinary analysis work.
