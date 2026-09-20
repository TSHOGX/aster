#!/usr/bin/env bash
# Build a new immutable application template; Khora owns PID 1 and placements.
set -euo pipefail
khora_template=${1:?usage: build-khora-template.sh NEW_TEMPLATE_DIRECTORY}
khora_template=$(realpath -m -- "$khora_template")
khora_repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
[[ $EUID == 0 ]] || { echo 'template build requires root in a disposable builder' >&2; exit 1; }
[[ ! -e "$khora_template" ]] || { echo 'template destination already exists; choose a new revision directory' >&2; exit 1; }
[[ ! -e "$khora_template.manifest.json" && ! -e "$khora_template.manifest.sha256" ]] || { echo 'template release metadata already exists; choose a new revision' >&2; exit 1; }
[[ -z $(git -C "$khora_repo" ls-files --others --ignored --exclude-standard sandbox-image/files) ]] || {
  echo 'ignored guest overlay files present; use a clean source checkout' >&2
  exit 1
}
khora_module_dir=$(go -C "$khora_repo/orchestrator" list -m -f '{{.Dir}}' github.com/TSHOGX/khora)
khora_module_version=$(go -C "$khora_repo/orchestrator" list -m -f '{{.Version}}' github.com/TSHOGX/khora)
[[ "$khora_module_version" == v0.0.2-0.20260920024301-e8cd021a9c7c ]] || { echo 'unsupported engine module pin' >&2; exit 1; }
ROOTFS_DIR="$khora_template" FORCE=1 SANDBOX_BUILD_ENV=/dev/null \
SANDBOX_AGENT_DRIVERS=pi,sh HARNESS_GENERIC_ROOTFS=1 \
  "$khora_repo/sandbox-image/build-rootfs.sh"
mkdir -p "$khora_template/usr/sbin"
CGO_ENABLED=0 go -C "$khora_module_dir" build -mod=readonly -trimpath \
  -o "$khora_template/usr/sbin/sandboxd" ./cmd/sandboxd
python3 "$khora_repo/sandbox-image/finalize-khora-template.py" "$khora_template" "$khora_module_version"
install -m 0644 "$khora_template/etc/harness-image/agents.json" "$khora_template.manifest.json"
install -m 0644 "$khora_template/etc/harness-image/agents.sha256" "$khora_template.manifest.sha256"
