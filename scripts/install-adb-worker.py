"""Install the ADB-shell worker on an authorized Android device.

Android runs app processes (Termux) on its efficiency cores and lets the
low-memory killer reclaim them. Processes started through the ADB shell may
use every core and are exempt, so compute runs several times faster. This
installs a Debian userland under /data/local/tmp/tidalbridge and runs the
worker as the shell user with the userland's Python (natively, through its
dynamic loader); each job runs in the userland through proot with only the
worker's own directory mounted. No root required.

proot comes from Termux's package repository (downloaded on this computer
and checked against the repository index; no Termux app is needed): Termux
builds it against Android's C library and ships the loader it needs. Its Termux-specific paths are rewritten
to the worker directory, because the shell user cannot enter Termux's private
data. The Debian image is the official arm64 layer, verified by digest.
"""
import argparse
import base64
import gzip
import hashlib
import io
import json
import os
import pathlib
import posixpath
import re
import secrets
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]
REMOTE = "/data/local/tmp/tidalbridge"
PORT = 47833
# Termux's proot has these paths built in; each is replaced in place by a
# shorter path in the worker directory.
TERMUX_PATHS = (
    ("/data/data/com.termux/files/applib/libproot-loader.so", REMOTE + "/libexec/loader"),
    ("/data/data/com.termux/files/applib/libproot-loader32.so", REMOTE + "/libexec/loader32"),
    ("/data/data/com.termux/files/usr/libexec/proot/loader", REMOTE + "/libexec/loader"),
    ("/data/data/com.termux/files/usr/libexec/proot/loader32", REMOTE + "/libexec/loader32"),
    ("/data/data/com.termux/files/usr/lib", REMOTE + "/lib"),
    ("/data/data/com.termux/files/usr/tmp/", REMOTE + "/tmp/"),
)
KIT_SCRIPT = r"""set -e
out=$(mktemp -d)
cp "$PREFIX/bin/proot" "$out/proot"
cp -L "$PREFIX/lib/libtalloc.so.2" "$out/libtalloc.so.2"
cp -L "$PREFIX/lib/libandroid-shmem.so" "$out/libandroid-shmem.so"
for f in "$PREFIX/libexec/proot/loader" /data/data/com.termux/files/applib/libproot-loader.so; do
  if [ -f "$f" ]; then cp -L "$f" "$out/loader"; break; fi
done
for f in "$PREFIX/libexec/proot/loader32" /data/data/com.termux/files/applib/libproot-loader32.so; do
  if [ -f "$f" ]; then cp -L "$f" "$out/loader32"; break; fi
done
echo "TIDALBRIDGE_KIT $(tar -C "$out" -cf - . | base64 -w0)"
rm -rf "$out"
"""

parser = argparse.ArgumentParser()
parser.add_argument("--serial", help="ADB serial (default: the approved real device)")
parser.add_argument("--data-dir", type=pathlib.Path, default=pathlib.Path.home() / ".tidalbridge")
parser.add_argument("--adb")
parser.add_argument("--debian-tag", default="trixie")
parser.add_argument("--skip-provision", action="store_true", help="Reuse an already provisioned userland")
parser.add_argument("--no-approve", action="store_true", help="Install and start the worker without approving it")
parser.add_argument("--kit-from-termux", action="store_true", help="Copy proot from the phone's Termux (through the Termux worker) instead of downloading it")
args = parser.parse_args()
CLI = ROOT / "bin" / "tidalbridge.exe"


def find_adb() -> str:
    if args.adb:
        return args.adb
    candidates = [pathlib.Path(os.environ.get("LOCALAPPDATA", "")) / "Android/Sdk/platform-tools/adb.exe", args.data_dir / "platform-tools/adb.exe"]
    for directory in os.environ.get("PATH", "").split(os.pathsep):
        candidates.append(pathlib.Path(directory) / "adb.exe")
    for candidate in candidates:
        if candidate.is_file():
            return str(candidate)
    raise SystemExit("adb not found")


