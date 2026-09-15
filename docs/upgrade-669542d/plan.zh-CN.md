# 669542d 现网上线评估与升级方案


> 本文是两个源码提交之间的比较和通用升级示例。实际运行产物必须另行核验；机器盘点与部署记录不属于公开文档。

日期：2026-09-15。范围：代码合并、源码对比、离线迁移演练和现网操作方案；本次没有连接或修改生产服务/数据库。

## 1. 精确版本与结论

- 现网基线（用户提供，Git PR head 已核实）：`669542d4ab3453509a6872e996cdf38bc7331edf`。
- 合并前远端 main：`1dc1343133ff3cd05a88eab8d1eef37489314b6b`。
- PR #4：`feat/kb-per-entry-review`，head = 上述现网基线。
- 应用合并提交：`237f8b3b82cb6bc03f141da3df1f98e55445670e`，双亲依次为原 main 和现网基线。后续本目录及演练脚本的提交只增加文档/工具，不改变应用代码。
- 合并只有 `orchestrator/internal/server/maintenance.go` 两个冲突块，均为丢 tick 日志：采用 PR 的互斥锁保护计数及每分钟统计，保留 main 的 stage 超时、独立 cold fallback、运行态同步及 scheduler 启动。自动合并的 main.go/server.go 也已检查 KB 注入及路由存在。
- 应用差异：120 文件，17,901 行增加、717 行删除。现网的 KB 单条审批、证据门槛、canonical facts、evolution 页面全部保留；`internal/memory`、`internal/memorystore`、`internal/dbsplit`、`internal/sqlitex` 对基线 **零差异**。

**建议：安排停写维护窗口升级，先用一致的真实备份演练，再整体切换后端、前端和 sandbox rootfs。不执行 db-split，不在运行中的数据库旁启动新版本试探，不按普通“换旧二进制”方式回滚。**

GitHub GraphQL API 返回 401，无法读取 PR 的审批/CI/保护规则详情；Git SSH 能读取和正常推送。本次依靠双亲 merge commit、完整本地检查和普通 fast-forward push（不 force、不绕过服务端拒绝）完成 Git 层整合。网页 PR 是否已标记 merged 需恢复 API 后确认，不将不可读取的远端 CI 声称为通过。

## 2. 对比明细

| 模块 | 相对 669542d 的变化 | 上线含义 |
|---|---|---|
| 用户/管理基础 | users.role、feature flags、审计、admin API | 旧认证 YAML 缺少 role 时仍为 user。管理员角色来自认证配置，不能只改 DB；密码/token 不需要迁移 |
| 定时任务 | 新 schedules/runs、手动运行、重试/超时、结果落已有会话、归档、misfire 限制 | 会自动处理持久任务；需要分别验证 pause/archive/重试与不补跑陈旧窗口 |
| 消息/turn | 新消息在 dispatch 时落 transcript；运行状态快照及 revision；Stop 原地 abort，队列停放、逐条 send/steer | 前后端及 bridge 必须配套；旧排队消息迁移不重写，需要验证显示和发送无重复 |
| 运行时 | stale session 修复、死 sandbox 立即回收、lease grace、bridge inbox prune 修复、输出批量提交/终态丢弃 | 新进程启动和 maintenance 会写运行态、清理资源；schema 演练不等于启动无副作用 |
| 数据库性能 | 高频读取转 reader pool、输出批量提交 | 保留 SQLite WAL 单 writer；不能新旧实例共用生产库做蓝绿 |
| sandbox/Pi | abort/steer 控制文件、agent turn 边界、构建期 RPC 合约检查 | 必须构建并验证新的 rootfs；旧存活进程/内存 checkpoint 不能视为已升级 |
| 运维 | 优雅关闭 SSE/WS、heartbeat/zombies 诊断 | 新版关闭改进不能修复正在退出的旧版本；停机需等实际进程结束 |
| KB/演化 | 保留现网实现，无 memory DB schema 增量 | 保留 DB、KB current/pending/snapshots、轨迹和外部演化工程；无需 replay/reseed |

复核命令：

```bash
git diff --stat 669542d 237f8b3
git diff 669542d 237f8b3 -- orchestrator/internal/store/schema.go
git diff --exit-code 669542d 237f8b3 -- orchestrator/internal/memory orchestrator/internal/memorystore orchestrator/internal/dbsplit orchestrator/internal/sqlitex
```

## 3. 数据库迁移逐项审计

迁移入口：`store.OpenWithOptions → migrate → ensureSchema`，在 reader pool 创建前执行。没有独立 schema version 表，也没有覆盖整个迁移过程的统一事务；按列存在性和 IF NOT EXISTS 重入。应用打开数据库就会执行 DDL；`OpenForMaintenance` 特意跳过迁移，不能用 `orchestrator user ...` 代替迁移。

