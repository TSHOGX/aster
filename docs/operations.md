# Publish 部署与运维手册（watt 操作指南）

> 面向 `watt` 用户。说明 publish 这套服务当前怎么部署的、日常怎么重启/排障、
> 以及为什么后端必须用 root 跑而 watt 仍能免密运维。
> 最后更新：2026-07-08。
>
> 系统架构见 [architecture.md](./architecture.md)；配置项与端口见
> [configuration.md](./configuration.md)；安全边界见 [security.md](./security.md)。

> ⚠️ **2026-07-08 分支合并的影响（务必先读）**：这里的 **`publish` 指的是「部署环境」**
> （systemd `harness-publish-*`、`/var/lib/harness-publish/`、端口 9090/9001/8083、nginx），
> 这套环境仍然在线、名字不变。但 **`publish` 作为分支和源码 checkout 已经没有了**——
> 2026-07-08 `publish` 分支成为 `main`,旧 `/home/agent-runtime-platform/publish/` 源码目录被清空。
> 部署源码现在统一在 **`/home/agent-runtime-platform/main/`**,下文所有 build/部署命令都已改到 `main/`。
>
> ✅ **已于 2026-07-08 完成 repoint + 重新部署**:两个 systemd unit 的
> `Documentation` / `WorkingDirectory` / `ExecStart` / `HARNESS_AUTH_USERS_PATH` 已从旧的
> `/home/agent-runtime-platform/publish/...` 改到 `main/`,`daemon-reload` 后从 `main/`
> 重新 build 并重启,现网(publish 环境)已在跑 `main/` 的版本。unit 备份留在
> `/etc/systemd/system/harness-publish-*.service.bak-2026-07-08`。`/var/lib/harness-publish/*`
> 运行时路径未改动;model proxy(`claude-proxy.service`,`/root/claude-code-proxy/`)不在仓库里,不受影响。
>
> ⚠️ **登录凭据文件在合并中丢失并已重建**:`config/auth-users.yaml`(gitignore,只存在于已删除的
> `publish/config/`)随目录清空一起没了。它**未提交**,所以 git 里恢复不了。已用 `cmd/authhash`
> 在 `main/config/auth-users.yaml` 重建 `lab` 用户(600 权限,root 所有)。**这一步不能省**:
> 该文件缺失时 `loadAuthUsers` 返回空、`AuthEnabled()=false`,`requireAuth` 会放行所有请求
>(见 `config.go:1342`、`server.go:3608`)——公网 IP 上的 workbench 会变成无登录裸奔。改密码/加用户走第 3 节。

---

## 1. 一句话总览

publish 是一套独立的 agent runtime 环境，和 `main/` 共用一台机器但端口/数据目录完全隔离：

| 组件 | 端口 | 跑在哪个用户 | 怎么管 |
| --- | --- | --- | --- |
| **nginx（TLS 终止反代）** | **443 / 9000（HTTPS）、80（跳转）** | `root` | **systemd** 服务 `nginx`，watt 可免密控制 |
| 前端 (Next.js) | **9001（仅 127.0.0.1）** | `watt` | **systemd** 服务 `harness-publish-frontend` |
| 后端 orchestrator | **9090** | `root` | **systemd** 服务 `harness-publish-orchestrator`，watt 可免密控制 |
| model proxy | 8083 | `root` | **systemd** 服务 `claude-proxy.service`（`/root/claude-code-proxy`），watt 可免密控制 |

