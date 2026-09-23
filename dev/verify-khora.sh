#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
usage() {
 cat <<'HELP'
Usage: dev/verify-khora.sh --template DIR --manifest FILE --output DIR [--engine FILE] [--suite release|gateway|mixed|browser|realtime|faults|all]
Builds test executables, then uses sudo -n for a disposable namespace/btrfs substrate.
Default suite: release. Browser/all require a frontend production build and Chromium.
Set PLAYWRIGHT_BROWSERS_PATH when the browser cache is outside the default location.
HELP
}
template= manifest= output= engine= suite=release
while (($#)); do
 case "$1" in
 --help|-h) usage;exit 0;;
 --template|--manifest|--output|--engine|--suite)
  (($#>=2)) || { usage >&2;exit 2; }
  case "$1" in --template) template=$2;; --manifest) manifest=$2;; --output) output=$2;; --engine) engine=$2;; --suite) suite=$2;; esac
  shift 2;;
 *) usage >&2;exit 2;;
 esac
done
[[ -d $template && -f $manifest && -n $output ]] || { usage >&2;exit 2; }
case "$suite" in release|gateway|mixed|browser|realtime|faults|all) ;; *) usage >&2;exit 2;; esac
for program in go python3 ip unshare mount mkfs.btrfs btrfs runsc sudo; do command -v "$program" >/dev/null || { echo "missing prerequisite: $program" >&2;exit 2; }; done
mkdir -p "$output"
output=$(realpath "$output")
module=$(go -C "$repo/orchestrator" list -m -f '{{.Dir}}' github.com/TSHOGX/khora)
version=$(go -C "$repo/orchestrator" list -m -f '{{.Version}}' github.com/TSHOGX/khora)
[[ $version == v0.0.2-0.20260920024301-e8cd021a9c7c ]] || { echo 'unsupported engine module' >&2;exit 2; }
if [[ -z $engine ]]; then
 engine="$output/engine"
 CGO_ENABLED=0 go -C "$module" build -mod=readonly -trimpath -ldflags='-X main.version=e8cd021a9c7c' -o "$engine" ./cmd/orchestrator
fi
engine=$(realpath "$engine")
for binary in orchestrator agent-runner workspace-files runtime-cleanup runtime-fixture; do
 CGO_ENABLED=0 go -C "$repo/orchestrator" build -trimpath -o "$output/$binary" "./cmd/$binary"
done
CGO_ENABLED=0 go -C "$repo/orchestrator" build -trimpath -ldflags='-X main.version=handoff-fixture-candidate' -o "$output/orchestrator-candidate" ./cmd/orchestrator
CGO_ENABLED=0 go -C "$repo/orchestrator" test -c -o "$output/projectfiles.test" ./internal/projectfiles
for package in application product producthttp servicegateway; do
 go -C "$repo/orchestrator" test -tags khoraintegration -c -o "$output/$package.test" "./internal/$package"
done
{
 git -C "$repo" rev-parse HEAD
 git -C "$repo" status --short
 go version
 uname -a
 "$engine" --version
 sha256sum "$manifest" "$output/orchestrator"
} > "$output/revisions.txt"
run() {
 local package=$1 selection=$2 name=$3 timeout=$4
 mkdir -p "$output/$name"
 sudo -n env PROJECT_TEST_ENGINE="$engine" PROJECT_TEST_TEMPLATE="$(realpath "$template")" PROJECT_TEST_MANIFEST="$(realpath "$manifest")" PROJECT_TEST_OUTPUT="$output/$name" PROJECT_TEST_BIN_DIR="$output" PROJECT_TEST_REPO="$repo" PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-}" \
 "$repo/dev/project-substrate.sh" "$output/$package.test" -test.v -test.run "$selection" -test.timeout "$timeout" 2>&1 | tee "$output/$name.log"
}
if [[ $suite == release || $suite == all ]]; then run application '^(TestReleasedExecutableOnKhora|TestReleasedActivationRollback|TestReleasedGatewaySurvivesOwnerOutage|TestReleasedOwnerHandoff)$' release 4m;fi
if [[ $suite == gateway ]]; then run application '^(TestReleasedGatewaySurvivesOwnerOutage|TestReleasedOwnerHandoff)$' gateway 2m;fi
if [[ $suite == mixed || $suite == all ]]; then run application '^TestReleasedMixedWorkload$' mixed 9m;fi
if [[ $suite == browser || $suite == all ]]; then run application '^TestReleasedBrowser$' browser 6m;fi
if [[ $suite == realtime || $suite == all ]]; then run producthttp '^TestResourceHubOnKhora$' resource-hub 3m;fi
if [[ $suite == faults || $suite == all ]]; then
 run product '^(TestProjectLifecycleOnKhora|TestGoAgentRunnersOnKhora|TestProjectFilesOnKhora|TestProjectTerminalsOnKhora)$' product 12m
 run producthttp '^TestComposedProjectRecoveryOnKhora$' recovery 9m
 run servicegateway '^TestProjectServiceAuthorityOnKhora$' services 6m
fi
