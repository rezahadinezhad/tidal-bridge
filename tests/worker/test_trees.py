"""Persistent workspace tree contracts (no network, no Android required)."""
import hashlib
import importlib.util
import pathlib
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
module_spec = importlib.util.spec_from_file_location('tidal_worker_trees', ROOT / 'apps/android-worker/worker.py')
worker_module = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(worker_module)


class TreeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='tidalbridge-tree-test-')
        self.addCleanup(self.temp.cleanup)
        self.worker = worker_module.Worker(pathlib.Path(self.temp.name) / 'worker', 'test-secret', 'low', 1)
        self.addCleanup(lambda: self.worker.pool.shutdown(wait=True))

    def blob(self, content: bytes) -> str:
        digest = hashlib.sha256(content).hexdigest()
        (self.worker.root / 'cache/blobs' / digest).write_bytes(content)
        return digest

    def manifest(self, files: dict) -> dict:
        entries = [{'path': p, 'hash': self.blob(c), 'size': len(c), 'executable': False} for p, c in files.items()]
        return {'id': 'm' + hashlib.sha256(repr(sorted(files.items())).encode()).hexdigest()[:20], 'files': entries, 'total_bytes': 0}

    def test_nested_copy_holds_files_from_one_folder_up(self):
        with self.assertRaises(ValueError):
            self.worker.apply_manifest(self.manifest({'../w.json': b'w'}), 'flat1')
        nested = self.manifest({'src/a.ts': b'a', '../planning/w.json': b'w'})
        self.assertEqual(self.worker.missing(nested), [])
        self.worker.apply_manifest(nested, 'wo-p1', 'frontend')
        tree = self.worker.tree('wo-p1')
        self.assertEqual(tree, self.worker.root / 'trees/wo-p1/frontend')
        self.assertEqual((tree / 'src/a.ts').read_bytes(), b'a')
        self.assertEqual((tree.parent / 'planning/w.json').read_bytes(), b'w')
        self.assertEqual(self.worker.tree_changes('wo-p1'), {})
        self.assertTrue(self.worker.remove_tree('wo-p1'))
        self.assertFalse((self.worker.root / 'trees/wo-p1').exists())
        self.assertEqual(self.worker.tree('wo-p1'), self.worker.root / 'trees/wo-p1')

    def test_incremental_tree_updates(self):
        key = 'project1'
        first = self.worker.apply_manifest(self.manifest({'a.txt': b'one', 'src/b.txt': b'two'}), key)
        self.assertEqual(first['written'], 2)
        tree = self.worker.tree(key)
        # A job leaves dependency and cache output behind.
        (tree / 'node_modules').mkdir()
        (tree / 'node_modules' / 'dep.js').write_text('x')
        second = self.worker.apply_manifest(self.manifest({'a.txt': b'one', 'src/b.txt': b'changed'}), key)
        self.assertEqual((second['written'], second['removed']), (1, 0))
        self.assertEqual((tree / 'src/b.txt').read_bytes(), b'changed')
        # A job modified a synced file: the next sync restores the host version.
        (tree / 'a.txt').write_bytes(b'edited on worker!')
        third = self.worker.apply_manifest(self.manifest({'a.txt': b'one', 'src/b.txt': b'changed'}), key)
        self.assertEqual(third['written'], 1)
        self.assertEqual((tree / 'a.txt').read_bytes(), b'one')
        # Removal deletes synced files and empty directories, never job output.
        fourth = self.worker.apply_manifest(self.manifest({'a.txt': b'one'}), key)
        self.assertEqual(fourth['removed'], 1)
        self.assertFalse((tree / 'src').exists())
        self.assertTrue((tree / 'node_modules' / 'dep.js').exists())

    def test_empty_workspace_manifest_is_accepted(self):
        # Older hosts encode an empty file list as null.
        result = self.worker.apply_manifest({'id': 'empty1', 'files': None, 'total_bytes': 0}, 'project4')
        self.assertEqual((result['files'], result['written']), (0, 0))

    def test_tree_sync_never_writes_through_links(self):
        key = 'project5'
        self.worker.apply_manifest(self.manifest({'src/a.txt': b'one'}), key)
        tree = self.worker.tree(key)
        outside = pathlib.Path(self.temp.name) / 'outside'
        outside.mkdir()
        try:
            # A job can leave links behind in its tree.
            (tree / 'linked').symlink_to(outside, target_is_directory=True)
            (tree / 'src' / 'b.txt.tidalbridge-tmp').symlink_to(outside / 'planted')
        except (OSError, NotImplementedError):
            self.skipTest('symlinks unavailable')
        with self.assertRaises(ValueError):
            self.worker.apply_manifest(self.manifest({'src/a.txt': b'one', 'linked/x.txt': b'x'}), key)
        self.assertFalse((outside / 'x.txt').exists())
        self.worker.apply_manifest(self.manifest({'src/a.txt': b'one', 'src/b.txt': b'two'}), key)
        self.assertEqual((tree / 'src' / 'b.txt').read_bytes(), b'two')
        self.assertFalse((outside / 'planted').exists())

    def test_tree_reports_missing_content(self):
        manifest = self.manifest({'a.txt': b'one'})
        (self.worker.root / 'cache/blobs' / manifest['files'][0]['hash']).unlink()
        with self.assertRaisesRegex(ValueError, 'missing content'):
            self.worker.apply_manifest(manifest, 'project6')

    def test_tree_rejects_unsafe_keys_and_paths(self):
        with self.assertRaises(ValueError):
            self.worker.tree('../escape')
        bad = {'id': 'bad1', 'files': [{'path': '../x', 'hash': 'a' * 64, 'size': 1}], 'total_bytes': 0}
        with self.assertRaises(ValueError):
            self.worker.apply_manifest(bad, 'project2')

    def test_dependency_status_tracks_lockfile_contents(self):
        key = 'project3'
        self.worker.apply_manifest(self.manifest({'package.json': b'{}', 'package-lock.json': b'{"v":1}'}), key)
        status = self.worker.environment_status({'workspace_key': key, 'working_directory': '', 'runtime': 'termux'})
        self.assertEqual(status, {'ready': False, 'kind': 'node'})
        calls = []
        self.worker.run_process = lambda jid, argv, cwd, env, remaining, **kw: calls.append(argv) or 0
        (self.worker.root / 'jobs' / 'j1').mkdir(parents=True)
        tree = self.worker.tree(key)
        self.worker.prepare_tree('j1', tree, {}, None, 'termux', False)
        self.assertEqual(calls[0][:2], ['npm', 'ci'])
        self.assertIn('--ignore-scripts', calls[0])
        self.assertTrue(self.worker.environment_status({'workspace_key': key, 'working_directory': '', 'runtime': 'termux'})['ready'])
        self.worker.prepare_tree('j1', tree, {}, None, 'termux', False)
        self.assertEqual(len(calls), 1, 'warm dependencies must not reinstall')
        self.worker.apply_manifest(self.manifest({'package.json': b'{}', 'package-lock.json': b'{"v":2}'}), key)
        self.assertFalse(self.worker.environment_status({'workspace_key': key, 'working_directory': '', 'runtime': 'termux'})['ready'])


    def test_write_back_changes(self):
        """A write-back job reports what it changed in a synced tree, outside
        dependency directories, and caches the new content."""
        key = 'ws-' + 'c' * 24
        self.worker.apply_manifest(self.manifest({'a.txt': b'one', 'src/b.txt': b'two', 'gone.txt': b'x'}), key)
        tree = self.worker.tree(key)
        (tree / 'a.txt').write_bytes(b'one fixed')
        (tree / 'gone.txt').unlink()
        (tree / 'src/new.txt').write_bytes(b'generated')
        (tree / 'node_modules/pkg').mkdir(parents=True)
        (tree / 'node_modules/pkg/index.js').write_bytes(b'ignored')
        changes = self.worker.tree_changes(key)
        self.assertEqual([item['path'] for item in changes['modified']], ['a.txt'])
        self.assertEqual([item['path'] for item in changes['created']], ['src/new.txt'])
        self.assertEqual(changes['deleted'], ['gone.txt'])
        digest = changes['modified'][0]['hash']
        self.assertEqual((self.worker.root / 'cache/blobs' / digest).read_bytes(), b'one fixed')
        self.assertEqual(self.worker.tree_changes('ws-' + 'd' * 24), {})

if __name__ == '__main__':
    unittest.main(verbosity=2)
