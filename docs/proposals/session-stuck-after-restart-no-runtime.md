# 排查记录：orchestrator 重启后 session 卡死「running_active 但 runtime 已死」，无自愈、无法终止

- **Status**: Draft
- **Date**: 2026-07-24
- **Scope**: orchestrator session/turn 生命周期、租约回收、interrupt 能力边界

## 现象

重启 orchestrator 后，个别会话的前端一直显示「agent is thinking」，既不出结果、也无法终止。
用户被彻底卡住，只能弃用该会话。

本次实例（publish 环境）：`sess_S00QBuAOMGlqTRGK`（turn 394）与 `sess_5PnMHex9zBbRWIyi`（turn 403）。

## 排查路径

只读盘点在途 turn（`turns.status IN ('queued','leased','running')`）并与活容器交叉比对：

| turn | session | turn 状态 | session 状态 | generation | gen 状态 | 有活 runsc 容器？ | 判定 |
|---|---|---|---|---|---|---|---|
| 394 | `sess_S00QBuAOMGlqTRGK` | `leased`（租约过期） | `running_active` | `gen_81279ad8` | `active` | 否 | 死 |
| 403 | `sess_5PnMHex9zBbRWIyi` | `queued` | `running_active` | `gen_83004d53` | `idle` | 否 | 死 |
| 405 | （其它用户） | `running` | `running_idle` | `gen_e693b6a8` | — | 是 | 真活跃 |

死会话的共同特征：session 仍是 `running_active`，`active_generation_id` 指向的 generation
在 `runtime_generations` 里还标 `active`/`idle`，但**没有对应的 runsc 容器**（重启时进程随之消失）。

## 根因：三处「活着的假象」叠加，且没有任何逻辑能纠正它

### 1. 重启不回收在途 turn / 不降级 session 状态
orchestrator 重启时，正在跑的 turn 的进程被杀，但 DB 里 turn 仍停在 `leased`/`running`、
session 仍停在 `running_active`。没有启动时的 recovery 把「本代 runtime 已不存在」这件事写回状态。

### 2. 自动清理器够不着（`store.go:444` `CancelTerminalSessionPendingTurns`）
维护循环（`maintenance.go:55`）会取消 pending turn，但其 `WHERE` 要求 **session 处于终态**
（`destroyed`/`failed`）。卡死会话是 `running_active`——非终态，落在覆盖范围外，永远不被清。

### 3. interrupt 只支持 shell（`server.go:1564`）
```go
if session.Agent != string(agents.Shell) {
    writeError(w, http.StatusConflict, "interrupt is only supported for shell sessions")
}
```
卡死的是 Pi（agent）会话，`POST /api/sessions/{id}/interrupt` 直接 409，这解释了前端「无法终止」。
唯一能收尾的公开入口是 `DELETE /api/sessions/{id}`（销毁整个会话），语义过重（删除会话），
没有「只终止当前 turn、保留会话」的能力。

### 为什么单改 turn 状态不够（发消息路径的陷阱）
`sendMessage`（`server.go:734`）会先判 `CanAcceptInput`：只有 `Created`/`RunningIdle`/`Checkpointed`
放行，`RunningActive` 归为 `IsBusy` → 返回「session is busy」，用户连 continue 都发不出。
即便把 session 降为 `running_idle`，`ensureActiveGeneration`（`generation_lifecycle.go:660`）
对状态为 `active`/`idle`（非 `failed`/`checkpointed`）的 generation 会**直接复用**（`IsNew:false`），
拿一个没有容器的 generation 去用 → 仍然卡。只有 generation 为 `failed` 时才走 `AllocateGeneration` 重建。

## 本次止血（手动改库，2026-07-24）

`.backup` 全库后，单事务对 394/403（不碰 405/活容器）改三处：
1. `turns`：在途 turn → `canceled`（比照 `CancelTerminalSessionPendingTurns` 填全 completed/error/清租约字段），`error_class='session_interrupted_restart'`。
2. `sessions`：`running_active` → `running_idle`（放开 `CanAcceptInput`）。
3. `runtime_generations`：死 generation `active`/`idle` → `failed`（触发下次发消息时 `AllocateGeneration` 重建 sandbox）。

结果：两会话脱离在途，用户可发新消息续用；405 与活容器未受影响。备份留在
`/var/lib/harness-publish/state/orchestrator.db.bak.20260724-143559`。

> ⚠️ 未重启 orchestrator（当时有活跃用户），故后端内存态可能短时滞后于库；`sendMessage` 开头
> 会重读库授权，通常能读到新状态。若仍报 busy，多为内存缓存未刷新。

## 尚待跟进的修复建议（按优先级）

1. **启动时 recovery（治本）**：orchestrator 启动时，对「session=running_active 但 active
   generation 无存活 runtime」的记录做一次纠正——把在途 turn 置 `canceled`、session 降 `running_idle`、
   死 generation 置 `failed`（或按 checkpoint 走 restore）。即本次手动事务的自动化版本。
2. **扩大自动清理覆盖**：让维护循环也能处理「非终态 session + 租约过期且无活 runtime」的在途 turn，
   而不仅限终态 session。
3. **给 agent 会话一个「终止当前 turn」入口**：现在 interrupt 限 shell、destroy 过重，中间是空的。
   前端「无法终止」的体验缺口需要一个 turn 级取消 API（保留会话）。
4. **前端**：turn 长时间无进展 / 租约过期时，给出可点击的「终止并重试」而非无限 thinking。

## 关键证据位置（供复核）

- 判定逻辑与实测：`harness-ctl inflight`（区分真活跃 vs 过期租约僵尸），见 [../../scripts/README.md](../../scripts/README.md)。
- `CanAcceptInput`/`IsBusy`：`orchestrator/internal/sessionstate/state.go:52,61`
- 自动清理器：`orchestrator/internal/store/store.go:444`、`orchestrator/internal/server/maintenance.go:55`
- interrupt 限制：`orchestrator/internal/server/server.go:1564`
- 发消息 / generation 复用：`orchestrator/internal/server/server.go:734`、`orchestrator/internal/server/generation_lifecycle.go:660`
- 相关：bridge 目录泄漏拖累回收，见 [bridge-dir-leak-reaper-blocked.md](./bridge-dir-leak-reaper-blocked.md)；
  重启打断 turn 的另一形态见 [turn-abort-proxy-correlation-timeout.md](./turn-abort-proxy-correlation-timeout.md)。
