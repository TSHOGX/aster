# 沙箱静默死亡：inbox 清理竞态 + 死亡无痕 + 「Idle」标签失真

- **Status**: Implemented
- **Date**: 2026-09-12
- **Scope**: `sandbox-image`（bridge client 的读取容错与 claim 重试）、`internal/bridge`（inbox 按时效清理、序号单调）、`internal/runtime`（容器输出落盘 + 退出钩子、cleanup 顺序、`.host` 目录回收）、`internal/server`（沙箱退出即时收敛、恢复分类）、`internal/store`（`SandboxState` 派生、去掉行级策略谓词）、`frontend`（休眠/唤醒状态、错误文案、队列取消反馈）

## 问题

用户报告三件事：老 session 或 idle 一段时间后的 session 会「各种异常」；界面上所有 session
都显示绿色 idle，几个月前的也是；经常收到 `orchestrator_restart_reconnect_grace_expired` 的
报错 toast。此外：静置几分钟后第一条消息要等几秒，之后就快；队列里的消息偶尔要叉两下。

最初的线索指向 `auto_checkpoint_enabled` 在老 session 上冻结为 0、因此从不被空闲释放。
**查库推翻**：18 个非 destroyed session 全为 1（`d4dfdec` 与打开开关同时加了启动对齐）。

## 取证

开发环境 DB（orchestrator 自 02:59Z 起从未重启，owner 不变）：

```
sess                   gen       started   ended     lived_s  最后心跳   error_class
sess_gC625w59Xznf8qhO  fd0f87a8  11:44:12  11:52:31   498    11:49:13  orchestrator_restart_reconnect_grace_expired
sess_nMYULSQt2QUuIsvf  5c6a3062  11:45:06  11:50:31   325    11:47:36  orchestrator_restart_reconnect_grace_expired
sess_cfvEs978mmom9rkS  fa6e22e4  11:46:41  11:54:01   439    11:51:11  orchestrator_restart_reconnect_grace_expired
sess_4AXTdHdKU5TcSYxv  b62d4772  11:46:59  11:51:31   271    11:48:59  orchestrator_restart_reconnect_grace_expired
```

18 个 session 里 17 个的 active generation 是 `failed`，沙箱寿命 3–8 分钟，心跳在某一刻戛然而止；
orchestrator 日志里**没有任何**沙箱死亡的记录。每个死掉的 generation 的 `.host/inbox` 里都躺着一条
从未被取走的 `no_work` 响应，mtime 正好等于心跳停止时刻。用 20ms 分辨率从宿主机盯 bridge 目录，
抓到一次死亡：orchestrator 在 20ms 内答复了 claim，沙箱却再也没有下一次 claim。

沙箱的 stderr 被 `OutputHub` 在零订阅者时丢弃（`Start` 返回后 `defer cancel()` 退订），所以先补了
输出落盘 + 退出钩子（见方案 A），重跑后拿到遗言：

```
File "/usr/local/bin/harness-bridge-client", line 156, in read_all
    with open(entry, "r", encoding="utf-8") as f:
FileNotFoundError: [Errno 2] No such file or directory: '/harness-control/bridge/inbox/00000000000000000001.json'
```

## 根因

### 一、inbox 清理竞态（沙箱死亡的直接原因）

`b579c45`（2026-05-28）把 inbox 改为 host 独占、以 **只读** bind 进沙箱。副作用有两条，当时都没被注意：

- 沙箱端 `wait_response` 里的 `self.inbox.unlink(path)` 从此**永远失败**（异常被静默吞掉），
  响应文件读完后仍留在 inbox 里；
- host 端为了清理，在**每次**写响应前 `pruneSequenceFiles()` 删掉 inbox 里**所有**序号文件。

于是沙箱每 50ms 轮询时会反复 `iterdir` → `open` 那些陈旧响应；host 写下一条响应时把它们删掉。
两者一旦交错（`iterdir` 已列出、`open` 时已被删），`FileNotFoundError` 从 `read_all` 一路穿出
`run_claim_loop` → `main()`。bridge client 是容器的 init 进程，它退出就是沙箱退出。

写压力越大（gofer RPC 越慢）窗口越宽，所以在输出洪流期间成片死亡；但静默期也会死——这是
概率事件，不是负载事件。

### 二、死亡无痕，且被错误归因

`launchContainer` 的输出订阅只在 `Start` 内存活。沙箱退出后，`runtime` 只做 `cleanupRunscContainer`，
不通知任何人。generation 继续以 `idle` 躺在库里，直到租约过期（60s）+ reconnect grace（30s）后被
`ListExpiredRuntimeRecoveryCandidates` 的第三条查询收走，`error_class` 写成
`orchestrator_restart_reconnect_grace_expired`——尽管 orchestrator 从未重启。这就是用户看到的 toast。

### 三、`running_idle` 一个词背两件事

会话状态里 `running_idle` 同时表示「沙箱活着、下一句立刻回」和「沙箱没了（重启回收 / 中断 /
失败）、下一句要冷启动几十秒」，`checkpointed`（空闲释放）也是绿点。前端只看 session 状态，
所以「全绿 Idle」，而实际上几乎全是死的。「静置几分钟后首条消息要等」就是在付冷启动的钱。

### 四、队列取消「按两下」

`QueuedTurns` 的 X 没有 pending/disabled 态，`CancelQueuedTurn` 是写事务，和 output 洪流抢同一把
写锁；慢请求和没反应在界面上一模一样。

### 附带发现

