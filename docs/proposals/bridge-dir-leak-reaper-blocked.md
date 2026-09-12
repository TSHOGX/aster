# 排查记录：bridge 目录泄漏，generation teardown 校验卡在「path still exists」

- **Status**: Superseded — 见 [sandbox-death-inbox-prune-race.md](./sandbox-death-inbox-prune-race.md)：`.host` 目录从未是 cleanup 目标，泄漏与心跳重建无关
- **Date**: 2026-07-24
- **Scope**: orchestrator runtime cleanup / reaper、bridge 目录生命周期

## 现象

orchestrator 日志每约 10 分钟刷一条 WARN（涉及多个 session）：

```
level=WARN msg="idle release sandbox teardown deferred to reaper"
  session_id=... generation_id=...
  error="destroy generation resources: verify bridge cleanup path
         \"/var/lib/harness-publish/run/bridge/gen-<gen>\" absence: path still exists"
```

`/var/lib/harness-publish/run/bridge/` 下 `gen-*.host` 目录持续堆积。2026-07-24 实测
**225 个**，最早可追到 6 月下旬，几乎**每天都有新增残留**（例：6/30 有 12、7/22 有 19、
当天已 26），是存在近一个月的慢性泄漏，而非突发。

## 排查路径

1. 报错串来自 `runtime_cleanup.go:277` 的 `filesystemAbsenceEvidence`：它在删除后用
   `os.Lstat` **校验路径已消失**，仍存在就返回 "path still exists"。
2. 实际删除在 `runtime_cleanup.go:335` 的 `os.RemoveAll(target.path)`。删除由 root 执行、
   目录亦 root 拥有，**权限不是原因**。
3. 抽查一个卡住的目录 `gen-<gen>.host/`，内含 `host-heartbeat/`、`host-tmp/`、`inbox/`，
   其中 `host-heartbeat/host` 的 mtime **比目录创建晚约 2 小时**——即目录被清理后，
   仍有一方在往里写心跳，把子目录/文件重新创建出来。
4. 该 generation 不在当前存活 runsc 容器列表中（活容器仅少数几个），说明写入方不是正常在跑的
   sandbox，而是某个未随 generation 结束而停止的 host 侧 bridge writer / 残留 goroutine。

## 根因（推断，待代码确认）

`RemoveAll` 与心跳写入之间缺乏互斥/顺序保证：teardown 先删目录，但**没有先停掉 host 侧
bridge 心跳写入方**。删除后写入方立即重建 `host-heartbeat/` 等子路径，紧接着的 `Lstat`
校验发现路径又出现 → 报 "path still exists" → defer 给 reaper → reaper 下轮再删又被重建，
形成永不收敛的循环，目录因此永久泄漏。

> 待确认：心跳写入方的确切来源（host bridge writer 的生命周期是否绑定 generation teardown）、
> 以及 teardown 是否应在 `RemoveAll` 前先 join/关闭该 writer。

## 影响判断

- **磁盘**：每个目录仅 4–12 KB，225 个总量极小，非空间风险。
- **可观测性**：WARN 噪声掩盖真实告警。
- **回收滞后**：过期租约 / 闲置 sandbox 资源的回收被拖住，与
  [session-stuck-after-restart-no-runtime.md](./session-stuck-after-restart-no-runtime.md) 的
  卡死会话相互加剧（回收链路整体不健康）。
- **未直接伤害在线用户**：实测期间真活跃会话正常运行。

## 尚待跟进的修复建议（按优先级）

1. **teardown 先停写入方再删（治本）**：generation 结束时，先确定性地关闭 / join host 侧 bridge
   心跳与 inbox writer，再 `RemoveAll`，最后校验。消除删除-重建竞态。
2. **校验容忍良性重建 / 加重试上限**：若允许「删除后可被重建」，`filesystemAbsenceEvidence`
   应带有限重试 + 退避，并在耗尽后升级为可诉诸人工的错误，而非静默每 10 分钟 defer。
3. **一次性回收存量泄漏**：给 225 个残留目录做安全清理——**必须先排除当前存活容器对应的
   generation**（对照 `/var/lib/harness-publish/runsc` 的活 gen 列表），只删已确认 terminal 的。
   不可无差别 `rm`。
4. **监控**：对 `run/bridge/` 目录数设阈值告警，泄漏复发能及时发现。

## 关键证据位置（供复核）

- 删除后校验：`orchestrator/internal/runtime/runtime_cleanup.go:277`（`filesystemAbsenceEvidence`）
- 实际删除：`orchestrator/internal/runtime/runtime_cleanup.go:335`（`os.RemoveAll`）
- defer 日志点：`orchestrator/internal/server/server.go:1938`
- 泄漏目录：`/var/lib/harness-publish/run/bridge/gen-*.host`
- 活容器对照：`/var/lib/harness-publish/runsc`（`gen_*`）
