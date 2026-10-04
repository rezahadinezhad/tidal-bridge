"""Warm tools on the phone: incremental type-checks and Node's code cache (no Android required)."""
import importlib.util
import json
import os
import pathlib
import tempfile
import time
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
module_spec = importlib.util.spec_from_file_location('tidal_worker_warm', ROOT / 'apps/android-worker/worker.py')
worker_module = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(worker_module)
tsc_incremental = worker_module.tsc_incremental


class WarmTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='tidalbridge-warm-test-')
        self.addCleanup(self.temp.cleanup)
        base = pathlib.Path(self.temp.name)
        self.project, self.warm = base / 'project', base / 'warm'
        self.script = self.typescript('5.6.3')

    def typescript(self, version: str) -> str:
        package = self.project / 'node_modules/typescript'
        (package / 'bin').mkdir(parents=True, exist_ok=True)
        (package / 'package.json').write_text(json.dumps({'version': version}))
        (package / 'bin/tsc').write_text('#!/usr/bin/env node\n')
        return str(package / 'bin/tsc')

    def test_a_type_check_keeps_incremental_state_outside_the_copy(self):
        args = tsc_incremental(['--noEmit', '-p', 'tsconfig.app.json'], self.script, self.project, self.warm)
        self.assertEqual(args[:5], ['--noEmit', '-p', 'tsconfig.app.json', '--incremental', '--tsBuildInfoFile'])
        self.assertTrue(args[5].startswith(str(self.warm / 'tsc')))
        other = tsc_incremental(['--noEmit', '-p', 'tsconfig.node.json'], self.script, self.project, self.warm)
        self.assertNotEqual(args[5], other[5], 'each configuration keeps its own state')
        self.assertEqual(args, tsc_incremental(['--noEmit', '-p', 'tsconfig.app.json'], self.script, self.project, self.warm))

    def test_builds_watch_mode_and_chosen_state_are_left_alone(self):
        for args in (['-b'], ['--build', '--noEmit'], ['--noEmit', '--watch'], ['--noEmit', '--incremental'],
                     ['--noEmit', '--tsBuildInfoFile=x'], ['-p', 'tsconfig.json'], ['--noemit', '-i']):
            self.assertEqual(tsc_incremental(list(args), self.script, self.project, self.warm), args)
        self.assertEqual(tsc_incremental(['--noemit'], self.script, self.project, self.warm)[1], '--incremental', 'tsc flags ignore case')

    def test_old_typescript_runs_as_before(self):
        self.assertEqual(tsc_incremental(['--noEmit'], self.typescript('3.9.10'), self.project, self.warm), ['--noEmit'])

    def test_node_keeps_compiled_code_between_runs(self):
        native = worker_module.NativeNode(pathlib.Path(self.temp.name) / 'userland')
        self.assertNotIn('NODE_COMPILE_CACHE', native.env({}))
        native.warm = self.warm
        self.assertEqual(native.env({})['NODE_COMPILE_CACHE'], str(self.warm / 'node'))
        self.assertEqual(native.env({'NODE_COMPILE_CACHE': '/own'})['NODE_COMPILE_CACHE'], '/own')

    def test_unused_state_is_pruned(self):
        worker = worker_module.Worker(pathlib.Path(self.temp.name) / 'worker', 'test-secret', 'low', 1)
        self.addCleanup(lambda: worker.pool.shutdown(wait=True))
        state = worker.root / 'warm/tsc/old.tsbuildinfo'
        state.parent.mkdir(parents=True)
        state.write_text('{}')
        fresh = state.with_name('fresh.tsbuildinfo')
        fresh.write_text('{}')
        old = time.time() - worker_module.IDLE_SECONDS - 60
        os.utime(state, (old, old))
        worker.prune_warm(time.time())
        self.assertFalse(state.exists())
        self.assertTrue(fresh.exists())


if __name__ == '__main__':
    unittest.main()
