# Session runtime health and reconciliation

- **Status**: Implemented (initial reliability slice)
- **Scope**: bridge producer fencing, atomic failure state, stale-session repair,
  maintenance watchdogs, operator diagnostics, and user-visible stop/recovery

## First-principles model

There are three independent actors: the sandbox producer, the orchestrator, and
the browser. None of them is allowed to be the sole source of truth.

1. The sandbox proves liveness with a heartbeat file and writes durable bridge
   messages.
2. The orchestrator commits turn/generation/session state and a durable event in
   one SQLite transaction.
3. The browser treats SSE as a latency optimization and periodically refreshes
   the session projection while work is active.

The session row is therefore a projection of turn + generation state. A failed
generation must never leave `running_active` pointing at a non-live runtime.

## Historical fixes and what they taught us

The recurring incidents were not unrelated bugs; each fix closed one missing
edge in the same lifecycle protocol:

| Commit | Symptom it fixed | Remaining gap it exposed |
| --- | --- | --- |
| `36e797b` | expired/dead bridge generations blocked recovery | lease expiry alone is not sandbox liveness |
| `8d3975e` | dead sandboxes self-renewed forever | teardown can still block the repair path |
| `eae4117` | unbounded `runsc`/`ip`/`nft` commands | a timed-out command can still leave host remnants |
| `bc31012` | one maintenance stage starved all others | bounded stages still need a durable health signal |
| `d7ec5e8` | cleanup failure skipped DB repair; OOM cause was hidden | failure transitions were still duplicated across paths |
| `4a79700` / `5d867ce` | failed turns incorrectly killed/retried generations | turn outcome and runtime outcome need explicit separation |
| `d109e1c` / `2358de0` / `dd5092a` / `7e49e85` | interrupted work had no recovery UX or had the wrong recovery promise | the browser must not infer health from a spinner alone |
| `9572d02` | scheduler restart orphaned `schedule_runs` | Cron execution needs the same lease/reconciliation contract as chat turns |

The unified design above consolidates these lessons rather than adding another
independent watchdog. New failure paths should call the same transition command
and emit the same durable event shape.

## Failure classes and convergence

| Failure | Durable action | User-visible result |
| --- | --- | --- |
| duplicate bridge output sequence | fail generation + turn; reconcile session; append `generation.error` | spinner stops, session becomes idle or remains explicitly queued for fallback |
| sandbox disappears / heartbeat stalls | stop renewing lease; cleanup, recovery, and reconciliation | queued work is cold-started; running work is marked interrupted |
| orchestrator/SQLite poll stalls | bounded bridge poll context + operator heartbeat diagnostics | worker cannot block forever; `harness-ctl zombies` distinguishes ingest from sandbox failure |
| user stops an agent turn | tear down generation, cancel turns, keep session/workspace | next message starts a fresh generation |

The repair path is idempotent and owner-fenced. Durable events carry the
resulting `session_status`, so live SSE reduction and replay reduction are the
same operation.

## Bridge producer safety

Output sequence numbers are scoped to a turn and generation. A claim-loop
producer takes an exclusive lock in the generation bridge directory and resumes
from both the host's committed sequence boundary and pending outbox files. This
prevents a restart or duplicate process from reusing a sequence already durable
on disk.

## Operational contract

`harness-ctl zombies` is read-only and reports three signals separately:

- sandbox heartbeat freshness (sandbox process liveness),
- database heartbeat freshness (bridge ingest liveness), and
- host heartbeat freshness (maintenance-loop liveness).

The orchestrator also writes a process-level `maintenance-heartbeat` under its
run directory. This is the authoritative loop signal when there are no live
sandboxes, where a per-sandbox host heartbeat cannot exist.

It also reports state drift (`running_active` with a failed/destroyed
generation and no in-flight turns). The orchestrator runs the same reconciliation
automatically on every maintenance pass, including the startup pass.

## Deliberate limits

Fresh sandbox heartbeats are not treated as proof that a model call is making
progress: long CPU/data operations can be silent but healthy. The watchdog only
declares a runtime dead when the sandbox heartbeat/lease contract fails or the
durable state already proves the generation terminal. This avoids killing valid
long-running jobs while still preventing indefinite “thinking” after a dead
process.
