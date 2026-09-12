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
| [turn-abort-proxy-correlation-timeout.md](./turn-abort-proxy-correlation-timeout.md) | Superseded | Root-caused a session self-terminating mid-turn (proxy-correlation timeout race); stopgap config fix shipped 2026-07-08. Orchestrator-side root cause fixed in [runtime-push-loss-and-writer-contention.md](./runtime-push-loss-and-writer-contention.md) |
| [session-stuck-after-restart-no-runtime.md](./session-stuck-after-restart-no-runtime.md) | Draft | 重启后 session 卡在 `running_active` 但 runtime 已死，无自愈、interrupt 够不着（限 shell）；手动改库止血，建议加启动时 recovery |
| [session-runtime-health-and-reconciliation.md](./session-runtime-health-and-reconciliation.md) | Implemented | 统一 session/generation/turn 收敛、bridge producer fencing、维护 watchdog、周期 reconciliation、操作诊断与 agent stop 逃生通道 |
| [bridge-dir-leak-reaper-blocked.md](./bridge-dir-leak-reaper-blocked.md) | Superseded | `run/bridge/gen-*` 目录泄漏（实测 225 个）；teardown 删除与心跳写入竞态致校验「path still exists」，reaper 永不收敛 |
| [runsc-cleanup-hang-blocks-maintenance.md](./runsc-cleanup-hang-blocks-maintenance.md) | Implemented | 沙箱 OOM 留下半死控制面 → `runsc kill` 永不返回（无 per-command 超时）→ 串行的 maintenance 循环整体阻塞 → `startColdFallbackSessions` 停摆，全平台 34 小时无法冷启新对话；监控全绿。止血=先杀沙箱残骸（无需重启） |
| [session-runtime-projection.md](./session-runtime-projection.md) | Implemented | turns 表成为「agent 在做什么/队列里有什么」的唯一真相：持久化 turn phase + 触发器维护的单调 revision + 单一快照契约（REST 与事件同载荷）；修掉正文/队列重复、空闲显示 Thinking、interrupt 后排队 turn 搁死、取消排队无效 |
| [runtime-push-loss-and-writer-contention.md](./runtime-push-loss-and-writer-contention.md) | Implemented | `session.runtime` 推送因 SSE 白名单遗漏被 EventSource 静默丢弃（handler 成死代码）→ 队列项重复执行、刷新才恢复；并从单写连接争用根治伪「模型不可用」：output 批处理（配置此前从无消费者）、热读迁 reader 池、phase 投影不再逐 token 写、turn 租约宽限窗口、`active_context_unavailable` 404→503；并修掉 proxy context 只在 ack 创建（claim→ack 窗口内 agent 的首个模型调用必被拒，排队消息因此稳定失败）|
| [session-and-artifact-sharing.md](./session-and-artifact-sharing.md) | Draft | 会话与 artifact 只读分享：owner/read 闸门分离 + `shareStore` 角色接口 + 身份分享（复用 authed 路由）+ 公开链接（独立未鉴权子树、读走 reader 池、内容寻址快照 + 白名单、trace/artifact 两层可见性）；含 `user rm` 必须撤销 share 的义务 |
| [sandbox-death-inbox-prune-race.md](./sandbox-death-inbox-prune-race.md) | Implemented | 沙箱 3–8 分钟静默死亡：host 每次写响应前清空只读 inbox，与沙箱 `iterdir→open` 竞态，bridge client（容器 init）因 `FileNotFoundError` 退出；死亡无痕（输出零订阅者被丢）且 90s 后被误归为 `orchestrator_restart_*`。修：输出落盘 + 退出钩子即时收敛、inbox 按时效清理且序号单调、client 读取容错 + claim 重试、cleanup 先杀进程并回收 `.host`、去掉行级 `auto_checkpoint_enabled` 谓词、UI 派生 `sandbox` 状态（Idle/Sleeping/Waking）与人话 toast |

Add new proposals here when created; move to `Implemented`/`Rejected`/`Superseded`
as they resolve instead of deleting the row, so the table stays a full history.
