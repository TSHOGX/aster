// kb/snapshot.ts — the deterministic spine: assemble, hash, stage, approve.
//
// One digest covers facts, skills AND derived tables, so a reviewer sees the
// whole change at once: the new fact, the new small table, and the skill that
// points at it are one diff and one decision. Nothing an agent produced is
// visible to any sandbox until a human approves that digest.
//
// Publication is an atomic symlink repoint. A running sandbox stays pinned to
// the snapshot it resolved at start; only new sessions pick up the new one.

import {
  cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  rmSync,
  symlinkSync,
  unlinkSync,
  writeFileSync,
  appendFileSync,
} from "node:fs";
import { join, relative, dirname } from "node:path";
import { createHash } from "node:crypto";
import { db, nowISO } from "../db.ts";
import { files, paths } from "../config.ts";
import { listMemories, listSkills } from "../memories.ts";

/** Credential/PII shapes that must never reach the shared KB from distilled
 *  output. Narrow by design: the curator prompt is the recall mechanism, this
 *  regex is the enforcement floor that holds even when an agent slips. */
const SECRET_PATTERNS = [
  /sk-[A-Za-z0-9_\-]{16,}/,
  /(?:password|passwd)\s*[:=]\s*\S+/i,
  /api[_-]?key\s*[:=]\s*\S+/i,
  /MYSQL_PWD\s*=\s*\S+/,
  /mysql:\/\/[^\s]*:[^\s]*@/,
  /bearer\s+[A-Za-z0-9_\-.]{20,}/i,
];

export function containsSecret(s: string): boolean {
  return SECRET_PATTERNS.some((re) => re.test(s));
}

type Tree = Map<string, string>;

function addDir(tree: Tree, dir: string, prefix: string, skip?: (rel: string) => boolean): void {
  if (!existsSync(dir)) return;
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const abs = join(dir, e.name);
    const rel = prefix ? `${prefix}/${e.name}` : e.name;
    if (skip?.(rel)) continue;
    if (e.isDirectory()) addDir(tree, abs, rel, skip);
    else if (e.isFile()) tree.set(rel, readFileSync(abs, "utf8"));
  }
}

function digestOf(tree: Tree): string {
  const h = createHash("sha256");
  for (const key of [...tree.keys()].sort()) {
    h.update(key);
    h.update("\0");
    h.update(tree.get(key)!);
    h.update("\0");
  }
  return h.digest("hex").slice(0, 16);
}

export interface AssembleResult {
  tree: Tree;
  digest: string;
  counts: { facts: number; skills: number; tables: number; dropped: number };
}

/** Build the snapshot tree. Seed content (constitution, hand-written skills,
 *  hand-provisioned corpora) is carried forward; only facts and distilled
 *  artifacts are regenerated, so a nightly cycle can never drop the seed. */
