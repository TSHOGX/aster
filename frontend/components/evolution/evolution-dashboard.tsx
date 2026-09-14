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

const knowledgeSuggestions = [
  { title: "先查工况目录，再生成 SQL", desc: "先匹配已有行驶、充电、静置和热管理方案；只有粒度或字段确实不足时才提出新表。", evidence: "避免重复建设已有工况能力" },
  { title: "车辆映射先行", desc: "分析车辆数据前先完成 VID、IMEI、车型映射，后续查询统一附带车型和时间分区条件。", evidence: "降低空查、错表和重复探测" },
  { title: "失败 SQL 也进入学习轨迹", desc: "把工具报错、用户纠正和最终成功 SQL 串联起来，提炼 Doris 方言、字段和查询边界。", evidence: "用于改善 unusable 会话" },
  { title: "主表查询必须带过滤条件", desc: "优先读取派生小表；回查明细时必须限制时间分区和车辆范围，避免宽表全量扫描。", evidence: "提升稳定性与查询速度" },
];

const fallbackPlans: Plan[] = [
  { gap: "battery / work_condition", related_tables: ["dwd_gongkuang_run_res_di", "dwd_gongkuang_charge_res_grid_di"], recipes: ["real_range", "range_attainment"], recommendation: "优先复用既有行驶工况表；仅在字段或粒度不足时提出新表。" },
  { gap: "charging", related_tables: ["dwd_gongkuang_charge_res_di"], recipes: ["charge_speed", "remaining_time_accuracy"], recommendation: "按 5% SOC 锚点复用充电速度与剩余时间方案。" },
];

const COVERAGE_SQL = `SELECT
  project_name,
  grid_type,
  temp_interval,
  soc_interval,
  curr_interval,
  COUNT(*) AS sample_count,
  COUNT(DISTINCT vehicle_id) AS vehicle_count,
  MIN(vehicle_date) AS first_date,
  MAX(vehicle_date) AS last_date,
  ROUND(SUM(during_time), 2) AS duration_sum
FROM vhr_data.dwd_gongkuang_charge_res_grid_di
WHERE vehicle_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 90 DAY)
GROUP BY project_name, grid_type, temp_interval, soc_interval, curr_interval`;

const MAP_SQL = `SELECT
  a.vid,
  a.imei,
  a.model_code,
  a.model_name,
  COALESCE(b.series, m.series) AS series,
  COALESCE(b.product, m.product) AS product,
  b.pack_code,
  b.pack_name,
  b.batt_type,
  b.batt_cap_ah,
  b.vehicle_type,
  GREATEST(a.created_at, IFNULL(b.update_time, a.created_at)) AS updated_at
FROM vehicle_signal.dim_vhcl_attr a
LEFT JOIN vhr_data.dim_vehicle_battery_info_v4 b
  ON a.vid = b.vid
LEFT JOIN vhr_data.dim_series_mapping m
  ON a.model_code = m.model_code`;

const RANGE_SQL = `WITH run_seg AS (
  SELECT
    COALESCE(a.model_name, r.project_name) AS model_name,
    DATE_TRUNC('month', r.start_time) AS month,
    CASE
      WHEN r.avg_env_temp < 0 THEN '<0'
      WHEN r.avg_env_temp < 15 THEN '0-15'
      WHEN r.avg_env_temp < 30 THEN '15-30'
      ELSE '>=30'
    END AS temp_bucket,
    r.driving_mileage,
    ABS(r.start_real_soc - r.end_real_soc) AS discharge_real_soc,
    r.start_ev_cltc_rang
  FROM vhr_data.dwd_gongkuang_run_res_di r
  LEFT JOIN vehicle_signal.dim_vhcl_attr a
    ON CAST(r.vehicle_id AS BIGINT) = a.vid
  WHERE r.start_time >= DATE_SUB(CURRENT_DATE(), INTERVAL 90 DAY)
    AND r.driving_mileage > 1
    AND r.start_real_soc IS NOT NULL
    AND r.end_real_soc IS NOT NULL
)
SELECT
  model_name,
  month,
  temp_bucket,
  ROUND(SUM(driving_mileage), 3) AS distance_km,
  ROUND(SUM(discharge_real_soc), 3) AS real_soc_used,
  ROUND(SUM(driving_mileage) / NULLIF(SUM(discharge_real_soc), 0), 3) AS real_range_km,
  ROUND(AVG(start_ev_cltc_rang), 3) AS cltc_range_km,
  ROUND(
    (SUM(driving_mileage) / NULLIF(SUM(discharge_real_soc), 0))
    / NULLIF(AVG(start_ev_cltc_rang), 0),
    4
  ) AS range_attainment,
  COUNT(*) AS sample_count
FROM run_seg
GROUP BY model_name, month, temp_bucket
HAVING SUM(discharge_real_soc) >= 60`;

