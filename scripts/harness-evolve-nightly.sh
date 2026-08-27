#!/usr/bin/env bash
# harness-evolve-nightly.sh — 定时跑 trajectory-evolve（与 harness-kb-nightly 并列）
#
#   ingest   读平台 memory.db → trajectory-evolve 的 rebuildable 表（零 LLM）
#   evolve   ingest + observe + harvest + 三轨进化 → 只暂存，不发布
#
# 这不是 harness-kb-nightly.sh 的替代。Go 侧 backfill/replay 仍负责捕获与
# （默认）蒸馏。本脚本写的是 trajectory-evolve 自己的 SQLite 与（默认）私有
# KB 树，避免和 memory.db / 线上 agents-kb 抢锁。
#
# 切到线上 KB 树：导出 TE_KB_ROOT=/var/lib/harness-publish/agents-kb
# 且当晚不要再跑 harness-kb-nightly.sh evolve。
#
# 审批：trajectory-evolve `cli kb approve` 或（切到同一棵树后）harness-ctl kb
# MCP 入口没有 approve 工具。
#
# 锁：/var/lock/harness-evolve.lock（与 harness-kb.lock 分开）。
# 日志：/var/log/harness-evolve-nightly-<mode>.log
set -euo pipefail

REPO_ROOT="${HARNESS_REPO_ROOT:-/home/agent-runtime-platform/main}"
TE_ROOT="${TRAJECTORY_EVOLVE_ROOT:-/home/agent-runtime-platform/trajectory-evolve}"
DATA_ROOT="${HARNESS_DATA_ROOT:-/var/lib/harness-publish}"
LOCK="/var/lock/harness-evolve.lock"
BUN="$TE_ROOT/node_modules/.bin/bun"

export PATH="/root/.nvm/versions/node/v24.15.0/bin:$TE_ROOT/node_modules/.bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
export TE_MEMORY_DB="${TE_MEMORY_DB:-$DATA_ROOT/state/memory.db}"
export TE_STATE_ROOT="${TE_STATE_ROOT:-$DATA_ROOT/trajectory-evolve}"
export TE_ENGINE="${TE_ENGINE:-pi}"
# TE_KB_ROOT 默认不设：产物留在 $TE_STATE_ROOT/agents-kb。

mode="${1:-}"
case "$mode" in
  ingest|evolve) ;;
  *) echo "usage: $(basename "$0") <ingest|evolve>" >&2; exit 2 ;;
esac

if [[ ! -x "$BUN" ]]; then
  echo "FATAL: bun not found at $BUN (set TRAJECTORY_EVOLVE_ROOT)" >&2
  exit 1
fi
if [[ ! -f "$TE_ROOT/src/cli.ts" ]]; then
  echo "FATAL: trajectory-evolve CLI missing under $TE_ROOT" >&2
  exit 1
fi

LOG="/var/log/harness-evolve-nightly-${mode}.log"
if [[ -f "$LOG" ]] && [[ $(stat -c%s "$LOG") -gt 33554432 ]]; then
  mv -f "$LOG" "$LOG.1"
fi
exec >>"$LOG" 2>&1

echo "=== $(date '+%F %T %Z') harness-evolve-nightly $mode start ==="
echo "TE_ROOT=$TE_ROOT TE_STATE_ROOT=$TE_STATE_ROOT TE_MEMORY_DB=$TE_MEMORY_DB TE_KB_ROOT=${TE_KB_ROOT:-}"

mkdir -p "$(dirname "$LOCK")" "$TE_STATE_ROOT"
exec 9>"$LOCK"
if ! flock -n 9; then
  echo "SKIP: another harness-evolve-nightly run holds $LOCK; exiting."
  echo "=== $(date '+%F %T %Z') harness-evolve-nightly $mode skipped ==="
  exit 0
fi

cd "$TE_ROOT"
start=$SECONDS
case "$mode" in
  ingest)
    "$BUN" run src/cli.ts init
    "$BUN" run src/cli.ts ingest --memory-db "$TE_MEMORY_DB"
    ;;
  evolve)
    "$BUN" run src/cli.ts init
    "$BUN" run src/cli.ts cycle all
    "$BUN" run src/cli.ts kb status || true
    ;;
esac
echo "=== $(date '+%F %T %Z') harness-evolve-nightly $mode done in $((SECONDS - start))s ==="
