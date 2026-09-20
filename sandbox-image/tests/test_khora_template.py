import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

MODULE = Path(__file__).resolve().parents[1] / 'finalize-khora-template.py'
spec = importlib.util.spec_from_file_location('khora_template', MODULE)
template = importlib.util.module_from_spec(spec)
spec.loader.exec_module(template)


class KhoraTemplateTests(unittest.TestCase):
    def test_manifest_pins_engine_and_guest_components_without_driver_home_mount(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for component in ('usr/sbin/sandboxd', 'usr/local/bin/harness-khora-launcher', 'usr/local/bin/harness-bridge-client',
                              'usr/local/bin/harness-shell-agent', 'usr/local/lib/harness_stream.py', 'usr/local/lib/harness_launch.py',
                              'usr/local/lib/harness_files.py', 'usr/local/bin/harness-workspace-helper',
                              'usr/local/lib/harness_recovery.py', 'usr/local/bin/harness-journal-replay'):
                path = root / component
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text('synthetic component')
            manifest = root / 'etc/harness-image/agents.json'
            manifest.parent.mkdir(parents=True)
            manifest.write_text(json.dumps({'schema_version': 1, 'drivers': [{'driver_id': 'pi'}, {'driver_id': 'sh'}]}))
            (root / 'root/.pi').mkdir(parents=True)
            (root / 'root/.pi/native.jsonl').write_text('build-only residue')
            result = template.finalize(root, template.ENGINE_MODULE)
            self.assertEqual(result['runtime']['home'], '/root')
            self.assertEqual(result['runtime']['engine_revision'], template.ENGINE_REVISION)
            self.assertIn('native_history_restore', result['drivers'][0]['capabilities'])
            self.assertFalse((root / 'root/.pi').exists())
            self.assertFalse((root / 'agent-home').exists())
            self.assertFalse((root / 'usr/local/bin/harness-agent-entrypoint').exists())
            self.assertEqual(len((manifest.parent / 'agents.sha256').read_text().strip()), 64)

    def test_wrong_engine_revision_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(ValueError):
                template.finalize(Path(directory), 'unverified-engine')


if __name__ == '__main__':
    unittest.main()