ADB = find_adb()


def serial() -> str:
    if args.serial:
        return args.serial
    config = args.data_dir / "config.yaml"
    text = config.read_text(encoding="utf-8") if config.exists() else ""
    for block in re.split(r"\n\s*- ", text):
        match = re.search(r"serial:\s*(\S+)", block)
        if match and not re.search(r"mock:\s*true", block):
            return match.group(1)
    # A new installation: the one phone that allowed USB debugging.
    lines = subprocess.run([ADB, "devices"], capture_output=True, text=True).stdout.splitlines()[1:]
    ready = [line.split()[0] for line in lines if line.strip().endswith("device")]
    waiting = [line.split()[0] for line in lines if line.strip().endswith("unauthorized")]
    if len(ready) == 1:
        return ready[0]
    if len(ready) > 1:
        raise SystemExit("several phones are connected; pass --serial (see `adb devices`)")
    if waiting:
        raise SystemExit("unlock the phone and allow USB debugging for this computer, then run this again")
    raise SystemExit("no phone found: connect it by USB and enable Developer options > USB debugging")


SERIAL = serial()


def adb(*command: str, check: bool = True, input_bytes: bytes | None = None) -> str:
    result = subprocess.run([ADB, "-s", SERIAL, *command], input=input_bytes, capture_output=True)
    if check and result.returncode != 0:
        raise SystemExit(f"adb {' '.join(command)} failed: {result.stderr.decode(errors='replace').strip()}")
    return result.stdout.decode(errors="replace")


def shell(script: str, check: bool = True) -> str:
    return adb("shell", script, check=check)


def curl(url: str, headers: dict | None = None, output: pathlib.Path | None = None) -> bytes:
    """Windows curl (Schannel) as the TLS fallback. Python's OpenSSL can build
    a certificate path through an expired cross-signed root where Windows
    validates the current chain. Redirects drop Authorization headers."""
    command = ["curl.exe", "-fsSL", "--retry", "2", "-m", "900"]
    for key, value in (headers or {}).items():
        command += ["-H", f"{key}: {value}"]
    if output is not None:
        command += ["-o", str(output)]
    result = subprocess.run(command + [url], capture_output=True)
    if result.returncode != 0:
        raise SystemExit(f"download failed: {url}: {result.stderr.decode(errors='replace').strip()}")
    return result.stdout


def fetch(url: str, headers: dict | None = None) -> bytes:
    request = urllib.request.Request(url, headers={"User-Agent": "tidalbridge-installer", **(headers or {})})
    try:
        with urllib.request.urlopen(request, timeout=300) as response:
            return response.read()
    except urllib.error.HTTPError as error:
        # Some CDNs refuse scripted clients outright; Windows curl gets through.
        if error.code != 403:
            raise
        return curl(url, headers)
    except urllib.error.URLError as error:
        if "CERTIFICATE_VERIFY_FAILED" not in str(error):
            raise
        return curl(url, headers)


TERMUX_REPO = "https://packages-cf.termux.dev/apt/termux-main/"
KIT_FILES = {"bin/proot": "proot", "libexec/proot/loader": "loader", "libexec/proot/loader32": "loader32",
             "lib/libtalloc.so.2": "libtalloc.so.2", "lib/libandroid-shmem.so": "libandroid-shmem.so"}


def deb_files(data: bytes) -> dict[str, bytes]:
    """The files of a Debian package's data archive, symbolic links resolved."""
    if not data.startswith(b"!<arch>\n"):
        raise SystemExit("not a Debian package")
    offset, members = 8, {}
    while offset + 60 <= len(data):
        header = data[offset:offset + 60]
        name, size = header[:16].decode().strip().rstrip("/"), int(header[48:58].decode().strip())
        members[name] = data[offset + 60:offset + 60 + size]
        offset += 60 + size + (size & 1)
    name = next(n for n in members if n.startswith("data.tar"))
    files, links = {}, {}
    with tarfile.open(fileobj=io.BytesIO(members[name]), mode="r:*") as archive:
        for member in archive.getmembers():
            path = member.name.removeprefix("./")
            if member.isfile():
                files[path] = archive.extractfile(member).read()
            elif member.issym():
                links[path] = posixpath.normpath(posixpath.join(posixpath.dirname(path), member.linkname))
    for path, target in links.items():
        if target in files:
            files[path] = files[target]
    return files


