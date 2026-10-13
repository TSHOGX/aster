#!/usr/bin/env python3
"""Build one complete, verified deployment from one committed source revision."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def run(args, cwd=ROOT, env=None):
    subprocess.run([str(a) for a in args], cwd=cwd, env=env, check=True)


def output(*args, cwd=ROOT, env=None):
    return subprocess.check_output(args, cwd=cwd, env=env, text=True).strip()


def build(destination, source_ref=None, os_base=None):
    destination = destination.resolve()
    if destination.exists() or destination.is_relative_to(ROOT):
        raise ValueError("choose a new build directory outside the checkout")
    if source_ref is None and output("git", "status", "--porcelain", "--untracked-files=all"):
        raise ValueError("wait for a clean committed checkout, or select --source-ref explicitly")
    commit = output("git", "rev-parse", "--verify", "--end-of-options", (source_ref or "HEAD") + "^{commit}")
    node_pin = output("git", "show", commit + ":.node-version")
    go_mod = output("git", "show", commit + ":orchestrator/go.mod")
    toolchain = re.search(r"^toolchain (\S+)$", go_mod, re.M)
    env = dict(os.environ, GOTOOLCHAIN=toolchain.group(1) if toolchain else "auto",
               GOSUMDB="sum.golang.org", NEXT_TELEMETRY_DISABLED="1")
    if output("node", "--version") != "v" + node_pin:
        raise ValueError("Node must match the source .node-version")
    if output("go", "env", "GOOS", env=env) != "linux" or output("go", "env", "GOARCH", env=env) != output("go", "env", "GOHOSTARCH", env=env):
        raise ValueError("runtime assembly requires a native privileged Linux builder")
    run(["sudo", "-n", "true"])
    destination.mkdir(parents=True)
    # Use a separate checkout: concurrent edits and ignored private inputs cannot
    # enter either host bundles or the guest image.
    with tempfile.TemporaryDirectory(prefix="aster-rebuild-") as temporary:
        source = Path(temporary) / "source"
        run(["git", "clone", "--quiet", "--shared", "--no-checkout", ROOT, source])
        run(["git", "checkout", "--quiet", "--detach", commit], cwd=source)
        env["NEXT_PUBLIC_PROJECT_BRAND_NAME"] = "Aster"
        run(["bash", "scripts/check.sh", "source"], cwd=source, env=env)
        run(["npm", "ci", "--ignore-scripts", "--prefix", "orchestrator/internal/runtimerelease"], cwd=source, env=env)
        run(["bash", "scripts/check.sh", "backend"], cwd=source, env=env)
        run(["python3", "scripts/candidate.py", "build", destination / "application"], cwd=source, env=env)
        run(["python3", "volumes-studio/scripts/release.py", "build", destination / "studio"], cwd=source, env=env)
        module = subprocess.check_output(
            ["go", "list", "-m", "-f", "{{.Dir}}", "github.com/TSHOGX/khora"],
            cwd=source / "orchestrator", env=env, text=True,
        ).strip()
        engine = destination / "bin/khora"
        engine.parent.mkdir()
        manifest_source = (source / "orchestrator/internal/runtimerelease/manifest.go").read_text()
        engine_revision = re.search(r'^const EngineRevision = "([0-9a-f]+)"$', manifest_source, re.M).group(1)
        run(["go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-X main.version=" + engine_revision, "-o", engine, "./cmd/orchestrator"],
            cwd=module, env=dict(env, CGO_ENABLED="0"))
        if output(engine, "--version") != "orchestrator " + engine_revision:
            raise ValueError("engine server build identity mismatch")
        verifier = destination / "bin/runtime-verify"
        run(["go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-o", verifier, "./cmd/runtime-verify"],
            cwd=source / "orchestrator", env=dict(env, CGO_ENABLED="0"))
        runtime = destination / "runtime"
        command = ["sudo", "-n", "env", "GOTOOLCHAIN=" + env["GOTOOLCHAIN"],
                   "GOSUMDB=" + env["GOSUMDB"],
                   "GOPATH=" + output("go", "env", "GOPATH", env=env),
                   "GOMODCACHE=" + output("go", "env", "GOMODCACHE", env=env),
                   "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory",
                   "GIT_CONFIG_VALUE_0=" + str(source),
                   "PATH=" + os.environ["PATH"], source / "sandbox-image/build-khora-template.sh", runtime]
        if os_base:
            command.append(os_base.resolve())
        run(command, cwd=source)
        app = json.loads((destination / "application/candidate.json").read_text())
        studio = json.loads((destination / "studio/studio-release.json").read_text())
        if app["commit"] != commit or studio["source_commit"] != commit:
            raise ValueError("application and Studio source identities differ")
        manifest = destination / "runtime.manifest.json"
        runtime_digest = (destination / "runtime.manifest.json.sha256").read_text().strip()
        run([verifier, "--root", runtime, runtime_digest])
        report = {
            "format": "aster-deployment-v1", "source_commit": commit,
            "application": "application", "studio": "studio", "engine": "bin/khora",
            "engine_revision": engine_revision,
            "runtime_verifier_sha256": hashlib.sha256(verifier.read_bytes()).hexdigest(),
            "runtime_digest": runtime_digest,
            "engine_sha256": hashlib.sha256(engine.read_bytes()).hexdigest(),
            "engine_module": output("go", "list", "-m", "-f", "{{.Version}}", "github.com/TSHOGX/khora", cwd=source / "orchestrator", env=env),
            "runtime": "runtime", "runtime_manifest": "runtime.manifest.json",
            "runtime_manifest_sha256": hashlib.sha256(manifest.read_bytes()).hexdigest(),
            "public_brand": app["public_brand"],
        }
        (destination / "deployment-build.json").write_text(json.dumps(report, indent=2) + "\n")
    print(f"Complete build ready: {destination}; services have not been changed.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--source-ref", help="explicit committed revision; otherwise require a clean checkout")
    parser.add_argument("--os-base", type=Path, help="optional reviewed pure OS/Pi base; never an application runtime")
    args = parser.parse_args()
    build(args.directory, args.source_ref, args.os_base)


if __name__ == "__main__":
    main()
