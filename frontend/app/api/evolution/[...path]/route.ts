import { NextRequest } from "next/server";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, readdirSync, renameSync, statSync, writeFileSync } from "node:fs";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const STATE_ROOT = process.env.EVOLUTION_STATE_ROOT ?? "";
const PROJECT_ROOT = process.env.EVOLUTION_PROJECT_ROOT ?? "";
const MEMORY_DB = process.env.EVOLUTION_MEMORY_DB ?? "";
const BUN = process.env.EVOLUTION_BUN ?? (PROJECT_ROOT ? `${PROJECT_ROOT}/node_modules/.bin/bun` : "");
const FRESH_MS = 4 * 60 * 60 * 1000;
// This optional external integration requires an explicit local allowlist.
const VIEWERS = new Set((process.env.EVOLUTION_VIEWERS ?? "").split(",").map((x) => x.trim()).filter(Boolean));

type Context = { params: Promise<{ path?: string[] }> };

function deploymentBlocked(path: string | undefined): boolean {
  if (!path) return false;
  try { statSync(path); return true; }
  catch (error) { return (error as NodeJS.ErrnoException).code !== "ENOENT"; }
}
function json(data: unknown, status = 200) { return Response.json(data, { status, headers: { "cache-control": "no-store" } }); }
function readJson(path: string, fallback: unknown = null) { try { return JSON.parse(readFileSync(path, "utf8")); } catch { return fallback; } }
function requireState() { return STATE_ROOT && PROJECT_ROOT && MEMORY_DB ? null : json({ error: "External evolution integration is not configured" }, 503); }
type RunRecord = { id:string; status:"queued"|"running"|"done"|"failed"|"skipped"; created_at:string; started_at?:string; finished_at?:string; message:string; error?:string; snapshot_digest?:string; runner_pid?:number };
function runDir() { return `${STATE_ROOT}/runs`; }
function runPath(id:string) { return `${runDir()}/${id}.json`; }
function writeRun(run:RunRecord) { mkdirSync(runDir(), { recursive:true }); const tmp=`${runPath(run.id)}.${process.pid}.tmp`; writeFileSync(tmp,JSON.stringify(run,null,2)); renameSync(tmp,runPath(run.id)); }
function pidAlive(pid?:number){if(!pid)return false;try{process.kill(pid,0);return true}catch{return false}}
function normalizeRun(run:RunRecord):RunRecord { const age=Date.now()-new Date(run.started_at??run.created_at).getTime(); if((run.status==="running"&&run.runner_pid&&!pidAlive(run.runner_pid))||(run.status==="queued"&&age>120000)){run.status="failed";run.finished_at=new Date().toISOString();run.error="后台任务意外终止";run.message="本次执行失败：后台任务意外终止，请重新运行。";writeRun(run)} return run }
function allRuns():RunRecord[] { try{return readdirSync(runDir()).filter(x=>x.endsWith(".json")).map(x=>readJson(`${runDir()}/${x}`) as RunRecord).filter(Boolean).map(normalizeRun).sort((a,b)=>String(b.created_at).localeCompare(String(a.created_at)))}catch{return []} }
function latestSuccessfulAt():Date|null { const completed=allRuns().find(x=>x.status==="done")?.finished_at; const pending=readJson(`${STATE_ROOT}/agents-kb/pending.json`) as {staged_at?:string}|null; const dates=[completed,pending?.staged_at].filter(Boolean).map(x=>new Date(String(x))).filter(x=>!Number.isNaN(x.getTime())); return dates.sort((a,b)=>b.getTime()-a.getTime())[0]??null; }
function emptyDetail() {
  return { condition_plans: [], memories: [], skills: [], tables: [] };
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
  // The integration stays unavailable without explicit private configuration.
  const parts = (await params).path ?? [];
  if (request.method !== "GET" && !(request.method === "POST" && parts[0] === "runs")) return json({ error: "approval write actions are disabled" }, 403);
  const stateError = requireState(); if (stateError) return stateError;
  const authError = await authorized(request); if (authError) return authError;

  if (request.method === "GET" && parts[0] === "pending") {
    const pending = readJson(`${STATE_ROOT}/agents-kb/pending.json`);
    const reportPath = pending ? `${STATE_ROOT}/agents-kb/reports/${String(new Date(pending.staged_at).toISOString().slice(0, 10))}.md` : "";
    const detailPath = pending ? `${STATE_ROOT}/agents-kb/reports/${String(new Date(pending.staged_at).toISOString().slice(0, 10))}.json` : "";
    return json({ pending, report: reportPath && existsSync(reportPath) ? readFileSync(reportPath, "utf8") : "No report is available.", detail: detailPath ? readJson(detailPath, emptyDetail()) : emptyDetail(), state_root: STATE_ROOT, read_only: true });
  }
  if (request.method === "GET" && parts[0] === "overview") {
    let current: string | null = null; try { current = readFileSync(`${STATE_ROOT}/agents-kb/current/.digest`, "utf8").trim(); } catch {}
    return json({ pending: readJson(`${STATE_ROOT}/agents-kb/pending.json`), current, state_root: STATE_ROOT });
  }
  if (request.method === "GET" && parts[0] === "runs") return json({ run:parts[1]?readJson(runPath(parts[1])):allRuns()[0]??null, latest_pending:readJson(`${STATE_ROOT}/agents-kb/pending.json`), read_only:true });
  if (request.method === "POST" && parts[0] === "runs") {
    const drain = process.env.HARNESS_DRAIN_FILE;
    const hold = process.env.HARNESS_BACKGROUND_HOLD_FILE;
    if (deploymentBlocked(drain) || deploymentBlocked(hold)) return json({ error: "deployment in progress" }, 503);
    if (!existsSync(BUN)) return json({ error: `bun not found: ${BUN}` }, 503);
    const active=allRuns().find(x=>x.status==="queued"||x.status==="running"); if(active)return json({run:active,message:"已有一轮自进化正在运行，请稍后查看进度。"},202);
    const freshAt=latestSuccessfulAt(); if(freshAt&&Date.now()-freshAt.getTime()<FRESH_MS){const id=crypto.randomUUID();const run:RunRecord={id,status:"skipped",created_at:new Date().toISOString(),finished_at:new Date().toISOString(),message:`当前已经使用最新数据（最近一次结果生成于 ${freshAt.toLocaleString("zh-CN",{timeZone:"Asia/Shanghai"})}），4 小时内无需重复执行。`};writeRun(run);return json({run,message:run.message});}
    const id=crypto.randomUUID();const run:RunRecord={id,status:"queued",created_at:new Date().toISOString(),message:"自进化任务已进入运行队列。"};writeRun(run);
    try{const child=spawn(BUN,["run","scripts/web-cycle-runner.ts",id],{cwd:PROJECT_ROOT,env:{...process.env,TE_STATE_ROOT:STATE_ROOT,TE_MEMORY_DB:MEMORY_DB},detached:true,stdio:"ignore"});child.unref();return json({run_id:id,run,message:run.message,state_root:STATE_ROOT},202)}catch(err){run.status="failed";run.finished_at=new Date().toISOString();run.error=err instanceof Error?err.message:String(err);run.message=`本次执行失败：${run.error}`;writeRun(run);return json({run,error:run.message},500)}
  }
  return json({ error: "unknown evolution endpoint" }, 404);
}

export const GET = handle; export const POST = handle; export const PUT = handle;
