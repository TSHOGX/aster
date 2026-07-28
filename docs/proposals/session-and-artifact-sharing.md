# Proposal: 会话与 artifact 分享

**Status:** Draft
**Date:** 2026-07-28（按 codebase 复核修订：分期顺序、可见性范围、快照数据模型、`user rm` 撤销强度）
**Scope:** 让 session 的对话记录与 artifact 能被 owner 之外的人查看，两个渠道——分享给指定用户、生成公开链接。第一版只读。

## Problem

登录网关落地后每个账号的数据是硬隔离的：`/api/sessions` 只列自己的会话，任何
`/api/sessions/{id}/*` 对非 owner 一律 404，artifact 下载同样按 owner 校验。隔离本身是对的，
但由此丢掉两个真实需求：同事之间无法互看分析过程（「我这次是怎么算出来的」只能截图）；
agent 产出的 HTML 图表、Excel、PDF 无法作为链接分发。

## 现状：所有权检查的收口点

| 位置 | 作用 |
|---|---|
| `server/server.go` `authorizeSession()` | 所有 `/api/sessions/{id}/*` 的闸门；owner 不匹配写 404，与「不存在」不可区分，避免探测 |
| `server/server.go` `openArtifactFile()` | artifact 读的独立校验，随后经 `VerifySessionWorkspaceVolume` 读**实时** workspace |
| `server/stream_http.go` `sessionVisibleTo()` | SSE/WS 事件流与 replay，带 30s owner 缓存 |

列表面另有两处按 `user_id` 硬过滤：`ListSessionsByUser()` 与 `store/search.go`。

两个约束决定了下面的设计：

- **`authorizeSession` 同时守读和写**——`sendMessage`、`interruptSession`、`renameSession`、
  `destroySession`、`uploadArtifacts`、artifact `PUT` 都走它。直接放宽它等于把写权限一并送出去。
- **`openArtifactFile` 被读写共用**——`getArtifact`、`workspace_upload.go` 的下载、以及
  `artifactIsStale`（PUT 的乐观并发检查）。PUT 今天的安全性靠「先 `authorizeSession` 再调它」的
  **调用顺序**兜住，不是靠类型。就地放宽它内部的 owner 比较会连带放宽写路径。

## 设计

### 1. 权限模型：owner 闸门与 read 闸门分离

`authorizeSession()` 语义不变（owner-only），新增 read 闸门，只在四条明确的读路由上替换：
`getSession`、`listMessages`、`listArtifacts`、artifact `GET`。

方向是刻意的：任何漏改的路由**默认仍是 owner-only**，fail closed。反过来（放宽公共闸门再逐个收紧
写路由）一旦漏一处就是越权写。

```go
type accessLevel int // none | reader | owner

// 返回 session、调用者的 access level、以及生效的 visibility（owner 恒为 full）。
func (s *Server) resolveAccess(ctx context.Context, sessionID, viewerID string) (store.Session, accessLevel, string, error)
```

`owner` 来自 `sessions.user_id`，命中即短路——**owner 自己的读不多一次 share 查询**（writer 池只有
一条连接，见 §4）。`reader` 来自 `session_shares`。`none` 写 404。

`openArtifactFile` 的签名改为显式接收所需的 access level，而不是在内部放宽 owner 比较：

```go
func (s *Server) openArtifactFile(ctx context.Context, sessionID, artifactPath string, granted accessLevel) (...)
```

读路径传 resolver 的结果，`writeArtifact` / `artifactIsStale` / 下载传 `accessOwner`。这样漏传是编译
错误，而不是靠调用顺序的隐式约定。

### 2. 数据模型

三张新表进 control.db，跟随 `store/schema.go` 既有的 `CREATE TABLE IF NOT EXISTS` + 幂等
`ALTER TABLE` 风格。

