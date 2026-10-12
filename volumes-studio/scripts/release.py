#!/usr/bin/env python3
"""Build and verify an independent Studio bundle from committed source only."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
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


def build(target, offline=False):
    target = target.resolve()
    if target.exists() or target.is_relative_to(ROOT):
        raise ValueError('output must be a new directory outside the source checkout')
    commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    with tempfile.TemporaryDirectory(prefix='studio-release-') as temp:
        scratch = Path(temp)
        archive = scratch / 'source.tar'
        with archive.open('wb') as output:
            subprocess.run(['git', 'archive', '--format=tar', commit, 'volumes-studio'], cwd=ROOT, stdout=output, check=True)
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
        shutil.copytree(app / 'frontend/.next/static', staged / 'frontend/.next/static')
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
    verify(target)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['build', 'verify'])
    parser.add_argument('directory', type=Path)
    parser.add_argument('--offline', action='store_true', help='use preinstalled npm cache without network')
    args = parser.parse_args()
    if args.action == 'build':
        build(args.directory, args.offline)
    else:
        verify(args.directory)


if __name__ == '__main__':
    main()
