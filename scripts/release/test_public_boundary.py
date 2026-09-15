import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from artifact_report import public_report, TESTS

ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('build_apps', ROOT / 'build-apps.py')
build_apps = importlib.util.module_from_spec(spec)
spec.loader.exec_module(build_apps)

class PublicBoundaryTest(unittest.TestCase):
    def test_packaging_uses_explicit_public_allowlist(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp)/'source';output=Path(temp)/'package'
            for src in build_apps.PUBLIC_DELIVERY_FILES:
                p=root/src;p.parent.mkdir(parents=True,exist_ok=True);p.write_text('synthetic example')
            for relative in ('deploy/local/profile.json','deploy/private/inventory.md','docs/delivery/instances.md','docs/delivery/operation.local.json'):
                p=root/relative;p.parent.mkdir(parents=True,exist_ok=True);p.write_text('PRIVATE TEST DATA')
            build_apps.copy_public_delivery_files(root,output)
            files={str(p.relative_to(output)) for p in output.rglob('*') if p.is_file()}
            self.assertEqual(files,set(build_apps.PUBLIC_DELIVERY_FILES.values()))
            self.assertTrue(all('PRIVATE TEST DATA' not in p.read_text() for p in output.rglob('*') if p.is_file()))

    def test_public_report_drops_all_host_metadata(self):
        manifest=json.dumps({'version':'v1.2.3','commit':'a'*40,'runsc_versions':['test-runsc']}).encode()
        import hashlib
        raw={'version':'v1.2.3','commit':'a'*40,'manifest_sha256':hashlib.sha256(manifest).hexdigest(),'ubuntu':'24.04','arch':'amd64','runsc_version':'test-runsc','production_verified':False,'tests':dict.fromkeys(TESTS,'passed'),'hostname':'private-host.invalid','machine_id':'private-test-id','logs':['private test log'],'paths':['/private/test/path']}
        raw['tests']['private-runtime-detail']='private value'
        approved=public_report(manifest,raw,'24.04')
        self.assertEqual(set(approved),{'version','commit','manifest_sha256','ubuntu','arch','runsc_version','production_verified','tests'})
        self.assertEqual(set(approved['tests']),set(TESTS))
        self.assertNotIn('private',json.dumps(approved))
        raw['production_verified']=True
        with self.assertRaises(ValueError):public_report(manifest,raw,'24.04')
        raw['production_verified']=False
        raw['tests']['baseline_669542d']='failed'
        with self.assertRaises(ValueError):public_report(manifest,raw,'24.04')

    def test_public_input_symlink_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp)/'source';src=next(iter(build_apps.PUBLIC_DELIVERY_FILES));p=root/src;p.parent.mkdir(parents=True);outside=Path(temp)/'private';outside.write_text('private');p.symlink_to(outside)
            with self.assertRaises(RuntimeError):build_apps.copy_public_delivery_files(root,Path(temp)/'output')
