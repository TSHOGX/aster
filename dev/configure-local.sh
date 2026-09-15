#!/usr/bin/env bash
# Seed per-machine gitignored config from committed *.example files.
#
# Safe to re-run: existing files are left alone unless --force is passed.
# Detects fake-ip DNS interception and wires SANDBOX_BUILD_DNS_HOOK when needed.
#
# Usage:
#   ./dev/configure-local.sh
#   ./dev/configure-local.sh --force
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
FORCE=0

for arg in "$@"; do
  case "${arg}" in
    --force) FORCE=1 ;;
    -h|--help)
      sed -n '2,12p' "$0"
      exit 0
      ;;
    *)
      echo "unknown argument: ${arg}" >&2
      exit 2
      ;;
  esac
done

log() { printf '[configure-local] %s\n' "$*"; }

copy_example() {
  local src="$1" dest="$2"
  if [[ -f "${dest}" && "${FORCE}" -eq 0 ]]; then
    log "keep existing ${dest#"${REPO_ROOT}/"}"
    return
  fi
  if [[ ! -f "${src}" ]]; then
    log "skip missing example ${src#"${REPO_ROOT}/"}"
    return
  fi
  cp "${src}" "${dest}"
  log "wrote ${dest#"${REPO_ROOT}/"} from example"
}

dns_is_intercepted() {
  local addr
  command -v getent >/dev/null 2>&1 || return 1
  addr="$(getent ahostsv4 pypi.org 2>/dev/null | awk 'NR==1 {print $1}')"
  case "${addr}" in
    198.1[89].*|"") return 0 ;;
    *) return 1 ;;
  esac
}

seed_build_env() {
  local dest="${REPO_ROOT}/sandbox-image/build.env"
  local example="${REPO_ROOT}/sandbox-image/build.env.example"
  local hook="${REPO_ROOT}/dev/build-dns-pin.sh"

  if [[ -f "${dest}" && "${FORCE}" -eq 0 ]]; then
    log "keep existing sandbox-image/build.env"
    return
  fi

  if [[ ! -f "${example}" ]]; then
    log "skip missing sandbox-image/build.env.example"
    return
  fi

  cp "${example}" "${dest}"

  if dns_is_intercepted; then
    # Absolute path so the hook works regardless of cwd.
    printf '\n# Auto-enabled by configure-local.sh (intercepting DNS detected).\n' >> "${dest}"
    printf 'SANDBOX_BUILD_DNS_HOOK=%s\n' "${hook}" >> "${dest}"
    chmod +x "${hook}" 2>/dev/null || true
    log "wrote sandbox-image/build.env with SANDBOX_BUILD_DNS_HOOK (fake-ip DNS detected)"
  else
    log "wrote sandbox-image/build.env (DNS looks normal; hook left commented)"
  fi
}

seed_local_env() {
  local dest="${REPO_ROOT}/dev/local.env"
  if [[ -f "${dest}" && "${FORCE}" -eq 0 ]]; then
    log "keep existing dev/local.env"
    return
  fi

  # start-all.sh already auto-detects VM IP + checkout owner; an empty file is
  # enough. Write a short commented stub so the path is discoverable.
  cat > "${dest}" <<'EOF'
# Per-machine overrides for ./dev/start-all.sh (gitignored).
# Defaults auto-detect VM IP and the checkout owner — only set overrides here.
#
#HARNESS_DEV_ORIGINS=203.0.113.10,my-other-host
#FRONTEND_USER=myuser
#PROXY_DIR=/path/to/model-proxy
#HARNESS_LOG_LEVEL=debug
EOF
  log "wrote dev/local.env (commented stub; auto-detect remains default)"
}

cd "${REPO_ROOT}"

copy_example "${REPO_ROOT}/dev/model-proxy/.env.example" "${REPO_ROOT}/dev/model-proxy/.env"
seed_local_env
seed_build_env

log "per-machine checklist:"
log "  - set UPSTREAM_API_KEY in dev/model-proxy/.env"
log "  - optional: mirrors / SANDBOX_DNS_SERVERS in sandbox-image/build.env"
log "  - optional: HARNESS_DEV_ORIGINS / FRONTEND_USER in dev/local.env"
if [[ -f "${REPO_ROOT}/dev/model-proxy/.env" ]]; then
  if grep -q '^UPSTREAM_API_KEY=$' "${REPO_ROOT}/dev/model-proxy/.env" 2>/dev/null || \
     grep -q '^UPSTREAM_API_KEY=\s*$' "${REPO_ROOT}/dev/model-proxy/.env" 2>/dev/null; then
    log "  ! UPSTREAM_API_KEY is still empty — model turns will not work until set"
  fi
fi