```sql
CREATE TABLE IF NOT EXISTS session_shares (
  id               TEXT PRIMARY KEY,
  session_id       TEXT NOT NULL,
  grantee_user_id  TEXT NOT NULL,
  role             TEXT NOT NULL CHECK(role IN ('reader')),
  visibility       TEXT NOT NULL CHECK(visibility IN ('summary','full')),
  created_by       TEXT NOT NULL,
  created_at       TEXT NOT NULL,
  revoked_at       TEXT,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

-- 唯一性只约束「生效中」的授权，否则撤销后再分享同一个人会撞唯一键。
CREATE UNIQUE INDEX IF NOT EXISTS session_shares_live_idx
  ON session_shares (session_id, grantee_user_id) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS session_shares_grantee_idx ON session_shares (grantee_user_id);

CREATE TABLE IF NOT EXISTS share_links (
  id               TEXT PRIMARY KEY,
  token_hash       TEXT NOT NULL UNIQUE,
  session_id       TEXT NOT NULL,
  visibility       TEXT NOT NULL CHECK(visibility IN ('summary','full')),
  max_message_id   INTEGER,            -- 消息可见范围的上界，见 §5
  created_by       TEXT NOT NULL,
  created_at       TEXT NOT NULL,
  expires_at       TEXT NOT NULL,      -- 匿名链接不允许永不过期，见 §4
  revoked_at       TEXT,
  last_accessed_at TEXT,               -- 惰性写，见 §4
  access_count     INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS share_links_session_idx ON share_links (session_id);

-- 一个公开链接暴露的文件白名单，每行冻结在创建时的那一份 blob。空集合 = 只分享会话文本。
CREATE TABLE IF NOT EXISTS share_link_files (
  link_id      TEXT NOT NULL,
  path         TEXT NOT NULL,          -- session workspace 内的相对路径
  blob_sha256  TEXT NOT NULL,          -- 内容寻址的快照，见 §6
  size         INTEGER NOT NULL,
  PRIMARY KEY(link_id, path),
  FOREIGN KEY(link_id) REFERENCES share_links(id) ON DELETE CASCADE
);
```

FK 都在 control.db 内部，`memory.db` 那 5 条被丢掉的跨库 FK 与此无关。但注意
`sessions(id) ON DELETE CASCADE` 在本仓库**从不触发**——session 销毁是软删（状态翻成
`destroyed`）。所以它是「将来真加硬删时的兜底」，不是本方案依赖的回收机制；blob 的回收见 §6。

token 用 32 字节随机、只存 SHA-256，复用 `newAuthToken()` / `hashAuthToken()`
（`server/auth_http.go`）。原始链接只在创建响应里返回一次，之后不可回显；丢了就 rotate（覆盖同一行
的 `token_hash`，旧链接立即失效）。

**新增 store 方法归入一个新角色接口**（`server/store_iface.go`），并入 `serverStore`——这跟随
`refactor(store): introduce consumer-side interfaces and freeze layering` 立下的规则，不能挂在一个
god interface 上：

```go
// shareStore covers session_shares / share_links / share_link_files: grants,
// public link tokens, the owner-facing listings that manage them, and the
// revocation sweep `user rm` depends on.
type shareStore interface {
	CreateSessionShare(context.Context, store.SessionShare) error
	ListSessionShares(context.Context, string) ([]store.SessionShare, error)
	ListSessionsSharedWith(context.Context, string) ([]store.SharedSession, error)
	ResolveSessionShare(context.Context, string, string) (store.SessionShare, bool, error)
	RevokeSessionShare(context.Context, store.RevokeShareParams) error // 按 (sessionID, shareID) 撤销
	RevokeSharesForUser(context.Context, string, time.Time) (int64, error)
	CreateShareLink(context.Context, store.ShareLink, []store.ShareLinkFile) error
	LookupShareLink(context.Context, string, time.Time) (store.ShareLink, bool, error)
	ListShareLinkFiles(context.Context, string) ([]store.ShareLinkFile, error)
	ListShareLinks(context.Context, string) ([]store.ShareLink, error)
	RotateShareLinkToken(context.Context, string, string, time.Time) error
	RevokeShareLink(context.Context, store.RevokeShareParams) error
	TouchShareLinkAccess(context.Context, []store.ShareLinkAccess) error   // 批量，见 §4
	SweepExpiredShareLinks(context.Context, time.Time) ([]string, error)   // 返回失去引用的 blob_sha256
}
```

