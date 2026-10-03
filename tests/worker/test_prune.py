"""Phone storage cleanup contracts (no Android required)."""
import hashlib
import importlib.util
import os
import pathlib
import tempfile
import time
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
module_spec = importlib.util.spec_from_file_location('tidal_worker_prune', ROOT / 'apps/android-worker/worker.py')
worker_module = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(worker_module)
DAY = 86400


class PruneTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='tidalbridge-prune-test-')
        self.addCleanup(self.temp.cleanup)
        self.worker = worker_module.Worker(pathlib.Path(self.temp.name) / 'worker', 'test-secret', 'low', 1)
        self.addCleanup(lambda: self.worker.pool.shutdown(wait=True))
        self.root = self.worker.root

    def copy(self, key: str, content: bytes) -> tuple[str, str]:
        """A synced project copy with installed dependencies."""
        digest = hashlib.sha256(content).hexdigest()
        (self.root / 'cache/blobs' / digest).write_bytes(content)
        manifest_id = 'm' + hashlib.sha256(key.encode()).hexdigest()[:20]
        self.worker.apply_manifest({'id': manifest_id, 'files': [{'path': 'a.txt', 'hash': digest, 'size': len(content), 'executable': False}], 'total_bytes': 0}, key)
        (self.worker.tree(key) / 'node_modules').mkdir()
        return digest, manifest_id

    def age(self, path: pathlib.Path, seconds: float) -> None:
        then = time.time() - seconds
        os.utime(path, (then, then))

    def test_unused_copies_and_content_go(self):
        (old, old_sync), (kept, _) = self.copy('old1', b'old'), self.copy('new1', b'new')
        stale = self.root / 'cache/blobs' / ('f' * 64)
        stale.write_bytes(b'stale')
        for path in (self.root / 'trees/old1.json', self.root / 'workspaces' / old_sync, stale, self.root / 'cache/blobs' / old):
            self.age(path, 20 * DAY)
        self.assertEqual(self.worker.prune(), ['old1'])
        self.assertFalse(self.worker.tree('old1').exists())
        self.assertTrue((self.worker.tree('new1') / 'node_modules').is_dir())
        self.assertFalse(stale.exists())
        self.assertFalse((self.root / 'cache/blobs' / old).exists())
        self.assertTrue((self.root / 'cache/blobs' / kept).exists())
        # The laptop's next sync rebuilds a removed copy from scratch.
        self.worker.apply_manifest({'id': old_sync, 'files': [{'path': 'a.txt', 'hash': kept, 'size': 3, 'executable': False}], 'total_bytes': 0}, 'old1')
        self.assertEqual((self.worker.tree('old1') / 'a.txt').read_bytes(), b'new')

    def test_running_jobs_keep_their_copy(self):
        self.copy('busy1', b'busy')
        self.age(self.root / 'trees/busy1.json', 60 * DAY)
        self.worker.jobs['j1'] = {'id': 'j1', 'state': 'RUNNING', 'spec': {'workspace_key': 'busy1'}}
        self.assertEqual(self.worker.prune(), [])
        self.assertTrue((self.worker.tree('busy1') / 'node_modules').is_dir())

    def test_low_storage_removes_the_least_recently_used_first(self):
        self.copy('first1', b'1')
        self.copy('second1', b'2')
        self.copy('fresh1', b'3')
        self.age(self.root / 'trees/first1.json', 3 * 3600)
        self.age(self.root / 'trees/second1.json', 2 * 3600)
        floors = iter([1 << 40])  # short of storage until one copy is gone
        self.worker.storage_floor_mb = lambda: next(floors, 0)
        self.assertEqual(self.worker.prune(), ['first1'])
        self.assertTrue(self.worker.tree('second1').exists() and self.worker.tree('fresh1').exists())


if __name__ == '__main__':
    unittest.main()
