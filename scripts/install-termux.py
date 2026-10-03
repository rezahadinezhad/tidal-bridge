"""Install official Termux for a discovered ABI after release checksum verification."""
import argparse
import hashlib
import json
import pathlib
import subprocess
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument("--adb", required=True)
parser.add_argument("--serial", required=True)
args = parser.parse_args()
root = pathlib.Path(__file__).resolve().parents[1] / ".tools"
root.mkdir(exist_ok=True)
abi = subprocess.check_output([args.adb, "-s", args.serial, "shell", "getprop", "ro.product.cpu.abi"], text=True).strip()
release = json.load(urllib.request.urlopen("https://api.github.com/repos/termux/termux-app/releases/latest", timeout=30))
asset = next(a for a in release["assets"] if a["name"].endswith("_" + abi + ".apk"))
checksums = next(a for a in release["assets"] if a["name"].endswith("sha256sums"))
expected = None
for line in urllib.request.urlopen(checksums["browser_download_url"], timeout=30).read().decode().splitlines():
    fields = line.split()
    if fields[-1].lstrip("*") == asset["name"]:
        expected = fields[0]
if not expected:
    raise SystemExit("Release checksum entry missing")
path = root / asset["name"]
if not path.exists():
    print("Downloading official Termux", release["tag_name"], abi, flush=True)
    urllib.request.urlretrieve(asset["browser_download_url"], path)
with path.open("rb") as stream:
    if hashlib.file_digest(stream, "sha256").hexdigest() != expected:
        raise SystemExit("Termux APK checksum mismatch")
print("Checksum verified. Installing package.", flush=True)
subprocess.run([args.adb, "-s", args.serial, "install", str(path)], check=True)
subprocess.run([args.adb, "-s", args.serial, "shell", "am", "start", "-n", "com.termux/.app.TermuxActivity"], check=True)
