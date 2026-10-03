"""Time limits on the phone: a slow job that keeps printing is not a hung one (no Android required)."""
import importlib.util
import pathlib
import subprocess
import sys
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
module_spec = importlib.util.spec_from_file_location('tidal_worker_overtime', ROOT / 'apps/android-worker/worker.py')
worker_module = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(worker_module)


class OvertimeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='tidalbridge-overtime-test-')
        self.addCleanup(self.temp.cleanup)
        self.worker = worker_module.Worker(pathlib.Path(self.temp.name) / 'worker', 'test-secret', 'low', 1)
        self.addCleanup(lambda: self.worker.pool.shutdown(wait=True))
        self.log = pathlib.Path(self.temp.name) / 'stdout.log'
        silent, worker_module.SILENT_LIMIT = worker_module.SILENT_LIMIT, 0.6
        self.addCleanup(setattr, worker_module, 'SILENT_LIMIT', silent)

    def run_python(self, code: str, timeout: float) -> int:
        with self.log.open('wb') as out:
            proc = subprocess.Popen([sys.executable, '-u', '-c', code], stdout=out)
            return self.worker.wait_for(proc, timeout, [self.log])

    def test_a_job_that_keeps_printing_runs_past_its_limit(self):
        code = 'import time\nfor i in range(6):\n    print(i, flush=True); time.sleep(0.12)'
        self.assertEqual(self.run_python(code, 0.5), 0)  # about 1 s: past the limit, under three times it

    def test_a_silent_job_stops_after_its_limit(self):
        with self.assertRaises(TimeoutError):
            self.run_python('import time; time.sleep(30)', 0.3)

    def test_overtime_has_a_ceiling(self):
        code = 'import time\nwhile True:\n    print(1, flush=True); time.sleep(0.1)'
        with self.assertRaisesRegex(TimeoutError, 'three times'):
            self.run_python(code, 0.2)


if __name__ == '__main__':
    unittest.main()
