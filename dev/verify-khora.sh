#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
usage() {
 cat <<'HELP'
Usage: dev/verify-khora.sh --template DIR --manifest FILE --output DIR [--engine FILE] [--suite release|gateway|native|provider|rollback|infrastructure|mixed|combined|browser|realtime|faults|all]
Builds test executables, then uses sudo -n for a disposable namespace/btrfs substrate.
Default suite: release. Browser/combined/all require a frontend production build and Chromium.
Combined is opt-in and adds two real browser tabs, network profiles and gated handoff.
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
case "$suite" in release|gateway|native|provider|rollback|infrastructure|mixed|combined|browser|realtime|faults|all) ;; *) usage >&2;exit 2;; esac
for program in go python3 ip unshare mount mkfs.btrfs btrfs runsc sudo; do command -v "$program" >/dev/null || { echo "missing prerequisite: $program" >&2;exit 2; }; done
if [[ $suite == provider ]]; then : "${PROJECT_PROVIDER_CONFIG:?explicit host-only provider YAML required}"; fi
if [[ $suite == rollback ]]; then : "${PROJECT_ROLLBACK_BINARY:?explicit previously built compatible owner required}"; fi
if [[ $suite == infrastructure ]]; then
 : "${PROJECT_TEMPLATE_CANDIDATE:?explicit second immutable template required}"
 : "${PROJECT_TEMPLATE_CANDIDATE_MANIFEST:?candidate manifest required}"
 [[ -d $PROJECT_TEMPLATE_CANDIDATE && -f $PROJECT_TEMPLATE_CANDIDATE_MANIFEST ]] || { echo 'candidate template/manifest missing' >&2; exit 2; }
 PROJECT_TEMPLATE_CANDIDATE=$(realpath "$PROJECT_TEMPLATE_CANDIDATE")
 PROJECT_TEMPLATE_CANDIDATE_MANIFEST=$(realpath "$PROJECT_TEMPLATE_CANDIDATE_MANIFEST")
fi
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
if [[ $suite == infrastructure ]]; then
 CGO_ENABLED=0 go -C "$module" build -mod=readonly -trimpath -ldflags='-X main.version=e8cd021a9c7c -buildid=qualification-engine-candidate' -o "$output/engine-candidate" ./cmd/orchestrator
fi
build_revision=$(git -C "$repo" rev-parse HEAD)
for binary in orchestrator agent-runner workspace-files runtime-cleanup runtime-fixture; do
 if [[ $binary == orchestrator ]]; then
  CGO_ENABLED=0 go -C "$repo/orchestrator" build -trimpath -ldflags="-X main.commit=$build_revision" -o "$output/$binary" "./cmd/$binary"
 else
  CGO_ENABLED=0 go -C "$repo/orchestrator" build -trimpath -o "$output/$binary" "./cmd/$binary"
 fi
done
CGO_ENABLED=0 go -C "$repo/orchestrator" build -trimpath -ldflags="-X main.version=handoff-fixture-candidate -X main.commit=$build_revision" -o "$output/orchestrator-candidate" ./cmd/orchestrator
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
python3 - "$repo" "$output" <<'PYMETA'
import hashlib, json, os, platform, shutil, subprocess, sys
from pathlib import Path
repo, output = map(Path, sys.argv[1:])
files = subprocess.check_output(["git", "-C", str(repo), "ls-files", "-z", "--cached", "--others", "--exclude-standard"]).decode().split("\0")
def digest(p):
    h = hashlib.sha256()
    with p.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""): h.update(chunk)
    return h.hexdigest()
