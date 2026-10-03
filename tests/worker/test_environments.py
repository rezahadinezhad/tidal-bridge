"""Real worker environment-cache contracts, without external package downloads."""
import hashlib
import importlib.util
import os
import pathlib
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
module_spec = importlib.util.spec_from_file_location('tidal_worker', ROOT / 'apps/android-worker/worker.py')
worker_module = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(worker_module)

class EnvironmentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='tidalbridge-worker-test-')
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.worker = worker_module.Worker(self.root / 'worker', 'test-secret', 'low', 1)
        self.addCleanup(lambda: self.worker.pool.shutdown(wait=True))

    def test_environment_cache_uses_dependencies_not_source_contents(self):
        hashes = {'package.json': hashlib.sha256(b'package').hexdigest(), 'package-lock.json': hashlib.sha256(b'lock').hexdigest()}
        key = self.worker.environment_key(hashes)
        self.assertFalse(self.worker.environment_status({'dependency_hashes': hashes})['ready'])
        directory = self.worker.root / 'environments' / key
        directory.mkdir(parents=True)
        (directory / 'ready').touch()
        self.assertTrue(self.worker.environment_status({'dependency_hashes': hashes})['ready'])
        changed = dict(hashes, **{'package-lock.json': hashlib.sha256(b'new lock').hexdigest()})
        self.assertNotEqual(self.worker.environment_key(changed), key)
        with self.assertRaises(ValueError):
            self.worker.environment_key({'../../private': 'a' * 64})

    def test_pinned_pyproject_dependencies_use_same_cached_environment(self):
        workspace = self.root / 'project'
        workspace.mkdir()
        (workspace / 'pyproject.toml').write_text('[project]\nname="fixture"\nversion="1"\ndependencies=["packaging==25.0"]\n')
        calls = []

        def fake_install(jid, argv, cwd, env, remaining):
            calls.append(argv)
            if argv[1:3] == ['-m', 'venv']:
                (pathlib.Path(argv[3]) / ('Scripts' if os.name == 'nt' else 'bin')).mkdir(parents=True)
            return 0

        self.worker.run_process = fake_install
        self.worker.provision('fixture', workspace, {}, 30)
        self.assertEqual(len(calls), 2)
        self.assertIn('--only-binary=:all:', calls[1])
        self.worker.provision('fixture', workspace, {}, 30)
        self.assertEqual(len(calls), 2)

    def test_unpinned_pyproject_is_rejected_before_installing(self):
        workspace = self.root / 'project'
        workspace.mkdir()
        (workspace / 'pyproject.toml').write_text('[project]\ndependencies=["packaging>=25"]\n')
        with self.assertRaisesRegex(ValueError, 'pinned'):
            self.worker.provision('fixture', workspace, {}, 30)

if __name__ == '__main__':
    unittest.main(verbosity=2)
