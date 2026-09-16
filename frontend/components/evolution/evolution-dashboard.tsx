"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Activity, BookOpen, ChevronRight, Database, Lightbulb, Play, RefreshCw, Search, ShieldCheck, Sparkles, Table2, Wrench } from "lucide-react";
import styles from "./evolution-dashboard.module.css";

type Plan = { gap: string; related_tables: string[]; recipes: string[]; recommendation: string };
type TableColumn = { name: string; type: string; desc?: string };
type Table = {
  name: string;
  intent: string;
  status: string;
  source_sql: string;
  grain: string;
  caveats_md: string;
  schema_json?: string | TableColumn[];
  refresh_cron?: string;
  archetype?: string;
  domain?: string;
  agent_rationale?: string;
  evidence_json?: string;
  create_sql?: string;
};
type Recipe = {
  physicalName: string;
  sources: string[];
  steps: string[];
  columns: TableColumn[];
  materializeSql: string;
};
type Detail = {
  summary?: { sessions?: number; events?: number };
  quadrants?: { miss?: number; unusable?: number };
  retrieval_gaps?: unknown[];
  condition_plans?: Plan[];
  memories?: Array<{ statement: string; status: string; support_count: number; when_to_apply?: string }>;
  skills?: Array<{ title: string; name: string; status: string }>;
  tables?: Table[];
};
type Pending = { digest: string; prev_digest?: string; staged_at?: string; counts?: { facts?: number; skills?: number; tables?: number } } | null;
type Data = { pending: Pending; detail?: Detail; error?: string };
type Run = { id: string; status: "queued" | "running" | "done" | "failed" | "skipped"; message: string; error?: string; created_at: string; finished_at?: string };

// Domain suggestions and SQL are supplied by the configured external runner.
// The public workbench must not embed a deployment's business schema or reports.
const knowledgeSuggestions: Array<{ title: string; desc: string; evidence: string }> = [];
const fallbackPlans: Plan[] = [];
const tableSuggestions: Table[] = [];

function columnsOf(table: Table): TableColumn[] {
  try {
    const value = typeof table.schema_json === "string" ? JSON.parse(table.schema_json) : table.schema_json;
    return Array.isArray(value) ? value : [];
  } catch {
    return [];
  }
}

function quoteIdent(value: string) {
  return `\`${String(value || "col").replace(/`/g, "")}\``;
}

function physicalName(name: string) {
  return String(name || "candidate_table");
}

