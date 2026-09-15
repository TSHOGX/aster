#!/usr/bin/env bash
# Install open-source pi-mcp-extension into the sandbox rootfs share dir.
# Run on the Linux build/lab host from the repo root:
#   ./scripts/install-pi-mcp-extension.sh
set -Eeuo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
FILES_EXT="$ROOT/sandbox-image/files/usr/local/share/pi-extensions/mcp"
ROOTFS_EXT="$ROOT/sandbox-image/rootfs/usr/local/share/pi-extensions/mcp"

[[ -f "$FILES_EXT/index.ts" ]] || { echo "missing $FILES_EXT (source not in tree)" >&2; exit 1; }

echo "[install-pi-mcp] npm install in files tree"
(
  cd "$FILES_EXT"
  npm install --omit=dev --no-fund --no-audit
)

if [[ -d "$ROOT/sandbox-image/rootfs" ]]; then
  echo "[install-pi-mcp] syncing into live rootfs"
  mkdir -p "$(dirname "$ROOTFS_EXT")"
  rsync -a --delete "$FILES_EXT/" "$ROOTFS_EXT/"
  chmod -R a+rX "$ROOTFS_EXT" || true
fi

# Keep entrypoint patched copy in sync if present in files/
if [[ -f "$ROOT/sandbox-image/files/usr/local/bin/harness-agent-entrypoint" && -d "$ROOT/sandbox-image/rootfs/usr/local/bin" ]]; then
  cp "$ROOT/sandbox-image/files/usr/local/bin/harness-agent-entrypoint" \
     "$ROOT/sandbox-image/rootfs/usr/local/bin/harness-agent-entrypoint"
  chmod +x "$ROOT/sandbox-image/rootfs/usr/local/bin/harness-agent-entrypoint"
fi

echo "[install-pi-mcp] done"
echo "  extension: $FILES_EXT"
echo "  demo mcp.json seeds demo tools: mcp_demo_echo / mcp_demo_get_current_time / mcp_demo_add_numbers"
echo "  new Pi sessions auto-link via harness-agent-entrypoint"
