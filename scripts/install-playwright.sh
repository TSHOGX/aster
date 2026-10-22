#!/usr/bin/env bash
# Install Playwright browsers and their system packages for one frontend.
# Usage: install-playwright.sh DIR BROWSER...
#
# When APT_ARCHIVE_CACHE names a directory, downloaded .deb files are reused
# from it and saved back, so a slow Ubuntu mirror only matters when the
# package set changes. A retry waits for the dpkg lock rather than failing
# while an earlier apt-get is still running.
set -euo pipefail
dir=$1; shift
cache=${APT_ARCHIVE_CACHE:-}
cache=${cache/#\~/$HOME}
if [[ -n "$cache" ]]; then
  sudo tee /etc/apt/apt.conf.d/90ci-playwright >/dev/null <<'CONF'
Binary::apt::APT::Keep-Downloaded-Packages "true";
APT::Keep-Downloaded-Packages "true";
DPkg::Lock::Timeout "300";
Acquire::Retries "5";
Acquire::http::Timeout "60";
CONF
  mkdir -p "$cache"
  sudo find "$cache" -maxdepth 1 -name '*.deb' -exec cp -t /var/cache/apt/archives/ {} +
fi
for attempt in 1 2; do
  if (cd "$dir" && npx --no-install playwright install --with-deps "$@"); then
    if [[ -n "$cache" ]]; then
      sudo find /var/cache/apt/archives -maxdepth 1 -name '*.deb' -exec cp -t "$cache" {} +
      sudo chown -R "$(id -u):$(id -g)" "$cache"
    fi
    exit 0
  fi
  echo "Playwright install attempt $attempt failed" >&2
done
exit 1