> ⚠️ **对外只经 nginx 走 HTTPS**：前端已从 `0.0.0.0:9000` 收回到 `127.0.0.1:9001`（不再直接对外裸 HTTP）。
> 浏览器访问 `https://<ip>/`（443）或 `https://<ip>:9000/`（兼容旧路径），nginx 终止 TLS 后反代到前端 9001。
> HTTPS 部署细节见 [第 8 节](#8-https-自签证书--nginx-反代)。
>
> 端口对照（和 `main/` 区分）：main 后端用 8090、前端 8000、proxy 8082；
> publish 后端用 **9090**、前端 **9001（内部）**、proxy 8083。
> ⚠️ 两套后端的代码默认端口都是 8090，**不能在同一台机器同时用默认端口跑**，
> 所以 publish 显式改成了 9090。

数据与运行时根目录全部在 `/var/lib/harness-publish/` 下（DB、sessions、
agent-homes、checkpoints、runsc、run），与 main 的 `/var/lib/harness/` 隔离。

---

## 2. 日常运维命令（watt 直接用，无需 root 密码）

### 后端 orchestrator（systemd 管理）

```bash
# 状态 / 日志
sudo systemctl status harness-publish-orchestrator
journalctl -u harness-publish-orchestrator -n 100 --no-pager
journalctl -u harness-publish-orchestrator -f          # 实时跟踪

# 重启 / 启停（均免 root 密码）
sudo systemctl restart harness-publish-orchestrator
sudo systemctl start   harness-publish-orchestrator
sudo systemctl stop    harness-publish-orchestrator
```

watt 的免密权限**只限这两个 publish 服务**（orchestrator + frontend）的
start/stop/restart/status，其它 `sudo` 仍需密码（规则见 `/etc/sudoers.d/harness-publish`）。

### 重新部署后端（改了 Go 代码后）

```bash
cd /home/agent-runtime-platform/main/orchestrator
go build -o bin/orchestrator ./cmd/orchestrator     # 用 watt 编译即可，产物归 watt
sudo systemctl restart harness-publish-orchestrator # 加载新二进制
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:9090/healthz  # 期望 200
```

> systemd unit 里 `ExecStart` 指向 `orchestrator/bin/orchestrator`，所以
> **必须 build 到 `bin/` 目录**（不是 `go run`，也不是仓库根的旧 `orchestrator` 二进制）。

### 前端（systemd 管理）

前端已纳入 systemd，服务名 `harness-publish-frontend`，以 `watt` 用户运行，
端口 9000，已指向后端 9090。

```bash
# 状态 / 日志
sudo systemctl status harness-publish-frontend
journalctl -u harness-publish-frontend -n 100 --no-pager
journalctl -u harness-publish-frontend -f          # 实时跟踪

# 重启 / 启停（均免 root 密码）
sudo systemctl restart harness-publish-frontend
sudo systemctl start   harness-publish-frontend
sudo systemctl stop    harness-publish-frontend
```

### 重新部署前端（改了前端代码后）

```bash
cd /home/agent-runtime-platform/main/frontend
npm run build                                   # 用 watt 编译
sudo systemctl restart harness-publish-frontend # 加载新构建
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:9001/  # 期望 200（前端内部端口）
```

> unit 里 `ExecStart` 直接用 nvm 的 node 跑 `next start`（不依赖 npm/nvm 的 PATH 初始化），
> 并固定了 `PORT=9001`、`--hostname 127.0.0.1`、`HARNESS_API_BASE_URL=http://127.0.0.1:9090`。
> ⚠️ 前端**只绑 127.0.0.1:9001**（对外由 nginx 终止 TLS 后反代过来，见第 8 节），不要改回 `0.0.0.0`。
> 如果 node 版本路径变了（当前 `v24.15.0`）或后端端口变了，需改 unit 后
> `sudo systemctl daemon-reload && sudo systemctl restart harness-publish-frontend`。

### nginx（TLS 终止反代，systemd 管理）

nginx 监听 443/9000（HTTPS）和 80（301 跳 https），反代到前端 `127.0.0.1:9001`。

```bash
# 状态 / 日志
sudo systemctl status nginx
sudo journalctl -u nginx -n 100 --no-pager

# 改完配置后先测再重载（reload 不断连，比 restart 温和）
sudo nginx -t                    # 语法检查，OK 才继续
sudo systemctl reload nginx      # 平滑加载新配置
sudo systemctl restart nginx     # 换证书 / 改监听端口时用 restart
```

> 站点配置 `/etc/nginx/sites-available/harness-publish`（软链到 `sites-enabled/`），
> 证书在 `/etc/ssl/harness-publish/`。改证书或反代逻辑后务必先 `nginx -t` 再 reload。

### model proxy（`claude-code-proxy`，systemd 管理）

model proxy 把 sandbox 里的 agent（Pi）发出的 `anthropic-messages`
请求翻译成上游 OpenAI-compatible 调用（当前上游 `https://api.modelarts-maas.com/openai/v1`，
模型 `glm-5.1`）。**Pi 也必须走这个 proxy**：Pi 的 provider 被硬编码成
`anthropic-messages` + 指向 `harness-model-proxy.internal:8083` + 一个 dummy key
（见 `orchestrator/internal/runtime/runtime.go` 的 `buildPiDriverConfigProjection`）。
真正的上游 key 只存在 proxy 侧（`.env` 的 `OPENAI_API_KEY`），sandbox 永远拿不到——
这是 Phase 8 的安全边界，不要试图让 Pi 直连上游（egress 防火墙也只放行 proxy alias）。

```bash
# 状态 / 日志（watt 免密）
sudo systemctl status  claude-proxy.service
sudo journalctl -u claude-proxy.service -n 100 --no-pager
sudo journalctl -u claude-proxy.service -f          # 实时跟踪

# 重启 / 启停（均免 root 密码）
sudo systemctl restart claude-proxy.service
sudo systemctl start   claude-proxy.service
sudo systemctl stop    claude-proxy.service

# 改完 .env 后一键重启（脚本属主 watt）
/root/claude-code-proxy/restart-proxy.sh
```

- 配置在 `/root/claude-code-proxy/.env`（`load_dotenv()` 全量注入 `os.environ`）。
- 进程**必须以 root 运行**：correlation socket 目录
  `/var/lib/harness-publish/run/proxy-internal/` 是 `root:root 0750`，
  proxy 要连里面的 `proxy-correlation.sock`（`root:root 0660`）。
  以 watt 跑会 `Errno 13 Permission denied` → 每个 turn 返回 503。
- ⚠️ **publish 专属环境变量**（proxy 源码默认指向 `main` 的路径，必须在 `.env` 里覆盖）：

  ```dotenv
  HARNESS_RUN_DIR="/var/lib/harness-publish/run"
  HARNESS_PROXY_CORRELATION_SOCKET="/var/lib/harness-publish/run/proxy-internal/proxy-correlation.sock"
  HARNESS_SANDBOX_MODEL_PROXY_BASE_URL="http://harness-model-proxy.internal:8083"
  PORT="8083"
  ```

  少了这些，proxy 会去找 `/var/lib/harness/run`（main 的 run dir），
  correlation socket 找不到 → `Errno 2 No such file` → turn 返回 503、agent 无回复。

健康 / 联通判断（只有 `healthz` 200 不代表 correlation 正常）：

```bash
curl -s http://127.0.0.1:8083/healthz                 # 期望 {"status":"ok"}
# 发一条 workbench 消息后看 journal 里出现这三行才算贯通：
#   Harness proxy request started: ... turn=N
#   POST .../openai/v1/chat/completions "HTTP/1.1 200 OK"
#   Harness proxy request finished: status=200
```

---

## 3. 登录（workbench 登录闸）

- 用户配置在 `config/auth-users.yaml`，密码**只存 PBKDF2-SHA256 哈希，无明文、不可逆**。
- 当前用户：`lab`（保留了登录功能上线前创建的历史 session）。
- 改密码 / 加用户：

```bash
cd /home/agent-runtime-platform/main/orchestrator
echo -n '你的新密码' | go run ./cmd/authhash     # 输出一行 pbkdf2-sha256$... 哈希
# 把哈希粘进 config/auth-users.yaml 对应用户的 password_hash，然后：
sudo systemctl restart harness-publish-orchestrator
```

`auth-users.yaml` 格式（用户名只允许字母/数字/`_`/`-`，1-64 字符）：

```yaml
users:
  - username: lab
    password_hash: "pbkdf2-sha256$600000$....$...."
```

> 验证登录（经 nginx 走 HTTPS，`-k` 跳过自签证书校验）：
> `curl -k -X POST https://127.0.0.1/api/login -H 'Content-Type: application/json' -d '{"username":"lab","password":"..."}'`
> 正确→200，错误→401。
> ⚠️ `config/auth-users.yaml` 是 **gitignore、不在仓库里** 的运行时文件(见 `.gitignore`)。
> 它在 2026-07-08 合并中随旧 `publish/config/` 一起丢失、已重建(文首 ⚠️ 块)。
> 因为不进 git,搬机器/重建 checkout 时**不会自动带上**——务必单独备份,否则重启即掉登录闸。
> Cookie 是 Secure 的（按 HTTPS 访问设计）。现在对外全程走 nginx 的 HTTPS，nginx 会转发
> `X-Forwarded-Proto https`，所以 Secure cookie 能正常下发，**无需** `HARNESS_COOKIE_INSECURE`。
> 只有在绕过 nginx 直连前端纯 HTTP 调试时才会遇到 cookie 不下发，那种情况才需要该开关。

---

## 4. 为什么后端是 root、watt 却还能运维？

orchestrator 进程自身要做这些**特权操作**（源码里 `geteuid != 0` 时直接报错）：

- `ip netns add` / `ip link add ... type veth` / `ip netns exec ...` → 需要 `CAP_NET_ADMIN`/`CAP_SYS_ADMIN`
- 调 `runsc` 启动 gVisor 沙箱
- 把每个沙箱文件 `chown` 成 uid/gid **65534**（沙箱身份）→ 需要 `CAP_CHOWN`

所以**进程必须是 root**——光把文件 chown 给 watt 没用，第一个新 session 会卡在
`ip netns add`。解决办法是：**进程用 root 跑（systemd），watt 通过一条限定范围的
sudoers 规则免密控制这个服务**。这样数据不丢、新 session 正常、旧 session 也能 resume。

> 不要对 `/var/lib/harness-publish` 做 `chown -R watt`：那会破坏 65534 沙箱身份分层
> （沙箱进程以 65534 运行，读不到被改属主的文件），且解决不了上面的特权问题。

---

## 5. 关键路径与文件

| 用途 | 路径 |
| --- | --- |
| 代码仓库根 | `/home/agent-runtime-platform/main/`（2026-07-08 合并前是 `.../publish/`） |
| 后端源码（go.mod 在此） | `orchestrator/` |
| 后端二进制（ExecStart 指向） | `orchestrator/bin/orchestrator` |
| 主配置 | `config/harness.yaml` |
| 登录用户 | `config/auth-users.yaml` |
| 前端 | `frontend/`（`next start`，监听 `127.0.0.1:9001`，systemd 管理） |
| 后端 systemd unit | `/etc/systemd/system/harness-publish-orchestrator.service` |
| 前端 systemd unit | `/etc/systemd/system/harness-publish-frontend.service` |
| nginx 站点配置 | `/etc/nginx/sites-available/harness-publish`（软链到 `sites-enabled/`） |
| 自签 TLS 证书 / 私钥 | `/etc/ssl/harness-publish/fullchain.pem`、`privkey.pem`（有效期至 2036-07） |
| model proxy 项目 / 配置 | `/root/claude-code-proxy/`（`.env`、`start_proxy.py`、`restart-proxy.sh`） |
| model proxy systemd unit | `/etc/systemd/system/claude-proxy.service` |
| model proxy sudoers 规则 | `/etc/sudoers.d/claude-proxy`（授权 watt 免密管理 `claude-proxy.service`） |
| 免密 sudoers 规则 | `/etc/sudoers.d/harness-publish`（0440，覆盖前后端两个服务） |
| 运行时数据根 | `/var/lib/harness-publish/`（root 所有） |
| ├ DB | `state/orchestrator.db` |
| ├ sessions / agent-homes / checkpoints / runsc | 同名子目录 |
| ├ run（pid、socket、bridge 等） | `run/` |
| └ proxy correlation socket | `run/proxy-internal/proxy-correlation.sock`（`root:root 0660`，proxy 必须以 root 连） |

systemd unit 里通过环境变量把上述根目录指到 `harness-publish`（默认是 `harness`），
端口设为 `:9090`。改这些就编辑 unit 后 `sudo systemctl daemon-reload && sudo systemctl restart harness-publish-orchestrator`。

---

## 6. 排障速查

| 现象 | 排查 |
| --- | --- |
| 后端起不来 | `journalctl -u harness-publish-orchestrator -n 50`；常见：9090 被占、DB 锁、config 报错 |
| `bind: address already in use` | `ss -ltnp \| grep 9090` 看谁占了；确认没有第二个 orchestrator（含 main 的） |
| 前端能开但接口 500/502 | 多半是后端没跑或前端 `HARNESS_API_BASE_URL` 指错端口（应为 9090） |
| 浏览器打不开 / 502 Bad Gateway | nginx 在跑但前端挂了：`sudo nginx -t` 看配置；`curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:9001/` 看前端本身；`journalctl -u nginx -n 30` |
| 浏览器提示证书不安全 | 自签证书的正常表现（客户端不认自签 CA），点“继续前往”即可。要消除需换可信 CA 证书（见第 8 节末） |
| 改了 nginx 配置不生效 | 忘了 reload；`sudo nginx -t && sudo systemctl reload nginx` |
| 登录总是 401 | 哈希没更新 / 没重启后端 / 密码错；用第 3 节的 curl（经 HTTPS）直接测 |
| 登录成功但没保持登录态 | cookie 没下发：确认是经 nginx 的 HTTPS 访问（nginx 转发 `X-Forwarded-Proto https`），不是绕过 nginx 的裸 HTTP |
| 新 session 卡住、网络/沙箱报错 | 确认后端是 **root**（`ps -o user= -p $(systemctl show -p MainPID --value harness-publish-orchestrator)`），`runsc`/`ip` 在 PATH |
| 消息能发出、无 toast、但 agent 不回复 | model proxy 没贯通。`sudo journalctl -u claude-proxy.service -n 50` 看：`Errno 13`=proxy 没以 root 跑；`Errno 2 No such file`=`.env` 缺 publish 的 `HARNESS_RUN_DIR`/`HARNESS_PROXY_CORRELATION_SOCKET`（指到了 main 的 `/var/lib/harness/run`）；`401`=client key 不匹配 `.env` 的 `ANTHROPIC_API_KEY`。修好后 `sudo systemctl restart claude-proxy.service` |
| pre-start network probe `10.201.0.1:8083` connect 失败 (curl exit 7) | model proxy 没在监听。`ss -ltnp \| grep 8083`；没有就 `sudo systemctl start claude-proxy.service`。注意端口是 **8083**（main 用 8082）、且必须 bind `0.0.0.0` |
| 改了端口/路径不生效 | 改的是 unit 还是 shell？unit 改完要 `daemon-reload` 再 restart |

### 健康检查一键脚本

```bash
echo "https :443     -> $(curl -sk -o /dev/null -w '%{http_code}' https://127.0.0.1/)"
echo "https :9000    -> $(curl -sk -o /dev/null -w '%{http_code}' https://127.0.0.1:9000/)"
echo "http  :80(跳转)-> $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1/)"   # 期望 301
echo "backend :9090  -> $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:9090/healthz)"
echo "frontend :9001 -> $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:9001/)"
echo "proxy :8083    -> $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8083/healthz)"
systemctl is-active nginx harness-publish-orchestrator harness-publish-frontend claude-proxy.service
ps -o user= -p "$(systemctl show -p MainPID --value harness-publish-orchestrator)"  # 应为 root
ps -o user= -p "$(systemctl show -p MainPID --value harness-publish-frontend)"      # 应为 watt
ps -o user= -p "$(systemctl show -p MainPID --value claude-proxy.service)"          # 应为 root
```

---

## 7. 回滚 / 拆除这套 systemd 部署

```bash
# 关掉 HTTPS 反代（可选：若想回到裸 HTTP 直连前端）
sudo systemctl disable --now nginx
sudo rm /etc/nginx/sites-enabled/harness-publish
# 若要让前端重新直接对外：把 unit 里 PORT 改回 9000、--hostname 改回 0.0.0.0，再 daemon-reload + restart

sudo systemctl disable --now harness-publish-frontend
sudo systemctl disable --now harness-publish-orchestrator
sudo rm /etc/systemd/system/harness-publish-frontend.service
sudo rm /etc/systemd/system/harness-publish-orchestrator.service
sudo rm /etc/sudoers.d/harness-publish
sudo systemctl daemon-reload
```

之后回到老办法（前台手动跑），需自己用 root 起后端进程并设好 `HARNESS_*` 环境变量，
前端用 nvm 的 node 跑 `next start`（带 `PORT=9001`、`--hostname 127.0.0.1`、`HARNESS_API_BASE_URL=http://127.0.0.1:9090`）。

---

## 8. HTTPS（自签证书 + nginx 反代）

对外访问全程走 HTTPS。做法是 **nginx 终止 TLS**（用自签证书），反代到只绑本机的前端
`127.0.0.1:9001`。这样浏览器全程 HTTPS，登录 cookie 的 `Secure` 属性也能正常下发。
**没有改动任何前后端代码**——只加了一层 nginx 并把前端端口收回内网。

### 访问地址

| 入口 | 说明 |
| --- | --- |
| `https://172.18.1.141/` | 内网 IP，标准 443 |
| `https://123.249.65.231/` | 外网 IP，标准 443 |
| `https://123.249.65.231:9000/` | 兼容旧的 `:9000` 访问路径（nginx 在 9000 上也终止 TLS） |
| `http://<任意上述>/` | 自动 `301` 跳转到对应的 https |

> ⚠️ 自签证书浏览器会提示“不安全”，点**继续前往**即可——这是自签证书的正常表现，不是故障。

### 当时怎么装的（复现 / 迁移到新机时照做）

**1) 生成自签证书**（SAN 覆盖所有访问地址，有效期 10 年）：

