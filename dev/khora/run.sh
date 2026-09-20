#!/usr/bin/env bash
set -euo pipefail
base=$KHORA_TEST_BASE
mkdir /sys/fs/cgroup/driver
printf '%s\n' "$$" > /sys/fs/cgroup/driver/cgroup.procs
printf '+cpuset +cpu +io +memory +pids\n' > /sys/fs/cgroup/cgroup.subtree_control
export TMPDIR="$base/tmp" GOPROXY=off
"$KHORA_TEST_ENGINE" --addr 127.0.0.1:18191 --templates "$base/templates" \
  --state "$base/fs/engine-state" --store "$base/fs/engine.db" \
  --volumes-dir "$base/fs/volumes" --manual-cold-recovery \
  --node-memory-byte 4294967296 --node-disk-byte 6442450944 \
  --via-uid "$KHORA_INTEGRATION_VIA_UID" --reaper-interval 30s > "$base/engine.log" 2>&1 &
engine_pid=$!
finish() { kill -TERM "$engine_pid" 2>/dev/null || true; wait "$engine_pid" || true; }
trap finish EXIT
python3 - <<'PY'
import time,urllib.request
for attempt in range(150):
    try:
        with urllib.request.urlopen('http://127.0.0.1:18191/readyz',timeout=1) as response:
            if response.status==200: break
    except OSError: time.sleep(.1)
else: raise SystemExit('isolated engine readiness timed out')
PY
export KHORA_INTEGRATION_ENDPOINT=http://127.0.0.1:18191 KHORA_INTEGRATION_TEMPLATE=battery KHORA_INTEGRATION_MANIFEST="$base/manifest.json"
go -C "$KHORA_TEST_REPO/orchestrator" test -p 1 -tags khoraintegration \
 ./internal/sandbox/khora ./internal/server ./internal/retainedapp ./cmd/orchestrator \
 -run "$KHORA_TEST_RUN" -v -count=1 -timeout="$KHORA_TEST_TIMEOUT" 2>&1 | tee "$base/tests.log"

python3 - "$base/tests.log" <<'PYVERIFY'
import pathlib,re,sys
log=pathlib.Path(sys.argv[1]).read_text()
if not re.search(r'^--- PASS: TestKhora',log,re.M):
    raise SystemExit('no real Khora test executed successfully')
if re.search(r'^--- SKIP: TestKhora',log,re.M):
    raise SystemExit('selected real Khora tests skipped; this is not acceptance evidence')
PYVERIFY
