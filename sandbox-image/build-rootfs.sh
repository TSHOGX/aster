#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

ROOTFS_DIR="${ROOTFS_DIR:-${SCRIPT_DIR}/rootfs}"

# Everything deployment-specific about this build — package mirrors, the DNS
# servers baked into the image, an optional build-host DNS hook — comes from an
# env file the host provides, never from a committed value. Defaults below are
# upstream-canonical and carry no environment's fingerprint, so a build with no
# env file at all is correct (just not mirrored). See build.env.example.
SANDBOX_BUILD_ENV="${SANDBOX_BUILD_ENV:-${SCRIPT_DIR}/build.env}"
if [ -f "${SANDBOX_BUILD_ENV}" ]; then
  # shellcheck disable=SC1090
  . "${SANDBOX_BUILD_ENV}"
fi

UBUNTU_SUITE="${UBUNTU_SUITE:-noble}"
# archive.ubuntu.com carries amd64/i386 only; every other arch (arm64 — the local
# dev VM on Apple silicon) is on ports.ubuntu.com. Pick by host arch so the same
# command works on both, and still allow an explicit override.
if [ -z "${UBUNTU_MIRROR:-}" ]; then
  case "$(dpkg --print-architecture 2>/dev/null || uname -m)" in
    amd64 | i386 | x86_64) UBUNTU_MIRROR="http://archive.ubuntu.com/ubuntu" ;;
    *) UBUNTU_MIRROR="http://ports.ubuntu.com/ubuntu-ports" ;;
  esac
fi
UBUNTU_COMPONENTS="${UBUNTU_COMPONENTS:-main,universe}"
# Package index overrides. Empty means the tool's own default (pypi.org,
# registry.npmjs.org, nodejs.org) — a mirrored deployment sets these.
PIP_INDEX_URL="${PIP_INDEX_URL:-}"
PIP_TRUSTED_HOST="${PIP_TRUSTED_HOST:-}"
NPM_REGISTRY="${NPM_REGISTRY:-}"
NODE_DIST_BASE_URL="${NODE_DIST_BASE_URL:-https://nodejs.org/dist}"
# Resolvers baked into the image's /etc/resolv.conf. These are what the sandbox
# uses when the orchestrator is not projecting a per-generation resolv.conf, so
# they have to be reachable from wherever the image runs — a cloud deployment
# points them at its VPC resolvers, and nothing here assumes any one of them.
SANDBOX_DNS_SERVERS="${SANDBOX_DNS_SERVERS:-1.1.1.1 8.8.8.8}"
SANDBOX_DNS_SEARCH="${SANDBOX_DNS_SEARCH:-}"
# Optional executable run after debootstrap with the rootfs dir as $1, to fix up
# build-host network quirks (e.g. pinning hosts by IP behind an intercepting
# resolver). Unset on any host with working DNS. See dev/build-dns-pin.sh.
SANDBOX_BUILD_DNS_HOOK="${SANDBOX_BUILD_DNS_HOOK:-}"
FORCE="${FORCE:-0}"
SANDBOX_AGENT_DRIVERS="${SANDBOX_AGENT_DRIVERS:-pi}"
PI_CODING_AGENT_VERSION="${PI_CODING_AGENT_VERSION:-0.77.0}"
PI_NODE_VERSION="${PI_NODE_VERSION:-24.15.0}"
PI_PACKAGE="@earendil-works/pi-coding-agent"
PI_PACKAGE_SHASUM="627664c042507babf8a134a3770285272ccae5d8"
PI_PACKAGE_INTEGRITY="sha512-huS+k+dhQRR9PlTK7crLfeSRUw3a96V6JYfP0ZH3Zkko/m10gsYk8dKQmwScSy5Dll516pXorz19BURfD6S2qQ=="
PI_EVENT_SCHEMA="pi_rpc_events_v1.0"

