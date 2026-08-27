# 排查记录：沙箱 OOM 后 `runsc kill` 永不返回，阻塞整个 maintenance 循环 → 全平台无法冷启新对话

- **Status**: Implemented
- **Date**: 2026-08-17（修复 2026-08-18 落地）
- **Scope**: orchestrator maintenance 循环的阻塞隔离、runsc 外部命令超时、沙箱内存上限

## 现象

全平台不可用 34 小时（2026-08-16 00:16 → 08-17 10:52）：**任何用户发消息都永久显示 running / 前端一直转圈，且没有任何 thinking 提示**。
不限于个别会话——所有新消息都起不了沙箱。

反直觉的是**一切监控都是绿的**，这是本次最大的诊断陷阱：

- `harness-ctl status`：4 个服务全 active，公网 :443 / 后端 :9090 / 前端 :9001 全 200
- orchestrator 进程健在：`/healthz` 200（0.3ms）、39 线程、44 个 fd（无泄漏）、`NRestarts=0`
- 宿主机毫无压力：load 0.00、60G 内存用了 4G、根盘 49% / 数据盘 5%
- 日志安静：从 08-12 部署后到故障期间只有零星 WARN，**没有任何 ERROR、没有 panic、没有重启**

## 排查路径

在途 turn 全是 `queued`（一个 `running`），session 全停在 `running_active`：

| session | turn 状态 | 卡了多久 | active generation | gen 状态 | 沙箱心跳 |
|---|---|---|---|---|---|
| `sess_tbTexd0rr7vn7a65` | `running` | 2022 分钟 | `gen_47d51b4b` | `active` | 08-16 00:12（停） |
| `sess_eipekjbJRFR3a3ug` | `queued` | 1956 分钟 | `gen_56f840a5` | `idle` | 08-16 00:16（停） |
| `sess_Coxurr8MC6WfcSKC` | `queued` | 928 分钟 | `gen_c97e870b` | `idle` | **从未有过** |
| `sess_9j24rpP7A3xa1WVp` | `queued` | 57 分钟 | `gen_0c98a7ef` | `idle` | **从未有过** |
| `sess_S-aMohtf3Qo_GScu` / `sess_iUOcJkgyQjCtcBfJ` / `sess_i_UKxV4wl3t2ORDx` | `queued` | 3–12 分钟 | 各自新 gen | `idle` | **从未有过** |

「沙箱心跳从未有过」是关键分水岭：后 5 个 generation 在 DB 里都建好了行、`runtime_resource_instances`
也标 `live`，但 **`run/logs/gen-*/` 目录是空的** —— runsc 从头到尾没被执行过。
即冷启动路径根本没走到，不是沙箱起来后死了。

据此定位到三条决定性证据：

1. **`ps --ppid <orch_pid>` 里有一个跑了 33 小时的僵持子进程**：
   `runsc -root ... kill harness-gen-gen_47d51b4b-... KILL`（PID 984197，`STAT=Sl`，`wchan=do_poll`）。
2. **所有 `run/bridge/gen-*.host/` 的 host-heartbeat mtime 全部冻结在 08-16 00:16:00** —— 这个文件由
   maintenance 每 tick 写（`maintenance.go:84`），冻结时刻即 maintenance 停摆时刻。同时 6 个 generation
   的 outbox 各积压着未被消费的消息。
3. **`runsc --root ... list` 挂死**（两次实测都得靠 `timeout` 打断），但同一个 root 下对**不存在的**
   container ID 执行 `state` / `kill` 都是秒回（`FetchSpec failed: loading container: file does not exist`）
   —— 说明是那个残骸容器本身把 runsc CLI 拖住的，不是 runsc 或 root 目录整体坏了。

`dmesg` 给出起点：08-16 00:10:02，`gen_47d51b4b` 的 memcg OOM。

```
Memory cgroup out of memory: Killed process 983442 (exe) total-vm:1479196kB,
  anon-rss:0kB, file-rss:88584kB, shmem-rss:873568kB
oom_memcg=/harness-gen-gen_47d51b4b-...  constraint=CONSTRAINT_MEMCG
```

