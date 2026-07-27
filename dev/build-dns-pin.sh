#!/usr/bin/env bash
# SANDBOX_BUILD_DNS_HOOK for a build host behind a fake-ip proxy.
#
# Some dev machines (clash and similar) answer every DNS query with an unroutable
# 198.18.0.0/15 address, and do so even for queries aimed straight at a public
# resolver, so switching nameservers in the chroot does not help. This resolves
# the build's download hosts over DoH — which bypasses the intercepting resolver
# — and pins the answers in the chroot's /etc/hosts.
#
# Wire it up from sandbox-image/build.env (gitignored):
#   SANDBOX_BUILD_DNS_HOOK=/path/to/repo/dev/build-dns-pin.sh
#
# It self-checks first and exits without touching anything when the host's DNS
# works, so leaving it wired up on a healthy host is harmless. The pins it writes
# do not reach the image: sanitize_isolated_rootfs resets /etc/hosts at the end
# of the build.
set -Eeuo pipefail

ROOTFS_DIR="${1:?usage: build-dns-pin.sh <rootfs-dir>}"

# Every host the build fetches from. Extend this when a build stage starts
# downloading from somewhere new — a missing entry shows up as that one stage
# failing to connect while every stage before it succeeded, and because
# build-rootfs.sh runs under `set -Eeuo pipefail` it aborts the whole rebuild.
PIN_HOSTS="${BUILD_DNS_PIN_HOSTS:-\
pypi.org files.pythonhosted.org \
ports.ubuntu.com archive.ubuntu.com \
registry.npmjs.org nodejs.org \
extensions.duckdb.org}"

DOH_URL="${BUILD_DNS_DOH_URL:-https://1.1.1.1/dns-query}"

log() { printf '[build-dns-pin] %s\n' "$*" >&2; }

dns_is_intercepted() {
  local addr
  addr="$(getent ahostsv4 pypi.org 2>/dev/null | awk 'NR==1 {print $1}')"
  case "${addr}" in
    198.1[89].*) return 0 ;;
    "") return 0 ;;
    *) return 1 ;;
  esac
}

resolve_over_doh() {
  curl -sS --max-time 20 -H 'accept: application/dns-json' \
    "${DOH_URL}?name=$1&type=A" 2>/dev/null |
    sed -n 's/.*"type":1,"TTL":[0-9]*,"data":"\([0-9.]*\)".*/\1/p' | head -1
}

if ! dns_is_intercepted; then
  log "build host DNS resolves normally; nothing to pin"
  exit 0
fi

log "build host DNS is intercepted; pinning build hosts by IP"
{
  # Carry over pins the build host itself already relies on.
  if [ -f /etc/hosts ]; then
    awk '/^[0-9]/ && $2 !~ /^(localhost|.*\.localdomain)$/ && $1 !~ /^127\./ { print }' /etc/hosts
  fi
  for host in ${PIN_HOSTS}; do
    addr="$(resolve_over_doh "${host}")"
    if [ -n "${addr}" ]; then
      printf '%s %s\n' "${addr}" "${host}"
    else
      log "warning: could not pin ${host}; the build may fail to fetch from it"
    fi
  done
} >> "${ROOTFS_DIR}/etc/hosts"
