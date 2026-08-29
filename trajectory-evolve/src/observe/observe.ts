// observe/observe.ts — trajectory_events -> behavioural observations. Zero LLM.
//
// This is the shared substrate both evolution tracks read. Track A needs to know
// where the KB failed to answer; track B needs to know which data questions get
// re-asked. Both are mechanical facts about the trace, so no model is spent on
// them, and both tracks then see the SAME facts instead of each re-interpreting
// the raw trace with its own prompt.

import { db, nowISO } from "../db.ts";
import { normalizeSQL, type QueryShape } from "./sqlnorm.ts";

/** Tools that read the mounted KB. */
const KB_TOOLS = new Set(["read", "grep", "glob", "bash"]);
/** Tools that hit a data warehouse. */
const QUERY_ENGINES: Record<string, string> = {
  doris_query: "doris",
  duckdb_query: "duckdb",
  sql: "doris",
};

/** User utterances that mean "you got it wrong". Deliberately narrow: a false
 *  positive here inflates the correction count that gates the failure lens. */
const CORRECTION_RE =
  /(不对|不是这个|你没理解|没理解我|算了|又错|再来一次|重新查|我说的是|不是我要的|wrong|not what i (asked|meant)|try again)/i;

/** Query shapes that mean the agent is still orienting rather than answering. */
const PROBE_SHAPES = new Set<QueryShape>(["enum_probe", "coverage_probe", "schema_probe"]);

interface EventRow {
  seq: number;
  at_turn: number;
  kind: string;
  text: string;
  tool: string | null;
  args_json: string | null;
  is_error: number;
  rows: number | null;
  latency_ms: number | null;
}

export interface ObserveResult {
  sessionID: string;
  queries: number;
  kbTouches: number;
  kbMisses: number;
  gaps: number;
}

/** Pull a KB path out of a tool call, whether it came as a path argument or
 *  inside a shell command. Returns null when the call is not KB-related. */
function kbTarget(tool: string, args: Record<string, unknown>): { path: string; mode: string; query: string } | null {
  const path = typeof args.path === "string" ? args.path : undefined;
  const pattern = typeof args.pattern === "string" ? args.pattern : undefined;
  if (tool === "read" && path?.startsWith("/agents")) return { path, mode: "read", query: "" };
  if (tool === "glob" && path?.startsWith("/agents")) return { path, mode: "glob", query: pattern ?? "" };
  if (tool === "grep" && path?.startsWith("/agents"))
    return { path, mode: "grep", query: pattern ?? "" };
  if (tool === "bash") {
    const cmd = typeof args.command === "string" ? args.command : "";
    if (!cmd.includes("/agents")) return null;
    const m = /(?:grep|rg)\s+(?:-\w+\s+)*(?:'([^']+)'|"([^"]+)"|(\S+))\s+(\S*\/agents\S*)/.exec(cmd);
    if (m) return { path: m[4]!, mode: "grep", query: m[1] ?? m[2] ?? m[3] ?? "" };
    const c = /(?:cat|less|head)\s+(\S*\/agents\S*)/.exec(cmd);
    if (c) return { path: c[1]!, mode: "read", query: "" };
    return { path: "/agents", mode: "grep", query: cmd };
  }
  return null;
}

/** A KB read counts as a miss when the tool errored or produced no match. */
function isMiss(result: EventRow | undefined): boolean {
  if (!result) return true;
  if (result.is_error) return true;
  const t = result.text.trim();
  if (t === "") return true;
  return /no matches|not found|no such file|0 matches|没有匹配|未找到/i.test(t);
}

function outcomeSignal(events: EventRow[], corrections: number): string {
  const errors = events.filter((e) => e.kind === "tool_result" && e.is_error).length;
  const last = events[events.length - 1];
  const lastText = events.filter((e) => e.kind === "assistant").pop()?.text ?? "";
  if (last?.kind === "tool_result" && last.is_error) return "abandoned";
  if (/无法|失败|没有权限|拿不到|做不了|cannot|failed/i.test(lastText) && errors > 0) return "failed";
  if (errors > 0 || corrections > 0) return "partial";
  if (events.some((e) => e.kind === "assistant" && e.text.length > 40)) return "success";
  return "unknown";
}

