import json
import os
import pathlib
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]


class PrivateDependencyCredentialTest(unittest.TestCase):
    def test_helper_scopes_access_and_is_removed_after_download(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = pathlib.Path(directory)
            report = directory / 'report.json'
            fake_go = directory / 'go'
            fake_go.write_text('''#!/usr/bin/env python3
import json, os, subprocess
helper = os.environ['GIT_CONFIG_VALUE_0'][1:]
def query(path):
    return subprocess.check_output([helper, 'get'], input=('protocol=https\\nhost=github.com\\npath=' + path + '\\n\\n').encode()).decode()
results = {'helper': helper, 'allowed': query('TSHOGX/khora'), 'unrelated': query('unrelated/repository'), 'private': os.environ['GOPRIVATE']}
with open(os.environ['DEPENDENCY_TEST_REPORT'], 'w') as output:
    json.dump(results, output)
''')
            fake_go.chmod(0o700)
            environment = dict(os.environ, PATH=str(directory) + os.pathsep + os.environ['PATH'],
                               KHORA_READ_TOKEN='synthetic-read-token', DEPENDENCY_TEST_REPORT=str(report))
            result = subprocess.run(['bash', str(ROOT / 'scripts/fetch-go-dependencies.sh')],
                                    env=environment, capture_output=True, text=True, check=True)
            data = json.loads(report.read_text())
            self.assertEqual(data['allowed'], 'username=x-access-token\npassword=synthetic-read-token\n')
            self.assertEqual(data['unrelated'], '')
            self.assertEqual(data['private'], 'github.com/TSHOGX/khora')
            self.assertFalse(pathlib.Path(data['helper']).exists())
            self.assertNotIn('synthetic-read-token', result.stdout + result.stderr)

    def test_missing_credential_fails_before_download(self):
        environment = dict(os.environ)
        environment.pop('KHORA_READ_TOKEN', None)
        result = subprocess.run(['bash', str(ROOT / 'scripts/fetch-go-dependencies.sh')],
                                env=environment, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('read-only access', result.stderr)


if __name__ == '__main__':
    unittest.main()