def termux_repo_kit() -> dict[str, bytes]:
    """proot and the two libraries it needs, from Termux's aarch64 repository.
    Each package is checked against the SHA-256 in the repository index."""
    index = fetch(TERMUX_REPO + "dists/stable/main/binary-aarch64/Packages").decode()
    entries = {}
    for block in index.split("\n\n"):
        fields = dict(line.split(": ", 1) for line in block.splitlines() if ": " in line)
        if fields.get("Package") in ("proot", "libtalloc", "libandroid-shmem"):
            entries[fields["Package"]] = fields
    kit = {}
    for name, entry in sorted(entries.items()):
        data = fetch(TERMUX_REPO + entry["Filename"])
        if hashlib.sha256(data).hexdigest() != entry["SHA256"]:
            raise SystemExit(f"Termux package {name} does not match the repository index")
        for path, content in deb_files(data).items():
            short = path.removeprefix("data/data/com.termux/files/usr/")
            if short in KIT_FILES:
                kit[KIT_FILES[short]] = content
        print(f"downloaded Termux {name} {entry['Version']}", flush=True)
    return kit


def proot_kit(workdir: pathlib.Path) -> dict[str, bytes]:
    """proot, its libraries and loader from Termux, cached in the data directory."""
    cache = args.data_dir / "adb-worker" / "proot-kit.tar"
    if not cache.exists() and not args.kit_from_termux:
        kit = termux_repo_kit()
        cache.parent.mkdir(parents=True, exist_ok=True)
        with tarfile.open(cache, "w") as archive:
            for name, content in kit.items():
                info = tarfile.TarInfo(name)
                info.size, info.mode = len(content), 0o755
                archive.addfile(info, io.BytesIO(content))
    if not cache.exists():
        print("copying proot from Termux through the Termux worker...", flush=True)
        (workdir / "kit.sh").write_bytes(KIT_SCRIPT.encode())
        result = subprocess.run([str(CLI), "--data-dir", str(args.data_dir), "run", "--remote", "--runtime", "termux", "--workspace", str(workdir), "--timeout", "300", "--", "sh", "kit.sh"], capture_output=True)
        match = re.search(rb"TIDALBRIDGE_KIT ([A-Za-z0-9+/=]+)", result.stdout)
        if result.returncode != 0 or not match:
            raise SystemExit("could not copy proot from Termux (is the Termux worker running with proot installed?):\n" + result.stderr.decode(errors="replace")[-2000:])
        cache.parent.mkdir(parents=True, exist_ok=True)
        cache.write_bytes(base64.b64decode(match.group(1)))
    files = {}
    with tarfile.open(cache) as archive:
        for member in archive.getmembers():
            if member.isfile():
                files[pathlib.PurePosixPath(member.name).name] = archive.extractfile(member).read()
    missing = {"proot", "libtalloc.so.2", "libandroid-shmem.so", "loader"} - files.keys()
    if missing:
        raise SystemExit(f"Termux proot kit is incomplete: {sorted(missing)}")
    files["proot"] = relocate(files["proot"])
    return files


def relocate(binary: bytes) -> bytes:
    # The app's build keeps its loader in applib/, the repository's in
    # libexec/proot/; a build has one of them.
    relocated = set()
    for old, new in TERMUX_PATHS:
        original, replacement = old.encode() + b"\0", new.encode()
        count = binary.count(original)
        if count > 1 or len(replacement) >= len(original):
            raise SystemExit(f"unexpected Termux proot build: cannot relocate {old}")
        if count:
            binary = binary.replace(original, replacement + b"\0" * (len(original) - len(replacement)))
            relocated.add(new)
    if REMOTE + "/libexec/loader" not in relocated or REMOTE + "/lib" not in relocated:
        raise SystemExit("unexpected Termux proot build: no loader or library path to relocate")
    return binary


