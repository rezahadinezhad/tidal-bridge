"""Install a checksum-verified, project-local Go toolchain from go.dev."""
import hashlib
import json
import pathlib
import urllib.request
import zipfile

root = pathlib.Path(__file__).resolve().parents[1]
dest = root / ".tools"
dest.mkdir(exist_ok=True)
releases = json.load(urllib.request.urlopen("https://go.dev/dl/?mode=json", timeout=30))
release = next(r for r in releases if r["stable"])
asset = next(f for f in release["files"] if f["os"] == "windows" and f["arch"] == "amd64" and f["kind"] == "archive")
archive = dest / asset["filename"]
if not archive.exists():
    print("Downloading", asset["filename"], flush=True)
    urllib.request.urlretrieve("https://go.dev/dl/" + asset["filename"], archive)
with archive.open("rb") as stream:
    digest = hashlib.file_digest(stream, "sha256").hexdigest()
if digest != asset["sha256"]:
    raise SystemExit("Go archive checksum mismatch")
with zipfile.ZipFile(archive) as stream:
    stream.extractall(dest)
print("Verified", release["version"], "at", dest / "go" / "bin" / "go.exe")
