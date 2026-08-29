# trajectory-evolve

把每天的用户轨迹变成两样东西：**更好的知识提取方式**，和**agent 能秒级查询的工况小表**。
用 [dynamic-workflow](https://github.com/TSHOGX/dynamic-workflow) 的原语编排，Bun + TypeScript。

这是 `agent-runtime-platform` 里 `docs/decisions/agentic-context-evolution.md` 那条思路的可运行版本：
确定性脊梁留在代码里，判断节点交给 agent，产物是一个内容寻址的 KB 快照——也就是平台里
只读挂载到 `/agents` 的那棵树。

默认用离线 mock 引擎跑，**不需要任何凭据、不联网、结果可复现**。换成真实 CLI 是一个参数。

---

## 一条流水线

```text
每天 (CST)
23:00  ingest       零 LLM    轨迹 JSONL → trajectory_events（按指纹跳过未变会话）
23:30  observe      零 LLM    → 会话观测 / KB 触达 / 查询指纹 → 痛点视图 + 四象限
00:00  harvest      LLM       三透镜抽取（版本化抽取器 + 硬门控）
00:30  evolve kb    LLM       blockkey → cluster → verify → curate → critic
01:30  evolve tables LLM+SQL  mine → design → drybuild → replay → panel → refute → skill → critic
每周日
02:30  evolve extractors LLM  reflect → propose → shadow A/B → 冻结金标打分 → judge
09:00  人工审批              kb diff / approve —— 一个 digest 同时含 facts、skills、小表
```

两条进化轨（知识提取方式、工况小表）共用同一层 **零 LLM 的观测底座**。这不是为了省钱而已：
两条轨看到的是同一份事实，而不是各自用自己的 prompt 去重新解读原始轨迹。

### 为什么“怎么抽”也能进化：四象限

“自进化”最容易翻车的地方是只改 prompt 却没有反馈信号。这里的梯度是四个由行为算出来的数：

| 象限 | 判据 | 说明该改什么 |
|---|---|---|
| **miss 漏抽** | KB 被搜了没命中；或全程没搜就开始试错 | 切片喂错了 / 门控太严 |
| **noise 噪声** | 抽出来却被 curate 拒、被 critic 否、被 verify 判失效 | 提示过度泛化 |
| **unused 无人用** | 进库但没有任何会话读过 | scope 归一化差、导航里找不到 |
| **unusable 用不上** | 读到了对应知识，会话仍然失败或被纠正 | `when_to_apply` 写得不可判定 |

`support_count` 只作为输入信号，不作为门槛——它按“合并簇里的**去重会话数**”由代码算出，
agent 只决定哪些条目算同一件事。

---

## 快速开始

需要 Bun ≥ 1.2（dynamic-workflow 是 Bun-only）。仓库里已装了一份本地 bun：

```sh
cd trajectory-evolve
export PATH="$PWD/node_modules/.bin:$PATH"   # 若已全局安装 bun 可跳过
bun install
bun run src/cli.ts demo
```

`demo` 会用 `fixtures/trajectories/` 里的 10 个会话跑完整条流水线，产物落在 `state/`
（可整个删掉重建）。

---

## 命令

```sh
cli init                       # 建库、生成模拟数仓、载入抽取器版本、写入 KB 种子快照
cli ingest [--dir <path>]      # 轨迹 JSONL → trajectory_events
cli observe                    # → 观测表 + 查询指纹 + 痛点/缺口/四象限
cli pains                      # 打印痛点、检索缺口、四象限
cli harvest                    # 三透镜抽取
cli evolve kb|tables|extractors|all
cli kb status | diff | ls [digest] | approve <digest> | reject <digest> --reason "..."
cli ext list | activate <id>    # 抽取器版本：查看 / 切换 active
cli report [--day YYYY-MM-DD]
cli mcp [--http PORT]          # MCP：stdio 或 streamable-http（无 approve 工具）
cli cycle [kb|tables|extractors|all]  # cron 入口：带锁的完整周期，只暂存
cli status                     # 与 MCP evolve_status 同一份只读快照
cli demo

# 公共参数
--engine mock|pi   --model <name>   --resume
```

看某条进化图跑成什么样（dynamic-workflow 的实时视图）：

```sh
DYNAMIC_WORKFLOW_RUNS_ROOT="$PWD/state/runs" dwf view table-evolve
```

---

## 三条硬约束

这套东西能不能放心每天自己跑，取决于三件事，而不取决于模型多强。

**1. agent 只提议，代码才处置。**
表的 SQL 由 agent 设计，但执行前 `assertReadOnlySelect` 会拒掉多语句、写操作、DDL、
引用未知表；执行时强制加 `LIMIT`、走只读连接；出来的结果还要过体检（非空、行数上界、
主键唯一、无整列为空）。任何一条不过，直接 reject，不给 agent 重试到“看起来对”的机会。
发布与否是人工 `kb approve`，PII 正则兜底在 stage 之前无条件跑。

**2. 打分是算术，不是 agent 自评。**
抽取器 A/B 的指标（召回、准确率、冗余、契约违规）由 `src/eval.ts` 用概念键重叠算出，
对照 `eval/gold.json` 这份**人工冻结**的金标。金标里**必须有负样本**（`s07`：正确答案是
什么都不抽），否则每一次进化都会因为“抽得更多”而被奖励。agent 只在看到指标之后回答
“值不值得切换”。

**3. 候选版本进不了知识库。**
shadow 版本的输出写在 `shadow=1` 上，发布路径根本不读它。切换 active 是
`cli ext activate <id>`——因为这会改变明天所有会话的行为，属于人的决定。

零 LLM 的门控同样重要：痛点视图为空、四象限全 0，整条轨在第一次 agent 调用之前就结束了。
agent 调用数随**新增知识/新增痛点**增长，不随流量增长。

---

## 运行示例

```text
$ bun run src/cli.ts demo

=== 1. ingest（零 LLM）===============================================
10 个会话，94 个事件

=== 2. observe（零 LLM）==============================================
查询 20 条 → 指纹 14 个；KB 触达 5（未命中 3）
  痛点 coverage_probe:bms_telemetry       会话=4 次数=6
  痛点 enum_probe:bms_telemetry           会话=4 次数=4
  痛点 analysis:bms_telemetry             会话=2 次数=4
  痛点 analysis:bms_telemetry+charge_session 会话=2 次数=2
四象限：miss=4 noise=0 unused=0 unusable=0

=== 3. harvest（三透镜，带硬门控）===================================
门控拦下 17，LLM 调用 13 → 候选记忆 8，候选技能 1

=== 4. evolve kb（cluster→verify→curate→critic）====================
候选 6 → 簇 4（吸收 2），promote 2 / hold 2 / reject 0 / veto 0

=== 5. evolve tables（mine→design→build→replay→panel→refute）=======
痛点 4 → 建表 3（体检拒绝 0），有收益 3 / 无收益 0，暂存 3

=== 6. evolve extractors（shadow A/B → 人工切换）====================
  facts v2 (id=2) → promote：召回 0.23 → 1.00，准确率 0.75 → 0.93

=== 7. 人工切换新版本后复跑（闭环）=================================
ext activate 2 → facts v2（slice=user+assistant+tool_call+tool_result）
复跑：新候选 18 → 簇 6（吸收 2），promote 3 / hold 3

=== 8. stage + 审批 ==================================================
暂存 1ebc6c2efc2ed35a
  added     data/battery/derived/bms_telemetry_coverage/...
  added     data/battery/derived/bms_telemetry_dict/...
  added     data/battery/derived/query_templates_bms_telemetry/...
  added     facts/charging.md
  added     facts/doris.md
  added     facts/work_condition.md
  modified  facts/INDEX.md
  added     skills/use-bms-telemetry-coverage/SKILL.md
  ...
```

### 这一轮实际发生了什么

**第 6 步是这套设计的关键证据。** `facts/v1` 的切片只有 `user + assistant`，所以它看不见
只出现在 SQL 参数和工具报错里的约束——召回 0.23。四象限里 `miss=4` 指出了这一点，
reflect 判断“该改切片而不是改提示”，候选 `v2` 把 `tool_call/tool_result` 纳入切片，
在冻结金标上召回升到 1.00 且准确率没掉。切换之后复跑，第 7 步里之前一直漏掉的工况知识
就被抽出来了：

```text
$ cat state/agents-kb/current/facts/work_condition.md
# work_condition

## bms_telemetry.work_condition 是有限的工况码枚举，取值可直接查小表而不必对主表做 SELECT DISTINCT

- 适用：需要知道有哪些工况码，或需要把工况码翻译成含义时
- 支持：4 个会话
- 证据：s06, s01, s02, s10
```

**工况小表是真跑出来的，不是编的。** 4 个痛点里有 3 个值得建表，SQL 真的在
`state/warehouse.db` 上执行过并通过体检：

```text
$ head -4 state/agents-kb/current/data/battery/derived/bms_telemetry_dict/bms_telemetry_dict.csv
code,samples,soc_min,soc_max,temp_c_min,temp_c_max,current_a_min,current_a_max
WC11,165,1.2,100,-4.7,39.4,-146,143
WC03,157,0.5,98.9,-4.7,40,-149.8,141.8
WC42,40,2.3,99.3,-3.7,38.8,-145.5,138.7
```

`WC42` 只有 40 个样本，正对应 fixture 里“这个工况码最近两天才出现”的覆盖空洞——
也正是 `s03`、`s10` 两个会话反复对主表做 `min/max(ts)` 探测想搞清楚的事。

**技能是被评审过一轮才发布的。** `skill` 阶段是 draft → critic → revise 循环：第一轮
故意漏了口径边界与时效，critic 指出来，第二轮补上才通过。日报和 journal 里都能看到
`r1` 之后有 `r2`。

**每一步都可审计。** `state/agents-kb/reports/<day>.md` 是给人看的日报（四象限、痛点、
各节点计数、版本提议、待审批 diff、agent 调用统计）；`dwf view` 是执行视角：

```text
├─ replay  [6✓ 0▶ 0✗ / 6]
│   ✓ mock:replay:bms_telemetry_coverage/without 0.0s 18tok
│   ✓ mock:replay:bms_telemetry_coverage/with 0.0s 37tok
├─ panel  [9✓ 0▶ 0✗ / 9]
│   ✓ mock:tablejudge:bms_telemetry_coverage/步数 0.0s 18tok
├─ refute  [6✓ 0▶ 0✗ / 6]
│   ✓ mock:refute#1 0.0s 12tok
```

### 审批流

`evolve` 只暂存，不发布。默认路径是人来看一眼：

```sh
$ cli kb status
live：420a0ccda3721dbc
pending：5743409ab9f2ca01

$ cli kb diff
  added     facts/doris.md
  modified  facts/INDEX.md

$ cli kb reject 5743409ab9f2ca01 --reason "先只发布 facts，等小表一起再发"
已驳回 5743409ab9f2ca01（快照保留在磁盘上作为决策证据）
```

`digest` 是必填且必须与 `pending` 一致——一个过期的终端没法批准评审者没看过的快照。
批准只影响**新**会话；运行中的沙箱仍然挂着它启动时解析到的那个快照。

---

## 目录

```text
src/
  observe/     零 LLM 底座：ingest、SQL 归一化与分类、观测表、痛点/缺口/四象限
  graphs/      harvest（三透镜）、kb-evolve、table-evolve、extractor-evolve
  tables/      drybuild —— 确定性构建 + 体检（agent 提议，代码处置）
  kb/          snapshot —— 内容寻址、carry-forward、stage/approve/reject/diff
  agents/      runner —— 唯一的 agent 调用入口（JSON 契约、数据围栏、fallback、审计）
  engines/     mock + adapters（mock 与 pi 都注册，叶子由 TE_ENGINE 选择）
  mcp/         宿主 MCP：五工具，无 approve；stdio 与 streamable-http
  eval.ts      对照冻结金标的算术打分
  concepts.ts  概念键（打分与聚类共用的确定性规则）
  extractors.ts 版本化抽取器：prompt / schema / gates / slice
extractors/<lens>/<version>/{prompt.md,schema.json,gates.json,slice.json}
eval/gold.json          冻结金标（含负样本）
fixtures/trajectories/  10 个示例会话
seed/                   KB 种子（CONSTITUTION、人工技能、语料 README）
tools/mock-agent.ts     被 spawn 的假 CLI，按输入用规则作答
state/                  运行产物：DB、模拟数仓、KB 快照、日报、journal
```

数据库分两组、生命周期相反：`trajectory_events` / 观测表是**可重建**的（重新 ingest 整段替换）；
`semantic_memories` / `skill_candidates` / `table_candidates` 是**不可重建**的（agent 产出 + 人的决定），
两边只用软键（session id、证据引用）相连。重新 ingest 永远不会级联删掉后者。

---

## 切到真实引擎

```sh
cli evolve all --engine pi --model glm-5.2
```

要求 `pi` 在 PATH 上并已认证（凭据在 pi 自己的配置里，不在本仓库）。图、契约、门控、
审批流都不变——只有叶子换了。dynamic-workflow 的 `engine` 是叶子上的字段，所以也可以
让设计用 glm、裁判用 claude/codex，减少“自己给自己点头”。

接进 Go 平台的落点：

- **MCP**（协议，架构无关）：宿主进程 `bun run src/mcp.ts --http 8104`，沙箱经
  `harness-evolve.internal` 调用五个工具。agent 换成别的只要还讲 MCP，工具名不变。
- **Skill**（何时/怎么调）：`/agents/skills/self-evolve/SKILL.md`，随 KB 快照走，
  不绑死在某个 CLI 的 system prompt 里。
- **Cron**：与 MCP 走同一套 `ops` / `jobs`（`cli cycle`）。`evolve_run` 立刻返回
  job id；并发周期返回 `skipped`，不排队。
- 沙箱**没有** `kb_approve` / `ext_activate`。发布仍是专家 CLI。
- `observe/` 那一层对应新增的确定性 SQL 作业，读 `trajectory_events`，不需要 LLM
- `graphs/` 三张图对应 `internal/memory` 里的节点（现有 `graph.go` 已实现 KB 那条）
- `kb/snapshot.ts` 对应 `internal/memory/kb.go` 的 stage/approve，产物结构一致
- `state/derived/<table>/` 是构建作业的输出目录，由发布器叠加进快照的 `data/<domain>/derived/`
- 平台接线清单：`agent-runtime-platform/docs/self-evolve-mcp.md`

---

## MCP / skill / cron

三扇门进同一套 `src/ops.ts` + `src/jobs.ts`。这是为了以后换 agent 运行时（今天
是 pi，明天可能是别的）不必重写进化图：前面的协议可以变，图不能变。

```text
用户说「进化一下」 → 读 /agents/skills/self-evolve
                   → MCP evolve_status（空痛点则停）
                   → evolve_run → job id
                   → 轮询 evolve_job
                   → 快照 STAGED；本会话仍挂当前 /agents
                   → 专家 cli kb approve / harness-ctl kb approve
```

```sh
bun run src/mcp.ts --http 8104          # 宿主 MCP
bun run src/cli.ts cycle all            # cron；拿不到锁就 skipped
```

工具只有：`evolve_status` `evolve_run` `evolve_job` `kb_status` `kb_diff`。
从平台 `memory.db` 吃轨迹：`TE_MEMORY_DB=... cli ingest` 或 cron 脚本。
把快照写进平台那棵树：`TE_KB_ROOT=/var/lib/harness-publish/agents-kb`（不要和
Go 夜间 `replay` 同一晚抢 `pending`）。

---

## 已知边界

诚实说明哪些地方是模拟的，别把 demo 数字当成生产结论：

- **mock 引擎不是模型。** 它按规则回答，用来把图、门控、fallback、审批、审计跑通并可复现。
  语义判断的质量得换真实引擎才能评估。
- **replay 是模拟的。** 离线模式下“挂表 vs 不挂表”的步数由痛点的探测深度推出。生产里
  这一步应该是起一次性沙箱、挂两个不同快照跑同一个历史问题，从 trace 里数步数。
- **`state/warehouse.db` 是 SQLite 假数仓。** 换 Doris 时 `drybuild` 里的连接与只读账号要改，
  SQL 方言差异也要过一遍（生成的 SQL 目前刻意只用最通用的语法）。
- **`adversarialVerify` 的叶子不入 `agent_calls`。** 那是 recipe 自己的 `agent()` 调用，
  记录在 dynamic-workflow 的 journal 里。对账时：journal 是全量，`agent_calls` 是本流水线自己拥有的节点。
- **金标只有 10 个会话。** 真实部署至少要人工标 20-30 个覆盖典型场景的会话，每月增补几个。
- **没有多租户隔离。** `data/` 下的东西对同一台机器上的所有沙箱可见，和平台现状一致。

---

## 测试

```sh
bun test              # SQL 指纹、drybuild、MCP 工具表、memory.db ingest、锁跳过
bun --bun tsc --noEmit
```

测试刻意集中在**不该出错的确定性部分**：指纹是否把“同一问题不同字面量”折叠成一个键、
守卫是否拦住写操作与未知表、体检是否拦住空表和重复主键、同一设计两次构建是否同一 digest。
