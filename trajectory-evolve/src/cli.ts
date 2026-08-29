#!/usr/bin/env bun
// cli.ts — the operator surface.
//
// Each daily stage is its own command so cron can schedule them independently
// and a human can re-run one without re-running the day. `demo` chains them for
// a first look.
//
// Every agent-bearing stage runs inside dynamic-workflow's `runWorkflow`, which
// owns the run scope: the journal is flushed and the backend closed in a
// `finally`, even when the body throws. Passing `workflow` (stable) plus a
// generated run id (unique) is what lets a re-run reuse the resume cache while
// keeping one journal per execution.

// Must come first: it points dynamic-workflow's journal root at this project's
// state dir before that module is evaluated.
import "./bootstrap.ts";

import { mkdirSync } from "node:fs";
import { runWorkflow } from "dynamic-workflow";
import { files, paths } from "./config.ts";
import { db, finishRun, startRun } from "./db.ts";
import { engines } from "./engines/adapters.ts";
import { ingestDir, ingestMemoryDb } from "./observe/ingest.ts";
import { observeAll } from "./observe/observe.ts";
import { quadrants, queryPains, refreshQueryPatterns, retrievalGaps } from "./observe/views.ts";
import { activateVersion, listVersions, syncFromDisk } from "./extractors.ts";
import { harvest } from "./graphs/harvest.ts";
import { evolveKB } from "./graphs/kb-evolve.ts";
import { evolveTables } from "./graphs/table-evolve.ts";
import { evolveExtractors, type Proposal } from "./graphs/extractor-evolve.ts";
import { approve, diff, listSnapshot, pendingInfo, readCurrentDigest, reject, seedIfEmpty, stage } from "./kb/snapshot.ts";
import { buildWarehouse } from "./warehouse.ts";
import { writeReport } from "./report.ts";
import { runCycleForeground } from "./jobs.ts";
import { opStatus, parseTrack } from "./ops.ts";

const argv = process.argv.slice(2);
const cmd = argv[0] ?? "help";
const sub = argv[1];

function flag(name: string): string | undefined {
  const i = argv.indexOf(`--${name}`);
  return i >= 0 ? argv[i + 1] : undefined;
}

function today(): string {
  return flag("day") ?? new Date().toISOString().slice(0, 10);
}

/** Shared run options: the mock (or chosen) engine adapter, one workflow name so
 *  the resume cache is shared across runs of the same track. */
function runOpts(workflow: string) {
  return { workflow, engines, opencodePool: false as const, resume: argv.includes("--resume") };
}

function line(s = ""): void {
  console.log(s);
}