```bash
sudo install -d -m 0755 /etc/ssl/harness-publish
sudo openssl req -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes \
  -keyout /etc/ssl/harness-publish/privkey.pem \
  -out    /etc/ssl/harness-publish/fullchain.pem \
  -subj "/CN=harness-publish" \
  -addext "subjectAltName=IP:172.18.1.141,IP:123.249.65.231,IP:127.0.0.1,DNS:localhost" \
  -addext "keyUsage=digitalSignature,keyEncipherment" \
  -addext "extendedKeyUsage=serverAuth"
sudo chmod 0644 /etc/ssl/harness-publish/fullchain.pem
sudo chmod 0600 /etc/ssl/harness-publish/privkey.pem
# 校验 SAN 与有效期：
sudo openssl x509 -in /etc/ssl/harness-publish/fullchain.pem -noout -text | grep -A1 "Subject Alternative Name"
```

> ⚠️ **SAN 必须列全所有会用来访问的 IP/域名**，否则该地址在浏览器里会额外报 SAN 不匹配。
> 换机器或加访问地址时，改上面的 `subjectAltName=` 重新生成即可（当前证书的 SAN：
> `172.18.1.141`、`123.249.65.231`、`127.0.0.1`、`localhost`）。

**2) 安装 nginx**：

```bash
sudo apt-get update
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y nginx
# 若报 “dpkg was interrupted”，先修复再装：
#   sudo dpkg --configure -a
nginx -v
```

