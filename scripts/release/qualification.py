"""Only generic technical evidence may leave the qualification host."""
import hashlib
import json

TESTS = (
    'offline_install_no_build_tools', 'two_instances', 'baseline_669542d',
    'baseline_237f8b3', 'backup_restore', 'real_pi_two_turns',
    'stop_park_steer', 'cold_start_checkpoint', 'scheduler_drain',
    'external_dependencies', 'failure_injection',
)

def public_report(manifest_bytes, raw, ubuntu):
    manifest = json.loads(manifest_bytes)
    expected = {
        'manifest_sha256': hashlib.sha256(manifest_bytes).hexdigest(),
        'version': manifest['version'], 'commit': manifest['commit'],
        'ubuntu': ubuntu, 'arch': 'amd64',
    }
    if ubuntu not in ('22.04', '24.04'):
        raise ValueError('unsupported qualification platform')
    if any(raw.get(k) != v for k, v in expected.items()):
        raise ValueError('qualification does not match release')
    runsc = raw.get('runsc_version')
    if runsc not in manifest['runsc_versions']:
        raise ValueError('runsc was not qualified for this release')
    tests = raw.get('tests', {})
    if any(tests.get(k) != 'passed' for k in TESTS):
        raise ValueError('required qualification tests have not passed')
    # Never copy arbitrary metadata, free text, unknown test names or logs.
    return {**expected, 'runsc_version': runsc, 'tests': {k: 'passed' for k in TESTS}}
