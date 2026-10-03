import importlib.util
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
module_spec = importlib.util.spec_from_file_location('tidal_worker_memory', ROOT / 'apps/android-worker/worker.py')
worker_module = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(worker_module)


class PhysicalMemoryTest(unittest.TestCase):
    def test_sold_size_from_what_android_sees(self):
        # Usable MB as Android reports it on phones of each size.
        for usable, sold in ((3700, 4), (5600, 6), (7400, 8), (9300, 10), (11044, 12), (15000, 16), (16800, 18), (22800, 24)):
            self.assertEqual(worker_module.physical_memory_mb(usable), sold * 1024, usable)


if __name__ == '__main__':
    unittest.main()
