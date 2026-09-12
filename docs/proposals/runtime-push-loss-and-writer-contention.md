# 运行时推送丢帧与写锁争用：队列重复执行 + 伪「模型不可用」

- **Status**: Implemented
- **Date**: 2026-09-12
- **Scope**: `frontend`（SSE 事件白名单、runtime 兜底收窄）、`internal/bridge`（output 批处理）、`internal/store`（proxy context 改在 claim 注册、批量事件追加、租约宽限、热读迁 reader 池）、`internal/server`（proxy 状态码、phase 投影降频、错误文案）、`internal/config`（`proxy_lease_grace`）

## 问题

用户在测试中报了三个现象，均为代码问题，与真实模型服务无关（第四条在首轮修复上线后复测时暴露，见根因三）：

1. 会话显示 idle 时发消息，消息**同时**出现在正文和排队列表；且正文那轮结束后，队列里那条会真的再执行一遍。
2. 收到「模型服务暂时不可用，本次回复中断」，但模型服务正常。
3. 有时连续遇到该报错，刷新整个页面才恢复。

## 根因

### 一、`session.runtime` 推送全部被静默丢弃（现象 1，以及现象 3 的客户端半边）

[session-runtime-projection](./session-runtime-projection.md) 建立了「turns 表为唯一真相 + 单调 revision + 单一快照契约」，方向正确，但**只完成了一半**：服务端 6 处 `publishSessionRuntime` 都在推，客户端从未注册这个事件名。

`writeSSEEventFramed` 给每帧写 `event: <type>`，而 EventSource 只把**未命名**帧投给 `onmessage`；命名帧必须显式 `addEventListener`。`SSE_TYPED_EVENT_TYPES` 缺 `session.runtime`，于是 `harness-provider.tsx` 里那个 `case "session.runtime"` 是**死代码**。

后果精确对应现象 1：`sendMessage` 的 POST 响应带回快照（队列能显示），但此后队列只靠补救性 GET 更新。agent 真正开始这一轮时服务端推 `message.created`（消息进正文）+ `session.runtime`（该项离开队列），后者被丢 → 消息同时留在队列里。而队列项是 `turns` 表的真实行，orchestrator 一定会跑掉它 —— **重复执行是真实的第二次执行**，不是显示问题。

同一个丢帧也解释了现象 3 的「刷新才恢复」：`revision` 守卫「只应用严格更新的快照」本身正确，但配合丢帧就成了单向棘轮，界面卡死直到 F5 走 `selectSession` → `refreshRuntime` 拿到新快照。

这类缺陷没有任何显式失败：handler 看着是对的，服务端日志也显示推送成功。

### 二、单写连接上的锁争用（现象 2、3 的服务端半边）

「模型服务暂时不可用」这句文案是 orchestrator **自己生成**的（`piErrorClass` 把含 `503` 的错误归为 `upstream_5xx`），不是上游返回。链路在 [turn-abort-proxy-correlation-timeout](./turn-abort-proxy-correlation-timeout.md) 已查明：proxy 转发前要向 `/internal/proxy/requests/start` 报备，该 RPC 超时 → 对 sandbox 返回 503 → 重试撞上单飞行约束 → 403 `active_context_unavailable` → pi 判定不可重试，turn 终止。

此前只调大了 proxy 侧超时，根因未除。本次复查发现压力比当时更大，且有三处此前未记录：

- **`EmitOutputBatchMaxRows/MaxAge` 从未被消费**。只在 `config.go` 定义、校验、写进 `harness.yaml`，全代码库无引用 —— 所谓「事件批处理」从不存在，每条 `emit_output` 都是独立的 `BeginTx`/`Commit`。这解释了当时实测的 27~34 条/秒各自提交。
- **reader 池几乎闲置**。`s.reader()` 仅 8 处调用，`GetSession`（每条 SSE 事件的可见性判定都打）、`listMessages`、`ListSessions` 全在写连接上，与写入抢同一把锁。
- **phase 投影引入了每 token 一次写事务**。`toolcall_delta` 与 `text_delta` 都是逐 token 帧，各自写不同 phase（thinking / streaming）；去重是 last-value-wins，交错回复下**每帧都换 key**，去重完全失效。实测 200 帧交错 = 200 个事务。

而 `StartProxyRequest` 要求 `t.lease_expires_at > now`，turn 租约只由 bridge 心跳续期（30s 间隔 / 60s TTL），心跳本身也抢这把写锁。写锁拥塞 → 心跳迟到 → 长 turn 中途租约过期 → 之后每次模型调用都拿 403，直到下次心跳成功。这正是「连续遇到」而非单次。

### 三、proxy context 在 claim 与 ack 之间不存在（队列消息**必然**失败的直接原因）

