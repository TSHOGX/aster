// ops.ts — the one implementation every trigger shares.
//
// CLI, MCP, and cron are three doors into the same functions. That is what
// lets the agent architecture change (pi today, something else tomorrow)
// without rewriting the evolution graph: the protocol in front can change,
// the ops cannot. Every write path STAGES; approve / extractor-activate stay
// on the expert CLI and are never imported by the MCP tool table.

import { mkdirSync } from "node:fs";
import { runWorkflow } from "dynamic-workflow";
import { files, paths } from "./config.ts";
import { db, finishRun, startRun } from "./db.ts";
import { engines } from "./engines/adapters.ts";
import { ingestDir, ingestMemoryDb, type IngestResult } from "./observe/ingest.ts";
import { observeAll } from "./observe/observe.ts";
import { quadrants, queryPains, refreshQueryPatterns, retrievalGaps } from "./observe/views.ts";
import { activateVersion, listVersions, syncFromDisk } from "./extractors.ts";
import { harvest, type HarvestCounts } from "./graphs/harvest.ts";
import { evolveKB, type KBCounts } from "./graphs/kb-evolve.ts";
import { evolveTables, type TableCounts } from "./graphs/table-evolve.ts";
import { evolveExtractors, type ExtractorCounts, type Proposal } from "./graphs/extractor-evolve.ts";
import {
  approve,
  diff,
  listSnapshot,
  pendingInfo,
  readCurrentDigest,
  reject,
  seedIfEmpty,
  stage,
  type DiffEntry,
  type StageResult,
} from "./kb/snapshot.ts";
import { buildWarehouse } from "./warehouse.ts";
import { writeReport } from "./report.ts";

export type Track = "kb" | "tables" | "extractors" | "all";

export function parseTrack(s: string | undefined): Track {
  const t = (s ?? "all").toLowerCase();
  if (t === "kb" || t === "tables" || t === "extractors" || t === "all") return t;
  throw new Error(`unknown track "${s}" — use kb | tables | extractors | all`);
}

export function workflowOpts(workflow: string) {
  return {
    workflow,
    engines,
    opencodePool: false as const,
    resume: process.env.TE_RESUME === "1" || process.argv.includes("--resume"),
  };
}

export function ensureState(): void {
  mkdirSync(files.kbRoot, { recursive: true });
  db();
}

export interface InitResult {
  db: string;
  warehouse: { telemetry: number; charge: number; models: number };
  extractors_loaded: number;
  seed_digest: string | null;
}

export function opInit(): InitResult {
  ensureState();
  const wh = buildWarehouse();
  const loaded = syncFromDisk();
  const seeded = seedIfEmpty();
  return { db: files.db, warehouse: wh, extractors_loaded: loaded, seed_digest: seeded };
}

export interface IngestSummary {
  dir: string;
  sessions: number;
  parsed: number;
  skipped: number;
  events: number;
  fresh: IngestResult[];
}

export function opIngest(dir = paths.trajectories): IngestSummary {
  ensureState();
  const memoryDb = process.env.TE_MEMORY_DB;
  const res = memoryDb ? ingestMemoryDb(memoryDb) : ingestDir(dir);
  const fresh = res.filter((r) => !r.skipped);
  return {
    dir: memoryDb ?? dir,
    sessions: res.length,
    parsed: fresh.length,
    skipped: res.length - fresh.length,
    events: fresh.reduce((n, r) => n + r.events, 0),
    fresh,
  };
}

export interface ObserveSummary {
  sessions: number;
  queries: number;
  kb_touches: number;
  kb_misses: number;
  gaps: number;
  patterns: number;
  quadrants: ReturnType<typeof quadrants>;
}

export function opObserve(): ObserveSummary {
  ensureState();
  const runID = startRun("observe");
  const res = observeAll();
  const patterns = refreshQueryPatterns();
  const q = quadrants();
  const summary: ObserveSummary = {
    sessions: res.length,
    queries: res.reduce((n, r) => n + r.queries, 0),
    kb_touches: res.reduce((n, r) => n + r.kbTouches, 0),
    kb_misses: res.reduce((n, r) => n + r.kbMisses, 0),
    gaps: res.reduce((n, r) => n + r.gaps, 0),
    patterns,
    quadrants: q,
  };
  finishRun(runID, {
    sessions: summary.sessions,
    queries: summary.queries,
    kb_touches: summary.kb_touches,
    gaps: summary.gaps,
    patterns,
  });
  return summary;
}