| 对象 | 迁移及旧数据处理 | 风险/验收 |
|---|---|---|
| users.role | ADD COLUMN TEXT NOT NULL DEFAULT 'user'；insert/update guard 约束 user/admin | 老用户保留且默认为 user；认证文件不需要批量改写；admin 授权独立验收 |
| feature_flags | 新表，插入四个默认值，ON CONFLICT DO NOTHING | cron_agent=true；admin_console/skill_store/evolution_admin=false；已有值不覆盖。feature flag 不是 scheduler 总开关 |
| admin_audit_log | 新表及两索引 | 无历史审计补造；新写入验收 |
| agent_schedules | 新表（含 archived=0、result_mode=new_session、target_session_id=NULL）及索引 | 纯基线无旧 schedule，要新建空表；若现场已有则说明曾跑过其他版本，必须核实 |
| schedule_runs | 新表及四索引，status 包含 skipped_busy | 纯 669542d → 目标不触发表重建；已有窄 CHECK 的中间版 DB 才执行事务内 create/copy/drop/rename，保留所有列与四索引。自定义列/触发器/引用此表的 FK 不在重建保留范围，发现就停止并单独设计迁移 |
| sessions.turn_revision | ADD INTEGER NOT NULL DEFAULT 0 | 不回放历史事件；后续触发器递增；会话/历史内容保留 |
| messages.turn_id | ADD 可空 INTEGER，新增 messages_turn_idx | 历史 NULL 不做猜测回填，不修改历史消息 ID/文本/trace/feedback/share 引用 |
| turns | phase=''；phase_at/tool/detail=NULL；materializes_message=0；auto_dispatch=1；steered_into_turn_id=NULL | 旧消息已在 enqueue 时落库，因此 materializes_message=0 避免重新生成消息。旧 queued 仍可自动派发，不自动停放。**不要全表改成 materializes_message=1** |
| revision triggers | sessions、runtime_generations、runtime_generation_resources 的运行态触发器；turns 的三个触发器每次重建 | 增加写放大；重开后存在且 revision 随状态更新递增。升级库的部分列没有新建库的 CHECK/FK（如 steered_into_turn_id），这是实现差异，应知悉 |
| memory.db | 九张表与源码均不变 | 语义记忆、技能、证据 session IDs、审核状态保持；不复制回控制库，不清空或重抽取 |
| 既有 schema repair | ensureRuntimeGenerationColumns、通知列、messages FTS 等旧逻辑继续运行 | 真实 DB 缺列/空 FTS 索引可能触发已有补齐；不能只根据 commit 假设现场 schema 完整 |

`669542d` 的 main.go 已同时打开 orchestrator.db 和 memory.db。`docs/db-split-runbook.md` 适用于更早的单库版本，**不是本次常规步骤**。如果真实备份只发现控制库内有 memory 表而 memory.db 空/缺失，应先查服务实际路径/环境及过去拆库情况，禁止创建空库掩盖问题。不要按旧文档固定的“25 表”判断成功，FTS shadow tables 也会影响计数。

## 4. 已完成的验证与证据边界

在隔离 worktree `/tmp/batterygpt-pr4`：

- `go build ./...`、`go vet ./...` 通过；以 root 运行 `go test ./...` 全通过，与 CI 的权限要求一致。
- `npm ci`、lockfile registry 检查、lint、typecheck、17 文件 224 前端测试、Next production build 全通过。
- bridge Python 60 测试全通过。
- [演练脚本](../../scripts/rehearse-upgrade-669542d.py)：用 **旧提交的真实 Go store** 创建 DB，植入中文历史消息、completed/queued/canceled turns、轨迹快照、episode、accepted fact、published skill 和证据列表；用目标提交真实 store 打开，再打开一次。所有旧表旧列行数及逐行 SHA256 集合保持一致，integrity_check=ok、foreign_key_check 为空，memory 表/列和数据保持，二次打开稳定。见 [完整机器结果](rehearsal.json)。
- 已有测试另覆盖 schedule_runs 窄 CHECK 重建保留三行/四索引、phase 旧库重开和重入、runtime revision、Stop/park/steer、权限以及 KB 审批。

**尚未完成（现网放量前必须完成）：**真实生产一致备份的演练、生产磁盘规模/耗时测量、完整应用在隔离主机启动、真实 gVisor/Pi/model proxy 冒烟、真实生产环境变量/路径/外部演化工程的核对。合成数据不证明所有现场历史脏数据均可迁移；本次没有读取现网数据。

## 5. 现网执行步骤

### A. 维护前准备

