#!/usr/bin/env python3
"""Build and verify an independent Evolution Space bundle."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = "evolution-space-release.json"
COMMANDS = ("evolution-space", "evolution-space-catalog", "evolution-space-backup", "evolution-space-restore")


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
    if document.get("format") != "evolution-space-release-v1" or document.get("base_path") != "/evolution-space/":
        raise ValueError("unsupported Evolution Space release")
    if inventory(root) != document.get("files"):
        raise ValueError("Evolution Space release inventory changed")
    for name in [*("bin/" + command for command in COMMANDS), "frontend/server.js"]:
        if not (root / name).is_file():
            raise ValueError(f"release is missing {name}")
    return document


def smoke_maintenance(root):
    """Exercise only disposable application state with packaged operator binaries."""
    with tempfile.TemporaryDirectory(prefix="evolution-space-maintenance-") as temporary:
        scratch = Path(temporary)
        state = scratch / "state"
        content = scratch / "evidence.txt"
        content.write_bytes(b"Retained smoke evidence")
        ref = {"id": "smoke-artifact", "group_id": "smoke-group", "grant_id": "smoke-grant",
               "policy_revision": 1, "schema_digest": "sha256:" + hashlib.sha256(b"schema").hexdigest(),
               "digest": "sha256:" + digest(content), "size": content.stat().st_size, "media_type": "text/plain"}

        def catalog(action, document, directory=state, extra=()):
            result = subprocess.run([str(root / "bin/evolution-space-catalog"), "--state-directory", str(directory),
                                     "--action", action, *extra], input=json.dumps(document), text=True,
                                    capture_output=True, check=True)
            return json.loads(result.stdout)

        catalog("seal-artifact", ref, extra=("--artifact-file", str(content)))
        before = catalog("inspect-artifact", ref)
        if before["artifact"] != ref or before["pins"] or "expired_at" in before:
            raise ValueError("packaged artifact inspection failed")
        request = {"request_id": "smoke-expiry", "artifact": ref, "reason": "Disposable bundle smoke evidence"}
        receipt = catalog("expire-artifact", request)
        if receipt["artifact"] != ref or receipt["released_bytes"] != ref["size"] or not receipt["expired_at"]:
            raise ValueError("packaged artifact expiry failed")
        if catalog("expire-artifact", request) != receipt:
            raise ValueError("packaged expiry retry changed receipt")
        backup, restored = scratch / "backup", scratch / "restored"
        run([str(root / "bin/evolution-space-backup"), "-state-directory", str(state), "-output", str(backup)], root)
        run([str(root / "bin/evolution-space-restore"), "-input", str(backup), "-output", str(restored)], root)
        after = catalog("inspect-artifact", ref, restored)
        if after["artifact"] != ref or after["expired_at"] != receipt["expired_at"]:
            raise ValueError("packaged restore lost artifact expiry")
        if catalog("expire-artifact", request, restored) != receipt:
            raise ValueError("packaged restore lost expiry receipt")
        reseal = subprocess.run([str(root / "bin/evolution-space-catalog"), "--state-directory", str(restored),
                                 "--action", "seal-artifact", "--artifact-file", str(content)],
                                input=json.dumps(ref), text=True, capture_output=True)
        if reseal.returncode == 0 or "artifact_expired" not in reseal.stderr:
            raise ValueError("packaged restore resurrected expired content")
    print("Packaged catalog, artifact expiry, backup and restore smoke passed.")


def smoke(root):
    """Verify the packaged frontend serves prefixed assets and deep links."""
    root = root.resolve()
    verify(root)
    smoke_maintenance(root)
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    env = dict(os.environ, HOSTNAME="127.0.0.1", PORT=str(port), NEXT_TELEMETRY_DISABLED="1")
    process = subprocess.Popen(["node", "frontend/server.js"], cwd=root, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        origin = f"http://127.0.0.1:{port}"
        for _ in range(100):
            if process.poll() is not None:
                raise ValueError("packaged Evolution Space frontend exited")
            try:
                with urllib.request.urlopen(origin + "/evolution-space/", timeout=1) as response:
                    html = response.read().decode()
                break
            except (OSError, urllib.error.URLError):
                time.sleep(0.1)
        else:
            raise ValueError("packaged Evolution Space frontend did not become ready")
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
                if value.startswith("/evolution-space/_next/"):
                    assets.add(value)
        if not assets:
            raise ValueError("packaged Evolution Space page has no prefix-safe assets")
        for asset in assets:
            with urllib.request.urlopen(origin + asset, timeout=5) as response:
                if response.status != 200 or not response.read():
                    raise ValueError(f"packaged asset unavailable: {asset}")
        with urllib.request.urlopen(origin + "/evolution-space/groups/smoke/", timeout=5) as response:
            if response.status != 200:
                raise ValueError("packaged Evolution Space deep link unavailable")
    finally:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
    verify(root)
    print("Packaged Evolution Space root, assets and deep link smoke passed.")


def build(target, offline=False, source_ref="HEAD"):
    target = target.resolve()
    if target.exists() or target.is_relative_to(ROOT):
        raise ValueError("output must be a new directory outside the source checkout")
    commit = subprocess.check_output(["git", "rev-parse", "--verify", source_ref + "^{commit}"], cwd=ROOT, text=True).strip()
    with __import__("tempfile").TemporaryDirectory(prefix="evolution-space-release-") as temporary:
        scratch = Path(temporary)
        source = scratch / "source"
        source.mkdir()
        subprocess.run(["git", "archive", "--format=tar", commit, "evolution-space", "contracts", "web"], cwd=ROOT, stdout=open(scratch / "source.tar", "wb"), check=True)
        run(["tar", "-xf", str(scratch / "source.tar"), "-C", str(source)], ROOT)
        app = source / "evolution-space"
        env = dict(os.environ, NEXT_TELEMETRY_DISABLED="1")
        run(["npm", "ci"] + (["--offline"] if offline else []), app / "frontend", env)
        run(["npm", "run", "lint"], app / "frontend", env)
        run(["npm", "run", "typecheck"], app / "frontend", env)
        run(["npm", "run", "build"], app / "frontend", env)
        run(["go", "test", "-mod=readonly", "./..."], app / "backend", env)
        staged = scratch / "bundle"
        (staged / "bin").mkdir(parents=True)
        for command in COMMANDS:
            run(["go", "build", "-trimpath", "-mod=readonly", "-o", str(staged / "bin" / command), "./cmd/" + command], app / "backend", env)
        shutil.copytree(app / "frontend/.next/standalone", staged / "frontend", symlinks=True)
        shutil.copytree(app / "frontend/.next/static", staged / "frontend/evolution-space/frontend/.next/static")
        (staged / "frontend/server.js").write_text("require('./evolution-space/frontend/server.js');\n")
        shutil.copytree(app / "deploy", staged / "deploy") if (app / "deploy").exists() else (staged / "deploy").mkdir()
        document = {"format": "evolution-space-release-v1", "source_commit": commit, "base_path": "/evolution-space/", "go": subprocess.check_output(["go", "version"], text=True, env=env).strip(), "frontend_build_id": (app / "frontend/.next/BUILD_ID").read_text().strip(), "files": inventory(staged)}
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
