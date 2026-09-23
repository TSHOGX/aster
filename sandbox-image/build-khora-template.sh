#!/usr/bin/env bash
# Assemble a new immutable Project runtime template and export its Go manifest.
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
destination=$(realpath -m -- "${1:?usage: build-khora-template.sh NEW_DIRECTORY [OS_PI_BASE]}")
base=${2:-}
[[ $EUID == 0 && $destination != / && ! -e $destination && ! -e $destination.manifest.json ]] || { echo 'choose a new template directory in a disposable privileged builder' >&2; exit 2; }
[[ -z $(git -C "$repo" ls-files --others --ignored --exclude-standard sandbox-image/files) ]] || { echo 'ignored guest overlay files present; use a clean source checkout' >&2; exit 2; }
module=$(go -C "$repo/orchestrator" list -m -f '{{.Dir}}' github.com/TSHOGX/khora)
version=$(go -C "$repo/orchestrator" list -m -f '{{.Version}}' github.com/TSHOGX/khora)
[[ $version == v0.0.2-0.20260920024301-e8cd021a9c7c ]] || { echo 'unsupported engine module pin' >&2; exit 2; }
if [[ -n $base ]]; then
 base=$(realpath -- "$base")
 [[ -d $base && ! -e $base/etc/project-runtime/manifest.json && ! -e $base/etc/harness-image && ! -e $base/usr/local/bin/harness-khora-launcher ]] || { echo 'base must be an OS/Pi filesystem, not an application template' >&2; exit 2; }
 cp -a --reflink=auto "$base" "$destination"
else
 ROOTFS_DIR="$destination" "$repo/sandbox-image/build-rootfs.sh"
fi
install -d -m 0755 "$destination/usr/local/bin" "$destination/usr/sbin" "$destination/workspace" "$destination/runtime" "$destination/opt/agent-config" "$destination/opt/shared-assets"
cp -a "$repo/sandbox-image/files/." "$destination/"
CGO_ENABLED=0 go -C "$module" build -mod=readonly -trimpath -buildvcs=false -o "$destination/usr/sbin/sandboxd" ./cmd/sandboxd
for binary in agent-runner workspace-files runtime-cleanup runtime-verify; do
 CGO_ENABLED=0 go -C "$repo/orchestrator" build -trimpath -buildvcs=false -o "$destination/usr/local/bin/$binary" "./cmd/$binary"
 chmod 0755 "$destination/usr/local/bin/$binary"
done
CGO_ENABLED=0 go -C "$repo/orchestrator" run ./cmd/runtime-release --patch-native --root "$destination" --output "$destination.manifest.json"
chmod -R a-w "$destination/etc/project-runtime" "$destination/usr/local/share/project-runtime"
printf 'Template: %s\nManifest: %s\n' "$destination" "$destination.manifest.json"
