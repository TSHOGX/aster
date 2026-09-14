#!/usr/bin/env python3
"""Rehearse store-only migration on disposable databases; never start services.

Requires git, Go, Python 3. With --snapshot-dir, reads a stopped, consistent
backup containing orchestrator.db and memory.db, then migrates ONLY copies.
Logs table names/counts/digests, never row contents. Temporary copies are removed.
"""
import argparse
import hashlib
import json
from pathlib import Path
import sqlite3
import subprocess
import tempfile

BASE = "669542d4ab3453509a6872e996cdf38bc7331edf"
HELPER = '''package main
import (
 "context"
 "os"
 "path/filepath"
 "harness-platform/orchestrator/internal/store"
 "harness-platform/orchestrator/internal/memorystore"
)
func main() {
 ctx := context.Background()
 s, err := store.OpenWithOptions(ctx, filepath.Join(os.Args[1], "orchestrator.db"), store.Options{AgentHomesRoot: filepath.Join(os.Args[1], "homes")})
 if err != nil { panic(err) }; if err = s.Close(); err != nil { panic(err) }
 m, err := memorystore.Open(ctx, filepath.Join(os.Args[1], "memory.db"))
 if err != nil { panic(err) }; if err = m.Close(); err != nil { panic(err) }
}
'''


def run(*args, **kwargs):
    subprocess.run(args, check=True, **kwargs)


def build(repo, rev, root):
    root.mkdir()
    archive = subprocess.check_output(["git", "-C", str(repo), "archive", rev, "orchestrator"])
    run("tar", "-x", "-C", str(root), input=archive)
    cmd = root / "orchestrator/cmd/upgrade-rehearsal"
    cmd.mkdir()
    (cmd / "main.go").write_text(HELPER)
    binary = root / "open-stores"
    run("go", "build", "-buildvcs=false", "-o", str(binary), "./cmd/upgrade-rehearsal", cwd=root / "orchestrator")
    return binary


def quote(name):
    return '"' + name.replace('"', '""') + '"'


def inspect(path, columns=None):
    with sqlite3.connect(path.as_uri() + "?mode=ro", uri=True) as db:
        assert db.execute("PRAGMA integrity_check").fetchall() == [("ok",)], path.name + " integrity failure"
        assert not db.execute("PRAGMA foreign_key_check").fetchall(), path.name + " foreign key failure"
        tables = [r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")]
        result = {}
        for table in (columns if columns is not None else tables):
            names = columns[table]["columns"] if columns is not None else [r[1] for r in db.execute("PRAGMA table_info(" + quote(table) + ")")]
            hashes = sorted(hashlib.sha256(repr(row).encode()).hexdigest() for row in db.execute("SELECT " + ','.join(map(quote, names)) + " FROM " + quote(table)))
            result[table] = {"columns": names, "count": len(hashes), "digest": hashlib.sha256(''.join(hashes).encode()).hexdigest()}
        return result


def seed(root):
    now = "2026-09-11T00:00:00Z"
    with sqlite3.connect(root / "orchestrator.db") as db:
        db.execute("PRAGMA foreign_keys=ON")
        db.execute("INSERT INTO users(id,display_name,created_at) VALUES('upgrade-user','升级演练',?)", (now,))
        db.execute("INSERT INTO sessions(id,user_id,status,driver_id,mode,workspace,restore_id,created_at,updated_at) VALUES('upgrade-session','upgrade-user','created','pi','agent','/isolated/workspace','upgrade-restore',?,?)", (now, now))
        for seq, status in enumerate(('completed', 'queued', 'canceled'), 1):
            db.execute("INSERT INTO turns(session_id,sequence,role,content,status,attempt,created_at) VALUES('upgrade-session',?,'user',?,?,0,?)", (seq, '历史消息-' + status, status, now))
            db.execute("INSERT INTO messages(session_id,role,content,created_at) VALUES('upgrade-session','user',?,?)", ('历史消息-' + status, now))
    with sqlite3.connect(root / "memory.db") as db:
        db.execute("INSERT INTO trajectory_snapshots(session_id,status,agent,created_at) VALUES('upgrade-session','completed','pi',?)", (now,))
        db.execute("INSERT INTO episode_memories(session_id,task,created_at) VALUES('upgrade-session','不可重建的总结',?)", (now,))
        db.execute("INSERT INTO semantic_memories(type,statement,status,evidence_session_ids_json,first_seen_at,last_seen_at,updated_at) VALUES('fact','保留审核与证据','accepted','[\"s1\",\"s2\"]',?,?,?)", (now, now, now))
        db.execute("INSERT INTO skill_candidates(target_skill_name,body_md,status,evidence_session_ids_json,created_at,updated_at) VALUES('upgrade-skill','# 历史技能','published','[\"s1\",\"s2\",\"s3\"]',?,?)", (now, now))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--target', default='HEAD', help='Committed target revision')
    parser.add_argument('--snapshot-dir', type=Path, help='Stopped consistent backup, never the live state directory')
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[1]
    target = subprocess.check_output(['git', '-C', str(repo), 'rev-parse', args.target], text=True).strip()
    with tempfile.TemporaryDirectory(prefix='upgrade-669542d-') as tmp:
        root = Path(tmp)
        state = root / 'state'
        state.mkdir()
        if args.snapshot_dir:
            for name in ('orchestrator.db', 'memory.db'):
                source = (args.snapshot_dir / name).resolve()
                if not source.is_file():
                    raise FileNotFoundError(source)
                with sqlite3.connect(source.as_uri() + '?mode=ro', uri=True) as src, sqlite3.connect(state / name) as dst:
                    src.backup(dst)
        else:
            old = build(repo, BASE, root / 'old')
            run(str(old), str(state))
            seed(state)
        names = ('orchestrator.db', 'memory.db')
        before = {n: inspect(state / n) for n in names}
        new = build(repo, target, root / 'new')
        run(str(new), str(state))
        for n in names:
            assert inspect(state / n, before[n]) == before[n], n + ': historical rows changed'
        after = {n: inspect(state / n) for n in names}
        assert after['memory.db'] == before['memory.db'], 'memory schema/data changed'
        with sqlite3.connect(state / 'orchestrator.db') as db:
            assert db.execute("SELECT COUNT(*) FROM users WHERE role NOT IN ('user','admin')").fetchone()[0] == 0
            assert db.execute("SELECT COUNT(*) FROM feature_flags").fetchone()[0] >= 4
            if not args.snapshot_dir:
                assert db.execute("SELECT COUNT(*) FROM turns WHERE materializes_message=0 AND auto_dispatch=1").fetchone()[0] == 3
                assert db.execute("SELECT COUNT(*) FROM messages WHERE turn_id IS NULL").fetchone()[0] == 3
                assert db.execute("SELECT COUNT(*) FROM schedule_runs").fetchone()[0] == 0
        run(str(new), str(state))
        assert {n: inspect(state / n) for n in names} == after, 'second open changed rows/schema'
        print(json.dumps({'base': BASE, 'target': target, 'source': 'snapshot-copy' if args.snapshot_dir else 'synthetic-old-version', 'result': 'PASS: old columns/rows preserved, integrity/FKs valid, repeated open stable', 'before': before, 'after': after}, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
