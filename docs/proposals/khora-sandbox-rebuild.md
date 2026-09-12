# 基于 Khora 重做 BatteryGPT 沙箱的架构方案

本文基于当前 `batterygpt` 与 `../khora` 的代码、测试和运行文档，规划沙箱底座重做。目标是把通用隔离能力交给 Khora，把 Session、Turn、Agent、Bridge、认证、模型代理和 Memory ingest 保留在 BatteryGPT 产品层。

## 结论

建议采用“Khora 独立进程 + BatteryGPT 适配层”的路线，分阶段替换现有 `internal/runtime`，暂不把 Khora 源码复制进本仓库，也不把 BatteryGPT 的产品语义塞进 Khora。

第一阶段用 Khora 的控制面和数据面建立一个 `SandboxEngine` 适配器，保留现有 bridge 协议和产品 API；第二阶段把进程、文件和 artifact watcher 迁移到 Khora 的 Process/FS/Watch；最后删除当前 runsc/OCI/generation 资源编排。这样可以立即复用 Khora 的生命周期、资源、网络、卷、事件和恢复能力，同时把 Khora 的 0.0.x 变更风险隔离在适配器边界内。

不建议一开始直接嵌入 Khora 的 `internal/*` 包。Go 的 `internal` 规则使外部模块不能直接使用这些实现；复制源码会形成双份生命周期和 provider 代码，后续升级与安全修复都会变成手工同步。独立 Khora 进程通过其 `pkg/khora` 客户端和 Connect 数据面接入，边界最清楚。

## 当前实现评估

当前系统已经具备可用的隔离能力，但沙箱责任集中在两个过大的组件中：

- `orchestrator/internal/runtime` 同时负责 OCI spec、rootfs/mount、runsc 命令、网络 namespace/veth/nft、checkpoint、清理、运行时证据、bridge 进程和宿主路径。
- `orchestrator/internal/server` 同时负责 Session/Turn 状态机、generation 启动恢复、bridge 处理、模型代理授权、artifact、事件流、维护和故障恢复。

持久化模型把逻辑 Session、物理 generation 和宿主资源强绑定。`runtime_resource_instances` 中保存 runsc container、netns、veth、nft、bundle、spec、control directory 等宿主实现细节；进程内 `containers` map 又是非权威状态，重启后需要复杂扫描和补偿。当前 `workspace`、`agent-home` 和 data volume 主要是宿主目录加精确 bind mount，缺少独立的 Volume 生命周期、快照/clone、容量和挂载关系模型。

网络声明和具体 nft 安装混在 runtime；rootfs 是固定路径而不是 Template/Overlay；bridge 文件队列、agent 输出解析、heartbeat 和 turn 生命周期交织。现有实现中的 timeout、runsc pin、cleanup evidence、OOM 归因等修复需要保留，但不应继续扩大 `runtime` 的职责。

Memory 语义层可以冻结：当前 memorystore 已是独立数据库，control store 通过 `controlsource.SessionSource` 单向 ingest。沙箱重做只需保持这个接口和事件/turn 数据形状，不迁移 nightly sweep、语义演化和知识库发布流程。

## Khora 能力与目标边界

Khora 的稳定对象是 `Sandbox`、`Placement`、`Process`、`Template`、`Overlay`、`Volume`、`Mount`、`Binding`、`Operation` 和 `Event`。它明确不包含 Session、Turn、Agent、Credential、Secret、User 或 Memory。该边界应原样保留。

Khora 可直接承担以下职责：

| 能力 | 由 Khora 提供的机制 | BatteryGPT 适配层负责的内容 |
| --- | --- | --- |
| 隔离生命周期 | Sandbox 创建、pause/resume/destroy、Placement epoch、启动恢复和 reconcile | Session 与 Sandbox 的关联、用户可见状态和产品事件 |
| 执行 | runsc、cgroup v2、网络 namespace、进程启动/停止/信号/PTY/重连 | driver 命令、turn claim/ack、输出语义和重试 |
| 文件系统 | Template、Overlay、FS Read/Write/List/Move/Remove/Watch | 只允许 `/workspace` 等产品路径、artifact 元数据和上传下载 |
| 持久存储 | Volume、Mount、exclusive/shared-ro/shared-rw、snapshot/clone、容量 | workspace/home/data 的业务命名和生命周期策略 |
| 网络 | deny-by-default 声明、via/publish、规则派生、原子安装、second-hop 限制 | model proxy/data proxy 地址与产品所需 allowlist |
| 资源与健康 | CPU/memory/disk admission、activity clock、idle reaper、death cause | 是否允许自动暂停、前端文案、业务级恢复策略 |
| 事件 | 事务 outbox、游标、有限保留窗口、重放 | 投影为现有 SSE/session 事件，去重和窗口过期后的 resync |

