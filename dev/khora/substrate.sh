#!/usr/bin/env bash
set -euo pipefail
[[ $EUID == 0 && ${KHORA_TEST_BASE:-} == /tmp/harness-khora.* ]] || exit 2
[[ $(readlink /proc/self/ns/mnt) != "$KHORA_TEST_HOST_MOUNT_NS" ]] || { echo 'refusing host mount namespace' >&2; exit 2; }
base=$KHORA_TEST_BASE
mount --bind "$KHORA_TEST_TEMPLATE" "$base/templates/battery"
mount -o remount,bind,ro "$base/templates/battery"
mount -t tmpfs tmpfs /run
mkdir -p /run/netns
ip link set lo up
ip link add contract0 type dummy
ip address add 192.0.2.1/24 dev contract0
ip link set contract0 up
ip route add default dev contract0
ip netns add contract-public
ip link add contract-out type veth peer name contract-in
ip link set contract-in netns contract-public
ip address add 198.18.0.1/30 dev contract-out
ip link set contract-out up
ip -n contract-public link set lo up
ip -n contract-public address add 198.18.0.2/30 dev contract-in
ip -n contract-public link set contract-in up
ip -n contract-public address add 1.1.1.1/32 dev lo
ip -n contract-public route add default via 198.18.0.1
ip route add 1.1.1.1/32 via 198.18.0.2 dev contract-out
ip netns exec contract-public python3 "$KHORA_TEST_REPO/dev/khora/public-probe.py" &
mount -o loop "$base/disk.img" "$base/fs"
mkdir "$base/fs/var-tmp"
mount --bind "$base/fs/var-tmp" /var/tmp
printf '%s\n' "$$" > "$KHORA_TEST_CGROUP/cgroup.procs"
mount --bind "$KHORA_TEST_CGROUP" /sys/fs/cgroup
exec unshare --cgroup bash "$KHORA_TEST_REPO/dev/khora/run.sh"
