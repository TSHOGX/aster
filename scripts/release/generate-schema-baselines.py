#!/usr/bin/env python3
"""Refresh current schema fingerprints using synthetic databases only.

Historical DDL fixtures and reviewed fingerprints remain intact. This is a
source-development tool, never a deployment or production-database command.
"""
import json
import pathlib
import sqlite3
import subprocess
import tempfile

REPO = pathlib.Path(__file__).resolve().parents[2]
ORCHESTRATOR = REPO / "orchestrator"
CATALOG = ORCHESTRATOR / "internal/schemastate/baselines.json"
FIXTURES = ORCHESTRATOR / "internal/dbupgrade/testdata"
HELPER = '''package main
import("context";"fmt";"os";"harness-platform/orchestrator/internal/store";"harness-platform/orchestrator/internal/memorystore";"harness-platform/orchestrator/internal/schemastate")
func main(){ctx:=context.Background();if os.Args[1]=="store"{s,e:=store.Open(ctx,os.Args[2]);if e!=nil{panic(e)};defer s.Close();h,e:=schemastate.Fingerprint(ctx,s.DBForTest());if e!=nil{panic(e)};fmt.Println(h)}else{s,e:=memorystore.Open(ctx,os.Args[2]);if e!=nil{panic(e)};defer s.Close();h,e:=schemastate.Fingerprint(ctx,s.DBForTest());if e!=nil{panic(e)};fmt.Println(h)}}
'''


def main():
    catalog = json.loads(CATALOG.read_text())
    with tempfile.TemporaryDirectory(prefix="schema-baseline-", dir=ORCHESTRATOR / "cmd") as source, tempfile.TemporaryDirectory(prefix="harness-baselines-") as scratch:
        source, scratch = pathlib.Path(source), pathlib.Path(scratch)
        (source / "main.go").write_text(HELPER)
        binary = scratch / "open-schema"
        subprocess.run(["go", "build", "-buildvcs=false", "-o", str(binary), "./cmd/" + source.name], cwd=ORCHESTRATOR, check=True)
        for kind in ("store", "memorystore"):
            fixtures = sorted(FIXTURES.glob("*-store.sql")) if kind == "store" else [FIXTURES / "memorystore.sql"]
            targets = set()
            for index, fixture in enumerate([None] + fixtures):
                database = scratch / f"{kind}-{index}.db"
                if fixture is not None:
                    with sqlite3.connect(database) as db:
                        db.executescript(fixture.read_text())
                fingerprint = subprocess.check_output([str(binary), kind, str(database)], text=True).strip()
                targets.add(fingerprint)
                catalog[kind][fingerprint] = "target-current-" + ("fresh" if fixture is None else "upgraded")
            for fingerprint, label in catalog[kind].items():
                if label.startswith("target-") and fingerprint not in targets:
                    catalog[kind][fingerprint] = "previous-" + label.removeprefix("target-")
    CATALOG.write_text(json.dumps(catalog, indent=2) + "\n")


if __name__ == "__main__":
    main()