sources = {name: digest(repo / name) for name in files if name and (repo / name).is_file()}
artifacts = {p.name: digest(p) for p in output.iterdir() if p.is_file() and os.access(p, os.X_OK)}
build_id = repo / "frontend/.next/BUILD_ID"
report = {"head": subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip(), "sources": sources, "artifacts": artifacts, "platform": platform.platform(), "cpu_count": os.cpu_count(), "memory_total_kib": next(line.split()[1] for line in Path("/proc/meminfo").read_text().splitlines() if line.startswith("MemTotal:")), "node": subprocess.check_output(["node", "--version"], text=True).strip() if shutil.which("node") else None, "go": subprocess.check_output(["go", "version"], text=True).strip(), "frontend_build_id": build_id.read_text().strip() if build_id.exists() else None}
(output / "fingerprint.json").write_text(json.dumps(report, indent=2) + "\n")
PYMETA
# Real-provider work is opt-in and never included in the default/all suites.
if [[ $suite == provider ]]; then
 provider_dir=$(mktemp -d /tmp/batterygpt-provider.XXXXXX)
 provider_pid=
 cleanup_provider() {
  if [[ -n $provider_pid ]]; then kill -TERM "$provider_pid" 2>/dev/null || true; wait "$provider_pid" || true; fi
  rm -rf -- "$provider_dir"
 }
 trap cleanup_provider EXIT
 export PROJECT_PROVIDER_SOCKET="$provider_dir/relay.sock" PROJECT_PROVIDER_REPORT="$output/provider-report.json"
 "$output/runtime-fixture" provider-relay --config "$(realpath "$PROJECT_PROVIDER_CONFIG")" --socket "$PROJECT_PROVIDER_SOCKET" --report "$PROJECT_PROVIDER_REPORT" > "$output/provider-relay.log" 2>&1 &
 provider_pid=$!
 for ((attempt=0;attempt<50;attempt++)); do
  [[ -S $PROJECT_PROVIDER_SOCKET && -f $PROJECT_PROVIDER_REPORT ]] && break
  kill -0 "$provider_pid" 2>/dev/null || { cat "$output/provider-relay.log" >&2; exit 1; }
  sleep 0.1
 done
 [[ -S $PROJECT_PROVIDER_SOCKET && -f $PROJECT_PROVIDER_REPORT ]] || { echo 'provider relay did not become ready' >&2; exit 1; }
 PROJECT_PROVIDER_MODEL=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["model"])' "$PROJECT_PROVIDER_REPORT")
 export PROJECT_PROVIDER_MODEL
fi
run() {
 local package=$1 selection=$2 name=$3 timeout=$4
 mkdir -p "$output/$name"
 sudo -n env PROJECT_TEST_ENGINE="$engine" PROJECT_TEST_TEMPLATE="$(realpath "$template")" PROJECT_TEST_MANIFEST="$(realpath "$manifest")" PROJECT_TEST_OUTPUT="$output/$name" PROJECT_TEST_BIN_DIR="$output" PROJECT_TEST_REPO="$repo" PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-}" PROJECT_PROVIDER_SOCKET="${PROJECT_PROVIDER_SOCKET:-}" PROJECT_PROVIDER_REPORT="${PROJECT_PROVIDER_REPORT:-}" PROJECT_PROVIDER_MODEL="${PROJECT_PROVIDER_MODEL:-}" PROJECT_ROLLBACK_BINARY="${PROJECT_ROLLBACK_BINARY:-}" PROJECT_BROWSER_OWNER_BINARY="${PROJECT_BROWSER_OWNER_BINARY:-}" PROJECT_TEST_TEMPLATE_CANDIDATE="${PROJECT_TEMPLATE_CANDIDATE:-}" PROJECT_TEST_TEMPLATE_CANDIDATE_MANIFEST="${PROJECT_TEMPLATE_CANDIDATE_MANIFEST:-}" \
 "$repo/dev/project-substrate.sh" "$output/$package.test" -test.v -test.run "$selection" -test.timeout "$timeout" 2>&1 | tee "$output/$name.log"
}
if [[ $suite == release || $suite == all ]]; then run application '^(TestReleasedExecutableOnKhora|TestReleasedActivationRollback|TestReleasedGatewaySurvivesOwnerOutage|TestReleasedOwnerHandoff|TestReleasedNativeRetrySettlement|TestReleasedNativeCompactionSettlement)$' release 4m;fi
if [[ $suite == gateway ]]; then run application '^(TestReleasedGatewaySurvivesOwnerOutage|TestReleasedOwnerHandoff)$' gateway 2m;fi
if [[ $suite == native ]]; then run application '^(TestReleasedNativeRetrySettlement|TestReleasedNativeCompactionSettlement)$' native 3m;fi
if [[ $suite == provider ]]; then run application '^TestReleasedRealProviderHandoff$' provider 5m;fi
if [[ $suite == rollback ]]; then run application '^TestReleasedRevisionRollback$' rollback 4m;fi
if [[ $suite == infrastructure ]]; then run application '^TestReleasedInfrastructureRollback$' infrastructure 4m;fi
if [[ $suite == mixed || $suite == all ]]; then run application '^TestReleasedMixedWorkload$' mixed 9m;fi
if [[ $suite == combined ]]; then run application '^TestReleasedCombinedWorkload$' combined 10m;fi
if [[ $suite == browser || $suite == all ]]; then run application '^TestReleasedBrowser$' browser 6m;fi
if [[ $suite == realtime || $suite == all ]]; then run producthttp '^TestResourceHubOnKhora$' resource-hub 3m;fi
if [[ $suite == faults || $suite == all ]]; then
 run product '^(TestProjectLifecycleOnKhora|TestGoAgentRunnersOnKhora|TestProjectFilesOnKhora|TestProjectTerminalsOnKhora)$' product 12m
 run producthttp '^TestComposedProjectRecoveryOnKhora$' recovery 9m
 run servicegateway '^TestProjectServiceAuthorityOnKhora$' services 6m
fi
