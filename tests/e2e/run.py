"""Real HTTP/process end-to-end suite with heterogeneous, explicitly simulated workers."""
import json
import os
import pathlib
import secrets
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[2]
BIN = ROOT / "bin/tidalbridge.exe"
processes = []


def port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def launch(command, log):
    stream = log.open("wb")
    proc = subprocess.Popen(command, cwd=ROOT, stdout=stream, stderr=subprocess.STDOUT)
    stream.close()
    processes.append(proc)
    return proc


def request(endpoint, token, path, body=None, timeout=30):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(endpoint + path, data=data, headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
    return json.load(urllib.request.urlopen(req, timeout=timeout))


def wait(condition, seconds=30):
    deadline = time.time() + seconds
    last = None
    while time.time() < deadline:
        try:
            last = condition()
            if last:
                return last
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.15)
    raise AssertionError("Timed out: " + str(last))


def kill(proc):
    if proc.poll() is None:
        subprocess.run(["taskkill", "/PID", str(proc.pid), "/T", "/F"], capture_output=True)
        proc.wait(timeout=10)


def main():
    with tempfile.TemporaryDirectory(prefix="tidalbridge-e2e-") as temporary:
        temp = pathlib.Path(temporary)
        host_dir = temp / "host"
        host_dir.mkdir()
        host_token = secrets.token_hex(24)
        (host_dir / "host.token").write_text(host_token)
        approved = []
        worker_procs = []
        for level in ("low", "mid", "high"):
            root = temp / level
            root.mkdir()
            token = secrets.token_hex(24)
            (root / "worker.token").write_text(token)
            worker_port = port()
            proc = launch([sys.executable, "apps/android-worker/worker.py", "--root", str(root), "--port", str(worker_port), "--mock", level], temp / (level + ".log"))
            worker_procs.append(proc)
            approved.append(dict(serial="mock-" + level, token=token, port=worker_port, endpoint="http://127.0.0.1:" + str(worker_port), mock=True))
        for device in approved:
            wait(lambda d=device: request(d["endpoint"], d["token"], "/v1/health")["simulated"], 60)
        host_port = port()
        configuration = "bind: 127.0.0.1:" + str(host_port) + "\nmode: AUTO\napproved:\n"
        for device in approved:
            configuration += "  - serial: " + device["serial"] + "\n    token: " + device["token"] + "\n    port: " + str(device["port"]) + "\n    endpoint: " + device["endpoint"] + "\n    mock: true\n"
        (host_dir / "config.yaml").write_text(configuration)
        host_proc = launch([str(BIN), "--data-dir", str(host_dir), "serve"], temp / "host.log")
        endpoint = "http://127.0.0.1:" + str(host_port)
        api = lambda path, body=None: request(endpoint, host_token, "/v1/" + path, body)
        try:
            wait(lambda: len(api("status")["workers"]) == 3 and all(w["profile"]["capabilities"]["simulated"] for w in api("status")["workers"]), 60)
            status = api("status")
            assert all(w["profile"]["capabilities"]["simulated"] for w in status["workers"])
            ids = {w["profile"]["capabilities"]["model"].split()[-1].lower(): w["id"] for w in status["workers"]}
            assert len(list((host_dir / "profiles").glob("*.json"))) == 3
            def submit(argv, **extra):
                spec = dict(argv=argv, timeout_seconds=15, estimated_duration_ms=1000, policy=dict(local_fallback=True, force_remote=True, idempotent=True, retryable=True))
                spec.update(extra)
                return api("jobs", spec)
            def done(job):
                return wait(lambda: (value if (value := api("jobs/" + job["id"])).get("finished") else None), 25)
            for argv, expected in ((["python", "-c", "print('hello from worker')"], "hello from worker"), (["node", "-e", "console.log('hello from node')"], "hello from node"), (["git", "--version"], "git version")):
                result = done(submit(argv))
                assert result["state"] == "COMPLETED", result
                assert expected in result["stdout"], result
            print("PASS handshake, heterogeneous profiles/calibration, Python/Node/Git execution", flush=True)
            fixture = temp / "repo"
            fixture.mkdir()
            (fixture / "test_math.py").write_text("import unittest\nclass TestMath(unittest.TestCase):\n def test_sum(self): self.assertEqual(2+2,4)\n")
            (fixture / ".env").write_text("SECRET=excluded")
            (fixture / ".gitignore").write_text("ignored.txt\n")
            (fixture / "ignored.txt").write_text("excluded")
            subprocess.run(["git", "init", "-q", str(fixture)], check=True)
            policy = dict(force_remote=True, device_id=ids["high"], local_fallback=True, idempotent=True, retryable=True)
            first = done(submit(["python", "-m", "unittest", "discover"], workspace=str(fixture), policy=policy))
            assert first["state"] == "COMPLETED", first
            second = done(submit(["python", "-c", "import os;print(sorted(os.listdir('.')))"], workspace=str(fixture), policy=policy))
            assert second["attempts"][-1]["bytes_sent"] == 0, second
            assert ".env" not in second["stdout"] and "ignored.txt" not in second["stdout"], second
            (fixture / "changed.txt").write_text("incremental")
            third = done(submit(["python", "-c", "print(open('changed.txt').read())"], workspace=str(fixture), policy=policy))
            assert third["attempts"][-1]["bytes_sent"] == len("incremental"), third
            artifact = done(submit(["python", "-c", "open('result.json','w').write('{\"ok\":true}')"], expected_outputs=["result.json"], policy=policy))
            assert list((host_dir / "results" / artifact["id"]).glob("*/artifacts/result.json"))
            print("PASS real unittest execution, ignored secrets, warm sync, changed-only sync, declared artifact", flush=True)
            a = submit(["python", "-c", "import time;time.sleep(2);print('A')"], policy={**policy, "device_id": ids["mid"]})
            b = submit(["python", "-c", "import time;time.sleep(2);print('B')"], policy=policy)
            ra, rb = done(a), done(b)
            assert ra["attempts"][0]["target"] != rb["attempts"][0]["target"]
            start_a, start_b = ra["attempts"][0]["started"], rb["attempts"][0]["started"]
            assert abs(__import__('datetime').datetime.fromisoformat(start_a.replace('Z','+00:00')).timestamp() - __import__('datetime').datetime.fromisoformat(start_b.replace('Z','+00:00')).timestamp()) < 1.5
            api("devices/" + ids["high"] + "/drain", {"draining": True})
            route = api("explain", dict(argv=["python", "-c", "print(1)"], policy=dict(force_remote=True)))
            assert route["target"] != ids["high"], route
            api("devices/" + ids["high"] + "/drain", {"draining": False})
            rejected = done(submit(["cmd", "/c", "echo local"], policy=policy))
            assert rejected["decision"]["target"] == "REJECT", rejected
            timed = done(submit(["python", "-c", "import time;time.sleep(20)"], timeout_seconds=1, policy=policy))
            assert timed["state"] == "FAILED", timed
            canceljob = submit(["python", "-c", "import time;time.sleep(20)"], policy=policy)
            wait(lambda: api("jobs/" + canceljob["id"])["state"] == "RUNNING")
            api("jobs/" + canceljob["id"] + "/cancel", {})
            assert done(canceljob)["state"] == "CANCELLED"
            wait(lambda: api("status")["active_jobs"] == 0)
            print("PASS parallel worker execution, drain, Windows compatibility, timeout, cancellation", flush=True)
            fallback = submit(["python", "-c", "import time;time.sleep(2);print('recovered')"], policy=policy)
            wait(lambda: api("jobs/" + fallback["id"])["state"] in ("RUNNING", "FAILED", "COMPLETED"))
            assert api("jobs/" + fallback["id"])["state"] == "RUNNING", api("jobs/" + fallback["id"])
            kill(worker_procs[2])
            recovered = done(fallback)
            assert recovered["state"] == "COMPLETED" and recovered["attempts"][-1]["target"] == "LOCAL", recovered
            unsafe = submit(["python", "-c", "print(1)"], policy=dict(force_remote=True, device_id=ids["high"], local_fallback=True))
            assert done(unsafe)["state"] == "FAILED"
            print("PASS disconnect recovery with preserved attempts and conservative non-replay", flush=True)
            try:
                request(endpoint, "wrong", "/v1/status")
                raise AssertionError("unauthenticated host accepted")
            except urllib.error.HTTPError as error:
                assert error.code == 401
            try:
                submit(["python"], expected_outputs=["../secret"])
                raise AssertionError("path traversal accepted")
            except urllib.error.HTTPError as error:
                assert error.code == 400
            snapshot = api("status")["host"]
            print("PASS API authentication and path traversal protection", flush=True)
            print("HOST SNAPSHOT " + json.dumps(snapshot), flush=True)
        finally:
            for proc in reversed(processes):
                kill(proc)
            if host_proc.returncode not in (0, 1):
                print((temp / "host.log").read_text(errors="replace")[-4000:])


if __name__ == "__main__":
    main()
