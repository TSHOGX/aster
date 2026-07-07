# Agent Runtime Platform

Control plane for long-lived, sandboxed AI agent sessions.

这个项目把每个 AI agent 会话运行在独立的 gVisor sandbox runtime 中，并由 Go orchestrator 管理会话、turn、runtime generation、事件流和 artifact。前端 workbench 负责会话操作、实时输出和文件产物浏览。

## 定位

本项目的核心定位是 **Agent Runtime Platform**：

- **Control Plane**: 管理 session/turn 生命周期、runtime generation、Agent Bridge、事件、artifact metadata、proxy correlation、quota 和 retention。
- **Sandbox Runtime**: 使用 gVisor `runsc` 启动隔离 sandbox，提供 rootfs、mount、network namespace、agent process、checkpoint/restore 等执行能力。
- **Workbench**: 使用 Next.js 提供浏览器界面，展示会话、实时输出和 artifact。

## 技术栈

- **Backend**: Go orchestrator, SQLite, HTTP API, SSE/WebSocket event endpoints
- **Frontend**: Next.js, TypeScript, same-origin API proxy, live artifact browser
- **Runtime**: gVisor `runsc`, OCI bundle/rootfs, per-generation sandbox resources
- **Agent paths**: Pi and PTY-backed shell; deployment config selects the product `Agent` default
- **Control protocol**: Agent Bridge file-queue claim/ack protocol
- **Artifacts**: host-side metadata watcher with safe read-only downloads/previews

## 快速启动

仓库默认 lab 配置中，Workbench 的 `Agent` 模式解析到 Pi，`Shell` 也启用。启动前先让 active rootfs 的 `/etc/harness-image/agents.json` 覆盖这两个 product modes：

```bash
SANDBOX_AGENT_DRIVERS=pi,sh ./sandbox-image/build-rootfs.sh
```

如果已有 rootfs 缺少这些 CLI，用同一 driver set 加 `FORCE=1` 全量重建；非 `FORCE` 路径只同步 overlay 并重新生成 manifest。

Orchestrator:

```bash
cd orchestrator
go run ./cmd/orchestrator
```

Frontend:

```bash
cd frontend
npm install
PORT=8000 npm run dev
```

默认访问：

- Frontend: <http://127.0.0.1:8000>
- Orchestrator: <http://127.0.0.1:8090>

健康检查：

```bash
curl -sS http://127.0.0.1:8090/healthz
curl -sS http://127.0.0.1:8000/api/healthz
curl -sS http://127.0.0.1:8090/api/deployment-capabilities
```

## 登录与多用户隔离

Workbench 支持用户名/密码登录，每个用户只能看到并操作自己创建的会话。

### 配置用户凭据

凭据放在 `config/auth-users.yaml`（已被 `.gitignore` 忽略，不会进 git）。复制模板并填入哈希：

```bash
cp config/auth-users.example.yaml config/auth-users.yaml
```

密码只以 PBKDF2-SHA256 哈希存储，绝不存明文。用内置工具生成哈希（`go.mod` 在 `orchestrator/` 下，需在该目录运行，或用 `go run -C`）：

```bash
# 从仓库根目录
echo -n '你的密码' | go run -C orchestrator ./cmd/authhash
# 或先进入 orchestrator 目录
cd orchestrator && echo -n '你的密码' | go run ./cmd/authhash
# 也可用 -password 传入（注意会留在 shell 历史里）
go run -C orchestrator ./cmd/authhash -password '你的密码'
```

把输出的 `pbkdf2-sha256$...` 行填入对应用户：

```yaml
users:
  - username: lab
    password_hash: "pbkdf2-sha256$600000$...$..."
  - username: alice
    password_hash: "pbkdf2-sha256$600000$...$..."
```

- 用户名只允许字母、数字、`_`、`-`（1-64 字符）。
- **保留 `lab` 用户**：登录前已存在的历史会话都归属于 `lab`，用 `lab` 登录才能继续看到它们。
- 若 `config/auth-users.yaml` 不存在，orchestrator 以开放模式运行（单一 `lab` 用户、无需登录），仅适合本地开发。

### 相关环境变量

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `HARNESS_AUTH_USERS_PATH` | `config/auth-users.yaml` | 凭据文件路径 |
| `HARNESS_AUTH_SESSION_TTL` | `24h` | 登录会话有效期 |
| `HARNESS_COOKIE_NAME` | `harness_auth` | cookie 名（HTTPS 下自动加 `__Host-` 前缀） |
| `HARNESS_COOKIE_INSECURE` | `false` | 设 `1` 关闭 Secure cookie（仅本地 http 调试用） |
| `HARNESS_TRUST_FORWARDED_PROTO` | `true` | 信任反代传来的 `X-Forwarded-Proto` 判定 HTTPS |

登录会话是服务端状态：存于 SQLite，可吊销、带过期，登出即吊销。后台维护循环会定期清理过期会话。登录失败按 用户名+IP 限速（15 分钟内 5 次失败后锁定）。

### HTTPS 部署（前置反代 TLS 终结）

orchestrator 自身只跑 HTTP，**生产环境必须**在前面放一个做 TLS 终结的反向代理，否则 cookie 和密码会明文传输。示例（nginx）：

```nginx
server {
  listen 443 ssl;
  server_name workbench.example.com;
  ssl_certificate     /etc/ssl/workbench.crt;
  ssl_certificate_key /etc/ssl/workbench.key;

  location / {
    proxy_pass http://127.0.0.1:8000;          # Next.js workbench
    proxy_set_header Host              $host;
    proxy_set_header X-Forwarded-Proto https;   # 让 orchestrator 知道是 HTTPS
    proxy_set_header X-Forwarded-For   $remote_addr;

    # SSE 事件流需要关闭缓冲
    proxy_buffering off;
    proxy_read_timeout 1h;
  }
}
```

把 orchestrator（`:8090`）和前端（`:8000`）都绑到 `127.0.0.1`，只通过反代对外暴露。Secure cookie 与 HSTS 会在判定为 HTTPS 时自动启用。


## 目录结构

```text
agent-runtime-platform/
├── config/             # harness.yaml、登录用户等配置
├── docs/               # 架构与设计文档（见 docs/README.md 索引）
├── orchestrator/       # Go control plane
├── frontend/           # Next.js workbench
└── sandbox-image/      # rootfs 构建脚本、OCI 模板、entrypoint、bridge client、skills
```

完整文档从 [`docs/README.md`](docs/README.md) 入手；架构总览见
[`docs/architecture.md`](docs/architecture.md)，部署运维见
[`docs/operations.md`](docs/operations.md)。
