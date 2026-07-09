# 排查记录：pi agent 在 turn 进行到一半自行终止（sess_2HG68uhmSrEIJh1k）

**Status:** Accepted — 止血修复已上线；orchestrator 端根治仍待跟进
**Date:** 2026-07-08
**Scope:** 定位 `sess_2HG68uhmSrEIJh1k` 在 2026-07-07 11:16 UTC（19:16 CST）左右自行终止的根因，并给出优化建议。

## 现象

用户反馈：Session `sess_2HG68uhmSrEIJh1k` 在执行一个电池数据分析任务时，进程"跑到一半"agent 自己停止了，没有任何报错提示给用户，也没有继续产出后续内容。

## 排查路径

按数据链路自下而上排查，涉及三个组件：

1. **pi agent 会话 trace**：`/var/lib/harness-publish/agent-homes/sess_2HG68uhmSrEIJh1k/pi/.pi/agent/sessions/2026-07-07T11-00-24-941Z_*.jsonl`（agent 自己的对话记录，含每次模型调用的 usage / stopReason / errorMessage）
2. **claude-code-proxy 日志**：`journalctl -u claude-proxy.service`（Anthropic↔OpenAI 协议转换层，同时也是 harness proxy correlation 的上报方）
3. **orchestrator 状态**：`/var/lib/harness-publish/state/orchestrator.db`（SQLite，记录 session/generation/turn/事件的权威状态）+ `orchestrator/internal/store/proxy.go`、`controlplane.go` 源码

三方时间戳互相印证，最终锁定问题窗口为 **2026-07-07 11:16:31 ~ 11:16:33 UTC（19:16:31~19:16:33 CST）**。

## 根因：两个问题叠加，第二个是直接致命的

### 问题 A（伴随现象，未致命）：`write` 工具连续两次收到空参数

pi trace 第 96-99 行：模型两次发出 `write` 工具调用，但 `arguments` 都是空对象 `{}`，被 pi 的工具校验直接拒绝：

```
"Validation failed for tool \"write\":\n  - path: must have required properties path, content\n\nReceived arguments:\n{}"
```

两次失败的响应 `usage.output` 都恰好是 **4097** token——这是当次唯一出现 4097 的两条记录，其余正常工具调用的 output 都在几百到两千多之间。这个数字和 claude-code-proxy 的 `MAX_TOKENS_LIMIT`（默认 4096，`/root/claude-code-proxy/src/core/config.py:23`，`.env` 未覆盖）非常接近，怀疑是模型输出在触达 token 上限时被截断，导致 `write` 调用的 JSON 参数没有流完整就被解析成了空对象。

**这一点目前只是强相关，未做到 100% 因果证实**（没有拿到 upstream 原始 `finish_reason`），列为"待确认"，不作为本次终止的直接原因——因为 agent 对工具校验失败是有自我纠正能力的（把错误喂回模型，模型可以重试),事实上模型确实又发起了第三次尝试。

### 问题 B（直接致命）：第三次尝试撞上 orchestrator 的 proxy-correlation 竞态，且第二个错误不可重试

第三次模型调用（trace 第 100-101 行）连续遇到两个错误：

```
line 100: stopReason=error, errorMessage="503 {\"detail\":\"Harness model proxy correlation unavailable\"}"  (11:16:31.662Z)
line 101: stopReason=error, errorMessage="403 {\"detail\":\"Harness model proxy context unavailable\"}"      (11:16:33.678Z)
```

pi trace 到第 101 行为止，**没有任何后续记录**（文件到此结束）。同时 orchestrator DB 里这个 turn（id=166）的状态是 `completed`（不是 `failed`），`completed_at` 精确等于 `ack_turn_completed` 事件时间 `11:16:31.907948037Z`——也就是说：这不是被外部强制 kill 掉的进程，而是 **pi agent 自己判定"无法重试"后，主动结束了这一轮 turn**，属于软停止。

claude-code-proxy 日志在同一秒精确对应上了这两个错误：

