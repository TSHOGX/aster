# 中断后的下一轮秒回空答：pi 事件流残留 + 终态 turn 输出重试风暴

- **Status**: Implemented
- **Date**: 2026-09-14
- **Scope**: `sandbox-image`（`PiRPCTurnRunner` 的回合边界不变量、barrier 排空原语、能反映真实 pi 嵌套的假 pi）、`internal/store`（输出 CAS 的永久/瞬时分类）、`internal/bridge`（单信封 `emit_output` 与批量路径对齐）

## 问题

用户报告两个现象：

1. 中断一个正在流式输出的回合后，点队列行的 ↑ 发送排队消息 —— 消息进了 transcript、也离开了队列，
   但**永远收不到回复**。追问「我刚才发了什么」，agent 能准确答出，证明 pi **确实收到了** prompt。
2. 有时界面卡在「Agent is thinking…」，队列行还在，再点一次 ↑ 报 `turn is not parked`。

## 取证

开发环境 DB。同一个 generation 内，先看回合耗时与事件数：

```
id   status     error_class       ms      emit_output
247  completed                        5   4      ← 秒回
248  canceled   user_interrupted  2475   8
249  completed                        5   5      ← 秒回
250  canceled   user_interrupted 11520   209
251  completed                       49   5      ← 秒回
252  completed                    10801   158    ← 健康
```

247 / 249 / 251 都在 5–50ms 内「完成」，且**每一个都紧跟在一个被中断的回合之后**。
健康回合 239 / 252 分别是 11 和 154 个事件。

决定性证据是 payload。turn 251 唯一的 `emit_output` 是一个 `agent_end`，而它的 user message 是：

```
{"type":"agent_end","messages":[{"role":"user","content":[{"type":"text","text":"讲一个简单的小故事"}]…
```

`讲一个简单的小故事` 是 **turn 250** 的 prompt。turn 247 的那条则带着更早的计数探针。
也就是说：251 的那条事件不属于 251，它是 250 的遗留。另一侧，被中断的 turn 250 记录了
**零个** `turn_end` / `agent_end`。

日志里同时有第二个故障：`grep -c "output event turn CAS failed"` = **2280**，两分钟内单个
generation 打了 2277 条。

## 根因

不是 abort 特有的问题，而是**每一个回合都在留残留**，abort 只是让它显形。

直接 spike 真实 pi 0.77.0（`--mode rpc`，配一个 `HARNESS_REQUIRE_CORRELATION=0` 的
临时 proxy）确认事件流是嵌套的：

```
agent_start … [turn_start … turn_end]* … agent_end
```

`turn_end` 是**每个模型回合的内层边界**，工具调用期间会重复出现；`agent_end` 才是一次 prompt
的结束。`run_turn` 在第一个非 `toolUse` 的 `turn_end` 上就 break，所以**总有一个 `agent_end`
留在 pi 的 stdout 里没被读走**。spike 实测的残留：

- 正常回合 → `[agent_end]`
- 被 abort 的回合 → `[agent_end, response{command:"abort"}]`

正常回合之所以侥幸没出事：完成路径紧接着会调 `get_session_stats`，而 `_request_response`
会丢弃一切直到看见自己的 response id —— 顺手把那个游荡的 `agent_end` 吃掉了。而 abort / 失败
路径（共 4 处）是**提前 return** 的，跳过了这个调用，残留就活了下来。

于是下一个回合的第一次读取拿到这个陈旧的 `agent_end`，当成自己的终止事件，在 ~50ms 内
ack `completed`。pi 那边照样收到了 prompt，答案吐给了没人在听的管道。

**每次 abort 精确污染下一个回合，再下一个回合自愈。**

### 第二个缺陷：一条陈旧事件卡死整个 bridge

假完成之后，pi 还在为一个宿主已经retire的回合继续输出。`assertOutputEventTurnTx` 要求
`status='running'`，于是失败。而 `processor.go` 里：