`RevokeSessionShare` / `RevokeShareLink` 都按 `(sessionID, id)` 撤销，不只按 id——路由层的 owner
闸门之外，store 层也不接受跨 session 的 IDOR。

入参校验（visibility 枚举、grantee 存在性与非自我分享、artifact 路径、TTL 上限）放
`server/share_rules.go`，紧邻 `session_rules.go`，返回 400；store 只保留持久化不变量。这是跟随同一个
commit 把 `sessionstate.Validate` / `agents.Lookup` 上移到 server 时确立的分工：**坏输入是 400，不是
500**。`internal/architecture/layering_test.go` 会在依赖方向回退时直接失败。

### 3. 渠道一：分享给指定用户

接收方是已登录用户，复用现有 authed 路由 + resolver，不需要新的读路径。

- **Owner 侧 API**：`POST /api/sessions/{id}/shares`（body: `grantee`, `visibility`）、
  `GET /api/sessions/{id}/shares`、`DELETE /api/sessions/{id}/shares/{shareID}`，三个都走 owner 闸门。
- **接收方侧**：`ListSessionsByUser()` 之外新增 `ListSessionsSharedWith()`，`listSessions` handler 合并
  两者并标注 `shared_by` / `access`；侧边栏据此分出「共享给我」分组，行内无重命名/销毁入口。
  （不改 `ListSessionsByUser` 的语义——它已在 `sessionStore` 接口里，动它会波及 quota/idle 的调用点。）
- **接收方看到的 DTO 同样要裁**：现有 `publicSession()` 带 `user_id` / `failure_reason` /
  `error_class`。这几项对非 owner 一律剥掉，与公开链接同一套裁剪逻辑，不因为「组织内」就放松。
- **用户选择器**：`GET /api/users`，数据来自 `authRegistry` 新增的 `usernames()`（持 `RLock` 读快照 +
  `refresh(false)` 节流 stat）。**不读 `cfg.AuthUsers`**——`feat(auth): manage login users without
  restarting the orchestrator` 之后那份快照已过期，凭据真相在 registry。该端点把全量用户名暴露给任何
  登录用户，内部工具可接受，记录在此。
- **收件通知**：`notifications` 目前是**全员广播**语义（`ListNotificationsForUser` 与
  `CountUnreadNotifications` 都无条件覆盖所有行，只 LEFT JOIN 已读状态）。需要加 nullable `user_id`：
  `NULL` 保持广播，非空为定向；**两个查询都要加谓词**，漏一个会让未读数与列表不一致。

### 4. 渠道二：公开链接

挂在 `requireAuth` **外面**的独立未鉴权子树。Go 1.22 的 ServeMux 按最具体模式优先，所以
`mux.Handle("/api/shared/", ...)` 与既有的 `mux.Handle("/api/", s.requireAuth(...))` 可以共存；这条依赖
要有测试钉住，避免将来改路由时静默掉进 authed 分支（或反过来）。

```
GET /api/shared/{token}              -- 精简 session DTO
GET /api/shared/{token}/messages
GET /api/shared/{token}/artifacts    -- 白名单，不是 workspace 全集
GET /shared/{token}/files/{path}     -- 文件字节（快照），见 §6
```