const tableSuggestions: Table[] = [
  {
    name: "工况覆盖字典",
    intent: "从充电工况分箱表汇总工况码、样本量、车辆数和可用日期，回答“有哪些工况、数据覆盖到哪天”。",
    status: "推荐",
    grain: "一行 = 一个 (project_name, grid_type, temp_interval, soc_interval, curr_interval)",
    refresh_cron: "0 2 * * *",
    domain: "coverage",
    archetype: "dict",
    source_sql: COVERAGE_SQL,
    caveats_md: "覆盖字典只描述已入库的分箱结果，不回放原始信号；grid_type 为空或 during_time<=0 的行应在上线前剔除。",
    schema_json: [
      { name: "project_name", type: "VARCHAR(100)", desc: "项目/车系" },
      { name: "grid_type", type: "VARCHAR(100)", desc: "工况分箱类型" },
      { name: "temp_interval", type: "VARCHAR(100)", desc: "温度分箱" },
      { name: "soc_interval", type: "VARCHAR(100)", desc: "SOC 分箱" },
      { name: "curr_interval", type: "VARCHAR(100)", desc: "电流分箱" },
      { name: "sample_count", type: "BIGINT", desc: "分箱样本数" },
      { name: "vehicle_count", type: "BIGINT", desc: "覆盖车辆数" },
      { name: "first_date", type: "DATE", desc: "最早有数日期" },
      { name: "last_date", type: "DATE", desc: "最晚有数日期" },
      { name: "duration_sum", type: "DOUBLE", desc: "累计时长" },
    ],
  },
  {
    name: "VID → 车型映射表",
    intent: "用车辆属性维表和电池维表统一 VID、IMEI、车型与 PACK，避免每次分析前重复探测。",
    status: "推荐",
    grain: "一行 = 一个 vid",
    refresh_cron: "0 1 * * *",
    domain: "lookup",
    archetype: "map",
    source_sql: MAP_SQL,
    caveats_md: "vehicle_signal.dim_vhcl_attr 没有 VIN，外部标识用 IMEI；车系/PACK 以 dim_vehicle_battery_info_v4 为准，缺失时回退 dim_series_mapping。",
    schema_json: [
      { name: "vid", type: "BIGINT", desc: "车辆 VID" },
      { name: "imei", type: "VARCHAR(64)", desc: "IMEI，维表中的外部标识" },
      { name: "model_code", type: "VARCHAR(32)", desc: "车型编码" },
      { name: "model_name", type: "VARCHAR(128)", desc: "车型名称" },
      { name: "series", type: "VARCHAR(32)", desc: "车系" },
      { name: "product", type: "VARCHAR(32)", desc: "产品线" },
      { name: "pack_code", type: "VARCHAR(64)", desc: "电池包编码" },
      { name: "pack_name", type: "VARCHAR(128)", desc: "电池包名称" },
      { name: "batt_type", type: "VARCHAR(32)", desc: "电芯类型" },
      { name: "batt_cap_ah", type: "DECIMAL(10,2)", desc: "电池容量" },
      { name: "vehicle_type", type: "VARCHAR(32)", desc: "车辆类型" },
      { name: "updated_at", type: "DATETIME", desc: "映射更新时间" },
    ],
  },
  {
    name: "车辆真实续航评估表",
    intent: "从行驶工况表按车型、月份和温度聚合真实续航与达成率，避免反复扫描行程明细。",
    status: "推荐",
    grain: "一行 = 一个 (model_name, month, temp_bucket)",
    refresh_cron: "0 3 * * 1",
    domain: "range",
    archetype: "range",
    source_sql: RANGE_SQL,
    caveats_md: "口径对齐 Run_Range_Rating：单段里程>1km，真实 SOC 覆盖去重后需>=60。行驶表没有 SOH，如需分箱再左连 dwd_gongkuang_rest_res_di.end_real_soh。",
    schema_json: [
      { name: "model_name", type: "VARCHAR(128)", desc: "车型" },
      { name: "month", type: "DATE", desc: "统计月份" },
      { name: "temp_bucket", type: "VARCHAR(16)", desc: "环境温度分箱" },
      { name: "distance_km", type: "DECIMAL(18,3)", desc: "累计行驶里程" },
      { name: "real_soc_used", type: "DECIMAL(18,3)", desc: "累计真实 SOC 消耗" },
      { name: "real_range_km", type: "DECIMAL(18,3)", desc: "真实续航 = 里程/真实SOC消耗" },
      { name: "cltc_range_km", type: "DECIMAL(18,3)", desc: "车型 CLTC 续航" },
      { name: "range_attainment", type: "DECIMAL(18,4)", desc: "真实续航 / CLTC" },
      { name: "sample_count", type: "BIGINT", desc: "有效行程数" },
    ],
  },
];

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
  const mapped: Record<string, string> = {
    工况覆盖字典: "work_condition_coverage_dict",
    "work_condition 覆盖字典": "work_condition_coverage_dict",
    work_condition_coverage_dict: "work_condition_coverage_dict",
    "VID → VIN / 车型映射表": "vehicle_id_model_map",
    "VID → VIN / 车型映射小表": "vehicle_id_model_map",
    "VID → 车型映射表": "vehicle_id_model_map",
    vehicle_id_model_map: "vehicle_id_model_map",
    车辆真实续航评估表: "vehicle_range_evaluation",
    vehicle_range_evaluation: "vehicle_range_evaluation",
  };
  if (mapped[name]) return mapped[name];
  return String(name || "candidate_table").toLowerCase().replace(/[^a-z0-9_]+/g, "_").replace(/^_+|_+$/g, "") || "candidate_table";
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

