"""Blob packs: many files in one request, optionally compressed, each checked against its hash (no Android required)."""
import hashlib
import importlib.util
import pathlib
import tempfile
import unittest
import zlib

ROOT = pathlib.Path(__file__).resolve().parents[2]
module_spec = importlib.util.spec_from_file_location('tidal_worker_pack', ROOT / 'apps/android-worker/worker.py')
worker_module = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(worker_module)


def record(content: bytes, digest: str | None = None) -> bytes:
    return (digest or hashlib.sha256(content).hexdigest()).encode() + f'{len(content):016x}'.encode() + content


class PackTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='tidalbridge-pack-test-')
        self.addCleanup(self.temp.cleanup)
        self.worker = worker_module.Worker(pathlib.Path(self.temp.name) / 'worker', 'test-secret', 'low', 1)
        self.addCleanup(lambda: self.worker.pool.shutdown(wait=True))
        self.blobs = self.worker.root / 'cache/blobs'

    def test_a_compressed_pack_stores_every_blob(self):
        contents = [f'export const v{i} = {i};\n'.encode() * 50 for i in range(40)] + [b'']
        body = b''.join(record(c) for c in contents)
        result = self.worker.store_pack(zlib.compress(body, 1), 'zlib')
        self.assertEqual(result['stored'], 41)
        for content in contents:
            self.assertEqual((self.blobs / hashlib.sha256(content).hexdigest()).read_bytes(), content)
        self.assertEqual(self.worker.store_pack(record(b'plain'), 'identity')['stored'], 1)

    def test_a_blob_that_does_not_match_its_hash_fails_the_pack(self):
        bad = record(b'changed', hashlib.sha256(b'original').hexdigest())
        with self.assertRaisesRegex(ValueError, 'invalid blob'):
            self.worker.store_pack(record(b'fine') + bad, 'identity')
        self.assertFalse((self.blobs / hashlib.sha256(b'original').hexdigest()).exists())

    def test_damaged_or_oversized_packs_are_refused(self):
        with self.assertRaisesRegex(ValueError, 'invalid pack record'):
            self.worker.store_pack(record(b'content')[:-3], 'identity')
        with self.assertRaisesRegex(ValueError, 'invalid pack'):
            self.worker.store_pack(zlib.compress(record(b'x'))[:-4], 'zlib')
        with self.assertRaisesRegex(ValueError, 'larger than allowed'):
            self.worker.store_pack(zlib.compress(bytes(worker_module.PACK_LIMIT + 1)), 'zlib')
        with self.assertRaisesRegex(ValueError, 'encoding'):
            self.worker.store_pack(b'', 'brotli')


if __name__ == '__main__':
    unittest.main()