APT_PACKAGES=(
  ca-certificates
  curl
  fonts-wqy-microhei
  fonts-wqy-zenhei
  git
  nodejs
  npm
  python3
  python3-pip
  python3-venv
  xz-utils
)

# The data-analysis stack agents are expected to have available. Everything a
# user's code imports directly is listed explicitly, even when another entry
# would pull it in transitively (matplotlib/pandas/scipy arrive via
# seaborn/statsmodels, lxml via python-docx) — otherwise an upstream dependency
# change silently removes it from the image.
PIP_PACKAGES=(
  # core numerics / dataframes
  matplotlib
  numpy
  pandas
  scipy
  seaborn
  statsmodels
  # columnar & analytical engines
  duckdb
  fastparquet
  pyarrow
  # interactive charts (kaleido renders them to static images)
  kaleido
  plotly
  # databases
  psycopg2-binary
  pymysql
  sqlalchemy
  # office documents
  lxml
  openpyxl
  python-docx
  python-pptx
  xlrd
  xlsxwriter
  # pdf
  fpdf2
  pypdf
  reportlab
  # misc formatting
  tabulate
)

log() {
  printf '[build-rootfs] %s\n' "$*"
}

SELECTED_AGENT_DRIVERS=()

parse_agent_drivers() {
  local raw driver canonical
  declare -A seen=()
  SELECTED_AGENT_DRIVERS=()
  IFS=',' read -ra raw_drivers <<< "${SANDBOX_AGENT_DRIVERS}"
  for raw in "${raw_drivers[@]}"; do
    driver="$(printf '%s' "${raw}" | tr -d '[:space:]')"
    if [ -z "${driver}" ]; then
      continue
    fi
    case "${driver}" in
      pi|sh) canonical="${driver}" ;;
      *)
        echo "unsupported SANDBOX_AGENT_DRIVERS entry: ${driver}" >&2
        exit 1
        ;;
    esac
    if [ -z "${seen[${canonical}]:-}" ]; then
      SELECTED_AGENT_DRIVERS+=("${canonical}")
      seen["${canonical}"]=1
    fi
  done
  if [ "${#SELECTED_AGENT_DRIVERS[@]}" -eq 0 ]; then
    echo "SANDBOX_AGENT_DRIVERS must select at least one driver" >&2
    exit 1
  fi
}

driver_selected() {
  local want="$1"
  local driver
  for driver in "${SELECTED_AGENT_DRIVERS[@]}"; do
    if [ "${driver}" = "${want}" ]; then
      return 0
    fi
  done
  return 1
}

selected_agent_drivers_csv() {
  local IFS=,
  printf '%s' "${SELECTED_AGENT_DRIVERS[*]}"
}

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "build-rootfs.sh must run as root because debootstrap/chroot need root privileges." >&2
    exit 1
  fi
}

copy_overlay_files() {
  log "installing harness files into ${ROOTFS_DIR}"
  rsync -a --chmod=F755,D755 "${SCRIPT_DIR}/files/" "${ROOTFS_DIR}/"
}

# DuckDB's httpfs extension is needed to read Parquet straight off Huawei OBS
# (skills/obs-parquet-duckdb). Sessions must not have to `INSTALL httpfs` at
# runtime, so bake it into a shared extension dir agents point at via
# `SET extension_directory=...` / connect(config={"extension_directory": ...}).
install_duckdb_extensions() {
  log "installing DuckDB extensions (httpfs)"
  chroot "${ROOTFS_DIR}" /bin/sh -lc '
set -eu
version="$(python3 -c "import duckdb; print(duckdb.__version__)")"
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

install_modern_node_for_pi() {
  if ! driver_selected pi; then
    return
  fi
  log "installing Node ${PI_NODE_VERSION} for Pi"
  chroot "${ROOTFS_DIR}" /bin/sh -lc '
set -eu
arch="$(dpkg --print-architecture)"
case "${arch}" in
  amd64) node_arch="x64" ;;
  arm64) node_arch="arm64" ;;
  *) echo "unsupported Node architecture for Pi: ${arch}" >&2; exit 1 ;;