1. 确认实际运行 binary 的 SHA/healthz、systemd ExecStart/WorkingDirectory/EnvironmentFile；将目标固定到完整 SHA（237f8b3 或仅附加本文档/脚本的后继提交），记录制品 SHA256。不可现场 `git pull` 后随意部署漂移的 main。
2. 核对真实路径：HARNESS_DB_PATH、HARNESS_MEMORY_DB_PATH、HARNESS_AGENTS_KB_PATH、HARNESS_TRAJECTORY_ROOT、HARNESS_AGENT_HOMES_ROOT、workspace/data volume、share blobs、checkpoints、HARNESS_ROOTFS_PATH；保留 config、认证文件、LLM/model proxy/MCP/Doris 凭据、权限/UID/GID 和 nginx/systemd 配置。只记录路径/权限，不把秘密写入报告。
3. evolution 前端依赖独立 `EVOLUTION_PROJECT_ROOT`、`EVOLUTION_STATE_ROOT`、`EVOLUTION_MEMORY_DB`、`EVOLUTION_BUN`，默认外部工程 `/home/agent-runtime-platform/trajectory-evolve` 并不在本 repo。保留其版本、运行记录及 agents-kb 状态；检查其 DB 路径与后端实际相同。EVOLUTION_VIEWERS 的默认 `*` 是现网已有行为，不能因新增 admin 基础就误以为全部演化接口已受 admin flag 控制。
4. 构建到独立 release 目录：Go binary（ldflags 注入 SHA）、npm ci/build、sandbox rootfs。不要覆盖运行中的 `.next`、binary 或 rootfs。Pi 版本固定为部署确认的版本（仓库默认 0.77.0），新 rootfs 必须通过 RPC 合约验证。完整构建路径会执行检查；FORCE=0 复用路径并不会重新安装 Pi/自动执行该检查，不能仅凭“复用成功”认定合约通过。
5. 盘点定时器、cron、nightly、独立 backfill/replay、前端触发的 Bun 子进程和所有 DB/KB 写者。预留两库、WAL、持久目录备份与迁移演练的空间；如存在 schedule_runs 重建，另留该表/索引/WAL空间。实际维护时长根据真实备份演练和复制量确定，不作无证据分钟数承诺。
6. 先在一份已停写一致备份上运行以下脚本（不在生产 live state 目录上运行）：

```bash
# 在目标源码 checkout，Python 3 + Go + git 可用；两文件位于同一快照时点。
python3 scripts/rehearse-upgrade-669542d.py \
  --target 237f8b3b82cb6bc03f141da3df1f98e55445670e \
  --snapshot-dir /secure/upgrade-snapshot/state \
  > /secure/upgrade-snapshot/rehearsal.json
```

脚本只读输入备份，以 SQLite backup API 复制到临时目录，再只迁移副本，不启动服务、不执行 runtime recovery、不调用 LLM。结果含元数据和摘要，不输出行内容；临时副本运行后删除。它严格拒绝已有完整性/FK问题或旧列数据变化（例如此前 FTS 缺失而被补齐）；失败需解释并修复演练方案，不能直接忽略。它也不会产出待替换生产库，实际迁移由目标应用首次启动执行。

7. 全应用预演必须另用隔离主机/网络、复制后的所有持久路径、独立 runsc/network namespace，禁止指向生产 DB/KB/workspace/rootfs。演练旧会话恢复、权限、KB pending 和 Stop/steer；确认旧 checkpoint 不会恢复旧 bridge 进程。不能证实冷启动路径或 checkpoint 兼容性时，不放行。

### B. 进入维护与最终一致备份

1. 维护页挡新流量；等模型请求、turn 和外部演化任务完成，核对 queued/leased/running 数量。维护页只是 nginx 503，已有 SSE/WS 和后台任务不会自动停止。
2. 停止前端、后端、nightly/cron/timer 与独立演化进程；确认进程真正退出，写入者全部停止。旧 orchestrator 的退出可能仍等流连接，应按服务超时机制处理并核实，不能把 systemctl 命令返回当成所有子进程已终止。
3. 在真正停写状态，对两库所在 state 目录做整目录备份，**包含 DB、-wal、-shm（如存在）**；同时快照 KB（含 current 符号链接、pending.json、snapshots/reports）、native traces、agent homes、workspace、share blobs、checkpoint、外部 evolution state。不得仅复制 SQLite 主文件而丢弃 WAL。保留所有权、符号链接及 xattr/ACL（用支持它们的备份/卷快照方式），校验备份可读与恢复流程。
4. 记录备份时间、SHA、配置/rootfs/Pi/runsc 版本、文件散列、表行数、任务状态分布、KB current/pending digest。两库执行只读 integrity_check / foreign_key_check；真实备份再跑一次上面的 store-only 演练。
5. 用应用支持的生命周期操作安排旧 sandbox 退出/新 generation 冷启动，保留会话/工作区/历史文件。不要直接 DELETE sessions/turns 或手改 lease“消除僵尸”。若旧 checkpoint 含旧 Pi/bridge 内存，不允许自动恢复它来完成版本切换；先在预演中确认保留数据的冷启动路径，未确认则停止上线。