**为什么不做成「token 换 cookie 再走正常路由」**：隔离。authed 路由永远不认识匿名主体，漏一个检查也
不会变成越权；GET-only 无 CSRF 面；不依赖 cookie，隐身窗口和链接预览抓取都能工作。代价是读 handler
有一份副本，但公开 DTO 本来就该裁掉 `user_id` / `failure_reason` / `error_class`——这份「重复」是收益。

**匿名读必须离开 writer 池。** `perf(store): split writer/reader SQLite connection pools` 之后，
writer 是**单连接**，而 reader 池「基础设施 only，没有任何查询迁过去」。公开链接是唯一匿名、可被高频
拉取的表面，把它的读压在那条连接上会和所有 session/turn 写入排队。所以：

- `access_count` / `last_accessed_at` **不写在请求路径上**：handler 只累加进程内的 map，由
  `RunMaintenance` 的周期循环批量 `TouchShareLinkAccess` 落盘。重启丢一个窗口的计数——审计用途够了，
  不值得拿写锁换精确。
- share 的**读查询直接写在 `readDB` 上**。这不违反那个 commit 的「行为可证不变」——它们是新增查询，
  没有需要保持不变的旧行为。token→link 的解析再加一层进程内 TTL 缓存。

滥用面控制：

- 全局 IP 限流已覆盖整个 mux（`s.rateLimit`，10 rps / burst 40），公开子树自动继承。注意反面风险——
  一个被转发到群里的链接会让整个 NAT 出口共用同一个桶，把该 IP 上的**已登录**用户一起限住。若实测有
  影响，给匿名子树单独一个 limiter 实例。
- `expires_at` NOT NULL、创建时必填且有上限（默认 30 天）：匿名链接不允许永不过期，这是成本最低的兜底。
- 到期与撤销都返回 404，不区分「过期」与「不存在」。

**v1 不开实时流。** 匿名请求不进 SSE/WS：不让匿名连接占用事件流资源；且 `sessionVisibleTo` 的 owner
缓存是按 session 缓存**单个** owner 值的，要支持 viewer-aware 得先改造它。身份分享的接收方同样先不给
实时跟看——`eventVisibleTo` 会滤掉这些事件，接收方看到的是**刷新时的快照**，前端需要给一个显式的
刷新入口而不是让页面看起来卡住。

### 5. 会泄漏什么：trace 与 artifact 两层

`Message.Trace` 里是 shell 命令、文件内容、SQL 语句、Doris 连接细节。但更粗的一条管子是 artifact：
`ListArtifacts(sessionID)` 列的是 watcher 全量扫出来的**整个 workspace**——agent 中途落盘的原始表、
导出的中间文件、随手写的配置。只裁 trace 不管文件，等于用一道窄门配一面敞开的墙。所以两层都要管：

- **消息**：`summary` 只留用户消息 + turn 终态的 assistant 回复并剥掉 `trace`；`full` 含执行轨迹。
- **artifact**：与 visibility 正交。公开链接只暴露创建时显式勾选的白名单（`share_link_files`），
  身份分享给全集。

**「turn 终态」不能写成 `stop_reason == "stop"`。** `persistShellOutput` 走 `AddMessage`，落库的
`stop_reason` 是空串；pre-trace 的历史消息同样是空串。按 `== "stop"` 过滤，一个 shell 会话的 summary
分享会只剩用户输入、输出全被删掉。规则是：**排除 `stop_reason == "toolUse"` 的中间步骤**，保留其余
assistant 行。且 shell 模式下 summary 没有语义（「输出」本身就是命令与执行结果），**shell 会话的公开
链接强制 `full`，创建时若传 `summary` 直接 400**。

公开链接默认 `summary`，要开 trace 得显式勾选并伴随提示；组织内身份分享默认 `full`。裁剪在 DTO 层做
（新增 `sharedMessage()` / `sharedSession()`），不依赖前端不渲染。

公开链接创建时记下 `max_message_id`：链接是**当时那一段对话**的快照，之后的 turn 不会自动出现在已经
发出去的链接里。否则「分享一次」在语义上等于「把这个会话的未来也公开了」。