export interface StatusSnapshot {
  live: string | null;
  pending: ReturnType<typeof pendingInfo>;
  quadrants: ReturnType<typeof quadrants>;
  pains: ReturnType<typeof queryPains>;
  gaps: ReturnType<typeof retrievalGaps>;
  extractors: Array<{
    id: number;
    lens: string;
    version: string;
    status: string;
    slice: string[];
  }>;
}

/** Cheap, zero-LLM picture of "what would a cycle do". The skill tells the
 *  agent to call this BEFORE kicking off a run, so a quiet day costs a tool
 *  call rather than a graph. */
export function opStatus(): StatusSnapshot {
  ensureState();
  return {
    live: readCurrentDigest(),
    pending: pendingInfo(),
    quadrants: quadrants(),
    pains: queryPains(),
    gaps: retrievalGaps(),
    extractors: listVersions().map((v) => ({
      id: v.id,
      lens: v.lens,
      version: v.version,
      status: v.status,
      slice: v.slice.include,
    })),
  };
}

export async function opHarvest(): Promise<HarvestCounts> {
  ensureState();
  const runID = startRun("harvest");
  const counts = await runWorkflow(workflowOpts("harvest"), () => harvest({ runID }));
  finishRun(runID, counts as unknown as Record<string, number>);
  return counts;
}

export interface EvolveResult {
  track: Track;
  harvest?: HarvestCounts;
  kb?: KBCounts;
  tables?: TableCounts;
  extractors?: ExtractorCounts;
  proposals?: Proposal[];
  stage: StageResult;
  report: string;
  note: string;
}

export async function opEvolve(track: Track, opts: { harvestFirst?: boolean } = {}): Promise<EvolveResult> {
  ensureState();
  const out: EvolveResult = {
    track,
    stage: { digest: "", changed: false, counts: { facts: 0, skills: 0, tables: 0, dropped: 0 } },
    report: "",
    note: "snapshot is STAGED, not live. An expert must `cli kb approve <digest>` before new sessions mount it. The calling session stays on its current snapshot.",
  };
  const allCounts: Record<string, Record<string, number>> = {};

  if (opts.harvestFirst) {
    out.harvest = await opHarvest();
    allCounts.harvest = out.harvest as unknown as Record<string, number>;
  }

  if (track === "kb" || track === "all") {
    const runID = startRun("kb");
    out.kb = await runWorkflow(workflowOpts("kb-evolve"), () => evolveKB(runID));
    finishRun(runID, out.kb as unknown as Record<string, number>);
    allCounts.kb = out.kb as unknown as Record<string, number>;
  }
  if (track === "tables" || track === "all") {
    const runID = startRun("tables");
    out.tables = await runWorkflow(workflowOpts("table-evolve"), () => evolveTables(runID));
    finishRun(runID, out.tables as unknown as Record<string, number>);
    allCounts.tables = out.tables as unknown as Record<string, number>;
  }
  let proposals: Proposal[] = [];
  if (track === "extractors" || track === "all") {
    const runID = startRun("extractors");
    const r = await runWorkflow(workflowOpts("extractor-evolve"), () => evolveExtractors(runID));
    finishRun(runID, r.counts as unknown as Record<string, number>);
    out.extractors = r.counts;
    out.proposals = r.proposals;
    proposals = r.proposals;
    allCounts.extractors = r.counts as unknown as Record<string, number>;
  }

  out.stage = stage(startRun("stage"));
  out.report = writeReport({
    day: new Date().toISOString().slice(0, 10),
    counts: allCounts,
    proposals,
  });
  return out;
}

export function opKbStatus(): { live: string | null; pending: ReturnType<typeof pendingInfo> } {
  ensureState();
  return { live: readCurrentDigest(), pending: pendingInfo() };
}

export function opKbDiff(): { from: string | null; to: string | null; entries: DiffEntry[] } {
  ensureState();
  return diff();
}

export function opKbLs(digest?: string): { digest: string; files: string[] } {
  ensureState();
  const d = digest || readCurrentDigest();
  if (!d) throw new Error("no snapshot");
  return { digest: d, files: listSnapshot(d) };
}

/** Expert-only. Not registered as an MCP tool. */
export function opKbApprove(digest: string) {
  ensureState();
  return approve(digest);
}

/** Expert-only. Not registered as an MCP tool. */
export function opKbReject(digest: string, reason: string) {
  ensureState();
  return reject(digest, reason);
}

/** Expert-only. Not registered as an MCP tool. */
export function opExtActivate(id: number) {
  ensureState();
  const v = activateVersion(id);
  return { id: v.id, lens: v.lens, version: v.version, slice: v.slice.include };
}