def debian_layer(path: pathlib.Path) -> None:
    token = json.loads(fetch("https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/debian:pull"))["token"]
    auth = {"Authorization": "Bearer " + token}
    base = "https://registry-1.docker.io/v2/library/debian/"
    index = json.loads(fetch(base + "manifests/" + args.debian_tag, dict(auth, Accept="application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json")))
    entry = next(m for m in index["manifests"] if m["platform"]["os"] == "linux" and m["platform"]["architecture"] == "arm64")
    manifest = json.loads(fetch(base + "manifests/" + entry["digest"], dict(auth, Accept="application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")))
    if len(manifest["layers"]) != 1:
        raise SystemExit("expected a single-layer Debian base image")
    digest = manifest["layers"][0]["digest"]
    curl(base + "blobs/" + digest, auth, path)
    sha = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1 << 20):
            sha.update(chunk)
    if "sha256:" + sha.hexdigest() != digest:
        path.unlink()
        raise SystemExit("Debian layer digest mismatch")
    print(f"Debian {args.debian_tag} arm64 layer verified ({digest[:19]}..., {path.stat().st_size >> 20} MB)", flush=True)


def flatten_hardlinks(layer: pathlib.Path, output: pathlib.Path) -> int:
    """Android denies hard links to the shell user, so store each hard-linked
    member as a regular copy. Inside the userland proot emulates links."""
    raw = layer.with_suffix(".raw")
    with gzip.open(layer, "rb") as source, raw.open("wb") as stream:
        while chunk := source.read(1 << 20):
            stream.write(chunk)
    copies = 0
    with tarfile.open(raw, "r:") as source, tarfile.open(output, "w:", format=tarfile.PAX_FORMAT) as target:
        for member in source:
            if member.islnk():
                linked = source.getmember(member.linkname)
                data = source.extractfile(linked).read()
                member.type, member.linkname, member.size, member.mode = tarfile.REGTYPE, "", len(data), linked.mode
                target.addfile(member, io.BytesIO(data))
                copies += 1
            elif member.isfile():
                target.addfile(member, source.extractfile(member))
            else:
                target.addfile(member)
    raw.unlink()
    return copies


def wait_for(marker: str, log: str, seconds: int) -> None:
    deadline = time.monotonic() + seconds
    last = ""
    while time.monotonic() < deadline:
        tail = shell(f"tail -c 2000 {log} 2>/dev/null", check=False)
        if marker in tail:
            return
        lines = [line for line in tail.strip().splitlines() if line.strip()]
        if lines and lines[-1] != last:
            last = lines[-1]
            print("  ..." + last[-110:], flush=True)
        if "TIDALBRIDGE_SETUP_FAILED" in tail:
            raise SystemExit("provisioning failed; see " + log)
        time.sleep(10)
    raise SystemExit(f"timed out waiting for {marker}; see {log}")


def stop_worker() -> None:
    """Stop every worker process of this installation, whatever the pid file
    says (a replacement that failed to bind its port leaves a stale one)."""
    # "[.]" keeps the pattern from matching the adb shell command carrying it.
    pattern = f"worker[.]py --root {REMOTE}/state"
    shell(f"[ -f {REMOTE}/worker.pid ] && kill $(cat {REMOTE}/worker.pid) 2>/dev/null; rm -f {REMOTE}/worker.pid; pkill -f '{pattern}'; true", check=False)
    deadline = time.monotonic() + 15
    while shell(f"pgrep -f '{pattern}'", check=False).strip():
        if time.monotonic() > deadline:
            shell(f"pkill -9 -f '{pattern}'; true", check=False)
            time.sleep(1)
            break
        time.sleep(0.5)