esac
version="'"${PI_NODE_VERSION}"'"
dist_base="'"${NODE_DIST_BASE_URL}"'"
prefix="/opt/node-v${version}"
if [ ! -x "${prefix}/bin/node" ]; then
  tmp="/tmp/node-v${version}.tar.xz"
  curl -fsSL "${dist_base}/v${version}/node-v${version}-linux-${node_arch}.tar.xz" -o "${tmp}"
  mkdir -p "${prefix}"
  tar -xJf "${tmp}" -C "${prefix}" --strip-components=1
  rm -f "${tmp}"
fi
ln -sf "${prefix}/bin/node" /usr/local/bin/node
ln -sf "${prefix}/bin/npm" /usr/local/bin/npm
ln -sf "${prefix}/bin/npx" /usr/local/bin/npx
node --version >/dev/null
npm --version >/dev/null
'
}

install_agent_drivers() {
  local driver
  for driver in "${SELECTED_AGENT_DRIVERS[@]}"; do
    case "${driver}" in
      pi)
        log "installing Pi coding agent ${PI_CODING_AGENT_VERSION}"
        local npm_registry_arg=""
        if [ -n "${NPM_REGISTRY}" ]; then
          npm_registry_arg="--registry ${NPM_REGISTRY}"
          log "using npm registry ${NPM_REGISTRY}"
        fi
        chroot "${ROOTFS_DIR}" /bin/sh -lc "npm install -g --prefix /usr/local ${npm_registry_arg} '${PI_PACKAGE}@${PI_CODING_AGENT_VERSION}'"
        ;;
      sh)
        log "using bundled shell agent"
        ;;
    esac
  done
}

generate_agent_manifest() {
  local drivers
  drivers="$(selected_agent_drivers_csv)"
  log "generating /etc/harness-image/agents.json for ${drivers}"
  python3 - "${ROOTFS_DIR}" "${drivers}" "${PI_CODING_AGENT_VERSION}" "${PI_PACKAGE}" "${PI_PACKAGE_SHASUM}" "${PI_PACKAGE_INTEGRITY}" "${PI_EVENT_SCHEMA}" "${PI_NODE_VERSION}" <<'PY'
import hashlib
import json
import os
import pathlib
import sys

rootfs = pathlib.Path(sys.argv[1]).resolve()
drivers = [driver for driver in sys.argv[2].split(",") if driver]
pi_version, pi_package, pi_shasum, pi_integrity, pi_event_schema, pi_node_version = sys.argv[3:9]


def rootfs_path(sandbox_path):
    if not sandbox_path.startswith("/"):
        raise SystemExit(f"manifest path must be absolute: {sandbox_path}")
    return os.path.join(str(rootfs), sandbox_path.lstrip("/"))


def resolve_under_rootfs(sandbox_path):
    path = rootfs_path(sandbox_path)
    root = str(rootfs)
    seen = set()
    for _ in range(40):
        if os.path.commonpath([root, os.path.abspath(path)]) != root:
            raise SystemExit(f"{sandbox_path} escapes rootfs via {path}")
        if not os.path.islink(path):
            return path
        if path in seen:
            raise SystemExit(f"{sandbox_path} contains a symlink cycle")
        seen.add(path)
        target = os.readlink(path)
        if os.path.isabs(target):
            path = rootfs_path(target)
        else:
            path = os.path.normpath(os.path.join(os.path.dirname(path), target))
    raise SystemExit(f"{sandbox_path} symlink resolution exceeded limit")


def file_sha256(sandbox_path):
    path = resolve_under_rootfs(sandbox_path)
    if not os.path.isfile(path):
        raise SystemExit(f"selected driver binary missing: {sandbox_path}")
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return "sha256:" + h.hexdigest()


def common(driver_id, label, kind, binary_path, output_schema, model_access):
    return {
        "driver_id": driver_id,
        "label": label,
        "kind": kind,
        "binary_path": binary_path,
        "installed_binary_digest": file_sha256(binary_path),
        "bridge_protocol": "harness_bridge_v2",
        "bridge_protocol_version": 2,
        "turn_input_schema": "RunTurn",
        "output_schema": output_schema,
        "model_access": model_access,
    }


entries = []
for driver in drivers:
    if driver == "pi":
        entry = common("pi", "Pi", "agent", "/usr/local/bin/pi", pi_event_schema, True)
        entry.update({
            "package_name": pi_package,
            "package_version": pi_version,
            "package_shasum": pi_shasum,
            "package_integrity": pi_integrity,
            "event_schema_version": pi_event_schema,
            "node_version": "v" + pi_node_version,
            "installed_config_paths": [
                "/harness-control/driver/pi/models.json",
                "/harness-control/driver/pi/settings.json",
                "/agent-home/.pi/agent/models.json",
                "/agent-home/.pi/agent/settings.json",
            ],
            "writable_state_paths": [
                "/agent-home/.pi/agent",
                "/agent-home/.pi/agent/sessions",
            ],
        })
    elif driver == "sh":
        entry = common("sh", "Shell", "shell", "/usr/local/bin/harness-shell-agent", "shell_pty_v1", False)
    else:
        raise SystemExit(f"unsupported selected driver: {driver}")
    entries.append(entry)

manifest = {
    "schema_version": 1,
    "build_input": {
        "sandbox_agent_drivers": drivers,
    },
    "drivers": entries,
}
out_dir = rootfs / "etc" / "harness-image"
out_dir.mkdir(parents=True, exist_ok=True)
with open(out_dir / "agents.json", "w", encoding="utf-8") as f:
    json.dump(manifest, f, indent=2, sort_keys=True)
    f.write("\n")
PY
}

