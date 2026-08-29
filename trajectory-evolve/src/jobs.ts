// jobs.ts — a single-writer queue in front of the evolution graph.
//
// Cron and a sandbox agent can both ask for a cycle. SQLite is one writer, and
// two graphs mutating the same candidate tables would corrupt support counts.
// So a cycle either holds the lock or returns `skipped` — it never queues behind
// a running job (a still-running nightly means the on-demand run would have
// seen a stale picture anyway).
//
// The MCP tool returns immediately with a job id. Pi's tool timeout is tens of
// seconds; a full cycle is minutes. Polling `evolve_job` is the contract.

import { closeSync, existsSync, mkdirSync, openSync, readFileSync, unlinkSync, writeSync } from "node:fs";
import { dirname, join } from "node:path";
import { files } from "./config.ts";
import { db, nowISO } from "./db.ts";
import { opEvolve, opIngest, opObserve, parseTrack, type Track } from "./ops.ts";

const JOBS_DDL = `
CREATE TABLE IF NOT EXISTS jobs (
  id           TEXT PRIMARY KEY,
  kind         TEXT NOT NULL,
  status       TEXT NOT NULL,           -- queued|running|done|error|skipped
  request_json TEXT NOT NULL DEFAULT '{}',
  result_json  TEXT NOT NULL DEFAULT '{}',
  error        TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,
  started_at   TEXT,
  finished_at  TEXT
);
`;

export type JobStatus = "queued" | "running" | "done" | "error" | "skipped";