export function assemble(): AssembleResult {
  const tree: Tree = new Map();
  let dropped = 0;

  addDir(tree, paths.seed, "");

  // Carry forward whatever the live snapshot already had under data/ and
  // skills/ — a previously approved derived table must survive a cycle that
  // did not rebuild it.
  const live = currentSnapshotDir();
  if (live) {
    addDir(tree, join(live, "data"), "data");
    addDir(tree, join(live, "skills"), "skills");
  }

  // ---- facts, regenerated from accepted memories.
  const accepted = listMemories({ shadow: false, status: "accepted" }).filter((m) => {
    if (containsSecret(m.statement) || containsSecret(m.when_to_apply)) {
      dropped++;
      return false;
    }
    return true;
  });
  const byScope = new Map<string, typeof accepted>();
  for (const m of accepted) {
    const s = m.canonical_scope || "global";
    byScope.set(s, [...(byScope.get(s) ?? []), m]);
  }
  for (const [scope, items] of byScope) {
    tree.set(
      `facts/${scope}.md`,
      [
        `# ${scope}`,
        "",
        ...items.flatMap((m) => [
          `## ${m.statement}`,
          "",
          `- 适用：${m.when_to_apply || "（未标注）"}`,
          `- 类型：${m.type}`,
          `- 支持：${m.support_count} 个会话`,
          `- 证据：${(JSON.parse(m.evidence_json) as string[]).join(", ")}`,
          "",
        ]),
      ].join("\n")
    );
  }

  // ---- derived tables staged by track B.
  const staged = db()
    .query<{ name: string; domain: string; intent: string }, []>(
      `SELECT name, domain, intent FROM table_candidates WHERE status IN ('staged', 'live') ORDER BY name`
    )
    .all();
  let tables = 0;
  for (const t of staged) {
    const src = join(files.derived, t.name);
    if (!existsSync(src)) continue;
    addDir(tree, src, `data/${t.domain}/derived/${t.name}`);
    tables++;
  }

  // ---- distilled skills.
  const skills = listSkills("published").filter((s) => {
    if (containsSecret(s.body_md)) {
      dropped++;
      return false;
    }
    return true;
  });
  for (const s of skills)
    tree.set(`skills/${s.name}/SKILL.md`, s.body_md.endsWith("\n") ? s.body_md : `${s.body_md}\n`);

  // ---- the navigation entry point. An agent that cannot find a fact will not
  // use it, so the index is regenerated with every publish.
  tree.set(
    "facts/INDEX.md",
    [
      "# 知识库导航",
      "",
      `生成时间：${nowISO()}`,
      "",
      "## 事实（按领域）",
      "",
      ...[...byScope.entries()].map(([scope, items]) => `- [\`facts/${scope}.md\`](./${scope}.md) — ${items.length} 条`),
      "",
      "## 技能",
      "",
      ...skills.map((s) => `- \`skills/${s.name}/SKILL.md\` — ${s.title}`),
      "",
      "## 派生小表（先查小表，再查主表）",
      "",
      ...staged.map((t) => `- \`data/${t.domain}/derived/${t.name}/\` — ${t.intent}`),
      "",
    ].join("\n")
  );

  return {
    tree,
    digest: digestOf(tree),
    counts: { facts: accepted.length, skills: skills.length, tables, dropped },
  };
}

function writeTree(dir: string, tree: Tree): void {
  rmSync(dir, { recursive: true, force: true });
  for (const [rel, content] of tree) {
    const abs = join(dir, rel);
    mkdirSync(dirname(abs), { recursive: true });
    writeFileSync(abs, content);
  }
}

function repoint(linkPath: string, target: string): void {
  if (existsSync(linkPath) || safeLstat(linkPath)) unlinkSync(linkPath);
  symlinkSync(target, linkPath);
}

function safeLstat(p: string): boolean {
  try {
    lstatSync(p);
    return true;
  } catch {
    return false;
  }
}

function snapshotsDir(): string {
  return join(files.kbRoot, "snapshots");
}

export function currentSnapshotDir(): string | null {
  const link = join(files.kbRoot, "current");
  if (!safeLstat(link)) return null;
  try {
    return join(snapshotsDir(), readFileSync(join(link, ".digest"), "utf8").trim());
  } catch {
    // The symlink target is authoritative; .digest is a convenience.
    return safeLstat(link) ? link : null;
  }
}

export function pendingInfo(): { digest: string; prev_digest: string; staged_at: string; counts: unknown } | null {
  const p = join(files.kbRoot, "pending.json");
  if (!existsSync(p)) return null;
  return JSON.parse(readFileSync(p, "utf8"));
}

export interface StageResult {
  digest: string;
  changed: boolean;
  counts: AssembleResult["counts"];
}

/** Write the snapshot and the `pending` marker. Never touches `current`:
 *  distilled knowledge lands in the context of EVERY new session, so the
 *  default is review, not publish. */
export function stage(runID: number): StageResult {
  const { tree, digest, counts } = assemble();
  const dir = join(snapshotsDir(), digest);
  const liveDigest = readCurrentDigest();
  if (liveDigest === digest) {
    // Byte-identical to what is already live: there is nothing to review.
    return { digest, changed: false, counts };
  }
  writeTree(dir, tree);
  writeFileSync(join(dir, ".digest"), digest);
  repoint(join(files.kbRoot, "pending"), dir);
  writeFileSync(
    join(files.kbRoot, "pending.json"),
    JSON.stringify({ digest, prev_digest: liveDigest ?? "", staged_at: nowISO(), run_id: runID, counts }, null, 2)
  );
  return { digest, changed: true, counts };
}

export function readCurrentDigest(): string | null {
  const p = join(files.kbRoot, "current", ".digest");
  try {
    return readFileSync(p, "utf8").trim();
  } catch {
    return null;
  }
}

/** Publish. The digest is mandatory and must match `pending`, so a stale
 *  terminal cannot promote a snapshot the reviewer never looked at. */