# The chroot resolves names itself (apt, pip, npm, nodejs.org), so it needs a
# usable resolv.conf and a /etc/hosts during the build — debootstrap leaves it
# with neither. Both files are build-time state: resolv.conf is replaced by
# render_sandbox_resolv_conf at the end, and hosts is reset to localhost-only by
# sanitize_isolated_rootfs, so nothing the build host resolved survives into the
# image.
#
# A host whose resolver cannot be used from the chroot supplies
# SANDBOX_BUILD_DNS_HOOK to fix that up; with the hook unset this is just the
# two default files.
prepare_build_time_dns() {
  log "seeding build-time resolv.conf/hosts into ${ROOTFS_DIR}"
  install -d -m 0755 "${ROOTFS_DIR}/etc"
  printf 'nameserver %s\n' ${BUILD_DNS_SERVERS:-1.1.1.1 8.8.8.8} \
    > "${ROOTFS_DIR}/etc/resolv.conf"
  write_localhost_only_hosts

  if [ -z "${SANDBOX_BUILD_DNS_HOOK}" ]; then
    return
  fi
  if [ ! -x "${SANDBOX_BUILD_DNS_HOOK}" ]; then
    echo "SANDBOX_BUILD_DNS_HOOK is set but not executable: ${SANDBOX_BUILD_DNS_HOOK}" >&2
    exit 1
  fi
  log "running build DNS hook ${SANDBOX_BUILD_DNS_HOOK}"
  "${SANDBOX_BUILD_DNS_HOOK}" "${ROOTFS_DIR}"
}

write_localhost_only_hosts() {
  {
    printf '127.0.0.1\tlocalhost\n'
    printf '::1\t\tlocalhost ip6-localhost ip6-loopback\n'
  } > "${ROOTFS_DIR}/etc/hosts"
}