Khora 的 `runtime.Provider`、`rootfs.Provider`、`volume.Backend` 和 `store.Store` seam 不应被 BatteryGPT 穿透。适配层只使用 ID、epoch、portable policy 和 opaque handle，不能依赖 cgroup 路径、netns fd、绝对宿主路径或 runsc container ID。

## 目标架构

```text
Browser / existing REST + SSE
              |
       BatteryGPT server
   (auth, session, turn, bridge,
    model proxy, artifacts, memory ingest)
              |
       sandboxadapter / engine
   (Session <-> Sandbox mapping,
    Placement epoch, Process handles,
    path policy, event projection)
              |
       Khora control + Connect data plane
              |
   Khora orchestrator + sandboxd
   (lifecycle, runsc, rootfs, volume,
    network, resource admission, reconcile)
```

建议新增两个明确的产品包：

1. `orchestrator/internal/engine`：封装 Khora client、连接和调用超时，不暴露 Khora 细节给 server。
2. `orchestrator/internal/sandboxadapter`：维护产品映射和策略，包括 Session/Sandbox、Turn/Process、workspace 路径白名单、Placement epoch、Khora Event/FS Watch 到现有事件的投影。

`internal/server` 只依赖 `SandboxEngine` 接口，例如 `EnsureRunning`、`Pause`、`Resume`、`StartProcess`、`ConnectProcess`、`Read/Write`、`Watch`、`RecordActivity`。它不再构造 OCI spec，也不接受 runsc/netns/path 字段。

## 核心对象映射

| 当前对象 | 目标对象 | 迁移规则 |
| --- | --- | --- |
| BatteryGPT Session | 产品 Session + 一个稳定 Khora SandboxID | Session ID 仍由产品控制；只持久化 `sandbox_id` 和产品 profile |
| `runtime_generation` | Khora Placement `(sandbox_id, epoch)` | 每次冷启动/resume 产生新 epoch；adopt 保持 epoch；不再把 generation 当物理资源实体 |
| `runtime_resource_instance` | Khora Placement snapshot/evidence | 删除宿主资源字段；产品只保留需要展示/归因的摘要 |
| 当前 Runtime Container | 适配器内的短生命周期 opaque handle | runsc ID、netns、veth、nft 只存在 Khora 内部 |
| Pi/Shell driver 进程 | Khora ProcessID + placement epoch | Turn 关联 Process handle；guest PID 不进入产品数据库 |
| 固定 rootfs | Khora immutable Template | 现有 rootfs 构建产物改造成 Khora Template；manifest 记录 driver/runtime 能力 |
| Session workspace | Khora Volume，exclusive mount 到 `/workspace` | 暂不使用 shared-rw，避免跨 sandbox cache coherence 未定义 |
| agent-home | Khora Volume，exclusive mount 到 `/agent-home` | 需要迁移现有宿主目录内容；权限在 Template/entrypoint 中处理 |
| data volume | Khora Volume + Mount | 业务层只保存 VolumeID、guest path 和访问模式 |
| network profile/egress policy | Khora Binding.Network declaration | adapter 生成 model proxy/data proxy 的明确 via/allow 规则 |
| bridge file queue | 第一阶段保留为兼容层，放入 Khora Volume | 第二阶段用 Khora Process stream/FS Watch 替换轮询和宿主 pipe |
| checkpoint/restore | 首期删除或关闭 | Khora pause 是 FS-first，保留 Volume/Overlay bytes，不保存进程内存和网络状态 |

需要特别处理两个安全差异：Khora guest 默认可视为 root，且 Khora 明确不把 guest 身份当作秘密边界；当前 BatteryGPT 依赖 uid/gid 65534、empty capabilities 和 `noNewPrivileges`。迁移时应在 Template 的 entrypoint/sandboxd 内显式降权到 65534，并重新验证 workspace/agent-home 权限。另一个差异是 Khora FS API 使用 guest-absolute path，产品 API 必须在适配器中限制到 `/workspace`、`/agent-home` 和允许的子路径。

