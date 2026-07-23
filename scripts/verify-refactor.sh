#!/usr/bin/env bash
# Reusable verification harness for the redundancy/architecture refactor.
# Go tests can't run on the macOS host (Linux-only syscalls) -> use docker golang:1.26.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2
ROOT="$(pwd)"
fail=0

echo "=== [1/4] GOOS=linux go build ==="
GOOS=linux go build -C orchestrator ./... 2>&1 | tail -20 || fail=1

echo "=== [2/4] GOOS=linux go vet ==="
GOOS=linux go vet -C orchestrator ./... 2>&1 | tail -30 || fail=1

echo "=== [3/4] Go tests (docker golang:1.26) ==="
docker run --rm -v "$ROOT/orchestrator":/src -v "$(go env GOMODCACHE)":/go/pkg/mod \
  -w /src golang:1.26 go test ./... 2>&1 | tail -40
[ "${PIPESTATUS[0]}" -eq 0 ] || fail=1

echo "=== [4/4] Frontend typecheck + lint + test ==="
( cd frontend && npm run typecheck 2>&1 | tail -5 ) || fail=1
( cd frontend && npm run lint 2>&1 | tail -5 ) || fail=1
( cd frontend && npm test 2>&1 | tail -8 ) || fail=1

echo ""
if [ "$fail" -eq 0 ]; then echo "VERIFY_RESULT=GREEN"; else echo "VERIFY_RESULT=RED"; fi
exit "$fail"
