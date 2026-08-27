# 排查记录：shmem OOM 再次拖死单会话 —— 清理失败连带阻塞 DB 修复，且用户完全无感

- **Status**: Implemented
- **Date**: 2026-08-20
- **Scope**: recovery 的状态修复与资源回收解耦、runsc 清理的 SIGKILL 兜底、OOM 归因与用户提示、内存压力预警
- **前序**: [runsc-cleanup-hang-blocks-maintenance.md](./runsc-cleanup-hang-blocks-maintenance.md)（2026-08-16 的 34 小时全平台故障）

## 现象

`sess_tbTexd0rr7vn7a65` 的一个 turn 卡在 `running` **22 小时**（08-19 07:03Z → 08-20 05:57Z）。
与前序故障的关键差异:**这次不是全平台故障**。

- maintenance 循环**始终在转**（前序的 stage 隔离与命令超时按设计生效了）
- 其它会话冷启动正常 —— 止血过程中就有新沙箱起来并跑完 turn
- live 资源实例 3 个,配额 30,没有配额挤压
- 日志里刷了 1343 条**完全相同**的 WARN,severity 从未升级

用户侧看到的是:最后一条可见消息是 07:06 的「已把 glob 重试拉到…现在启动续跑」,
然后 22 小时静默。**UI 里没有任何地方说明发生了什么。**

## 根因

三个独立缺陷叠加。前序修复加的那一道防线（30s 命令超时）挡住了全平台停摆,但没挡住单会话锁死。

### 1. 状态机进展绑在一个可能永不成功的外部命令上（决定性）

`maintenance.go` 的 `RecoverExpiredRuntimeResources`:

```go
if err := s.runtime.Destroy(ctx, runtimeID); err != nil {
    result.RuntimeCleanupSkipped++
    s.log.Warn("runtime cleanup before recovery failed", ...)
    continue          // ← DB 修复被整个跳过
}
cleaned = append(cleaned, candidate)
```

只有 `cleaned` 里的 candidate 才进 `RepairExpiredRuntimeRecovery`。半死沙箱让 `Destroy` 永远失败,
于是:

generation 永远留在 `active` → 唯一索引 `runtime_generations_one_nonterminal_per_session`
挡住替补 generation → cold-fallback 无从接管 → turn 永远 `running`。

**DB 状态修复和宿主资源回收是两件事,不该耦合。** 沙箱在心跳停摆 90s 后就被 stall-grace 判死了,
它不会再写 bridge、不会再抢 turn 租约;先修 DB 让用户恢复服务是安全的。

### 2. OOM 残骸没人杀

`deleteRunscContainerDetailed` 只发 `runsc kill` 和 `runsc delete -force` —— 两者都要走那个
**已不应答的控制 socket**。前序修复的注释说 `kill` 挂住 "must not stop `delete -force`",
但两者挂在同一个原因上,30s+30s 全部白费。没有任何路径回退到 SIGKILL 宿主进程,
而这恰恰是唯一有效手段（本次人工止血正是如此,`runsc list` 立刻恢复）。

### 3. 用户完全被蒙在鼓里,而且系统主动删掉了原因

- recovery **从不写 transcript**（`grep "INSERT INTO messages" internal/store/recovery.go` 为空）
- `repairRecoveredSessionTx` 把 session 上的 `error_class` / `failure_reason` **显式置 NULL**
- 前端只有一句泛化的「本次回复未完成就中断了」,不区分 OOM / 上游故障 / 编排器重启
- 「继续」按钮的注释写着 "resumes the work in the existing sandbox with all its state intact" ——
  沙箱因 OOM 死亡时这个前提是**假的**,`/dev/shm` 的数据全没了,用户点下去 agent 会在空白沙箱里
  以为自己有上下文

`unknown_after_ack_started` 这个归类本身也不准确:cgroup v2 的 `memory.events:oom_kill`
就在那里,**系统确实知道**是 OOM,只是把事实丢掉了。

### 触发条件（与前序同源,上限已无意义）

```
Memory cgroup out of memory: Killed process 1716385 (exe)
  shmem-rss:3903248kB  oom_memcg=/harness-gen-gen_f152a2e0-...
```

3.9 GB 落在 **shmem**,顶满 4 GiB 上限。前序把上限从 1 GiB 提到 4 GiB,
而这次是 3.9 GB 撞 4 GiB —— **shmem 随负载走,提高上限只是推迟阈值,不是解法。**

## 止血（2026-08-20 13:5x）

按 `harness-ctl zombies` 已内置的 runbook:SIGKILL 半死沙箱的 boot 进程（gofer 与 run 随之退出）。
动手前用 `/proc/<pid>/cmdline` 确认归属。

结果:`runsc list` 立刻返回、挂住的 `delete -force` 自行返回、recovery 把 generation 标 `failed`、
turn 1262 释放、僵尸清零。**无需改库、无需重启。**

## 已落地的修复（2026-08-20,四层）

`go build` / `go vet` / 20 个 Go 包 / 前端 198 个测试全绿。

### 第一层:解耦 DB 修复与资源回收（`maintenance.go`）

`Destroy` 失败不再 `continue`。改为:失败后检查宿主进程是否真的没了
（`SandboxRemnantsGone`）,没了就照样把 candidate 送进 `RepairExpiredRuntimeRecovery`,
generation 立即变 `failed`,cold-fallback 秒级接管。