**3) 前端收回内部端口**（改 `harness-publish-frontend.service`）：把 `PORT` 从 `9000` 改成 `9001`，
`--hostname 0.0.0.0` 改成 `--hostname 127.0.0.1`，然后
`sudo systemctl daemon-reload && sudo systemctl restart harness-publish-frontend`。

**4) 写 nginx 站点配置** `/etc/nginx/sites-available/harness-publish`：443 与 9000 都终止 TLS
反代到前端，80 跳 https，`/api` 关闭 buffering 以支持 agent 的 SSE 流式输出：

```nginx
map $http_upgrade $connection_upgrade { default upgrade; '' close; }

# 80 -> 443 跳转
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;
    return 301 https://$host$request_uri;
}

# HTTPS：443 与 9000 共用同一套反代逻辑
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    listen 9000 ssl;
    listen [::]:9000 ssl;
    server_name _;

    ssl_certificate     /etc/ssl/harness-publish/fullchain.pem;
    ssl_certificate_key /etc/ssl/harness-publish/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_ciphers         HIGH:!aNULL:!MD5;

    client_max_body_size 50m;   # 大 prompt / 上传不被 413 卡住

    location / {
        proxy_pass http://127.0.0.1:9001;
        proxy_http_version 1.1;

        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;   # 让后端认为是 HTTPS -> Secure cookie 生效
        proxy_set_header X-Forwarded-Host  $host;

        proxy_set_header Upgrade    $http_upgrade;  # WebSocket / SSE 升级
        proxy_set_header Connection $connection_upgrade;

        proxy_buffering     off;    # SSE 流式输出不缓冲，否则 agent 回复会一次性堆到最后
        proxy_cache         off;
        proxy_read_timeout  3600s;  # 长连接不被 nginx 提前掐断
        proxy_send_timeout  3600s;
    }
}
```

