// graphs/extractor-evolve.ts — track A: the extraction method evolves.
//
//   0 reflect   single    read the four quadrants, pick what to change and why
//   A propose   barrier   one agent per target -> candidate versions (files!)
//   B shadow    pipeline  each candidate re-extracts the FROZEN eval sessions
//   C score     code      concept-overlap arithmetic vs the human gold set
//   D judge     single    do the numbers justify switching? promote / hold
//   E stage     code      write extractor_evals; activation stays a human act
//
// Three guardrails make this safe to run unattended:
//
//   - shadow output is written with shadow=1 and is never publishable, so a
//     candidate version cannot leak into the KB by winning its own A/B;
//   - the gold set is frozen and includes negative samples, so "extract more"
//     is not automatically rewarded;
//   - promote only records a recommendation. Flipping which version runs
//     tomorrow on every session is `cli ext activate <id>` — a human decision.

import { barrier, phase, pipeline } from "dynamic-workflow";
import { callNode } from "../agents/runner.ts";
import { db, nowISO } from "../db.ts";
import { quadrants } from "../observe/views.ts";
import { activeVersion, writeShadowVersion, type ExtractorVersion, type LensName } from "../extractors.ts";
import { goldSessions, scoreVersion, type Metrics } from "../eval.ts";
import { harvest } from "./harvest.ts";

export interface ExtractorCounts {
  targets: number;
  variants: number;
  scored: number;
  promoted: number;
  held: number;
  rejected: number;
}

export interface Proposal {
  versionID: number;
  lens: LensName;
  version: string;
  verdict: string;
  reason: string;
  metrics: Metrics;
  baseline: Metrics;
}

