#!/usr/bin/env bash
# Build a fresh OS/workload/native-Pi base for an immutable Project template.
set -Eeuo pipefail
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
ROOTFS_DIR=$(realpath -m -- "${ROOTFS_DIR:-$SCRIPT_DIR/rootfs}")
[[ $EUID == 0 && $ROOTFS_DIR != / && ! -e $ROOTFS_DIR ]] || { echo 'a new non-root filesystem path and a privileged disposable builder are required' >&2; exit 2; }
SANDBOX_BUILD_ENV=${SANDBOX_BUILD_ENV:-$SCRIPT_DIR/build.env}
if [[ -f $SANDBOX_BUILD_ENV ]]; then source "$SANDBOX_BUILD_ENV"; fi
UBUNTU_SUITE=${UBUNTU_SUITE:-noble}
if [[ -z ${UBUNTU_MIRROR:-} ]]; then
 case $(dpkg --print-architecture) in amd64|i386) UBUNTU_MIRROR=http://archive.ubuntu.com/ubuntu;; *) UBUNTU_MIRROR=http://ports.ubuntu.com/ubuntu-ports;; esac
fi
UBUNTU_COMPONENTS=${UBUNTU_COMPONENTS:-main,universe}
NODE_DIST_BASE_URL=${NODE_DIST_BASE_URL:-https://nodejs.org/dist}
PI_NODE_VERSION=24.15.0
PI_CODING_AGENT_VERSION=0.77.0
PI_PACKAGE=@earendil-works/pi-coding-agent
PI_PACKAGE_INTEGRITY='sha512-huS+k+dhQRR9PlTK7crLfeSRUw3a96V6JYfP0ZH3Zkko/m10gsYk8dKQmwScSy5Dll516pXorz19BURfD6S2qQ=='
SANDBOX_FAMILY=${SANDBOX_FAMILY:-minimal}
case "$SANDBOX_FAMILY" in minimal|work) ;; *) echo 'SANDBOX_FAMILY must be minimal or work' >&2; exit 2;; esac
APT_PACKAGES=(ca-certificates curl git xz-utils)
if [[ $SANDBOX_FAMILY == work ]]; then
 APT_PACKAGES+=(fonts-wqy-microhei fonts-wqy-zenhei mysql-client-core-8.0 python3 python3-pip python3-venv)
fi
log(){ printf '[build-rootfs] %s\n' "$*"; }

install_duckdb_extensions() {
  log "installing DuckDB extensions (httpfs)"
  chroot "${ROOTFS_DIR}" /bin/sh -lc '
set -eu
# __duckdb_version__, not __version__: the extension directory is named after the
# bundled engine version, while __version__ is the Python wrapper version read
# from dist-info. They match today, so the old check passed, but they are
# independent values and only the engine one addresses this path.
version="$(python3 -c "import duckdb; print(duckdb.__duckdb_version__)")"
dest="/usr/local/share/duckdb-extensions"
python3 - "$dest" <<PY
import duckdb, sys
# INSTALL writes into the given extension_directory; LOAD then needs no network.
c = duckdb.connect(config={"extension_directory": sys.argv[1]})
c.execute("INSTALL httpfs;")
c.execute("LOAD httpfs;")
PY
chmod -R a+rX "${dest}"
test -s "${dest}/v${version}/"*"/httpfs.duckdb_extension"
'
}

# matplotlib ships a matplotlibrc whose font.family is plain `sans-serif`, which
# resolves to DejaVu Sans and renders every Chinese glyph as a tofu box (□□□).
# Users here title almost every chart in Chinese, so the CJK families have to be
# the image default rather than something each session remembers to set.
#
# Both keys are patched on purpose. matplotlib's per-glyph fallback only walks the
# `font.family` LIST — it does not consult the `font.sans-serif` sublist for
# fallback — so a CJK family must appear in font.family itself to survive code
# that does `rcParams["font.family"] = "monospace"`. font.sans-serif is patched
# too so the normal sans path also has a CJK entry.
#
# This was previously a hand-edit inside the deployed rootfs and was silently
# reverted by the first clean rebuild; keep it in the script so it survives.
patch_matplotlibrc_for_cjk() {
  log "patching matplotlibrc for CJK default fonts"
  chroot "${ROOTFS_DIR}" /bin/sh -lc '
set -eu
rc="$(python3 -c "import matplotlib, pathlib; print(pathlib.Path(matplotlib.matplotlib_fname()))")"
test -f "$rc"
python3 - "$rc" <<PY
import re, sys, pathlib
p = pathlib.Path(sys.argv[1])
text = p.read_text()
# Uncomment-and-extend, matching the commented default or an already-set key.
text = re.sub(r"^#?font\.family:.*$",
              "font.family:  sans-serif, WenQuanYi Zen Hei",
              text, count=1, flags=re.M)
text = re.sub(r"^#?font\.sans-serif:(.*)$",
              lambda m: "font.sans-serif:" + m.group(1).rstrip()
                        + ", WenQuanYi Zen Hei, WenQuanYi Micro Hei",
              text, count=1, flags=re.M)
p.write_text(text)
PY
grep -q "^font.family:.*WenQuanYi" "$rc"
grep -q "^font.sans-serif:.*WenQuanYi" "$rc"
# Prove Chinese actually renders with these defaults. Do NOT check this with
# findfont(): given a family LIST it returns the first match (DejaVu Sans for
# `sans-serif`), because CJK fallback is per-glyph at draw time, not a lookup
# result. The real signal is whether drawing emits "Glyph ... missing from font".
MPLCONFIGDIR=/tmp/mplbuild python3 - <<PY
import warnings
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.font_manager import findfont, FontProperties

# The faces must at least be registered.
face = findfont(FontProperties(family="WenQuanYi Zen Hei"))
assert "wqy" in face.lower(), f"WenQuanYi not registered: {face}"

fig, ax = plt.subplots()
ax.set_title("电池健康度分布")
ax.set_xlabel("充电次数")
with warnings.catch_warnings(record=True) as caught:
    warnings.simplefilter("always")
    fig.canvas.draw()
missing = [str(w.message) for w in caught if "missing from font" in str(w.message)]
assert not missing, f"CJK glyphs did not fall back: {missing[:3]}"
print("matplotlibrc CJK default OK; WenQuanYi at", face)
PY
rm -rf /tmp/mplbuild
'
}



