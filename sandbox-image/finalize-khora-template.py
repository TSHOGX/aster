#!/usr/bin/env python3
"""Finalize a newly built application rootfs as a retained Khora template."""
import hashlib
import json
import pathlib
import shutil
import sys

ENGINE_MODULE = 'v0.0.2-0.20260920024301-e8cd021a9c7c'
ENGINE_REVISION = 'e8cd021a9c7c'


def finalize(root, engine_module):
    root = root.resolve()
    if engine_module != ENGINE_MODULE or root == pathlib.Path('/'):
        raise ValueError('unsupported engine module or template root')
    for relative in ('root/.pi', 'root/.agents'):
        target = root / relative
        if target.is_symlink() or target.is_file():
            target.unlink()
        elif target.exists():
            shutil.rmtree(target)
    for relative in ('workspace', 'harness-bridge', 'harness-config', 'root'):
        (root / relative).mkdir(parents=True, exist_ok=True)
    files = {}
    for relative in ('usr/sbin/sandboxd', 'usr/local/bin/harness-khora-launcher',
                     'usr/local/bin/harness-bridge-client',
                     'usr/local/lib/harness_stream.py', 'usr/local/lib/harness_launch.py',
                     'usr/local/lib/harness_files.py', 'usr/local/bin/harness-workspace-helper',
                     'usr/local/lib/harness_recovery.py', 'usr/local/bin/harness-journal-replay'):
        path = root / relative
        if path.is_symlink() or not path.is_file():
            raise ValueError(f'missing regular template component: {relative}')
        files['/' + relative] = 'sha256:' + hashlib.sha256(path.read_bytes()).hexdigest()
    manifest_path = root / 'etc/harness-image/agents.json'
    manifest = json.loads(manifest_path.read_text())
    found = set()
    for driver in manifest['drivers']:
        found.add(driver['driver_id'])
        driver['capabilities'] = ['process_stream', 'filesystem', 'durable_workspace', 'cold_restart', 'signal']
        if driver['driver_id'] == 'pi':
            driver['capabilities'].append('native_history_restore')
            driver['installed_config_paths'] = ['/harness-config/pi/models.json', '/harness-config/pi/settings.json',
                                                '/root/.pi/agent/models.json', '/root/.pi/agent/settings.json']
            driver['writable_state_paths'] = ['/root/.pi/agent', '/root/.pi/agent/sessions']
        elif driver['driver_id'] == 'sh':
            driver['capabilities'].append('pty')
        else:
            raise ValueError('unsupported template driver')
    if found != {'pi', 'sh'}:
        raise ValueError('Khora template requires Pi and shell drivers')
    manifest['runtime'] = {'backend': 'khora', 'engine_module': engine_module, 'engine_revision': ENGINE_REVISION,
                           'bridge_stream_version': 1, 'home': '/root', 'retained_overlay': True,
                           'launcher': '/usr/local/bin/harness-khora-launcher', 'files': files}
    raw = json.dumps(manifest, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()
    manifest_path.write_bytes(raw + b'\n')
    (manifest_path.parent / 'agents.sha256').write_text(hashlib.sha256(raw).hexdigest() + '\n')
    return manifest


if __name__ == '__main__':
    finalize(pathlib.Path(sys.argv[1]), sys.argv[2])
