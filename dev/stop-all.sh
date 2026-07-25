#!/usr/bin/env bash
# Tear down everything dev/start-all.sh launched.
set -Eeuo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_DIR="${REPO_ROOT}/dev/logs"

log() { printf '[stop-all] %s\n' "$*"; }

# Kill by port so `go run`'s child binary and Next.js workers go down too.
for port in 8000 8083 8090; do
  pids="$(ss -ltnp 2>/dev/null | awk -v p=":${port}" '$4 ~ p" *$" || $4 ~ p"$" {print}' | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u || true)"
  if [ -n "${pids}" ]; then
    log "killing :${port} -> ${pids}"
    # shellcheck disable=SC2086
    kill ${pids} 2>/dev/null || true
  fi
done

sleep 2
for port in 8000 8083 8090; do
  pids="$(ss -ltnp 2>/dev/null | awk -v p=":${port}" '$4 ~ p" *$" || $4 ~ p"$" {print}' | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u || true)"
  if [ -n "${pids}" ]; then
    log "force killing :${port} -> ${pids}"
    # shellcheck disable=SC2086
    kill -9 ${pids} 2>/dev/null || true
  fi
done

rm -f "${LOG_DIR}"/*.pid 2>/dev/null || true

# Killing the orchestrator does NOT tear down the sandboxes it launched: the
# runsc containers, netns, and nft tables outlive it. On the next start the
# orchestrator waits bridge.reconnect_grace for those sandboxes to reconnect,
# they never do (their bridge client died with them), and every in-flight turn
# fails as `orchestrator_restart_reconnect_grace_expired` — the session appears
# stuck "thinking" with no reply. So reap the leftovers here.
#
# Only safe because this is a single-tenant dev box. Never do this on a shared or
# deployed host: use `scripts/harness-ctl safe-restart` there, which drains
# active conversations first.
if [ "${SKIP_SANDBOX_REAP:-0}" != "1" ]; then
  RUNSC_ROOT="${RUNSC_ROOT:-/var/lib/harness/runsc}"

  if command -v runsc >/dev/null 2>&1 && [ -d "${RUNSC_ROOT}" ]; then
    ids="$(runsc --root="${RUNSC_ROOT}" list 2>/dev/null | awk 'NR>1 && $1 != "" {print $1}' || true)"
    for id in ${ids}; do
      log "deleting sandbox ${id}"
      runsc --root="${RUNSC_ROOT}" delete --force "${id}" >/dev/null 2>&1 || true
    done
  fi

  for ns in $(ip netns list 2>/dev/null | awk '/^harness-gen-/ {print $1}'); do
    log "deleting netns ${ns}"
    ip netns delete "${ns}" >/dev/null 2>&1 || true
  done

  for table in $(nft list tables 2>/dev/null | awk '/harness_gen_/ {print $NF}'); do
    log "deleting nft table ${table}"
    nft delete table inet "${table}" >/dev/null 2>&1 || true
  done
fi

log "done"
