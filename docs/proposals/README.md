# Proposals Index

Lightweight RFCs: one Markdown file per proposal/investigation, no separate
tracker (see [../release-process.md](../release-process.md) for how this fits
into the broader requirements/release process). Each file starts with
`Status` / `Date` / `Scope` fields (see existing files for the format).
`Status` uses one of:

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
| [session-display-names.md](./session-display-names.md) | Implemented | Session titles: placeholder from the first message + async host-side LLM refinement, manual rename via `PATCH /api/sessions/{id}`, shown in the sidebar |
| [workbench-slash-commands.md](./workbench-slash-commands.md) | Draft | `/`-prefixed commands in the chat composer (`/new`, `/end`, `/help` first; agent-side commands like `/compact` deferred) |
| [token-usage-in-activity-indicator.md](./token-usage-in-activity-indicator.md) | Reviewing | Surface per-turn token usage in the activity indicator; usage data currently dead-ends before reaching orchestrator or frontend |
| [turn-abort-proxy-correlation-timeout.md](./turn-abort-proxy-correlation-timeout.md) | Accepted | Root-caused a session self-terminating mid-turn (proxy-correlation timeout race); stopgap config fix shipped 2026-07-08, orchestrator-side fix still open |
| [session-stuck-after-restart-no-runtime.md](./session-stuck-after-restart-no-runtime.md) | Draft | 重启后 session 卡在 `running_active` 但 runtime 已死，无自愈、interrupt 够不着（限 shell）；手动改库止血，建议加启动时 recovery |
| [bridge-dir-leak-reaper-blocked.md](./bridge-dir-leak-reaper-blocked.md) | Draft | `run/bridge/gen-*` 目录泄漏（实测 225 个）；teardown 删除与心跳写入竞态致校验「path still exists」，reaper 永不收敛 |

Add new proposals here when created; move to `Implemented`/`Rejected`/`Superseded`
as they resolve instead of deleting the row, so the table stays a full history.
