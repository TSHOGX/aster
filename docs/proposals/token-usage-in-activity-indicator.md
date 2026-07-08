# 提案：在 thinking/responding 状态指示器里展示 token 用量

**Status:** 调研完成，待决策实现方案
**Date:** 2026-07-08
**Scope:** 评估能否在前端 `ActivityIndicator`（thinking/responding 状态条）里加上每次请求的上行/下行 token 数。

## 问题

用户提问：pi agent 应该能拿到每次请求的 token 上行下行(input/output)数量吧，现在 thinking/responding 的状态显示里能不能把这个也加上？

## 现象 / 需求背景

前端在 `frontend/components/workbench/activity-indicator.tsx` 里有一个 `ActivityIndicator` 组件，展示当前 turn 的活动状态（`thinking` / `tool` / `streaming` / `compacting` / `retrying` 等），旁边带请求计数和耗时。用户希望这里也能顺带展示当次模型调用消耗的 token 数（类似很多 IDE/CLI 工具里"用了多少 token"的实时反馈）。

## 排查路径

按数据从哪里产生、怎么流转、最终展示在哪三段链路排查：

1. **pi agent 侧**：token usage 到底有没有被算出来
2. **orchestrator 侧**：usage 有没有被这一层接住、存下来、透传出去
3. **前端侧**：现有的状态类型和组件是否已经有字段留空，还是完全没有

### 1. pi agent 确实拿到了 usage，但只写进了自己的私有 session 文件

参考此前的排查记录 `docs/proposals/turn-abort-proxy-correlation-timeout.md`：pi agent 的会话 trace 文件（`.../pi/.pi/agent/sessions/*.jsonl`）里，**每一次模型调用都记录了 `usage` 字段**（连同 `stopReason`、`errorMessage`）。这个文件目前只用于事后排查（比如那次 turn 中途自行终止的根因分析），是 pi agent 进程本地的日志，orchestrator 和前端都不会读取它。

结论：token usage 数据源头是存在的，pi agent 本身确实知道每次调用花了多少 token。

### 2. orchestrator 这一层，usage 数据被丢弃，从未落库或转发

orchestrator 用两条腿感知 pi agent 的行为：

- **proxy-correlation 事件**（`orchestrator/internal/store/proxy.go`）：`FinishProxyRequestParams`（第 35-46 行）只记录了 `ProxyConnectLatencyMS`、`UpstreamFirstByteLatencyMS`、`UpstreamTotalLatencyMS`、`RetryCount`、`TimeoutKind`、`HTTPStatus`、`ErrorClass`、`Error`。`StartProxyRequestParams`（第 18-24 行）和相关的结果结构体（第 26-33、48-55 行）里都**没有任何 token/usage 字段**。这一层压根没有设计承接 usage 的位置。

- **pi 输出事件流**（`orchestrator/internal/server/stream_parser.go` 第 240-297 行，`piOutputNormalizer.Handle`）：这是把 pi agent 吐出的事件规范化成 orchestrator 内部事件的地方，用 switch 处理各种 pi 事件类型，**没有 `"usage"` 这个 case**。任何不认识的合法事件类型都会落到 `default` 分支（第 285-296 行），被记录日志后直接丢弃——代码里甚至专门写了注释，说"未来可能出现的 usage/token 等事件不应该中断处理流程，忽略掉即可"。也就是说，即便 pi agent 现在或未来往这条流里塞 usage 事件，orchestrator 也是设计成主动无视它的。

- `orchestrator/internal/store/driver_state_pi.go`：只持久化 pi session 文件的指针和状态，跟 usage 无关。

结论：orchestrator 这一层不是"暂时没读"，是**架构上根本没有 usage 的容身之处**——DB schema 没有字段，事件规范化逻辑显式丢弃。

### 3. 前端类型和组件里，同样一片空白

- `frontend/components/harness-provider.tsx`（第 45-60 行）：`ActivityPhase` 和 `ConversationActivity` 类型只有 `phase`、`startedAt`、`requestCount`、`inflight`、`toolName`、`toolDetail`，没有 usage/token 字段。
- WebSocket 事件流里 `proxy.request.started/completed/failed` 这几个事件（`harness-provider.tsx` 第 527-545 行的 reducer），前端目前只用它们来做 `requestCount`/`inflight` 计数的信号触发，**完全不读事件payload 里的内容**——即便 orchestrator 未来把 usage 塞进这个 payload，前端现在的处理逻辑也接不住。
- `frontend/lib/types.ts`（第 52-58 行）：`ApiMessage` 类型（`id, session_id, role, content, created_at`）也没有 token 字段，所以就算想做"每条消息标注 token 数"（类似 f6ecb9d 加时间戳的做法，见 `frontend/components/workbench/message-bubble.tsx` 第 30-39 行的时间戳 UI 模式）也没有数据来源。
- `frontend/components/workbench/activity-indicator.tsx`（第 59-112 行）：组件本身结构上很容易加一个字段展示（旁边已经有 `requestCount`/`inflight` 的展示位，第 92-102 行），但目前没有数据可喂。

## 结论

**token 用量数据目前在整条链路里是"有头无尾"**：

```
pi agent（有 usage，写入本地 session 文件）
    │
    ✗ 断点：proxy-correlation 事件不带 usage 字段
    ✗ 断点：pi 输出事件流里 usage 事件被 piOutputNormalizer 显式丢弃
    ↓
orchestrator（DB schema 无 usage 字段，事件流不转发）
    │
    ✗ 断点：ConversationActivity / ApiMessage 类型无 usage 字段
    ✗ 断点：前端 reducer 不读 payload 内容，只用作计数触发信号
    ↓
frontend（ActivityIndicator 组件结构上可以展示，但拿不到数据）
```

不是"数据已经在传输链路里，只是 UI 没画出来"，而是**三层里有两个断点，需要跨 Go 后端 + WS 协议 + 前端三层改造**才能把这个数字端到端打通。

## 需要决策的问题（在写实现方案前）

1. **usage 数据的粒度**：pi agent 的 usage 是"每次完整模型调用结束后一次性给出"，还是流式过程中有增量？这决定了前端展示是"实时累加读数"还是"每轮结束后定格显示一个总数"。需要抓一份真实的 pi 事件流样本确认。
2. **展示位置**：只在 `ActivityIndicator`（turn 级别、临时状态条)里显示当前 turn 的用量，还是也要沉淀到 `ApiMessage`，做成消息气泡上的永久标注（类似时间戳）？两者数据模型改动范围不同——后者需要 DB 持久化，前者只需要走一遍 WS 事件不用落库。
3. **口径**：只统计 pi 与上游模型之间的 usage，还是要覆盖 claude-code-proxy 转发层可能存在的额外开销/重试导致的多次计费？

## 建议的实现路径（跨三层，按此顺序）

1. orchestrator: `piOutputNormalizer.Handle` 增加 `"usage"` 事件的 case，不再落入 default 丢弃分支；同时视决策问题 2 的结论，决定是否扩展 `FinishProxyRequestParams` / DB schema 持久化。
2. orchestrator: 把 usage 数据放进现有 WS 事件（或新增一个专门的 `usage` 事件类型）转发给前端。
3. frontend: `ConversationActivity` 类型加 usage 字段，reducer 里读取并更新；如需消息级持久化则同步扩展 `ApiMessage`。
4. frontend: `ActivityIndicator` 组件按 `requestCount`/`inflight` 的既有展示模式加一栏 token 计数。
