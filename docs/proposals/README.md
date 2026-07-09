# Proposals Index

Lightweight RFCs: one Markdown file per proposal/investigation, no separate
tracker. Each file starts with `Status` / `Date` / `Scope` fields (see existing
files for the format). `Status` uses one of:

| Status | Meaning |
|---|---|
| `Draft` | Written up, not yet reviewed |
| `Reviewing` | Under discussion, direction not locked |
| `Accepted` | Direction locked, implementation may be partial or landed as a stopgap |
| `Implemented` | Fully landed |
| `Rejected` | Will not do |
| `Superseded` | Replaced by a later proposal (link it) |

## Current proposals

| Proposal | Status | Summary |
|---|---|---|
| [session-display-names.md](./session-display-names.md) | Draft | Auto-generate a short session title after the first turn via pi's `set_session_name`, surface it in the sidebar |
| [workbench-slash-commands.md](./workbench-slash-commands.md) | Draft | `/`-prefixed commands in the chat composer (`/new`, `/end`, `/help` first; agent-side commands like `/compact` deferred) |
| [token-usage-in-activity-indicator.md](./token-usage-in-activity-indicator.md) | Reviewing | Surface per-turn token usage in the activity indicator; usage data currently dead-ends before reaching orchestrator or frontend |
| [turn-abort-proxy-correlation-timeout.md](./turn-abort-proxy-correlation-timeout.md) | Accepted | Root-caused a session self-terminating mid-turn (proxy-correlation timeout race); stopgap config fix shipped 2026-07-08, orchestrator-side fix still open |

Add new proposals here when created; move to `Implemented`/`Rejected`/`Superseded`
as they resolve instead of deleting the row, so the table stays a full history.
