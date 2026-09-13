# Cron Agent：misfire 全量补跑、停用不生效、以及一个写不进库的状态

- **Status**: Implemented
- **Date**: 2026-09-13
- **Scope**: `internal/server`（misfire 策略、启动重锚、worker 准入闸门、claim 顺序、停用即退役积压）、`internal/store`（`status` CHECK 拓宽 + 表重建迁移、`enabled` 谓词、积压计数与取消、错误不再吞掉）、`internal/config`（`misfire_grace`、`max_pending_runs_per_schedule`）

## 问题

上一轮沙箱生命周期排查的收尾发现 Cron Agent 有两个缺陷，做了止血（三个 schedule 全部
`enabled=0`，47 个积压 run 标为 `canceled`）但没有修：

1. 环境停机约一天后重启，调度器开始逐分钟补跑昨天的窗口，每个补跑冷启动一个沙箱、跑真实模型
   调用、烧真实 token。实测 10 分钟内积压从 36 涨到 47，**且还在涨**。
2. `PATCH enabled=false` 之后 schedule 又跑了三四轮，`next_run_at` 停在昨天。取消库里的 queued
   run 也没用。最后只能靠重启丢弃内存 channel 才真正停下。

排查中又发现两个未被报告的缺陷（其中一个是活跃回归），见下文三、四。

## 取证

### 一、misfire 全量补跑

机制在 `dispatch()`：`ListDueSchedules` 取 `next_run_at <= now` 的 schedule，然后
`next := spec.Next(scheduled)` —— 从**上一个错过的时刻**推进一格，而不是从 `now` 推进。用库里的
真实数据（`next_run_at` 卡在 `2026-09-11T05:40:00Z`，重启于 `09-12T05:47Z`）跑这段算术：

```
current code: next_run_at 2026-09-11T05:40:00Z -> 2026-09-11T05:41:00Z (advance = 1m0s)
  still <= now? true  => immediately due again next tick
  backlog runs to walk through: 1447
proposed skip: -> 2026-09-12T05:48:00Z (1 run, no backlog)
```

**追不上是算术必然，不是负载巧合。** 库里 33 个有时长的 succeeded run，近期真实执行 304–455s：

```
scheduled_at          started_at                      finished_at                     dur_s
2026-09-11T05:24:00Z  2026-09-12T05:46:01.889277748Z  2026-09-12T05:52:14.364569657Z  372.5
2026-09-11T04:51:00Z  2026-09-12T05:43:06.725648749Z  2026-09-12T05:49:43.848459084Z  397.1
2026-09-11T04:47:00Z  2026-09-12T05:29:41.527061083Z  2026-09-12T05:37:17.220842965Z  455.7
```

2 worker ÷ ~370s ≈ **0.32 run/min**，而窗口以 **1.0/min** 产生。缺口 3×，积压只能单调涨。

### 二、停用不中断已入队的 run

`ClaimScheduleDue` 和 `ListDueSchedules` 都正确带了 `enabled=1`，但 claim 之后 run 进了内存里的
`jobs` channel（`make(chan store.ScheduleRun, workers*2)`，容量 4）。改库动不了它们：
`executeScheduleRun` 拿到 run 后只检查 `sched.Archived`，不检查 `sched.Enabled`，也不重读 run 自己的
status。

**channel 只是第一层。** `ListQueuedScheduleRuns` 的谓词是
`r.status='queued' AND a.archived=0` —— **没有 `enabled=1`**。所以停用之后，下一个 tick 会把库里
剩下的 queued run 重新捞出来重新入队。实测：

```
after disabling schedule: ListQueuedScheduleRuns returned 1 rows
```

`ClaimScheduleDue`/`ListDueSchedules` 查了 `enabled`，`ListQueuedScheduleRuns` 没查 —— 三个谓词里
漏一个，"关闭"就不成立。

### 三、`skipped_busy` 违反 CHECK 约束 → 热循环烧 token（活跃回归）

`50ebd26`（2026-09-11 12:37，就在止血当天）引入了 `skipped_busy` 状态，但 `schema.go` 的
`status CHECK` 从未学会这个值。实测：

```
FinishScheduleRun(skipped_busy) err = constraint failed: CHECK constraint failed: status IN (...)
run status after finish attempt = "queued"
ListQueuedScheduleRuns returned 1 rows (infinite redispatch if >0)
```

写入失败 → `finishScheduleFailureStatus` 用 `_ =` 丢掉返回值 → run 永远停在 `queued` → 下个 tick
（5s）再捞出来 → 冷启动沙箱 + 真实模型调用。**每 5 秒一次。**