```
Jul 07 19:16:31 ... WARNING - Harness proxy request finish failed: id=0d86f40d... error=timed out
Jul 07 19:16:31 ... WARNING - Harness proxy request start denied: id=56eb61e8... error=timed out
Jul 07 19:16:31 ... INFO - "POST /v1/messages HTTP/1.1" 503 Service Unavailable
Jul 07 19:16:33 ... WARNING - Harness proxy request start denied: id=33de3cd1... error={"error":"proxy active context unavailable","error_class":"active_context_unavailable"}
Jul 07 19:16:33 ... INFO - "POST /v1/messages HTTP/1.1" 403 Forbidden
```

#### 为什么 503 之后重试了，403 之后没有重试

pi 内置了错误分类重试逻辑（`@earendil-works/pi-ai/dist/utils/retry.js`，`isRetryableAssistantError`）：只有当 `errorMessage` 命中一组正则（含 `"503"`、`"timed?.out"`、`"service.?unavailable"` 等）才会自动重试。

- 第一个错误文本含 `"503"` → 命中规则 → pi 自动重试，2 秒后发起了第三次尝试
- 第二个错误文本是 `403 ... "active_context_unavailable"`，不含任何可重试关键词 → **判定为不可重试**，直接放弃，turn 结束

#### 503/403 从哪来：claude-code-proxy → orchestrator 的一次内部 RPC 超时链路

- claude-code-proxy 每次向 upstream 转发模型请求前，会先向 orchestrator 的 `/internal/proxy/requests/start`（Unix socket）报备，成功后转发结束再调 `/internal/proxy/requests/finish`（`/root/claude-code-proxy/src/core/harness_observability.py:126-158`）。
- 这次 RPC 的超时 `HARNESS_OBSERVABILITY_TIMEOUT` 默认是 **0.25 秒**（`config.py:44-46`），`.env` 里没有覆盖，实际生效就是 0.25s。
- orchestrator 侧 SQLite 是单写连接（`db.SetMaxOpenConns(1)`）+ `busy_timeout=5000ms`（`store.go:104,124`）。当时（11:15:32~11:16:31 这一分钟）事件写入速率持续在 **27~34 条/秒**，写锁处于高争用状态。
- 一旦 orchestrator 处理耗时超过 0.25s（在 5s busy_timeout 生效前很容易发生），claude-code-proxy 的 RPC 客户端先超时放弃 → 对 sandbox 返回 **503**（`harness_observability.py:158`，捕获到非 401/403/404 的异常统一映射为 503）。
- 客户端超时之后立刻重试 `start`，但此时 orchestrator 端上一次 RPC 可能仍在推进，`active_model_request_contexts` 的"同一时刻只允许一个在途 turn"约束（`orchestrator/internal/store/proxy.go:111-133`）被打破 → `StartProxyRequest` 返回 `ErrProxyContextUnavailable` → 映射为 **403**（`harness_observability.py:156-157`）。

这个 403 本质上是**基础设施内部记账竞态的副作用**，不是真正的鉴权或模型问题，但 pi 把它当作硬错误终止了任务。

### 这类超时不是孤例

对当天全天日志做统计，`timed out` / `active_context_unavailable` 组合出现了近 20 次，且几乎总是成对出现（先 503 超时，紧接着 403 correlation unavailable），分布在全天多个时间点，不是这一次的偶发现象：

```
Jul 07 16:49:41/43, 17:13:11/13, 18:35:41, 18:41:11, 19:00:31/33, 19:15:01/03, 19:16:31/33, 19:40:01/03, 20:02:01, 20:05:01/03 ...
```

## 影响判断

- 直接原因：`HARNESS_OBSERVABILITY_TIMEOUT`（0.25s）明显小于 orchestrator SQLite 的 `busy_timeout`（5s），在 DB 写入繁忙时极易触发客户端先超时 → 503 → 竞态 → 403，且 403 不在 pi 的可重试白名单里，导致任务直接终止。
- 次要现象：`write` 工具两次收到空参数、疑似与 4096 token 上限相关，值得单独确认，但不是本次终止的直接原因。

两项均已给出止血修复，见下节。

## 已完成的止血修复（2026-07-08）

以下两项已经在 `/root/claude-code-proxy/.env` 落地并重启 `claude-proxy.service` 生效，通过 `curl http://127.0.0.1:8083/` 的 `config.max_tokens_limit` 确认生效值：

