import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest

MODULE = Path(__file__).resolve().parents[1] / 'files/usr/local/lib/harness_launch.py'
spec = importlib.util.spec_from_file_location('harness_launch', MODULE)
launch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launch)


class KhoraLauncherTests(unittest.TestCase):
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