上线后复测暴露了一条更直接、且**确定性**的成因：第一条消息正常，**后续排队的每一条都报错**；再发一条也报错；隔几秒才成功。而且 agent「记得」那些报错的消息 —— 说明 prompt 确实到达了 agent，只是模型调用被拒。

`active_model_request_contexts` 此前**只在 `AckTurnStarted` 创建**。但两条路径速度完全不同：

- ack 走 bridge **文件队列**回传，由 orchestrator 下一次轮询（50ms）才应用；
- agent 的首个模型调用走 **HTTP** 直连 proxy。

冷启动的第一轮，sandbox 与 agent 初始化耗时掩盖了这个窗口，ack 先落地；而排队 turn 落在**已经热着的** sandbox 上，agent 立刻发问 —— 稳定赢下竞速，于是首个模型调用找不到 context 被拒，turn 直接死掉。prompt 却已经进了 agent 的历史，这正是「记得但没回答」的由来。

这条与前两条独立，且**不是**本次改动引入的：它一直存在，只是队列功能让「热 sandbox 上连续派发」从边缘情况变成了常态，于是从偶发变成必然。

**修复**：context 改为在 **`ClaimNextTurn`** 注册 —— claim 才是「这一轮归这个 sandbox 执行」成立的时刻，租约为其定界。相应地 `StartProxyRequest` 的 `t.status` 放宽为 `IN ('leased','running')`：leased 的 turn 已被授予该 sandbox，只是 ack 还在路上。这**不放宽「谁」可以请求**（generation、`lease_owner`、live 资源、单飞行约束全部不变），只修正「turn 内什么时候」允许。

两个必须一起处理的细节：

- **ack 不得重置 `next_request_sequence`**。ack 通常在 agent 已发出若干请求之后才到，重置会让序号从 1 重来，与该 turn 已记录的 `proxy.request.started` 事件撞号。改为「同一 turn 则保留，换 turn 才重置」。
- **心跳续期要覆盖 leased**。否则 ack 未落地期间 context 会在 turn 正在启动时过期。

`dataproxy.go` 的**状态**判据同步对齐（同一个「turn 是否已指派」问题；若不对齐，同窗口内的工具调用会以同样方式失败）。但其**租约新鲜度**判据仍刻意保持严格、不加宽限 —— 两条轴不同，代码注释已写明，避免下一个人整体对齐。

## 方案

分三层，按「是否阻断用户」排优先级；每层独立可验、独立可回滚。

### 1. 修复丢帧（一行修复 + 两侧契约测试）

`SSE_TYPED_EVENT_TYPES` 加入 `session.runtime`。

这个白名单与服务端 `hub.Publish` 的类型集合是隐式契约，人工同步已失效一次，因此补的是**结构性**测试而非钉死一个名字：

- 客户端读 `harness-provider.tsx` 源码，断言**provider 处理的每个事件类型**都已注册。下一次遗漏会被同一个断言接住，而不需要有人想起来加。
- 服务端走真实 SSE 序列化，断言 POST 消息后 `event: session.runtime` 帧确实上线，且 payload 过完 sanitizer 仍带 `revision` 和排队项（缺 `revision` 客户端会丢弃，与丢帧不可区分）。

两个测试都验证过：删掉白名单条目 / 重命名事件类型，它们分别失败。

推送到位后，`refreshRuntime` 的补救性 GET 从「唯一通路」退回兜底，因此收窄为按事件类型显式选择。保留在故障与恢复路径上（`session.error`、`generation.error`、retire、状态变更）—— `ReconcileStaleSessionStates`、cold fallback、`turn_resume` 都会改 turn 状态而**不**发快照，这些地方不重读队列就不会收敛。

### 2. 消除写锁争用

- **output 批处理**：`ProcessOnce` 内把**连续**的 `emit_output` 信封合并进一个事务。只合并连续同 session+generation 的一段，因此与其他信封类型（ack 等）的相对顺序完全不变。新增 `AppendEventsBatch`，与单条路径共用 `appendOneEventTx`，校验/幂等语义不可能漂移。

  **每条一个 SAVEPOINT**：序列不匹配要归因到单个信封并据此退役 generation，若整批回滚，已被接受的 output 会消失、且故障可能被记到无辜信封头上。实测 600 行：600 事务 9.3ms → 10 事务 3.0ms（写时间 3.1×，锁获取次数 64×）。

- **热读迁 reader 池**：`GetSession` / `ListSessions` / `listMessages`。WAL 下读不阻塞写。另跑了跨池读己之写测试（50 次提交后立刻读回），确认无陈旧读。

