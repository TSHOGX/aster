# Proposal: Slash commands in the workbench chat

**Status:** Draft
**Date:** 2026-07-08
**Scope:** Make `/`-prefixed commands (starting with `/new`) work in the workbench chat input, with a discoverable autocomplete menu — designed as a reusable command layer rather than a one-off `/new` patch.

## Problem

Typing `/new` (or any `/command`) in the workbench chat does nothing useful. The input sends raw text straight to the agent:

- `Composer.submit` passes `value.trim()` verbatim to `onSend` (`frontend/components/workbench/composer.tsx:31`).
- `Conversation` wires `onSend` to `sendMessage` (`frontend/components/workbench/conversation.tsx:119`).
- `sendMessage` POSTs it as the turn body (`frontend/components/harness-provider.tsx:889`) → `POST /api/sessions/:id/messages` → pi turn `content`.

No layer intercepts a leading `/`, so `/new` is delivered to the model as the literal string `"/new"`. There is also no discoverability: even if a command worked, nothing tells the user it exists.

Unlike the Claude Code CLI (where the client owns `/clear`, `/help`, …), this web UI is a thin chat client driving pi headlessly over RPC. pi has **no interactive slash-command layer of its own** in this mode — so slash commands are entirely a frontend/orchestrator concern for us to design.

## Key finding: pi exposes a rich RPC command surface we currently ignore

From `@earendil-works/pi-coding-agent/dist/modes/rpc/rpc-types.d.ts`, pi's RPC protocol supports far more than `prompt`:

`new_session`, `compact`, `abort`, `switch_session`, `fork`, `clone`, `set_model`, `cycle_model`, `cycle_thinking_level`, `set_thinking_level`, `set_auto_compaction`, `get_session_stats`, `get_state`, and notably **`get_commands`** — pi can enumerate its own available commands.

But the bridge runner uses almost none of it. `PiRPCTurnRunner` (`sandbox-image/files/usr/local/bin/harness-bridge-client`) only issues `prompt` per turn, plus `switch_session`/`get_session_stats` for restore. The entire command surface is invisible to the UI today.

This is why the proposal is a *layer*, not a patch: there are two fundamentally different kinds of "command," and conflating them is how this feature rots.

## The hard constraint: turn-only protocol

The sandbox has **no control channel outside a turn grant**. The grant schema is fixed to `turn_input_schema == "RunTurn"` with a single `content` field (`orchestrator/internal/agents/agents.go:196,281`; bridge dispatch at `harness-bridge-client:803-805`). The sandbox only receives work inside a turn and only reports back via `ack_turn_completed` — the same constraint documented in `session-display-names.md`.

Consequence: any command that must actually reach pi (e.g. `/compact`, `/model`) requires either a new turn-input schema or a bridge that maps it into pi RPC calls inside a turn. Commands that only touch orchestrator REST APIs (create/destroy/select session) need none of that.

## Approach: two command tiers behind one registry

Declare every command once in a **command registry** (name, aliases, description, arg spec, and an `available(session)` predicate keyed on status/mode). Both the parser and the autocomplete menu consume the same registry — mirroring how `frontend/lib/agents.ts` derives session options from deployment capabilities. This is the backbone that keeps the feature coherent as commands are added.

**Tier 1 — client-side commands (no sandbox round-trip). Ship first.**
Handled entirely in the frontend against existing REST APIs:

| Command | Action | Backs onto |
|---|---|---|
| `/new` | Create a new agent session and switch to it (true fresh context) | `createSession` (`harness-provider.tsx`) → `POST /api/sessions` (`server.go:399`) |
| `/end` | Destroy the current session (with confirm) | `destroySession` → `DELETE /api/sessions/:id` (`server.go:431`) |
| `/help` | Render the command list inline (from the registry) | none |

`/new` is the command the user actually wants. It is the honest mapping for "fresh context": there is **no** server-side "reset/compact context" endpoint, so an in-place `/clear` that claimed to wipe pi's memory would be misleading. (A view-only `/clear` that just hides local messages while pi keeps its context could be added later, clearly labeled — but it is deliberately not in the first cut.)

