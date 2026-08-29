#!/usr/bin/env bun
// tools/mock-agent.ts — a deterministic stand-in for a coding-agent CLI.
//
// It speaks the same contract a real CLI does: read a prompt on stdin, emit one
// JSON event per stdout line, end with a fenced json block that satisfies the
// node's schema. Each node is answered by rules over that node's own input, so
// the demo's output is derived from the fixtures rather than canned — change a
// fixture and the clusters, the table design and the verdicts all change.
//
// What it is NOT: a model. It cannot judge nuance, and it is not meant to. It
// exists so the GRAPH (fan-out, gates, fallbacks, staging, audit) can be run and
// reviewed end to end for free; `--engine pi` swaps in the real thing.

import { conceptOf, canonicalScope } from "../src/concepts.ts";

/** Which lookup-table archetype can retire a given query shape. Declared up
 *  here because dispatch runs at module top level. */
const ARCHETYPE_BY_SHAPE: Record<string, string> = {
  enum_probe: "dict",
  coverage_probe: "coverage",
  schema_probe: "coverage",
  analysis: "template",
  other: "template",
};

const input = await Bun.stdin.text();

const node = /\[\[NODE:([a-z_]+)\]\]/.exec(input)?.[1] ?? "unknown";
const dataRaw = /<<<DATA\n([\s\S]*?)\nDATA>>>/.exec(input)?.[1] ?? "{}";
let data: any = {};
try {
  data = JSON.parse(dataRaw);
} catch {
  data = {};
}

const say = (o: unknown) => process.stdout.write(`${JSON.stringify(o)}\n`);

say({ type: "session", id: `mock-${Bun.hash(input).toString(16).slice(0, 8)}` });

let answer: unknown;
try {
  answer = dispatch(node, data);
} catch (err) {
  say({ type: "error", message: err instanceof Error ? err.message : String(err) });
  say({ type: "end", status: "failed" });
  process.exit(0);
}

say({ type: "text", text: "```json\n" + JSON.stringify(answer, null, 2) + "\n```" });
say({
  type: "end",
  status: "ok",
  tokens: { input: Math.ceil(input.length / 4), output: Math.ceil(JSON.stringify(answer).length / 4) },
});

// --------------------------------------------------------------------- routing

function dispatch(n: string, d: any): unknown {
  switch (n) {
    case "blockkey":
      return blockkey(d);
    case "cluster":
      return cluster(d);
    case "verify":
      return verify(d);
    case "curate":
      return curate(d);
    case "critic":
      return critic(d);
    case "extract":
      return extract(d);
    case "skillinduce":
      return skillinduce(d);
    case "extreflect":
      return extreflect(d);
    case "extpropose":
      return extpropose(d);
    case "extjudge":
      return extjudge(d);
    case "mine":
      return mine(d);
    case "design":
      return design(d);
    case "replay":
      return replay(d);
    case "tablejudge":
      return tablejudge(d);
    case "tableskill":
      return tableskill(d);
    case "skillcritic":
      return skillcritic(d);
    case "tablecritic":
      return tablecritic(d);
    case "refute":
      // The adversarialVerify recipe writes its own prose prompt, so this node
      // reads the raw text rather than a JSON payload.
      return refute(input);
    default:
      throw new Error(`mock-agent: no rule for node "${n}"`);
  }
}

// ------------------------------------------------------------------ KB track

/** B0: collapse free-text scope labels into a canonical taxonomy. */
function blockkey(d: any) {
  const scopes: string[] = d.scopes ?? [];
  const mapping = scopes.map((s) => ({ from: s, to: canonicalScope(s) }));
  const blocks = [...new Set(mapping.map((m) => m.to))].sort().map((scope) => ({ scope }));
  return { mapping, blocks };
}

/** B1: one agent sees a whole block and returns equivalence clusters. The rule
 *  here is concept identity, which is what a model does natively across zh/en
 *  paraphrase — the point being that membership is decided over the whole block
 *  in one pass, with no pairwise threshold. */
