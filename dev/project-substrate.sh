#!/usr/bin/env bash
# Run a test command against a disposable pinned Khora engine and immutable image.
# Required: PROJECT_TEST_ENGINE, PROJECT_TEST_TEMPLATE, PROJECT_TEST_OUTPUT.
set -euo pipefail
if [[ ${1:-} == --inner ]]; then
 shift
 base=$PROJECT_TEST_BASE
 [[ $EUID == 0 && $base == /tmp/project-khora-runtime.* ]] || exit 2
 [[ $(readlink /proc/self/ns/mnt) != "$PROJECT_TEST_HOST_NS" ]] || exit 2
 mount --bind "$PROJECT_TEST_TEMPLATE" "$base/templates/agent-runtime"
 mount -o remount,bind,ro "$base/templates/agent-runtime"

 mount -t tmpfs tmpfs /run
 ip link set lo up
 ip link add spike0 type dummy
 ip address add 192.0.2.1/24 dev spike0
 ip link set spike0 up
 ip route add default dev spike0
 mount -o loop "$base/disk.img" "$base/fs"
 mkdir "$base/fs/var-tmp"
 mount --bind "$base/fs/var-tmp" /var/tmp
 printf '%s\n' "$$" > "$PROJECT_TEST_CGROUP/cgroup.procs"
 mount --bind "$PROJECT_TEST_CGROUP" /sys/fs/cgroup
 exec unshare --cgroup bash "$0" --engine "$@"
fi
if [[ ${1:-} == --engine ]]; then
 shift
 base=$PROJECT_TEST_BASE
 mkdir /sys/fs/cgroup/driver
 printf '%s\n' "$$" > /sys/fs/cgroup/driver/cgroup.procs
 printf '+cpuset +cpu +io +memory +pids\n' > /sys/fs/cgroup/cgroup.subtree_control
 export TMPDIR="$base/tmp"
 "$PROJECT_TEST_ENGINE" --addr 127.0.0.1:18191 --templates "$base/templates" --state "$base/fs/state" --store "$base/fs/engine.db" --volumes-dir "$base/fs/volumes" --manual-cold-recovery --node-memory-byte 8589934592 --node-disk-byte 12884901888 --via-uid "$PROJECT_TEST_VIA_UID" --reaper-interval 30s > "$PROJECT_TEST_OUTPUT/engine.log" 2>&1 &
 engine_pid=$!
 export PROJECT_TEST_ENGINE_PID="$engine_pid" PROJECT_TEST_ENGINE_BINARY="$PROJECT_TEST_ENGINE"
 trap 'kill -TERM "$engine_pid" 2>/dev/null || true; wait "$engine_pid" || true' EXIT
 export PROJECT_TEST_ENDPOINT=http://127.0.0.1:18191
 "$@"
 exit
fi
[[ $EUID == 0 && $# -gt 0 ]] || { echo 'Run as root with a test command; see dev/README.md.' >&2; exit 2; }
: "${PROJECT_TEST_ENGINE:?}" "${PROJECT_TEST_TEMPLATE:?}" "${PROJECT_TEST_OUTPUT:?}"
export PROJECT_TEST_ENGINE=$(realpath "$PROJECT_TEST_ENGINE") PROJECT_TEST_TEMPLATE=$(realpath "$PROJECT_TEST_TEMPLATE")
mkdir -p "$PROJECT_TEST_OUTPUT"
export PROJECT_TEST_OUTPUT=$(realpath "$PROJECT_TEST_OUTPUT")
export PROJECT_TEST_VIA_UID=${PROJECT_TEST_VIA_UID:-65534} PROJECT_TEST_VIA_GID=${PROJECT_TEST_VIA_GID:-65534}
base=$(mktemp -d /tmp/project-khora-runtime.XXXXXX)
chmod 0755 "$base"
export PROJECT_TEST_BASE="$base" PROJECT_TEST_CGROUP="/sys/fs/cgroup/$(basename "$base")" PROJECT_TEST_HOST_NS
PROJECT_TEST_HOST_NS=$(readlink /proc/self/ns/mnt)
cleanup() {
 if [[ -e "$PROJECT_TEST_CGROUP/cgroup.kill" ]]; then printf '1\n' > "$PROJECT_TEST_CGROUP/cgroup.kill" || true; fi
 find "$PROJECT_TEST_CGROUP" -depth -type d -exec rmdir {} \; 2>/dev/null || true
 # This directory was created by this invocation and contains only disposable test state.
 python3 -c 'import shutil,sys; shutil.rmtree(sys.argv[1])' "$base"
}
trap cleanup EXIT
mkdir -p "$PROJECT_TEST_CGROUP" "$base/fs" "$base/tmp" "$base/templates/agent-runtime"
chmod 1777 "$base/tmp"
truncate -s 16G "$base/disk.img"
mkfs.btrfs -f -O squota "$base/disk.img" >/dev/null
unshare --mount --net --pid --fork --kill-child=KILL --mount-proc --propagation private bash "$0" --inner "$@"