- **phase 投影不再逐 token 写**：`toolcall_delta` 不写 phase（外层 `toolcall_start` 已记录）。这是架构性修正而非加节流阀 —— 逐 token 帧本就不该参与一个刻意粗粒度的投影。留了正反两个测试：80 帧交错只允许 ≤1 次写，且工具边界必须仍被记录（防止有人用「静音投影」来降压）。

### 3. 让基础设施抖动不再终止 turn

- **`active_context_unavailable`：404 → 503**。这是内部记账竞态，不是「模型或 turn 不存在」。状态码决定 turn 生死：4xx 被 proxy 映射为硬错误，pi 直接放弃；503 命中其可重试白名单，且「稍后重试」本就是对该状态的诚实描述。

- **turn 租约准入加宽限窗口**（新增 `harness.bridge.proxy_lease_grace`，默认 60s）。仅放宽 turn 租约这一条判据；generation 活跃、`lease_owner` 一致、`active_generation_id`、`ri.state='live'` 全部不动，单飞行约束保留。

  两个实现细节值得记下：
  - 单飞行子查询**必须用同一个 cutoff**。否则被宽限的 turn 数不到自己，`= 1` 永不成立，宽限成为死代码。放宽该窗口只可能暴露**更多**竞争 turn，因此这个约束不会变松。
  - **宽限必须 < `sandbox_heartbeat_stall_grace`**，且这一点由 `validateHarnessConfig` 强制而非写在注释里。超过该点后编排器**故意**停止续租以便回收僵尸会话；宽限越过它就会给已判定停摆的 sandbox 继续放行模型调用，把那条保护悄悄抵消掉。

  另有 `LeaseWithinGrace` 上报：靠宽限进来的请求打 WARN。请求是合法的，但非零速率正是心跳开始输给写锁的前兆信号。

- **`dataproxy.go` 的租约新鲜度判据刻意保持严格**（不加宽限），并在代码里写明原因：拒绝模型调用会终止整个 turn（pi 视为不可重试），拒绝数据代理连接只失败一次工具调用、agent 能看到并重试。失效后果不同，不应「顺手对齐」。注意与根因三区分：那里对齐的是**状态**判据（`leased` 是否算已指派），两条轴不同。

- **文案不再臆断上游**：`upstream_5xx` / `upstream_timeout` 改为「模型请求未能完成 / 超时」。这里的 5xx 常常由本机 correlation RPC 产生，从未到达供应商 —— 说成「模型服务不可用」会把人送去检查一个一直健康的服务。

### 顺带修正

`EmitOutputBatchMaxAge` 仍无消费者，但这次是**有意**的，并写进了注释：批处理发生在单次 `ProcessOnce` 内，信封在 bridge outbox 已经落盘，该趟出现的每个信封返回前都会提交 —— 没有任何东西被暂留，也就没有陈旧度需要 age 去约束。保留字段（并继续校验）以免打断既有配置，同时明确写下「不要为了让它生效而加定时器」：那会把当前 crash-safe 的 output 重新变成内存缓冲。

## 验证

- `go build ./... && go vet ./... && go test ./...` — 20 包全绿（需 root：data-volume evidence 要求 root 属主）。
- `frontend`：`tsc --noEmit`、`npm test`（17 文件 218 测试）、`lint`、`build` 全绿。
- **每个新测试都做了反向验证**（故意改坏对应实现，确认测试失败），包括：删除白名单条目、重命名事件类型、去掉配置上界校验、重新引入逐 token phase 写、关闭批处理。
- 性能：output 批处理 600 行 3.1× 写时间 / 64× 更少锁获取；phase 写实测 128µs 每次，交错场景下从 200 次降到 1 次。
- claim 期 context：先写了一个复现测试（claim 后未 ack 即发起模型请求 → 被拒），确认现象后再修；并逐项回退验证 —— 只回退 `leased` 状态放宽、或只回退序号保留，对应测试分别失败。
- `TestAckTurnStartedRequiresLiveRuntimeResourceInstance` 从「数 context 行数」改为**直接断言模型访问被拒**。claim 期注册后该行会合法存在，行数不再是安全属性；真正的属性是资源非 live 时 `StartProxyRequest` 仍拒绝（由其独立的 `ri.state = 'live'` 保证）。直接断言比数行数更强，也不会因错误原因而通过。

## 待跟进（不在本次范围）

- claude-code-proxy 侧（`harness_observability.py`，本仓库外）自身也把非 401/403/404 异常统一映射为 503。本次改的是 orchestrator 返回的状态码；proxy 侧映射表建议一并复核，确认 503 会原样透传而不是被二次改写。
- 上线后观察 `proxy request admitted on an expired turn lease` 的 WARN 速率。持续非零说明写压力仍在，应继续把剩余读路径迁出写池（`internal/store` 尚有 40+ 处读走 `s.db`），而不是继续调大宽限。