873MB 落在 **shmem**（`/dev/shm`，典型是 pandas/pyarrow 之类的共享内存），顶满了 1G 上限。

## 根因：OOM 残骸让清理命令永久阻塞，而清理跑在 maintenance 的关键路径上、没有超时

### 1. OOM 只杀掉工作进程，留下半死的 sandbox 控制面
memcg OOM 杀的是沙箱内的 `exe`（agent 进程），但 `runsc-sandbox`（boot）/ `runsc-gofer` / `runsc run`
三个宿主进程仍然存活（实测存活 33 小时，`STAT=Ssl`，`wchan=do_poll`）。控制 socket 还在
（`ss -xl` 能看到），但**不再应答** —— 于是任何针对这个 container 的 runsc CLI 操作都会挂死等应答。

### 2. 判死之后的清理动作本身卡住（`runtime.go:349`）
stall-grace 闸门这次**工作正常**，正确判死了心跳停止的 generation，recovery 照常推进：

`RecoverExpiredRuntimeResources`（`maintenance.go:154`）→ `runtime.Destroy`（`runtime.go:327`）
→ `deleteRunscContainerDetailed`（`runtime.go:347`），其第一行：

```go
_, _ = r.runner.CombinedOutput(ctx, runscBinary, "-root", r.cfg.RunscRoot, "kill", containerID, "KILL")
```

这行永不返回。08-16 00:16:30 发起，到 08-17 10:52 人工干预时仍在 `do_poll`。

### 3. 外部命令没有 per-command 超时（`runtime.go:92`）
```go
func (execCommandRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
```
超时完全取决于调用方传入的 ctx。而 maintenance 循环传的是**服务生命周期 ctx**（`RunMaintenance(ctx)`，
`maintenance.go:15`），只在 orchestrator 关停时才 cancel。于是「没有超时」= 永久阻塞。

### 4. maintenance 是单 goroutine 串行的，一处卡死 = 后面全部停摆
`runMaintenance` 是一个顺序执行的闭包，`RecoverExpiredRuntimeResources` 在 **第 65 行**，
后面还排着 11 个阶段。被它卡住后，以下全部再没执行过：

| 位置 | 阶段 | 停摆后果 |
|---|---|---|
| `maintenance.go:67` | `RenewLiveGenerationLeases` | 活 generation 租约不再续 |
| **`maintenance.go:76`** | **`startColdFallbackSessions`** | **← 直接病灶：新消息再也起不了沙箱** |
| `maintenance.go:84` | `touchHostHeartbeat` | host-heartbeat 冻结（成了本次的诊断指纹） |
| `maintenance.go:87+` | checkpoint 退休 / `ReapResources` / 资源销毁 / 事件裁剪 | 回收与裁剪全停 |

`startColdFallbackSessions`（`server.go:1482`）负责给「active generation 已死但有排队 turn」的 session
冷启新沙箱。它停了，就等于所有用户的新消息都只能排队 —— **单个沙箱 OOM 升级成了全平台故障**。

注意 `pollBridge` 走的是另一个 ticker 分支（`maintenance.go:147`），所以 bridge 轮询仍在跑、
`/healthz` 仍然 200，这正是监控全绿的原因。

## 本次止血（2026-08-17 10:52，未重启 orchestrator）

先 `.backup` 两个库到 `/var/lib/harness-publish/backups/{orchestrator,memory}.db.20260817-1002.bak`，
然后只做了一件事 —— SIGKILL 掉 `gen_47d51b4b` 的三个残骸进程（boot / gofer / run）。

结果：**卡住的 `runsc kill` 立即自行返回**，maintenance 循环当场复活，无需重启、无需任何手工 SQL。

- 40 秒内 cold-fallback 自动为 5 个卡住的 session 分配新 generation 并拉起沙箱（`runsc list` 可见）
- turn 自动从 `queued` → `leased` → `running`；6 分钟内已有 2 个 turn 跑完（含卡了 16 小时的 `sess_Coxurr8MC6WfcSKC`）
- `harness-ctl zombies`：僵尸 7 → **0**；host-heartbeat 恢复秒级刷新；无残留 `runsc kill` 进程

