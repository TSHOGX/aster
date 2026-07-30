#!/usr/bin/env bash
# One-shot local Linux setup for agent-runtime-platform (OrbStack/VM/bare metal).
#
# Portable pieces (safe to run on any Linux host with sudo):
#   apt packages, Go, Node, runsc, frontend npm install
#
# Per-machine pieces are NOT committed — this script only seeds gitignored
# files from *.example (see configure-local.sh). Fill secrets afterward.
#
# Usage (from anywhere):
#   ./dev/setup-linux.sh
#   ./dev/setup-linux.sh --configure-only
#   REPO=/path/to/checkout ./dev/setup-linux.sh
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
REPO="${REPO:-${REPO_ROOT}}"
CONFIGURE_ONLY=0

for arg in "$@"; do
  case "${arg}" in
    --configure-only) CONFIGURE_ONLY=1 ;;
    -h|--help)
      sed -n '2,16p' "$0"
      exit 0
      ;;
    *)
      echo "unknown argument: ${arg}" >&2
      exit 2
      ;;
  esac
done

export DEBIAN_FRONTEND=noninteractive

log() { printf '[setup-linux] %s\n' "$*"; }

die() { log "ERROR: $*"; exit 1; }

require_linux() {
  [[ "$(uname -s)" == "Linux" ]] || die "run this on Linux (e.g. OrbStack VM), not macOS"
}

host_go_arch() {
  case "$(uname -m)" in
    aarch64|arm64) printf 'arm64\n' ;;
    x86_64|amd64) printf 'amd64\n' ;;
    *) die "unsupported arch: $(uname -m)" ;;
  esac
}

host_runsc_arch() {
  # gVisor release tarball path uses aarch64 / x86_64.
  case "$(uname -m)" in
    aarch64|arm64) printf 'aarch64\n' ;;
    x86_64|amd64) printf 'x86_64\n' ;;
    *) die "unsupported arch: $(uname -m)" ;;
  esac
}

go_version_ok() {
  local v
  v="$(go env GOVERSION 2>/dev/null || true)"
  case "${v}" in
    go1.22*|go1.23*|go1.24*|go1.25*) return 0 ;;
    *) return 1 ;;
  esac
}

install_packages() {
  log "apt update + base packages"
  sudo apt-get update -y
  sudo apt-get install -y \
    curl ca-certificates gnupg lsb-release \
    build-essential git \
    python3 python3-venv python3-pip \
    nftables iproute2 iptables uidmap \
    debootstrap squashfs-tools rsync jq \
    unzip
}

install_go() {
  if command -v go >/dev/null 2>&1 && go_version_ok; then
    log "go: $(go version) (already ok)"
    return
  fi
  local arch go_ver=1.22.12
  arch="$(host_go_arch)"
  log "installing Go ${go_ver} (${arch})"
  curl -fsSL "https://go.dev/dl/go${go_ver}.linux-${arch}.tar.gz" -o /tmp/go.tgz
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf /tmp/go.tgz
  sudo ln -sfn /usr/local/go/bin/go /usr/local/bin/go
  sudo ln -sfn /usr/local/go/bin/gofmt /usr/local/bin/gofmt
  log "go: $(go version)"
}

install_node() {
  if command -v node >/dev/null 2>&1; then
    log "node: $(node -v) npm: $(npm -v) (already present)"
    return
  fi
  log "installing Node.js 22"
  curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash -
  sudo apt-get install -y nodejs
  log "node: $(node -v) npm: $(npm -v)"
}

install_runsc() {
  if command -v runsc >/dev/null 2>&1; then
    log "runsc: $(runsc --version 2>&1 | head -1)"
    return
  fi
  local arch tmp
  arch="$(host_runsc_arch)"
  log "installing gVisor runsc (${arch})"
  tmp="$(mktemp -d)"
  curl -fsSL "https://storage.googleapis.com/gvisor/releases/release/latest/${arch}/runsc" \
    -o "${tmp}/runsc"
  chmod +x "${tmp}/runsc"
  sudo mv "${tmp}/runsc" /usr/local/bin/runsc
  rm -rf "${tmp}"
  log "runsc: $(runsc --version 2>&1 | head -1)"
}

install_frontend_deps() {
  log "npm install (frontend)"
  cd "${REPO}/frontend"
  if [[ -d node_modules ]]; then
    log "node_modules present; skipping npm install"
  else
    npm install
  fi
}

[[ -d "${REPO}/dev" && -d "${REPO}/orchestrator" ]] || die "REPO does not look like a checkout: ${REPO}"
cd "${REPO}"
require_linux

if [[ "${CONFIGURE_ONLY}" -eq 0 ]]; then
  install_packages
  install_go
  install_node
  install_runsc
  install_frontend_deps
fi

log "seeding per-machine gitignored config"
"${SCRIPT_DIR}/configure-local.sh"

log "setup complete"
log "1) edit secrets in dev/model-proxy/.env (UPSTREAM_API_KEY=...)"
log "2) optional: edit sandbox-image/build.env for mirrors / DNS"
log "3) sudo SANDBOX_AGENT_DRIVERS=pi,sh ./sandbox-image/build-rootfs.sh"
log "4) sudo ./dev/start-all.sh"