function cluster(d: any) {
  const candidates: any[] = d.candidates ?? [];
  const groups = new Map<string, any[]>();
  for (const c of candidates) {
    const key = `${c.type}|${conceptOf(`${c.statement} ${c.when_to_apply ?? ""}`)}`;
    groups.set(key, [...(groups.get(key) ?? []), c]);
  }
  const clusters = [...groups.entries()].map(([key, members]) => {
    // The canonical statement is the member with the most evidence behind it,
    // longest as a tiebreak (more specific wording survives).
    const best = [...members].sort(
      (a, b) =>
        (b.evidence_sessions?.length ?? 0) - (a.evidence_sessions?.length ?? 0) ||
        b.statement.length - a.statement.length
    )[0];
    return {
      canonical: {
        type: best.type,
        scope: canonicalScope(best.scope ?? "", best.statement),
        statement: best.statement,
        when_to_apply: best.when_to_apply ?? "",
      },
      member_ids: members.map((m) => m.id),
      rationale: `同一知识点(${key.split("|")[1]})的 ${members.length} 条表述合并；跨中英文改写视为等价`,
    };
  });
  return { clusters };
}

/** C: check a statement against the live tree. */
function verify(d: any) {
  const statement: string = `${d.statement ?? ""} ${d.when_to_apply ?? ""}`;
  const known: string[] = d.kb_paths ?? [];
  const fixed: string[] = d.fixed_faults ?? [];
  const checked: string[] = [];

  const paths = [...statement.matchAll(/(\/[\w./-]+)/g)].map((m) => m[1]!);
  for (const p of paths) {
    checked.push(p);
    const exists = known.some((k) => k === p || k.endsWith(p) || p.endsWith(k));
    if (!exists) {
      const suggestion = known.find((k) => k.split("/").pop() === p.split("/").pop());
      return {
        verdict: "stale",
        corrected_statement: suggestion
          ? statement.replace(p, suggestion).trim()
          : statement.replace(p, "(路径已失效)").trim(),
        checked,
        rationale: `引用的路径 ${p} 在当前树上不存在`,
      };
    }
  }
  for (const f of fixed) {
    if (statement.includes(f))
      return {
        verdict: "fixed",
        corrected_statement: "",
        checked: [...checked, f],
        rationale: `该故障已在当前镜像中修复：${f}`,
      };
  }
  return { verdict: "current", corrected_statement: "", checked, rationale: "未发现失效引用" };
}

/** D: the promotion gate. */
function curate(d: any) {
  const it = d.item ?? {};
  const support = Number(it.support_count ?? 1);
  const verdict = it.verify_verdict ?? "current";
  if (String(it.statement ?? "").trim().length < 12)
    return { disposition: "reject", reason: "陈述过短，不足以在未来会话中被理解和应用" };
  if (verdict === "fixed") return { disposition: "reject", reason: "问题已修复，作为教训会误导后续会话" };
  if (verdict === "stale") return { disposition: "hold", reason: "引用已失效，等待修正后的表述再评估" };
  if (!String(it.when_to_apply ?? "").trim())
    return { disposition: "hold", reason: "缺少适用条件，agent 无法判断何时使用" };
  if (support >= 2) return { disposition: "promote", reason: `跨 ${support} 个会话的独立证据，且引用仍然有效` };
  return { disposition: "hold", reason: "仅单会话证据，等待跨会话复现" };
}

/** F: the one global gate over this cycle's additions. */
function critic(d: any) {
  const news: any[] = d.new_items ?? [];
  const existing: any[] = d.existing ?? [];
  const seen = new Map<string, string>();
  for (const e of existing) seen.set(`${canonicalScope(e.scope ?? "", e.statement)}|${conceptOf(e.statement)}`, e.statement);
  const vetoes: any[] = [];
  for (const it of news) {
    const key = `${canonicalScope(it.scope ?? "", it.statement)}|${conceptOf(it.statement)}`;
    const clash = seen.get(key);
    if (clash && clash !== it.statement) {
      vetoes.push({ id: it.id, reason: `与已有知识重复：「${clash.slice(0, 40)}…」` });
      continue;
    }
    seen.set(key, it.statement);
  }
  return { vetoes };
}

// ------------------------------------------------- the extraction lens itself

/** The facts/failure lens. Its output depends ONLY on the events the slice
 *  policy let it see, which is exactly the lever track A tunes: a
 *  dialogue-only slice cannot recover a constraint that only ever appeared in
 *  a tool result. */