顺序很重要：**必须先杀沙箱残骸，再处理卡住的 `runsc kill`**。否则该函数下一行紧跟着
`runsc delete -force`（`runtime.go:350`）会对同一个残骸容器再次挂死，等于原地复现故障。

代价：`sess_tbTexd0rr7vn7a65`（OOM 的那个会话）丢失，其 agent 进程 08-16 就已被 OOM 杀死，无可挽救。
其余会话均为换新沙箱续用，DB 内历史消息未丢。

## 与既有 proposal 的区别

**不是** [session-stuck-after-restart-no-runtime.md](./session-stuck-after-restart-no-runtime.md)
描述的那类「DB 状态活着的假象」，也不是它后续修复（`sandbox_last_seen_at` + stall-grace 闸门，
commit `8d3975e`）覆盖的问题：

- 那次的缺口在**判死之前**——orchestrator 无条件自续租，导致永远判不了死。
- 这次 stall-grace 判死**完全正常**，卡的是**判死之后的宿主侧清理动作**。属于新的一层缺口。

也因此 `harness-ctl zombies`（commit `ffa3577`）这次给出的是**表象**：它把 7 个 session 全报成
「僵尸（沙箱失联）」，方向没错，但那 7 个里有 5 个是「压根没启动」而非「启动后失联」，
真正的单点是 orchestrator 那个僵持的 `runsc kill` 子进程。

## 已落地的修复（2026-08-18）

五项全部实现，`go build` / `go vet` / `go test -race ./...` 全绿。**需重启 orchestrator 生效**。

### 1. 每条外部命令独立超时（`CommandRunner` 层，覆盖全部 17 处调用点）
在 `execCommandRunner`（`runtime.go:92`）统一加超时，而不是只补 `deleteRunscContainerDetailed`
那一行——盘查确认全部 17 个调用点都是短命的探测/teardown 命令（checkpoint/restore 不走 runner），
所以一个统一的默认上限是安全的，这同时消掉了建议 3 的风险面。

- 默认 30s，可配 `harness.runtime.command_timeout`；`Config.CommandTimeout` 为零时取默认值
  ——**零值必须落到默认超时而不是「无超时」**，这正是故障的根源，有测试钉住。
- 超时错误显式包住 `context.DeadlineExceeded` 并带上命令行文本，日志里能直接看到是谁挂了，
  而不是一个语焉不详的 `signal: killed`。
- **`cmd.WaitDelay = 5s`**：这一点是实现时才发现的关键细节。ctx cancel 只 SIGKILL 直接子进程，
  但 `CombinedOutput` 还要等所有持有输出管道的写端关闭——而挂死的 runsc 会留下一个 gofer
  子进程握着管道。没有 `WaitDelay`，加了超时也**依然会永久阻塞**。有专门的测试复现这个形态
  （`sh -c 'sleep 30 & exit 0'`），实测 5.01s 返回而非 30s。

### 2. maintenance 阶段隔离（结构性修复）
12 个阶段拆成带命名的 `stage()`，每个阶段独立 deadline（`harness.maintenance.stage_timeout`，
默认 2m）。超时的阶段留在后台继续跑，后续 tick **跳过**它直到它返回，因此一个阶段永不与自身并发
（避免卡死阶段每 tick 堆一个 goroutine）。

关键点：**tick 派发不能阻塞主循环**。第一版把 `stage()` 直接在 select 循环里调用，结果
`stage` 等待 deadline 时照样把循环卡住、cold-fallback 的 ticker 永不触发——写的回归测试
当场抓到了这个自制缺陷。最终改成三个独立 worker（清理阶段 / cold-fallback / bridge 轮询），
主循环只做**非阻塞**投递，worker 忙则丢弃该 tick（重放一个过期 tick 没有意义）。
cold-fallback 因此既有独立 ticker（默认 5s）又有独立 worker，彻底与清理阶段解耦。

### 3. 沙箱资源上限可配置
`runtime_spec.go` 里硬编码的 `1073741824` 改为读配置，默认 **4 GiB**
（`harness.runtime.sandbox_memory_limit_bytes`），pids 上限同样可配。这是 memcg *上限*不是预留，
`max_sessions × limit` 允许超过宿主内存——沙箱绝大多数时间空闲，内核只在实际使用时才强制。

