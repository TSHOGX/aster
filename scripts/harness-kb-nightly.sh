#!/usr/bin/env bash
# Optional external memory job wrapper. Install no schedule or service here.
# Required environment may be loaded from a private operator-owned file.
set -euo pipefail
if [[ -n "${HARNESS_JOB_ENV:-}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$HARNESS_JOB_ENV"
  set +a
fi
: "${ORCH_BIN:?Set the absolute orchestrator executable path}"
: "${HARNESS_CONFIG_PATH:?Set the absolute application YAML path}"
: "${HARNESS_JOB_LOCK:?Set a lock shared by this deployment's memory jobs}"
[[ "$ORCH_BIN" = /* && "$HARNESS_CONFIG_PATH" = /* && "$HARNESS_JOB_LOCK" = /* ]] || {
  echo 'binary, config and lock paths must be absolute' >&2; exit 2;
}
[[ -x "$ORCH_BIN" && -r "$HARNESS_CONFIG_PATH" ]] || {
  echo 'binary or configuration is unavailable' >&2; exit 2;
}
export HARNESS_MEMORY_AUTO_PUBLISH=false
# Optional private prompt overlay belongs in HARNESS_JOB_ENV as
# HARNESS_KB_PROMPTS_PATH. Unset uses embedded defaults; set-but-unusable fails.
mode=${1:-}
case "$mode" in
  ingest) [[ $# == 1 ]] || exit 2 ;;
  evolve) [[ $# == 2 ]] || { echo 'evolve requires an explicit YYYY-MM-DD date' >&2; exit 2; } ;;
  *) echo 'usage: harness-kb-nightly.sh ingest | evolve YYYY-MM-DD' >&2; exit 2 ;;
esac
exec 9>"$HARNESS_JOB_LOCK"
flock -n 9 || { echo 'Another memory job owns the lock; skipping.'; exit 0; }
if [[ "$mode" == ingest ]]; then
  exec "$ORCH_BIN" backfill --config "$HARNESS_CONFIG_PATH"
fi
exec "$ORCH_BIN" replay --date "$2" --config "$HARNESS_CONFIG_PATH"
