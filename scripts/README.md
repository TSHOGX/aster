# scripts/

publish 环境的运维脚本。

| 脚本 | 用途 |
| --- | --- |
| [`harness-ctl`](#harness-ctl) | publish 环境运维 CLI：监控流量/在途对话、安全上下维护页、防打断重启 |
| `verify-refactor.sh` | 重构验证脚本 |

---

## harness-ctl

一个 bash CLI，把 publish 环境最常用的运维动作固化成安全、可复现的子命令：
看流量、看有没有正在进行的对话（避免重启打断用户）、一键上/下维护页、
重启前自动拦截在途对话。

已软链到 `PATH`：`/usr/local/bin/harness-ctl -> main/scripts/harness-ctl`，
任意目录下直接 `harness-ctl <cmd>`。

> 运维总览见 [`docs/operations.md`](../docs/operations.md)；维护模式机制见其第 9 节。

### 子命令

```
harness-ctl status                服务/入口/在途对话 一屏总览
harness-ctl inflight              列出正在进行的 turn（含时长、僵尸 lease 标注）
harness-ctl traffic [N]           汇总最近 N 行 nginx 流量（默认 5000）
harness-ctl stats [天数]          每日提问/会话/用户新增表 + 柱状图（默认 14 天）
harness-ctl users [N] [--by=…]    用户维度用量与存储 Top N（--by=msgs|storage|sessions）
harness-ctl user list|add|passwd|rm  登录账号管理（改完免重启生效）
harness-ctl storage [N]           存储总览：按目录 + Top N 会话占用
harness-ctl maintenance on        上维护页（nginx 503，后端不动）
harness-ctl maintenance off       下维护页，恢复服务
harness-ctl maintenance status    只看维护模式开关
harness-ctl safe-restart <svc>                  零打断重启：上维护页→等活跃对话跑完→重启→下维护页
```

服务名限 `harness-publish-orchestrator` / `harness-publish-frontend`。

### 各命令说明

#### `status`
一屏看全：4 个 systemd 服务状态、对外是否维护模式、公网/后端/前端 HTTP 探活、
以及当前在途对话数。重启前先跑它。

#### `inflight`
列出所有「正在进行」的 turn（`turns.status` ∈ `queued|leased|running`），
标注每个的时长和 lease 是否过期，并区分：

- **真活跃**：lease 未过期的 `running`/`leased` + 所有 `queued`（用户在等回复）。
- **僵尸 lease**：`leased`/`running` 但 `lease_expires_at` 已过期——是卡死残留，
  不代表有人在等，重启它们无害。

这是「防止重启打断对话」的依据。

#### `traffic [N]`
从 `/var/log/nginx/access.log` 取最近 N 行（默认 5000），汇总：时间窗口与粗略 QPS、
HTTP 状态码分布、Top 10 客户端 IP、Top 10 请求路径。纯只读。

#### `stats [天数]`（别名 `usage`）
按**本地自然日**（UTC+8）列出每日新增：user message（提问）、assistant message（回复）、
新会话、活跃用户、新用户，末尾给合计。表格下方是提问量纵向柱状图（Unicode 方块，
带 y 轴刻度与日期轴）和新会话/活跃用户的 sparkline。默认 14 天，纯只读。

口径（踩过坑，别改错）：

- **用户身份看 `sessions.user_id`，不要查 `users` 表**——后者只有 1 行内置账号 `lab`，
  真实工号由外部系统托管、直接写在 `sessions.user_id`。
- **不要用 nginx `remote_addr` 数人**——前面有代理，access.log 全是 `127.0.0.1`。
- `created_at` 是 UTC（带 Z），一律 `date(created_at,'+8 hours')` 归到本地日。
- 无活动的日子由 SQL 里的 `spine` 递归 CTE 补 0 行，折线/柱状图才不会错位。

#### `users [N] [--by=msgs|storage|sessions]`
用户维度一张表：会话数、提问数、存储占用、首次/最近活跃日，默认按提问数排序取 Top 20。
下方两张横向条形图（存储占用、提问量）。合计行统计**全部**用户，不止 Top N。

存储 = 该用户所有 session 的 `sessions/<id>`（workspace）+ `agent-homes/<id>`（agent HOME）
之和。DB 里已无记录、目录还在的算到 `(孤儿目录)` 一行，方便发现该清的残留。

#### `storage [N]`
数据根目录 `/var/lib/harness-publish` 的总览：磁盘水位、按子目录占用条形图、
`orchestrator.db(+wal)` 单独一行，以及 Top N 会话占用（标签为「工号 + 会话标题」）。
默认 Top 15。

> `du` 必须以 root 跑：session workspace 是 `0750 nobody:nogroup`，父目录可读但子目录不可读，
> 非 root 直接 `du` 会**静默少算**。脚本内部统一走 `as_root`。

#### `user list|add <名>|passwd <名>|rm <名>`
登录账号管理。注意和只读统计命令 `users`（复数）区分：**`user` 改凭据，`users` 看用量**。

```bash
harness-ctl user list                        # 只打印算法/迭代数，从不打印哈希
harness-ctl user add alice                   # 交互式输入密码两次
harness-ctl user passwd alice                # 默认吊销该账号已有 cookie
harness-ctl user passwd alice --keep-sessions  # 改密码但不踢下线
harness-ctl user rm alice                    # 删最后一个账号需 --force
```

**改完立即生效，不要重启 orchestrator**（重启会杀掉所有活跃沙箱）。后端发现
`config/auth-users.yaml` 的 mtime 变了就重载凭据，下一次登录请求就用新的。

实现分工：真正的读写在 orchestrator 二进制的 `user` 子命令里（复用后端的 YAML 校验、
PBKDF2 参数、会话吊销）；脚本只负责定位二进制、从 systemd unit 取
`HARNESS_AUTH_USERS_PATH`/`HARNESS_DB_PATH` 保证和服务读同一份文件，以及用 `read -s`
静默读密码后管道喂进去（密码不进 argv、不进 shell history）。非 TTY 下直接透传 stdin，
便于脚本化。

几个刻意的安全取舍：

- 写文件是「同目录 temp → 0600 → rename」原子替换，中断不会留半个文件。
- 删最后一个账号会让登录闸**对所有人开放**，所以必须显式 `--force`。
- 文件写坏/被删时后端保留内存里上一份凭据并打 ERROR，**不会**因此放开登录闸；
  但重启后就真的裸奔了，所以坏文件要尽快修。
- `rm` 只删登录凭据，该用户的历史会话和数据仍在库里（`sessions.user_id` 无外键）。

#### `maintenance on|off|status`
在 **nginx 层**挂/撤维护页，**完全不动后端**（orchestrator/frontend/DB/会话照常跑）。

- `on`：先备份当前正常配置为 `.bak.pre-maintenance.<ts>` 并刷新 `.normal` 快照，
  再从**当前 live 配置动态生成**维护版（把反代 `location /` 换成 `return 503` +
  维护页，其余 TLS/证书/端口原样继承），`nginx -t` 通过才 reload。
  **若生成的配置 `nginx -t` 不过，自动回滚到 `.normal`，绝不 reload 坏配置。**
- `off`：从 `.normal` 快照恢复正常反代（找不到则退回最近的 `.bak.pre-maintenance.*`）。
- reload 后固定 `sleep 3` 再探活：nginx 平滑重载时旧 worker 仍在 draining，
  紧跟其后的探测可能命中旧 worker（曾误判维护页没生效），等 worker 接管后再验证。

维护页文件：`/var/www/harness-maintenance/maintenance.html`（纯静态，不在 git 里）。

#### `safe-restart <svc> [--timeout=秒] [--interval=秒] [--force]`
只允许重启 `harness-publish-orchestrator` / `harness-publish-frontend`。
**固定走一套零打断流程，维护页强制、不可关闭**：

```bash
harness-ctl safe-restart harness-publish-orchestrator
```

一条命令自动完成四步：

1. **上维护页**（nginx 层 503）挡住新对话进来——不挡新流量的话活跃数会被新请求持续补进、迟迟不归零；
2. **循环 drain**：每隔 `--interval` 秒（默认 5s）轮询一次，等正在跑的对话全部收尾，最长等 `--timeout` 秒（默认 900s）。期间实时刷一行进度：`活跃(等待中) / 排队 / 僵尸(不等) / 待排查 / 剩余秒数`；
3. **重启服务**并自动探活；
4. **下维护页**恢复对外服务。

超时仍没归零则默认**不重启**并自动回退维护页（不会把站晾在维护态）；确要超时也硬重启，加 `--force`。
若进入前站点本就处于维护模式，则沿用现状、重启后也不动维护页（避免误关别人开的维护窗口）。

> ⚠️ **drain 等的是「沙箱心跳新鲜」的活跃 session，不是「lease 未过期」的。**
> 因为 orchestrator 的 maintenance 循环会无条件自续租 generation lease，僵尸
> session 的 lease 永不过期——若按 lease 判，一个僵尸就能让循环干等到超时。故 drain
> 改用沙箱 bridge 心跳新鲜度（判据同 `zombies` 命令）识别谁真在跑，僵尸单独计数、
> **不纳入等待**（重启本就是清理它们的手段）。

需要时调等待参数（也可用环境变量 `DRAIN_TIMEOUT` / `DRAIN_INTERVAL` 覆盖）：
```bash
harness-ctl safe-restart harness-publish-orchestrator --timeout=1800 --interval=3
```

### 依赖与权限

- 依赖：`bash`、`sqlite3`、`curl`、`systemctl`、`nginx`、`awk`（机器上均已有）。
- 只读命令（`status`/`inflight`/`traffic`/`stats`）普通用户即可（DB `0644` 可读、日志需属 `adm` 组或 sudo）。
- `users`/`storage` 只读，但要统计 session workspace 大小需 root `du`（目录 `0750 nobody`），
  非 root 下会走 `sudo`。图表用 gawk 渲染，依赖 UTF-8 locale（机器上 `en_US.UTF-8`）。
- `maintenance`/`safe-restart` 需要能 reload nginx / 控制 systemd。脚本内部按需 `sudo`：
  以 root 运行时直接执行，否则走 `sudo`。
  - `systemctl start/stop/restart/status` 这两个 publish 服务：watt 已有免密 sudoers
    （`/etc/sudoers.d/harness-publish`）。
  - **`nginx -t` / `systemctl reload nginx` 目前 watt 无免密 sudoers**——`maintenance on|off`
    在 watt 下会要 root 密码。要让 watt 免密操作维护页，需给 nginx 相关命令补一条 sudoers 规则。

### 数据来源

| 信号 | 来源 |
| --- | --- |
| 服务状态 | `systemctl is-active`（nginx / orchestrator / frontend / claude-proxy） |
| 维护模式 | live nginx 配置里是否有生效的 `return 503` |
| 在途对话 | 只读查 SQLite `/var/lib/harness-publish/state/orchestrator.db` 的 `turns` 表 |
| 流量 | nginx `/var/log/nginx/access.log`（combined 格式） |
| 提问/回复量 | `messages` 表按 `role`（`user`/`assistant`）分组，`created_at` +8h 归日 |
| 会话/用户 | `sessions` 表（`id`、`user_id`、`created_at`、`title`） |
| 存储占用 | `du -sk` 扫 `sessions/<id>` + `agent-homes/<id>`，按 `sessions.user_id` 归并 |

`turns.status` 的取值由 DB CHECK 约束固定为
`queued|leased|running|completed|failed|canceled`，前三个即「在途」。
若后端改了这套状态机，需同步更新脚本顶部的 `INFLIGHT_STATES`。

### 设计原则

- 只读监控零副作用；有副作用的操作（维护/重启）一律「先备份 → 校验 → 执行 → 探活」。
- 维护配置从当前 live 配置动态生成，不硬编码模板，避免证书/端口漂移。
- 破坏性前置检查：`safe-restart` 默认拦截、`maintenance on` 自动回滚坏配置。
