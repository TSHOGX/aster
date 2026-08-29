// extractors.ts — the extraction METHOD as a versioned artifact.
//
// The thing track A evolves is not a string constant buried in the source. Each
// lens version is four reviewable files on disk:
//
//   prompt.md    what the lens is told to extract
//   schema.json  the output contract
//   gates.json   the cheap hard signals that must hold before an LLM is spent
//   slice.json   WHICH events the lens is allowed to see
//
// slice.json earns its place: a large share of "the lens missed it" is really
// "the lens never saw it" — a constraint that only ever appeared in a SQL
// argument or a tool_result error cannot be recovered by rewording the prompt.
// Making the slice a versioned, A/B-able field is what turns that from a guess
// into a measurement.

import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createHash } from "node:crypto";
import { db, nowISO } from "./db.ts";
import { paths } from "./config.ts";

export type LensName = "facts" | "failure" | "skill";
export const LENSES: LensName[] = ["facts", "failure", "skill"];

export interface Slice {
  /** Event kinds the lens may read. */
  include: string[];
  max_events: number;
}

export interface Gates {
  min_turns?: number;
  require_failure_signal?: boolean;
}

export interface ExtractorVersion {
  id: number;
  lens: LensName;
  version: string;
  prompt_md: string;
  schema: Record<string, unknown>;
  gates: Gates;
  slice: Slice;
  status: "draft" | "shadow" | "active" | "retired";
}

interface Row {
  id: number;
  lens: string;
  version: string;
  prompt_md: string;
  schema_json: string;
  gates_json: string;
  slice_json: string;
  status: string;
}

function hydrate(r: Row): ExtractorVersion {
  return {
    id: r.id,
    lens: r.lens as LensName,
    version: r.version,
    prompt_md: r.prompt_md,
    schema: JSON.parse(r.schema_json),
    gates: JSON.parse(r.gates_json),
    slice: JSON.parse(r.slice_json),
    status: r.status as ExtractorVersion["status"],
  };
}

/** Load every version on disk into the DB. The first version of a lens becomes
 *  `active`; anything else lands as `draft` and only a human flip makes it live. */
