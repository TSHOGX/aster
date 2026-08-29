// memories.ts — writes into the non-rebuildable side.
//
// `support_count` is NOT incremented by string equality here. Two LLM statements
// almost never collide byte-for-byte, so counting exact matches pins support at
// 1 forever and nothing ever promotes. Support is computed later, by the
// consolidate node, as the number of DISTINCT evidence sessions in a merged
// cluster — the count is code, the merge decision is the agent's.

import { db, nowISO } from "./db.ts";
import { canonicalScope } from "./concepts.ts";

export interface CandidateMemory {
  type: string;
  scope: string;
  statement: string;
  when_to_apply?: string;
  confidence?: number;
}

export interface MemoryRow {
  id: number;
  type: string;
  scope: string;
  canonical_scope: string;
  statement: string;
  when_to_apply: string;
  support_count: number;
  contradiction_count: number;
  evidence_json: string;
  status: string;
  verify_verdict: string | null;
  corrected_statement: string | null;
  extractor_version_id: number | null;
  shadow: number;
}

const TYPES = new Set(["fact", "heuristic", "procedure", "bug", "preference"]);

export function upsertCandidate(
  m: CandidateMemory,
  sessionID: string,
  extractorVersionID: number,
  shadow = false
): number | null {
  const statement = m.statement?.trim();
  if (!statement) return null;
  const type = TYPES.has(m.type) ? m.type : "fact";
  const scope = (m.scope || "global").trim();
  const d = db();
  const existing = d
    .query<{ id: number; evidence_json: string }, [string, string, string, number, number]>(
      `SELECT id, evidence_json FROM semantic_memories
       WHERE type = ? AND scope = ? AND statement = ? AND shadow = ? AND extractor_version_id = ?`
    )
    .get(type, scope, statement, shadow ? 1 : 0, extractorVersionID);

  if (existing) {
    const evidence = new Set<string>(JSON.parse(existing.evidence_json) as string[]);
    evidence.add(sessionID);
    d.run(
      `UPDATE semantic_memories SET evidence_json = ?, support_count = ?, updated_at = ? WHERE id = ?`,
      [JSON.stringify([...evidence]), evidence.size, nowISO(), existing.id]
    );
    return existing.id;
  }

  const row = d
    .query<{ id: number }, any[]>(
      `INSERT INTO semantic_memories
         (type, scope, canonical_scope, statement, when_to_apply, support_count, evidence_json,
          status, extractor_version_id, shadow, first_seen_at, updated_at)
       VALUES (?, ?, ?, ?, ?, 1, ?, 'candidate', ?, ?, ?, ?)
       RETURNING id`
    )
    .get(
      type,
      scope,
      canonicalScope(scope, statement),
      statement,
      (m.when_to_apply ?? "").trim(),
      JSON.stringify([sessionID]),
      extractorVersionID,
      shadow ? 1 : 0,
      nowISO(),
      nowISO()
    );
  return row!.id;
}

export function listMemories(opts: { shadow?: boolean; status?: string; versionID?: number } = {}): MemoryRow[] {
  const where: string[] = [];
  const args: any[] = [];
  where.push(`shadow = ?`);
  args.push(opts.shadow ? 1 : 0);
  if (opts.status) {
    where.push(`status = ?`);
    args.push(opts.status);
  }
  if (opts.versionID !== undefined) {
    where.push(`extractor_version_id = ?`);
    args.push(opts.versionID);
  }
  return db()
    .query<MemoryRow, any[]>(
      `SELECT id, type, scope, canonical_scope, statement, when_to_apply, support_count,
              contradiction_count, evidence_json, status, verify_verdict, corrected_statement,
              extractor_version_id, shadow
       FROM semantic_memories WHERE ${where.join(" AND ")} ORDER BY id`
    )
    .all(...args);
}

export interface CandidateSkill {
  name: string;
  scope?: string;
  when_to_use: string;
  steps: string[];
  pitfalls?: string[];
  confidence?: number;
}

export function slugify(s: string): string {
  return (
    s
      .toLowerCase()
      .replace(/[^\p{L}\p{N}]+/gu, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 60) || "skill"
  );
}

export function renderSkillMarkdown(sk: CandidateSkill, sessionID: string): string {
  return [
    "---",
    `name: ${slugify(sk.name)}`,
    "status: observing",
    "---",
    "",
    `# ${sk.name}`,
    "",
    "## When To Use",
    "",
    sk.when_to_use,
    "",
    "## Procedure",
    "",
    ...sk.steps.map((s, i) => `${i + 1}. ${s}`),
    "",
    "## Known Pitfalls",
    "",
    ...(sk.pitfalls?.length ? sk.pitfalls.map((p) => `- ${p}`) : ["- （暂无）"]),
    "",
    "## Evidence",
    "",
    `- session: ${sessionID}`,
  ].join("\n");
}

export function upsertSkillCandidate(sk: CandidateSkill, sessionID: string): number | null {
  const name = slugify(sk.name);
  if (!name || !sk.steps?.length) return null;
  const d = db();
  const existing = d
    .query<{ id: number; evidence_json: string }, [string]>(
      `SELECT id, evidence_json FROM skill_candidates WHERE name = ?`
    )
    .get(name);
  if (existing) {
    const evidence = new Set<string>(JSON.parse(existing.evidence_json) as string[]);
    evidence.add(sessionID);
    d.run(`UPDATE skill_candidates SET evidence_json = ?, support_count = ?, updated_at = ? WHERE id = ?`, [
      JSON.stringify([...evidence]),
      evidence.size,
      nowISO(),
      existing.id,
    ]);
    return existing.id;
  }
  const row = d
    .query<{ id: number }, any[]>(
      `INSERT INTO skill_candidates
         (name, title, scope, body_md, evidence_json, support_count, status, kb_status, created_at, updated_at)
       VALUES (?, ?, ?, ?, ?, 1, 'draft', 'observing', ?, ?)
       RETURNING id`
    )
    .get(
      name,
      sk.name,
      sk.scope ?? "global",
      renderSkillMarkdown(sk, sessionID),
      JSON.stringify([sessionID]),
      nowISO(),
      nowISO()
    );
  return row!.id;
}

export interface SkillRow {
  id: number;
  name: string;
  title: string;
  scope: string;
  body_md: string;
  support_count: number;
  status: string;
  kb_status: string;
}

export function listSkills(status?: string): SkillRow[] {
  return status
    ? db()
        .query<SkillRow, [string]>(
          `SELECT id, name, title, scope, body_md, support_count, status, kb_status
           FROM skill_candidates WHERE status = ? ORDER BY id`
        )
        .all(status)
    : db()
        .query<SkillRow, []>(
          `SELECT id, name, title, scope, body_md, support_count, status, kb_status
           FROM skill_candidates ORDER BY id`
        )
        .all();
}
