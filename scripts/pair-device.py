"""Pair/bootstrap an authorized, debuggable Termux worker without shared-storage secrets."""
import argparse
import os
import pathlib
import re
import secrets
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument("--adb", required=True)
parser.add_argument("--serial", required=True)
parser.add_argument("--data-dir", type=pathlib.Path, default=pathlib.Path.home() / ".tidalbridge")
parser.add_argument("--bootstrap", action="store_true")
args = parser.parse_args()
ROOT = pathlib.Path(__file__).resolve().parents[1]
prefix = [args.adb, "-s", args.serial]
subprocess.run(prefix + ["shell", "run-as com.termux id"], check=True)
args.data_dir.mkdir(parents=True, exist_ok=True)
if not re.fullmatch(r"[a-zA-Z0-9_-]+", args.serial):
    raise SystemExit("Unsupported serial format")
token_path = args.data_dir / ("pair-" + args.serial + ".token")
if not token_path.exists():
    token_path.write_text(secrets.token_hex(24))
subprocess.run(prefix + ["shell", "run-as com.termux mkdir -p files/home/tidalbridge/worker"], check=True)
for source, target in [(ROOT / "apps/android-worker/worker.py", "worker/worker.py"), (ROOT / "scripts/setup-android.sh", "setup-android.sh"), (token_path, "worker.token")]:
    command = "run-as com.termux sh -c 'cat > files/home/tidalbridge/" + target + "'"
    data = source.read_bytes().replace(b"\r\n", b"\n")
    subprocess.run(prefix + ["shell", command], input=data, check=True)
subprocess.run(prefix + ["shell", "run-as com.termux chmod 600 files/home/tidalbridge/worker.token"], check=True)
subprocess.run([str(ROOT / "bin/tidalbridge.exe"), "--data-dir", str(args.data_dir), "approve", "--serial", args.serial, "--token-file", str(token_path)], check=True)
if args.bootstrap:
    command = "run-as com.termux sh -c 'export HOME=/data/data/com.termux/files/home; export PREFIX=/data/data/com.termux/files/usr; export PATH=$PREFIX/bin:/system/bin; export TMPDIR=$PREFIX/tmp; bash $HOME/tidalbridge/setup-android.sh'"
    subprocess.run(prefix + ["shell", command], check=True)
else:
    print("Paired. Run once in Termux: bash ~/tidalbridge/setup-android.sh")
