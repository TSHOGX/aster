#!/usr/bin/env bash
# Download the pinned private engine module without persisting its credential.
set -euo pipefail
if [[ -z "${KHORA_READ_TOKEN:-}" ]]; then
  echo 'KHORA_READ_TOKEN must grant read-only access to TSHOGX/khora' >&2
  exit 1
fi
repo_root="$(cd -- "$(dirname -- "$0")/.." && pwd)"
credential_helper="$(mktemp /tmp/harness-go-credential.XXXXXX)"
trap 'rm -f -- "$credential_helper"' EXIT
cat > "$credential_helper" <<'HELPER'
#!/usr/bin/env bash
set -euo pipefail
credential_host=''
credential_path=''
while IFS= read -r field && [[ -n "$field" ]]; do
  case "$field" in
    host=*) credential_host="${field#host=}" ;;
    path=*) credential_path="${field#path=}" ;;
  esac
done
if [[ "${1:-}" == get && "$credential_host" == github.com ]] &&
   [[ "$credential_path" == TSHOGX/khora || "$credential_path" == TSHOGX/khora.git ]]; then
  printf 'username=x-access-token\npassword=%s\n' "$KHORA_READ_TOKEN"
fi
HELPER
chmod 0700 "$credential_helper"
GOPRIVATE=github.com/TSHOGX/khora \
GIT_TERMINAL_PROMPT=0 \
GIT_CONFIG_COUNT=2 \
GIT_CONFIG_KEY_0=credential.https://github.com.helper \
GIT_CONFIG_VALUE_0="!$credential_helper" \
GIT_CONFIG_KEY_1=credential.useHttpPath \
GIT_CONFIG_VALUE_1=true \
  go -C "$repo_root/orchestrator" mod download