**5) 启用并加载**：

```bash
sudo ln -sf /etc/nginx/sites-available/harness-publish /etc/nginx/sites-enabled/harness-publish
sudo rm -f /etc/nginx/sites-enabled/default   # 默认站点占 80 default_server，会冲突
sudo nginx -t                                  # 语法检查
sudo systemctl enable --now nginx
sudo systemctl reload nginx
```

**6) 验证**：

```bash
curl -sk -o /dev/null -w '%{http_code}\n' https://172.18.1.141/          # 期望 200
curl -sk -o /dev/null -w '%{http_code}\n' https://172.18.1.141:9000/     # 期望 200
curl -s  -o /dev/null -w '%{http_code} -> %{redirect_url}\n' http://172.18.1.141/  # 期望 301 -> https
# 看握手拿到的证书 SAN：
echo | openssl s_client -connect 127.0.0.1:443 2>/dev/null | openssl x509 -noout -ext subjectAltName
```

### 换证书 / 到期续期

自签证书到期（2036-07）或想换成可信 CA 证书时：把新的 `fullchain.pem` / `privkey.pem`
覆盖到 `/etc/ssl/harness-publish/`（或改配置里 `ssl_certificate*` 指向新路径），
然后 `sudo nginx -t && sudo systemctl reload nginx`。

> 🔒 **安全说明**：自签证书只加密传输，**不能防中间人**（客户端不校验证书真伪）。
> 内网自用足够；若 `123.249.65.231` 长期公网对外，建议换成可信 CA 证书
> （有域名的话用 Let's Encrypt / `certbot` 最省事，还能自动续期）。