1. **`HARNESS_OBSERVABILITY_TIMEOUT`: 0.25s → 8s**（新增配置项，此前未设置过，走的是代码默认值）。
   取 8s 而不是刚好等于 orchestrator 的 5s busy_timeout，是为了在 DB 繁忙的情况下仍留出 3s 冗余，避免两个超时窗口贴边对齐导致客户端依然经常性地先放弃。这一项直接命中根因（问题 B），预期能大幅减少"503 超时 → 403 竞态 → pi 判定不可重试 → 任务终止"这条链路的触发概率。
2. **`MAX_TOKENS_LIMIT`: 4096 → 12288**（3 倍）。
   注意这个值只控制**单轮回复/工具调用的输出 token 上限**（`request_converter.py:80-83` 里传给 upstream 的 `max_tokens`），不是整个 session 的上下文窗口。整个 session 的上下文窗口由 pi agent 自己管理，走的是 pi 侧 `models.json`（`/var/lib/harness-publish/run/control/gen-*/driver/pi/models.json`）里 `sonnet` 这个模型条目——该条目没有覆盖 `contextWindow`，落到 pi 的默认值 **128000 tokens**（`model-registry.js:471`），这次改动未涉及、也不受影响。
   这一项对应问题 A（`write` 工具两次收到空参数、`usage.output` 恰好卡在旧上限 4097 附近的强相关现象），扩大输出上限后大文件/长 JSON 参数的 `write` 调用不会再在 4096 token 处被截断成空对象。

**尚未验证的部分**：这两项改动目前只确认了配置生效（`/` 接口 + 服务重启无报错），还没有拿到"改动后再跑一次类似规模任务，同类超时/截断不再出现"的真实回放证据。建议后续观察 `journalctl -u claude-proxy.service` 里 `timed out` / `active_context_unavailable` 的出现频率是否明显下降，作为验证手段。

## 尚待跟进的优化建议（按优先级）

1. **排查 orchestrator 事件写入是否真正走了批处理**：配置里有 `EmitOutputBatchMaxRows=64` / `EmitOutputBatchMaxAge=100ms`（`config.go` Events 默认值），但事发时实测写入速率仍有 27~34 条/秒的量级，值得确认批处理是否生效、是否还能进一步合批，从根上降低单写连接的锁争用——这是本次问题的深层原因，上面的超时调大只是加大容错窗口，没有解决写入压力本身。
2. **（可选，影响面较大，需评估）** 对由 `active_context_unavailable` 竞态触发的 403，可考虑在 claude-code-proxy 侧统一改为返回 503，让 pi 的现有重试逻辑能够覆盖这类"基础设施短暂抖动"场景，而不是直接放弃任务。即使超时窗口调大后触发概率降低，只要写入压力仍然存在，这条竞态路径本身没有被消除。

## 关键证据位置（供复核）

- pi 会话 trace：`/var/lib/harness-publish/agent-homes/sess_2HG68uhmSrEIJh1k/pi/.pi/agent/sessions/2026-07-07T11-00-24-941Z_019f3c3c-7ced-74d8-b745-78b56df2569b.jsonl` 第 96-101 行
- claude-code-proxy 日志：`journalctl -u claude-proxy.service`，`Jul 07 19:14:25` ~ `19:17:05`
- orchestrator DB：`sessions` / `runtime_generations` / `turns` / `events` 表，`session_id='sess_2HG68uhmSrEIJh1k'`，`turn_id=166`
- 源码：
  - `/root/claude-code-proxy/src/core/harness_observability.py:126-158`（503/403 映射逻辑）
  - `/root/claude-code-proxy/src/core/config.py:44-46`（默认超时 0.25s）
  - `/home/agent-runtime-platform/main/orchestrator/internal/store/proxy.go:65-172`（`StartProxyRequest` 单飞行约束与 `ErrProxyContextUnavailable`）
  - `/home/agent-runtime-platform/main/orchestrator/internal/store/store.go:104,124-126`（SQLite 单写连接 + busy_timeout）
  - `@earendil-works/pi-ai/dist/utils/retry.js`（`isRetryableAssistantError` 可重试错误正则白名单）
