#!/usr/bin/env python3
"""Build source-only host bundles; verify bytes before deployment inspection."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import stat
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = "candidate.json"
FRONTENDS = {"frontend": "", "admin-frontend": "/admin"}


def output(*args, cwd=ROOT):
    return subprocess.check_output(args, cwd=cwd, text=True).strip()


def digest(path):
    h = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def inventory(root):
    result = {}
    for path in sorted(root.rglob("*")):
        name = path.relative_to(root).as_posix()
        if name == MANIFEST:
            continue
        if path.is_symlink():
            target = os.readlink(path)
            if os.path.isabs(target) or not path.resolve().is_relative_to(root.resolve()) or not path.exists():
                raise ValueError(f"unsafe or dangling artifact symlink: {name}")
            result[name] = {"symlink": target}
        elif path.is_file():
            result[name] = {"sha256": digest(path), "mode": stat.S_IMODE(path.stat().st_mode)}
        elif not path.is_dir():
            raise ValueError(f"unsupported artifact entry: {name}")
    return result


def verify(root):
    manifest_path = root / MANIFEST
    if manifest_path.is_symlink():
        raise ValueError("candidate manifest must be a regular file")
    report = json.loads(manifest_path.read_text())
    if report.get("format") != 2 or not re.fullmatch(r"[0-9a-f]{40}", report.get("commit", "")):
        raise ValueError("invalid candidate identity")
    if inventory(root) != report["files"]:
        raise ValueError("candidate files, modes or links differ from the recorded inventory")
    if set(report.get("frontends", {})) != set(FRONTENDS):
        raise ValueError("both frontend identities are required")
    required = ["bin/orchestrator", "bin/edge-proxy"]
    for frontend, base in FRONTENDS.items():
        required.extend(f"{frontend}/{name}" for name in ("server.js", ".next/BUILD_ID", "public/favicon.svg"))
        if report["frontends"][frontend].get("base_path") != base:
            raise ValueError("frontend base path mismatch")
    for name in required:
        if name not in report["files"]:
            raise ValueError(f"missing required artifact: {name}")
    office_pages = list((root / "frontend/public/office").glob("*/document.html"))
    if not office_pages:
        raise ValueError("missing required Office viewer assets")
    for page in office_pages:
        for asset in ("document.js", "document.css", "spreadsheet.html", "spreadsheet.js", "spreadsheet.css", "presentation.html", "presentation.js", "presentation.css"):
            if not (page.parent / asset).is_file():
                raise ValueError("missing required Office viewer entry")
    for frontend in FRONTENDS:
        if (root / frontend / ".next/BUILD_ID").read_text().strip() != report["frontends"][frontend]["build_id"]:
            raise ValueError("frontend build identity mismatch")
    return report


def smoke(root):
    """Exercise both independently packaged servers and their assets."""
    for frontend, base in FRONTENDS.items():
        smoke_frontend(root, frontend, base)


def smoke_frontend(root, frontend, base):
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    env = {**os.environ, "HOSTNAME": "127.0.0.1", "PORT": str(port), "NODE_ENV": "production"}
    with tempfile.TemporaryFile() as log:
        server = subprocess.Popen(["node", "server.js"], cwd=root / frontend, env=env, stdout=log, stderr=log)
        try:
            origin = f"http://127.0.0.1:{port}"
            deadline = time.monotonic() + 30
            while True:
                if server.poll() is not None:
                    raise ValueError("packaged frontend exited before readiness")
                try:
                    with urllib.request.urlopen(origin + (base or "/"), timeout=1) as response:
                        html = response.read().decode()
                    break
                except (urllib.error.URLError, TimeoutError):
                    if time.monotonic() >= deadline:
                        raise ValueError("packaged frontend did not become ready")
                    time.sleep(0.1)
            chunk = re.search(r'src="(' + re.escape(base) + r'/_next/static/[^" ]+\.js(?:\?[^" ]*)?)"', html)
            if not chunk:
                raise ValueError("packaged frontend did not reference a JavaScript chunk")
            for asset in (base + "/favicon.svg", chunk.group(1)):
                with urllib.request.urlopen(origin + asset, timeout=5) as response:
                    if response.status != 200 or not response.read():
                        raise ValueError(f"empty or unavailable frontend asset: {asset}")
            if base:
                with urllib.request.urlopen(origin + base + "/users/qualification", timeout=5) as response:
                    if response.status != 200 or not response.read():
                        raise ValueError("admin deep link unavailable")
            for page in (root / frontend / "public/office").glob("*/document.html"):
                for asset in page.parent.iterdir():
                    if not asset.is_file():
                        continue
                    url = "/" + asset.relative_to(root / frontend / "public").as_posix()
                    with urllib.request.urlopen(origin + url, timeout=5) as response:
                        if response.status != 200 or not response.read():
                            raise ValueError("Office viewer asset unavailable")
                        if response.headers.get("Access-Control-Allow-Origin") != "*":
                            raise ValueError("Office viewer assets need opaque-origin CORS")
                        policy = response.headers.get("Content-Security-Policy", "")
                        if "connect-src blob:" not in policy or "form-action 'none'" not in policy:
                            raise ValueError("Office viewer isolation policy missing")
        finally:
            server.terminate()
            try:
                server.wait(timeout=5)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()


def build(destination, source_ref=None):
    if source_ref is None and output("git", "status", "--porcelain", "--untracked-files=all"):
        raise ValueError("candidate builds require a clean committed checkout")
    if output("go", "env", "GOOS") != "linux" or output("go", "env", "GOARCH") != output("go", "env", "GOHOSTARCH"):
        raise ValueError("build host candidates on Linux for their native architecture")
    if destination.is_relative_to(ROOT):
        raise ValueError("place candidate output outside the source checkout")
    commit = output("git", "rev-parse", "--verify", "--end-of-options", (source_ref or "HEAD") + "^{commit}")
    version = "candidate-" + commit[:12]
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.mkdir()  # Never overwrite a previous candidate, including a failed build.
    with tempfile.TemporaryDirectory(prefix="candidate-source-") as temporary:
        temporary = Path(temporary)
        source = temporary / "source"
        source.mkdir()
        archive = temporary / "source.tar"
        subprocess.run(["git", "archive", "--format=tar", "-o", str(archive), commit], cwd=ROOT, check=True)
        subprocess.run(["tar", "-xf", str(archive), "-C", str(source)], check=True)
        # The snapshot excludes ignored .env files, private inputs and stale build output.
        for frontend in FRONTENDS:
            directory = source / frontend
            subprocess.run(["npm", "ci"], cwd=directory, check=True)
            subprocess.run(["npm", "run", "build"], cwd=directory, check=True)
            shutil.copytree(directory / ".next/standalone" / frontend, destination / frontend, symlinks=True)
            shutil.copytree(directory / ".next/static", destination / frontend / ".next/static")
            shutil.copytree(directory / "public", destination / frontend / "public")
        shutil.copyfile(source / "docs/third-party-notices.md", destination / "THIRD-PARTY-NOTICES.md")
        (destination / "bin").mkdir()
        for binary in ("orchestrator", "edge-proxy"):
            subprocess.run([
                "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false",
                "-ldflags", f"-X main.version={version} -X main.commit={commit}",
                "-o", str(destination / "bin" / binary), f"./cmd/{binary}",
            ], cwd=source / "orchestrator", env={**os.environ, "CGO_ENABLED": "0"}, check=True)
        smoke(destination)
        report = {
            "format": 2, "commit": commit, "version": version,
            "platform": output("go", "env", "GOOS") + "/" + output("go", "env", "GOARCH"),
            "go": output("go", "version"), "node": output("node", "--version"),
            "frontends": {name: {"build_id": (destination / name / ".next/BUILD_ID").read_text().strip(),
                                 "lock_sha256": digest(source / name / "package-lock.json"), "base_path": base}
                          for name, base in FRONTENDS.items()},
            "go_sum_sha256": digest(source / "orchestrator/go.sum"),
            "public_brand": os.environ.get("NEXT_PUBLIC_PROJECT_BRAND_NAME", ""),
            "verification": {"standalone_http_assets": "passed", "runtime_qualification": "not_run"},
            "files": inventory(destination),
        }
        (destination / MANIFEST).write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
        verify(destination)
    print(f"Candidate built and verified: {destination}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("build", "verify", "inspect"))
    parser.add_argument("directory", type=Path)
    parser.add_argument("--config", type=Path, help="reviewed target application config; inspect only")
    parser.add_argument("--source-ref", help="explicit committed source revision; build only")
    args = parser.parse_args()
    if (args.action == "inspect") != (args.config is not None):
        parser.error("--config is required only with inspect")
    if args.source_ref and args.action != "build":
        parser.error("--source-ref is valid only with build")
    root = args.directory.resolve()
    if args.action == "build":
        build(root, args.source_ref)
    else:
        report = verify(root)
        if args.action == "inspect":
            # Read-only: the existing command never opens the Store or contacts the engine.
            subprocess.run([str(root / "bin/orchestrator"), "release-identity", "--config", str(args.config.resolve())], check=True)
        else:
            print(json.dumps({"commit": report["commit"], "platform": report["platform"], "frontends": report["frontends"], "verified": True}))


if __name__ == "__main__":
    main()
