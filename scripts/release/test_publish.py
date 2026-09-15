import importlib.util,io,json,os,pathlib,tempfile,unittest
from unittest.mock import patch
from artifact_report import TESTS,public_report
ROOT=pathlib.Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('publish',ROOT/'publish.py');publish=importlib.util.module_from_spec(spec);spec.loader.exec_module(publish)

class PublishTest(unittest.TestCase):
    def test_existing_public_release_is_refused_after_authenticated_lookup(self):
        # Covers the nested API helper before upload setup, including imports.
        with tempfile.TemporaryDirectory() as temp:
            directory=pathlib.Path(temp)
            m={'version':'v1.2.3','commit':'a'*40,'runsc_versions':['test-runtime']}
            encoded=json.dumps(m).encode();(directory/'release-manifest.json').write_bytes(encoded)
            import hashlib
            for ubuntu in ('22.04','24.04'):
                raw={'manifest_sha256':hashlib.sha256(encoded).hexdigest(),'version':m['version'],'commit':m['commit'],'ubuntu':ubuntu,'arch':'amd64','production_verified':False,'runsc_version':'test-runtime','tests':dict.fromkeys(TESTS,'passed')}
                (directory/f'artifact-smoke-{ubuntu}.json').write_text(json.dumps(public_report(encoded,raw,ubuntu)))
            with patch.dict(os.environ,{'GH_TOKEN':'synthetic-token'}),patch('sys.argv',['publish.py','--directory',temp,'--repository','example/repo']),patch('urllib.request.urlopen',return_value=io.BytesIO(b'{"draft": false}')) as request:
                with self.assertRaisesRegex(RuntimeError,'already public'):publish.main()
                self.assertEqual(request.call_count,1)
                req=request.call_args.args[0]
                self.assertEqual(req.full_url,'https://api.github.com/repos/example/repo/releases/tags/v1.2.3')
                self.assertEqual(req.get_header('Authorization'),'Bearer synthetic-token')
