// graphs/kb-evolve.ts — track 0: candidates -> accepted knowledge.
//
//   B0 block-key   single      free-text scopes -> canonical taxonomy
//   B1 cluster     barrier     one agent per block, sees the WHOLE block at once
//   C  verify      pipeline    check each claim against the live tree
//   D  curate      pipeline    the promotion gate
//   F  critic      single      one global gate over THIS cycle's additions
//
// Two properties are deliberate. First, no vectors and no cosine cutoff: a fixed
// geometry is itself a hand-tuned scalar, and it breaks on bilingual paraphrase
// ("current_curve is double-encoded" ≡ "充电曲线字段需要二次解析"). Blocking plus
// in-context clustering is the classic scalable dedup shape, with attention doing
// the all-pairs compare a threshold used to approximate.
//
// Second, support_count is computed by CODE from the distinct evidence sessions
// of a merged cluster. The agent decides membership; it never sets the number.

import { barrier, phase, pipeline } from "dynamic-workflow";
import { readdirSync, existsSync } from "node:fs";
import { join } from "node:path";
import { callNode } from "../agents/runner.ts";
import { db, nowISO } from "../db.ts";
import { listMemories, listSkills, type MemoryRow } from "../memories.ts";
import { paths } from "../config.ts";

export interface KBCounts {
  candidates: number;
  blocks: number;
  clusters: number;
  absorbed: number;
  verified_stale: number;
  verified_fixed: number;
  promoted: number;
  held: number;
  rejected: number;
  vetoed: number;
  skills_published: number;
}

const CLUSTER_SCHEMA = {
  type: "object",
  required: ["clusters"],
  properties: {
    clusters: {
      type: "array",
      items: {
        type: "object",
        required: ["canonical", "member_ids"],
        properties: {
          canonical: {
            type: "object",
            required: ["type", "scope", "statement"],
            properties: {
              type: { type: "string" },
              scope: { type: "string" },
              statement: { type: "string" },
              when_to_apply: { type: "string" },
            },
          },
          member_ids: { type: "array", items: { type: "integer" } },
          rationale: { type: "string" },
        },
      },
    },
  },
} as const;

/** Everything the live tree currently offers, for the verify node to check
 *  claims against. Cheap to compute and it is the whole point of node C: a claim
 *  that names a path nobody can open is worse than no claim. */
function livePaths(): string[] {
  const out: string[] = [];
  const walk = (dir: string, prefix: string) => {
    if (!existsSync(dir)) return;
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = `${prefix}/${e.name}`;
      if (e.isDirectory()) walk(join(dir, e.name), p);
      else out.push(p);
    }
  };
  walk(paths.seed, "/agents");
  return out;
}

/** Claims worth a verify call: only those naming a path, a tool, an env var or a
 *  named fault have anything to check against code. A pure data-schema fact does
 *  not, and spending a call on it buys nothing. */
function needsVerify(m: MemoryRow): boolean {
  return /(\/[\w./-]+|\b[A-Z][A-Z0-9_]{3,}\b|missing|缺失|权限|permission|not found|失效)/.test(
    `${m.statement} ${m.when_to_apply}`
  );
}

