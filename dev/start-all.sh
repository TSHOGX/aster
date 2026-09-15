#!/usr/bin/env bash
# Local dev launcher for the whole platform inside the sbx OrbStack VM.
#
#   orchestrator  :8090  (root — needs netns/nft/runsc + /var/lib/harness*)
#   frontend      :8000  (checkout owner — Next.js dev, proxies /api to the orchestrator)
#   model proxy   :8083  — NOT started here by default (START_PROXY=0); run it yourself
#   MCP gateway   :8103  — never started here; run dev/mcp-gateway/gateway.py yourself
#
# Logs land in dev/logs/. Use dev/stop-all.sh to tear everything down.
# stop-all does not kill :8083/:8103 unless KILL_PROXY=1 / KILL_MCP=1.
set -Eeuo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
PROXY_DIR="${PROXY_DIR:-${REPO_ROOT}/dev/model-proxy}"
LOG_DIR="${REPO_ROOT}/dev/logs"
RUN_DIR="/var/lib/harness-publish/run"

mkdir -p "${LOG_DIR}"

log() { printf '[start-all] %s\n' "$*"; }

# Anything machine-specific goes here (gitignored), so no tracked file ever needs
# a local path, IP, or port. See dev/local.env.example.
if [ -f "${REPO_ROOT}/dev/local.env" ]; then
  log "loading dev/local.env"
  set -a
  # shellcheck disable=SC1091
  . "${REPO_ROOT}/dev/local.env"
  set +a
fi

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "start-all.sh must run under sudo (orchestrator needs root for netns/nft/runsc)" >&2
    exit 1
  fi
}
require_root

# The unprivileged user the frontend runs as: whoever owns the checkout, so this
# does not assume a particular username.
: "${FRONTEND_USER:=$(stat -c '%U' "${REPO_ROOT}")}"

# Detected, not hardcoded: OrbStack can hand this VM a different DHCP address.
VM_IP="$(ip -4 -o addr show scope global | awk 'NR==1 {split($4, a, "/"); print a[1]}')"
: "${VM_IP:=127.0.0.1}"

log "preparing state dirs"
install -d -m 0755 /var/lib/harness/state /var/lib/harness/sessions \
  /var/lib/harness/agent-homes /var/lib/harness/checkpoints \
  /var/lib/harness/runsc /var/lib/harness/agents-kb \
  /var/lib/harness/trajectories "${RUN_DIR}"

# ---------------------------------------------------------------- orchestrator
log "building orchestrator"
cd "${REPO_ROOT}/orchestrator"
sudo -u "${FRONTEND_USER}" go build -o "${REPO_ROOT}/dev/orchestrator" ./cmd/orchestrator

log "starting orchestrator on :8090"
# setsid so the process survives the launching shell (orb/ssh non-interactive
# shells otherwise take backgrounded children down with them).
HARNESS_COOKIE_INSECURE=1 \
HARNESS_TRUST_PROXY_HEADERS=0 \
HARNESS_REPO_ROOT="${REPO_ROOT}" \
HARNESS_LOG_LEVEL="${HARNESS_LOG_LEVEL:-info}" \
  setsid "${REPO_ROOT}/dev/orchestrator" \
  >"${LOG_DIR}/orchestrator.log" 2>&1 &
echo $! >"${LOG_DIR}/orchestrator.pid"

# ------------------------------------------------------------------ model proxy / MCP gateway
# Default: you start these. START_PROXY=1 launches the stand-in on :8083.
: "${START_PROXY:=0}"
http_code() {
  curl -sS -o /dev/null -w '%{http_code}' --max-time 2 "$1" 2>/dev/null || echo 000
}

if [ "${START_PROXY}" = "1" ]; then
  if [ "$(http_code http://127.0.0.1:8083/healthz)" = 200 ]; then
    log "model proxy already healthy on :8083 — not starting another"
  else
    log "starting model proxy on :8083"
    set -a
    # shellcheck disable=SC1091
    . "${PROXY_DIR}/.env"
    set +a
    cd "${PROXY_DIR}"
    setsid python3 proxy.py >"${LOG_DIR}/model-proxy.log" 2>&1 &
    echo $! >"${LOG_DIR}/model-proxy.pid"
  fi
else
  log "skipping model proxy (START_PROXY=0; manage :8083 yourself)"
fi
log "skipping MCP gateway (manage :8103 yourself; see dev/mcp-gateway/README.md)"

# --------------------------------------------------------------------- frontend
log "starting frontend on :8000"
cd "${REPO_ROOT}/frontend"
setsid sudo -u "${FRONTEND_USER}" env PORT=8000 \
  HARNESS_API_BASE_URL=http://127.0.0.1:8090 \
  HARNESS_DEV_ORIGINS="${HARNESS_DEV_ORIGINS:-${VM_IP},sbx.orb.local,localhost,127.0.0.1}" \
  npm run dev >"${LOG_DIR}/frontend.log" 2>&1 &
echo $! >"${LOG_DIR}/frontend.pid"

log "waiting for health (orchestrator + frontend; proxy/mcp are external)"
for i in $(seq 1 60); do
  orch=$(http_code http://127.0.0.1:8090/healthz)
  front=$(http_code http://127.0.0.1:8000/api/healthz)
  proxy=$(http_code http://127.0.0.1:8083/healthz)
  mcp=$(http_code http://127.0.0.1:8103/healthz)
  if [ "${orch}" = 200 ] && [ "${front}" = 200 ]; then
    log "stack healthy (orchestrator=${orch} frontend=${front} proxy=${proxy} mcp=${mcp})"
    log "open http://${VM_IP}:8000 from the host browser"
    if [ "${proxy}" != 200 ]; then
      log "reminder: bring up model proxy on :8083 (correlation socket under ${RUN_DIR})"
    fi
    if [ "${mcp}" != 200 ]; then
      log "reminder: bring up MCP gateway on :8103 if dingpan tools are needed"
    fi
    exit 0
  fi
  sleep 2
done

log "TIMEOUT waiting for health: orchestrator=${orch} frontend=${front} proxy=${proxy} mcp=${mcp}"
log "check ${LOG_DIR}/*.log"
exit 1