### 6. 快照存储

`openArtifactFile` 读实时 workspace，直接拿来做公开链接有两个问题：agent 后续覆盖文件会让链接内容悄悄
变化；会话销毁或 retention 清理后链接 404。所以公开链接在创建时对白名单里的每个文件**快照**：字节复制
进独立的内容寻址 blob store（sha256 → 路径），`share_link_files.blob_sha256` 指向它。

三点收益：链接内容不变；会话销毁后仍可访问；匿名路径完全不碰活 workspace，
`VerifySessionWorkspaceVolume` 那条 evidence 校验链不参与。

实现约束：

- blob store 是**新的 leaf 包**（如 `internal/shareblob`），只做「写字节 / 按 sha256 读 / unlink」，
  不 import store 或 server；同步在 `layering_test.go` 的规则表里加一条。
- 复制发生在创建链接的 POST handler 内，需要**单链接总字节上限**（配置项，默认取 `MaxEditBytes`
  同量级的保守值）。没有上限时一次「整会话勾全选」可以是几个 G。
- **OBLIGATION（按 `memorystore/schema.go` 的先例，这段注释要写进 `store/schema.go`）**：blob 字节在
  SQLite 之外，`ON DELETE CASCADE` 只删表行、删不掉磁盘文件；且本仓库从不硬删 sessions，那条 CASCADE
  实际从未触发。回收只能走显式路径——`SweepExpiredShareLinks` 返回失去引用的 `blob_sha256`，
  `RunMaintenance` 负责 unlink。引用计数与 unlink 必须在**同一个写事务内**判定，否则一个并发的
  `CreateShareLink` 命中同一 sha256 时，GC 会删掉新链接正在引用的文件。

**身份分享保持实时**（组织内、已鉴权，看到最新状态更符合预期），走 `openArtifactFile` 而非快照。

### 7. 前端与响应头

- **新增 `/s/{token}` 页面。** 不能直接「复用 `conversation.tsx` / `artifact-viewer.tsx` 去掉 composer」：
  `conversation.tsx` 一次性从 `harness-provider` 取 `useConversation` / `useActivity` / `useHarness` /
  `useSelectedSession`，而 provider 绑着 auth、SSE、sessions 全套，匿名页跑起来第一件事就是 401 弹登录。
  要么把这两个组件的数据入口改成 props 注入，要么为匿名页实现第二套 provider。**这是一块独立的重构，
  不是删一个 composer**，排期要按此计。
- **新增 `app/shared/[token]/files/[...path]/route.ts` 代理。** 现有 route handler 只覆盖
  `/api/[...path]` 与 `/artifacts/[sessionId]/[...path]`，新的 `/shared/...` 顶层路径没人转发。
- **`next.config.mjs` 的 header 例外要放开 `/shared/`。** 当前规则是
  `source: "/((?!artifacts/).*)"`，只给 `/artifacts/` 开了口。新路径会被套上 `X-Frame-Options: DENY` +
  `frame-ancestors 'none'`，与 orchestrator 给 HTML artifact 发的 `SAMEORIGIN` 撞车——公开页里的
  HTML/PDF 预览 iframe 会白屏，正是 `next.config.mjs` 注释里记着的那个「refused to connect」bug。
- **公开子树的响应加 `Referrer-Policy: no-referrer`。** token 在 URL 路径里，artifact 内的外链会把它带
  出去。应用侧现在是 `strict-origin-when-cross-origin`，对 token 不够。
- **orchestrator 侧 CSP 不用改**：HTML artifact 已经是 `connect-src 'none'` / `form-action 'none'` /
  `frame-ancestors 'self'`（`getArtifact`），这层「能渲染但不能外发」的策略在公开暴露后正好是必需的。

### 8. 用户名复用：`user rm` 必须撤销 share