command -v debootstrap >/dev/null || { echo 'debootstrap is required' >&2; exit 2; }
mkdir -p "$ROOTFS_DIR"
debootstrap --variant=minbase --include=ca-certificates --components="$UBUNTU_COMPONENTS" "$UBUNTU_SUITE" "$ROOTFS_DIR" "$UBUNTU_MIRROR"
printf 'nameserver %s\n' ${BUILD_DNS_SERVERS:-1.1.1.1 8.8.8.8} > "$ROOTFS_DIR/etc/resolv.conf"
printf '127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\n' > "$ROOTFS_DIR/etc/hosts"
if [[ -n ${SANDBOX_BUILD_DNS_HOOK:-} ]]; then "$SANDBOX_BUILD_DNS_HOOK" "$ROOTFS_DIR"; fi
chroot "$ROOTFS_DIR" apt-get update
chroot "$ROOTFS_DIR" env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${APT_PACKAGES[@]}"
chroot "$ROOTFS_DIR" apt-get clean
# debootstrap on rootless-device hosts may leave a plain /dev/null placeholder.
# Chroot package tooling still needs a writable sink during this disposable build.
chmod 0666 "$ROOTFS_DIR/dev/null"
if [[ $SANDBOX_FAMILY == work ]]; then
cp "$SCRIPT_DIR/requirements.lock" "$ROOTFS_DIR/tmp/project-requirements.lock"
chroot "$ROOTFS_DIR" env PIP_INDEX_URL="${PIP_INDEX_URL:-https://pypi.org/simple}" PIP_TRUSTED_HOST="${PIP_TRUSTED_HOST:-}" python3 -m pip install --break-system-packages --no-cache-dir --require-hashes -r /tmp/project-requirements.lock
unlink "$ROOTFS_DIR/tmp/project-requirements.lock"
install_duckdb_extensions
patch_matplotlibrc_for_cjk
fi
printf '%s\n' "$SANDBOX_FAMILY" > "$ROOTFS_DIR/etc/aster-sandbox-family"
chroot "$ROOTFS_DIR" env NODE_DIST_BASE_URL="$NODE_DIST_BASE_URL" NODE_VERSION="$PI_NODE_VERSION" /bin/bash -s <<'NATIVE'
set -euo pipefail
case $(dpkg --print-architecture) in amd64) architecture=x64;; arm64) architecture=arm64;; *) exit 2;; esac
archive="node-v${NODE_VERSION}-linux-${architecture}.tar.xz"
curl -fsSL "$NODE_DIST_BASE_URL/v$NODE_VERSION/$archive" -o "/tmp/$archive"
curl -fsSL "$NODE_DIST_BASE_URL/v$NODE_VERSION/SHASUMS256.txt" -o /tmp/node-checksums
(cd /tmp && sha256sum -c --ignore-missing node-checksums)
mkdir -p "/opt/node-v$NODE_VERSION"
tar -xJf "/tmp/$archive" -C "/opt/node-v$NODE_VERSION" --strip-components=1
ln -s "/opt/node-v$NODE_VERSION/bin/node" /usr/local/bin/node
ln -s "/opt/node-v$NODE_VERSION/bin/npm" /usr/local/bin/npm
ln -s "/opt/node-v$NODE_VERSION/bin/npx" /usr/local/bin/npx
unlink "/tmp/$archive"
unlink /tmp/node-checksums
NATIVE
chroot "$ROOTFS_DIR" env NPM_CONFIG_REGISTRY="${NPM_REGISTRY:-https://registry.npmjs.org}" PI_PACKAGE="$PI_PACKAGE" PI_VERSION="$PI_CODING_AGENT_VERSION" PI_INTEGRITY="$PI_PACKAGE_INTEGRITY" /bin/bash -s <<'PI'
set -euo pipefail
mkdir -p /tmp/pi-install
cd /tmp/pi-install
npm pack "$PI_PACKAGE@$PI_VERSION" --pack-destination . --json > package.json
node -e 'const fs=require("node:fs"),crypto=require("node:crypto");const entry=JSON.parse(fs.readFileSync("package.json","utf8"))[0];const actual="sha512-"+crypto.createHash("sha512").update(fs.readFileSync(entry.filename)).digest("base64");if(actual!==process.env.PI_INTEGRITY)throw new Error("Pi package differs from reviewed integrity pin");fs.writeFileSync("package-path",entry.filename);'
npm install -g --prefix /usr/local "./$(cat package-path)"
PI
# Build caches and temporary package archives are not runtime state.
for directory in "$ROOTFS_DIR/root/.cache" "$ROOTFS_DIR/root/.npm" "$ROOTFS_DIR/tmp/pi-install" "$ROOTFS_DIR/var/lib/apt/lists"; do
 if [[ -d $directory ]]; then find "$directory" -depth -mindepth 1 -delete; rmdir "$directory" || true; fi
done
printf '127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\n' > "$ROOTFS_DIR/etc/hosts"
printf '# Project lifecycle supplies its resolver configuration.\n' > "$ROOTFS_DIR/etc/resolv.conf"
log "OS/Pi base ready at $ROOTFS_DIR"
