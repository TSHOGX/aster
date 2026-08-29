// report.ts — the daily reviewable artifact.
//
// A reviewer should not have to read a database to decide whether to approve.
// Everything an agent decided, and every gate that fired, lands here with its
// rationale — including the gates that did NOT fire, because "0 vetoes" and
// "the veto pass never ran" look identical otherwise.

import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { db } from "./db.ts";
import { files } from "./config.ts";
import { quadrants, queryPains, retrievalGaps } from "./observe/views.ts";
import { diff, pendingInfo, readCurrentDigest } from "./kb/snapshot.ts";
import { listMemories, listSkills } from "./memories.ts";
import type { Proposal } from "./graphs/extractor-evolve.ts";

export interface ReportInput {
  day: string;
  counts: Record<string, Record<string, number>>;
  proposals?: Proposal[];
}

export function renderReport(input: ReportInput): string {
  const q = quadrants();
  const pains = queryPains();
  const gaps = retrievalGaps();
  const d = diff();
  const pending = pendingInfo();
  const accepted = listMemories({ shadow: false, status: "accepted" });
  const skills = listSkills("published");
  const tables = db()
    .query<{ name: string; archetype: string; status: string; reject_reason: string | null }, []>(
      `SELECT name, archetype, status, reject_reason FROM table_candidates ORDER BY name`
    )
    .all();
  const calls = db()
    .query<{ node: string; n: number; ok: number; ms: number }, []>(
      `SELECT node, COUNT(*) AS n, SUM(ok) AS ok, SUM(latency_ms) AS ms FROM agent_calls GROUP BY node ORDER BY n DESC`
    )
    .all();

  const lines: string[] = [
    `# 进化日报 ${input.day}`,
    "",
    "## 一、四象限（提取质量的梯度）",
    "",
    "| 象限 | 数量 | 含义 |",
    "|---|---|---|",
    `| miss 漏抽 | ${q.miss} | KB 里没有、agent 又确实需要的知识 |`,
    `| noise 噪声 | ${q.noise} | 抽出来却被否决或已失效 |`,
    `| unused 无人用 | ${q.unused} | 进库但没有会话读过 |`,
    `| unusable 用不上 | ${q.unusable} | 读到了却仍然失败/被纠正 |`,
    "",
  ];

  if (q.detail.missByScope.length) {
    lines.push("漏抽分布：", "");
    for (const m of q.detail.missByScope) lines.push(`- \`${m.scope}\`：${m.sessions} 个会话`);
    lines.push("");
  }

  lines.push("## 二、查询痛点（工况表的来源）", "");
  if (pains.length === 0) lines.push("本轮无跨会话查询痛点，表轨道未触发。", "");
  else {
    lines.push("| 痛点 | 形态 | 会话 | 次数 | 错误率 | 平均耗时 |", "|---|---|---|---|---|---|");
    for (const p of pains)
      lines.push(
        `| \`${p.key}\` | ${p.shape} | ${p.sessions} | ${p.occurrences} | ${(p.err_rate * 100).toFixed(0)}% | ${Math.round(p.avg_latency)}ms |`
      );
    lines.push("");
  }

  lines.push("## 三、本轮各节点计数", "");
  for (const [track, counts] of Object.entries(input.counts)) {
    lines.push(`- **${track}**：` + Object.entries(counts).map(([k, v]) => `${k}=${v}`).join(", "));
  }
  lines.push("");

  lines.push("## 四、知识库现状", "");
  lines.push(`- 已接受记忆：${accepted.length} 条`);
  lines.push(`- 已发布技能：${skills.length} 个`);
  lines.push(`- 派生小表：${tables.filter((t) => t.status === "staged" || t.status === "live").length} 张`);
  lines.push("");
  if (tables.length) {
    lines.push("| 小表 | 原型 | 状态 | 未通过原因 |", "|---|---|---|---|");
    for (const t of tables)
      lines.push(`| \`${t.name}\` | ${t.archetype} | ${t.status} | ${t.reject_reason ?? ""} |`);
    lines.push("");
  }

  if (input.proposals?.length) {
    lines.push("## 五、抽取器版本提议（需人工切换才会生效）", "");
    lines.push("| 透镜 | 候选版本 | 判定 | 召回 | 准确率 | 契约违规 | 理由 |", "|---|---|---|---|---|---|---|");
    for (const p of input.proposals)
      lines.push(
        `| ${p.lens} | ${p.version} (id=${p.versionID}) | **${p.verdict}** | ` +
          `${p.baseline.gap_recall.toFixed(2)} → ${p.metrics.gap_recall.toFixed(2)} | ` +
          `${p.baseline.precision_pct.toFixed(2)} → ${p.metrics.precision_pct.toFixed(2)} | ` +
          `${p.metrics.contract_violations} | ${p.reason} |`
      );
    lines.push("", "切换命令：`cli ext activate <id>`。未切换前，线上跑的仍是当前 active 版本。", "");
  }

  lines.push("## 六、待审批快照", "");
  if (!pending) {
    lines.push(`当前无待审批快照（live=${readCurrentDigest() ?? "无"}）。`, "");
  } else {
    lines.push(`- pending：\`${pending.digest}\`（前一版 \`${pending.prev_digest || "无"}\`）`);
    lines.push(`- 变更文件：${d.entries.length} 个`, "");
    for (const e of d.entries.slice(0, 40)) lines.push(`  - ${e.change} \`${e.path}\``);
    lines.push("", "审批命令：`cli kb approve " + pending.digest + "` / `cli kb reject " + pending.digest + " --reason ...`", "");
  }

  if (gaps.length) {
    lines.push("## 七、检索缺口明细", "");
    for (const g of gaps) lines.push(`- [${g.kind}] \`${g.scope_hint}\`：${g.sessions} 个会话 — ${g.query.slice(0, 80)}`);
    lines.push("");
  }

  lines.push("## 八、Agent 调用审计", "");
  if (calls.length === 0) lines.push("本轮没有 agent 调用（门控全部拦下，属于零成本安静日）。", "");
  else {
    lines.push("| 节点 | 调用 | 成功 | 累计耗时 |", "|---|---|---|---|");
    for (const c of calls) lines.push(`| ${c.node} | ${c.n} | ${c.ok} | ${Math.round(c.ms)}ms |`);
    lines.push("");
  }

  return lines.join("\n");
}

export function writeReport(input: ReportInput): string {
  mkdirSync(files.reports, { recursive: true });
  const path = join(files.reports, `${input.day}.md`);
  writeFileSync(path, renderReport(input));
  return path;
}