- `cleanupTargetBridge` 只删 `BridgeDirPath`，`<root>.host` 从不删（实测 58:1）。这解释了
  [bridge-dir-leak-reaper-blocked.md](./bridge-dir-leak-reaper-blocked.md) 观察到的堆积；那份草稿
  推测的「心跳写入方重建目录」不是主因。
- `DestroyGenerationResources` 先删文件、任一路径失败即提前返回，`runsc kill/delete` 根本不执行——
  一个持续的 fs 错误会让沙箱进程永远活着。
- `ListAutoCheckpointCandidates` / `BeginGenerationCheckpoint` 这条真 runsc checkpoint 路径已无生产
  调用者（仅测试引用）。本次未删。

## 方案

### A. 让死亡可见（`internal/runtime` + `internal/server`）

- `launchContainer` 在 `cmd.Start` 前挂一个常驻订阅者：每行输出写入
  `<log_dir_path>/sandbox.log`，内存保留最后 50 行。
- 监视 goroutine在 `cmd.Wait` 返回后调用 `ContainerExitHandler`，携带退出状态、是否为 host 主动停止
  （`stopContainer` / `Destroy` / `DestroyGenerationResources` 事先 `markStopping`）、以及尾部输出。
- `server.HandleSandboxExit`：非主动停止且 generation 仍为 `active/idle` → 立即
  `FailGenerationWithEvent(error_class=sandbox_exited, reason=退出状态 + 最后一行 stderr)`；
  running 的 turn 失败并写入沙箱死亡通知；leased 的 turn 留给 reconcile 重排。
  收敛时间从 90s+ 降到毫秒级，且 `error_class` 诚实。
- 恢复分类：租约在**同一 owner** 下过期的 generation，`Cause = sandbox_heartbeat_lost`；
  `orchestrator_restart_*` 只留给真正的 owner 变更。诊断的 Cause 现在写在 session 修复**之前**，
  所以 `generation.error` 事件里带的是诊断而不是路由类名。

### B. 止血（`sandbox-image` + `internal/bridge`）

- 沙箱端 `read_all` 容忍文件在 `iterdir` 与 `open` 之间消失或半写（跳过）；`wait_response` 用
  `inbox_seen_seq` 高水位只打开新文件、从不打开已消费/陈旧的；去掉永远失败的 `unlink`。
- claim 超时/异常改为**重试**（退避 1s×n，上限 10s），连续 20 次才退出并写明原因；`--timeout` 从
  10s 提到 30s（可用 `HARNESS_BRIDGE_TIMEOUT` 覆盖）。
- host 端 inbox 只清理 **5s 以前**的响应，且**永不删最新的一个**，序号因此在一个 generation 内单调，
  沙箱端的高水位才成立。

### C. 收敛与清理

- `DestroyGenerationResources` 先 `runsc kill/delete`，再删文件，fs 错误不再跳过杀进程；
  `<root>.host` 纳入清理目标与缺席证据。
- `ListIdleReleaseCandidates` 去掉 `auto_checkpoint_enabled` 谓词；策略只在 `MonitorIdleSessions`
  一处判断；`AlignAutoCheckpointEnabled` 删除。列保留但不再读（若将来要 per-session 覆盖，应做
  可空的 override 列，而不是冻结默认值的副本）。

### D. 界面说真话（`frontend` + `session_dto` + runtime snapshot）

- 新增派生的 `SandboxState`（`live | starting | released`），由 active generation 状态 + 资源状态算出，
  不新增持久状态。随 `GET /api/sessions`、`GET /api/sessions/{id}` 和 runtime snapshot 下发。
- 标签：沙箱活着 → 实心绿点「Idle」；没有 → 空心绿点「Sleeping」；启动中 → 脉冲「Waking」。
- 冷启动期间 composer 底部提示「正在唤醒沙箱…」（发送请求同步阻塞在冷启动上，这段等待现在说明白）。
- `generation.error` toast 按 `error_class` 用人话：`sandbox_exited` / `sandbox_heartbeat_lost` /
  `orchestrator_restart_*` → 「沙箱已停止，下一条消息会自动启动新沙箱」；`sandbox_oom` 另有文案。
- 队列 X 有 pending 态（转圈 + disabled），不再能连点。

## 验证

- Go：`internal/runtime`（退出钩子三种路径 + 尾部裁剪）、`internal/bridge`（时效清理 + 序号单调）、
  `internal/store`（同 owner → `sandbox_heartbeat_lost`，异 owner 保持重启类；`SandboxState` 派生）、
  `internal/server` 全包通过（需 root）。
- 沙箱端 `test_harness_bridge_client.py` 45 个用例：新增消失/半写文件、高水位不重读、claim 超时重试、
  持续失败退出。
- 前端 `vitest` 222 个用例，`tsc`、`eslint` 干净。
- 开发环境实测：修复前沙箱寿命 18s–8min；修复后同一 session 的沙箱持续存活、序号单调（1516+）、
  `claim failed` 计数为 0。

## 未做 / 后续

- **写压力**：`emit_output` 事件平均 16KB/行（`message_end` / `turn_end` 重复整条消息），单日 848MB。
  它不再致命（claim 超时可重试），但仍是 30s 维护 tick 卡住 bridge 轮询的主因。事件表同时承担 SSE
  重放与去重比对，裁剪 payload 是协议决定，另立提案。
- 真 runsc checkpoint 路径的删除。
- 更彻底的状态机整理：把「无沙箱但可唤醒」统一成一个 session 状态而不是靠派生字段。本次选派生字段，
  因为它不动任何 CAS 谓词。