**Tier 2 — agent-side commands (must reach pi). Deferred.**
`/compact`, `/model`, `/abort`, `/thinking`, … These require extending the turn protocol — e.g. a new `turn_input_schema` such as `RunCommand`, or a bridge path that maps a command to the corresponding pi RPC (`compact`, `set_model`, `abort`) inside a turn. Deferred because of the `RunTurn`-only constraint above. When built, the menu for this tier should be driven by pi's **`get_commands`** RPC (surfaced through `driver_state` or the deployment capabilities channel) so it isn't hand-maintained.

Note the interrupt asymmetry: `POST /api/sessions/:id/interrupt` exists but is **shell-only** — pi reports `SupportsInterrupt: false` (`agents.go:209`), even though pi RPC has `abort`. So `/abort` for agent sessions is Tier 2, not a quick win.

## UX design for the `/` menu

- **Trigger:** input's first non-space char is `/`. The menu is an anchored popover above the composer (new lightweight component — no popover primitive exists in `frontend/components/ui/` yet).
- **Filter:** live-filter by prefix as the user types after `/`.
- **Keyboard:** ↑/↓ to move, Tab/Enter to complete the highlighted command, Enter on a fully-typed command to run it, Esc to dismiss. Mouse click also completes.
- **Row content:** command name + one-line description + availability. Unavailable commands (e.g. `/end` on a terminal session) are shown greyed with the reason, not hidden — consistent with how `agents.ts` carries `disabledReason`.
- **Unknown-command safety:** an unrecognized `/foo` is **not** silently sent as chat. On submit, either block with a subtle "unknown command" hint or offer "send as message" — never surprise the user by shipping `/foo` to the model.
- **Accessibility:** `role="listbox"`/`role="option"`, `aria-activedescendant` on the textarea, and visible focus styling — matching the repo's stated a11y expectations.

## Recommended first increment

Client-side registry + autocomplete menu + `/new`, `/end`, `/help`. **Zero backend changes.** This delivers the user's actual need (`/new`) and establishes the registry + menu architecture so Tier 2 slots in later without rework.

## Feasibility verification

| Concern | Result | Evidence |
|---|---|---|
| Where to intercept `/` | ✓ | Single choke point: `Composer.submit` (`composer.tsx:31`) before `onSend` |
| `/new` with no backend work | ✓ | `createSession` + `selectSession` already in `harness-provider.tsx` |
| `/end` with no backend work | ✓ | `destroySession` already wired (`sidebar.tsx` uses it) |
| In-place `/clear`/`/compact` | ✗ (Tier 2) | No reset endpoint; `RunTurn`-only grant schema blocks out-of-turn control |
| `/abort` for agents | ✗ (Tier 2) | pi `SupportsInterrupt: false` (`agents.go:209`); interrupt route is shell-only (`server.go:446`) |
| Menu source of truth for Tier 2 | ✓ | pi `get_commands` RPC exists (`rpc-types.d.ts`); expose via `driver_state`/capabilities |
| Registry pattern precedent | ✓ | `frontend/lib/agents.ts` derives options + `disabledReason` from capabilities |

## Open questions

- **`/new` mode:** always create an `agent` session, or offer a mode picker (agent/shell) inline in the menu? Leaning: default to `agent`, revisit if shell is common.
- **Confirmation for `/end`:** inline confirm in the menu vs. the existing sidebar destroy affordance. Should reuse whatever destroy-confirm UX the sidebar already has.
- **Tier 2 transport:** new `RunCommand` turn schema vs. overloading `RunTurn` `content` with a structured marker. The former is cleaner but touches grant schema, bridge, and orchestrator.

## Explicitly out of scope

- Any Tier 2 (agent-side) command implementation — deferred pending the turn-protocol decision.
- A view-only `/clear` — can be added later as a clearly-labeled "clear local view" that does not touch pi's server-side context.
- Server-side context reset/compaction endpoints.
