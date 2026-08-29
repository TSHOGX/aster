// eval.ts — scoring an extractor version against the frozen gold set.
//
// The scoring is ARITHMETIC, not a judgment. An agent grading its own extractor
// is how a self-evolving loop talks itself into a regression, so the numbers here
// come from concept-key overlap against a human-frozen expectation, and the only
// thing an agent decides afterwards is whether the numbers justify a switch.
//
// The gold set includes negative items on purpose: "this session should yield
// nothing" is a correct answer, and without such items every evolution step is
// rewarded for extracting more.

import { readFileSync } from "node:fs";
import { paths } from "./config.ts";
import { conceptOf } from "./concepts.ts";
import { listMemories } from "./memories.ts";
import type { LensName } from "./extractors.ts";

export interface GoldItem {
  session_id: string;
  lens: LensName;
  /** Concepts a correct extraction must surface. Empty = negative sample. */
  gold: Array<{ concept: string; note?: string }>;
}

export interface GoldSet {
  frozen_at: string;
  curated_by: string;
  items: GoldItem[];
}

export function loadGold(): GoldSet {
  return JSON.parse(readFileSync(paths.evalSet, "utf8")) as GoldSet;
}

export function goldSessions(lens: LensName): string[] {
  return loadGold()
    .items.filter((i) => i.lens === lens)
    .map((i) => i.session_id);
}

export interface Metrics {
  /** Share of produced items that were expected. Punishes noise. */
  precision_pct: number;
  /** Share of expected items that were produced. Punishes misses. */
  gap_recall: number;
  /** Share of produced items that restate a concept already produced. */
  redundancy: number;
  /** Items violating the output contract (here: no usable trigger condition). */
  contract_violations: number;
  produced: number;
  expected: number;
  matched: number;
  /** Per-session detail, so a regression can be traced to one trace. */
  perSession: Array<{ session_id: string; expected: string[]; produced: string[]; missed: string[]; extra: string[] }>;
}

/** Score the shadow output of one version over the gold sessions of one lens. */
export function scoreVersion(lens: LensName, versionID: number): Metrics {
  const gold = loadGold().items.filter((i) => i.lens === lens);
  const rows = listMemories({ shadow: true, versionID });

  let produced = 0;
  let expected = 0;
  let matched = 0;
  let redundant = 0;
  let violations = 0;
  const perSession: Metrics["perSession"] = [];

  for (const item of gold) {
    const mine = rows.filter((r) => (JSON.parse(r.evidence_json) as string[]).includes(item.session_id));
    const concepts = mine.map((m) => conceptOf(m.statement));
    const distinct = new Set(concepts);
    redundant += concepts.length - distinct.size;
    violations += mine.filter((m) => m.when_to_apply.trim() === "").length;

    const want = new Set(item.gold.map((g) => g.concept));
    const hit = [...distinct].filter((c) => want.has(c));
    produced += distinct.size;
    expected += want.size;
    matched += hit.length;

    perSession.push({
      session_id: item.session_id,
      expected: [...want],
      produced: [...distinct],
      missed: [...want].filter((c) => !distinct.has(c)),
      extra: [...distinct].filter((c) => !want.has(c)),
    });
  }

  return {
    // With nothing expected and nothing produced, precision is 1: correctly
    // staying silent on a negative sample is a pass, not a division by zero.
    precision_pct: produced === 0 ? (expected === 0 ? 1 : 0) : matched / produced,
    gap_recall: expected === 0 ? 1 : matched / expected,
    redundancy: produced === 0 ? 0 : redundant / (produced + redundant),
    contract_violations: violations,
    produced,
    expected,
    matched,
    perSession,
  };
}
