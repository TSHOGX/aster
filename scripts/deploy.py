#!/usr/bin/env python3
"""Validate and activate a fresh Aster installation on a prepared local host."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import secrets
import shutil
import subprocess
import tempfile
import time

import candidate

STATE_ROOTS = (Path("/var/lib/aster"), Path("/var/lib/aster-studio"), Path("/srv/aster/engine"))

SERVICES = ("web", "gateway", "application", "studio", "frontend", "admin-frontend",
            "studio-frontend", "studio-tls", "engine-tls", "engine")


def run(args, **kwargs):
    return subprocess.run([str(a) for a in args], check=True, **kwargs)


def validate(build, plan):
    build = build.resolve()
    report = json.loads((build / "deployment-build.json").read_text())
    if report["format"] != "aster-deployment-v1":
        raise ValueError("unsupported complete build")
    app = candidate.verify(build / "application")
    run(["python3", Path(__file__).resolve().parents[1] / "volumes-studio/scripts/release.py",
         "verify", build / "studio"])
    studio = json.loads((build / "studio/studio-release.json").read_text())
    if app["commit"] != studio["source_commit"] or app["commit"] != report["source_commit"]:
        raise ValueError("mixed source revisions")
    for name, key in (("bin/khora", "engine_sha256"), ("runtime.manifest.json", "runtime_manifest_sha256"),
                      ("bin/runtime-verify", "runtime_verifier_sha256")):
        if hashlib.sha256((build / name).read_bytes()).hexdigest() != report[key]:
            raise ValueError("build bytes changed: " + name)
    if report.get("public_brand") != "Aster" or app.get("public_brand") != "Aster":
        raise ValueError("complete deployment must use the Aster brand")
    digest = report["runtime_digest"]
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
        raise ValueError("invalid runtime digest")
    if (build / "runtime.manifest.json.sha256").read_text().strip() != digest:
        raise ValueError("runtime digest sidecar changed")
    if (build / "runtime/etc/project-runtime/manifest.json").read_bytes() != (build / "runtime.manifest.json").read_bytes():
        raise ValueError("guest and exported runtime manifests differ")
    run([build / "bin/runtime-verify", "--root", build / "runtime", digest])
    work_manifest = build / "runtime-work.manifest.json"
    if hashlib.sha256(work_manifest.read_bytes()).hexdigest() != report["work_runtime_manifest_sha256"]:
        raise ValueError("work runtime manifest changed")
    work_digest = report["work_runtime_digest"]
    if (build / "runtime-work.manifest.json.sha256").read_text().strip() != work_digest:
        raise ValueError("work runtime digest sidecar changed")
    if (build / "runtime-work/etc/project-runtime/manifest.json").read_bytes() != work_manifest.read_bytes():
        raise ValueError("guest and exported work runtime manifests differ")
    run([build / "bin/runtime-verify", "--root", build / "runtime-work", work_digest])
    engine = subprocess.check_output([str(build / "bin/khora"), "--version"], text=True).strip()
    if engine != "orchestrator " + report["engine_revision"]:
        raise ValueError("engine server identity mismatch")
    config_path = Path(plan["config"])
    if config_path != Path("/etc/aster/application.json"):
        raise ValueError("fresh local configuration must be /etc/aster/application.json")
    config = json.loads(config_path.read_text())
    if config["state_directory"] != "/var/lib/aster":
        raise ValueError("unexpected application state directory")
    sources = config.get("volume_imports", {}).get("sources", [])
    if not sources or any(s["publisher_id"] != "aster-local-studio" or s["client_id"] != "aster" for s in sources):
        raise ValueError("fresh local Studio must use aster-local-studio and the aster distribution client")
    if config["template"]["manifest"] != str(build / "runtime.manifest.json"):
        raise ValueError("configuration must select the newly built runtime manifest")
    if config["template"]["digest"] != digest:
        raise ValueError("runtime contract digest mismatch")
    if not re.fullmatch(r"[a-zA-Z0-9_-]+", plan["namespace"]):
        raise ValueError("invalid network namespace")
    if config.get("work_template", {}).get("manifest") != str(work_manifest) or config["work_template"].get("digest") != work_digest:
        raise ValueError("configuration must select the newly built Work release")
    templates = Path(plan["unit_templates"]).resolve()
    replacements = {"@APPLICATION@": str(build / "application"), "@STUDIO@": str(build / "studio"),
                    "@ENGINE@": str(build / "bin/khora"), "@RUNTIME@": str(build / "runtime"), "@WORK_RUNTIME@": str(build / "runtime-work"),
                    "@CONFIG@": str(config_path)}
    units = {}
    for service in SERVICES:
        name = f"aster-{service}.service"
        text = (templates / name).read_text()
        for old, new in replacements.items():
            text = text.replace(old, new)
        if re.search(r"@[A-Z_]+@", text):
            raise ValueError("unresolved supervisor placeholder: " + name)
        units[name] = text
    if not Path("/srv/aster/engine").is_mount():
        raise ValueError("prepare the fresh engine btrfs mount before activation")
    for user in ("asterapp", "asterweb"):
        pwd.getpwnam(user)
    for root in STATE_ROOTS:
        if build.is_relative_to(root) or templates.is_relative_to(root):
            raise ValueError("build and unit templates must be outside reset state")
        validate_state_root(root)
    environment = activation_environment()
    run([build / "application/bin/orchestrator", "release-identity", "--config", config_path],
        env=environment, stdout=subprocess.DEVNULL)
    with tempfile.TemporaryDirectory(prefix="aster-unit-check-") as temporary:
        paths = []
        for name, text in units.items():
            path = Path(temporary) / name
            path.write_text(text)
            paths.append(path)
        run(["systemd-analyze", "verify", *paths])
    return config, units


def validate_state_root(root):
    if root.is_symlink() or not root.is_dir() or root.resolve() != root:
        raise ValueError("state must be a regular prepared directory: " + str(root))
    # A nested mount may belong to another service. Validate the complete reset
    # scope before deleting any root, and never traverse a mounted child.
    for line in Path("/proc/self/mountinfo").read_text().splitlines():
        mount = Path(re.sub(r"\\([0-7]{3})", lambda match: chr(int(match[1], 8)), line.split()[4]))
        if mount != root and mount.is_relative_to(root):
            raise ValueError("nested mount inside reset state: " + str(mount))


def activation_environment():
    env = dict(os.environ, SSL_CERT_FILE="/etc/aster/tls/upstream-ca.pem")
    for name in ("owner.env", "gateway.env", "studio.env"):
        for line in Path("/etc/aster", name).read_text().splitlines():
            if line and not line.startswith("#") and "=" in line:
                key, value = line.split("=", 1)
                env[key] = value.strip().strip('"').strip("'")
    return env


def clear_state(root):
    validate_state_root(root)
    if root == Path("/srv/aster/engine"):
        listing = subprocess.check_output(["btrfs", "subvolume", "list", "-o", str(root)], text=True)
        paths = [line.split(" path ", 1)[1] for line in listing.splitlines()]
        for relative in paths:
            path = root / relative
            if path.is_symlink() or path.resolve() == root or not path.resolve().is_relative_to(root) or ".." in Path(relative).parts or relative.split("/")[0] not in ("state", "volumes", "tmp"):
                raise ValueError("unexpected engine subvolume: " + relative)
        for relative in sorted(paths, key=lambda value: len(Path(value).parts), reverse=True):
            path = root / relative
            run(["btrfs", "subvolume", "delete", path], stdout=subprocess.DEVNULL)
    for path in root.iterdir():
        if path.is_dir() and not path.is_symlink():
            shutil.rmtree(path)
        else:
            path.unlink()


def activate(build, plan, config, units):
    if os.geteuid() != 0:
        raise ValueError("activation requires root")
    # Fail on missing secrets/configuration before interrupting any service.
    env = activation_environment()
    for root in STATE_ROOTS:
        validate_state_root(root)
    loaded = [name for name in units if subprocess.check_output(
        ["systemctl", "show", name, "-p", "LoadState", "--value"], text=True).strip() != "not-found"]
    if loaded:
        run(["systemctl", "stop", *loaded])
    for name in units:
        state = subprocess.check_output(["systemctl", "show", name, "-p", "ActiveState", "--value"], text=True).strip()
        if state not in ("inactive", "failed"):
            raise ValueError("service still owns state: " + name)
    for root in STATE_ROOTS:
        validate_state_root(root)
    for root in STATE_ROOTS:
        clear_state(root)
    for name, text in units.items():
        Path("/etc/systemd/system", name).write_text(text)
    run(["systemctl", "daemon-reload"])
    run(["systemctl", "start", "aster-engine.service", "aster-engine-tls.service"])
    for _ in range(100):
        result = subprocess.run(["ip", "netns", "exec", plan["namespace"], "curl", "-fsS",
                                 "--max-time", "2", "http://127.0.0.1:18191/v1/info"], capture_output=True, text=True)
        if result.returncode == 0:
            config["engine"]["store_id"] = json.loads(result.stdout)["store_id"]
            break
        time.sleep(.2)
    else:
        raise ValueError("fresh engine did not become ready; public ingress remains stopped")
    Path(plan["config"]).write_text(json.dumps(config, indent=2) + "\n")
    credentials = {"url": config["front_door"]["public_origin"], "username": "administrator",
                   "password": secrets.token_urlsafe(24)}
    path = Path("/etc/aster/administrator.json")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w") as stream:
        os.fchmod(stream.fileno(), 0o600)
        stream.write(json.dumps(credentials, indent=2) + "\n")
    executable = build / "application/bin/orchestrator"
    run(["runuser", "-u", "asterapp", "--", executable, "account", "bootstrap", "--config",
         plan["config"], "--username", credentials["username"], "--password-stdin"],
        input=credentials["password"] + "\n", text=True, env=env)
    run(["systemctl", "start", *[f"aster-{name}.service" for name in
         ("frontend", "admin-frontend", "studio-frontend", "studio-tls", "application", "studio")]])
    for _ in range(100):
        if Path("/var/lib/aster/control.sock").exists():
            break
        time.sleep(.2)
    else:
        raise ValueError("application did not become ready; public ingress remains stopped")
    run(["runuser", "-u", "asterapp", "--", executable, "publish", "--socket",
         "/var/lib/aster/control.sock", "--config", plan["config"]], env=env)
    run(["systemctl", "start", "aster-gateway.service"])
    run([executable, "diagnostics", "--socket", "/var/lib/aster/control.sock"])
    for name in units:
        if name != "aster-web.service":
            run(["systemctl", "is-active", "--quiet", name])
    run(["systemctl", "enable", *units])
    run(["systemctl", "start", "aster-web.service"])
    run(["systemctl", "is-active", "--quiet", "aster-web.service"])
    print("Fresh installation activated. Administrator credentials: /etc/aster/administrator.json")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("build", type=Path)
    parser.add_argument("--plan", type=Path, required=True)
    parser.add_argument("--fresh", action="store_true", help="activate and erase all application, Studio and engine state")
    args = parser.parse_args()
    build = args.build.resolve()
    plan = json.loads(args.plan.read_text())
    config, units = validate(build, plan)
    if args.fresh:
        activate(build, plan, config, units)
    else:
        print("Build, configuration and supervisor plan verified; no services or data changed.")


if __name__ == "__main__":
    main()
