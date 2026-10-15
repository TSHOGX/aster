#!/usr/bin/env python3
"""Build and verify an independent Evaluation Space bundle."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import socket
import subprocess
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = "evaluation-space-release.json"


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def inventory(root):
    result = {}
    for path in sorted(root.rglob("*")):
        name = path.relative_to(root).as_posix()
        if name == MANIFEST:
            continue
        if path.is_symlink():
            target = os.readlink(path)
            if os.path.isabs(target) or not path.resolve().is_relative_to(root) or not path.exists():
                raise ValueError(f"unsafe bundle symlink: {name}")
            result[name] = {"symlink": target}
        elif path.is_file():
            result[name] = {"sha256": digest(path), "mode": stat.S_IMODE(path.stat().st_mode)}
        elif not path.is_dir():
            raise ValueError(f"unsupported bundle entry: {name}")
    return result


def run(command, cwd, env=None):
    subprocess.run(command, cwd=cwd, env=env, check=True)


def verify(root):
    document = json.loads((root / MANIFEST).read_text())
    if document.get("format") != "evaluation-space-release-v1" or document.get("base_path") != "/evaluation-space/":
        raise ValueError("unsupported Evaluation Space release")
    if inventory(root) != document.get("files"):
        raise ValueError("Evaluation Space release inventory changed")
    for name in ["bin/evaluation-space", "bin/evaluation-space-backup", "bin/evaluation-space-restore", "frontend/server.js"]:
        if not (root / name).is_file():
            raise ValueError(f"release is missing {name}")
    return document


def smoke(root):
    """Verify the packaged frontend serves prefixed assets and deep links."""
    root = root.resolve()
    verify(root)
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    env = dict(os.environ, HOSTNAME="127.0.0.1", PORT=str(port), NEXT_TELEMETRY_DISABLED="1")
    process = subprocess.Popen(["node", "frontend/server.js"], cwd=root, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        origin = f"http://127.0.0.1:{port}"
        for _ in range(100):
            if process.poll() is not None:
                raise ValueError("packaged Evaluation Space frontend exited")
            try:
                with urllib.request.urlopen(origin + "/evaluation-space/", timeout=1) as response:
                    html = response.read().decode()
                break
            except (OSError, urllib.error.URLError):
                time.sleep(0.1)
        else:
            raise ValueError("packaged Evaluation Space frontend did not become ready")
        assets = set()
        for marker in ('src="', 'href="'):
            start = 0
            while True:
                start = html.find(marker, start)
                if start < 0:
                    break
                start += len(marker)
                end = html.find('"', start)
                if end < 0:
                    break
                value = html[start:end]
                if value.startswith("/evaluation-space/_next/"):
                    assets.add(value)
        if not assets:
            raise ValueError("packaged Evaluation Space page has no prefix-safe assets")
        for asset in assets:
            with urllib.request.urlopen(origin + asset, timeout=5) as response:
                if response.status != 200 or not response.read():
                    raise ValueError(f"packaged asset unavailable: {asset}")
        with urllib.request.urlopen(origin + "/evaluation-space/groups/smoke/", timeout=5) as response:
            if response.status != 200:
                raise ValueError("packaged Evaluation Space deep link unavailable")
    finally:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
    verify(root)
    print("Packaged Evaluation Space root, assets and deep link smoke passed.")


def build(target, offline=False, source_ref="HEAD"):
    target = target.resolve()
    if target.exists() or target.is_relative_to(ROOT):
        raise ValueError("output must be a new directory outside the source checkout")
    commit = subprocess.check_output(["git", "rev-parse", "--verify", source_ref + "^{commit}"], cwd=ROOT, text=True).strip()
    with __import__("tempfile").TemporaryDirectory(prefix="evaluation-space-release-") as temporary:
        scratch = Path(temporary)
        source = scratch / "source"
        source.mkdir()
        subprocess.run(["git", "archive", "--format=tar", commit, "evaluation-space", "contracts", "web"], cwd=ROOT, stdout=open(scratch / "source.tar", "wb"), check=True)
        run(["tar", "-xf", str(scratch / "source.tar"), "-C", str(source)], ROOT)
        app = source / "evaluation-space"
        env = dict(os.environ, NEXT_TELEMETRY_DISABLED="1")
        run(["npm", "ci"] + (["--offline"] if offline else []), app / "frontend", env)
        run(["npm", "run", "lint"], app / "frontend", env)
        run(["npm", "run", "typecheck"], app / "frontend", env)
        run(["npm", "run", "build"], app / "frontend", env)
        run(["go", "test", "-mod=readonly", "./..."], app / "backend", env)
        staged = scratch / "bundle"
        (staged / "bin").mkdir(parents=True)
        for command in ["evaluation-space", "evaluation-space-backup", "evaluation-space-restore"]:
            run(["go", "build", "-trimpath", "-mod=readonly", "-o", str(staged / "bin" / command), "./cmd/" + command], app / "backend", env)
        shutil.copytree(app / "frontend/.next/standalone", staged / "frontend", symlinks=True)
        shutil.copytree(app / "frontend/.next/static", staged / "frontend/evaluation-space/frontend/.next/static")
        (staged / "frontend/server.js").write_text("require('./evaluation-space/frontend/server.js');\n")
        shutil.copytree(app / "deploy", staged / "deploy") if (app / "deploy").exists() else (staged / "deploy").mkdir()
        document = {"format": "evaluation-space-release-v1", "source_commit": commit, "base_path": "/evaluation-space/", "go": subprocess.check_output(["go", "version"], text=True, env=env).strip(), "frontend_build_id": (app / "frontend/.next/BUILD_ID").read_text().strip(), "files": inventory(staged)}
        (staged / MANIFEST).write_text(json.dumps(document, indent=2) + "\n")
        verify(staged)
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copytree(staged, target, symlinks=True)
    verify(target)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["build", "verify", "smoke"])
    parser.add_argument("directory", type=Path)
    parser.add_argument("--offline", action="store_true")
    parser.add_argument("--source-ref", default="HEAD")
    args = parser.parse_args()
    if args.action == "build":
        build(args.directory, args.offline, args.source_ref)
    elif args.action == "smoke":
        smoke(args.directory)
    else:
        verify(args.directory.resolve())


if __name__ == "__main__":
    main()
