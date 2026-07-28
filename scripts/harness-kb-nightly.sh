#!/usr/bin/env bash
# harness-kb-nightly.sh — 定时的轨迹入库 / 上下文自进化(单一入口,两个模式)
#
#   ingest   23:00  只把 pi 原生轨迹归档 + 解析入库(trajectory_* 表)。零 LLM 成本。
#   evolve   00:00  入库 + 全量蒸馏(agentic 图)→ 产出 pending 快照等专家审批。
#
# 为什么用带外 CLI 而不是让服务自己跑 ticker:memory.db 是 SQLite 单写者(WAL),
# 服务的 nightly loop 和这里的 CLI 同时写会 SQLITE_BUSY。所以 memory-llm.yaml 保持
# enabled:false(服务只挂 KB 不蒸馏),cron 的 CLI 是唯一写者。
#
# 两个模式共用一把 flock:23:00 的 ingest 没跑完时 00:00 的 evolve 直接跳过,
# 而不是并发打架。
#
# 审批门禁:HARNESS_MEMORY_AUTO_PUBLISH=false 强制只暂存不上线,无论配置文件写了什么。
# 审批入口:harness-ctl kb status / diff / approve <digest> / reject <digest>
#
# 日志:/var/log/harness-kb-nightly-<mode>.log(自带轮转:保留最近 14 天)
# 详见 docs/agents-kb.md「定时调度」与「审批门禁」两节。
set -euo pipefail

REPO_ROOT="/home/agent-runtime-platform/main"
ORCH_BIN="$REPO_ROOT/orchestrator/bin/orchestrator"
DATA_ROOT="/var/lib/harness-publish"
LOCK="/var/lock/harness-kb.lock"

# publish 环境的运行时根目录。与 systemd drop-in 10-agents-kb.conf 保持一致 ——
# 不导这些的话 config.go 的默认值会指向 lab 环境 /var/lib/harness/*。
export HARNESS_DB_PATH="$DATA_ROOT/state/orchestrator.db"
export HARNESS_SESSIONS_ROOT="$DATA_ROOT/sessions"
export HARNESS_AGENT_HOMES_ROOT="$DATA_ROOT/agent-homes"
export HARNESS_AGENTS_KB_PATH="$DATA_ROOT/agents-kb"
export HARNESS_TRAJECTORY_ROOT="$DATA_ROOT/trajectories"
# memory.db 由 HARNESS_DB_PATH 同目录推导,无需单独导。

# 进化用 agentic 图;并发 8(串行冷启动约 3h,并发约 20-40min)。
export HARNESS_MEMORY_AGENTIC=true
export HARNESS_MEMORY_AGENT_CONCURRENCY="${HARNESS_MEMORY_AGENT_CONCURRENCY:-8}"
# 审批门禁:定时任务永不自动上线。
export HARNESS_MEMORY_AUTO_PUBLISH=false
# pi 子进程需要 node 在 PATH 上(cron 的 PATH 很窄)。
export PATH="/root/.nvm/versions/node/v24.15.0/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

mode="${1:-}"
case "$mode" in
  ingest|evolve) ;;
  *) echo "usage: $(basename "$0") <ingest|evolve>" >&2; exit 2 ;;
esac

# config.Load() 从 cwd 推导 repoRoot（去掉末尾的 orchestrator 段），config/memory-llm.yaml
# 和 config/harness.yaml 都靠它解析；agentic 图的 Node C 也用它当读代码的 WorkDir。
# 所以必须和 systemd unit 的 WorkingDirectory 一致，否则 replay 会报
# "distillation disabled (no memory LLM configured)"。cron 的 cwd 是 /，必踩。
cd "$REPO_ROOT/orchestrator"

LOG="/var/log/harness-kb-nightly-${mode}.log"
# 轮转:超过 32MB 就归档一份,只留一个 .1(日志量小,不值得上 logrotate 配置)。
if [[ -f "$LOG" ]] && [[ $(stat -c%s "$LOG") -gt 33554432 ]]; then
  mv -f "$LOG" "$LOG.1"
fi
exec >>"$LOG" 2>&1

echo "=== $(date '+%F %T %Z') harness-kb-nightly $mode start ==="

if [[ ! -x "$ORCH_BIN" ]]; then
  echo "FATAL: orchestrator binary not found or not executable: $ORCH_BIN"
  exit 1
fi

# flock -n:拿不到锁就退出,不排队。上一轮还在跑说明这轮本来也该跳过。
exec 9>"$LOCK"
if ! flock -n 9; then
  echo "SKIP: another harness-kb-nightly run holds $LOCK (still running); exiting."
  echo "=== $(date '+%F %T %Z') harness-kb-nightly $mode skipped ==="
  exit 0
fi

start=$SECONDS
case "$mode" in
  ingest)
    # 幂等:指纹去重,只处理新增/变化的会话。不带 --since = 全历史,便于补欠账。
    "$ORCH_BIN" backfill
    ;;
  evolve)
    # replay --date 昨天:ListSessionsForDistillation 的条件是
    # "normalized_at IS NULL OR extracted_at IS NULL OR ended_at >= since",
    # 所以昨天这个 date 既覆盖当天新增,也自动把历史欠抽取的会话一并纳入。
    "$ORCH_BIN" replay --date "$(date -d yesterday +%F)"
    echo "--- kb status ---"
    "$ORCH_BIN" kb status || true
    ;;
esac
echo "=== $(date '+%F %T %Z') harness-kb-nightly $mode done in $((SECONDS - start))s ==="
