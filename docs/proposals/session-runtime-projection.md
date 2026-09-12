# Session Runtime Projection: turns 表作为唯一真相

- **Status**: Implemented
- **Date**: 2026-09-11
- **Scope**: `internal/store`（schema/turns 投影）、`internal/server`（快照端点与推送、队列操作）、`frontend`（runtime 状态层、composer/队列 UI）

## 问题

会话「正在做什么」这件事，UI 需要回答三个问题，而每个问题都有多个互相矛盾的来源：

1. **对话里有哪些消息** — `listMessages` 用「队首 queued turn 例外」的相关子查询决定消息是否进入正文，只为补偿 POST 到 bridge 认领之间的空窗。
2. **agent 在干什么** — 三个来源打架：粗粒度的 `sessions.status`（排队时同样是 `running_active`）、客户端事件 FSM、以及从「turn 的最后一条 event」反推 phase。最后这个必然出错：turn 跑工具时最新 event 往往是 `agent.delta`，因此刷新后显示 Responding 而非真实的工具。
3. **队列里有什么** — 三个写者且不收敛：`conversation.tsx` 的本地 `queued` 数组（id 用时间戳）、`runtime.queued_turns`（id 是真 turn id）、`message.queued`。本地项在 POST resolve 时删除，durable 项要等下一次 refresh，于是**同一条消息同时出现在正文和排队列表**，且排空的条目残留。按内容字符串去重的两个 ref 是这个结构缺陷的症状，不是修复。

此外还有两个真实的正确性 bug（不只是显示问题）：

- **排队 turn 搁死**：`InterruptSession` 曾被改成只取消 `leased`/`running`。但 interrupt 会销毁 generation 并把 session 置为 `running_idle`，而 `ClaimNextTurn` 只在活跃 generation 上授予 turn —— 残留的 queued turn 永远无法被认领，直到某次后续 POST 顺手分配了 generation，然后用户几分钟前放弃的 prompt 突然执行。
- **取消排队无效**：队列面板的「×」只改本地数组，服务端那一轮照跑。

## 方案

**turn 的阶段成为持久化事实，而不是推断结果；客户端是纯渲染器。**

### 1. Store：持久化投影 + 单调 revision

- `turns` 新增 `phase` / `phase_at` / `phase_tool` / `phase_detail`；`sessions` 新增 `turn_revision`。
- `SetTurnPhase` 是 phase 的唯一写者，限定 `leased`/`running`（迟到的帧不能复活已结束的 turn），并在 WHERE 中比较新旧值——重复帧是零行更新。
- **revision 由数据库触发器维护**，不在调用点手工 bump。turns 有十余处写者，其中若干在 recovery/reaper 路径；漏 bump 会产生「永不消失的队列行」这种只表现为 UI 困惑、无任何显式失败的 bug。触发器写在 `ensureTurnColumns` 而非主 DDL，因为 WHEN 子句引用了刚添加的 phase 列。
- `listMessages` 恢复扁平规则：`turn_id IS NULL OR turn.status <> 'queued'`，删除两段相关子查询与 `Message.Queued`。
- 新增 `CancelQueuedTurn`（CAS queued→canceled，并删除其 message 行——该 turn 从未被提问，留在正文会读作未被回答的 prompt）。
- `InterruptSession` 恢复取消 queued turn，返回 `CanceledQueued`。

### 2. Server：一个快照契约

- `GET /api/sessions/{id}/runtime` 与 `session.runtime` 事件**载荷完全相同**，客户端只有一种形状要处理。
- `POST .../messages` 的响应同时带回 message 与 `runtime`，客户端一次原子更新落地——不再需要「发完再 GET」，而那个 GET 可能在 worker 认领之后才到达。
- `DELETE /api/sessions/{id}/turns/{turnId}` 取消排队项；被认领后返回 409（`turn_not_cancelable`）。
- streamParser 在粗粒度边界写 phase（内存去重，delta 不写）。`toolPhaseDetail` 的键顺序与 80 字符截断刻意与前端 `lib/trace.ts` 一致，否则同一个工具在实时与刷新后会显示不同标签。

### 3. Frontend：快照即真相

`lib/session-runtime.ts` 承载两条 load-bearing 规则，作为纯函数以便直接测试：

- **revision 守卫**：只应用严格更新的快照。请求与推送持续竞争，旧响应会复活已排空的队列条目，而此后再无任何东西否证它。
- **activity 从属于快照**：没有 active turn 就是 idle，实时事件只能细化快照声明为运行中的 turn 的 phase，不能凭空创造。这条规则以构造方式消灭「空闲 session 显示 Thinking」整类问题，而不是逐个修补可能留下残余 phase 的事件路径。

删除：本地 `queued` 数组、`queuePaused`、`queuedDispatchRef`、两个内容去重 ref、自动派发 effect、收敛 effect、`sendMessage` 的乐观消息拼接与 `options.queued`。

**composer 按钮**：仅在输入框为空且有工作在飞时显示 Stop；一旦有文字就始终是发送。此前的单按钮切换会让「agent 忙 + 已输入文字」时点击变成停止，恰好拒绝用户正在进行的动作（排队）。

## 时序说明

`sendMessage` 之后约一个 RTT 内没有 thinking 指示器。这是诚实的：用户消息气泡立即出现，而 POST 响应带回的快照才是第一个可信的「已开始」信号。这正是对「消息还没显示就 thinking」的正面修复，而不是用乐观状态盖住。

## 验证

- `go test ./...`（20 包全绿，需 root：data-volume evidence 要求 root 所属）
- `runtime_snapshot_migration_test.go`：对退化到旧 schema 的库重新 Open，验证迁移成功、旧行可读、触发器恢复、且迁移幂等。此项专门针对历史上「ALTER 时机错误只在既有库上造成崩溃循环」的教训。
- `internal/store`：revision 单调性、重复帧不推进 revision、迟到帧不复活终态 turn、取消排队、interrupt 连带取消并清理 message、正文可见性规则。
- `internal/server`：send 响应契约、取消排队（含 409 竞争）、interrupt 上报丢弃数量。
- `frontend`：`npm test`（16 文件 215 测试）、`tsc --noEmit`、`lint`、`build`；`session-runtime.test.ts` 覆盖上述两条规则。
