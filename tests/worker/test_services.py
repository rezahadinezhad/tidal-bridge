"""Noticing laptop-service tunnels in use, so the host never reuses such a result (no Android required)."""
import importlib.util
import pathlib
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
module_spec = importlib.util.spec_from_file_location('tidal_worker_services', ROOT / 'apps/android-worker/worker.py')
worker_module = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(worker_module)

HEADER = '  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n'
LISTENING_8000 = '   0: 0100007F:1F40 00000000:0000 0A 00000000:00000000 00:00000000 00000000  2000        0 1 1 0 100 0 0 10 0\n'
OTHER = '   1: 0100007F:9C40 0100007F:8DFB 01 00000000:00000000 00:00000000 00000000  2000        0 2 1 0 100 0 0 10 0\n'
CLIENT_TO_8000 = '   2: 0100007F:9C41 0100007F:1F40 01 00000000:00000000 00:00000000 00000000  2000        0 3 1 0 100 0 0 10 0\n'
CLOSED_9000 = '   3: 0100007F:2328 0100007F:9C42 06 00000000:00000000 00:00000000 00000000     0        0 4 1 0 100 0 0 10 0\n'


class TunnelTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='tidalbridge-services-test-')
        self.addCleanup(self.temp.cleanup)

    def table(self, *rows: str) -> str:
        path = pathlib.Path(self.temp.name) / f'tcp{len(list(pathlib.Path(self.temp.name).iterdir()))}'
        path.write_text(HEADER + ''.join(rows))
        return str(path)

    def test_listeners_and_other_ports_do_not_count(self):
        self.assertFalse(worker_module.tunnel_in_use({8000}, (self.table(LISTENING_8000, OTHER),)))

    def test_a_connection_to_a_tunnelled_port_counts_from_either_end(self):
        self.assertTrue(worker_module.tunnel_in_use({8000}, (self.table(LISTENING_8000, CLIENT_TO_8000),)))
        self.assertTrue(worker_module.tunnel_in_use({9000}, (self.table(CLOSED_9000),)), 'a closed connection lingers in TIME_WAIT')

    def test_an_unreadable_table_means_unknown(self):
        self.assertIsNone(worker_module.tunnel_in_use({8000}, (str(pathlib.Path(self.temp.name) / 'missing'),)))

    def test_once_seen_a_job_stays_marked(self):
        samples = iter([False, True, False])
        original = worker_module.tunnel_in_use
        worker_module.tunnel_in_use = lambda ports: next(samples)
        try:
            with worker_module.ServiceWatch({8000}, every=60) as watch:
                watch.sample()
            self.assertIs(watch.used, True)
        finally:
            worker_module.tunnel_in_use = original

    def test_no_tunnels_means_nothing_to_watch(self):
        with worker_module.ServiceWatch(set()) as watch:
            pass
        self.assertIs(watch.used, False)


if __name__ == '__main__':
    unittest.main()
