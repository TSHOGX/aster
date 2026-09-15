#!/usr/bin/env bash
set -euo pipefail
release_version=${1:?exact release version required}
release_output=${2:?output directory required}
release_repo=$(cd "$(dirname "$0")/../.." && pwd)
[[ $(uname -m) == x86_64 ]] || { echo 'native amd64 builder required' >&2; exit 1; }
[[ $EUID == 0 ]] || { echo 'root privileges on the disposable builder required' >&2; exit 1; }
# A fresh checkout must never copy ignored credentials from a developer host.
if [[ -n $(git -C "$release_repo" ls-files --others --ignored --exclude-standard sandbox-image/files) ]]; then
  echo 'ignored overlay files found; build from a clean checkout without credentials' >&2
  exit 1
fi
mkdir -p "$release_output"
release_work=$(mktemp -d)
trap 'rm -rf --one-file-system "$release_work"' EXIT
export ROOTFS_DIR="$release_work/rootfs"
export SANDBOX_BUILD_ENV=/dev/null
# Use the disposable builder's resolver only while installing packages. The
# generic image is sanitized and receives its real resolver at deployment.
export SANDBOX_DNS_SERVERS
SANDBOX_DNS_SERVERS=$(awk '/^nameserver / {print $2}' /etc/resolv.conf | tr '\n' ' ')
[[ -n "$SANDBOX_DNS_SERVERS" ]] || { echo 'build resolver missing' >&2; exit 1; }
export HARNESS_GENERIC_ROOTFS=1
export UBUNTU_SUITE=noble
export UBUNTU_MIRROR=https://snapshot.ubuntu.com/ubuntu/20260901T000000Z/
export SANDBOX_AGENT_DRIVERS=pi,sh
export PI_CODING_AGENT_VERSION=0.77.0
export PI_NODE_VERSION=24.15.0
export FORCE=1
"$release_repo/sandbox-image/build-rootfs.sh"
chroot "$ROOTFS_DIR" dpkg-query -W > "$release_output/rootfs-apt-packages.txt"
chroot "$ROOTFS_DIR" python3 -m pip freeze --all > "$release_output/rootfs-python-packages.txt"
cp "$release_repo/sandbox-image/requirements.lock" "$release_output/rootfs-requirements.lock"
release_archive="$release_output/sandbox-$release_version-linux-amd64.tar.gz"
[[ ! -e "$release_archive" ]] || { echo 'archive exists' >&2; exit 1; }
# Device and runtime mounts are supplied by runsc, never restored from tar.
tar --xattrs --numeric-owner --exclude='./dev/*' --exclude='./proc/*' --exclude='./sys/*' --exclude='./run/*' --exclude='./tmp/*' \
  -czf "$release_archive" -C "$ROOTFS_DIR" .
