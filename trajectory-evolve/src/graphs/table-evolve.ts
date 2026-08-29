// graphs/table-evolve.ts — track B: query pains -> small lookup tables.
//
//   0 gate       code      no new cross-session pain -> the cycle ends free
//   A mine       barrier   one agent per pain cluster: intent + archetype
//   B design     pipeline  schema + a source SELECT over columns that exist
//   C drybuild   code      run it read-only, health-check it, or reject it
//   D replay     barrier   the same historical question, with and without the table
//   E panel      barrier   three judges on the replay delta, majority verdict
//   F refute     recipe    adversarialVerify — skeptics try to kill the claim
//   G skill      loop      draft -> critic -> revise, so the table is findable
//   H critic     single    duplicate / overfit / empty veto, then stage
//
// The two nodes that matter most are the cheapest: the code gate at 0 (an empty
// pain view costs nothing) and drybuild at C (no agent output becomes an
// artifact without a real query and a real health check).

import { barrier, loop, phase, pipeline, predicate } from "dynamic-workflow";
import { adversarialVerify, type SpecFactory } from "dynamic-workflow/recipes";
import { callNode } from "../agents/runner.ts";
import { db, nowISO } from "../db.ts";
import { engineChoice, limits, paths } from "../config.ts";
import { painEvidence, queryPains, type Pain } from "../observe/views.ts";
import { warehouseSchema } from "../warehouse.ts";
import { drybuild, type TableDesign } from "../tables/drybuild.ts";
import { slugify, upsertSkillCandidate } from "../memories.ts";

export interface TableCounts {
  pains: number;
  mined: number;
  designed: number;
  built: number;
  rejected_build: number;
  useful: number;
  no_gain: number;
  refuted: number;
  vetoed: number;
  staged: number;
}

const MAX_PAINS_PER_CYCLE = 4;