/** Longest consecutive run of probe queries — the "agent is groping around"
 *  signal that a lookup table is meant to collapse. */
function longestProbeChain(shapes: QueryShape[]): number {
  let best = 0;
  let cur = 0;
  for (const s of shapes) {
    if (PROBE_SHAPES.has(s)) {
      cur++;
      best = Math.max(best, cur);
    } else cur = 0;
  }
  return best;
}

export function observeSession(sessionID: string): ObserveResult {
  const d = db();
  const events = d
    .query<EventRow, [string]>(
      `SELECT seq, at_turn, kind, text, tool, args_json, is_error, rows, latency_ms
       FROM trajectory_events WHERE session_id = ? ORDER BY seq`
    )
    .all(sessionID);
  const meta = d
    .query<{ day: string; user_id: string }, [string]>(
      `SELECT day, user_id FROM sessions WHERE session_id = ?`
    )
    .get(sessionID);
  if (!meta) throw new Error(`observe: unknown session ${sessionID}`);

  const out: ObserveResult = { sessionID, queries: 0, kbTouches: 0, kbMisses: 0, gaps: 0 };
  const shapes: QueryShape[] = [];
  let corrections = 0;
  let errors = 0;

  d.transaction(() => {
    d.run(`DELETE FROM kb_touch WHERE session_id = ?`, [sessionID]);
    d.run(`DELETE FROM query_observations WHERE session_id = ?`, [sessionID]);
    d.run(`DELETE FROM retrieval_gaps WHERE session_id = ?`, [sessionID]);

    const insertTouch = d.query(
      `INSERT INTO kb_touch (session_id, at_turn, path, mode, query, matched) VALUES (?, ?, ?, ?, ?, ?)`
    );
    const insertQuery = d.query(
      `INSERT INTO query_observations
         (session_id, at_turn, engine, sql_raw, sql_norm, sql_sha, shape, tables_json, columns_json,
          filters_json, status, error_class, rows, latency_ms)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
    );
    const insertGap = d.query(
      `INSERT INTO retrieval_gaps (day, session_id, kind, query, scope_hint, detail) VALUES (?, ?, ?, ?, ?, ?)`
    );

    for (let i = 0; i < events.length; i++) {
      const e = events[i]!;
      if (e.kind === "user" && CORRECTION_RE.test(e.text)) corrections++;
      if (e.kind === "tool_result" && e.is_error) errors++;
      if (e.kind !== "tool_call" || !e.tool) continue;
      const args = e.args_json ? (JSON.parse(e.args_json) as Record<string, unknown>) : {};
      // The paired result is the next tool_result event for the same tool.
      const result = events.slice(i + 1).find((x) => x.kind === "tool_result" && x.tool === e.tool);

      if (KB_TOOLS.has(e.tool)) {
        const target = kbTarget(e.tool, args);
        if (target) {
          const miss = isMiss(result);
          insertTouch.run(sessionID, e.at_turn, target.path, target.mode, target.query, miss ? 0 : 1);
          out.kbTouches++;
          if (miss) {
            out.kbMisses++;
            insertGap.run(
              meta.day,
              sessionID,
              "searched_no_match",
              target.query || target.path,
              scopeHint(target.query || target.path),
              `KB looked up at turn ${e.at_turn} and produced nothing`
            );
            out.gaps++;
          }
          continue;
        }
      }

      const engine = QUERY_ENGINES[e.tool];
      if (engine) {
        const sql = typeof args.sql === "string" ? args.sql : "";
        if (!sql) continue;
        const n = normalizeSQL(sql);
        shapes.push(n.shape);
        insertQuery.run(
          sessionID,
          e.at_turn,
          engine,
          sql,
          n.norm,
          n.sha,
          n.shape,
          JSON.stringify(n.tables),
          JSON.stringify(n.columns),
          JSON.stringify(n.filters),
          result?.is_error ? "error" : "ok",
          result?.is_error ? errorClass(result.text) : null,
          result?.rows ?? null,
          result?.latency_ms ?? null
        );
        out.queries++;
      }
    }

    const turnCount = events.reduce((m, e) => Math.max(m, e.at_turn), 0);
    const probeChain = longestProbeChain(shapes);
    const signal = outcomeSignal(events, corrections);

    // The second gap kind: the agent never consulted the KB at all and then had
    // to grope (long probe chain) or failed. That is a discoverability failure,
    // and it is invisible if you only count searches that missed.
    if (out.kbTouches === 0 && (probeChain >= 2 || signal === "failed" || signal === "abandoned")) {
      d.run(
        `INSERT INTO retrieval_gaps (day, session_id, kind, query, scope_hint, detail) VALUES (?, ?, ?, ?, ?, ?)`,
        [
          meta.day,
          sessionID,
          "never_searched_then_failed",
          firstUserText(events).slice(0, 200),
          scopeHint(firstUserText(events)),
          `no KB read; probe_chain=${probeChain} outcome=${signal}`,
        ]
      );
      out.gaps++;
    }

    d.run(
      `INSERT INTO session_observations
         (session_id, day, user_id, turn_count, outcome_signal, error_count, correction_count,
          query_count, kb_touch_count, kb_miss_count, probe_chain_len, observed_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT(session_id) DO UPDATE SET
         day = excluded.day, user_id = excluded.user_id, turn_count = excluded.turn_count,
         outcome_signal = excluded.outcome_signal, error_count = excluded.error_count,
         correction_count = excluded.correction_count, query_count = excluded.query_count,
         kb_touch_count = excluded.kb_touch_count, kb_miss_count = excluded.kb_miss_count,
         probe_chain_len = excluded.probe_chain_len, observed_at = excluded.observed_at`,
      [
        sessionID,
        meta.day,
        meta.user_id,
        turnCount,
        signal,
        errors,
        corrections,
        out.queries,
        out.kbTouches,
        out.kbMisses,
        probeChain,
        nowISO(),
      ]
    );

    // Usage ledger: which knowledge actually got read. `helped` stays NULL —
    // whether it helped is a judgment, and this table only records facts.
    d.run(`DELETE FROM kb_usage WHERE session_id = ?`, [sessionID]);
    const hits = d
      .query<{ path: string }, [string]>(
        `SELECT path FROM kb_touch WHERE session_id = ? AND matched = 1`
      )
      .all(sessionID);
    for (const h of hits) {
      const kind = h.path.includes("/skills/") ? "skill" : h.path.includes("/derived/") ? "table" : "memory";
      d.run(`INSERT INTO kb_usage (day, session_id, kind, ref) VALUES (?, ?, ?, ?)`, [
        meta.day,
        sessionID,
        kind,
        h.path,
      ]);
    }
  })();

  return out;
}

function firstUserText(events: EventRow[]): string {
  return events.find((e) => e.kind === "user")?.text ?? "";
}

/** A coarse domain label from free text. Only a hint: canonicalizing scopes is a
 *  judgment the consolidate node makes, not something a regex should decide. */
function scopeHint(text: string): string {
  const t = text.toLowerCase();
  if (/工况|work_condition|working.?condition/.test(t)) return "work_condition";
  if (/soc|电池|battery|bms/.test(t)) return "battery";
  if (/充电|charg/.test(t)) return "charging";
  if (/doris|sql|表|table/.test(t)) return "doris";
  return "global";
}

function errorClass(text: string): string {
  const t = text.toLowerCase();
  if (/permission|denied|compute.?group/.test(t)) return "permission";
  if (/unknown column|no such column/.test(t)) return "unknown_column";
  if (/unknown table|no such table|doesn't exist/.test(t)) return "unknown_table";
  if (/syntax/.test(t)) return "syntax";
  if (/timeout|timed out/.test(t)) return "timeout";
  return "other";
}

/** Observe every ingested session. Idempotent: re-running replaces the
 *  rebuildable rows for each session. */
export function observeAll(): ObserveResult[] {
  const ids = db()
    .query<{ session_id: string }, []>(`SELECT session_id FROM sessions ORDER BY session_id`)
    .all();
  return ids.map((r) => observeSession(r.session_id));
}
