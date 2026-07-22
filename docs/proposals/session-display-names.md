# Proposal: Semantic session display names

**Status:** Implemented
**Date:** 2026-07-07 (implemented 2026-07-22)
**Scope:** Give sessions a human-readable name instead of showing the raw `sess_…` id in the workbench sidebar.

## Problem

Every session is created with a random id (`newID("sess")`) and the sidebar rendered
that id verbatim as the row title. The `sessions` table had no title column, so there
was nothing more meaningful to show — users saw a wall of opaque `sess_…` strings and
could not tell sessions apart.

## Shipped design

Titles are stored and served by the orchestrator, generated automatically and
editable by the user:

- **Storage.** `sessions` gains two columns (`orchestrator/internal/store/schema.go`):
  `title` (display name, empty until set) and `title_custom` (1 once the user renames,
  so automatic naming never overwrites a manual title). Added via an idempotent
  `ALTER TABLE` migration so existing databases upgrade in place.
- **Automatic naming (two-stage).** On the **first** message of an untitled session,
  `POST /api/sessions/{id}/messages` immediately sets a placeholder title — the
  first message collapsed and truncated (`maybeSetPlaceholderTitle`) — so the sidebar
  is meaningful at once. If a title LLM is configured, it then generates a concise
  title asynchronously (`generateLLMTitle`, off the request path) and replaces the
  placeholder. Both writes use `UpdateSessionTitle(..., custom=false)`, which is a
  no-op once `title_custom=1`.
- **Manual rename.** `PATCH /api/sessions/{id}` (`{"title":"..."}`) validates and
  stores the title with `custom=true`. Ownership is enforced like every other session
  route (404 on mismatch).
- **The title LLM** is a plain OpenAI-compatible endpoint (`internal/titler`),
  configured from the gitignored `config/title-llm.yaml`
  ([configuration.md](../configuration.md)). It is a **host-side call, wholly
  independent of the model proxy and the sandbox** — see the constraint note below for
  why this path was chosen over routing through the sandbox.
- **Delivery.** `publicSession` exposes `title`; a `session.updated` SSE event carries
  the full session on every title change. The frontend renders `title || id`, offers
  inline rename + a `…` menu (`frontend/components/workbench/sidebar.tsx`), and merges
  `session.updated` in the provider.

## Why host-side generation, not the sandbox

An earlier draft routed naming through the pi agent (its `set_session_name` RPC,
generating inside the sandbox bridge during the first turn). That was rejected: the
sandbox protocol is **turn-driven with no independent control channel** — the sandbox
only receives work inside a turn grant and only reports back via `ack_turn_completed`.
Titling through it couples naming to turn execution and the pi trace, and manual
rename (an out-of-turn action) does not fit at all. A host-side titler sidesteps this
entirely: it needs no turn context, works for shell sessions too, and keeps model
credentials on the host. (This same turn-grant constraint shapes
[workbench-slash-commands.md](./workbench-slash-commands.md).)

## Not done

- **Semantic re-titling on topic drift.** Titles are generated once from the first
  message; a session that changes topic keeps its original title unless the user
  renames it.
- **Long first messages** occasionally yield a weak auto-title (the model may not
  condense a very long prompt well). These are non-custom, so a rename or a future
  re-run fixes them.