def proot_command(command: str, binds: str = "") -> str:
    """A shell command line that runs @command as root in the Debian userland."""
    return (f"cd {REMOTE} && PROOT_TMP_DIR={REMOTE}/tmp PROOT_L2S_DIR={REMOTE}/rootfs/.l2s ./bin/proot --kill-on-exit --link2symlink --sysvipc -L -0 "
            f"-r rootfs -b /dev -b /proc -b /sys -b /dev/urandom:/dev/random -b /proc/self/fd:/dev/fd {binds} -w /root "
            f"/usr/bin/env -i HOME=/root PATH=/opt/node/bin:/root/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin LANG=C.UTF-8 TERM=dumb {command}")


def main() -> None:
    if shell("echo ok").strip() != "ok":
        raise SystemExit("device shell unavailable")
    workdir = pathlib.Path(tempfile.mkdtemp(prefix="tidalbridge-adbw-"))
    shell(f"mkdir -p {REMOTE}/bin {REMOTE}/lib {REMOTE}/libexec {REMOTE}/rootfs {REMOTE}/worker {REMOTE}/state {REMOTE}/logs {REMOTE}/tmp {REMOTE}/sysdata")
    workspace = workdir / "kit-workspace"
    workspace.mkdir()
    kit = proot_kit(workspace)
    for name, target, mode in (("proot", "bin/proot", "755"), ("loader", "libexec/loader", "755"), ("loader32", "libexec/loader32", "755"), ("libtalloc.so.2", "lib/libtalloc.so.2", "644"), ("libandroid-shmem.so", "lib/libandroid-shmem.so", "644")):
        if name not in kit:
            continue
        staged = workdir / name
        staged.write_bytes(kit[name])
        current = shell(f"sha256sum {REMOTE}/{target} 2>/dev/null", check=False).split(" ")[0]
        if current != hashlib.sha256(kit[name]).hexdigest():
            adb("push", str(staged), f"{REMOTE}/{target}")
        shell(f"chmod {mode} {REMOTE}/{target}")
    # Stand-ins for /proc/sys entries Android denies to the shell user, named
    # like proot-distro's (the worker binds them only where reads fail).
    shell(f"mkdir -p {REMOTE}/sysdata/sys_empty")
    for name, value in (("sysctl_entry_cap_last_cap", "40"), ("sysctl_inotify_max_user_watches", "524288"), ("sysctl_kernel_overflowuid", "65534"), ("sysctl_kernel_overflowgid", "65534")):
        shell(f"echo {value} > {REMOTE}/sysdata/{name}")
    if not shell(f"[ -x {REMOTE}/rootfs/usr/bin/env ] && echo yes", check=False).strip():
        layer = workdir / "debian.tar.gz"
        debian_layer(layer)
        flat = workdir / "rootfs.tar"
        copies = flatten_hardlinks(layer, flat)
        print(f"stored {copies} hard link(s) as copies; pushing the userland...", flush=True)
        adb("push", str(flat), f"{REMOTE}/rootfs.tar")
        shell(f"cd {REMOTE}/rootfs && tar -xf ../rootfs.tar 2>{REMOTE}/logs/extract.log; rm -f ../rootfs.tar", check=False)
        if not shell(f"[ -x {REMOTE}/rootfs/usr/bin/env ] && echo yes", check=False).strip():
            raise SystemExit(f"extracting the userland failed; see {REMOTE}/logs/extract.log")
        shell(f"printf 'nameserver 8.8.8.8\\nnameserver 1.1.1.1\\n' > {REMOTE}/rootfs/etc/resolv.conf; printf '127.0.0.1 localhost\\n::1 localhost ip6-localhost ip6-loopback\\n' > {REMOTE}/rootfs/etc/hosts")
        shell(f"mkdir -p {REMOTE}/rootfs/etc/apt/apt.conf.d && echo 'APT::Sandbox::User \"root\";' > {REMOTE}/rootfs/etc/apt/apt.conf.d/01-tidalbridge-sandbox")
    shell(f"mkdir -p {REMOTE}/rootfs/.l2s {REMOTE}/rootfs/tmp && chmod 1777 {REMOTE}/rootfs/tmp")
    if "proot-ok" not in shell(proot_command("/bin/echo proot-ok") + " 2>&1", check=False):
        raise SystemExit("proot cannot enter the Debian userland as the shell user on this device")
    shell(f"rm -f {REMOTE}/debian.tar.gz {REMOTE}/tmp/tb {REMOTE}/tmp/echo {REMOTE}/tmp/loader2 {REMOTE}/tmp/ptrace-probe {REMOTE}/tmp/cprobe {REMOTE}/tmp/watch /data/local/tmp/tb-loader", check=False)
    for source, target in (("apps/android-worker/worker.py", "worker/worker.py"), ("apps/android-worker/setup-debian.sh", "worker/setup-debian.sh"), ("apps/android-worker/start-adb-worker.sh", "start-worker.sh")):
        staged = workdir / pathlib.Path(target).name
        staged.write_bytes((ROOT / source).read_bytes().replace(b"\r\n", b"\n"))
        adb("push", str(staged), f"{REMOTE}/{target}")
    provisioned = "yes" in shell(f"[ -x {REMOTE}/rootfs/opt/node/bin/node ] && [ -f {REMOTE}/rootfs/var/lib/tidalbridge-apt-done ] && echo yes", check=False)
    if not provisioned and not args.skip_provision:
        print("provisioning Node, uv, git and a C toolchain in the userland (several minutes)...", flush=True)
        command = proot_command("sh -c 'sh /tidalbridge-runtime/setup-debian.sh || echo TIDALBRIDGE_SETUP_FAILED'", f"-b {REMOTE}/worker:/tidalbridge-runtime")
        shell(f"rm -f {REMOTE}/logs/setup.log; setsid nohup sh -c \"{command}\" > {REMOTE}/logs/setup.log 2>&1 < /dev/null &")
        wait_for("TIDALBRIDGE_DEBIAN_READY", f"{REMOTE}/logs/setup.log", 3600)
        print(shell(f"grep '^node ' {REMOTE}/logs/setup.log").strip(), flush=True)
    token_path = args.data_dir / f"adb-worker-{re.sub(r'[^A-Za-z0-9._-]', '_', SERIAL)}.token"
    if not token_path.exists():
        token_path.write_text(secrets.token_hex(24))
    token = token_path.read_text().strip()
    adb("shell", f"cat > {REMOTE}/state/worker.token && chmod 600 {REMOTE}/state/worker.token", input_bytes=token.encode())
    stop_worker()
    print(shell(f"sh {REMOTE}/start-worker.sh").strip(), flush=True)
    local = adb("forward", "tcp:0", f"tcp:{PORT}").strip()
    try:
        deadline = time.monotonic() + 120
        while True:
            try:
                request = urllib.request.Request(f"http://127.0.0.1:{local}/v1/capabilities", headers={"Authorization": "Bearer " + token})
                capabilities = json.load(urllib.request.urlopen(request, timeout=10))
                break
            except OSError:
                if time.monotonic() > deadline:
                    raise SystemExit("worker did not start; see " + REMOTE + "/logs/worker.log")
                time.sleep(2)
    finally:
        adb("forward", "--remove", f"tcp:{local}", check=False)
    runtimes = capabilities["runtimes"]
    print(f"worker {capabilities['stable_id']} ready: {capabilities['model']} Android {capabilities['android_version']}, node {runtimes.get('node')}, python {runtimes.get('python')}", flush=True)
    if args.no_approve:
        return
    subprocess.run([str(CLI), "--data-dir", str(args.data_dir), "approve", "--serial", SERIAL, "--token-file", str(token_path), "--port", str(PORT), "--launcher", "adb-shell"], check=True)
    print("Approved. Restart the host to use it: tidalbridge service stop; tidalbridge service start", flush=True)


if __name__ == "__main__":
    main()