async function main(): Promise<void> {
  mkdirSync(files.kbRoot, { recursive: true });

  switch (cmd) {
    case "init": {
      db();
      const wh = buildWarehouse();
      const loaded = syncFromDisk();
      const seeded = seedIfEmpty();
      line(`数据库：${files.db}`);
      line(`模拟数仓：${files.warehouse}（bms_telemetry ${wh.telemetry} 行，charge_session ${wh.charge} 行）`);
      line(`抽取器版本载入：${loaded} 个`);
      line(`KB 种子快照：${seeded ?? "已存在"}`);
      break;
    }

    case "ingest": {
      const memoryDb = flag("memory-db") ?? process.env.TE_MEMORY_DB;
      const dir = flag("dir") ?? paths.trajectories;
      const res = memoryDb ? ingestMemoryDb(memoryDb) : ingestDir(dir);
      const fresh = res.filter((r) => !r.skipped);
      line(memoryDb ? `memory.db：${memoryDb}` : `轨迹目录：${dir}`);
      line(`会话总数 ${res.length}，本次解析 ${fresh.length}，指纹未变跳过 ${res.length - fresh.length}`);
      for (const r of fresh) line(`  ${r.sessionID}: ${r.events} 个事件`);
      break;
    }

    case "observe": {
      const runID = startRun("observe");
      const res = observeAll();
      const patterns = refreshQueryPatterns();
      const q = quadrants();
      finishRun(runID, {
        sessions: res.length,
        queries: res.reduce((n, r) => n + r.queries, 0),
        kb_touches: res.reduce((n, r) => n + r.kbTouches, 0),
        gaps: res.reduce((n, r) => n + r.gaps, 0),
        patterns,
      });
      line(`观测 ${res.length} 个会话，查询 ${res.reduce((n, r) => n + r.queries, 0)} 条，指纹 ${patterns} 个`);
      line(`四象限：miss=${q.miss} noise=${q.noise} unused=${q.unused} unusable=${q.unusable}`);
      break;
    }

    case "pains": {
      const pains = queryPains();
      line("查询痛点（跨会话）:");
      for (const p of pains)
        line(
          `  ${p.key.padEnd(34)} 会话=${p.sessions} 次数=${p.occurrences} 错误率=${(p.err_rate * 100).toFixed(0)}% 平均=${Math.round(p.avg_latency)}ms`
        );
      if (!pains.length) line("  （无）");
      line("");
      line("检索缺口:");
      for (const g of retrievalGaps()) line(`  [${g.kind}] ${g.scope_hint} — ${g.sessions} 个会话`);
      const q = quadrants();
      line("");
      line(`四象限：miss=${q.miss} noise=${q.noise} unused=${q.unused} unusable=${q.unusable}`);
      break;
    }

    case "harvest": {
      const runID = startRun("harvest");
      const counts = await runWorkflow(runOpts("harvest"), () => harvest({ runID }));
      finishRun(runID, counts as unknown as Record<string, number>);
      line(
        `抽取完成：会话 ${counts.sessions}，门控拦下 ${counts.gated}，LLM 调用 ${counts.calls}，候选记忆 ${counts.memories}，候选技能 ${counts.skills}`
      );
      break;
    }

    case "evolve": {
      const track = sub ?? "all";
      const allCounts: Record<string, Record<string, number>> = {};
      let proposals: Proposal[] = [];

      if (track === "kb" || track === "all") {
        const runID = startRun("kb");
        const counts = await runWorkflow(runOpts("kb-evolve"), () => evolveKB(runID));
        finishRun(runID, counts as unknown as Record<string, number>);
        allCounts.kb = counts as unknown as Record<string, number>;
        line(
          `KB：候选 ${counts.candidates} → 块 ${counts.blocks} / 簇 ${counts.clusters}（吸收 ${counts.absorbed}），` +
            `失效 ${counts.verified_stale} 已修复 ${counts.verified_fixed}，promote ${counts.promoted} hold ${counts.held} reject ${counts.rejected} veto ${counts.vetoed}`
        );
      }
      if (track === "tables" || track === "all") {
        const runID = startRun("tables");
        const counts = await runWorkflow(runOpts("table-evolve"), () => evolveTables(runID));
        finishRun(runID, counts as unknown as Record<string, number>);
        allCounts.tables = counts as unknown as Record<string, number>;
        line(
          `工况表：痛点 ${counts.pains} → 值得建表 ${counts.mined}，设计 ${counts.designed}，构建通过 ${counts.built}（体检拒绝 ${counts.rejected_build}），` +
            `回放有收益 ${counts.useful} 无收益 ${counts.no_gain}，对抗否决 ${counts.refuted}，critic 否决 ${counts.vetoed}，暂存 ${counts.staged}`
        );
      }
      if (track === "extractors" || track === "all") {
        const runID = startRun("extractors");
        const out = await runWorkflow(runOpts("extractor-evolve"), () => evolveExtractors(runID));
        finishRun(runID, out.counts as unknown as Record<string, number>);
        allCounts.extractors = out.counts as unknown as Record<string, number>;
        proposals = out.proposals;
        line(
          `抽取器：目标 ${out.counts.targets}，候选版本 ${out.counts.variants}，评分 ${out.counts.scored}，` +
            `promote ${out.counts.promoted} hold ${out.counts.held} reject ${out.counts.rejected}`
        );
        for (const p of proposals)
          line(
            `  ${p.lens} ${p.version} (id=${p.versionID}) ${p.verdict}：召回 ${p.baseline.gap_recall.toFixed(2)} → ${p.metrics.gap_recall.toFixed(2)}，准确率 ${p.baseline.precision_pct.toFixed(2)} → ${p.metrics.precision_pct.toFixed(2)}`
          );
      }

      const st = stage(startRun("stage"));
      line(
        st.changed
          ? `已暂存快照 ${st.digest}（facts ${st.counts.facts} / skills ${st.counts.skills} / tables ${st.counts.tables}，PII 拦下 ${st.counts.dropped}）`
          : `内容与线上一致（${st.digest}），无需审批`
      );
      const report = writeReport({ day: today(), counts: allCounts, proposals });
      line(`日报：${report}`);
      break;
    }

    case "kb": {
      if (sub === "status") {
        const p = pendingInfo();
        line(`live：${readCurrentDigest() ?? "无"}`);
        line(`pending：${p?.digest ?? "无"}`);
        if (p) line(`暂存于 ${p.staged_at}，counts=${JSON.stringify(p.counts)}`);
        break;
      }
      if (sub === "diff") {
        const d = diff();
        line(`${d.from ?? "无"} → ${d.to ?? "无"}`);
        for (const e of d.entries) line(`  ${e.change.padEnd(9)} ${e.path}`);
        if (!d.entries.length) line("  （无差异）");
        break;
      }
      if (sub === "ls") {
        const digest = argv[2] ?? readCurrentDigest();
        if (!digest) {
          line("没有快照");
          break;
        }
        for (const f of listSnapshot(digest)) line(`  ${f}`);
        break;
      }
      if (sub === "approve") {
        const r = approve(argv[2] ?? "");
        line(r.message);
        process.exitCode = r.ok ? 0 : 1;
        break;
      }
      if (sub === "reject") {
        const r = reject(argv[2] ?? "", flag("reason") ?? "未说明");
        line(r.message);
        process.exitCode = r.ok ? 0 : 1;
        break;
      }
      line("用法：cli kb status|diff|ls [digest]|approve <digest>|reject <digest> --reason ...");
      break;
    }

    case "ext": {
      if (sub === "list") {
        for (const v of listVersions())
          line(
            `  id=${String(v.id).padStart(3)} ${v.lens.padEnd(8)} ${v.version.padEnd(5)} ${v.status.padEnd(7)} slice=${v.slice.include.join("+")}`
          );
        break;
      }
      if (sub === "activate") {
        const v = activateVersion(Number(argv[2]));
        line(`已切换：${v.lens} → ${v.version}（slice=${v.slice.include.join("+")}）`);
        break;
      }
      line("用法：cli ext list|activate <id>");
      break;
    }

    case "report": {
      const path = writeReport({ day: today(), counts: {} });
      line(`日报：${path}`);
      break;
    }

    case "demo": {
      line("=== 0. init ==========================================================");
      db();
      const wh = buildWarehouse();
      syncFromDisk();
      seedIfEmpty();
      line(`模拟数仓 ${wh.telemetry} 行遥测；KB 种子已就位`);

      line("");
      line("=== 1. ingest（零 LLM）===============================================");
      const ing = ingestDir(paths.trajectories);
      line(`${ing.length} 个会话，${ing.reduce((n, r) => n + r.events, 0)} 个事件`);

      line("");
      line("=== 2. observe（零 LLM）==============================================");
      const obs = observeAll();
      const patterns = refreshQueryPatterns();
      const q0 = quadrants();
      line(
        `查询 ${obs.reduce((n, r) => n + r.queries, 0)} 条 → 指纹 ${patterns} 个；KB 触达 ${obs.reduce((n, r) => n + r.kbTouches, 0)}（未命中 ${obs.reduce((n, r) => n + r.kbMisses, 0)}）`
      );
      for (const p of queryPains())
        line(`  痛点 ${p.key.padEnd(34)} 会话=${p.sessions} 次数=${p.occurrences}`);
      line(`四象限：miss=${q0.miss} noise=${q0.noise} unused=${q0.unused} unusable=${q0.unusable}`);

      line("");
      line("=== 3. harvest（三透镜，带硬门控）===================================");
      const hRun = startRun("harvest");
      const hc = await runWorkflow(runOpts("harvest"), () => harvest({ runID: hRun }));
      finishRun(hRun, hc as unknown as Record<string, number>);
      line(`门控拦下 ${hc.gated}，LLM 调用 ${hc.calls} → 候选记忆 ${hc.memories}，候选技能 ${hc.skills}`);

      line("");
      line("=== 4. evolve kb（cluster→verify→curate→critic）====================");
      const kRun = startRun("kb");
      const kc = await runWorkflow(runOpts("kb-evolve"), () => evolveKB(kRun));
      finishRun(kRun, kc as unknown as Record<string, number>);
      line(
        `候选 ${kc.candidates} → 簇 ${kc.clusters}（吸收 ${kc.absorbed}），promote ${kc.promoted} / hold ${kc.held} / reject ${kc.rejected} / veto ${kc.vetoed}`
      );

      line("");
      line("=== 5. evolve tables（mine→design→build→replay→panel→refute）=======");
      const tRun = startRun("tables");
      const tc = await runWorkflow(runOpts("table-evolve"), () => evolveTables(tRun));
      finishRun(tRun, tc as unknown as Record<string, number>);
      line(
        `痛点 ${tc.pains} → 建表 ${tc.built}（体检拒绝 ${tc.rejected_build}），有收益 ${tc.useful} / 无收益 ${tc.no_gain}，暂存 ${tc.staged}`
      );

      line("");
      line("=== 6. evolve extractors（shadow A/B → 人工切换）====================");
      const eRun = startRun("extractors");
      const ec = await runWorkflow(runOpts("extractor-evolve"), () => evolveExtractors(eRun));
      finishRun(eRun, ec.counts as unknown as Record<string, number>);
      for (const p of ec.proposals)
        line(
          `  ${p.lens} ${p.version} (id=${p.versionID}) → ${p.verdict}：召回 ${p.baseline.gap_recall.toFixed(2)} → ${p.metrics.gap_recall.toFixed(2)}，准确率 ${p.baseline.precision_pct.toFixed(2)} → ${p.metrics.precision_pct.toFixed(2)}`
        );
      if (!ec.proposals.length) line("  （四象限无缺陷，未提议新版本）");

      line("");
      line("=== 7. 人工切换新版本后复跑（闭环）=================================");
      const promoted = ec.proposals.find((p) => p.verdict === "promote");
      if (!promoted) line("无 promote 提议，跳过（线上仍跑当前 active 版本）");
      else {
        const v = activateVersion(promoted.versionID);
        line(`ext activate ${promoted.versionID} → ${v.lens} ${v.version}（slice=${v.slice.include.join("+")}）`);
        const h2Run = startRun("harvest");
        const hc2 = await runWorkflow(runOpts("harvest"), () => harvest({ runID: h2Run }));
        finishRun(h2Run, hc2 as unknown as Record<string, number>);
        const k2Run = startRun("kb");
        const kc2 = await runWorkflow(runOpts("kb-evolve"), () => evolveKB(k2Run));
        finishRun(k2Run, kc2 as unknown as Record<string, number>);
        line(
          `复跑：新候选 ${hc2.memories} → 簇 ${kc2.clusters}（吸收 ${kc2.absorbed}），promote ${kc2.promoted} / hold ${kc2.held}`
        );
      }

      line("");
      line("=== 8. stage + 审批 ==================================================");
      const st = stage(startRun("stage"));
      line(st.changed ? `暂存 ${st.digest}` : `与线上一致 ${st.digest}`);
      const d = diff();
      for (const e of d.entries.slice(0, 24)) line(`  ${e.change.padEnd(9)} ${e.path}`);
      if (st.changed) {
        const r = approve(st.digest);
        line(r.message);
      }
      const rep = writeReport({
        day: today(),
        counts: {
          harvest: hc as unknown as Record<string, number>,
          kb: kc as unknown as Record<string, number>,
          tables: tc as unknown as Record<string, number>,
          extractors: ec.counts as unknown as Record<string, number>,
        },
        proposals: ec.proposals,
      });
      line("");
      line(`日报：${rep}`);
      line(`KB 快照：${files.kbRoot}/current`);
      line(`journal：${files.runs}`);
      break;
    }

    case "status": {
      line(JSON.stringify(opStatus(), null, 2));
      break;
    }

    case "cycle": {
      const job = await runCycleForeground({
        track: parseTrack(sub ?? "all"),
        ingest: !argv.includes("--no-ingest"),
        observe: !argv.includes("--no-observe"),
        harvest: !argv.includes("--no-harvest"),
      });
      line(`job ${job.id} ${job.status}${job.error ? ` — ${job.error}` : ""}`);
      if (job.status === "done") {
        const result = job.result as { evolve?: { stage?: { digest?: string; changed?: boolean } } };
        const st = result?.evolve?.stage;
        if (st?.digest) line(st.changed ? `暂存 ${st.digest}（未发布）` : `与线上一致 ${st.digest}`);
      }
      if (job.status === "error") process.exit(1);
      break;
    }

    case "mcp": {
      const { serveHttp } = await import("./mcp/http.ts");
      const { serveStdio } = await import("./mcp/stdio.ts");
      const httpI = argv.indexOf("--http");
      if (httpI >= 0) {
        const port = Number(argv[httpI + 1] || process.env.TE_MCP_PORT || 8104);
        const hostname = process.env.TE_MCP_HOST ?? "0.0.0.0";
        const { url } = serveHttp(port, hostname);
        process.stderr.write(`trajectory-evolve MCP http ${url}\n`);
      } else {
        await serveStdio();
      }
      break;
    }

    default:
      line(`用法：cli <command>

  init                     建库、生成模拟数仓、载入抽取器版本、写入 KB 种子快照
  ingest [--dir <path>] [--memory-db <path>]
                           轨迹 JSONL 或平台 memory.db → trajectory_events（零 LLM）
  observe                  → 观测表 + 查询指纹 + 痛点/缺口/四象限（零 LLM）
  pains                    打印痛点、检索缺口、四象限
  status                   只读：痛点 / 四象限 / live+pending digest（MCP evolve_status 同源）
  harvest                  三透镜抽取（硬门控 + 版本化抽取器）
  evolve [kb|tables|extractors|all]
                           进化图，跑完后暂存快照并写日报
  cycle [kb|tables|extractors|all]
                           带锁的完整周期（ingest+observe+harvest+evolve）；cron 入口，只暂存
  kb status|diff|ls|approve <digest>|reject <digest> --reason ...
  ext list|activate <id>   抽取器版本：查看 / 切换 active
  report [--day YYYY-MM-DD]
  mcp [--http PORT]        MCP 服务（stdio 或 streamable-http）。无 approve 工具
  demo                     一键跑完整条流水线（离线 mock 引擎）

  公共参数：--engine mock|pi   --model <name>   --resume
  环境：TE_ENGINE TE_MODEL TE_RESUME=1 TE_MEMORY_DB TE_KB_ROOT TE_STATE_ROOT TE_MCP_PORT`);
      break;
  }
}

await main();
