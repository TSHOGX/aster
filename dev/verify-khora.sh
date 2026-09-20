#!/usr/bin/env bash
# Disposable integration substrate. Application code still uses only public APIs.
set -euo pipefail
usage() {
  cat <<'HELP'
Usage: sudo -E dev/verify-khora.sh --template-dir DIR --manifest FILE [options]
  --engine FILE       Existing pinned engine binary; otherwise build the Go pin
  --via-user USER     Non-root gateway account (default: nobody)
  --run REGEX         Go test selection (default: ^TestKhora)
  --timeout DURATION  Per-package Go test deadline (default: 5m)
  --output DIR        Save logs and revision metadata outside disposable storage
  --browser           Include the optional real-daemon browser journey
Requires Linux namespaces, cgroup v2 delegation, btrfs, iproute2, runsc, Go and Python.
Set GOPATH/GOCACHE before sudo -E when using a pre-populated private module cache.
HELP
}
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
template= manifest= engine= output= via_user=nobody selection='^TestKhora' deadline=5m browser=0
while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0;;
    --browser) browser=1; shift;;
    --template-dir|--manifest|--engine|--via-user|--run|--timeout|--output)
      (($#>=2)) || { usage >&2; exit 2; }
      case "$1" in
        --template-dir) template=$2;; --manifest) manifest=$2;; --engine) engine=$2;;
        --via-user) via_user=$2;; --run) selection=$2;; --timeout) deadline=$2;; --output) output=$2;;
      esac
      shift 2;;
    *) usage >&2; exit 2;;
  esac
done
[[ $EUID == 0 && -n "$template" && -n "$manifest" ]] || { usage >&2; exit 2; }
for program in go python3 ip unshare mount mkfs.btrfs btrfs runsc; do command -v "$program" >/dev/null || { echo "missing dependency: $program" >&2; exit 2; }; done
[[ -f /sys/fs/cgroup/cgroup.controllers ]] || { echo 'cgroup v2 required' >&2; exit 2; }
template=$(realpath -- "$template")
manifest=$(realpath -- "$manifest")
[[ -d "$template" && -f "$manifest" && -f "$template/usr/sbin/sandboxd" ]] || { echo 'finalized application template and exported manifest required' >&2; exit 2; }
via_uid=$(id -u "$via_user")
via_gid=$(id -g "$via_user")
[[ "$via_uid" != 0 ]] || { echo 'gateway UID must differ from the root engine UID' >&2; exit 2; }
base=$(mktemp -d /tmp/harness-khora.XXXXXX)
cgroup="/sys/fs/cgroup/$(basename "$base")"
cleanup() {
  if [[ -e "$cgroup/cgroup.kill" ]]; then printf '1\n' > "$cgroup/cgroup.kill" || true; fi
  python3 - "$cgroup" <<'PY'
import os, sys
root = sys.argv[1]
if root.startswith('/sys/fs/cgroup/harness-khora.'):
    for directory, _, _ in os.walk(root, topdown=False):
        try: os.rmdir(directory)
        except OSError as error: print(f'cleanup pending: {directory}: {error}', file=sys.stderr)
PY
  if [[ -n "$output" ]]; then
    mkdir -p -- "$output"
    for log in tests.log engine.log revisions.json; do [[ ! -f "$base/$log" ]] || cp -- "$base/$log" "$output/$log"; done
  fi
  [[ "$base" == /tmp/harness-khora.* ]] && rm -rf -- "$base"
}
trap cleanup EXIT
if [[ -z "$engine" ]]; then
  module=$(go -C "$repo/orchestrator" list -m -f '{{.Dir}}' github.com/TSHOGX/khora)
  module_version=$(go -C "$repo/orchestrator" list -m -f '{{.Version}}' github.com/TSHOGX/khora)
  [[ "$module_version" == v0.0.2-0.20260920024301-e8cd021a9c7c ]] || { echo 'unsupported engine pin' >&2; exit 2; }
  engine="$base/khora-engine"
  CGO_ENABLED=0 go -C "$module" build -mod=readonly -trimpath -ldflags="-X main.version=e8cd021a9c7c" -o "$engine" ./cmd/orchestrator
else
  engine=$(realpath -- "$engine")
fi
cp -- "$manifest" "$base/manifest.json"
python3 - "$repo" "$base" "$engine" <<'PY'
import hashlib,json,pathlib,subprocess,sys
repo,base,engine=map(pathlib.Path,sys.argv[1:])
meta={'application':subprocess.check_output(['git','-C',str(repo),'rev-parse','HEAD'],text=True).strip(),
      'dirty':bool(subprocess.check_output(['git','-C',str(repo),'status','--porcelain'],text=True)),
      'engine_module':subprocess.check_output(['go','-C',str(repo/'orchestrator'),'list','-m','-f','{{.Version}}','github.com/TSHOGX/khora'],text=True).strip(),
      'engine_binary_sha256':hashlib.sha256(engine.read_bytes()).hexdigest(),
      'manifest_raw_sha256':hashlib.sha256((base/'manifest.json').read_bytes()).hexdigest(),
      'manifest_digest':'sha256:'+hashlib.sha256(json.dumps(json.loads((base/'manifest.json').read_text()),sort_keys=True,separators=(',',':'),ensure_ascii=False).encode()).hexdigest()}
(base/'revisions.json').write_text(json.dumps(meta,indent=2)+'\n')
print(json.dumps(meta))
PY
mkdir "$cgroup" "$base/fs" "$base/tmp" "$base/templates" "$base/templates/battery"
truncate -s 8G "$base/disk.img"
mkfs.btrfs -f -O squota "$base/disk.img" >/dev/null
export KHORA_TEST_REPO="$repo" KHORA_TEST_BASE="$base" KHORA_TEST_CGROUP="$cgroup"
export KHORA_TEST_TEMPLATE="$template" KHORA_TEST_ENGINE="$engine" KHORA_TEST_RUN="$selection" KHORA_TEST_TIMEOUT="$deadline"
export KHORA_INTEGRATION_VIA_UID="$via_uid" KHORA_INTEGRATION_VIA_GID="$via_gid" KHORA_INTEGRATION_BROWSER="$browser"
export KHORA_TEST_HOST_MOUNT_NS
KHORA_TEST_HOST_MOUNT_NS=$(readlink /proc/self/ns/mnt)
unshare --mount --net --pid --fork --kill-child=KILL --mount-proc --propagation private \
  bash "$repo/dev/khora/substrate.sh"