export async function evolveTables(runID: number): Promise<TableCounts> {
  const counts: TableCounts = {
    pains: 0,
    mined: 0,
    designed: 0,
    built: 0,
    rejected_build: 0,
    useful: 0,
    no_gain: 0,
    refuted: 0,
    vetoed: 0,
    staged: 0,
  };

  // ---- 0: the code gate. Cross-session evidence only, top N by rank.
  const pains = queryPains().slice(0, MAX_PAINS_PER_CYCLE);
  counts.pains = pains.length;
  if (pains.length === 0) return counts;

  const schema = warehouseSchema();

  // ---- A: mine. One agent per pain, all in parallel.
  const mined = await phase("mine", () =>
    barrier(
      pains.map((pain) => async () => {
        const out = await callNode<{
          worth: boolean;
          domain: string;
          name: string;
          archetype: string;
          intent: string;
          rationale: string;
        }>({
          runID,
          node: "mine",
          ref: pain.key,
          instructions:
            "下面是一个跨会话反复出现的查询痛点。判断它是否值得为其建一张小表（供 agent 秒级查询），" +
            "如果值得，给出这张表的意图、原型（coverage/dict/template/range/map 之一）和名字。" +
            "只有跨会话的共性痛点才值得建表；只出现在单个会话里的属于个人习惯。",
          data: { pain },
          schema: {
            type: "object",
            required: ["worth", "name", "archetype", "intent"],
            properties: {
              worth: { type: "boolean" },
              domain: { type: "string" },
              name: { type: "string" },
              archetype: { type: "string", enum: ["coverage", "dict", "template", "range", "map"] },
              intent: { type: "string" },
              rationale: { type: "string" },
            },
          },
          cacheKey: `mine:${pain.key}`,
        });
        return out ? { pain, mined: out } : null;
      })
    )
  );

  // One table serves one intent, so two pains that mine to the same table name
  // are the same table. Keep the higher-ranked pain and drop the duplicate here
  // rather than letting both write the same artifact.
  const worth: Array<NonNullable<(typeof mined)[number]>> = [];
  const claimed = new Set<string>();
  for (const m of mined) {
    if (!m || !m.mined.worth || claimed.has(m.mined.name)) continue;
    claimed.add(m.mined.name);
    worth.push(m);
  }
  counts.mined = worth.length;

  // ---- B + C: design (agent), then build (code). Per item, no barrier.
  interface Built {
    pain: Pain;
    design: TableDesign;
    candidateID: number;
    rows: number;
    digest: string;
    asOf: string;
    evidence: Array<{ session_id: string; question: string }>;
  }
  const built: Built[] = [];

  await phase("design+build", () =>
    pipeline(worth, async (_prev: unknown, item: unknown) => {
      const w = item as (typeof worth)[number];
      const out = await callNode<Omit<TableDesign, "domain" | "archetype" | "intent">>({
        runID,
        node: "design",
        ref: w.mined.name,
        instructions:
          "为这张小表给出可执行的设计：列（名字/类型/含义）、来源 SELECT、粒度、刷新周期、已知边界。" +
          "SELECT 只能引用给定 schema 中真实存在的表和列，必须是单条只读查询。" +
          "目标是一张 agent 能秒级读完的小表，不是一张宽表。",
        data: {
          name: w.mined.name,
          archetype: w.mined.archetype,
          intent: w.mined.intent,
          pain: w.pain,
          warehouse_schema: schema,
          patterns_schema: { query_patterns: ["sql_sha", "sql_norm", "shape", "sessions", "occurrences"] },
        },
        schema: {
          type: "object",
          required: ["name", "source", "columns", "source_sql", "grain", "refresh_cron"],
          properties: {
            name: { type: "string" },
            source: { type: "string", enum: ["warehouse", "patterns"] },
            columns: {
              type: "array",
              items: {
                type: "object",
                required: ["name", "type"],
                properties: { name: { type: "string" }, type: { type: "string" }, desc: { type: "string" } },
              },
            },
            source_sql: { type: "string" },
            grain: { type: "string" },
            refresh_cron: { type: "string" },
            caveats_md: { type: "string" },
            rationale: { type: "string" },
          },
        },
        cacheKey: `design:${w.mined.name}`,
      });
      if (!out) return null;
      counts.designed++;

      const design: TableDesign = {
        ...out,
        name: w.mined.name,
        domain: w.mined.domain || "battery",
        archetype: w.mined.archetype,
        intent: w.mined.intent,
        caveats_md: out.caveats_md ?? "",
      };

      const evidence = painEvidence(w.pain.shape, w.pain.tables);
      const candidate = db()
        .query<{ id: number }, any[]>(
          `INSERT INTO table_candidates
             (run_id, domain, name, intent, archetype, evidence_json, pain_json, schema_json, source_sql,
              grain, refresh_cron, caveats_md, status, agent_rationale, created_at, updated_at)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'designed', ?, ?, ?)
           ON CONFLICT(name) DO UPDATE SET
             run_id = excluded.run_id, intent = excluded.intent, schema_json = excluded.schema_json,
             source_sql = excluded.source_sql, grain = excluded.grain, refresh_cron = excluded.refresh_cron,
             caveats_md = excluded.caveats_md, status = 'designed', evidence_json = excluded.evidence_json,
             pain_json = excluded.pain_json, updated_at = excluded.updated_at
           RETURNING id`
        )
        .get(
          runID,
          design.domain,
          design.name,
          design.intent,
          design.archetype,
          JSON.stringify(evidence.map((e) => e.session_id)),
          JSON.stringify(w.pain),
          JSON.stringify(design.columns),
          design.source_sql,
          design.grain,
          design.refresh_cron,
          design.caveats_md,
          w.mined.rationale ?? "",
          nowISO(),
          nowISO()
        )!;

      // ---- C: deterministic build. A rejection here is final for this cycle.
      const b = drybuild(design);
      db().run(
        `INSERT INTO table_builds (candidate_id, digest, rows, bytes, as_of, health_json, ok, log, csv_path, created_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        [
          candidate.id,
          b.digest,
          b.rows,
          b.bytes,
          b.asOf,
          JSON.stringify(b.health),
          b.ok ? 1 : 0,
          b.log,
          b.csvPath,
          nowISO(),
        ]
      );
      if (!b.ok) {
        counts.rejected_build++;
        db().run(`UPDATE table_candidates SET status = 'rejected', reject_reason = ?, updated_at = ? WHERE id = ?`, [
          b.health.problems.join("；"),
          nowISO(),
          candidate.id,
        ]);
        return null;
      }
      counts.built++;
      db().run(`UPDATE table_candidates SET status = 'built', updated_at = ? WHERE id = ?`, [
        nowISO(),
        candidate.id,
      ]);
      built.push({
        pain: w.pain,
        design,
        candidateID: candidate.id,
        rows: b.rows,
        digest: b.digest,
        asOf: b.asOf,
        evidence,
      });
      return design.name;
    })
  );

  // ---- D + E + F: does it actually help? Replay, panel, then skeptics.
  const validated: Built[] = [];
  for (const item of built) {
    const question =
      item.evidence.find((e) => e.question)?.question ?? `关于 ${item.design.name} 的历史提问`;
    const tablePath = `/agents/data/${item.design.domain}/derived/${item.design.name}/${item.design.name}.csv`;

    const [before, after] = await phase("replay", () =>
      barrier(
        [false, true].map((hasTable) => async () => {
          const out = await callNode<{ steps: number; errors: number; answer_ok: boolean; notes: string }>({
            runID,
            node: "replay",
            ref: `${item.design.name}/${hasTable ? "with" : "without"}`,
            instructions:
              "回放这个历史问题：在给定上下文下，你需要几步、出几次错才能得到答案？" +
              "只报告过程指标，不要给出业务结论。",
            data: {
              question,
              has_table: hasTable,
              table_path: hasTable ? tablePath : undefined,
              table_readme: hasTable ? item.design.intent : undefined,
              pain: item.pain,
            },
            schema: {
              type: "object",
              required: ["steps", "errors", "answer_ok"],
              properties: {
                steps: { type: "integer" },
                errors: { type: "integer" },
                answer_ok: { type: "boolean" },
                notes: { type: "string" },
              },
            },
            cacheKey: `replay:${item.design.name}:${hasTable}`,
          });
          return out;
        })
      )
    );

    if (!before || !after) continue;

    // Panel: three judges, each told to weigh a different axis. Majority wins,
    // so one judge's mistake cannot promote or kill a table on its own.
    const verdicts = await phase("panel", () =>
      barrier(
        ["步数", "错误与稳定性", "答案是否一致"].map((angle) => async () => {
          const out = await callNode<{ verdict: string; reason: string; gain_steps?: number }>({
            runID,
            node: "tablejudge",
            ref: `${item.design.name}/${angle}`,
            instructions: `从「${angle}」这个角度判断这张小表是否真的带来收益。`,
            data: {
              angle,
              before,
              after,
              table: { name: item.design.name, rows: item.rows },
            },
            schema: {
              type: "object",
              required: ["verdict", "reason"],
              properties: {
                verdict: { type: "string", enum: ["useful", "no_gain", "harmful"] },
                reason: { type: "string" },
                gain_steps: { type: "integer" },
              },
            },
            cacheKey: `judge:${item.design.name}:${angle}`,
          });
          return out;
        })
      )
    );
    const votes = verdicts.filter((v): v is NonNullable<typeof v> => v !== null);
    const useful = votes.filter((v) => v.verdict === "useful").length;
    const majority = useful > votes.length / 2;
    const verdict = majority ? "useful" : votes.some((v) => v.verdict === "harmful") ? "harmful" : "no_gain";

    db().run(
      `INSERT INTO table_evals (candidate_id, replay_json, before_json, after_json, judge_json, verdict, created_at)
       VALUES (?, ?, ?, ?, ?, ?, ?)`,
      [
        item.candidateID,
        JSON.stringify({ question, table_path: tablePath }),
        JSON.stringify(before),
        JSON.stringify(after),
        JSON.stringify(votes),
        verdict,
        nowISO(),
      ]
    );

    if (!majority) {
      counts.no_gain++;
      db().run(`UPDATE table_candidates SET status = 'rejected', reject_reason = ?, updated_at = ? WHERE id = ?`, [
        `回放未显示收益（${verdict}）`,
        nowISO(),
        item.candidateID,
      ]);
      continue;
    }
    counts.useful++;

    // Skeptics get the last word on publishing. The claim states the evidence
    // plainly so a refuter has something concrete to attack.
    //
    // These leaves go through the recipe's own agent() calls rather than
    // callNode, so they land in the dynamic-workflow journal but NOT in
    // agent_calls. Worth knowing when reconciling cost: the journal is the
    // complete record, agent_calls covers the nodes this pipeline owns.
    const { engine, model } = engineChoice();
    const mk: SpecFactory = (prompt, label) => ({
      engine,
      model,
      cwd: paths.state,
      prompt: `[[NODE:refute]]\n${prompt}`,
      label: label ?? `refute:${item.design.name}`,
    });
    const claim =
      `小表 ${item.design.name}（${item.design.archetype}，${item.rows} 行，证据来自 ` +
      `${item.evidence.length} 个会话）值得进入共享知识库：回放显示步数 ${before.steps} → ${after.steps}，` +
      `错误 ${before.errors} → ${after.errors}。`;
    const adv = await phase("refute", () => adversarialVerify(claim, mk, 2));
    if (!adv.survives) {
      counts.refuted++;
      db().run(`UPDATE table_candidates SET status = 'rejected', reject_reason = ?, updated_at = ? WHERE id = ?`, [
        `对抗验证否决（${adv.refutedCount} 票证伪）`,
        nowISO(),
        item.candidateID,
      ]);
      continue;
    }

    validated.push(item);
    db().run(`UPDATE table_candidates SET status = 'validated', updated_at = ? WHERE id = ?`, [
      nowISO(),
      item.candidateID,
    ]);
  }

  // ---- G: a table nobody can find is a table nobody uses. Draft -> critic ->
  // revise, bounded at maxSkillRounds.
  for (const item of validated) {
    let markdown = "";
    let issues: string[] = [];
    await phase("skill", () =>
      loop<string>(
        async (s) => {
          const draft = await callNode<{ markdown: string }>({
            runID,
            node: "tableskill",
            ref: `${item.design.name}/r${s.round}`,
            instructions:
              "为这张小表写一份 SKILL.md：什么时候该用它、怎么用、有什么边界。" +
              (s.round > 1 ? `上一轮评审指出的问题：${issues.join("；")}。请修正。` : ""),
            data: {
              round: s.round,
              table: {
                // The skill's own directory name, so the frontmatter and the
                // path it is stored under cannot drift apart.
                skill_name: slugify(`use-${item.design.name}`),
                name: item.design.name,
                domain: item.design.domain,
                intent: item.design.intent,
                grain: item.design.grain,
                caveats_md: item.design.caveats_md,
                as_of: item.asOf,
                columns: item.design.columns,
              },
            },
            schema: {
              type: "object",
              required: ["markdown"],
              properties: { markdown: { type: "string" } },
            },
            cacheKey: `tableskill:${item.design.name}:${s.round}`,
          });
          if (draft?.markdown) markdown = draft.markdown;
          const review = await callNode<{ ok: boolean; issues: string[] }>({
            runID,
            node: "skillcritic",
            ref: `${item.design.name}/r${s.round}`,
            instructions: "评审这份 SKILL.md：路径是否确切、口径边界是否写清、时效是否提示。",
            data: { markdown, table: { name: item.design.name } },
            schema: {
              type: "object",
              required: ["ok"],
              properties: { ok: { type: "boolean" }, issues: { type: "array", items: { type: "string" } } },
            },
            cacheKey: `skillcritic:${item.design.name}:${s.round}`,
          });
          issues = review?.issues ?? [];
          return markdown;
        },
        { until: predicate(() => issues.length === 0), maxRounds: limits.maxSkillRounds }
      )
    );
    if (markdown && issues.length === 0) {
      // Update by the id the upsert returned, not by a name rebuilt here: the
      // stored name is slugified, so a hand-built name silently matches nothing
      // and the reviewed draft never replaces the placeholder body.
      const skillID = upsertSkillCandidate(
        {
          name: `use-${item.design.name}`,
          scope: item.design.domain,
          when_to_use: item.design.intent,
          steps: [`读 /agents/data/${item.design.domain}/derived/${item.design.name}/README.md`],
          pitfalls: [item.design.caveats_md],
        },
        item.evidence[0]?.session_id ?? "derived"
      );
      if (skillID !== null) {
        db().run(`UPDATE skill_candidates SET body_md = ?, status = 'published', updated_at = ? WHERE id = ?`, [
          markdown,
          nowISO(),
          skillID,
        ]);
      }
    }
  }

  // ---- H: one global veto pass, then stage.
  if (validated.length > 0) {
    const live = db()
      .query<{ name: string }, []>(`SELECT name FROM table_candidates WHERE status = 'live'`)
      .all()
      .map((r) => r.name);
    const out = await phase("table-critic", () =>
      callNode<{ vetoes: Array<{ name: string; reason: string }> }>({
        runID,
        node: "tablecritic",
        ref: `${validated.length}tables`,
        instructions:
          "这是本轮准备发布的小表。否决重复、过拟合到单会话、空表，或与已有口径冲突的表。",
        data: {
          candidates: validated.map((v) => ({
            name: v.design.name,
            archetype: v.design.archetype,
            evidence_sessions: v.evidence.length,
            rows: v.rows,
          })),
          live_tables: live,
        },
        schema: {
          type: "object",
          required: ["vetoes"],
          properties: {
            vetoes: {
              type: "array",
              items: {
                type: "object",
                required: ["name", "reason"],
                properties: { name: { type: "string" }, reason: { type: "string" } },
              },
            },
          },
        },
        cacheKey: `tablecritic:${validated.map((v) => v.design.name).join("-")}`,
      })
    );
    const vetoed = new Set((out?.vetoes ?? []).map((v) => v.name));
    for (const v of out?.vetoes ?? []) {
      counts.vetoed++;
      db().run(`UPDATE table_candidates SET status = 'rejected', reject_reason = ?, updated_at = ? WHERE name = ?`, [
        `critic veto: ${v.reason}`,
        nowISO(),
        v.name,
      ]);
    }
    for (const item of validated) {
      if (vetoed.has(item.design.name)) continue;
      db().run(`UPDATE table_candidates SET status = 'staged', updated_at = ? WHERE id = ?`, [
        nowISO(),
        item.candidateID,
      ]);
      counts.staged++;
    }
  }

  return counts;
}