# The image's own resolv.conf, rendered from the deployment's resolvers. Runs
# after copy_overlay_files so it replaces the build-time one written above. The
# orchestrator may also project a per-generation /etc/hosts over the sandbox
# (renderNetworkHostsProjection), but that projection is conditional — see
# requiresNetworkHostsProjection — so what is baked here has to stand on its own.
render_sandbox_resolv_conf() {
  log "rendering /etc/resolv.conf for: ${SANDBOX_DNS_SERVERS}"
  {
    printf 'nameserver %s\n' ${SANDBOX_DNS_SERVERS}
    printf 'options edns0\n'
    if [ -n "${SANDBOX_DNS_SEARCH}" ]; then
      printf 'search %s\n' "${SANDBOX_DNS_SEARCH}"
    fi
  } > "${ROOTFS_DIR}/etc/resolv.conf"
  chmod 0644 "${ROOTFS_DIR}/etc/resolv.conf"
}

sanitize_isolated_rootfs() {
  log "sanitizing rootfs for sandbox-isolation-v1"
  rm -rf --one-file-system \
    "${ROOTFS_DIR}/workspace" \
    "${ROOTFS_DIR}/agent-home" \
    "${ROOTFS_DIR}/sessions" \
    "${ROOTFS_DIR}/agent-homes" \
    "${ROOTFS_DIR}/harness-secrets" \
    "${ROOTFS_DIR}/root/.cache"
  install -d -m 0755 "${ROOTFS_DIR}/workspace" "${ROOTFS_DIR}/agent-home"
  find "${ROOTFS_DIR}" -maxdepth 1 -type f -name '.gvisor.filestore.*' -delete
  # Drop whatever the build host resolved into /etc/hosts, so the image carries
  # no trace of the machine it was built on. Reset rather than delete: the
  # per-generation hosts projection is conditional, and a sandbox running
  # without it still needs localhost to resolve.
  write_localhost_only_hosts
  chmod 0644 "${ROOTFS_DIR}/etc/hosts"
}

# Both entry paths (fresh build and FORCE=0 reuse) end here, so the image is
# assembled the same way either way.
finalize_rootfs() {
  copy_overlay_files
  render_sandbox_resolv_conf
  generate_agent_manifest
  sanitize_isolated_rootfs
}

parse_agent_drivers

if [ -d "${ROOTFS_DIR}" ] && [ "${FORCE}" != "1" ]; then
  log "reusing existing rootfs at ${ROOTFS_DIR}; set FORCE=1 to rebuild"
  finalize_rootfs
  log "done"
  exit 0
fi

require_root

if ! command -v debootstrap >/dev/null 2>&1; then
  echo "debootstrap is required. Install it with: apt-get update && apt-get install -y debootstrap" >&2
  exit 1
fi

if [ -e "${ROOTFS_DIR}" ]; then
  log "removing existing rootfs because FORCE=1"
  rm -rf --one-file-system "${ROOTFS_DIR}"
fi

log "bootstrapping Ubuntu ${UBUNTU_SUITE} rootfs"
mkdir -p "${ROOTFS_DIR}"
debootstrap --variant=minbase --components="${UBUNTU_COMPONENTS}" "${UBUNTU_SUITE}" "${ROOTFS_DIR}" "${UBUNTU_MIRROR}"

prepare_build_time_dns

log "installing apt packages"
chroot "${ROOTFS_DIR}" /bin/sh -lc "apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ${APT_PACKAGES[*]} && apt-get clean && rm -rf /var/lib/apt/lists/*"

log "installing Python packages"
pip_args=""
if [ -n "${PIP_INDEX_URL}" ]; then
  pip_args="--index-url ${PIP_INDEX_URL}"
  log "using pip index ${PIP_INDEX_URL}"
fi
if [ -n "${PIP_TRUSTED_HOST}" ]; then
  pip_args="${pip_args} --trusted-host ${PIP_TRUSTED_HOST}"
fi
chroot "${ROOTFS_DIR}" /bin/sh -lc "python3 -m pip install --break-system-packages --no-cache-dir ${pip_args} ${PIP_PACKAGES[*]}"

install_duckdb_extensions

install_modern_node_for_pi
install_agent_drivers

finalize_rootfs

log "rootfs ready at ${ROOTFS_DIR}"