`feat(auth)` 让用户可以在运行时增删，而**身份没有稳定 ID**：`session_shares.grantee_user_id` 存的是
用户名字符串（`sessions.user_id` 同理，且无 FK——`users` 表只有内建 lab 一行）。于是把 `alice` 删掉、
之后又建一个同名 `alice`，新的这个人会继承旧 alice 的全部 share。

`user rm` 已经会撤销该用户的 cookie（`revokeSessions` → `RevokeAuthSessionsForUser`）。同一处**必须**
一并撤销 `session_shares` 里 `grantee_user_id` / `created_by` 命中该用户的行，以及
`share_links.created_by` 命中的行（否则同名新用户继承公开链接的管理权）。

两条边界：

- **只有 `rm` 撤销 share，`passwd` 不撤销。** 改密码是同一个人换凭据，撤销他的 share 是破坏数据。
- **撤销失败必须非零退出。** `revokeSessions` 目前是 best-effort、只打 warning：凭据文件已经改完，DB
  打不开就提示一句了事。cookie 漏撤销最终会过期，**share 不会过期**，一次静默失败就是永久越权。同理
  `OpenForMaintenance` 跳过 `migrate()`，遇到 share 表不存在不能当成「没有 share 要撤」，要报错。

更彻底的做法是引入不可复用的 user id，但那要动 `sessions.user_id` 的全部历史数据。注意即使不引入，
**同名新用户继承旧用户的全部会话**这个洞本来就存在（`ListSessionsByUser` 按用户名查），share 撤销只闭合
share 这一路，不闭合那一路——那是独立命题。

## 分期

1. **权限地基 + 身份分享**：resolver + owner/read 闸门分离（含 `openArtifactFile` 签名改造）+ 三张表 +
   `shareStore` 角色接口 + `share_rules.go` 校验 + owner 侧 share API + 非 owner DTO 裁剪 +
   §8 的 `user rm` 撤销。
2. **公开链接（只含会话文本）**：`/api/shared/*` 子树 + `/s/{token}` 页面（含前端 provider 解耦）+
   `max_message_id` + visibility 裁剪 + 到期/撤销/rotate + 访问计数惰性 flush + 读走 `readDB`。
   **刻意不含文件字节**——快照在第 3 期，此时给匿名请求发文件只能读活 workspace，与 §6 直接冲突。
3. **快照存储 + artifact 公开链接**：内容寻址 blob store + `share_link_files` 白名单 +
   引用计数 GC（含 schema 注释里的 OBLIGATION）+ `/shared/{token}/files/*` + 前端代理与 header 例外。
4. **`notifications.user_id`** 定向通知（列表与未读数两个查询都改）+ 侧边栏「共享给我」。

每期都要通过 `internal/architecture/layering_test.go`，并同步更新 `docs/api.md`（路由树 + DTO）与
`docs/security.md`（第 131 行那段「owner 不匹配返回 404」的表述在 read 闸门落地后不再完整）。

测试上至少要钉住：非 owner 无 share 读 → 404；有 share 读 → 200；有 share 的**写**路由 → 404；
`/api/shared/*` 不经 `requireAuth` 而 `/api/*` 经过（ServeMux 优先级）；`summary` 下 shell 会话仍有输出。

## Not planned (v1)

- **共享会话进 search**。`store/search.go` 按 user_id 过滤，v1 不扩展——接收方只能从侧边栏进入。
- **协作**（接收方能发消息）。牵扯 turn grant、quota 归属、bridge 与 owner 的数据卷，是独立命题。
- **公开链接与身份分享的实时流**，见 §4。
- **组/部门维度分享**。当前没有 group 概念，`auth-users.yaml` 是平铺用户表。
- **稳定 user id**，见 §8。
- **把既有读查询迁到 reader 池**。share 的新查询直接用 `readDB`（§4），但存量查询的迁移是独立可评审的
  diff，不混进本方案。