export function syncFromDisk(): number {
  let n = 0;
  for (const lens of LENSES) {
    const dir = join(paths.extractors, lens);
    if (!existsSync(dir)) continue;
    for (const version of readdirSync(dir).sort()) {
      const vdir = join(dir, version);
      const prompt = readFileSync(join(vdir, "prompt.md"), "utf8");
      const schema = readFileSync(join(vdir, "schema.json"), "utf8");
      const gates = readFileSync(join(vdir, "gates.json"), "utf8");
      const slice = readFileSync(join(vdir, "slice.json"), "utf8");
      const existing = db()
        .query<{ id: number }, [string, string]>(
          `SELECT id FROM extractor_versions WHERE lens = ? AND version = ?`
        )
        .get(lens, version);
      if (existing) continue;
      const anyActive = db()
        .query<{ n: number }, [string]>(
          `SELECT COUNT(*) AS n FROM extractor_versions WHERE lens = ? AND status = 'active'`
        )
        .get(lens)!.n;
      db().run(
        `INSERT INTO extractor_versions
           (lens, version, prompt_sha, prompt_md, schema_json, gates_json, slice_json, status, rationale, created_at, activated_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        [
          lens,
          version,
          createHash("sha256").update(prompt).digest("hex").slice(0, 16),
          prompt,
          schema,
          gates,
          slice,
          anyActive === 0 ? "active" : "draft",
          "loaded from disk",
          nowISO(),
          anyActive === 0 ? nowISO() : null,
        ]
      );
      n++;
    }
  }
  return n;
}

export function activeVersion(lens: LensName): ExtractorVersion {
  const r = db()
    .query<Row, [string]>(
      `SELECT id, lens, version, prompt_md, schema_json, gates_json, slice_json, status
       FROM extractor_versions WHERE lens = ? AND status = 'active' ORDER BY id DESC LIMIT 1`
    )
    .get(lens);
  if (!r) throw new Error(`no active extractor for lens "${lens}" — run \`cli init\` first`);
  return hydrate(r);
}

export function versionByID(id: number): ExtractorVersion {
  const r = db()
    .query<Row, [number]>(
      `SELECT id, lens, version, prompt_md, schema_json, gates_json, slice_json, status
       FROM extractor_versions WHERE id = ?`
    )
    .get(id);
  if (!r) throw new Error(`no extractor version ${id}`);
  return hydrate(r);
}

export function listVersions(lens?: LensName): ExtractorVersion[] {
  const rows = lens
    ? db()
        .query<Row, [string]>(
          `SELECT id, lens, version, prompt_md, schema_json, gates_json, slice_json, status
           FROM extractor_versions WHERE lens = ? ORDER BY id`
        )
        .all(lens)
    : db()
        .query<Row, []>(
          `SELECT id, lens, version, prompt_md, schema_json, gates_json, slice_json, status
           FROM extractor_versions ORDER BY lens, id`
        )
        .all();
  return rows.map(hydrate);
}

/** Persist a proposed version: files on disk (so it is reviewable as a diff) and
 *  a `shadow` row (so it can run without touching the live KB). */
export function writeShadowVersion(v: {
  lens: LensName;
  version: string;
  prompt_md: string;
  schema: Record<string, unknown>;
  gates: Gates;
  slice: Slice;
  rationale: string;
  parentID: number;
}): ExtractorVersion {
  const vdir = join(paths.extractors, v.lens, v.version);
  mkdirSync(vdir, { recursive: true });
  writeFileSync(join(vdir, "prompt.md"), v.prompt_md);
  writeFileSync(join(vdir, "schema.json"), JSON.stringify(v.schema, null, 2));
  writeFileSync(join(vdir, "gates.json"), JSON.stringify(v.gates, null, 2));
  writeFileSync(join(vdir, "slice.json"), JSON.stringify(v.slice, null, 2));

  const row = db()
    .query<{ id: number }, any[]>(
      `INSERT INTO extractor_versions
         (lens, version, prompt_sha, prompt_md, schema_json, gates_json, slice_json, status, rationale, parent_id, created_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, 'shadow', ?, ?, ?)
       ON CONFLICT(lens, version) DO UPDATE SET
         prompt_md = excluded.prompt_md, schema_json = excluded.schema_json,
         gates_json = excluded.gates_json, slice_json = excluded.slice_json,
         rationale = excluded.rationale
       RETURNING id`
    )
    .get(
      v.lens,
      v.version,
      createHash("sha256").update(v.prompt_md).digest("hex").slice(0, 16),
      v.prompt_md,
      JSON.stringify(v.schema),
      JSON.stringify(v.gates),
      JSON.stringify(v.slice),
      v.rationale,
      v.parentID,
      nowISO()
    );
  return versionByID(row!.id);
}

/** Flip a shadow version to active. Deliberately a separate, explicit action:
 *  the graph may propose and measure, but changing what runs tomorrow on every
 *  session is a human decision. */
export function activateVersion(id: number): ExtractorVersion {
  const v = versionByID(id);
  db().transaction(() => {
    db().run(`UPDATE extractor_versions SET status = 'retired' WHERE lens = ? AND status = 'active'`, [v.lens]);
    db().run(`UPDATE extractor_versions SET status = 'active', activated_at = ? WHERE id = ?`, [nowISO(), id]);
  })();
  return versionByID(id);
}

export interface SliceEvent {
  kind: string;
  at_turn: number;
  text: string;
  tool?: string;
  args?: unknown;
  is_error?: boolean;
}

/** The events one lens version is allowed to see, per its slice policy. */
export function sliceEvents(sessionID: string, slice: Slice): SliceEvent[] {
  const rows = db()
    .query<
      { kind: string; at_turn: number; text: string; tool: string | null; args_json: string | null; is_error: number },
      [string]
    >(
      `SELECT kind, at_turn, text, tool, args_json, is_error
       FROM trajectory_events WHERE session_id = ? ORDER BY seq`
    )
    .all(sessionID);
  const out: SliceEvent[] = [];
  for (const r of rows) {
    if (!slice.include.includes(r.kind)) continue;
    out.push({
      kind: r.kind,
      at_turn: r.at_turn,
      // Long tool payloads are truncated: a lens that needs 20KB of one result
      // is reading the wrong thing, and an unbounded slice makes cost unbounded.
      text: r.text.slice(0, 1200),
      tool: r.tool ?? undefined,
      args: r.args_json ? JSON.parse(r.args_json) : undefined,
      is_error: r.is_error === 1,
    });
    if (out.length >= slice.max_events) break;
  }
  return out;
}

/** Hard pre-gate: cheap signals that must hold before an LLM call is made. */
export function gatePasses(
  gates: Gates,
  target: { turn_count: number; error_count: number; correction_count: number }
): boolean {
  if (gates.min_turns && target.turn_count < gates.min_turns) return false;
  if (gates.require_failure_signal && target.error_count === 0 && target.correction_count === 0) return false;
  return true;
}
