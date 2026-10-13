#!/usr/bin/env python3
"""Build and verify an independent Studio bundle from committed source only."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import socket
import time
import re
import urllib.request
import urllib.error
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = 'studio-release.json'


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def inventory(root):
    root = root.resolve()
    result = {}
    for path in sorted(root.rglob('*')):
        name = path.relative_to(root).as_posix()
        if name == MANIFEST:
            continue
        if path.is_symlink():
            target = os.readlink(path)
            if os.path.isabs(target) or not path.resolve().is_relative_to(root) or not path.exists():
                raise ValueError(f'unsafe artifact symlink: {name}')
            result[name] = {'symlink': target}
        elif path.is_file():
            result[name] = {'sha256': digest(path), 'mode': stat.S_IMODE(path.stat().st_mode)}
        elif not path.is_dir():
            raise ValueError(f'unsupported artifact entry: {name}')
    return result


def run(args, cwd, env=None):
    subprocess.run(args, cwd=cwd, env=env, check=True)


def verify(root):
    root = root.resolve()
    doc = json.loads((root / MANIFEST).read_text())
    if doc['format'] != 'studio-release-v1' or doc['base_path'] != '/volumes-studio/':
        raise ValueError('unsupported Studio release')
    if inventory(root) != doc['files']:
        raise ValueError('Studio release contains changed, missing or extra files')
    for name in ['bin/studio', 'bin/front-door', 'bin/studio-backup', 'frontend/server.js']:
        if not (root / name).is_file():
            raise ValueError(f'release is missing {name}')
    print('Studio release inventory verified.')
    return doc


def smoke(root):
    verify(root)
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    env = dict(os.environ, HOSTNAME='127.0.0.1', PORT=str(port), NEXT_TELEMETRY_DISABLED='1')
    with tempfile.TemporaryDirectory(prefix='studio-smoke-') as temporary, tempfile.TemporaryFile() as log:
        runtime = Path(temporary) / 'bundle'
        shutil.copytree(root, runtime, symlinks=True)
        process = subprocess.Popen(['node', 'frontend/server.js'], cwd=runtime, env=env, stdout=log, stderr=log)
        try:
            origin = f'http://127.0.0.1:{port}'
            for _ in range(100):
                if process.poll() is not None:
                    raise ValueError('packaged frontend exited')
                try:
                    with urllib.request.urlopen(origin + '/volumes-studio/', timeout=1) as response:
                        html = response.read().decode()
                    break
                except (OSError, urllib.error.URLError):
                    time.sleep(.1)
            else:
                raise ValueError('packaged frontend did not become ready')
            assets = set(re.findall(r'(?:src|href)="(/volumes-studio/_next/[^" ]+)"', html))
            if not assets:
                raise ValueError('packaged page has no prefix-safe assets')
            # Verify lazy editor languages, local fonts and upload worker chunks too.
            for static_root in (runtime / 'frontend').glob('**/.next/static'):
                assets.update('/volumes-studio/_next/static/' + file.relative_to(static_root).as_posix()
                              for file in static_root.rglob('*') if file.is_file())
            for asset in assets:
                with urllib.request.urlopen(origin + asset, timeout=5) as response:
                    if response.status != 200 or not response.read():
                        raise ValueError(f'packaged asset unavailable: {asset}')
            with urllib.request.urlopen(origin + '/volumes-studio/libraries/smoke/?view=tree', timeout=5) as response:
                if response.status != 200:
                    raise ValueError('packaged deep link unavailable')
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
    verify(root)
    print('Packaged Studio root, assets and deep link smoke passed.')


def build(target, offline=False, source_ref="HEAD"):
    target = target.resolve()
    if target.exists() or target.is_relative_to(ROOT):
        raise ValueError('output must be a new directory outside the source checkout')
    commit = subprocess.check_output(['git', 'rev-parse', '--verify', source_ref + '^{commit}'], cwd=ROOT, text=True).strip()
    with tempfile.TemporaryDirectory(prefix='studio-release-') as temp:
        scratch = Path(temp)
        archive = scratch / 'source.tar'
        with archive.open('wb') as output:
            subprocess.run(['git', 'archive', '--format=tar', commit, 'volumes-studio', 'web/surfaces.css', 'web/appearance.css', 'web/appearance.ts', 'web/select.css', 'web/select-picker.tsx', 'web/picker-popover.ts'], cwd=ROOT, stdout=output, check=True)
        with tarfile.open(archive) as source:
            source.extractall(scratch / 'source', filter='data')
        app = scratch / 'source/volumes-studio'
        env = dict(os.environ, NEXT_TELEMETRY_DISABLED='1')
        install = ['npm', 'ci'] + (['--offline'] if offline else [])
        run(install, app / 'frontend', env)
        run(['npm', 'run', 'lint'], app / 'frontend', env)
        run(['npm', 'test'], app / 'frontend', env)
        run(['npm', 'run', 'build'], app / 'frontend', env)
        run(['npm', 'run', 'typecheck'], app / 'frontend', env)
        run(['go', 'test', '-mod=readonly', './...'], app / 'backend', env)
        staged = scratch / 'bundle'
        (staged / 'bin').mkdir(parents=True)
        for command in ['studio', 'front-door', 'studio-backup']:
            run(['go', 'build', '-trimpath', '-mod=readonly', '-o', str(staged / 'bin' / command), './cmd/' + command], app / 'backend', env)
        shutil.copytree(app / 'frontend/.next/standalone', staged / 'frontend', symlinks=True)
        shutil.copytree(app / 'frontend/.next/static', staged / 'frontend/volumes-studio/frontend/.next/static')
        (staged / 'frontend/server.js').write_text("require('./volumes-studio/frontend/server.js');\n")
        shutil.copytree(app / 'contracts', staged / 'contracts')
        shutil.copytree(app / 'deploy', staged / 'deploy')
        doc = {
            'format': 'studio-release-v1', 'source_commit': commit,
            'base_path': '/volumes-studio/', 'manifest_format': 'volume-manifest-v1',
            'node': subprocess.check_output(['node', '--version'], text=True).strip(),
            'go': subprocess.check_output(['go', 'version'], text=True, env=env).strip(),
            'dependency_locks': {name: digest(app / name) for name in ['frontend/package-lock.json', 'backend/go.sum']},
            'frontend_build_id': (app / 'frontend/.next/BUILD_ID').read_text().strip(),
            'files': inventory(staged),
        }
        (staged / MANIFEST).write_text(json.dumps(doc, indent=2) + '\n')
        verify(staged)
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copytree(staged, target, symlinks=True)
    smoke(target)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['build', 'verify', 'smoke'])
    parser.add_argument('directory', type=Path)
    parser.add_argument('--offline', action='store_true', help='use preinstalled npm cache without network')
    parser.add_argument('--source-ref', default='HEAD', help='committed source revision to build (default HEAD)')
    args = parser.parse_args()
    if args.action == 'build':
        build(args.directory, args.offline, args.source_ref)
    elif args.action == 'smoke':
        smoke(args.directory.resolve())
    else:
        verify(args.directory)


if __name__ == '__main__':
    main()