export async function evolveExtractors(runID: number): Promise<{ counts: ExtractorCounts; proposals: Proposal[] }> {
  const counts: ExtractorCounts = { targets: 0, variants: 0, scored: 0, promoted: 0, held: 0, rejected: 0 };
  const proposals: Proposal[] = [];

  const q = quadrants();
  // The gate: with no measured weakness there is nothing to evolve toward, and
  // proposing changes anyway is how a loop drifts.
  if (q.miss === 0 && q.noise === 0 && q.unused === 0 && q.unusable === 0) return { counts, proposals };

  const lenses: LensName[] = ["facts", "failure", "skill"];
  const current = lenses.map((l) => {
    const v = activeVersion(l);
    return { lens: l, version: v.version, slice: v.slice, gates: v.gates };
  });

  // ---- 0: reflect.
  const plan = await phase("ext-reflect", () =>
    callNode<{ targets: Array<{ lens: string; change_kind: string; hypothesis: string }>; budget_hint?: number }>({
      runID,
      node: "extreflect",
      ref: "plan",
      instructions:
        "下面是知识库的四象限质量指标与当前各透镜的配置。" +
        "miss=该有却没抽到；noise=抽了但被否/已失效；unused=进库却没人读；unusable=读到了但仍然失败。" +
        "选出最值得改的 1-2 个透镜，说明改什么（slice 切片 / prompt 提示 / gates 门控）以及为什么。",
      data: { quadrants: q, lenses: current },
      schema: {
        type: "object",
        required: ["targets"],
        properties: {
          targets: {
            type: "array",
            items: {
              type: "object",
              required: ["lens", "change_kind", "hypothesis"],
              properties: {
                lens: { type: "string" },
                change_kind: { type: "string", enum: ["slice", "prompt", "gates"] },
                hypothesis: { type: "string" },
              },
            },
          },
          budget_hint: { type: "integer" },
        },
      },
      cacheKey: `extreflect:${q.miss}-${q.noise}-${q.unused}-${q.unusable}`,
    })
  );

  const targets = (plan?.targets ?? []).filter((t) => lenses.includes(t.lens as LensName));
  counts.targets = targets.length;
  if (targets.length === 0) return { counts, proposals };

  // ---- A: propose. One agent per target, in parallel.
  const proposed = await phase("ext-propose", () =>
    barrier(
      targets.map((t) => async () => {
        const base = activeVersion(t.lens as LensName);
        const out = await callNode<{
          variants: Array<{
            version: string;
            change_kind: string;
            prompt_md: string;
            gates: Record<string, unknown>;
            slice: { include: string[]; max_events: number };
            rationale: string;
          }>;
        }>({
          runID,
          node: "extpropose",
          ref: `${t.lens}:${t.change_kind}`,
          instructions:
            "基于这个假设，提出候选抽取器版本。每个候选只改一个变量（切片/提示/门控之一），" +
            "这样 A/B 的结果才能归因。保留原有的接地约束与宁缺毋滥要求。",
          data: {
            lens: t.lens,
            change_kind: t.change_kind,
            hypothesis: t.hypothesis,
            current: {
              version: base.version,
              prompt_md: base.prompt_md,
              gates: base.gates,
              slice: base.slice,
            },
          },
          schema: {
            type: "object",
            required: ["variants"],
            properties: {
              variants: {
                type: "array",
                items: {
                  type: "object",
                  required: ["version", "prompt_md", "slice"],
                  properties: {
                    version: { type: "string" },
                    change_kind: { type: "string" },
                    prompt_md: { type: "string" },
                    gates: { type: "object" },
                    slice: {
                      type: "object",
                      required: ["include"],
                      properties: {
                        include: { type: "array", items: { type: "string" } },
                        max_events: { type: "integer" },
                      },
                    },
                    rationale: { type: "string" },
                  },
                },
              },
            },
          },
          cacheKey: `extpropose:${t.lens}:${t.change_kind}:${base.version}`,
        });
        return out ? { target: t, base, variants: out.variants } : null;
      })
    )
  );

  // ---- B + C + D: shadow-run, score, judge. Per variant, no barrier.
  const flat = proposed
    .filter((p): p is NonNullable<typeof p> => p !== null)
    .flatMap((p) => p.variants.map((v) => ({ ...p, variant: v })));
  counts.variants = flat.length;

  await phase("ext-shadow", () =>
    pipeline(flat, async (_prev: unknown, item: unknown) => {
      const f = item as (typeof flat)[number];
      const lens = f.target.lens as LensName;
      const sessions = goldSessions(lens);
      if (sessions.length === 0) return null;

      const shadowVersion: ExtractorVersion = writeShadowVersion({
        lens,
        version: f.variant.version,
        prompt_md: f.variant.prompt_md,
        schema: f.base.schema,
        gates: (f.variant.gates ?? f.base.gates) as ExtractorVersion["gates"],
        slice: {
          include: f.variant.slice.include,
          max_events: f.variant.slice.max_events ?? f.base.slice.max_events,
        },
        rationale: f.variant.rationale ?? f.target.hypothesis,
        parentID: f.base.id,
      });

      // Baseline and candidate are both run in shadow over the SAME frozen
      // sessions, so the comparison isn't contaminated by the live sweep.
      await harvest({ runID, override: { lens, version: f.base }, shadow: true, onlySessions: sessions, lenses: [lens] });
      await harvest({
        runID,
        override: { lens, version: shadowVersion },
        shadow: true,
        onlySessions: sessions,
        lenses: [lens],
      });

      const baseline = scoreVersion(lens, f.base.id);
      const metrics = scoreVersion(lens, shadowVersion.id);
      counts.scored++;

      const judged = await callNode<{ verdict: string; reason: string }>({
        runID,
        node: "extjudge",
        ref: `${lens}@${shadowVersion.version}`,
        instructions:
          "对比候选版本与当前版本在冻结评测集上的指标，判断是否值得切换。" +
          "召回提升但准确率明显下降不值得；契约违规一律不通过。指标是代码算出的，不要重新估算。",
        data: {
          lens,
          candidate: { version: shadowVersion.version, rationale: f.variant.rationale },
          metrics,
          baseline_metrics: baseline,
          samples: metrics.perSession.slice(0, 3),
        },
        schema: {
          type: "object",
          required: ["verdict", "reason"],
          properties: {
            verdict: { type: "string", enum: ["promote", "hold", "reject"] },
            reason: { type: "string" },
          },
        },
        cacheKey: `extjudge:${shadowVersion.id}:${metrics.matched}-${metrics.produced}`,
      });

      const verdict = judged?.verdict ?? "hold";
      if (verdict === "promote") counts.promoted++;
      else if (verdict === "reject") counts.rejected++;
      else counts.held++;

      db().run(
        `INSERT INTO extractor_evals
           (run_id, version_id, precision_pct, gap_recall, redundancy, contract_violations, judge_json, verdict, created_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        [
          runID,
          shadowVersion.id,
          metrics.precision_pct,
          metrics.gap_recall,
          metrics.redundancy,
          metrics.contract_violations,
          JSON.stringify({ judged, metrics, baseline }),
          verdict,
          nowISO(),
        ]
      );

      proposals.push({
        versionID: shadowVersion.id,
        lens,
        version: shadowVersion.version,
        verdict,
        reason: judged?.reason ?? "判官未给出结论，按 hold 处理",
        metrics,
        baseline,
      });
      return shadowVersion.version;
    })
  );

  return { counts, proposals };
}
