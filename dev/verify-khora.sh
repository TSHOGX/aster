#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
usage() {
 cat <<'HELP'
Usage: dev/verify-khora.sh --template DIR --manifest FILE --output NEW_DIR [--engine FILE] [--candidate DIR] [--secondary-template DIR --secondary-manifest FILE] [--suite release|slots|paths|publication|suspension|files|network|gateway|native|provider|mixed|combined|browser|realtime|faults|all]
Builds test executables, then uses sudo -n for a disposable namespace/btrfs substrate.
Default suite: release. Browser/combined/all require both frontend production builds and Chromium.
Combined is opt-in and adds two real browser tabs, network profiles and gated handoff.
Set PLAYWRIGHT_BROWSERS_PATH when the browser cache is outside the default location.
--candidate verifies and uses the packaged owner/frontends instead of rebuilding them.
All includes release, files, network, mixed, browser, realtime and faults; excludes provider/combined.
HELP
}
template= manifest= output= engine= candidate= secondary_template= secondary_manifest= suite=release
while (($#)); do
 case "$1" in
 --help|-h) usage;exit 0;;
 --template|--manifest|--output|--engine|--candidate|--suite|--secondary-template|--secondary-manifest)
  (($#>=2)) || { usage >&2;exit 2; }
  case "$1" in --secondary-template) secondary_template=$2;; --secondary-manifest) secondary_manifest=$2;; --template) template=$2;; --manifest) manifest=$2;; --output) output=$2;; --engine) engine=$2;; --candidate) candidate=$2;; --suite) suite=$2;; esac
  shift 2;;
 *) usage >&2;exit 2;;
 esac
done
[[ -d $template && -f $manifest && -n $output ]] || { usage >&2;exit 2; }
case "$suite" in release|slots|paths|publication|suspension|files|network|gateway|native|provider|mixed|combined|browser|realtime|faults|all) ;; *) usage >&2;exit 2;; esac
if [[ $suite == paths ]]; then
 [[ -d $secondary_template && -f $secondary_manifest ]] || { echo 'paths requires a second immutable runtime and manifest' >&2;exit 2; }
 secondary_template=$(realpath "$secondary_template")
 secondary_manifest=$(realpath "$secondary_manifest")
fi
for program in go python3 ip unshare mount mkfs.btrfs btrfs runsc sudo; do command -v "$program" >/dev/null || { echo "missing prerequisite: $program" >&2;exit 2; }; done
if [[ $suite == provider ]]; then : "${PROJECT_PROVIDER_CONFIG:?explicit host-only provider YAML required}"; fi
if [[ -n $candidate ]]; then
 candidate=$(realpath "$candidate")
 python3 "$repo/scripts/candidate.py" verify "$candidate"
 [[ -z $(git -C "$repo" status --porcelain) ]] || { echo 'candidate qualification requires its clean source checkout' >&2; exit 2; }
 [[ $(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["commit"])' "$candidate/candidate.json") == "$(git -C "$repo" rev-parse HEAD)" ]] || { echo 'candidate/source revision mismatch' >&2; exit 2; }
fi
if [[ $suite == browser || $suite == combined || $suite == all ]]; then
 command -v node >/dev/null
 [[ -f "$repo/frontend/node_modules/@playwright/test/cli.js" && -f "${candidate:-$repo}/frontend/.next/BUILD_ID" && -f "${candidate:-$repo}/admin-frontend/.next/BUILD_ID" ]] || { echo 'browser qualification requires installed Playwright and both production frontend builds' >&2; exit 2; }
fi

[[ ! -e $output ]] || { echo 'use a new output directory; never mix qualification evidence' >&2; exit 2; }
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

build_revision=$(git -C "$repo" rev-parse HEAD)
for binary in orchestrator agent-runner workspace-files runtime-cleanup runtime-fixture; do
 if [[ $binary == orchestrator && -n $candidate ]]; then
  cp "$candidate/bin/$binary" "$output/$binary"
 elif [[ $binary == orchestrator ]]; then
  CGO_ENABLED=0 go -C "$repo/orchestrator" build -trimpath -ldflags="-X main.commit=$build_revision" -o "$output/$binary" "./cmd/$binary"
 else
  CGO_ENABLED=0 go -C "$repo/orchestrator" build -trimpath -o "$output/$binary" "./cmd/$binary"
 fi
done
CGO_ENABLED=0 go -C "$repo/orchestrator" build -trimpath -ldflags="-X main.version=handoff-fixture-candidate -X main.commit=$build_revision" -o "$output/orchestrator-candidate" ./cmd/orchestrator
packages=(application)
case "$suite" in
 paths) packages=(application product);;
 slots) packages=(product);;
 realtime) packages=(producthttp);;
 faults) packages=(product producthttp servicegateway);;
 all) packages=(application product producthttp servicegateway);;