## 分阶段实施

### P0：契约和垂直切片（2–4 人日）

- 固定 Khora commit，建立独立进程启动方式和 loopback endpoint。
- 解决 Go 1.22 到 Khora Go 1.23/toolchain 1.23.4 的构建策略；优先升级项目 toolchain，或把 Khora 作为独立二进制而非 module 依赖。
- 定义 `SandboxEngine`、`ProcessHandle`、`PlacementRef` 和错误映射，不把 Khora 类型散落在 server。
- 用现有 rootfs/entrypoint 做一个 Khora Template，创建一个 Sandbox，挂载 workspace/home，启动 bridge/driver。
- 验证 Process Start/Connect/Signal、FS Read/Write、Pause/Resume、Placement epoch 和基础网络访问。

验收标准：一个真实 turn 可以在 Khora 沙箱中启动、输出、完成；宿主进程看不到真实模型 credential；旧 REST/SSE API 不变。

### P1：运行时适配（5–8 人日）

- 实现 Khora client engine 和 Session↔Sandbox 映射。
- 把当前 `runtime.Start/Destroy/PrepareGeneration` 调用替换为 `EnsureRunning`/`Pause`/`Resume`。
- 将 rootfs、network、resource、volume 配置转换为 Khora Template/Binding/NetworkDecl/Mount。
- 把 health、activity、death cause、epoch/stale request 映射到产品错误和状态。
- 保留现有 bridge file queue，先让其通过 Khora Volume 工作。
- 为 adapter 加 fake engine contract tests，覆盖超时、409 wrong state、503 guest unavailable、410 event cursor 和 stale epoch。

### P2：生命周期、持久化和恢复（5–10 人日）

- 在产品 store 中新增稳定 `sandbox_id` 与当前 epoch；保留旧 generation 字段的读取兼容窗口。
- 新建一次性映射流程：旧 Session 创建对应 Sandbox；活动 generation 通过 Khora placement 查询恢复；无法确认的实例进入可重试 paused/interrupted 状态。
- 将 Khora Operation/idempotency 和 Event cursor 用于跨请求重试；产品 turn 继续使用自己的 turn 状态机。
- 用 Khora startup reconcile 替换当前 runtime resource scan。BatteryGPT 只 reconcile 产品映射和未完成 turn。
- 把 idle release 改成 Khora pause + 下次 fresh process；明确不再承诺 checkpoint 后继续同一进程内存。

### P3：Bridge、数据面和 artifact（5–10 人日）

- 将 driver stdout/PTY、stdin、signal 和重连切换到 Khora Process Connect stream。
- 用 FS Watch cursor 替换 workspace 轮询；处理 placement epoch 变化、overflow/reset 和 rescan。
- 保留 turn claim/ack、SO_PEERCRED 模型代理授权和现有 bridge 事件解析，这些是产品语义，不应移入 Khora。
- 将 Khora 生命周期事件投影到现有 SSE；以 Event ID 去重，cursor 过期时执行完整 session snapshot/resync。
- 把 `/workspace` artifact API 全部通过路径策略适配器访问，禁止把 Khora 任意 guest path 暴露出去。

### P4：清理和基准验收（4–8 人日）

- 删除旧 OCI/spec、runsc 命令、宿主 network projection、runtime resource evidence 和 checkpoint 代码。
- 更新 `dev/start-all.sh`、`dev/stop-all.sh`、systemd、配置和运维 runbook，使 Khora 成为单独受管进程。
- 在真实 Linux 主机跑 Khora substrate suite：runsc、overlayfs、netns、cgroup v2、nft、btrfs squota。
- 做并发 turn、pause barrier、慢 subscriber、sandbox death、OOM、Khora 重启和 cursor gap 压测。
- 完成旧数据映射/回滚开关的删除条件和上线检查。

整体估算为单人 17–36 人日，约 3–5 周；两人并行且 Khora 环境已可用时约 2–3 周。若选择独立 Khora 服务，P1/P3 会增加 3–5 人日的部署、连接健康检查和故障注入；这是值得的隔离成本。若选择复制源码嵌入，短期可能少 2–4 人日，但会增加长期维护和升级成本，不建议作为主线。

## 数据迁移与兼容策略