export async function evolveKB(runID: number): Promise<KBCounts> {
  const counts: KBCounts = {
    candidates: 0,
    blocks: 0,
    clusters: 0,
    absorbed: 0,
    verified_stale: 0,
    verified_fixed: 0,
    promoted: 0,
    held: 0,
    rejected: 0,
    vetoed: 0,
    skills_published: 0,
  };

  const candidates = listMemories({ shadow: false, status: "candidate" });
  counts.candidates = candidates.length;
  if (candidates.length === 0) return counts;

  // ---- B0: one global view of the scope labels -> canonical taxonomy.
  const scopes = [...new Set(candidates.map((c) => c.scope))].sort();
  const block = await phase("blockkey", () =>
    callNode<{ mapping: Array<{ from: string; to: string }>; blocks: Array<{ scope: string }> }>({
      runID,
      node: "blockkey",
      ref: `${scopes.length}scopes`,
      instructions:
        "下面是候选知识里出现过的全部 scope 标签（只有标签，没有内容）。把同义、跨语言、粒度不同的标签归并成一套规范分类，" +
        "并给出每个原标签到规范标签的映射。目标是让等价的知识落进同一个块，便于后续在一个上下文里比较。",
      data: { scopes },
      schema: {
        type: "object",
        required: ["mapping", "blocks"],
        properties: {
          mapping: {
            type: "array",
            items: {
              type: "object",
              required: ["from", "to"],
              properties: { from: { type: "string" }, to: { type: "string" } },
            },
          },
          blocks: {
            type: "array",
            items: { type: "object", required: ["scope"], properties: { scope: { type: "string" } } },
          },
        },
      },
      cacheKey: `blockkey:${scopes.join(",")}`,
    })
  );

  const canonical = new Map<string, string>();
  for (const m of block?.mapping ?? []) canonical.set(m.from, m.to);
  for (const c of candidates) {
    const to = canonical.get(c.scope) ?? (c.canonical_scope || "global");
    db().run(`UPDATE semantic_memories SET canonical_scope = ? WHERE id = ?`, [to, c.id]);
    c.canonical_scope = to;
  }

  // ---- B1: one agent per (canonical scope) block, each seeing its whole block.
  const byBlock = new Map<string, MemoryRow[]>();
  for (const c of candidates) {
    const key = c.canonical_scope || "global";
    byBlock.set(key, [...(byBlock.get(key) ?? []), c]);
  }
  counts.blocks = byBlock.size;

  const clusterResults = await phase("cluster", () =>
    barrier(
      [...byBlock.entries()].map(([scope, members]) => async () => {
        const out = await callNode<{
          clusters: Array<{
            canonical: { type: string; scope: string; statement: string; when_to_apply?: string };
            member_ids: number[];
            rationale?: string;
          }>;
        }>({
          runID,
          node: "cluster",
          ref: scope,
          instructions:
            "下面是同一个规范 scope 下的全部候选知识。判断哪些条目其实在说同一件事（包括中英文改写、结构性改写），" +
            "把它们合并成一个簇，并为每个簇写一条最准确的规范表述。不要跨簇合并不同的知识点；不确定时宁可拆开。",
          data: {
            block: { scope },
            candidates: members.map((m) => ({
              id: m.id,
              type: m.type,
              scope: m.scope,
              statement: m.statement,
              when_to_apply: m.when_to_apply,
              evidence_sessions: JSON.parse(m.evidence_json) as string[],
            })),
          },
          schema: CLUSTER_SCHEMA as unknown as Record<string, unknown>,
          cacheKey: `cluster:${scope}:${members.map((m) => m.id).join("-")}`,
        });
        return { scope, out, members };
      })
    )
  );

  for (const res of clusterResults) {
    if (!res?.out) continue; // hold: the block stays as-is for the next cycle
    const byID = new Map(res.members.map((m) => [m.id, m]));
    for (const cl of res.out.clusters) {
      const ids = cl.member_ids.filter((id) => byID.has(id));
      if (ids.length === 0) continue;
      counts.clusters++;
      // The primary row survives and carries the canonical statement; the others
      // are folded into it. Support = distinct evidence sessions across the
      // whole cluster, counted here rather than trusted from the agent.
      const members = ids.map((id) => byID.get(id)!);
      const primary = members.reduce((a, b) => (b.support_count > a.support_count ? b : a));
      const evidence = new Set<string>();
      for (const m of members) for (const s of JSON.parse(m.evidence_json) as string[]) evidence.add(s);
      db().run(
        `UPDATE semantic_memories
         SET statement = ?, when_to_apply = ?, canonical_scope = ?, support_count = ?,
             evidence_json = ?, canonical_of_json = ?, agent_rationale = ?, updated_at = ?
         WHERE id = ?`,
        [
          cl.canonical.statement,
          cl.canonical.when_to_apply ?? primary.when_to_apply,
          cl.canonical.scope || primary.canonical_scope,
          evidence.size,
          JSON.stringify([...evidence]),
          JSON.stringify(ids),
          cl.rationale ?? "",
          nowISO(),
          primary.id,
        ]
      );
      for (const m of members) {
        if (m.id === primary.id) continue;
        db().run(`UPDATE semantic_memories SET status = 'merged', updated_at = ? WHERE id = ?`, [
          nowISO(),
          m.id,
        ]);
        counts.absorbed++;
      }
    }
  }

  // ---- C: verify surviving candidates against the live tree.
  const survivors = listMemories({ shadow: false, status: "candidate" });
  const toVerify = survivors.filter(needsVerify);
  const kbPaths = livePaths();
  await phase("verify", () =>
    pipeline(toVerify, async (_prev: unknown, item: unknown) => {
      const m = item as MemoryRow;
      const out = await callNode<{
        verdict: string;
        corrected_statement?: string;
        checked?: string[];
        rationale?: string;
      }>({
        runID,
        node: "verify",
        ref: String(m.id),
        instructions:
          "判断这条知识对当前代码/镜像是否仍然成立。它引用的路径、工具、环境变量是否还存在？" +
          "如果这个故障已经被修复，那么它作为教训会误导后续会话。逐项核对后给出结论。",
        data: {
          statement: m.statement,
          when_to_apply: m.when_to_apply,
          kb_paths: kbPaths,
          fixed_faults: ["mysql client missing", "中文字体缺失"],
        },
        schema: {
          type: "object",
          required: ["verdict"],
          properties: {
            verdict: { type: "string", enum: ["current", "stale", "fixed", "unverifiable"] },
            corrected_statement: { type: "string" },
            checked: { type: "array", items: { type: "string" } },
            rationale: { type: "string" },
          },
        },
        cacheKey: `verify:${m.id}`,
      });
      if (!out) return null;
      if (out.verdict === "stale") counts.verified_stale++;
      if (out.verdict === "fixed") counts.verified_fixed++;
      db().run(
        `UPDATE semantic_memories
         SET verify_verdict = ?, corrected_statement = ?,
             contradiction_count = contradiction_count + ?, updated_at = ?
         WHERE id = ?`,
        [
          out.verdict,
          out.corrected_statement ?? "",
          out.verdict === "stale" || out.verdict === "fixed" ? 1 : 0,
          nowISO(),
          m.id,
        ]
      );
      return out.verdict;
    })
  );

  // ---- D: curate. The promotion decision, one item at a time.
  const forCuration = listMemories({ shadow: false, status: "candidate" });
  const promotedIDs: number[] = [];
  await phase("curate", () =>
    pipeline(forCuration, async (_prev: unknown, item: unknown) => {
      const m = item as MemoryRow;
      const out = await callNode<{ disposition: string; reason: string }>({
        runID,
        node: "curate",
        ref: String(m.id),
        instructions:
          "决定这条知识是否应该进入所有新会话都会读到的共享知识库。" +
          "只有稳定、有跨会话证据、适用条件可判断、且不会误导的条目才应 promote；" +
          "证据不足的 hold，明显无用或已失效的 reject。给出理由。",
        data: {
          item: {
            type: m.type,
            scope: m.canonical_scope,
            statement: m.corrected_statement || m.statement,
            when_to_apply: m.when_to_apply,
            support_count: m.support_count,
            verify_verdict: m.verify_verdict ?? "current",
          },
          kb_manifest: listMemories({ shadow: false, status: "accepted" }).map((x) => x.statement),
        },
        schema: {
          type: "object",
          required: ["disposition", "reason"],
          properties: {
            disposition: { type: "string", enum: ["promote", "hold", "reject"] },
            reason: { type: "string" },
          },
        },
        cacheKey: `curate:${m.id}:${m.support_count}:${m.verify_verdict ?? "current"}`,
      });
      if (!out) {
        counts.held++;
        return "hold";
      }
      if (out.disposition === "promote") {
        db().run(
          `UPDATE semantic_memories SET status = 'accepted', agent_rationale = ?, updated_at = ? WHERE id = ?`,
          [out.reason, nowISO(), m.id]
        );
        promotedIDs.push(m.id);
        counts.promoted++;
      } else if (out.disposition === "reject") {
        db().run(
          `UPDATE semantic_memories SET status = 'deprecated', agent_rationale = ?, updated_at = ? WHERE id = ?`,
          [out.reason, nowISO(), m.id]
        );
        counts.rejected++;
      } else {
        db().run(`UPDATE semantic_memories SET agent_rationale = ?, updated_at = ? WHERE id = ?`, [
          out.reason,
          nowISO(),
          m.id,
        ]);
        counts.held++;
      }
      return out.disposition;
    })
  );

  // ---- F: the one global gate. Scoped to THIS cycle's promotions: re-reading
  // the entire accepted set every night grows without bound and the node then
  // times out, which means the gate silently never runs.
  if (promotedIDs.length > 0) {
    const news = listMemories({ shadow: false, status: "accepted" }).filter((m) => promotedIDs.includes(m.id));
    const existing = listMemories({ shadow: false, status: "accepted" }).filter(
      (m) => !promotedIDs.includes(m.id)
    );
    const out = await phase("critic", () =>
      callNode<{ vetoes: Array<{ id: number; reason: string }> }>({
        runID,
        node: "critic",
        ref: `${news.length}new`,
        instructions:
          "这是本轮准备加入知识库的新条目，以及库里已有的条目。找出会造成矛盾、重复、或让已失效说法复活的新条目并否决它们。" +
          "只否决新条目，不要改动已有条目。",
        data: {
          new_items: news.map((m) => ({ id: m.id, scope: m.canonical_scope, statement: m.statement })),
          existing: existing.map((m) => ({ scope: m.canonical_scope, statement: m.statement })),
        },
        schema: {
          type: "object",
          required: ["vetoes"],
          properties: {
            vetoes: {
              type: "array",
              items: {
                type: "object",
                required: ["id", "reason"],
                properties: { id: { type: "integer" }, reason: { type: "string" } },
              },
            },
          },
        },
        cacheKey: `critic:${promotedIDs.join("-")}`,
      })
    );
    for (const v of out?.vetoes ?? []) {
      db().run(
        `UPDATE semantic_memories SET status = 'candidate', agent_rationale = ?, updated_at = ? WHERE id = ?`,
        [`critic veto: ${v.reason}`, nowISO(), v.id]
      );
      counts.vetoed++;
      counts.promoted--;
    }
  }

  // Skills: a draft with cross-session support becomes publishable, still marked
  // `observing` in the KB. Sustained support across cycles is what flips it to
  // in-use — never a single night's evidence.
  for (const sk of listSkills("draft")) {
    if (sk.support_count >= 1) {
      db().run(`UPDATE skill_candidates SET status = 'published', updated_at = ? WHERE id = ?`, [
        nowISO(),
        sk.id,
      ]);
      counts.skills_published++;
    }
  }

  return counts;
}
