// observe/views.ts — the pain views and the four quality quadrants. Zero LLM.
//
// Everything here is a GROUP BY over the observation tables. These views are the
// ONLY inputs the two evolution tracks are allowed to read, which is what keeps
// agent cost proportional to distinct problems rather than to session volume:
// an empty pain view ends a cycle before the first agent call.

import { db } from "../db.ts";
import { limits } from "../config.ts";

export interface QueryPattern {
  sql_sha: string;
  sql_norm: string;
  shape: string;
  sessions: number;
  occurrences: number;
  err_rate: number;
  avg_latency: number;
  tables_json: string;
  columns_json: string;
  first_seen: string;
  last_seen: string;
}

/** Rebuild query_patterns from query_observations. */
export function refreshQueryPatterns(): number {
  const d = db();
  d.transaction(() => {
    d.run(`DELETE FROM query_patterns`);
    d.run(
      `INSERT INTO query_patterns
         (sql_sha, sql_norm, shape, sessions, occurrences, err_rate, avg_latency,
          tables_json, columns_json, first_seen, last_seen)
       SELECT q.sql_sha,
              MIN(q.sql_norm),
              MIN(q.shape),
              COUNT(DISTINCT q.session_id),
              COUNT(*),
              CAST(SUM(CASE WHEN q.status = 'error' THEN 1 ELSE 0 END) AS REAL) / COUNT(*),
              COALESCE(AVG(q.latency_ms), 0),
              MIN(q.tables_json),
              MIN(q.columns_json),
              MIN(s.day),
              MAX(s.day)
       FROM query_observations q
       JOIN sessions s ON s.session_id = q.session_id
       GROUP BY q.sql_sha`
    );
  })();
  return d.query<{ n: number }, []>(`SELECT COUNT(*) AS n FROM query_patterns`).get()!.n;
}

export interface Pain {
  key: string;
  shape: string;
  sessions: number;
  occurrences: number;
  err_rate: number;
  avg_latency: number;
  tables: string[];
  columns: string[];
  examples: string[];
  /** Ranking score. Only used to ORDER candidates, never to accept or reject
   *  one — that judgment belongs to the mine/judge agents. */
  score: number;
}

/** Query pains: fingerprints that recur across sessions, error out, or take a
 *  long time. Grouped by (shape, table set) because that is the granularity a
 *  single lookup table can retire — one table per query intent. */
export function queryPains(): Pain[] {
  // Grouped straight off the observations, because the distinct-SESSION count is
  // the whole gate and it cannot be recovered from per-fingerprint rollups: three
  // sessions asking the same question with different WHERE constants are three
  // fingerprints of one session each, which looks like no shared pain at all.
  const groups = db()
    .query<
      {
        shape: string;
        tables_json: string;
        sessions: number;
        occurrences: number;
        err_rate: number;
        avg_latency: number;
      },
      []
    >(
      `SELECT shape, tables_json,
              COUNT(DISTINCT session_id) AS sessions,
              COUNT(*) AS occurrences,
              CAST(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) AS REAL) / COUNT(*) AS err_rate,
              COALESCE(AVG(latency_ms), 0) AS avg_latency
       FROM query_observations
       GROUP BY shape, tables_json`
    )
    .all();

  const out: Pain[] = groups.map((g) => {
    const tables = JSON.parse(g.tables_json) as string[];
    const patterns = db()
      .query<{ sql_norm: string; columns_json: string }, [string, string]>(
        `SELECT sql_norm, columns_json FROM query_patterns WHERE shape = ? AND tables_json = ?
         ORDER BY occurrences DESC LIMIT 3`
      )
      .all(g.shape, g.tables_json);
    const columns = new Set<string>();
    for (const p of patterns) for (const c of JSON.parse(p.columns_json) as string[]) columns.add(c);
    return {
      key: `${g.shape}:${tables.join("+")}`,
      shape: g.shape,
      sessions: g.sessions,
      occurrences: g.occurrences,
      err_rate: g.err_rate,
      avg_latency: g.avg_latency,
      tables,
      columns: [...columns].sort(),
      examples: patterns.map((p) => p.sql_norm),
      score: 0,
    };
  });

  const kept = out.filter(
    // Cross-session evidence only. One session that ran a query five times is a
    // habit, not a shared pain, and building a table from it overfits.
    (p) => p.sessions >= limits.minPainSessions
  );
  for (const p of kept) {
    p.score =
      p.occurrences * 2 +
      p.sessions * 5 +
      p.err_rate * 20 +
      Math.min(p.avg_latency / 1000, 20) +
      (p.shape === "analysis" ? 0 : 6);
  }
  return kept.sort((a, b) => b.score - a.score);
}

export interface Gap {
  kind: string;
  scope_hint: string;
  query: string;
  sessions: number;
  detail: string;
}