function extract(d: any) {
  const events: any[] = d.slice_events ?? [];
  const memories: any[] = [];
  const push = (m: any) => {
    if (memories.length < 4 && !memories.some((x) => conceptOf(x.statement) === conceptOf(m.statement)))
      memories.push(m);
  };

  for (const e of events) {
    const text: string = e.text ?? "";
    const sql: string = e.args?.sql ?? "";

    if (e.kind === "tool_result" && e.is_error && /permission|compute.?group|denied/i.test(text)) {
      push({
        type: "bug",
        scope: "doris",
        statement:
          "Doris 查询返回 compute-group 或 backend 权限错误时，应按环境权限问题处理，而不是继续改写 SQL 语法",
        when_to_apply: "Doris SQL 执行返回 compute-group / backend permission 类报错时",
        confidence: 0.8,
      });
    }
    if (/distinct\s+work_condition/i.test(sql)) {
      push({
        type: "fact",
        scope: "work_condition",
        statement:
          "bms_telemetry.work_condition 是有限的工况码枚举，取值可直接查小表而不必对主表做 SELECT DISTINCT",
        when_to_apply: "需要知道有哪些工况码，或需要把工况码翻译成含义时",
        confidence: 0.75,
      });
    }
    if (/min\s*\(\s*ts|max\s*\(\s*ts|group\s+by\s+dt/i.test(sql)) {
      push({
        type: "heuristic",
        scope: "doris",
        statement:
          "确认某时间段是否有数据前，先查覆盖度小表；对主表做 min/max(ts) 或按 dt 计数属于昂贵探测",
        when_to_apply: "用户问某段时间/某车型有没有数据时",
        confidence: 0.7,
      });
    }
    if (/current_curve|充电曲线/i.test(text) && /json|解析|encode/i.test(text)) {
      push({
        type: "fact",
        scope: "charging",
        statement: "charge_session.current_curve 是双重编码的 JSON 字符串，取用前需要二次解析",
        when_to_apply: "读取充电曲线字段做分析或绘图时",
        confidence: 0.8,
      });
    }
    if (e.kind === "user" && /(不对|不是这个|我说的是|又错)/.test(text)) {
      push({
        type: "preference",
        scope: canonicalScope("", text),
        statement: `用户纠正过一次同类理解偏差：${text.slice(0, 40)}，回答前应先确认口径`,
        when_to_apply: "同一用户再次提出同类分析请求时",
        confidence: 0.6,
      });
    }
  }
  return { memories };
}

/** The skill lens: induce a reusable procedure from a long multi-tool session. */
function skillinduce(d: any) {
  const events: any[] = d.slice_events ?? [];
  const sqls = events.filter((e) => e.args?.sql).map((e) => e.args.sql as string);
  if (sqls.length < 2) return { skills: [] };
  const domain = /work_condition/i.test(sqls.join(" ")) ? "work_condition" : "doris";
  return {
    skills: [
      {
        // ASCII name: it becomes a directory under skills/ that agents grep by
        // path. The human-readable framing lives in when_to_use.
        name: `${domain.replace(/_/g, "-")}-query-order`,
        scope: domain,
        when_to_use: "用户提出需要先确认口径/范围再出结论的数据分析请求时",
        steps: [
          "先读 /agents/facts/INDEX.md 与相关 fact，确认已知口径",
          "再查覆盖度或枚举小表，确认 <时间范围> 与 <工况码> 存在",
          "只在确认范围后对主表做一次聚合查询",
          "输出结论时标注所用的表与时间范围",
        ],
        pitfalls: ["对主表反复做 SELECT DISTINCT / min-max 探测", "未确认工况码含义就直接下结论"],
        confidence: 0.7,
      },
    ],
  };
}

// ------------------------------------------------------------- track A nodes

function extreflect(d: any) {
  const q = d.quadrants ?? {};
  const targets: any[] = [];
  if ((q.miss ?? 0) > 0)
    targets.push({
      lens: "facts",
      change_kind: "slice",
      hypothesis:
        "漏抽集中在只出现于 tool_result / SQL 参数里的约束；facts 透镜当前只看对话文本，扩大切片应能召回",
    });
  if ((q.unusable ?? 0) > 0)
    targets.push({
      lens: "facts",
      change_kind: "prompt",
      hypothesis: "读到了却仍失败，说明 when_to_apply 写得不可判定，应要求写成可观察的触发条件",
    });
  if ((q.noise ?? 0) > 0)
    targets.push({
      lens: "failure",
      change_kind: "gates",
      hypothesis: "噪声偏高，失败透镜的硬信号门控应更严格",
    });
  return { targets: targets.slice(0, 2), budget_hint: targets.length * 4 };
}

function extpropose(d: any) {
  const cur = d.current ?? {};
  const lens = d.lens ?? "facts";
  const bump = (v: string) => `v${Number(String(v).replace(/^v/, "")) + 1}`;
  const variants: any[] = [];
  if (d.change_kind === "slice") {
    variants.push({
      version: bump(cur.version ?? "v1"),
      change_kind: "slice",
      slice: { include: ["user", "assistant", "tool_call", "tool_result"], max_events: 200 },
      gates: cur.gates ?? { min_turns: 1 },
      prompt_md:
        `${cur.prompt_md ?? ""}\n\n补充要求：工具调用的参数（尤其 SQL）与工具返回的报错文本也属于证据来源。` +
        `凡是从中能读出的稳定约束（枚举取值有限、字段需要二次解析、某类报错的真实归因），都要作为记忆抽出。`,
      rationale: "把 tool_call/tool_result 纳入切片，覆盖只在工具层出现的约束",
    });
  }
  if (d.change_kind === "prompt") {
    variants.push({
      version: bump(cur.version ?? "v1"),
      change_kind: "prompt",
      slice: cur.slice ?? { include: ["user", "assistant"], max_events: 200 },
      gates: cur.gates ?? { min_turns: 1 },
      prompt_md: `${cur.prompt_md ?? ""}\n\n补充要求：when_to_apply 必须写成可观察的触发条件（出现哪个报错、用户问哪类问题），不得写成“需要时”。`,
      rationale: "让适用条件可判定，修的是可用性而不是内容",
    });
  }
  if (d.change_kind === "gates") {
    variants.push({
      version: bump(cur.version ?? "v1"),
      change_kind: "gates",
      slice: cur.slice ?? { include: ["user", "assistant", "tool_result"], max_events: 200 },
      gates: { ...(cur.gates ?? {}), require_failure_signal: true, min_turns: 2 },
      prompt_md: cur.prompt_md ?? "",
      rationale: "收紧硬门控，减少无失败信号会话上的无效抽取",
    });
  }
  return { variants };
}

function extjudge(d: any) {
  const m = d.metrics ?? {};
  const b = d.baseline_metrics ?? {};
  const gained = (m.gap_recall ?? 0) > (b.gap_recall ?? 0);
  const kept = (m.precision_pct ?? 0) >= (b.precision_pct ?? 0) - 0.1;
  const clean = (m.contract_violations ?? 0) === 0;
  if (gained && kept && clean)
    return {
      verdict: "promote",
      reason: `召回从 ${fmt(b.gap_recall)} 升到 ${fmt(m.gap_recall)}，准确率未明显下降(${fmt(m.precision_pct)})，契约无违规`,
    };
  if (!clean) return { verdict: "reject", reason: "输出违反 JSON 契约" };
  if (!kept) return { verdict: "reject", reason: `准确率下降过多：${fmt(b.precision_pct)} → ${fmt(m.precision_pct)}` };
  return { verdict: "hold", reason: "指标无实质提升，保持现版本" };
}

function fmt(x: unknown): string {
  return typeof x === "number" ? x.toFixed(2) : "n/a";
}

// ------------------------------------------------------------- track B nodes

function mine(d: any) {
  const p = d.pain ?? {};
  const archetype = ARCHETYPE_BY_SHAPE[p.shape] ?? "template";
  const table = (p.tables ?? [])[0] ?? "unknown";
  const base = String(table).replace(/[^\w]/g, "_");
  const name = archetype === "template" ? `query_templates_${base}` : `${base}_${archetype}`;
  const worth = (p.sessions ?? 0) >= 2;
  const intent =
    archetype === "dict"
      ? `直接给出 ${base} 的枚举取值与样本量，替代对主表的 SELECT DISTINCT 探测`
      : archetype === "coverage"
        ? `按分区给出 ${base} 的数据覆盖情况，替代 min/max(ts) 与按 dt 计数的探测`
        : `沉淀 ${base} 上反复出现的分析 SQL 模板与参数说明`;
  return {
    worth,
    domain: "battery",
    name,
    archetype,
    intent,
    rationale: worth
      ? `该查询指纹在 ${p.sessions} 个会话中重复 ${p.occurrences} 次，平均 ${Math.round(p.avg_latency ?? 0)}ms，属于跨会话共性痛点`
      : "证据只来自单个会话，可能是个人习惯而非共性痛点，不建议建表",
  };
}

function design(d: any) {
  const name: string = d.name;
  const archetype: string = d.archetype;
  const wh = d.warehouse_schema ?? {};
  const painTables: string[] = d.pain?.tables ?? [];
  const table = painTables.find((t) => wh[t]) ?? Object.keys(wh)[0] ?? "bms_telemetry";
  const cols: string[] = wh[table] ?? [];
  const has = (c: string) => cols.includes(c);

  if (archetype === "coverage") {
    const part = has("dt") ? "dt" : cols[0]!;
    const key = has("work_condition") ? "work_condition" : cols[1]!;
    return {
      name,
      source: "warehouse",
      columns: [
        { name: part, type: "TEXT", desc: "分区日期" },
        { name: key, type: "TEXT", desc: "工况码" },
        { name: "rows_cnt", type: "INTEGER", desc: "该分区该工况的样本数" },
        { name: "first_ts", type: "TEXT", desc: "最早时间戳" },
        { name: "last_ts", type: "TEXT", desc: "最晚时间戳" },
      ],
      source_sql:
        `SELECT ${part}, ${key}, COUNT(*) AS rows_cnt, MIN(ts) AS first_ts, MAX(ts) AS last_ts ` +
        `FROM ${table} GROUP BY ${part}, ${key} ORDER BY ${part}, ${key}`,
      grain: `一行 = 一个 (${part}, ${key})`,
      refresh_cron: "0 2 * * *",
      caveats_md:
        "只统计已落库分区；当天分区在 02:00 刷新前不完整。rows_cnt 是样本数而非车辆数。",
      rationale: "覆盖度问题只需要按分区的存在性与计数，不需要读明细，因此可以压到很小的一张表",
    };
  }
  if (archetype === "dict") {
    const key = has("work_condition") ? "work_condition" : cols[1]!;
    const numeric = ["soc", "temp_c", "current_a"].filter(has);
    const extra = numeric.flatMap((c) => [
      { name: `${c}_min`, type: "REAL", desc: `${c} 观测下界` },
      { name: `${c}_max`, type: "REAL", desc: `${c} 观测上界` },
    ]);
    return {
      name,
      source: "warehouse",
      columns: [
        { name: "code", type: "TEXT", desc: "工况码" },
        { name: "samples", type: "INTEGER", desc: "样本数" },
        ...extra,
      ],
      source_sql:
        `SELECT ${key} AS code, COUNT(*) AS samples` +
        numeric.map((c) => `, MIN(${c}) AS ${c}_min, MAX(${c}) AS ${c}_max`).join("") +
        ` FROM ${table} GROUP BY ${key} ORDER BY samples DESC`,
      grain: "一行 = 一个工况码",
      refresh_cron: "0 2 * * *",
      caveats_md:
        "取值区间是历史观测范围，不是工程规格阈值；不要当作合规判定标准使用。含义列需要人工补充。",
      rationale: "枚举 + 观测区间足以回答绝大多数“有哪些工况/大致范围”的问题",
    };
  }
  return {
    name,
    source: "patterns",
    columns: [
      { name: "sql_template", type: "TEXT", desc: "归一化后的 SQL 模板（字面量已替换为 ?）" },
      { name: "shape", type: "TEXT", desc: "查询形态" },
      { name: "sessions", type: "INTEGER", desc: "出现过的会话数" },
      { name: "occurrences", type: "INTEGER", desc: "总出现次数" },
    ],
    source_sql:
      "SELECT sql_norm AS sql_template, shape, sessions, occurrences FROM query_patterns " +
      "WHERE shape = 'analysis' AND sessions >= 2 ORDER BY occurrences DESC",
    grain: "一行 = 一个查询指纹",
    refresh_cron: "0 3 * * *",
    caveats_md: "模板中的 ? 是被剥离的字面量，使用前必须按业务口径填回，不可直接执行。",
    rationale: "反复重写的分析 SQL 沉淀成模板，省掉每次重新拼装的过程",
  };
}

/** Replay one historical question, with and without the candidate table in
 *  context. In the offline demo the step count is derived from the pain's own
 *  probe depth; with a real engine this node is a sandbox A/B and the steps are
 *  counted off the trace. */
function replay(d: any) {
  const hasTable = Boolean(d.has_table);
  const p = d.pain ?? {};
  const probe = Math.min(Number(p.occurrences ?? 2), 4);
  if (hasTable) {
    return {
      steps: 2,
      errors: 0,
      answer_ok: true,
      notes: `先读 ${d.table_path ?? "小表"} 命中口径，再做一次聚合即可作答`,
    };
  }
  return {
    steps: 2 + probe,
    errors: (p.err_rate ?? 0) > 0 ? 1 : 0,
    answer_ok: true,
    notes: `需要 ${probe} 次探测查询才能确定口径与范围`,
  };
}

function tablejudge(d: any) {
  const before = d.before ?? {};
  const after = d.after ?? {};
  const fewerSteps = (after.steps ?? 99) < (before.steps ?? 0);
  const noWorseErrors = (after.errors ?? 0) <= (before.errors ?? 0);
  const bothAnswered = before.answer_ok !== false && after.answer_ok !== false;
  if (fewerSteps && noWorseErrors && bothAnswered)
    return {
      verdict: "useful",
      reason: `步数 ${before.steps} → ${after.steps}，错误 ${before.errors} → ${after.errors}，答案一致`,
      gain_steps: (before.steps ?? 0) - (after.steps ?? 0),
    };
  if (!noWorseErrors || !bothAnswered)
    return { verdict: "harmful", reason: "挂表后错误变多或答案不一致，说明表把 agent 带偏了", gain_steps: 0 };
  return { verdict: "no_gain", reason: "步数没有减少，表没有解决这个痛点", gain_steps: 0 };
}

function tableskill(d: any) {
  const t = d.table ?? {};
  const round = Number(d.round ?? 1);
  const lines = [
    "---",
    `name: ${t.skill_name ?? `use-${t.name}`}`,
    "status: observing",
    "---",
    "",
    `# 何时使用 ${t.name}`,
    "",
    "## When To Use",
    "",
    t.intent ?? "",
    "",
    "## Procedure",
    "",
    `1. 先读 \`/agents/data/${t.domain ?? "battery"}/derived/${t.name}/README.md\` 确认列含义与粒度。`,
    `2. 用 \`${t.name}.csv\` 回答“有哪些/有没有/大致范围”这类问题，不要对主表做探测查询。`,
    "3. 只有在小表回答不了时，才对主表做一次带明确范围的聚合查询。",
    "4. 作答时标注引用的小表与其 as_of 时间。",
    "",
    "## Grain",
    "",
    t.grain ?? "",
  ];
  // Round 1 deliberately omits the caveats section; the critic below is what
  // catches that, which is the reflect-loop earning its round.
  if (round >= 2) {
    lines.push("", "## Known Pitfalls", "", t.caveats_md ?? "", "", `数据截止：${t.as_of ?? "见 README"}`);
  }
  return { markdown: lines.join("\n") };
}

function skillcritic(d: any) {
  const md: string = d.markdown ?? "";
  const issues: string[] = [];
  if (!/\/agents\/data\//.test(md)) issues.push("没有给出 KB 内的确切路径，agent 找不到这张表");
  if (!/Known Pitfalls/.test(md)) issues.push("缺少 Known Pitfalls：没有写明口径边界，容易被当成规格阈值使用");
  if (!/as_of|数据截止/.test(md)) issues.push("没有提示数据时效，过期表会被当成事实");
  return { ok: issues.length === 0, issues };
}

/** The skeptic in `adversarialVerify`: it tries to REFUTE the publish claim, and
 *  the claim only survives if a minority manages to. Cheap protection against a
 *  single over-eager verdict promoting a table nobody needed. */
function refute(claim: string) {
  const weak = /单会话|证据不足|空表|no_gain|harmful|0 行|rows=0/.test(claim);
  return weak
    ? { refuted: true, reason: "证据不足或收益未被回放证实，不应进入所有新会话的上下文" }
    : { refuted: false, reason: "跨会话证据与回放收益一致，未能证伪" };
}

function tablecritic(d: any) {
  const cands: any[] = d.candidates ?? [];
  const live: string[] = d.live_tables ?? [];
  const vetoes: any[] = [];
  for (const c of cands) {
    if (live.includes(c.name)) {
      vetoes.push({ name: c.name, reason: "同名表已在库中，重复发布会造成两份口径" });
      continue;
    }
    if ((c.evidence_sessions ?? 0) < 2) {
      vetoes.push({ name: c.name, reason: "证据只来自单个会话，属于过拟合" });
      continue;
    }
    if ((c.rows ?? 0) === 0) vetoes.push({ name: c.name, reason: "构建结果为空表，没有可用信息" });
  }
  return { vetoes };
}
