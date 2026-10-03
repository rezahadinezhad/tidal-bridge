import importlib.util
import json
import pathlib
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
module_spec = importlib.util.spec_from_file_location('tidal_worker_native', ROOT / 'apps/android-worker/worker.py')
worker_module = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(worker_module)


class NativeCommandTest(unittest.TestCase):
    """Only direct Node programs run without proot; anything needing a shell
    or another program keeps the proot path."""

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        root = pathlib.Path(self.temporary.name)
        self.native = worker_module.NativeNode(root)
        self.tree = root / 'state/trees/ws-1'
        bins = self.tree / 'node_modules/.bin'
        bins.mkdir(parents=True)
        (bins / 'tsc').write_text('#!/usr/bin/env node\nrequire("../typescript/lib/tsc.js")\n')
        (bins / 'esbuild').write_bytes(b'\x7fELF\x02\x01\x01')
        scripts = {'typecheck': 'tsc --noEmit', 'chain': 'tsc && eslint .', 'binary': 'esbuild src', 'assign': 'A=1 tsc',
                   'quoted': "tsc -p 'tsconfig.app.json'", 'expand': 'tsc -p $CONFIG'}
        (self.tree / 'package.json').write_text(json.dumps({'scripts': scripts}))

    def tearDown(self):
        self.temporary.cleanup()

    def command(self, *argv, env=None):
        return self.native.command(list(argv), self.tree, {} if env is None else env)

    def test_package_scripts_of_node_programs_run_directly(self):
        node, tsc = str(self.native.node), str((self.tree / 'node_modules/.bin/tsc').resolve())
        env = {}
        self.assertEqual(self.command('npm', 'run', 'typecheck', env=env), [node, tsc, '--noEmit'])
        self.assertEqual(env['npm_lifecycle_event'], 'typecheck')
        self.assertEqual(self.command('npm', 'run', 'typecheck', '--', '--pretty'), [node, tsc, '--noEmit', '--pretty'])
        self.assertEqual(self.command('npm', 'run', 'quoted')[2:], ['-p', 'tsconfig.app.json'])
        self.assertEqual(self.command('tsc', '--noEmit'), [node, tsc, '--noEmit'])
        self.assertEqual(self.command('node', 'server.js'), [node, 'server.js'])

    def test_shell_features_and_other_programs_keep_proot(self):
        for argv in (('npm', 'run', 'chain'), ('npm', 'run', 'binary'), ('npm', 'run', 'assign'), ('npm', 'run', 'expand'),
                     ('npm', 'run', 'missing'), ('npm', 'ci'), ('npm', 'run', 'typecheck', '--silent'), ('eslint', '.')):
            self.assertIsNone(self.command(*argv), argv)


if __name__ == '__main__':
    unittest.main()