export function approve(digest: string): { ok: boolean; message: string } {
  const p = pendingInfo();
  if (!p) return { ok: false, message: "没有待审批快照" };
  if (p.digest !== digest) return { ok: false, message: `digest 不匹配：待审批的是 ${p.digest}` };
  repoint(join(files.kbRoot, "current"), join(snapshotsDir(), digest));
  db().run(`UPDATE table_candidates SET status = 'live', updated_at = ? WHERE status = 'staged'`, [nowISO()]);
  db().run(`UPDATE skill_candidates SET kb_status = 'in-use', updated_at = ? WHERE status = 'published' AND support_count >= 2`, [
    nowISO(),
  ]);
  audit(`approve ${digest}`);
  clearPending();
  return { ok: true, message: `已发布 ${digest}；新会话将挂载它，运行中的会话保持原快照` };
}

/** Reject. The snapshot stays on disk: it is the evidence behind the decision. */
export function reject(digest: string, reason: string): { ok: boolean; message: string } {
  const p = pendingInfo();
  if (!p) return { ok: false, message: "没有待审批快照" };
  if (p.digest !== digest) return { ok: false, message: `digest 不匹配：待审批的是 ${p.digest}` };
  audit(`reject ${digest} reason=${reason}`);
  clearPending();
  return { ok: true, message: `已驳回 ${digest}（快照保留在磁盘上作为决策证据）` };
}

function clearPending(): void {
  const link = join(files.kbRoot, "pending");
  if (safeLstat(link)) unlinkSync(link);
  const marker = join(files.kbRoot, "pending.json");
  if (existsSync(marker)) rmSync(marker);
}

function audit(line: string): void {
  mkdirSync(files.kbRoot, { recursive: true });
  appendFileSync(join(files.kbRoot, "approvals.log"), `${nowISO()} ${line}\n`);
}

export interface DiffEntry {
  path: string;
  change: "added" | "removed" | "modified";
}

/** File-level diff between the live snapshot and the pending one. */
export function diff(): { from: string | null; to: string | null; entries: DiffEntry[] } {
  const cur = readCurrentDigest();
  const pend = pendingInfo()?.digest ?? null;
  const readTree = (digest: string | null): Tree => {
    const t: Tree = new Map();
    if (!digest) return t;
    addDir(t, join(snapshotsDir(), digest), "", (rel) => rel === ".digest");
    return t;
  };
  const a = readTree(cur);
  const b = readTree(pend);
  const entries: DiffEntry[] = [];
  for (const k of new Set([...a.keys(), ...b.keys()])) {
    if (!a.has(k)) entries.push({ path: k, change: "added" });
    else if (!b.has(k)) entries.push({ path: k, change: "removed" });
    else if (a.get(k) !== b.get(k)) entries.push({ path: k, change: "modified" });
  }
  return { from: cur, to: pend, entries: entries.sort((x, y) => x.path.localeCompare(y.path)) };
}

/** First boot: point `current` at the seed so the mount source always exists. */
export function seedIfEmpty(): string | null {
  if (readCurrentDigest()) return null;
  const tree: Tree = new Map();
  addDir(tree, paths.seed, "");
  if (tree.size === 0) return null;
  const digest = digestOf(tree);
  const dir = join(snapshotsDir(), digest);
  writeTree(dir, tree);
  writeFileSync(join(dir, ".digest"), digest);
  mkdirSync(files.kbRoot, { recursive: true });
  repoint(join(files.kbRoot, "current"), dir);
  audit(`seed ${digest}`);
  return digest;
}

/** Copy the resolved snapshot to a path, the way a sandbox mount would. Used by
 *  the demo to show what an agent would actually see. */
export function materializeCurrent(dest: string): string | null {
  const digest = readCurrentDigest();
  if (!digest) return null;
  rmSync(dest, { recursive: true, force: true });
  mkdirSync(dirname(dest), { recursive: true });
  cpSync(join(snapshotsDir(), digest), dest, { recursive: true });
  return digest;
}

/** Relative path of every file in a snapshot, for reporting. */
export function listSnapshot(digest: string): string[] {
  const dir = join(snapshotsDir(), digest);
  const tree: Tree = new Map();
  addDir(tree, dir, "", (rel) => rel === ".digest");
  return [...tree.keys()].sort().map((k) => relative("", k));
}
