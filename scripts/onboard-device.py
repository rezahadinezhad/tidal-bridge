"""One-command Termux setup through an ephemeral, token-protected ADB reverse tunnel."""
import argparse
import hashlib
import json
import os
import pathlib
import re
import secrets
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = pathlib.Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--adb", required=True)
parser.add_argument("--serial", required=True)
parser.add_argument("--port", type=int, default=47833)
parser.add_argument("--data-dir", type=pathlib.Path, default=pathlib.Path.home() / ".tidalbridge")
args = parser.parse_args()
args.data_dir.mkdir(parents=True, exist_ok=True)
if not re.fullmatch(r"[a-zA-Z0-9_-]+", args.serial):
    raise SystemExit("Unsupported serial format")
token_file = args.data_dir / ("pair-" + args.serial + ".token")
if not token_file.exists():
    token_file.write_text(secrets.token_hex(24))
token = token_file.read_text().strip()
code_file = args.data_dir / "onboarding.code"
if not code_file.exists():
    code_file.write_text(secrets.token_hex(16))
code = code_file.read_text().strip()
base = f"http://127.0.0.1:{args.port}/{code}"
command = f"curl -fsS {base}/setup.sh | bash"
(args.data_dir / "ONBOARDING.txt").write_text(command + "\n")
finished = threading.Event()
worker = (ROOT / "apps/android-worker/worker.py").read_bytes().replace(b"\r\n", b"\n")
installer = (ROOT / "scripts/setup-android.sh").read_bytes().replace(b"\r\n", b"\n")
worker_hash = hashlib.sha256(worker).hexdigest()
installer_hash = hashlib.sha256(installer).hexdigest()
script = f'''#!/data/data/com.termux/files/usr/bin/bash
set -eu
mkdir -p "$HOME/tidalbridge/worker"
chmod 700 "$HOME/tidalbridge"
curl -fsS '{base}/worker.py' -o "$HOME/tidalbridge/worker/worker.py"
curl -fsS '{base}/install.sh' -o "$HOME/tidalbridge/setup-android.sh"
echo '{worker_hash}  '"$HOME/tidalbridge/worker/worker.py" | sha256sum -c -
echo '{installer_hash}  '"$HOME/tidalbridge/setup-android.sh" | sha256sum -c -
umask 077
printf '%s' '{token}' > "$HOME/tidalbridge/worker.token"
bash "$HOME/tidalbridge/setup-android.sh"
curl -fsS -X POST '{base}/ready' -d 'ready' || true
echo 'Worker setup finished. The Windows host will connect automatically.'
'''.encode()

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass
    def do_GET(self):
        name = self.path.removeprefix('/' + code + '/')
        payload = {"setup.sh":script,"worker.py":worker,"install.sh":installer}.get(name) if self.path.startswith('/' + code + '/') else None
        if payload is None:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header('Content-Length',str(len(payload)))
        self.send_header('Content-Type','text/plain')
        self.end_headers()
        self.wfile.write(payload)
    def do_POST(self):
        if self.path != '/' + code + '/ready':
            self.send_error(404)
            return
        self.send_response(200)
        self.end_headers()
        finished.set()

subprocess.run([str(ROOT / "bin/tidalbridge.exe"), "--data-dir", str(args.data_dir), "approve", "--serial", args.serial, "--token-file", str(token_file)],check=True)
subprocess.run([args.adb,"-s",args.serial,"reverse","tcp:" + str(args.port),"tcp:" + str(args.port)],check=True)
server = ThreadingHTTPServer(('127.0.0.1',args.port),Handler)
server.timeout = 1
print("Run this once in Termux:",flush=True)
print(command,flush=True)
try:
    deadline = time.monotonic() + 3600
    while not finished.is_set() and time.monotonic() < deadline:
        server.handle_request()
    print("Onboarding complete" if finished.is_set() else "Onboarding expired; rerun the script",flush=True)
finally:
    server.server_close()
    subprocess.run([args.adb,"-s",args.serial,"reverse","--remove","tcp:" + str(args.port)],capture_output=True)
    code_file.unlink(missing_ok=True)
    (args.data_dir / "ONBOARDING.txt").unlink(missing_ok=True)
