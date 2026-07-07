# Proposal: Semantic session display names

**Status:** Draft
**Date:** 2026-07-07
**Scope:** Give sessions a human-readable name instead of showing the raw `sess_…` id in the workbench sidebar.

## Problem

Every session is created with a random id (`newID("sess")`, `orchestrator/internal/server/server.go:556`) and the sidebar renders that id verbatim as the row title (`frontend/components/workbench/sidebar.tsx:36`). The `sessions` table has no name/title column, so there is nothing more meaningful to show. Users see a wall of opaque `sess_…` strings and cannot tell sessions apart.

## Key finding: pi already has a naming channel

The pi coding agent (`@earendil-works/pi-coding-agent`, installed in the sandbox rootfs) natively supports session display names. We do **not** need to invent a "name from the first sentence" scheme — pi owns the storage and retrieval, we only supply the content.

- `set_session_name` RPC appends a `session_info` entry (`type: "session_info"`, `name?`) into the pi session trace. It persists in the trace, so it survives checkpoint/restore.
- `get_state` RPC returns `RpcSessionState.sessionName`, read from the latest `session_info` entry via `getSessionName()`.
- pi has **no** automatic naming: `sessionName` is empty until something explicitly calls `set_session_name` (RPC, the interactive `/name` command, or an extension). So the name *content* is our responsibility; pi is purely the store/fetch channel.

## Approach

Chosen direction: **auto-generate a short title after the first turn, write it through the pi channel, surface it in the frontend. Manual rename is deferred.**

Name content comes from a one-shot LLM summary of the first turn (not first-sentence truncation, which was explicitly rejected).

### Flow

1. First turn runs normally in the sandbox bridge.
2. In `run_turn`, after the turn succeeds but **before** `ack_turn_completed`, the bridge checks whether the session already has a name. If empty, it makes one model call (via the proxy it already reaches at `ANTHROPIC_BASE_URL`) to produce a short title.
3. Bridge calls `set_session_name` (writes into the pi trace), then `get_state` to read `sessionName` back.
4. Bridge includes `session_name` in the `driver_state_update` payload it returns.
5. Orchestrator, on `CompleteTurn`, extracts `session_name` into a new `sessions.title` column.
6. API `publicSession` emits `title`; frontend `ApiSession.title` drives the sidebar (`title || id`).

## Feasibility verification (all clear)

| Concern | Result | Evidence |
|---|---|---|
| pi store/fetch of a name | ✓ | `set_session_name` writes `session_info`; `get_state` returns `sessionName`; persists across checkpoint/restore |
| pi auto-naming exists? | ✗ (expected) | No auto-naming; content must be supplied by us |
| bridge can call the LLM | ✓ | Bridge holds `ANTHROPIC_BASE_URL`; proxy authorizes by sandbox source-IP + running turn, not a per-request key |
| authorization window | ✓ | Proxy requires turn `status='running'` and exactly one in-flight turn (`orchestrator/internal/store/proxy.go:117,130`); generating before `ack_turn_completed` satisfies it |
| adding `session_name` to driver_state | ✓ | Canonicalization only sorts keys, does not reject unknown fields (`data_volumes.go:1003`); bridge and orchestrator both digest with sorted keys, so digests still match |
| strict host validation | ✓ | Host validator only checks the existing required fields (`driver_state_pi.go`); `session_name` is a safe pass-through field |
| persist + API + frontend | ✓ | Add `sessions.title` column → `publicSession` → `ApiSession.title` → sidebar `title || id` |

The one hard architectural constraint: the sandbox protocol is turn-driven with no independent control channel — the sandbox only receives instructions inside a turn grant and only reports back via `ack_turn_completed`. The auto-generation flow fits this (it rides the existing turn), which is why manual rename (which would need out-of-turn dispatch) is deferred.

## Files to change (6)

1. `sandbox-image/files/usr/local/bin/harness-bridge-client` — in `run_turn`, add "first-turn title generation + `set_session_name`"; add `session_name` to `_driver_state_update`.
2. `orchestrator/internal/store/driver_state_pi.go` — accept `session_name` in the pi payload validator (optional pass-through field).
3. `orchestrator/internal/store/schema.go` — add `title` column to `sessions`.
4. `orchestrator/internal/store/resources.go` — in `CompleteTurn`, write `session_name` into `sessions.title`.
5. `orchestrator/internal/server/server.go` (`publicSession`) — expose `title` in the DTO.
6. `frontend/lib/types.ts` + `frontend/components/workbench/sidebar.tsx` — add `title` to `ApiSession`; render `title || id`.

## Open questions

- **Generation timing:** generate once on the first turn only, or retry every turn while the name is empty? (Leaning: first turn only, to save model calls.)
- **Shell mode:** non-pi shell sessions have no pi trace, so this path does not apply — they would keep showing the raw id. Acceptable, or should the orchestrator provide a fallback name for shell sessions?

## Explicitly out of scope

- Manual rename from the frontend. Would require either a new lightweight "SetName" turn type (bridge calls `set_session_name` without running the agent — keeps pi trace authoritative but larger change to the grant schema and turn path) or an orchestrator-only `sessions.title` write (simpler but risks divergence from the pi trace on restore). Revisit after the auto-generation path lands.