示意服务命令（仅适用于已核实与文档同名的 publish 部署）：

```bash
harness-ctl maintenance on
sudo systemctl stop harness-publish-frontend
sudo systemctl stop harness-publish-orchestrator
# 另行停止盘点到的 timer/cron/Bun 等写者；确认停止后才快照。
```

### C. 切换与检查

1. 切换完整 release 的 binary、frontend assets、rootfs 路径及审核过的配置；保留原 release 原样。旧认证文件继续有效。新配置建议 bridge.poll_interval=50ms、proxy_lease_grace=60s（必须小于 heartbeat stall grace），scheduler 参数按目标配置核对。
2. 初期设 `harness.scheduler.enabled: false`，并停 nightly/禁用 memory-llm 自动循环。但必须理解：scheduler 仍恢复 queued/running rows，false 只阻止新 cron 触发；feature_flags.cron_agent=false 也不停止后台派发。纯基线应无 schedule 数据；如现场已有，先逐个通过支持的禁用操作处理并确认无运行任务，不能靠这个配置实现停写。
3. 保持入口维护，启动后端。首次启动会执行 schema DDL，随后启动恢复、资源回收及后台逻辑；观察 migration/open store、FK/CHECK、SQLITE_BUSY、owner/lease、recovery 和重试日志。失败即保持维护，禁止反复启动试运气。
4. 启动前端；从受控内部入口做下面验收。不能仅凭 `/healthz` 正常就下维护页。

| 验收 | 必须满足 |
|---|---|
| DB | 两库 integrity/FK 正常；旧表数据无非预期损失；新增四表、列、索引/trigger 齐全；role/default 符合预期；运行态变化能解释 |
| 历史/用户 | 旧账号登录、历史文本/trace/feedback、附件/分享链接、workspace 文件、native pi history 可用；旧 queued 不重复生成消息 |
| 新消息 | 真实 Pi 一轮成功，proxy 正常；连续两轮不串输出；停止进行中的 turn 后无后续旧输出污染 |
| 队列 | Stop 保留其余 queued；单条发送可在活 sandbox steer；已回收 sandbox 冷启动后释放 parked turn 成功 |
| 运行态 | 刷新页面/SSE 重连状态一致；死亡 sandbox 会被回收；maintenance heartbeat 递进；无 runaway cold starts |
| KB | current/pending digest、审批项、facts/evidence/skills 原样可读；外部 evolution 页面连到正确 DB/工程；正式审批写入在专用演练数据上验证 |
| 定时任务 | 先不开放；专用测试账号验证一次性手动/cron、已有会话结果、disable/archive、misfire 不追跑、并发限制与重启恢复 |
| 权限 | 普通用户不能访问 admin；管理员使用显式认证 role；现有 evolution 访问策略单独核对 |

5. 通过后解除维护，小范围放量；监测错误率、SQLite busy/锁等待、磁盘/WAL、model 401/403/5xx、队列与僵尸/重启数。再分别恢复 scheduler/nightly，观察至少覆盖一次真实周期，不一次性全开。新建 release rootfs 才能确保新进程使用新 bridge；现场保留原 rootfs 用于回退。

### D. 回滚

- **迁移失败或未开放用户写入时**：维持维护，停止全部新版本写者，先保留失败现场（两库与日志）；恢复同一时点两库及所有被新进程改动的 KB/evolution/workspace/agent-home 等持久状态，再恢复旧 binary、前端、配置、rootfs。DB 在空的替换目录中恢复整套备份，确保不混入新版本遗留 WAL/SHM；不要把旧主文件覆盖到新 WAL 旁。内部验证旧历史/KB后才下维护。
- **已开放写入后**：恢复 T0 快照会丢失 T0 后用户消息、workspace 修改、KB审批和运行记录。应立即再停写并保存 T1 完整现场，优先向前修复；如必须恢复 T0，先评估并获得业务对数据损失的接受，再设计导出/人工重放。禁止用 INSERT SELECT 盲目合并两版本 turns/messages/运行态。
- 不保证只换回 `669542d` binary 安全：新 turn 的延迟消息落库、park/steer 状态旧代码不理解；即便新增列兼容，行为和数据语义不兼容。新 revision triggers 仍会存在；旧启动还会重新对齐 auto-checkpoint 设置。
- **不执行 `db-split --reverse`**：两版本都用独立 memory.db，逆向拆库会把旧 binary 仍需读取的 memory 数据搬错位置。

放行底线：真实备份演练通过、成套可恢复快照完成、旧 sandbox/checkpoint 兼容或冷启动已验证、实际模型端到端与历史数据验收通过。当前只能宣布代码集成与合成迁移演练通过，不能宣布现网升级完成。
