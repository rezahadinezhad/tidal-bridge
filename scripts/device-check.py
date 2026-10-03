"""Checks the features that already work on the real phone, so a change can
be compared with the previous version: projects with and without a task file
run on the phone, edits reach a running dev server, and a dev server that
stops on the phone restarts on the laptop.

    python scripts/device-check.py [--skip-projects]

Your own projects are type-checked on the phone when listed in
TIDALBRIDGE_CHECK_PROJECTS, as name=path pairs separated by semicolons:

    TIDALBRIDGE_CHECK_PROJECTS="web=C:\\code\\web\\frontend;api=C:\\code\\api"
"""
import json
import os
import signal
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
CLI = str(REPO / "bin" / "tidalbridge.exe")
SHIMS = str(Path.home() / ".tidalbridge" / "shims")
FIXTURE = REPO / "tests" / "fixtures" / "live-service"
FORMAT_FIXTURE = REPO / "tests" / "fixtures" / "write-back"
MESSY = 'const  greeting = {text:"hello",   target : "phone"}\nexport default greeting\n'
FORMATTED = 'const greeting = { text: "hello", target: "phone" };\nexport default greeting;\n'
PROJECTS = [tuple(pair.split("=", 1)) for pair in os.environ.get("TIDALBRIDGE_CHECK_PROJECTS", "").split(";") if "=" in pair]
results = []


def shim_env(**extra):
    env = dict(os.environ, PATH=SHIMS + os.pathsep + os.environ["PATH"], **extra)
    env.pop("TIDALBRIDGE_INTERNAL", None)
    return env


def report(name, ok, detail):
    results.append(ok)
    print(f"{'PASS' if ok else 'FAIL'}  {name}: {detail}", flush=True)


def fetch(timeout=60, want=None):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            body = urllib.request.urlopen("http://127.0.0.1:47900/", timeout=3).read().decode()
            if want is None or body.startswith(want):
                return body
        except OSError:
            pass
        time.sleep(1)
    return None


def project_checks():
    for name, path in PROJECTS:
        if not Path(path).is_dir():
            report(name, False, "project missing")
            continue
        started = time.time()
        run = subprocess.run(["npm", "run", "typecheck"], cwd=path, env=shim_env(TIDALBRIDGE_FORCE_REMOTE="1"), capture_output=True, text=True, shell=True)
        remote = "-> worker-" in run.stderr
        report(name + " typecheck on the phone", remote and "Tidal Bridge adapter:" not in run.stderr,
               f"exit {run.returncode} in {time.time() - started:.0f} s" + ("" if remote else " (did not reach the phone)"))


def write_back_check():
    source = FORMAT_FIXTURE / "src" / "messy.ts"
    with open(source, "w", newline="\n") as f:
        f.write(MESSY)
    try:
        run = subprocess.run(["npm", "run", "format"], cwd=FORMAT_FIXTURE, env=shim_env(TIDALBRIDGE_FORCE_REMOTE="1"), capture_output=True, text=True, shell=True)
        with open(source, newline="") as f:
            formatted = f.read()
        copied = "-> worker-" in run.stderr and "Copied 1 changed file" in run.stderr
        report("formatter on the phone, change copied back", copied and formatted == FORMATTED,
               f"exit {run.returncode}" + ("" if copied else ": " + run.stderr.strip().splitlines()[-1] if run.stderr.strip() else ""))
    finally:
        with open(source, "w", newline="\n") as f:
            f.write(MESSY)


def service_checks():
    message = FIXTURE / "message.txt"
    message.write_text("one\n")
    log = open(REPO / "tmp-device-check.log", "w")
    flags = subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0
    adapter = subprocess.Popen([str(Path(SHIMS) / "node.exe"), "server.js"], cwd=FIXTURE, env=shim_env(TIDALBRIDGE_FORCE_REMOTE="1"),
                               stdout=log, stderr=subprocess.STDOUT, creationflags=flags)
    try:
        body = fetch(180, "one")
        report("dev server on the phone", bool(body) and "linux" in body, body or "no answer on 127.0.0.1:47900")
        message.write_text("two\n")
        body = fetch(30, "two")
        report("edit reaches the running server", bool(body), body or "edit not seen within 30 s")
        jobs = json.loads(subprocess.run([CLI, "jobs"], capture_output=True, text=True).stdout or "[]")
        running = [j["id"] for j in jobs if j.get("state") == "RUNNING" and "server.js" in " ".join(j.get("spec", {}).get("argv", []))]
        if running:
            subprocess.run([CLI, "cancel", running[0]], capture_output=True)
        body = fetch(60, "two from win32")
        report("server restarts on the laptop when the phone run ends", bool(body), body or "no local server within 60 s")
    finally:
        adapter.send_signal(signal.CTRL_BREAK_EVENT) if os.name == "nt" else adapter.terminate()
        try:
            adapter.wait(20)
        except subprocess.TimeoutExpired:
            adapter.kill()
        message.write_text("one\n")
        log.close()
        (REPO / "tmp-device-check.log").unlink(missing_ok=True)


if "--skip-projects" not in sys.argv:
    project_checks()
write_back_check()
service_checks()
print(f"{sum(results)}/{len(results)} checks passed")
sys.exit(0 if all(results) else 1)