**安全取舍（重要）**:宿主进程若仍存活,则**保持 generation 锁定并升级为 ERROR**,
拒绝放行替补。因为替补 generation 会挂载同一个 overlay upperdir,而系统没有任何
单挂载保护（见 memory: dual-mount-no-protection）—— 两个沙箱共享 upperdir 会损坏它。
**数据完整性优先于该会话的可用性。**

### 第二层:SIGKILL 兜底（`runtime_remnant.go`,新文件）

`delete -force` 失败后,按 container ID 找出 boot/gofer/run 三个宿主进程并 SIGKILL,
然后重试一次 `delete -force`。

实现要点:**gofer 的命令行里没有 container ID**,只有 `--bundle=.../gen-<generation-id>`。
只按 container ID 匹配会漏掉 gofer。因此分进程角色分别匹配,并要求进程名以 `runsc` 开头 ——
否则一个恰好提到 generation ID 的 shell 或 grep 会成为 SIGKILL 目标。
kill 顺序为 boot → gofer → run（杀 boot 后另两个会自行退出）。

### 第三层:归因 + 告诉用户（`sandbox_death_notice.go`,新文件）

- 从 cgroup v2 `memory.events:oom_kill` 判定 OOM,归因为 `sandbox_oom` 而非 `unknown_*`。
  比解析 dmesg 可靠（不会被 ring buffer 冲掉),且必须在资源回收**之前**读 —— 回收会删掉 cgroup 目录。
- `ok=false`（cgroup 已消失 / cgroup v1）时**保持「未知」**,不臆造原因。
- 往 transcript 写一条 assistant 消息,明确**已保留**（工作区文件）与**已丢失**（/dev/shm、
  执行上下文）,并给出可执行建议（分批、及时落盘)。落库,刷新页面仍在。
- 用新的 `stop_reason = "sandbox_died"`（区别于模型调用失败的 `"error"`）。
- 前端 `interruptionKind()` 据此分流:沙箱死亡时文案改为「沙箱被终止…执行上下文已丢失」,
  按钮改为「重新开始」,续跑 prompt 改为**先检查工作区确认进度再继续** ——
  不再让用户点一个前提已不成立的「继续」。
- generation 与 turn 行都写入诊断后的 `error_class`,供事后排查（transcript 会滚走,DB 不会）。

### 第四层:防复发（`sandbox_memory_pressure.go`,新文件）

- **内存压力预警**:用量超上限 85% 时,往 transcript 写一条带实际数值的警告,让 agent
  自己收敛（落盘、分批)。**警告写给 agent 看,而不是只写日志** —— 日志只能提醒一个没在看屏幕的
  运维,transcript 能改变那个即将被杀的进程的行为。每 generation 2 分钟冷却,回落到阈值下即重置。
- **清理失败升级**:同一 generation 连续失败 10 次（≈5 分钟）升级为 ERROR,之后每 10 次再报一次。
  不是每次都报 —— 那正是掩盖了上次故障的噪音本身。成功一次即清零。

### 回归测试

- `runtime`:三种进程角色的识别（含只能靠 bundle 路径识别的 gofer）、拒绝非 runsc 进程与
  其它 generation、kill 顺序、`memory.events` 解析、cgroup 缺失时判为「未知」。
- `server`:**清理失败但宿主干净 → 释放 generation**（直接复现本次故障的因果链）、
  **宿主残骸存活 → 保持锁定**（安全取舍）、OOM 提示落库且含保留/丢失说明、
  归因不足时不臆造 OOM、被锁定的 generation 不发提示、升级阈值与冷却。
- `frontend`:`interruptionKind` 区分 model / sandbox、取最近一次中断、已跨过的中断不再提示。

另外实测了真实宿主:`memory.max` 为 `"max"`（无上限）的 cgroup 必须读成「不可用」,
否则会被当成 0 字节上限而让每个沙箱都判定为「压力中」。

## 仍未做

- **单挂载租约**:第一层的安全取舍靠「SIGKILL 成功才放行」兜住,但根本保护（upperdir
  单挂载租约）仍不存在。若宿主进程进入不可中断的 D 状态,该会话会一直锁定直到人工介入 ——
  这是有意的选择,但代价是可用性。
- **上限策略**:4 GiB 仍是配置默认。真正的方向是让 OOM 变成可恢复的普通错误
  （已由第三、四层部分达成),而不是继续调大数字。

## 关键位置

- 解耦后的 recovery:`orchestrator/internal/server/maintenance.go`（`RecoverExpiredRuntimeResources`）
- SIGKILL 兜底:`orchestrator/internal/runtime/runtime_remnant.go`,
  接入点 `runtime.go`（`deleteRunscContainerDetailed`）
- 归因与提示:`orchestrator/internal/server/sandbox_death_notice.go`
- 压力预警与升级:`orchestrator/internal/server/sandbox_memory_pressure.go`
- 诊断落库:`orchestrator/internal/store/recovery.go`（`RepairExpiredRuntimeRecovery` 尾部）
- 前端分流:`frontend/lib/message-grouping.ts`（`interruptionKind`）、
  `frontend/components/workbench/conversation.tsx`（`InterruptedNotice`）