export interface Job {
  id: string;
  kind: string;
  status: JobStatus;
  request: Record<string, unknown>;
  result: unknown;
  error: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface CycleRequest {
  ingest?: boolean;
  observe?: boolean;
  harvest?: boolean;
  track?: Track | string;
}

function jobsReady(): void {
  mkdirSync(dirname(files.db), { recursive: true });
  db().exec(JOBS_DDL);
}

/** Path of the single-writer cycle lock. Exported so tests can plant a holder. */
export function cycleLockPath(): string {
  return join(dirname(files.db), "evolve.lock");
}

function lockPath(): string {
  return cycleLockPath();
}

function pidAlive(pid: number): boolean {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

/** Exclusive lock. Stale locks (dead pid) are stolen so a crashed nightly
 *  cannot wedge the next day. */
export function acquireLock(): { ok: true; release: () => void } | { ok: false; holder: string } {
  const path = lockPath();
  mkdirSync(dirname(path), { recursive: true });
  const tryOnce = (): ReturnType<typeof acquireLock> => {
    try {
      const fd = openSync(path, "wx");
      writeSync(fd, `${process.pid}\n${nowISO()}\n`);
      closeSync(fd);
      return {
        ok: true,
        release: () => {
          try {
            unlinkSync(path);
          } catch {
            /* already gone */
          }
        },
      };
    } catch {
      let holder = "unknown";
      try {
        holder = readFileSync(path, "utf8").trim();
      } catch {
        /* unreadable */
      }
      const pid = Number(holder.split("\n")[0]);
      if (pidAlive(pid)) return { ok: false, holder };
      try {
        unlinkSync(path);
      } catch {
        /* raced */
      }
      return tryOnce();
    }
  };
  return tryOnce();
}

function insertJob(kind: string, request: unknown): string {
  jobsReady();
  const id = crypto.randomUUID();
  db().run(
    `INSERT INTO jobs (id, kind, status, request_json, created_at) VALUES (?, ?, 'queued', ?, ?)`,
    [id, kind, JSON.stringify(request ?? {}), nowISO()]
  );
  return id;
}

function patchJob(id: string, fields: Partial<Job> & { result?: unknown }): void {
  const sets: string[] = [];
  const args: string[] = [];
  if (fields.status) {
    sets.push("status = ?");
    args.push(fields.status);
  }
  if (fields.error !== undefined) {
    sets.push("error = ?");
    args.push(fields.error);
  }
  if (fields.result !== undefined) {
    sets.push("result_json = ?");
    args.push(JSON.stringify(fields.result));
  }
  if (fields.started_at) {
    sets.push("started_at = ?");
    args.push(fields.started_at);
  }
  if (fields.finished_at) {
    sets.push("finished_at = ?");
    args.push(fields.finished_at);
  }
  if (!sets.length) return;
  args.push(id);
  db().run(`UPDATE jobs SET ${sets.join(", ")} WHERE id = ?`, args);
}

export function getJob(id: string): Job | null {
  jobsReady();
  const r = db()
    .query<
      {
        id: string;
        kind: string;
        status: string;
        request_json: string;
        result_json: string;
        error: string;
        created_at: string;
        started_at: string | null;
        finished_at: string | null;
      },
      [string]
    >(
      `SELECT id, kind, status, request_json, result_json, error, created_at, started_at, finished_at
       FROM jobs WHERE id = ?`
    )
    .get(id);
  if (!r) return null;
  return {
    id: r.id,
    kind: r.kind,
    status: r.status as JobStatus,
    request: JSON.parse(r.request_json || "{}"),
    result: JSON.parse(r.result_json || "{}"),
    error: r.error,
    created_at: r.created_at,
    started_at: r.started_at,
    finished_at: r.finished_at,
  };
}

export function runningJob(): Job | null {
  jobsReady();
  const r = db()
    .query<{ id: string }, []>(`SELECT id FROM jobs WHERE status IN ('queued','running') ORDER BY created_at DESC LIMIT 1`)
    .get();
  return r ? getJob(r.id) : null;
}

async function runCycle(req: CycleRequest): Promise<Record<string, unknown>> {
  const result: Record<string, unknown> = {};
  if (req.ingest !== false) result.ingest = opIngest();
  if (req.observe !== false) result.observe = opObserve();
  const track = parseTrack(typeof req.track === "string" ? req.track : "all");
  result.evolve = await opEvolve(track, { harvestFirst: req.harvest !== false });
  return result;
}

/** Start a cycle in the background. Returns immediately. */
export function startCycleJob(req: CycleRequest = {}): Job {
  const existing = runningJob();
  if (existing) {
    const id = insertJob("cycle", req);
    patchJob(id, {
      status: "skipped",
      error: `another job is ${existing.status} (${existing.id}); not queued`,
      result: { skipped_because: existing.id },
      finished_at: nowISO(),
    });
    return getJob(id)!;
  }

  const lock = acquireLock();
  if (!lock.ok) {
    const id = insertJob("cycle", req);
    patchJob(id, {
      status: "skipped",
      error: `lock held by ${lock.holder}`,
      result: { holder: lock.holder },
      finished_at: nowISO(),
    });
    return getJob(id)!;
  }

  const id = insertJob("cycle", req);
  patchJob(id, { status: "running", started_at: nowISO() });

  // Detach: the MCP response must not wait for the graph. Errors land on the
  // job row, which evolve_job reads.
  void (async () => {
    try {
      const result = await runCycle(req);
      patchJob(id, { status: "done", result, finished_at: nowISO() });
    } catch (err) {
      patchJob(id, {
        status: "error",
        error: err instanceof Error ? err.message : String(err),
        finished_at: nowISO(),
      });
    } finally {
      lock.release();
    }
  })();

  return getJob(id)!;
}

/** Cron path: wait for the cycle. Same lock, so a live MCP job makes this skip. */
export async function runCycleForeground(req: CycleRequest = {}): Promise<Job> {
  const lock = acquireLock();
  const id = insertJob("cycle", req);
  if (!lock.ok) {
    patchJob(id, {
      status: "skipped",
      error: `lock held by ${lock.holder}`,
      result: { holder: lock.holder },
      finished_at: nowISO(),
    });
    return getJob(id)!;
  }
  patchJob(id, { status: "running", started_at: nowISO() });
  try {
    const result = await runCycle(req);
    patchJob(id, { status: "done", result, finished_at: nowISO() });
  } catch (err) {
    patchJob(id, {
      status: "error",
      error: err instanceof Error ? err.message : String(err),
      finished_at: nowISO(),
    });
  } finally {
    lock.release();
  }
  return getJob(id)!;
}

/** Test helper: true when a lock file exists. */
export function lockHeld(): boolean {
  return existsSync(lockPath());
}
