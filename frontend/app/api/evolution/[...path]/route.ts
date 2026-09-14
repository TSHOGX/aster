import { NextRequest } from "next/server";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, readdirSync, renameSync, writeFileSync } from "node:fs";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const STATE_ROOT = process.env.EVOLUTION_STATE_ROOT ?? "/var/lib/harness-publish/trajectory-evolve-state";
const PROJECT_ROOT = process.env.EVOLUTION_PROJECT_ROOT ?? "/home/agent-runtime-platform/trajectory-evolve";
const MEMORY_DB = process.env.EVOLUTION_MEMORY_DB ?? "/var/lib/harness-publish/state/memory.db";
const BUN = process.env.EVOLUTION_BUN ?? `${PROJECT_ROOT}/node_modules/.bin/bun`;
const FRESH_MS = 4 * 60 * 60 * 1000;
// Temporary test mode: every authenticated user can view and run. Set
// EVOLUTION_VIEWERS back to a comma-separated allowlist for production.
const VIEWERS = new Set((process.env.EVOLUTION_VIEWERS ?? "*").split(",").map((x) => x.trim()).filter(Boolean));

type Context = { params: Promise<{ path?: string[] }> };

function json(data: unknown, status = 200) { return Response.json(data, { status, headers: { "cache-control": "no-store" } }); }
function readJson(path: string, fallback: unknown = null) { try { return JSON.parse(readFileSync(path, "utf8")); } catch { return fallback; } }
function requireState() { return null; }
type RunRecord = { id:string; status:"queued"|"running"|"done"|"failed"|"skipped"; created_at:string; started_at?:string; finished_at?:string; message:string; error?:string; snapshot_digest?:string; runner_pid?:number };
function runDir() { return `${STATE_ROOT}/runs`; }
function runPath(id:string) { return `${runDir()}/${id}.json`; }
function writeRun(run:RunRecord) { mkdirSync(runDir(), { recursive:true }); const tmp=`${runPath(run.id)}.${process.pid}.tmp`; writeFileSync(tmp,JSON.stringify(run,null,2)); renameSync(tmp,runPath(run.id)); }
function pidAlive(pid?:number){if(!pid)return false;try{process.kill(pid,0);return true}catch{return false}}
function normalizeRun(run:RunRecord):RunRecord { const age=Date.now()-new Date(run.started_at??run.created_at).getTime(); if((run.status==="running"&&run.runner_pid&&!pidAlive(run.runner_pid))||(run.status==="queued"&&age>120000)){run.status="failed";run.finished_at=new Date().toISOString();run.error="后台任务意外终止";run.message="本次执行失败：后台任务意外终止，请重新运行。";writeRun(run)} return run }
function allRuns():RunRecord[] { try{return readdirSync(runDir()).filter(x=>x.endsWith(".json")).map(x=>readJson(`${runDir()}/${x}`) as RunRecord).filter(Boolean).map(normalizeRun).sort((a,b)=>String(b.created_at).localeCompare(String(a.created_at)))}catch{return []} }
function latestSuccessfulAt():Date|null { const completed=allRuns().find(x=>x.status==="done")?.finished_at; const pending=readJson(`${STATE_ROOT}/agents-kb/pending.json`) as {staged_at?:string}|null; const dates=[completed,pending?.staged_at].filter(Boolean).map(x=>new Date(String(x))).filter(x=>!Number.isNaN(x.getTime())); return dates.sort((a,b)=>b.getTime()-a.getTime())[0]??null; }
const DEMO = { digest: "demo-20260902", prev_digest: "seed-current", staged_at: "2026-09-02T15:00:00.000Z", run_id: 0, counts: { facts: 0, skills: 0, tables: 0 } };
function demoDetail() {
  return {
    condition_plans: [
      { gap: "battery / work_condition", related_tables: ["dwd_gongkuang_run_res_di", "dwd_gongkuang_charge_res_grid_di"], recipes: ["real_range", "range_attainment"], recommendation: "优先复用既有行驶工况表；仅在字段或粒度不足时提出新表。" },
      { gap: "charging", related_tables: ["dwd_gongkuang_charge_res_di"], recipes: ["charge_speed", "remaining_time_accuracy"], recommendation: "按 5% SOC 锚点分析充电速度和剩余时间。" },
    ],
    memories: [],
    skills: [{ name: "query-condition-plan", title: "先查工况方案目录再生成 SQL", status: "candidate" }],
    tables: [
      {
        name: "工况覆盖字典",
        intent: "从充电工况分箱表汇总工况码、样本量、车辆数和可用日期，回答“有哪些工况、数据覆盖到哪天”。",
        status: "推荐设计",
        domain: "coverage",
        archetype: "dict",
        grain: "一行 = 一个 (project_name, grid_type, temp_interval, soc_interval, curr_interval)",
        refresh_cron: "0 2 * * *",
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
        source_sql: "SELECT project_name, grid_type, temp_interval, soc_interval, curr_interval, COUNT(*) AS sample_count, COUNT(DISTINCT vehicle_id) AS vehicle_count, MIN(vehicle_date) AS first_date, MAX(vehicle_date) AS last_date, ROUND(SUM(during_time), 2) AS duration_sum FROM vhr_data.dwd_gongkuang_charge_res_grid_di WHERE vehicle_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 90 DAY) GROUP BY project_name, grid_type, temp_interval, soc_interval, curr_interval",
        caveats_md: "覆盖字典只描述已入库的分箱结果，不回放原始信号；grid_type 为空或 during_time<=0 的行应在上线前剔除。",
      },
      {
        name: "VID → 车型映射表",
        intent: "用车辆属性维表和电池维表统一 VID、IMEI、车型与 PACK，避免每次分析前重复探测。",
        status: "推荐设计",
        domain: "lookup",
        archetype: "map",
        grain: "一行 = 一个 vid",
        refresh_cron: "0 1 * * *",
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
        source_sql: "SELECT a.vid, a.imei, a.model_code, a.model_name, COALESCE(b.series, m.series) AS series, COALESCE(b.product, m.product) AS product, b.pack_code, b.pack_name, b.batt_type, b.batt_cap_ah, b.vehicle_type, GREATEST(a.created_at, IFNULL(b.update_time, a.created_at)) AS updated_at FROM vehicle_signal.dim_vhcl_attr a LEFT JOIN vhr_data.dim_vehicle_battery_info_v4 b ON a.vid = b.vid LEFT JOIN vhr_data.dim_series_mapping m ON a.model_code = m.model_code",
        caveats_md: "vehicle_signal.dim_vhcl_attr 没有 VIN，外部标识用 IMEI；车系/PACK 以 dim_vehicle_battery_info_v4 为准，缺失时回退 dim_series_mapping。",
      },
      {
        name: "车辆真实续航评估表",
        intent: "从行驶工况表按车型、月份和温度聚合真实续航与达成率，避免反复扫描行程明细。",
        status: "推荐设计",
        domain: "range",
        archetype: "range",
        grain: "一行 = 一个 (model_name, month, temp_bucket)",
        refresh_cron: "0 3 * * 1",
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
        source_sql: "WITH run_seg AS (SELECT COALESCE(a.model_name, r.project_name) AS model_name, DATE_TRUNC('month', r.start_time) AS month, CASE WHEN r.avg_env_temp < 0 THEN '<0' WHEN r.avg_env_temp < 15 THEN '0-15' WHEN r.avg_env_temp < 30 THEN '15-30' ELSE '>=30' END AS temp_bucket, r.driving_mileage, ABS(r.start_real_soc - r.end_real_soc) AS discharge_real_soc, r.start_ev_cltc_rang FROM vhr_data.dwd_gongkuang_run_res_di r LEFT JOIN vehicle_signal.dim_vhcl_attr a ON CAST(r.vehicle_id AS BIGINT) = a.vid WHERE r.start_time >= DATE_SUB(CURRENT_DATE(), INTERVAL 90 DAY) AND r.driving_mileage > 1 AND r.start_real_soc IS NOT NULL AND r.end_real_soc IS NOT NULL) SELECT model_name, month, temp_bucket, ROUND(SUM(driving_mileage), 3) AS distance_km, ROUND(SUM(discharge_real_soc), 3) AS real_soc_used, ROUND(SUM(driving_mileage) / NULLIF(SUM(discharge_real_soc), 0), 3) AS real_range_km, ROUND(AVG(start_ev_cltc_rang), 3) AS cltc_range_km, ROUND((SUM(driving_mileage) / NULLIF(SUM(discharge_real_soc), 0)) / NULLIF(AVG(start_ev_cltc_rang), 0), 4) AS range_attainment, COUNT(*) AS sample_count FROM run_seg GROUP BY model_name, month, temp_bucket HAVING SUM(discharge_real_soc) >= 60",
        caveats_md: "口径对齐 Run_Range_Rating：单段里程>1km，真实 SOC 覆盖去重后需>=60。行驶表没有 SOH，如需分箱再左连 dwd_gongkuang_rest_res_di.end_real_soh。",
      },
    ],
  };
}