这正是上一轮示范的那种缺陷形态：一处改动的副作用，在别处以完全不同的症状爆发，且中间环节
（`_ =`）把证据丢掉了。它此前不可达 —— 42 个 `failed`/`enqueue`/`session cannot accept input`
全部写于 `50ebd26` 之前，那时这条路径还写 `failed`，能落库。前端 `lib/types.ts` 与两份 i18n 都已
包含 `skipped_busy`，唯独 schema 没跟上。

### 四、`existing_session` 把补跑放大成必然失败

用户的 schedule 是 `result_mode=existing_session`，指向单个 session。42 个 `enqueue` 失败全是
`session cannot accept input`：补跑挤在同一个 session 上，前一个还在跑，后面每一个都撞 busy。

这不是独立缺陷，是缺陷一的放大器 —— 但它说明 `skipped_busy` 是**高频路径**，不是边角。缺陷三
因此不是理论风险。

## 根因

四个缺陷共享一个形状：**权威状态与执行决策之间存在时空落差，而落差里没有复查。**

- misfire：游标从"错过的时刻"推进，而不是从"现在"推进 —— 决策依据的是历史而非当下。
- 停用：run 行被**拷贝**进 channel，快照从入队那一刻起就不再跟踪数据库。
- `skipped_busy`：写失败的事实被 `_ =` 丢弃，调用方以为成功。
- claim 顺序：`EnqueueTurnMessage` 先于 `StartScheduleRun` 的 CAS，副作用先于检查，
  所以 CAS 失败时 prompt 已经问出去了 —— 取消**无法**被兑现。

## 方案

### A. misfire：跳过 + grace 窗口（`internal/server` + `internal/config`）

`planScheduleTick` 是一个纯函数，承载全部 misfire 策略：

- 迟到在 `misfire_grace` 内 → 照常执行该窗口，游标从窗口本身推进（没真正错过时保持节奏对齐）。
- 超出 grace → 放弃该窗口，游标跳到 `spec.Next(now)`，并记一条带窗口计数的日志。
- **`Next` 永远从 `now` 计算，永不从错过的窗口计算** —— 这是终止爬行的不变式。每个 tick 最多欠
  一个 run，所以停机一分钟和停机一个月代价相同。

方向定为 skip，理由不是"用户这个 schedule 适合 skip"，而是 cron 表达的是"在这些时刻做"，不是
"做这么多次"：时刻过去了，那次执行的语义前提就不存在了。grace 的存在只是为了让跨过分钟边界的
重启不要静默丢掉它落在的那一分钟。

**策略放 config，不放数据行。** 上一轮刚拆掉 `auto_checkpoint_enabled` 那种"把默认值冻进每一行"
的设计（见那份提案 C 节），per-schedule `misfire_policy` 会重犯同样的错：老行冻结旧默认值，且给
三个语义组合去测。真需要 per-schedule 覆盖时，正确形态是**可空的 override 列**，而不是默认值的
副本。默认 `90s`：够吸收一次普通重启，远小于任何有意义的 cron 周期。

跳过**不写 run 行** —— 那等于把积压从 channel 搬进表。

启动时 `recoverStaleScheduleCursors` 一次性重锚所有过期游标，而不是让 poll 循环一格一格爬 ——
后者正是缺陷一的爬行路径。

### B. 停用传播：channel 只传 run id（`internal/server` + `internal/store`）

`jobs chan store.ScheduleRun` → `jobs chan string`。`startScheduleRunFromQueue` 成为 worker 池的
单一准入闸门，在任何工作开始前重读 run status + schedule `enabled`/`archived`。

**权威读必须发生在 worker 里**，因为 dispatch 与 execution 之间的间隙正是用户决策落地的地方。

配套三处：

- `ListQueuedScheduleRuns` 补 `a.enabled=1`（缺陷二的持久化那一半）。
- `ClaimScheduleRunStart` 的 CAS **前移到 session/沙箱创建之前**，`AttachScheduleRunTurn` 事后补记
  session/turn。旧顺序里副作用先于检查；新顺序里输掉 CAS 不花任何代价。
- `StartScheduleRun`/`FinishScheduleRun` 返回 `ErrScheduleRunNotOpen`，不再吞掉写失败 —— 这是让
  证据活下来的那一环。若 run 在 enqueue 期间被取消，排队的 turn 会被 `CancelQueuedTurn` 撤回，
  而不是留给 agent。

API 层 `PATCH enabled=false`（或 archived）立即退役 queued 积压，避免日后重新启用时释放一批陈旧
run。

### C. 积压上限（`internal/config`）