esac
if [[ $suite == faults || $suite == paths || $suite == all ]]; then
 CGO_ENABLED=0 go -C "$repo/orchestrator" test -c -o "$output/projectfiles.test" ./internal/projectfiles
fi
for package in "${packages[@]}"; do
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
python3 - "$repo" "$output" "$candidate" "$engine" "$manifest" "$secondary_manifest" <<'PYMETA'
import hashlib, json, os, platform, shutil, subprocess, sys
from pathlib import Path
repo, output = map(Path, sys.argv[1:3])
candidate = Path(sys.argv[3]) if sys.argv[3] else None
files = subprocess.check_output(["git", "-C", str(repo), "ls-files", "-z", "--cached", "--others", "--exclude-standard"]).decode().split("\0")
def digest(p):
    h = hashlib.sha256()
    with p.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""): h.update(chunk)
    return h.hexdigest()
sources = {name: digest(repo / name) for name in files if name and (repo / name).is_file()}
artifacts = {p.name: digest(p) for p in output.iterdir() if p.is_file() and os.access(p, os.X_OK)}
build_ids = {name: (candidate or repo) / name / ".next/BUILD_ID" for name in ("frontend", "admin-frontend")}
report = {"head": subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip(), "sources": sources, "artifacts": artifacts, "platform": platform.platform(), "cpu_count": os.cpu_count(), "memory_total_kib": next(line.split()[1] for line in Path("/proc/meminfo").read_text().splitlines() if line.startswith("MemTotal:")), "node": subprocess.check_output(["node", "--version"], text=True).strip() if shutil.which("node") else None, "go": subprocess.check_output(["go", "version"], text=True).strip(), "frontend_build_ids": {name: path.read_text().strip() if path.exists() else None for name, path in build_ids.items()}}
report["engine_sha256"] = digest(Path(sys.argv[4]))
report["runtime_manifest_sha256"] = digest(Path(sys.argv[5]))
report["secondary_runtime_manifest_sha256"] = digest(Path(sys.argv[6])) if sys.argv[6] else None
report["candidate_manifest_sha256"] = digest(candidate / "candidate.json") if candidate else None
(output / "fingerprint.json").write_text(json.dumps(report, indent=2) + "\n")
PYMETA
# Real-provider work is opt-in and never included in the default/all suites.
if [[ $suite == provider ]]; then
 provider_dir=$(mktemp -d /tmp/provider-config.XXXXXX)
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
 # A renamed/deleted test must fail qualification rather than produce a green no-op.
 local listed
 listed=$("$output/$package.test" -test.list "$selection")
 [[ $listed == *Test* ]] || { echo "no tests selected for $name" >&2; return 2; }
 mkdir -p "$output/$name"
 sudo -n env PROJECT_TEST_ENGINE="$engine" PROJECT_TEST_TEMPLATE="$(realpath "$template")" PROJECT_TEST_SECONDARY_TEMPLATE="$secondary_template" PROJECT_TEST_SECONDARY_MANIFEST="$secondary_manifest" PROJECT_TEST_MANIFEST="$(realpath "$manifest")" PROJECT_TEST_OUTPUT="$output/$name" PROJECT_TEST_BIN_DIR="$output" PROJECT_TEST_REPO="$repo" PROJECT_TEST_CANDIDATE="$candidate" PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-}" PROJECT_PROVIDER_SOCKET="${PROJECT_PROVIDER_SOCKET:-}" PROJECT_PROVIDER_REPORT="${PROJECT_PROVIDER_REPORT:-}" PROJECT_PROVIDER_MODEL="${PROJECT_PROVIDER_MODEL:-}" \
 "$repo/dev/project-substrate.sh" "$output/$package.test" -test.v -test.run "$selection" -test.timeout "$timeout" 2>&1 | tee "$output/$name.log"
}
if [[ $suite == release || $suite == all ]]; then run application '^(TestReleasedNativeSchedules|TestReleasedExecutableOnKhora|TestReleasedActivationTarget|TestReleasedGatewaySurvivesOwnerOutage|TestReleasedOwnerHandoff|TestReleasedNativeRetrySettlement|TestReleasedNativeCompactionSettlement|TestReleasedQueuedSteerConsumption|TestReleasedQueuedSteerStopRemovesUnconsumedInput)$' release 5m;fi
if [[ $suite == paths ]]; then
 run application '^TestReleasedRuntimePathsAndVersions$' paths 4m
 run product '^(TestGoAgentRunnersOnKhora|TestProjectFilesOnKhora|TestProjectTerminalsOnKhora|TestProjectVolumeSlotsOnKhora)$' role-paths 8m
