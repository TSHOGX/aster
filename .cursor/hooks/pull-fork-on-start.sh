#!/usr/bin/env bash
# sessionStart: fetch/pull the latest code from the user's fork (origin).
set -u

input="$(cat || true)"

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$repo_root" || {
  printf '%s\n' '{"additional_context":"pull-fork-on-start: failed to cd to repo root"}'
  exit 0
}

branch="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
status_msg=""

if ! git remote get-url origin >/dev/null 2>&1; then
  status_msg="origin remote is missing; skipped pull"
else
  fetch_out="$(git fetch origin 2>&1)" || true
  if git rev-parse --verify --quiet "origin/${branch}" >/dev/null; then
    if pull_out="$(git pull --ff-only "origin" "${branch}" 2>&1)"; then
      status_msg="fetched origin and fast-forwarded ${branch}: ${pull_out}"
    else
      status_msg="fetched origin; could not fast-forward ${branch}: ${pull_out}"
    fi
  else
    status_msg="fetched origin; no origin/${branch} yet (local branch only). fetch: ${fetch_out}"
  fi
fi

# Escape for JSON string
json_escape() {
  printf '%s' "$1" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'
}

ctx="Fork pull on session start (${branch}): ${status_msg}"
escaped="$(json_escape "$ctx")"
printf '{"additional_context":%s}\n' "$escaped"
exit 0
