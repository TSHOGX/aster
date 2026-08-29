// warehouse.ts — a stand-in for the real warehouse (Doris), as local SQLite.
//
// The table track has to actually RUN the SQL an agent designs, or the "does
// this table help" question stays theoretical. In the real deployment this is a
// read-only Doris account; here it is a deterministic fixture so a build, its
// health checks, and its failure modes can be exercised offline.

import { Database } from "bun:sqlite";
import { existsSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";
import { files } from "./config.ts";

const WORK_CONDITIONS = ["WC01", "WC02", "WC03", "WC07", "WC11", "WC42"];
const MODELS = ["EX5", "EX7", "ZS11"];
const DAYS = ["2026-08-15", "2026-08-16", "2026-08-17", "2026-08-18", "2026-08-19"];

/** Deterministic PRNG: the same fixture every time, so a diff in output is a
 *  diff in the pipeline. */
function mulberry32(seed: number): () => number {
  let a = seed;
  return () => {
    a |= 0;
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export function buildWarehouse(): { telemetry: number; charge: number; models: number } {
  mkdirSync(dirname(files.warehouse), { recursive: true });
  const d = new Database(files.warehouse, { create: true });
  d.exec(`
    DROP TABLE IF EXISTS bms_telemetry;
    DROP TABLE IF EXISTS charge_session;
    DROP TABLE IF EXISTS vehicle_model;
    CREATE TABLE bms_telemetry (
      ts TEXT NOT NULL, dt TEXT NOT NULL, vehicle_id TEXT NOT NULL, model TEXT NOT NULL,
      work_condition TEXT NOT NULL, soc REAL NOT NULL, temp_c REAL NOT NULL, current_a REAL NOT NULL
    );
    CREATE TABLE charge_session (
      session_id TEXT NOT NULL, vehicle_id TEXT NOT NULL, dt TEXT NOT NULL,
      start_ts TEXT NOT NULL, end_ts TEXT NOT NULL, current_curve TEXT NOT NULL
    );
    CREATE TABLE vehicle_model (
      model TEXT NOT NULL, battery_type TEXT NOT NULL, capacity_kwh REAL NOT NULL, sample_rate_hz REAL NOT NULL
    );
  `);

  const rnd = mulberry32(20260820);
  const insT = d.query(
    `INSERT INTO bms_telemetry (ts, dt, vehicle_id, model, work_condition, soc, temp_c, current_a)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
  );
  const insC = d.query(
    `INSERT INTO charge_session (session_id, vehicle_id, dt, start_ts, end_ts, current_curve) VALUES (?, ?, ?, ?, ?, ?)`
  );
  let telemetry = 0;
  let charge = 0;

  d.transaction(() => {
    for (const [di, dt] of DAYS.entries()) {
      for (let v = 0; v < 8; v++) {
        const vehicle = `V${String(v + 1).padStart(3, "0")}`;
        const model = MODELS[v % MODELS.length]!;
        for (let i = 0; i < 20; i++) {
          // WC42 only appears on the last two days: a real coverage hole, which
          // is exactly what the coverage table is supposed to make visible.
          const pool = di >= 3 ? WORK_CONDITIONS : WORK_CONDITIONS.slice(0, 5);
          const wc = pool[Math.floor(rnd() * pool.length)]!;
          const hour = String(Math.floor(rnd() * 24)).padStart(2, "0");
          const min = String(Math.floor(rnd() * 60)).padStart(2, "0");
          insT.run(
            `${dt}T${hour}:${min}:00Z`,
            dt,
            vehicle,
            model,
            wc,
            Math.round(rnd() * 1000) / 10,
            Math.round((rnd() * 45 - 5) * 10) / 10,
            Math.round((rnd() * 300 - 150) * 10) / 10
          );
          telemetry++;
        }
        if (v % 3 === 0) {
          // Double-encoded JSON on purpose: the trace's "needs a second parse"
          // lesson has to be true of the data, not just asserted in a fixture.
          const curve = JSON.stringify([
            { t: 0, a: 12.5 },
            { t: 60, a: 33.1 },
          ]);
          insC.run(`CS-${dt}-${vehicle}`, vehicle, dt, `${dt}T01:00:00Z`, `${dt}T02:10:00Z`, JSON.stringify(curve));
          charge++;
        }
      }
    }
    for (const [i, m] of MODELS.entries()) {
      d.run(`INSERT INTO vehicle_model (model, battery_type, capacity_kwh, sample_rate_hz) VALUES (?, ?, ?, ?)`, [
        m,
        i % 2 === 0 ? "LFP" : "NCM",
        [52.5, 71.4, 63.0][i]!,
        [1, 2, 1][i]!,
      ]);
    }
  })();

  d.close();
  return { telemetry, charge, models: MODELS.length };
}

export function openWarehouse(): Database {
  if (!existsSync(files.warehouse)) throw new Error(`warehouse missing — run \`cli init\` first`);
  // Read-only by construction: the build step must not be able to mutate the
  // source, no matter what SQL an agent proposes.
  return new Database(files.warehouse, { readonly: true });
}

/** table -> columns, handed to the design node so it proposes SQL over columns
 *  that exist rather than columns it imagines. */
export function warehouseSchema(): Record<string, string[]> {
  const d = openWarehouse();
  try {
    const tables = d
      .query<{ name: string }, []>(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
      .all();
    const out: Record<string, string[]> = {};
    for (const t of tables) {
      out[t.name] = d
        .query<{ name: string }, []>(`SELECT name FROM pragma_table_info('${t.name}')`)
        .all()
        .map((c) => c.name);
    }
    return out;
  } finally {
    d.close();
  }
}