function inferSteps(table: Table, sources: string[], sql: string) {
  const steps: string[] = [];
  if (sources.length) steps.push(`读取 ${sources.join("、")}。`);
  if (/\bjoin\b/i.test(sql)) steps.push("按 vid / vehicle_id 关联车辆或电池维表，补齐车型字段。");
  if (/\bwhere\b/i.test(sql)) steps.push("先过滤时间分区、车辆范围和质量门槛，避免扫宽表。");
  if (/\bcase\s+when\b/i.test(sql)) steps.push("对温度、SOC 或工况字段做分箱，形成查询主键。");
  if (/\bgroup\s+by\b/i.test(sql)) steps.push(`按 ${table.grain || "推荐粒度"} 聚合。`);
  if (/\bsum\(|\bcount\(|\bavg\(/i.test(sql)) steps.push("计算样本量、覆盖区间或指标公式。");
  if (/\bhaving\b/i.test(sql)) steps.push("用 HAVING 丢掉覆盖不足或样本过少的组合。");
  steps.push("复制下面整段 SQL 到 Doris 执行：先建表，再按来源 SELECT 首次装载。");
  return steps;
}

function keyColumns(table: Table, columns: TableColumn[]) {
  const names = columns.map((c) => c.name);
  const fromGrain = names.filter((name) => table.grain?.includes(name));
  if (fromGrain.length) return fromGrain;
  const metric = /count|sum|avg|km|used|attainment|duration|sample|updated|first_|last_/i;
  const keys = names.filter((name) => !metric.test(name));
  return (keys.length ? keys : names).slice(0, Math.min(4, Math.max(1, keys.length || 1)));
}

function executableSQL(table: Table, name: string, columns: TableColumn[], sourceSql: string) {
  if (table.create_sql?.trim()) return table.create_sql.trim();
  const qualified = `vhr_data.${quoteIdent(name)}`;
  const select = sourceSql.replace(/;\s*$/, "").trim();
  const header = [
    `-- 目标表：vhr_data.${name}`,
    `-- 复制后整段执行即可建表并首次装载`,
    `-- 如需重建：先执行 DROP TABLE IF EXISTS ${qualified};`,
  ].join("\n");
  if (!columns.length && /^(select|with)\b/i.test(select)) {
    return `${header}\nCREATE TABLE IF NOT EXISTS ${qualified} AS\n${select};`;
  }
  if (!columns.length || !/^(select|with)\b/i.test(select)) {
    return `${header}\n-- 缺少字段或来源 SELECT，无法生成可执行建表语句`;
  }
  const keys = keyColumns(table, columns);
  const defs = columns.map((c) => {
    const comment = String(c.desc || "").replace(/'/g, "''");
    return `  ${quoteIdent(c.name)} ${c.type || "STRING"} NULL COMMENT '${comment}'`;
  }).join(",\n");
  const keyList = keys.map(quoteIdent).join(", ");
  const hashKey = quoteIdent(keys[0] || columns[0].name);
  return [
    header,
    `CREATE TABLE IF NOT EXISTS ${qualified} (`,
    defs,
    `)`,
    `ENGINE=OLAP`,
    `DUPLICATE KEY(${keyList})`,
    `DISTRIBUTED BY HASH(${hashKey}) BUCKETS 8`,
    `PROPERTIES (`,
    `  "replication_allocation" = "tag.location.default: 3",`,
    `  "storage_format" = "V3",`,
    `  "compression" = "zstd",`,
    `  "light_schema_change" = "true"`,
    `);`,
    ``,
    `INSERT INTO ${qualified}`,
    `${select};`,
  ].join("\n");
}

function knownRecipe(table: Table): Recipe | null {
  const key = physicalName(table.name);
  if (key === "work_condition_coverage_dict") {
    const columns = columnsOf(table);
    return {
      physicalName: key,
      sources: ["vhr_data.dwd_gongkuang_charge_res_grid_di"],
      steps: [
        "读取充电工况分箱表 vhr_data.dwd_gongkuang_charge_res_grid_di，先取近 90 天 vehicle_date。",
        "工况码直接用已有分箱：grid_type + temp_interval + soc_interval + curr_interval，并保留 project_name。",
        "按上述主键 COUNT(*) / COUNT(DISTINCT vehicle_id)，同时取 MIN/MAX(vehicle_date) 和 SUM(during_time)。",
        "复制建表 SQL 整段执行，Doris 会先建表再从分箱表装载。",
      ],
      columns,
      materializeSql: executableSQL(table, key, columns, table.source_sql?.trim() || COVERAGE_SQL),
    };
  }
  if (key === "vehicle_id_model_map") {
    const columns = columnsOf(table);
    return {
      physicalName: key,
      sources: ["vehicle_signal.dim_vhcl_attr", "vhr_data.dim_vehicle_battery_info_v4", "vhr_data.dim_series_mapping"],
      steps: [
        "主表用 vehicle_signal.dim_vhcl_attr，取出 vid、imei、model_code、model_name。",
        "左连 vhr_data.dim_vehicle_battery_info_v4，按 vid 补 series、product、pack_code、batt_type、batt_cap_ah。",
        "再按 model_code 左连 vhr_data.dim_series_mapping，填电池维表缺失的车系/产品线。",
        "维表没有 VIN，映射键是 vid，外部标识用 imei。复制建表 SQL 整段执行即可。",
      ],
      columns,
      materializeSql: executableSQL(table, key, columns, table.source_sql?.trim() || MAP_SQL),
    };
  }
  if (key === "vehicle_range_evaluation") {
    const columns = columnsOf(table);
    return {
      physicalName: key,
      sources: ["vhr_data.dwd_gongkuang_run_res_di", "vehicle_signal.dim_vhcl_attr"],
      steps: [
        "读取行驶工况表 vhr_data.dwd_gongkuang_run_res_di 近 90 天完整行程。",
        "按 Run_Range_Rating 口径过滤：driving_mileage > 1，且 start/end_real_soc 非空。",
        "用 CAST(vehicle_id AS BIGINT) = dim_vhcl_attr.vid 补车型；没有维表命中时回退 project_name。",
        "按月份和 avg_env_temp 分箱：<0 / 0-15 / 15-30 / >=30。",
        "真实续航 = SUM(driving_mileage) / SUM(ABS(start_real_soc - end_real_soc))；达成率再除以 AVG(start_ev_cltc_rang)。",
        "HAVING 真实 SOC 累计消耗 >= 60 才装入评估表。复制建表 SQL 整段执行即可。",
      ],
      columns,
      materializeSql: executableSQL(table, key, columns, table.source_sql?.trim() || RANGE_SQL),
    };
  }
  return null;
}

function buildRecipe(table: Table): Recipe {
  const known = knownRecipe(table);
  if (known) return known;
  const sql = table.source_sql?.trim() || "";
  const sources = extractSources(sql);
  const name = physicalName(table.name);
  const columns = columnsOf(table);
  return {
    physicalName: name,
    sources: sources.length ? sources : (sql && !/^(select|with)\b/i.test(sql) ? [sql] : []),
    steps: inferSteps(table, sources, sql),
    columns,
    materializeSql: executableSQL(table, name, columns, sql),
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
    { label: "方案建议", value: tables.length + Math.max(plans.length, knowledgeSuggestions.length), note: "工况表与知识查询技巧", icon: Sparkles },
  ], [d, plans.length, tables.length]);

  return <main className={styles.page}><div className={styles.shell}>
    <header className={styles.hero}>
      <div>
        <div className={styles.eyebrow}><Sparkles size={14}/> BATTERYGPT · TRAJECTORY ANALYTICS</div>
        <h1>轨迹自进化结果</h1>
        <p>从真实会话轨迹中发现重复查询、知识缺口与工况分析需求，生成分析建议；这里不直接审批或发布知识库文件。</p>
      </div>
      <div className={styles.heroActions}>
        <span className={styles.status}><span/>分析结果 · 尚未发布</span>
        <a className={styles.reviewLink} href="/evolution/review"><ShieldCheck size={16}/>进入知识库审批</a>
        <button className={styles.runButton} onClick={run} disabled={busy}>{busy ? <RefreshCw className={styles.spin} size={17}/> : <Play size={17}/>}手动运行</button>
      </div>
    </header>
    {message && <div className={`${styles.notice} ${activeRun?.status === "failed" ? styles.noticeError : activeRun?.status === "done" ? styles.noticeSuccess : ""}`}>{busy && <RefreshCw className={styles.spin} size={15}/>}<span>{message}</span></div>}
    <section className={styles.metrics}>{metrics.map((m) => <article className={styles.metric} key={m.label}><div className={styles.metricIcon}><m.icon size={20}/></div><div><span>{m.label}</span><strong>{m.value}</strong><small>{m.note}</small></div></article>)}</section>
    <section className={styles.conclusion}><div className={styles.conclusionIcon}><Lightbulb size={22}/></div><div><h2>本轮建议摘要</h2><p>优先复用现有行驶、充电和工况分箱表，并补充车辆映射、覆盖字典和真实续航评估等轻量查询表。知识侧建议将失败 SQL、用户纠正与成功方案共同纳入学习，形成更稳定的查询步骤。</p></div></section>
    <div className={styles.sectionHead}><div><span>01</span><h2>新工况表建议</h2><p>每张表给出可直接复制执行的建表 + 装载 SQL，按真实源表加工，不用再改模板。</p></div><b>{tables.length} 条建议</b></div>
    <section className={styles.tableGrid}>{tables.map((t, i) => {
      const recipe = buildRecipe(t);
      const cols = recipe.columns;
      return <article className={styles.proposal} key={`${t.name}-${i}`}>
        <div className={styles.proposalTop}><div className={styles.proposalIcon}><Table2 size={20}/></div><span>{t.status || "候选"}</span></div>
        <h3>{t.name}</h3>
        <p>{t.intent}</p>
        <dl>
          <div><dt>目标表</dt><dd><code>vhr_data.{recipe.physicalName}</code></dd></div>
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
    <div className={styles.sectionHead}><div><span>03</span><h2>已有方案复用建议</h2><p>在设计新表之前，先判断哪些既有工况表和计算方案可以直接使用。</p></div><b>{plans.length || 2} 组匹配</b></div>
    <section className={styles.reuse}>{(plans.length ? plans : fallbackPlans).map((p, i) => <article key={`${p.gap}-${i}`}>
      <div className={styles.reuseHead}><Wrench size={17}/><strong>{p.gap}</strong><span>匹配度高</span></div>
      <div className={styles.flow}>
        <div><small>检索缺口</small><b>{p.gap}</b></div>
        <ChevronRight/>
        <div><small>推荐表</small><b>{p.related_tables[0] || "既有工况表"}</b></div>
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
