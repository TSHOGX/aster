# Self-evolve MCP (trajectory-evolve)

How the standalone `trajectory-evolve` pipeline plugs into this platform so
an agent can trigger self-evolution **on demand**, a cron job can run it
**unattended**, and a later agent architecture change does not require rewriting
the graph.

The Bun pipeline lives in-tree at `trajectory-evolve/` (MCP server, CLI, graphs).
Override with `TRAJECTORY_EVOLVE_ROOT` only if you run a checkout from elsewhere.

## Three doors, one implementation

```text
 cron  ──►  ops/jobs  ◄──  MCP tools (protocol)
 CLI   ──►     │
               ▼
        staged KB snapshot
               │
        expert approve (CLI only)  ──►  /agents mount
```

| Door | What it is | Survives agent-architecture change? |
|---|---|---|
| **MCP** | Five tools over JSON-RPC 2024-11-05. Host process. | Yes — any client that speaks MCP. Pi prefixes `mcp_trajectory_evolve_*`; a future agent calls the same names. |
| **Skill** | `/agents/skills/self-evolve/SKILL.md` — WHEN/HOW. | Yes — as long as the new agent still reads `/agents`. |
| **Cron** | `scripts/harness-evolve-nightly.sh` → `cli cycle`. | Yes — no agent involved. |

Approve / reject / extractor-activate are **not** MCP tools. A sandbox that
could publish would write into every future session.

## Sandbox wiring (opt-in, off by default)

The control plane only projects `/etc/hosts` aliases for **enabled**
`harness.data_proxies`. `mcp.json` lives in the agent home, not this repo.

1. Run the MCP server on the host (no upstream token — unlike dingpan):

   ```bash
   cd /home/agent-runtime-platform/main/trajectory-evolve   # or $TRAJECTORY_EVOLVE_ROOT
   export PATH="$PWD/node_modules/.bin:$PATH"
   bun install
   bun run src/mcp.ts --http 8104
   ```

   systemd sketch: `WorkingDirectory` = the pipeline root,
   `ExecStart=…/node_modules/.bin/bun run src/mcp.ts --http 8104`,
   `TE_MCP_HOST=0.0.0.0`. Bind is a wildcard for the same reason as the other
   gateways (per-generation `/30` veth). Restrict the port with the existing
   packet filter.

2. Uncomment `harness.data_proxies.evolve` in `config/harness.yaml`
   (`enabled: true`, alias `harness-evolve.internal`, port `8104`). Restart the
   orchestrator. **New** sessions get the hosts line.

3. Add a second server to the sandbox `mcp.json` (no credentials):

   ```json
   {
     "mcpServers": {
       "trajectory-evolve": {
         "transport": "streamable-http",
         "url": "http://harness-evolve.internal:8104/mcp",
         "lifecycle": "eager",
         "settings": { "requestTimeoutMs": 120000 }
       }
     }
   }
   ```

   Keep the dingpan server. Raise `requestTimeoutMs` — `evolve_run` returns at
   once, but a slow `evolve_status` against a large DB should not hit the
   default 30s client timeout.

4. Seed the skill without wiping distilled facts:

   ```bash
   orchestrator kb reseed --path skills/self-evolve,CONSTITUTION.md,facts/INDEX.md
   harness-ctl kb diff
   harness-ctl kb approve <digest>
   ```

   `kb reseed` stages onto the live snapshot; it does not publish.

5. Smoke from a sandbox (after a new session):

   ```bash
   curl -sS http://harness-evolve.internal:8104/healthz
   ```

   Then ask the agent to follow `skills/self-evolve`.

Until step 2 is enabled, the skill is present but the tools are not — the
agent should report wiring failure rather than curl/bash around it.

## Cron (does not replace the Go nightly)

`scripts/harness-kb-nightly.sh` still owns capture (`backfill`) and the existing
Go distillation. `scripts/harness-evolve-nightly.sh` is a **second** writer
against trajectory-evolve's own SQLite (`TE_STATE_ROOT`), so it does not take
`memory.db`'s write lock.

Default layout keeps `TE_KB_ROOT` **out** of `/var/lib/harness-publish/agents-kb`
so a demo cycle cannot clobber the live mount. Cutover (same tree `harness-ctl
kb` already reviews):

```bash
export TE_MEMORY_DB=/var/lib/harness-publish/state/memory.db
export TE_KB_ROOT=/var/lib/harness-publish/agents-kb
export TE_ENGINE=pi
export TE_MODEL=glm-5.2
```

Do not enable both Go `replay` and this cycle against the same `TE_KB_ROOT` on
the same night — two stagers would race `pending`. Pick one publisher.

`/etc/cron.d/harness-evolve` example (CST):

```
0 23 * * * root /home/agent-runtime-platform/main/scripts/harness-evolve-nightly.sh ingest
30 0 * * * root /home/agent-runtime-platform/main/scripts/harness-evolve-nightly.sh evolve
```

`ingest` here means "read `memory.db` after `harness-kb-nightly.sh ingest` has
run" — schedule it later than 23:00, or call `evolve` only and let `cli cycle`
ingest from `TE_MEMORY_DB`.

## Operator commands

```bash
# pipeline repo
cli status                       # same payload as MCP evolve_status
cli cycle all                    # lock + ingest + observe + harvest + evolve; STAGES
cli kb diff && cli kb approve <digest>

# this repo
harness-ctl kb status|diff|approve|reject
orchestrator kb reseed --path skills/self-evolve
```

## What the sandbox can never do

- Publish or activate an extractor version.
- See the new snapshot in the **current** session (mount is pinned at start).
- Queue behind a running cycle — `evolve_run` returns `skipped`.