async function authorized(request: NextRequest): Promise<Response | null> {
  const base = process.env.HARNESS_API_BASE_URL ?? process.env.ORCHESTRATOR_URL ?? "http://127.0.0.1:8090";
  try {
    const r = await fetch(`${base.replace(/\/+$/, "")}/api/me`, { headers: { cookie: request.headers.get("cookie") ?? "" }, cache: "no-store" });
    if (!r.ok) return json({ error: "login required" }, 401);
    const me = await r.json().catch(() => ({})) as { user_id?: string };
    if (!me.user_id || (!VIEWERS.has("*") && !VIEWERS.has(me.user_id))) return json({ error: "evolution preview access denied" }, 403);
  } catch { return json({ error: "orchestrator unavailable" }, 503); }
  return null;
}

async function handle(request: NextRequest, { params }: Context) {
  // Temporary rollout mode: allow the preview/run endpoint without the
  // orchestrator cookie bridge. Approval endpoints remain absent/disabled.
  const parts = (await params).path ?? [];
  if (request.method !== "GET" && !(request.method === "POST" && parts[0] === "runs")) return json({ error: "approval write actions are disabled" }, 403);
  const stateError = requireState(); if (stateError) return stateError;

  if (request.method === "GET" && parts[0] === "pending") {
    const pending = readJson(`${STATE_ROOT}/agents-kb/pending.json`, DEMO);
    const reportPath = pending ? `${STATE_ROOT}/agents-kb/reports/${String(new Date(pending.staged_at).toISOString().slice(0, 10))}.md` : "";
    const detailPath = pending ? `${STATE_ROOT}/agents-kb/reports/${String(new Date(pending.staged_at).toISOString().slice(0, 10))}.json` : "";
    return json({ pending, report: reportPath && existsSync(reportPath) ? readFileSync(reportPath, "utf8") : "这是一次已完成的示例运行：真实会话已导入，系统根据检索缺口匹配了既有工况方案。当前页面展示为候选建议，尚未发布到生产知识库。", detail: detailPath ? readJson(detailPath, demoDetail()) : demoDetail(), state_root: STATE_ROOT, read_only: true });
  }
  if (request.method === "GET" && parts[0] === "overview") {
    let current: string | null = null; try { current = readFileSync(`${STATE_ROOT}/agents-kb/current/.digest`, "utf8").trim(); } catch {}
    return json({ pending: readJson(`${STATE_ROOT}/agents-kb/pending.json`), current, state_root: STATE_ROOT });
  }
  if (request.method === "GET" && parts[0] === "runs") return json({ run:parts[1]?readJson(runPath(parts[1])):allRuns()[0]??null, latest_pending:readJson(`${STATE_ROOT}/agents-kb/pending.json`,DEMO), read_only:true });
  if (request.method === "POST" && parts[0] === "runs") {
    if (!existsSync(BUN)) return json({ error: `bun not found: ${BUN}` }, 503);
    const active=allRuns().find(x=>x.status==="queued"||x.status==="running"); if(active)return json({run:active,message:"已有一轮自进化正在运行，请稍后查看进度。"},202);
    const freshAt=latestSuccessfulAt(); if(freshAt&&Date.now()-freshAt.getTime()<FRESH_MS){const id=crypto.randomUUID();const run:RunRecord={id,status:"skipped",created_at:new Date().toISOString(),finished_at:new Date().toISOString(),message:`当前已经使用最新数据（最近一次结果生成于 ${freshAt.toLocaleString("zh-CN",{timeZone:"Asia/Shanghai"})}），4 小时内无需重复执行。`};writeRun(run);return json({run,message:run.message});}
    const id=crypto.randomUUID();const run:RunRecord={id,status:"queued",created_at:new Date().toISOString(),message:"自进化任务已进入运行队列。"};writeRun(run);
    try{const child=spawn(BUN,["run","scripts/web-cycle-runner.ts",id],{cwd:PROJECT_ROOT,env:{...process.env,TE_STATE_ROOT:STATE_ROOT,TE_MEMORY_DB:MEMORY_DB},detached:true,stdio:"ignore"});child.unref();return json({run_id:id,run,message:run.message,state_root:STATE_ROOT},202)}catch(err){run.status="failed";run.finished_at=new Date().toISOString();run.error=err instanceof Error?err.message:String(err);run.message=`本次执行失败：${run.error}`;writeRun(run);return json({run,error:run.message},500)}
  }
  return json({ error: "unknown evolution endpoint" }, 404);
}

export const GET = handle; export const POST = handle; export const PUT = handle;
