#!/usr/bin/env bash
# Install Playwright browsers and their system packages for one frontend.
# Each attempt is bounded so a stalled mirror fails fast and retries instead of
# consuming the whole job timeout. Usage: install-playwright.sh DIR BROWSER...
set -euo pipefail
dir=$1; shift
for attempt in 1 2; do
  if (cd "$dir" && timeout 480 npx --no-install playwright install --with-deps "$@"); then
    exit 0
  fi
  echo "Playwright install attempt $attempt failed or timed out" >&2
done
exit 1