- `handle()` 的单信封 `TypeEmitOutput` 路径**直接返回原始错误** → `ProcessOnce` 中止 →
  文件从未 unlink → 轮询循环永远重试同一条记录。
- `handleOutputBatch` 早就处理对了（记录单条失败、unlink、继续）。

因为 `ProcessOnce` 这一趟永远走不完，后面的信封（包括客户端等的 runtime projection）
一个都处理不到 —— 这就是界面卡在「Agent is thinking…」的原因。

## 修复

当作**一类 bug**（「提前 return 污染下一个回合」）处理，不是三个补丁。

**1. sandbox client：确立显式不变量。**
`run_turn` 返回时，pi 的 stdout 不含任何属于刚结束回合的事件 —— 下一回合的第一次读取
必然是它自己的第一个事件。用一个有界的 barrier 原语 `_barrier_request` 在 `finally` 里建立：
写一条 `get_session_stats`，读到自己的 response id 为止，之前的一切按定义都是残留。

选 barrier 而不是「读到安静为止」：安静无法区分「残留已排空」和「模型还在想」，而且每个
干净回合都要白等一个 timeout。barrier 是自定时的，且完成路径**复用同一条 response** 拿
driver-state update，健康回合的往返次数与修复前一致。超时只记日志不失败回合 —— abort
路径必须立刻返回，不能因为排空而让 Stop 挂住。

**2. host：给 CAS 失败分类。**
`assertOutputEventTurnTx` 改成查回 `turns.status`：
- turn 存在但已终态 → 永久（`errOutputEventTurnNotRunning`），丢弃事件、unlink、继续。
  它**不算** generation 故障 —— 一条迟到的事件不该让用户丢掉沙箱。
- lease / generation 谓词不匹配 → 瞬时，照旧向上传播以便重试。

单信封路径据此对齐批量路径的行为。

**3. 测试：假 pi 过于宽容。**
57/57 全绿而 bug 活着，是因为假 pi 是**固定行列表**，从不在 `turn_end` 之后发 `agent_end` ——
污染下一回合的残留在测试里根本不存在。新增 `ScriptedPi`：它**响应 stdin**，按真实 pi 的嵌套
生成事件，abort 时额外留下 `response{command:"abort"}`。

三个新回合边界测试 + 一个 Go 测试（终态 turn 的 `emit_output` 被丢弃且 `ProcessOnce` 继续推进，
断言 outbox 被排空、generation 未被retire）。

其中 `test_second_turn_does_not_consume_first_turns_agent_end` 修复前后都通过 —— 完成路径
本来就有那个意外的排空。保留它作为守卫：现在完成路径不再依赖意外行为，这个不变量需要被钉住。
另两个 abort 测试修复前失败，正是要防的回归。

## 验证

Go 全量（root）21 个包 + `go vet`、sandbox 60/60、frontend 222 测试 + typecheck + lint 全绿。

活栈上跑两轮中断→发送：

```
id   status     error_class       ms      emit_output
254  canceled   user_interrupted  94417  3005   ← 流式中途中断
255  completed                    16375   436   ← 真实回复
256  canceled   user_interrupted   8614   122   ← 再中断一次
257  completed                    10053   254   ← 真实回复
```

255 对「我刚才问了你什么」答出「You asked me to count slowly from 1 to 400, one number per
line.」—— 引用了真实会话历史；257 精确回 `BOUNDARY`。对比修复前同一位置的 5ms / 1 条陈旧事件。

`output event turn CAS failed` 从 2280 降到 3 条瞬时告警（abort 期间的 lease 竞态，重试即解决），
卡住的 outbox 文件 0 个。

## 未追查

- turn 251 那条真实回复最终去了哪里 —— 大概率被下一回合的 stats 排空吞掉，未完全证实。
- 静置后的「sandbox stopped」toast：可能只是空闲释放正常工作但文案骇人，未调查。
