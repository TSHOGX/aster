// config.ts — every path and knob the pipeline uses, resolved once.
//
// Nothing here reads a secret. The engine used for agent nodes is selected by
// name only (`mock` for the offline demo, `pi` for a real local CLI); its
// credentials live in that CLI's own config, never in this repo.

import { resolve } from "node:path";

const root = resolve(import.meta.dir, "..");

function env(name: string, fallback: string): string {
  const v = process.env[name];
  return v && v.trim() !== "" ? v : fallback;
}

export const paths = {
  /** Project root. */
  root,
  /** Everything derived/mutable lands here; safe to delete and rebuild. */
  state: env("TE_STATE_ROOT", resolve(root, "state")),
  /** Versioned extractor artifacts (prompt/schema/gates/slice per lens version). */
  extractors: resolve(root, "extractors"),
  /** Frozen evaluation set for track A. */
  evalSet: resolve(root, "eval", "gold.json"),
  /** Raw trajectories to ingest (one JSONL per session). */
  trajectories: env("TE_TRAJECTORY_DIR", resolve(root, "fixtures", "trajectories")),
  /** KB seed carried into every snapshot. */
  seed: resolve(root, "seed"),
} as const;

export const files = {
  db: resolve(paths.state, "evolve.db"),
  /** Stand-in for the real warehouse (Doris). Read-only during a build. */
  warehouse: resolve(paths.state, "warehouse.db"),
  /** Override to stage into the platform's live agents-kb tree (cutover).
   *  Default is a private tree under TE_STATE_ROOT so a demo/cron cannot
   *  clobber the Go nightly snapshot. */
  kbRoot: env("TE_KB_ROOT", resolve(paths.state, "agents-kb")),
  /** Where a deterministic build drops a derived table before it is staged. */
  derived: resolve(paths.state, "derived"),
  reports: resolve(paths.state, "agents-kb", "reports"),
  runs: resolve(paths.state, "runs"),
} as const;

export interface EngineChoice {
  engine: string;
  model?: string;
}

/** Which CLI backs the agent nodes. `mock` spawns tools/mock-agent.ts, so the
 *  whole pipeline runs offline and deterministically; `pi` uses the real local
 *  pi CLI (must be on PATH and authenticated). */
export function engineChoice(argv: string[] = process.argv): EngineChoice {
  const flag = (name: string): string | undefined => {
    const i = argv.indexOf(`--${name}`);
    return i >= 0 ? argv[i + 1] : undefined;
  };
  const engine = flag("engine") ?? env("TE_ENGINE", "mock");
  const model = flag("model") ?? process.env.TE_MODEL;
  return { engine, model };
}

/** Hard caps that keep a cycle bounded regardless of what any agent proposes.
 *  These are PRE-GATES and BACKSTOPS (code), never judgment thresholds. */
export const limits = {
  /** A pain pattern must be seen in at least this many distinct sessions before
   *  it is worth an agent call. Cross-session evidence only. */
  minPainSessions: 2,
  /** Max derived rows a working-condition table may hold. Bigger than this and
   *  it is not a "small table an agent can read"; the build fails the health
   *  check instead of shipping something unusable. */
  maxTableRows: 5000,
  /** Row cap injected into every generated SELECT. */
  sqlRowCap: 20000,
  /** Ceiling on rounds of the skill draft->critic->revise loop. */
  maxSkillRounds: 2,
  /** Judge panel size on the high-stakes nodes. */
  judges: 3,
  /** Days without a KB hit before a memory/table is reported as retirement bait. */
  unusedDays: 90,
} as const;
