"""Command-adapter integration checks. The HTTP peer is deliberately simulated;
these checks establish semantics, not physical laptop resource relief.
"""
import base64
import http.server
import json
import os
import pathlib
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
TOKEN = 'adapter-test-token-' * 3

class Peer(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        self.server.requests.append((self.path, body))
        if self.headers.get('Authorization') != 'Bearer ' + TOKEN:
            self.send_error(401)
            return
        if self.path == '/v1/explain':
            self.respond({'target': self.server.route, 'explanation': 'Simulated routing test'})
        elif self.path == '/v1/jobs':
            self.server.accepted += 1
            if self.server.drop_submission:
                self.connection.shutdown(socket.SHUT_RDWR)
                self.connection.close()
            else:
                self.respond({'id': 'test-job'})
        elif self.path == '/v1/observations/local':
            self.respond({'recorded': True})
        elif self.path == '/v1/observations/adapter':
            self.respond({'recorded': True})
        else:
            self.send_error(404)

    def do_GET(self):
        if self.server.drop_poll:
            self.connection.shutdown(socket.SHUT_RDWR)
            self.connection.close()
            return
        if '/output?' in self.path:
            from urllib.parse import parse_qs, urlsplit
            query = parse_qs(urlsplit(self.path).query)
            data = b'REMOTE\n' if query['stream'][0] == 'stdout' else b'worker stderr\n'
            offset = int(query['offset'][0])
            self.respond({'data_b64': base64.b64encode(data[offset:]).decode(), 'next_offset': len(data)})
        else:
            self.respond({'id': 'test-job', 'finished': '2026-10-01T00:00:00Z', 'exit_code': 7,
                          'attempts': [{'target': 'simulated-peer'}], 'decision': {'target': 'simulated-peer', 'explanation': 'Simulated routing test'}})

    def respond(self, body):
        encoded = json.dumps(body).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

class AdapterTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='tidalbridge-adapter-')
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.data = self.root / 'data'
        self.data.mkdir()
        self.project = self.root / 'project'
        (self.project / '.tidalbridge').mkdir(parents=True)
        self.shims = self.root / 'shims'
        self.shims.mkdir()
        suffix = '.exe' if os.name == 'nt' else ''
        shutil.copy2(ROOT / ('bin/tidalbridge-task' + suffix), self.shims / ('python' + suffix))
        self.binary = self.shims / ('python' + suffix)
        self.code = "import pathlib,sys;pathlib.Path('local-ran').write_text('yes');print('LOCAL');sys.exit(7)"
        self.task_path = self.project / '.tidalbridge/tasks.json'
        self.task_path.write_text(json.dumps({'version': 1, 'enabled': True, 'tasks': [{'name': 'semantic-test', 'command': ['python', '-c', self.code]}]}))
        (self.shims / 'shims.json').write_text(json.dumps({'version': 1, 'data_dir': str(self.data), 'local_commands': {'python': [sys.executable]}}))
        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Peer)
        self.server.requests = []
        self.server.route = 'LOCAL'
        self.server.accepted = 0
        self.server.drop_submission = False
        self.server.drop_poll = False
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.stop_server)
        (self.data / 'host.token').write_text(TOKEN)
        (self.data / 'config.yaml').write_text('bind: 127.0.0.1:' + str(self.server.server_port) + '\n')

    def stop_server(self):
        self.server.shutdown()
        self.server.server_close()

    def run_adapter(self, argv=None, env=None):
        environment = {k: v for k, v in os.environ.items() if not k.startswith('TIDALBRIDGE_')}
        environment.update(env or {})
        return subprocess.run([str(self.binary), *(argv or ['-c', self.code])], cwd=self.project, env=environment, capture_output=True, text=True, timeout=20)

    def test_local_preserves_output_exit_and_records_real_observation(self):
        result = self.run_adapter()
        self.assertEqual(result.returncode, 7)
        self.assertEqual(result.stdout, 'LOCAL\n')
        self.assertEqual(result.stderr, '')
        self.assertTrue((self.project / 'local-ran').exists())
        observation = [body for path, body in self.server.requests if path == '/v1/observations/local'][0]
        self.assertEqual(observation['exit_code'], 7)
        self.assertGreater(observation['duration_ms'], 0)
        self.assertEqual(observation['decision']['target'], 'LOCAL')
        self.assertEqual(observation['decision']['explanation'], 'Simulated routing test')

    def test_remote_preserves_streams_exit_and_never_launches_local_duplicate(self):
        self.server.route = 'simulated-peer'
        result = self.run_adapter()
        self.assertEqual(result.returncode, 7)
        self.assertEqual(result.stdout, 'REMOTE\n')
        self.assertIn('worker stderr', result.stderr)
        self.assertFalse((self.project / 'local-ran').exists())
        self.assertEqual(self.server.accepted, 1)
        metrics = [body for path, body in self.server.requests if path == '/v1/observations/adapter'][0]
        self.assertEqual(metrics['job_id'], 'test-job')
        self.assertGreater(metrics['metrics']['peak_ram_mb'], 0)

    def test_accepted_job_poll_error_never_replays_locally(self):
        self.server.route = 'simulated-peer'
        self.server.drop_poll = True
        result = self.run_adapter()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('inspect this ID', result.stderr)
        self.assertFalse((self.project / 'local-ran').exists())
        self.assertEqual(self.server.accepted, 1)

    def test_ambiguous_submission_does_not_replay_locally(self):
        self.server.route = 'simulated-peer'
        self.server.drop_submission = True
        result = self.run_adapter()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('do not blindly replay', result.stderr)
        self.assertFalse((self.project / 'local-ran').exists())
        self.assertEqual(self.server.accepted, 1)

    def test_disabled_project_and_bypass_never_contact_peer(self):
        self.task_path.write_text(json.dumps({'version': 1, 'enabled': False, 'tasks': []}))
        result = self.run_adapter()
        self.assertEqual(result.stdout, 'LOCAL\n')
        self.assertEqual(self.server.requests, [])
        self.server.route = 'simulated-peer'
        result = self.run_adapter(env={'TIDALBRIDGE_DISABLE': '1'})
        self.assertEqual(result.stdout, 'LOCAL\n')
        self.assertEqual(self.server.requests, [])

    def test_unknown_command_and_recursion_guard_stay_local(self):
        result = self.run_adapter(['-c', "print('unapproved')"])
        self.assertEqual(result.stdout, 'unapproved\n')
        self.assertEqual(self.server.requests, [])
        self.server.route = 'simulated-peer'
        result = self.run_adapter(env={'TIDALBRIDGE_INTERNAL': '1'})
        self.assertEqual(result.stdout, 'LOCAL\n')
        self.assertEqual(self.server.requests, [])

    def test_daemon_absent_runs_original_once(self):
        with socket.socket() as unused:
            unused.bind(('127.0.0.1', 0))
            port = unused.getsockname()[1]
        (self.data / 'config.yaml').write_text('bind: 127.0.0.1:' + str(port) + '\n')
        result = self.run_adapter()
        self.assertEqual(result.returncode, 7)
        self.assertEqual(result.stdout, 'LOCAL\n')

if __name__ == '__main__':
    unittest.main(verbosity=2)