/** Retrieval gaps grouped by scope hint: where the KB failed to answer. */
export function retrievalGaps(): Gap[] {
  return db()
    .query<Gap, []>(
      `SELECT kind, scope_hint, MIN(query) AS query, COUNT(DISTINCT session_id) AS sessions, MIN(detail) AS detail
       FROM retrieval_gaps
       GROUP BY kind, scope_hint
       ORDER BY sessions DESC, scope_hint`
    )
    .all();
}

export interface Quadrants {
  /** Knowledge the KB should have had and did not. */
  miss: number;
  /** Extracted knowledge that was rejected/vetoed/found stale. */
  noise: number;
  /** Accepted knowledge nobody has read. */
  unused: number;
  /** Knowledge that WAS read, yet the session still failed or got corrected. */
  unusable: number;
  detail: {
    missByScope: Array<{ scope: string; sessions: number }>;
    noiseByReason: Array<{ reason: string; n: number }>;
    unusedRefs: string[];
    unusableSessions: string[];
  };
}

/** The four quadrants are the gradient for track A. Without them "self-evolving
 *  extraction" has no direction and degenerates into rewriting prompts by
 *  vibes: every one of these numbers is computed from behaviour, not opinion. */
export function quadrants(): Quadrants {
  const d = db();
  const miss = d.query<{ n: number }, []>(
    `SELECT COUNT(DISTINCT session_id || '|' || scope_hint) AS n FROM retrieval_gaps`
  ).get()!.n;

  const noise = d.query<{ n: number }, []>(
    `SELECT COUNT(*) AS n FROM semantic_memories
     WHERE shadow = 0 AND (status = 'deprecated' OR verify_verdict IN ('stale', 'fixed'))`
  ).get()!.n;

  const unused = d.query<{ n: number }, []>(
    `SELECT COUNT(*) AS n FROM semantic_memories m
     WHERE m.shadow = 0 AND m.status = 'accepted'
       AND NOT EXISTS (SELECT 1 FROM kb_usage u WHERE u.kind = 'memory' AND u.ref LIKE '%' || m.canonical_scope || '%')`
  ).get()!.n;

  // Read the knowledge, still went wrong: the phrasing (when_to_apply), not the
  // content, is what failed. A different fix than a miss, so counted apart.
  const unusable = d.query<{ n: number }, []>(
    `SELECT COUNT(*) AS n FROM session_observations o
     WHERE o.kb_touch_count > o.kb_miss_count
       AND (o.correction_count > 0 OR o.outcome_signal IN ('failed', 'abandoned'))`
  ).get()!.n;

  return {
    miss,
    noise,
    unused,
    unusable,
    detail: {
      missByScope: d
        .query<{ scope: string; sessions: number }, []>(
          `SELECT scope_hint AS scope, COUNT(DISTINCT session_id) AS sessions
           FROM retrieval_gaps GROUP BY scope_hint ORDER BY sessions DESC`
        )
        .all(),
      noiseByReason: d
        .query<{ reason: string; n: number }, []>(
          `SELECT COALESCE(verify_verdict, status) AS reason, COUNT(*) AS n
           FROM semantic_memories WHERE shadow = 0 AND (status = 'deprecated' OR verify_verdict IN ('stale','fixed'))
           GROUP BY reason`
        )
        .all(),
      unusedRefs: d
        .query<{ statement: string }, []>(
          `SELECT statement FROM semantic_memories WHERE shadow = 0 AND status = 'accepted' LIMIT 5`
        )
        .all()
        .map((r) => r.statement),
      unusableSessions: d
        .query<{ session_id: string }, []>(
          `SELECT session_id FROM session_observations
           WHERE kb_touch_count > kb_miss_count AND (correction_count > 0 OR outcome_signal IN ('failed','abandoned'))`
        )
        .all()
        .map((r) => r.session_id),
    },
  };
}

/** Sessions worth spending an extraction call on, with their gate signals. */
export interface HarvestTarget {
  session_id: string;
  turn_count: number;
  error_count: number;
  correction_count: number;
  outcome_signal: string;
}

/** The sessions behind one pain, with the question that started them. Used to
 *  replay real historical asks against a candidate table, and to prove the
 *  evidence is cross-session before anything is published. */
export function painEvidence(shape: string, tables: string[]): Array<{ session_id: string; question: string }> {
  const like = `%${tables[0] ?? ""}%`;
  return db()
    .query<{ session_id: string; question: string }, [string, string]>(
      `SELECT q.session_id,
              COALESCE((SELECT e.text FROM trajectory_events e
                        WHERE e.session_id = q.session_id AND e.kind = 'user'
                        ORDER BY e.seq LIMIT 1), '') AS question
       FROM query_observations q
       WHERE q.shape = ? AND q.tables_json LIKE ?
       GROUP BY q.session_id
       ORDER BY q.session_id`
    )
    .all(shape, like);
}

export function harvestTargets(): HarvestTarget[] {
  return db()
    .query<HarvestTarget, []>(
      `SELECT session_id, turn_count, error_count, correction_count, outcome_signal
       FROM session_observations ORDER BY session_id`
    )
    .all();
}
