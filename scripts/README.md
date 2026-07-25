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
- 只读命令（`status`/`inflight`/`traffic`）普通用户即可（DB `0644` 可读、日志需属 `adm` 组或 sudo）。
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

`turns.status` 的取值由 DB CHECK 约束固定为
`queued|leased|running|completed|failed|canceled`，前三个即「在途」。
若后端改了这套状态机，需同步更新脚本顶部的 `INFLIGHT_STATES`。

### 设计原则

- 只读监控零副作用；有副作用的操作（维护/重启）一律「先备份 → 校验 → 执行 → 探活」。
- 维护配置从当前 live 配置动态生成，不硬编码模板，避免证书/端口漂移。
- 破坏性前置检查：`safe-restart` 默认拦截、`maintenance on` 自动回滚坏配置。