fi
if [[ $suite == slots ]]; then run product '^TestProjectVolumeSlotsOnKhora$' slots 3m;fi
if [[ $suite == network || $suite == all ]]; then run application '^TestReleasedDatabaseNetworkPolicy$' network 4m;fi
if [[ $suite == suspension || $suite == all ]]; then run application '^TestReleasedAccountSuspensionReconcilesAfterOwnerRestart$' suspension 4m;fi
if [[ $suite == files || $suite == all ]]; then run application '^TestReleasedPausedVolumeFiles$' files 4m;fi
if [[ $suite == gateway ]]; then run application '^(TestReleasedGatewaySurvivesOwnerOutage|TestReleasedOwnerHandoff)$' gateway 2m;fi
if [[ $suite == native ]]; then run application '^(TestReleasedNativeRetrySettlement|TestReleasedNativeCompactionSettlement|TestReleasedQueuedSteerConsumption|TestReleasedQueuedSteerStopRemovesUnconsumedInput)$' native 4m;fi
if [[ $suite == provider ]]; then run application '^TestReleasedRealProviderHandoff$' provider 5m;fi
if [[ $suite == mixed || $suite == all ]]; then run application '^TestReleasedMixedWorkload$' mixed 9m;fi
if [[ $suite == combined ]]; then run application '^TestReleasedCombinedWorkload$' combined 10m;fi
if [[ $suite == browser || $suite == all ]]; then run application '^(TestReleasedBrowser|TestReleasedQueueBrowser)$' browser 7m;fi
if [[ $suite == realtime || $suite == all ]]; then run producthttp '^TestResourceHubOnKhora$' resource-hub 3m;fi
if [[ $suite == faults || $suite == all ]]; then
 run product '^(TestProjectLifecycleOnKhora|TestProjectVolumeSlotsOnKhora|TestGoAgentRunnersOnKhora|TestProjectFilesOnKhora|TestProjectTerminalsOnKhora)$' product 12m
 run producthttp '^TestComposedProjectRecoveryOnKhora$' recovery 9m
 run servicegateway '^TestProjectServiceAuthorityOnKhora$' services 6m
fi

if [[ $suite == publication || $suite == all ]]; then run application '^TestReleasedBuiltinVolumePublication$' publication 4m;fi