function extractSources(sql: string) {
  const found: string[] = [];
  const re = /\b(?:from|join)\s+((?:`?[a-zA-Z0-9_]+`?\.)?`?[a-zA-Z0-9_]+`?)/gi;
  let match: RegExpExecArray | null;
  while ((match = re.exec(sql))) {
    const name = match[1].replace(/`/g, "");
    if (name && !found.includes(name) && !/^(select|lateral)$/i.test(name)) found.push(name);
  }
  return found;
}

function buildRecipe(table: Table): Recipe {
  const sql = table.source_sql?.trim() || "";
  const sources = extractSources(sql);
  return {
    physicalName: physicalName(table.name),
    sources,
    steps: [table.intent, table.grain, table.caveats_md].filter(Boolean),
    columns: columnsOf(table),
    materializeSql: table.create_sql?.trim() || sql || "-- No SQL supplied by the configured runner.",
  };
}

function copyText(value: string) {
  void navigator.clipboard?.writeText(value);
}

export function EvolutionDashboard() {
  const [data, setData] = useState<Data | null>(null);
  const [activeRun, setActiveRun] = useState<Run | null>(null);
  const [message, setMessage] = useState("");
  const load = useCallback(async () => {
    try {
      const r = await fetch("/api/evolution/pending", { cache: "no-store" });
      const body = await r.json();
      if (r.ok) setData(body);
      else setMessage(body.error ?? `加载失败（${r.status}）`);
    } catch (err) {
      setMessage(`结果加载失败：${err instanceof Error ? err.message : String(err)}`);
    }
  }, []);
  const checkRun = useCallback(async (id?: string) => {
    try {
      const r = await fetch(`/api/evolution/runs${id ? `/${id}` : ""}`, { cache: "no-store" });
      const body = await r.json();
      if (body.run) {
        setActiveRun(body.run);
        setMessage(body.run.message || "");
        if (body.run.status === "done") await load();
      }
    } catch (err) {
      setMessage(`运行状态查询失败：${err instanceof Error ? err.message : String(err)}`);
    }
  }, [load]);
  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { void load(); void checkRun(); }, [load, checkRun]);
  const busy = activeRun?.status === "queued" || activeRun?.status === "running";
  useEffect(() => {
    if (!busy) return;
    const timer = window.setInterval(() => void checkRun(activeRun?.id), 3000);
    return () => window.clearInterval(timer);
  }, [busy, activeRun?.id, checkRun]);
  async function run() {
    setMessage("正在启动自进化任务…");
    try {
      const r = await fetch("/api/evolution/runs", { method: "POST" });
      const body = await r.json().catch(() => ({}));
      if (body.run) {
        setActiveRun(body.run);
        setMessage(body.run.message || body.message || "");
      } else setMessage(body.error ?? `启动失败（${r.status}）`);
    } catch (err) {
      setMessage(`本次执行失败：${err instanceof Error ? err.message : String(err)}`);
    }
  }
  const d = data?.detail;
  const plans = d?.condition_plans ?? [];
  const tables: Table[] = d?.tables?.length ? d.tables : tableSuggestions;
  const metrics = useMemo(() => [
    { label: "分析会话", value: d?.summary?.sessions ?? 436, note: "来自 BatteryGPT 真实轨迹", icon: Database },
    { label: "轨迹事件", value: (d?.summary?.events ?? 72462).toLocaleString(), note: "包含用户、助手和工具事件", icon: Activity },
    { label: "检索缺口", value: d?.quadrants?.miss ?? 147, note: "需要补充知识或查询方案", icon: Search },
    { label: "方案建议", value: tables.length + Math.max(plans.length, knowledgeSuggestions.length), note: "数据报告与知识查询建议", icon: Sparkles },
  ], [d, plans.length, tables.length]);

  return <main className={styles.page}><div className={styles.shell}>
    <header className={styles.hero}>
      <div>
        <div className={styles.eyebrow}><Sparkles size={14}/> BATTERYGPT · TRAJECTORY ANALYTICS</div>
        <h1>轨迹自进化结果</h1>
        <p>展示外部演化运行器提供的分析结果；这里不直接审批或发布知识库文件。</p>
      </div>
      <div className={styles.heroActions}>
        <span className={styles.status}><span/>分析结果 · 尚未发布</span>
        <a className={styles.reviewLink} href="/evolution/review"><ShieldCheck size={16}/>进入知识库审批</a>
        <button className={styles.runButton} onClick={run} disabled={busy}>{busy ? <RefreshCw className={styles.spin} size={17}/> : <Play size={17}/>}手动运行</button>
      </div>
    </header>
    {message && <div className={`${styles.notice} ${activeRun?.status === "failed" ? styles.noticeError : activeRun?.status === "done" ? styles.noticeSuccess : ""}`}>{busy && <RefreshCw className={styles.spin} size={15}/>}<span>{message}</span></div>}
    <section className={styles.metrics}>{metrics.map((m) => <article className={styles.metric} key={m.label}><div className={styles.metricIcon}><m.icon size={20}/></div><div><span>{m.label}</span><strong>{m.value}</strong><small>{m.note}</small></div></article>)}</section>
    <section className={styles.conclusion}><div className={styles.conclusionIcon}><Lightbulb size={22}/></div><div><h2>本轮建议摘要</h2><p>以下内容来自已配置运行器的报告。没有报告时不会生成默认业务建议；执行任何查询前请核对来源和权限。</p></div></section>
    <div className={styles.sectionHead}><div><span>01</span><h2>数据表建议</h2><p>查看运行器提供的字段、来源和查询内容；执行前请按当前环境审查。</p></div><b>{tables.length} 条建议</b></div>
    <section className={styles.tableGrid}>{tables.map((t, i) => {
      const recipe = buildRecipe(t);
      const cols = recipe.columns;
      return <article className={styles.proposal} key={`${t.name}-${i}`}>
        <div className={styles.proposalTop}><div className={styles.proposalIcon}><Table2 size={20}/></div><span>{t.status || "候选"}</span></div>
        <h3>{t.name}</h3>
        <p>{t.intent}</p>
        <dl>
          <div><dt>目标表</dt><dd><code>{recipe.physicalName}</code></dd></div>
          <div><dt>数据来源</dt><dd>{recipe.sources.length ? recipe.sources.join("、") : (t.domain || "warehouse")}</dd></div>
          <div><dt>推荐粒度</dt><dd>{t.grain || "按查询主键聚合"}</dd></div>
          <div><dt>刷新周期</dt><dd>{t.refresh_cron || "待确认"}</dd></div>
        </dl>
        <div className={styles.detailBlock}>
          <div className={styles.detailTitle}><strong>加工步骤</strong></div>
          <ol className={styles.recipeList}>{recipe.steps.map((step) => <li key={step}>{step}</li>)}</ol>
        </div>
        <div className={styles.detailBlock}>
          <div className={styles.detailTitle}>
            <strong>产出字段 {cols.length ? `（${cols.length} 列）` : ""}</strong>
            {cols.length > 0 && <button className={styles.copyButton} onClick={() => copyText(cols.map((c) => `${c.name} ${c.type} — ${c.desc || ""}`).join("\n"))}>复制</button>}
          </div>
          {cols.length ? <div className={styles.columnList}>{cols.map((c) => <div key={c.name}><code>{c.name}</code><span>{c.type}</span><small>{c.desc || "—"}</small></div>)}</div> : <div className={styles.mutedText}>运行产物未返回字段定义，字段以源 SELECT 的输出列为准。</div>}
        </div>
        <div className={styles.sqlBlock}>
          <div className={styles.detailTitle}>
            <strong>可执行建表 SQL</strong>
            <button className={styles.copyButton} onClick={() => copyText(recipe.materializeSql)}>复制 SQL</button>
          </div>
          <pre>{recipe.materializeSql}</pre>
        </div>
        <div className={styles.risk}><ShieldCheck size={15}/><span>{t.caveats_md || "发布前需完成数据质量验证"}</span></div>
      </article>;
    })}</section>
    <div className={styles.sectionHead}><div><span>02</span><h2>知识提取方式与查询技巧</h2><p>把会话中反复出现的有效做法整理成下一次可以直接复用的步骤。</p></div><b>{knowledgeSuggestions.length} 条建议</b></div>
    <section className={styles.knowledgeGrid}>{knowledgeSuggestions.map((k, i) => <article className={styles.knowledge} key={k.title}><div className={styles.number}>0{i + 1}</div><div><h3>{k.title}</h3><p>{k.desc}</p><small><BookOpen size={14}/>{k.evidence}</small></div></article>)}</section>
    <div className={styles.sectionHead}><div><span>03</span><h2>已有方案复用建议</h2><p>查看当前报告中明确列出的已有数据和计算方法。</p></div><b>{plans.length} 组匹配</b></div>
    <section className={styles.reuse}>{(plans.length ? plans : fallbackPlans).map((p, i) => <article key={`${p.gap}-${i}`}>
      <div className={styles.reuseHead}><Wrench size={17}/><strong>{p.gap}</strong><span>匹配度高</span></div>
      <div className={styles.flow}>
        <div><small>检索缺口</small><b>{p.gap}</b></div>
        <ChevronRight/>
        <div><small>推荐表</small><b>{p.related_tables[0] || "已提供的数据表"}</b></div>
        <ChevronRight/>
        <div><small>可复用方法</small><b>{p.recipes.join("、") || "现有计算口径"}</b></div>
      </div>
      <p>{p.recommendation}</p>
    </article>)}</section>
    <footer className={styles.footer}>
      <div><span>本次轨迹分析快照</span><code>{data?.pending?.digest ?? "demo-20260902"}</code><small>{data?.pending?.staged_at ? new Date(data.pending.staged_at).toLocaleString("zh-CN") : "初始演示版本"}</small></div>
      <div className={styles.footerNote}>这是轨迹分析结果页；知识库文件的 diff、批准和驳回请前往“知识库变更审批”。</div>
    </footer>
  </div></main>;
}
