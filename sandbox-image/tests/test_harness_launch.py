import importlib.util
import json
import hashlib
import os
from pathlib import Path
import shutil
import tempfile
import unittest

MODULE = Path(__file__).resolve().parents[1] / 'files/usr/local/lib/harness_launch.py'
spec = importlib.util.spec_from_file_location('harness_launch', MODULE)
launch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launch)


class KhoraLauncherTests(unittest.TestCase):
    def test_template_protocol_verifies_manifest_and_component_bytes(self):
        with tempfile.TemporaryDirectory() as root:
            files = {}
            for name in ('/usr/local/bin/harness-bridge-client', '/usr/local/bin/harness-khora-launcher',
                         '/usr/local/lib/harness_stream.py', '/usr/local/lib/harness_launch.py'):
                path = Path(root) / name.lstrip('/')
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b'component')
                files[name] = 'sha256:' + hashlib.sha256(b'component').hexdigest()
            metadata = {'schema_version': 1, 'runtime': {'backend': 'khora', 'engine_revision': 'e8cd021a9c7c',
                                    'engine_module': 'v0.0.2-0.20260920024301-e8cd021a9c7c',
                                    'bridge_stream_version': 1, 'home': '/root', 'retained_overlay': True, 'files': files},
                        'drivers': [{'driver_id': 'sh', 'bridge_protocol_version': 2, 'turn_input_schema': 'RunTurn',
                                     'binary_path': '/usr/local/bin/harness-khora-launcher',
                                     'installed_binary_digest': 'sha256:' + hashlib.sha256(b'component').hexdigest()}]}
            manifest = Path(root) / 'manifest.json'
            manifest.write_text(json.dumps(metadata))
            protocol = launch.template_protocol('sh', manifest, Path(root))
            canonical = json.dumps(metadata, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()
            self.assertEqual(protocol['manifest_digest'], 'sha256:' + hashlib.sha256(canonical).hexdigest())
            (Path(root) / 'usr/local/lib/harness_stream.py').write_bytes(b'changed')
            with self.assertRaises(ValueError):
                launch.template_protocol('sh', manifest, Path(root))
    def fixture(self, root):
        home, source, assets = Path(root) / 'root', Path(root) / 'config', Path(root) / 'assets'
        source.mkdir()
        assets.mkdir()
        (assets / 'AGENTS.md').write_text('shared synthetic context')
        (source / 'models.json').write_text(json.dumps({'providers': {'harness_anthropic_proxy': {
            'baseUrl': 'http://model.internal:443', 'apiKey': 'harness-placeholder', 'models': [{'id': 'synthetic-model'}]}}}))
        (source / 'settings.json').write_text('{}')
        return home, source, assets

    def test_config_locks_remain_writable_and_history_survives_relaunch(self):
        with tempfile.TemporaryDirectory() as root:
            home, source, assets = self.fixture(root)
            agent = launch.prepare_pi(home, source, assets)
            self.assertTrue((agent / 'AGENTS.md').is_symlink())
            self.assertEqual((agent / 'AGENTS.md').read_text(), 'shared synthetic context')
            self.assertEqual((agent / 'models.json').stat().st_mode & 0o777, 0o444)
            (agent / 'settings.json.lock').mkdir()
            history = agent / 'sessions/native.jsonl'
            history.write_text('native history')
            launch.prepare_pi(home, source, assets)
            self.assertEqual(history.read_text(), 'native history')
            self.assertEqual((agent / 'auth.json').read_text(), '{}\n')

    def test_session_context_wins_over_shared_assets(self):
        with tempfile.TemporaryDirectory() as root:
            home, source, assets = self.fixture(root)
            agent = home / '.pi/agent'
            agent.mkdir(parents=True)
            (agent / 'AGENTS.md').write_text('session override')
            launch.prepare_pi(home, source, assets)
            self.assertEqual((agent / 'AGENTS.md').read_text(), 'session override')
            self.assertFalse((agent / 'AGENTS.md').is_symlink())

    def test_rejects_secret_config_and_symlinked_agent_directory(self):
        with tempfile.TemporaryDirectory() as root:
            home, source, assets = self.fixture(root)
            config = json.loads((source / 'models.json').read_text())
            config['providers']['harness_anthropic_proxy']['apiKey'] = 'not-a-placeholder'
            (source / 'models.json').write_text(json.dumps(config))
            with self.assertRaises(ValueError):
                launch.prepare_pi(home, source, assets)
            (home / '.pi/agent/sessions').rmdir()
            (home / '.pi/agent').rmdir()
            (home / '.pi').rmdir()
            (home / '.pi').symlink_to(assets)
            with self.assertRaises(ValueError):
                launch.prepare_pi(home, source, assets)

    def test_environment_uses_real_guest_home_without_uid_projection(self):
        environment = launch.launch_environment('pi')
        self.assertEqual(environment['HOME'], '/root')
        self.assertEqual(environment['USER'], 'root')
        self.assertEqual(environment['PI_CODING_AGENT_DIR'], '/root/.pi/agent')
        self.assertNotIn('HARNESS_AGENT_UID', environment)
        self.assertNotIn('HARNESS_AGENT_GID', environment)


if __name__ == '__main__':
    unittest.main()


class ServiceHostProjectionTests(unittest.TestCase):
    def _hosts(self, body='127.0.0.1\tlocalhost\n'):
        directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, directory)
        path = Path(directory) / 'hosts'
        path.write_text(body)
        return path

    def test_projects_entitled_service_names(self):
        path = self._hosts()
        resolved = launch.project_service_hosts(
            ['Model.Internal=10.80.0.1', 'data.internal=10.80.0.2'], path=path)
        self.assertEqual(resolved, {'model.internal': '10.80.0.1', 'data.internal': '10.80.0.2'})
        text = path.read_text()
        self.assertIn('127.0.0.1\tlocalhost', text)
        self.assertIn('10.80.0.1\tmodel.internal', text)
        self.assertIn('10.80.0.2\tdata.internal', text)
        # The block is rewritten whole, so a superseded generation's names cannot
        # survive into the next launch.
        launch.project_service_hosts(['other.internal=10.80.0.3'], path=path)
        text = path.read_text()
        self.assertNotIn('model.internal', text)
        self.assertIn('10.80.0.3\tother.internal', text)
        self.assertIn('127.0.0.1\tlocalhost', text)

    def test_no_entitlement_leaves_only_image_hosts(self):
        path = self._hosts()
        launch.project_service_hosts(['model.internal=10.80.0.1'], path=path)
        self.assertEqual(launch.project_service_hosts([], path=path), {})
        self.assertEqual(path.read_text(), '127.0.0.1\tlocalhost\n')

    def test_refuses_unusable_declarations(self):
        path = self._hosts()
        for entry in ('model.internal', '=10.80.0.1', 'model.internal=',
                      'model.internal=10.80.0.1 extra', 'model.internal=not-an-ip',
                      'model.internal=10.80.0.1:443', 'model.internal=10.80.0.256',
                      'model.internal=010.80.0.1', 'bad_name=10.80.0.1',
                      '-lead.internal=10.80.0.1'):
            with self.assertRaises(ValueError, msg=entry):
                launch.project_service_hosts([entry], path=path)
        with self.assertRaises(ValueError):
            launch.project_service_hosts(
                ['model.internal=10.80.0.1', 'model.internal=10.80.0.2'], path=path)

    def test_refuses_symlinked_host_file(self):
        directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, directory)
        target = Path(directory) / 'real'
        target.write_text('127.0.0.1\tlocalhost\n')
        link = Path(directory) / 'hosts'
        link.symlink_to(target)
        with self.assertRaises(ValueError):
            launch.project_service_hosts(['model.internal=10.80.0.1'], path=link)


class ResolverProjectionTest(unittest.TestCase):
    def test_generation_replaces_and_clears_resolver_configuration(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'resolv.conf'
            launch.project_resolvers(['192.0.2.53', '192.0.2.54', '192.0.2.53'], path)
            self.assertEqual(path.read_text(), 'nameserver 192.0.2.53\nnameserver 192.0.2.54\n')
            self.assertEqual(path.stat().st_mode & 0o777, 0o444)
            launch.project_resolvers([], path)
            self.assertEqual(path.read_text(), '')
            with self.assertRaises(ValueError):
                launch.project_resolvers(['192.0.2.53\nsearch forbidden'], path)
