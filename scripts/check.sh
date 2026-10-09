#!/usr/bin/env bash
# Shared local/CI gates. Dependency installation is explicit and separate.
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"
case "${1:-all}" in
  source)
    python3 scripts/check-public-docs.py
    scripts/check-lockfile-registry.sh
    scripts/check-lockfile-registry.sh orchestrator/internal/runtimerelease/package-lock.json
    while IFS= read -r -d '' script; do bash -n "$script"; done < <(git ls-files -z '*.sh')
    python3 -m unittest discover -s scripts/tests -p 'test_*.py'
    ;;
  backend)
    # Native RPC tests need Node; do not silently skip them on a Go-only runner.
    command -v node >/dev/null
    command -v python3 >/dev/null
    node --test orchestrator/internal/runtimerelease/full_session_identity.test.mjs
    go -C orchestrator build -mod=readonly ./...
    go -C orchestrator vet -mod=readonly ./...
    go -C orchestrator test -mod=readonly ./...
    preview_check_dir=$(mktemp -d)
    trap 'rm -rf "$preview_check_dir"' EXIT
    CGO_ENABLED=0 go -C orchestrator build -mod=readonly -o "$preview_check_dir/preview-worker" ./cmd/preview-worker
    PREVIEW_TEST_WORKER="$preview_check_dir/preview-worker" go -C orchestrator test -mod=readonly ./internal/product -run '^TestPreviewJobs' -count=1
    # Compile every privileged test without executing it on a shared runner.
    go -C orchestrator test -mod=readonly -tags khoraintegration -run '^$' ./...
    go -C orchestrator test -mod=readonly -tags khoraintegration -run '^TestMixed(PercentilesUseNearestRank|ProjectionObservationIsIndependentAndRequiresReady)$' ./internal/application
    ;;
  race)
    command -v node >/dev/null
    go -C orchestrator test -mod=readonly -race ./internal/product ./internal/agentrunner ./internal/runtimerelease
    ;;
  frontend)
    cd frontend
    npm run lint
    npm run typecheck
    npm test
    npm run build
    npm run test:ui:built
    ;;
  all)
    "$0" source
    "$0" backend
    "$0" frontend
    ;;
  *) echo 'Usage: scripts/check.sh [source|backend|race|frontend|all]' >&2; exit 2 ;;
esac