Khora 0.0.x 明确不承诺持久化 schema 兼容，因此 BatteryGPT 不应直接写入或读取 Khora SQLite。产品数据库只保存 Khora opaque IDs 和必要的 epoch/profile 摘要；Khora state 由 Khora 自己管理。

建议采用一次性、可重试的映射任务：

1. 为每个未销毁 Session 创建 Sandbox，并建立 workspace/home Volume。
2. 将现有宿主目录内容导入对应 Volume；导入完成后做 digest/size 校验。
3. 查询 Khora Sandbox/Placement，写入产品映射和当前 epoch。
4. 对旧 generation 中处于 active/leased 的 turn，若无法确认对应 Process，标记为 interrupted，由现有恢复策略决定是否重试。
5. 迁移完成后只读旧 generation 的展示字段，经过一个稳定发布周期再删表和旧代码。

上线前保留一个 adapter feature flag，允许按 Session 或 deployment 打开 Khora 路径；禁止同一 Session 同时由旧 runtime 和 Khora 管理，以免两套网络/挂载状态互相破坏。

## 必须保留的产品行为

- 模型 credential 永远只存在 model proxy；不通过 Khora secret mount 传入 guest。
- 现有认证、SO_PEERCRED、turn claim/ack、输出事件解析、Session/Turn API 和前端 SSE 语义保持兼容。
- `/workspace` 和 `/agent-home` 的产品路径、artifact 白名单、上传下载限制保持不变。
- Memory 数据库、nightly sweep、semantic evolution 和 `controlsource` 单向 ingest 不纳入此次重构。
- 现有 runsc 命令 timeout、资源不足归因、sandbox death notice 和清理证据要有等价的 Khora 错误映射和运维可见性。

## 主要风险和对策

| 风险 | 影响 | 对策 |
| --- | --- | --- |
| Session/generation 与 Sandbox/Placement 语义不一致 | 恢复、重试、UI 状态错乱 | 先引入稳定 SandboxID/epoch，再迁移 turn；禁止把 generation 继续当物理实例 |
| Khora 0.0.x API/state 变化 | 升级时不可预测 | 固定 commit、只依赖 `pkg/khora`/proto、adapter contract tests、Khora 独立 state |
| Go 版本不一致 | 构建阻塞 | P0 先解决 toolchain；不要在未验证前引入 replace 到 Khora internal |
| guest 默认 root 与当前 nobody 语义不同 | 文件权限/安全回归 | Template 内显式降权，加入权限和 capability substrate 测试 |
| Pause 丢失进程内存 | 用户误以为可 checkpoint 恢复 | 产品状态和文案改为“保存文件后重新启动”；首期关闭 checkpoint 入口 |
| FS Watch cursor/overflow | artifact 列表缺失 | overflow/reset 时强制 `/workspace` rescan，事件只作增量优化 |
| Khora 控制面无认证 | 暴露后可完全控制沙箱 | 仅 loopback 监听；外部访问只走现有认证层/gateway |
| shared-rw cache coherence 未定义 | 多沙箱同时写数据不一致 | workspace/home 使用 exclusive mount；共享数据显式 shared-ro 或业务层协调 |
| 真实 substrate 环境不足 | 线上才暴露 runsc/nft/btrfs 问题 | P4 纳入 Khora substrate suite 和上线主机验收，CI 标注 skip 原因 |

## 验收门槛

重构完成前至少需要通过：

- BatteryGPT：`go build ./...`、`go vet ./...`、`go test ./...`、frontend lint/typecheck/test/build、bridge Python tests。
- Khora：`go build ./...`、`go vet ./...`、`go test ./...`、generated/architecture checks。
- 集成：创建/启动/输出/完成 turn；文件读写和 artifact watch；pause/resume 后 epoch 变化；旧 epoch 请求被拒绝；Khora 重启后 reconcile；sandbox death/OOM 后产品状态可恢复。
- 安全：guest 无法读取 proxy 真实 credential；产品 API 无法访问 `/etc` 等非产品路径；Khora 控制面未暴露到非 loopback。
- 性能：首个 Sandbox 冷启动、resume、Process Connect、artifact rescan 和并发 Session 数量与当前基线对比并记录。

当前工作树有未提交的 BatteryGPT 前端、server/store 改动。正式实施前应先在独立分支或 worktree 固化这些改动，避免迁移期间把业务修复和沙箱替换混在同一组 diff 中。

