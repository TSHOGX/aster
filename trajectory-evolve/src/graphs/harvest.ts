// graphs/harvest.ts — node A: sessions -> candidate knowledge.
//
// Shape: `pipeline` over sessions. Sessions are independent, so there is no
// barrier between them — session 7 can be on its skill lens while session 2 is
// still on facts, and wall-clock is the slowest single session rather than the
// sum of per-lens maxima.
//
// Every lens call is preceded by a code gate (turn count, failure signal). An
// unchanged or uninteresting session costs zero tokens, which is what keeps the
// daily bill proportional to new knowledge instead of to traffic.

import { pipeline, phase } from "dynamic-workflow";
import { callNode } from "../agents/runner.ts";
import {
  activeVersion,
  gatePasses,
  sliceEvents,
  type ExtractorVersion,
  type LensName,
} from "../extractors.ts";
import { harvestTargets, type HarvestTarget } from "../observe/views.ts";
import { upsertCandidate, upsertSkillCandidate, type CandidateMemory, type CandidateSkill } from "../memories.ts";

export interface HarvestCounts {
  sessions: number;
  gated: number;
  calls: number;
  memories: number;
  skills: number;
}

export interface HarvestOptions {
  runID: number;
  /** Override the active version for one lens (track A's shadow runs). */
  override?: { lens: LensName; version: ExtractorVersion };
  /** Shadow output never reaches the KB; it exists only to be scored. */
  shadow?: boolean;
  /** Restrict to these sessions (the frozen eval set, for track A). */
  onlySessions?: string[];
  /** Which lenses to run. Defaults to all three. */
  lenses?: LensName[];
}

/** Run the lenses over every eligible session. */
export async function harvest(opts: HarvestOptions): Promise<HarvestCounts> {
  const counts: HarvestCounts = { sessions: 0, gated: 0, calls: 0, memories: 0, skills: 0 };
  let targets = harvestTargets();
  if (opts.onlySessions) targets = targets.filter((t) => opts.onlySessions!.includes(t.session_id));
  const lenses = opts.lenses ?? (["facts", "failure", "skill"] as LensName[]);

  const versionFor = (lens: LensName): ExtractorVersion =>
    opts.override?.lens === lens ? opts.override.version : activeVersion(lens);

  await phase("harvest", () =>
    pipeline(
      targets,
      // One stage per lens. A stage that throws drops only that session.
      async (_prev: unknown, item: unknown) => {
        const t = item as HarvestTarget;
        counts.sessions++;
        for (const lens of lenses) {
          const v = versionFor(lens);
          if (!gatePasses(v.gates, t)) {
            counts.gated++;
            continue;
          }
          const events = sliceEvents(t.session_id, v.slice);
          if (events.length === 0) {
            counts.gated++;
            continue;
          }
          counts.calls++;
          if (lens === "skill") {
            const out = await callNode<{ skills: CandidateSkill[] }>({
              runID: opts.runID,
              node: "skillinduce",
              ref: `${t.session_id}/${lens}@${v.version}`,
              instructions: v.prompt_md,
              data: { lens, slice_events: events, gates: v.gates },
              schema: v.schema,
              cacheKey: `skill:${t.session_id}:${v.id}`,
            });
            // A null answer is a HOLD, never a "nothing to extract": the session
            // stays eligible for the next cycle instead of being marked done.
            if (!out) continue;
            for (const sk of out.skills ?? []) {
              if (upsertSkillCandidate(sk, t.session_id) !== null) counts.skills++;
            }
            continue;
          }
          const out = await callNode<{ memories: CandidateMemory[] }>({
            runID: opts.runID,
            node: "extract",
            ref: `${t.session_id}/${lens}@${v.version}`,
            instructions: v.prompt_md,
            data: { lens, slice_events: events, gates: v.gates },
            schema: v.schema,
            cacheKey: `extract:${t.session_id}:${v.id}`,
          });
          if (!out) continue;
          for (const m of out.memories ?? []) {
            if (upsertCandidate(m, t.session_id, v.id, opts.shadow) !== null) counts.memories++;
          }
        }
        return t.session_id;
      }
    )
  );

  return counts;
}
