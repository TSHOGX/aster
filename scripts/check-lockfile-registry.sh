#!/usr/bin/env bash
# Fail if frontend/package-lock.json has `resolved` URLs pointing at anything but
# the canonical npm registry.
#
# Running `npm install` on a host configured with a mirror rewrites `resolved` to
# that mirror and, if committed, makes the lockfile only installable from that
# one network — a checkout elsewhere gets ECONNRESET on every mirrored entry, and
# a failed partial install then leaves node_modules corrupt (ENOTEMPTY on retry).
# This happened with 27 entries pointing at repo.huaweicloud.com.
#
# The registry a build fetches from is host configuration: set it in the host's
# ~/.npmrc or via npm_config_registry. `integrity` hashes make the packages
# themselves verifiable either way, so a mirror needs no lockfile change.
set -Eeuo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
LOCKFILE="${1:-${REPO_ROOT}/frontend/package-lock.json}"
ALLOWED_HOST="${ALLOWED_REGISTRY_HOST:-registry.npmjs.org}"

if [ ! -f "${LOCKFILE}" ]; then
  echo "lockfile not found: ${LOCKFILE}" >&2
  exit 1
fi

offenders="$(
  python3 - "${LOCKFILE}" "${ALLOWED_HOST}" <<'PY'
import json, sys
from urllib.parse import urlsplit

lockfile, allowed = sys.argv[1], sys.argv[2]
with open(lockfile, encoding="utf-8") as f:
    lock = json.load(f)

bad = []
for name, pkg in sorted((lock.get("packages") or {}).items()):
    resolved = (pkg or {}).get("resolved")
    if not resolved:
        continue
    host = urlsplit(resolved).hostname
    # Local/git/file deps have no host and are not registry fetches.
    if host and host != allowed:
        bad.append(f"{name or '<root>'}\t{resolved}")
print("\n".join(bad))
PY
)"

if [ -n "${offenders}" ]; then
  count="$(printf '%s\n' "${offenders}" | wc -l | tr -d ' ')"
  echo "${count} lockfile entr(ies) resolve to a host other than ${ALLOWED_HOST}:" >&2
  printf '%s\n' "${offenders}" >&2
  cat >&2 <<EOF

Rewrite the 'resolved' URLs back to https://${ALLOWED_HOST}/... (leave every
'integrity' hash untouched — the packages are the same ones). Configure a mirror
in the host's ~/.npmrc or npm_config_registry instead of in the lockfile.
EOF
  exit 1
fi

echo "lockfile registry check passed: all resolved URLs point at ${ALLOWED_HOST}"