### 4. `harness-ctl zombies` 增加单点故障检测
新增第二段输出，两条独立判据：① orchestrator 名下长寿命 `runsc` **清理**子进程；
② host-heartbeat 冻结。只有 ② 才判定「循环已停摆」，① 单独出现只报「有清理命令挂住、循环仍在转」。

实现时踩到三个误报，都已修正并实测：**(a)** 判据必须排除 `runsc ... run`——那是健康沙箱的常驻
监督进程，会跑几小时，算进来则每个正常沙箱都误报；**(b)** 最初想用「`run/logs/gen-*/` 为空」
判断 runsc 从未执行，实测**健康沙箱的 log 目录也是空的**，该判据完全无效，已改用 host-heartbeat
新鲜度（这个信号更直接：它就是 maintenance 每 tick 写的）；**(c)** 采样必须限定
`generation.status IN ('active','idle')`，否则几个月前的泄漏 `live` 行会让它常态误报冻结。
另修了 `set -e` + `pipefail` 下 `ps` 无匹配（退出码 1）会中断整个脚本、以及「无 running_active
session 就早退」导致循环停摆时反而不检查的问题——循环停摆时新对话根本进不了 `running_active`。

### 回归测试
- `runtime`：超时生效且释放及时、grandchild 持管道仍能超时、caller deadline 优先、
  零值落到默认、`kill` 失败不阻止 `delete -force`、资源上限可配。
- `server`：**wedged recovery 阶段下 cold-fallback 仍能拉起新沙箱且排队 turn 可被 claim**
  （核心回归，直接复现 34h 故障的因果链）、卡死阶段不与自身并发。
- `config`：6 条新校验（含 `stage_timeout >= command_timeout` 的互斥约束）。

### 仍未做
OOM 的显式可观测性——目前沙箱被 OOM-kill 只能事后翻 `dmesg`，没有事件/指标。
建议后续把 memcg OOM 计数暴露成 generation 上的 `error_class` 或事件，让「会话是被内存打死的」
这件事在产品里能被看见。

> **2026-08-20 更新**：本节预判的缺口在 08-19 实际复发（3.9 GB shmem 撞 4 GiB 上限），
> 且暴露出第二个缺口：清理失败会连带跳过 DB 修复，导致单个会话锁死 22 小时。
> 本次的超时与 stage 隔离按设计生效（未再全平台停摆），但不足以恢复该会话。
> 已实现 OOM 归因（cgroup v2 `memory.events`）、用户可见提示、SIGKILL 兜底与内存压力预警，
> 详见 [sandbox-oom-blocks-recovery-and-hides-cause.md](./sandbox-oom-blocks-recovery-and-hides-cause.md)。

## 关键证据位置（行号为故障当时，修复后已变动）

- 无超时的 runner：`orchestrator/internal/runtime/runtime.go:92`
- 挂死的清理命令：`orchestrator/internal/runtime/runtime.go:349`（`kill`）、`:350`（`delete -force`）
- 调用链：`orchestrator/internal/runtime/runtime.go:327`（`Destroy`）←
  `orchestrator/internal/server/maintenance.go:154`（`RecoverExpiredRuntimeResources`）←
  `orchestrator/internal/server/maintenance.go:65`
- 被饿死的关键路径：`orchestrator/internal/server/maintenance.go:76` →
  `orchestrator/internal/server/server.go:1482`（`startColdFallbackSessions`）
- 诊断指纹（host-heartbeat）：`orchestrator/internal/server/maintenance.go:84`
- 沙箱内存上限：`orchestrator/internal/runtime/runtime_spec.go:389`
- 同类风险面：`orchestrator/internal/runtime/runtime_network.go`（14 处 `CombinedOutput`）
- 相关：判死之前的缺口见 [session-stuck-after-restart-no-runtime.md](./session-stuck-after-restart-no-runtime.md)；
  bridge 目录泄漏拖累回收见 [bridge-dir-leak-reaper-blocked.md](./bridge-dir-leak-reaper-blocked.md)。
