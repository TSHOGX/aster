# Database Split Runbook (orchestrator.db → memory.db)

Moves the 9 trajectory-distillation tables out of `orchestrator.db` into a
sibling `memory.db`, in one maintenance window with the orchestrator stopped.
Both directions are implemented and rehearsed; the reverse migration is the only
rollback path once step 5 has run.

Tool: `orchestrator db-split` (a subcommand of the orchestrator binary, so it
uses the same pinned `modernc.org/sqlite` driver and needs no `sqlite3` on the
host).

**Tables moved:** `trajectory_snapshots`, `trajectory_events`,
`extraction_state`, `episode_memories`, `semantic_memories`, `skill_candidates`,
`raw_sources`, `evolution_runs`, `agent_calls`. The other 25 stay in
`orchestrator.db`.

**Schema change:** the 5 `sessions(id)` foreign keys (on
`trajectory_snapshots`, `trajectory_events`, `extraction_state`,
`episode_memories`, `raw_sources`) are dropped in `memory.db` — cross-file
foreign keys are impossible in SQLite. All other constraints, primary keys and
indexes are preserved. The reverse migration restores the FKs.

Paths below assume the `publish` deployment (`/var/lib/harness-publish/state/`).
Adjust for your environment; the default is `HARNESS_DB_PATH`'s directory.

---

## Forward migration

### 1. Pre-flight (service still running)

```bash
DB=/var/lib/harness-publish/state/orchestrator.db
STATE_DIR=$(dirname "$DB")

# Disk: VACUUM rebuilds the file, so you need free space >= the DB size,
# and the backup in step 3 needs about as much again.
df -h "$STATE_DIR"
sudo du -sh "$DB"

# Plan + current state, changes nothing. Safe to run against the live service.
sudo /usr/local/bin/orchestrator db-split --dry-run --control "$DB"
```

Read the dry-run output. It prints per-table row counts, which side each table is
currently on, the exact plan, and whether free space is sufficient for the
VACUUM. Do not continue if it reports a table missing from both files.

### 2. Stop the orchestrator

```bash
harness-ctl maintenance on
sudo systemctl stop harness-publish-orchestrator
systemctl is-active harness-publish-orchestrator   # expect: inactive
```

The migration drops tables from the live control database. The service must be
stopped, not merely idle.

### 3. Backup (mandatory)

```bash
TS=$(date +%Y%m%d-%H%M%S)
sudo cp -a "$DB" "$DB.pre-split.$TS"
# WAL sidecars, if present after the clean stop:
sudo cp -a "$DB-wal" "$DB-wal.pre-split.$TS" 2>/dev/null || true
sudo cp -a "$DB-shm" "$DB-shm.pre-split.$TS" 2>/dev/null || true
ls -lh "$DB".pre-split.* "$DB"-wal.pre-split.* 2>/dev/null
```

### 4. Forward migrate

```bash
sudo /usr/local/bin/orchestrator db-split --control "$DB"
```

Type `yes` at the confirmation, or pass `--yes` for a scripted run. Watch the
four steps: copy → verify → drop → VACUUM. It is idempotent and resumable — if
it dies partway, re-run the same command.

If verification fails it refuses to drop anything and exits non-zero. The source
database is untouched and still authoritative; investigate, then re-run.

### 5. Verify

```bash
# Expect 25 tables in orchestrator.db, and the 9 in memory.db.
sudo /usr/local/bin/orchestrator db-split --dry-run --control "$DB"
```

The dry run should report `nothing to do — all 9 tables are already in
memory.db`. Then confirm the files:

```bash
ls -lh "$STATE_DIR"/orchestrator.db "$STATE_DIR"/memory.db
```

### 6. Start and smoke

```bash
sudo systemctl start harness-publish-orchestrator
sleep 3
harness-ctl status
harness-ctl maintenance off
```

Confirm the workbench loads, an existing session's history renders, and a new
message round-trips.

---

## Rollback (reverse migration)

Use this if the application misbehaves after the split. It moves all 9 tables
back into `orchestrator.db`, restoring the original DDL **including the 5
`sessions(id)` foreign keys**, then drops them from `memory.db`.

```bash
harness-ctl maintenance on
sudo systemctl stop harness-publish-orchestrator

# Back up both files first — the rollback drops tables from memory.db.
TS=$(date +%Y%m%d-%H%M%S)
sudo cp -a "$DB" "$DB.pre-rollback.$TS"
sudo cp -a "$STATE_DIR/memory.db" "$STATE_DIR/memory.db.pre-rollback.$TS"

# Preview, then run.
sudo /usr/local/bin/orchestrator db-split --reverse --dry-run --control "$DB"
sudo /usr/local/bin/orchestrator db-split --reverse --control "$DB"

# Confirm restoration, then restart.
sudo /usr/local/bin/orchestrator db-split --reverse --dry-run --control "$DB"
sudo systemctl start harness-publish-orchestrator
harness-ctl status
harness-ctl maintenance off
```

You must also redeploy the orchestrator binary that reads both tables from
`orchestrator.db`, since the post-split binary expects `memory.db`.

If the rollback reports foreign-key violations, those are rows whose `sessions`
parent was deleted while the data lived in `memory.db`. The rollback restores the
rows faithfully rather than dropping them; list them with
`PRAGMA foreign_key_check` and delete if unwanted.

### Last resort

If the reverse migration itself fails, restore the step-3 backup:

```bash
sudo systemctl stop harness-publish-orchestrator
sudo cp -a "$DB.pre-split.$TS" "$DB"
sudo rm -f "$DB-wal" "$DB-shm"
sudo systemctl start harness-publish-orchestrator
```

This loses any memory-table writes made after the split.

---

## Flags

| Flag | Effect |
|---|---|
| `--dry-run` | Report plan and current state; change nothing |
| `--reverse` | Roll back: `memory.db` → `orchestrator.db`, FKs restored |
| `--control PATH` | Path to `orchestrator.db` (default: `HARNESS_DB_PATH` / config) |
| `--memory PATH` | Path to `memory.db` (default: beside `orchestrator.db`) |
| `--no-vacuum` | Skip VACUUM after a verified drop (space stays reusable in-file) |
| `--yes` | Skip the interactive confirmation |

Exits non-zero on any failure.

## Safety properties

- **Verification is a hard gate.** Row counts *and* per-column signatures (name,
  type, NOT NULL, DEFAULT, PK position) must match before anything is dropped. A
  silently dropped column fails the gate.
- **Idempotent and resumable.** Each table's copy commits in its own
  destination-only transaction, so per-table state is derived from the files
  themselves. Re-running after an abort completes the job without duplicating
  rows. Running against an already-migrated pair is a clean no-op.
- **Never destroys data on failed verification.** The drop phase is only reached
  after every table passes.
- **Live schema, not hardcoded columns.** Column sets are read from each file's
  `sqlite_master` / `pragma_table_info`, so `ALTER TABLE ... ADD COLUMN`
  migrations already applied in production (e.g.
  `trajectory_events.native_id` / `parent_id`) are carried across automatically.
- **VACUUM is space-checked** and only runs after a verified drop. A failed
  VACUUM is reported as a warning, not a migration failure.

Rehearsals for both directions, resume-after-abort, the no-op case and the
mismatch refusal live in `orchestrator/internal/dbsplit/dbsplit_test.go`:

```bash
cd orchestrator && go test ./internal/dbsplit/ -v
```