`max_pending_runs_per_schedule`（默认 3）同时约束 cron 路径、retry 路径和手动 run。超限时 cron
路径推进游标而不 claim，手动路径返回 429。理由：`existing_session` 下积压本就自我挫败 —— 每个
排队的 run 都会发现 session 忙于它前面那个。

### D. `skipped_busy` 入库（`internal/store`）

`status` CHECK 加入 `skipped_busy`。**拓宽 CHECK 需要重建表**：`users.role` 的先例（用
`BEFORE INSERT/UPDATE` 触发器代替 ALTER TABLE 加不了的 CHECK）只在**收紧**方向成立 —— 触发器能
拒绝更多值，永远不能放行更少。旧 CHECK 编译在表定义里，实测无论旁边有什么触发器都继续拒绝：

```
--- old CHECK still rejects skipped_busy even with a trigger present ---
Error: stepping, CHECK constraint failed: status IN ('queued','canceled') (19)
```

重建在 FK 开启下安全：没有任何表引用 `schedule_runs`，所以 drop/rename 不会孤立子行或改写别人的
FK 子句；`migrate()` 跑在 writer 上、早于 reader 池打开，没有 reader 能观察到中间态。

## 验证

Go 全 20 包通过（`sudo -n env "PATH=$PATH" "HOME=$HOME" go test ./...`），`go vet` 干净，五个
commit 各自独立可构建。新增回归：misfire 精确复现事故形状（每分钟 schedule 停机一天 → 不执行、
游标严格晚于 now、1448 个被跳过的窗口）、grace 边界两侧 + hourly/daily、停用传播五例、
`skipped_busy` 往返、旧库重建后行/索引/FK 完整。

### 开发环境实测

迁移在真实库副本上先行验证：132 行全保留、4 个索引恢复、`foreign_keys=1`、
`foreign_key_check` 0 违规、`skipped_busy` 落库、二次打开幂等。

**停机重启不再全量补跑。** 造一个 `next_run_at` 停在 24 小时前的 schedule（欠 1440 个窗口）后重启：

```
level=WARN msg="cron cursor re-anchored after downtime" schedule_id=sched_VERIFY_misfire
  stale_next_run_at=2026-09-12T08:59:00Z skipped_windows=1441 resumed_at=2026-09-13T09:00:00.000Z
level=INFO msg="cron schedule claimed" schedule_id=sched_VERIFY_misfire run_id=srun_zJ9RFoeNka_ht0-d
  scheduled_at=2026-09-13T09:00:00.000Z next_run_at=2026-09-13T09:01:00.000Z
```

启动时一次写入跳过 1441 个窗口，只创建 **1** 个 run（当前窗口），而非 1440 个。

**停用立即中断已入队 run。** 在 2 个 run 处于 `running`、1 个仍 `queued` 时 `PATCH enabled=false`：

```
srun_VQ1  succeeded          sess_92Xm2SM9AUnbsYtj
srun_VQ2  succeeded          sess_469abpBRu-eAUh4N
srun_VQ3  canceled   schedule_disabled  schedule was disabled   (session_id 为空)

level=INFO msg="schedule backlog canceled" schedule_id=sched_VERIFY_misfire canceled_runs=1
level=INFO msg="schedule run dropped: no longer pending" run_id=srun_VQ3 status=canceled
```

两层防护在同一条 trace 里可见：库里即时取消，然后**已在 channel 里**的 worker 重读状态并丢弃它 ——
`session_id` 为空，即没有冷启动沙箱、没有 token 消耗。已 running 的 run 各自干净收尾。

另一轮：停用后 09:02、09:03 两个窗口过去，**零新 run**，游标冻结在 version 5（旧行为会再跑三四轮）。
停用期间注入的 3 个 queued run 静置 12s（2+ 个 poll 周期）无人问津。

测试用 schedule 与 run 已删除，库回到基线（132 run、三个 schedule 全 `enabled=0`）。用户的
`sched_LJK8-nkqydl9XGgv` 未被改动（version 136），**未自行重新启用** —— 是否启用由用户决定。

## 未做 / 后续

- **验证残留 4 个 checkpointed session**（`verify-misfire · ...`）。已空闲释放、无活沙箱，删除
  session 属破坏性操作，留给用户决定。
- **`existing_session` 的 busy 语义**：`skipped_busy` 现在能落库了，但"目标 session 忙就跳过"对
  每分钟级 schedule 意味着大部分窗口都会跳过。这是配置选择的后果而非缺陷，不过 UI 也许该在
  schedule 层面提示这种组合。
- **per-schedule misfire override**：真需要时按可空 override 列做，不要复制默认值。
- 132 行的 `schedule_runs` 没有保留策略；长期运行下值得一个裁剪窗口。
