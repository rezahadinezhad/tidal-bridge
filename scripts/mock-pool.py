"""Run a persistent dev-only mock pool. CTRL+C stops all children."""
import json
import pathlib
import secrets
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
DATA = ROOT / ".tidalbridge/mock-pool"
DATA.mkdir(parents=True, exist_ok=True)
processes = []
token_path = DATA / "host.token"
if not token_path.exists():
    token_path.write_text(secrets.token_hex(24))
configuration = "bind: 127.0.0.1:47841\nmode: AUTO\napproved:\n"
try:
    for i, level in enumerate(("low", "mid", "high")):
        root = DATA / level
        root.mkdir(exist_ok=True)
        token_file = root / "worker.token"
        if not token_file.exists():
            token_file.write_text(secrets.token_hex(24))
        token = token_file.read_text().strip()
        port = 47842 + i
        processes.append(subprocess.Popen([sys.executable, str(ROOT / "apps/android-worker/worker.py"), "--root", str(root), "--port", str(port), "--mock", level]))
        configuration += f"  - serial: mock-{level}\n    token: {token}\n    port: {port}\n    endpoint: http://127.0.0.1:{port}\n    mock: true\n"
    (DATA / "config.yaml").write_text(configuration)
    host = subprocess.Popen([str(ROOT / "bin/tidalbridge.exe"), "--data-dir", str(DATA), "serve"])
    processes.append(host)
    print("MOCK POOL: all jobs execute on this host. No physical resource relief.", flush=True)
    print("Dashboard: http://127.0.0.1:47841/#token=" + token_path.read_text(), flush=True)
    host.wait()
except KeyboardInterrupt:
    pass
finally:
    for proc in reversed(processes):
        if proc.poll() is None:
            subprocess.run(["taskkill", "/PID", str(proc.pid), "/T", "/F"], capture_output=True)
