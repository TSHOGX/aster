#!/usr/bin/env bash
# Shared local/CI gates. Dependency installation is explicit and separate.
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"
case "${1:-all}" in
  studio)
    go -C volumes-studio/backend build -mod=readonly ./...
    go -C volumes-studio/backend vet -mod=readonly ./...
    go -C volumes-studio/backend test -mod=readonly -race ./...
    node --test volumes-studio/tests/contract/*.test.mjs
    ;;
  studio-frontend)
    npm --prefix volumes-studio/frontend run lint
    npm --prefix volumes-studio/frontend test
    npm --prefix volumes-studio/frontend run build
    npm --prefix volumes-studio/frontend run typecheck
    ;;
  studio-browser)
    npm --prefix volumes-studio/frontend run test:browser
    ;;
  source)
    python3 scripts/check-public-docs.py
    scripts/check-lockfile-registry.sh
    scripts/check-lockfile-registry.sh admin-frontend/package-lock.json
    scripts/check-lockfile-registry.sh volumes-studio/frontend/package-lock.json
    scripts/check-lockfile-registry.sh orchestrator/internal/runtimerelease/package-lock.json
    while IFS= read -r -d '' script; do bash -n "$script"; done < <(git ls-files -z '*.sh')
    python3 -m unittest discover -s scripts/tests -p 'test_*.py'
    ;;
  backend)
    # Native RPC tests need Node; do not silently skip them on a Go-only runner.
    command -v node >/dev/null
    command -v python3 >/dev/null
    node --test orchestrator/internal/runtimerelease/full_session_identity.test.mjs orchestrator/internal/runtimerelease/platform-schedules.test.mjs
    go -C orchestrator build -mod=readonly ./...
    go -C orchestrator vet -mod=readonly ./...
    go -C orchestrator test -mod=readonly ./...
    # Compile every privileged test without executing it on a shared runner.
    go -C orchestrator test -mod=readonly -tags khoraintegration -run '^$' ./...
    go -C orchestrator test -mod=readonly -tags webintegration -run '^$' ./internal/edge
    go -C orchestrator test -mod=readonly -tags khoraintegration -run '^TestMixed(PercentilesUseNearestRank|ProjectionObservationIsIndependentAndRequiresReady)$' ./internal/application
    ;;
  race)
    command -v node >/dev/null
    # Ordinary backend runs the full cancellation stress; race keeps all tests
    # but uses its bounded iteration count and allows slow instrumented SQLite.
    go -C orchestrator test -mod=readonly -race -short -timeout 30m ./internal/product ./internal/agentrunner ./internal/runtimerelease
    ;;
  frontend-lint)
    npm --prefix frontend run lint
    npm --prefix admin-frontend run lint
    ;;
  frontend-unit)
    npm --prefix frontend test
    npm --prefix admin-frontend test
    ;;
  frontend-build)
    for frontend in frontend admin-frontend; do
      npm --prefix "$frontend" run build
      npm --prefix "$frontend" run typecheck:built
    done
    ;;
  frontend-ui)
    npm --prefix frontend run test:ui:built
    npm --prefix admin-frontend run test:ui:built
    ;;
  frontend-edge)
    go -C orchestrator test -mod=readonly -tags webintegration ./internal/edge -run TestProductionFrontendBoundary -count=1
    ;;
  frontend)
    "$0" frontend-lint
    "$0" frontend-unit
    "$0" frontend-build
    "$0" frontend-ui
    "$0" frontend-edge
    ;;
  all)
    "$0" source
    "$0" studio
    "$0" studio-frontend
    "$0" studio-browser
    "$0" backend
    "$0" frontend
    ;;
  *) echo 'Usage: scripts/check.sh [source|studio|studio-frontend|studio-browser|backend|race|frontend|frontend-lint|frontend-unit|frontend-build|frontend-ui|frontend-edge|all]' >&2; exit 2 ;;
esac
