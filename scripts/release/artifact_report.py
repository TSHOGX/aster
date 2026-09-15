"""Public evidence describes hosted artifact tests, never production acceptance."""
import hashlib,json
TESTS=('archive_integrity','cli_version','node_runtime','frontend_standalone',
       'new_database','baseline_669542d','baseline_237f8b3','sandbox_startup')

def public_report(manifest_bytes,raw,ubuntu):
    m=json.loads(manifest_bytes)
    expected={'manifest_sha256':hashlib.sha256(manifest_bytes).hexdigest(),
              'version':m['version'],'commit':m['commit'],'ubuntu':ubuntu,
              'arch':'amd64','production_verified':False}
    if ubuntu not in ('22.04','24.04') or any(raw.get(k)!=v for k,v in expected.items()):
        raise ValueError('artifact test report does not match release/platform/scope')
    if raw.get('runsc_version') not in m['runsc_versions']:
        raise ValueError('artifact test runtime mismatch')
    if any(raw.get('tests',{}).get(k)!='passed' for k in TESTS):
        raise ValueError('required artifact tests have not passed')
    return {**expected,'runsc_version':raw['runsc_version'],'tests':dict.fromkeys(TESTS,'passed')}
