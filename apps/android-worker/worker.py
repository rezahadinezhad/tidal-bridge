#!/usr/bin/env python3
"""Tidal Bridge Worker: loopback-only authenticated, bounded Python executor.

Runtimes
  termux  Android-native (bionic) Termux userland, the default.
  debian  A proot-distro Debian container, entered by invoking proot directly
          (the proot-distro wrapper costs about a second per call). glibc
          userland: PyPI manylinux wheels and npm linux-arm64 binaries work.

Workspaces
  A job either runs in a private copy of an immutable snapshot (legacy), or in
  a persistent per-project tree that is updated incrementally from manifests.
  Trees keep dependency directories and tool caches between jobs, like a
  normal checkout, so warm runs do not reinstall or recopy anything.

Services
  A service job (dev server) has no deadline, runs until cancelled, and
  reports the TCP ports it announces so the host can forward them.
"""
from __future__ import annotations

import argparse
import base64
import concurrent.futures
import glob
import hashlib
import hmac
import json
import os
import pathlib
import platform
import re
import secrets
import shlex
import shutil
import signal
import stat
import subprocess
import sys
import threading
import time
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, unquote, urlsplit

VERSION = "0.2.1"
PROTOCOL = 1
MAX_JSON = 16 * 1024 * 1024
MAX_LOG = 64 * 1024 * 1024
MAX_SERVICES = 4
KEEP_JOB_DIRS = 200
# Project copies and cached file content nothing has used for two weeks are
# removed; below the storage floor the least recently used copies go first.
# A removed copy is rebuilt from the laptop the next time it is needed.
IDLE_SECONDS = 14 * 86400
PROOT_TEST_WORKERS = 2
# Past its time limit a job that is still printing keeps running: a slow phone
# is not a hung job. It stops after SILENT_LIMIT seconds without output, or at
# OVERTIME times its limit. The host waits as long (overtime in host.go).
SILENT_LIMIT = 600
OVERTIME = 3
# Write-back jobs report files they changed outside these directories.
SKIP_CHANGE_DIRS = {"node_modules", ".tidalbridge-deps", ".git", ".hg", ".svn", ".next", ".nuxt", ".svelte-kit", ".turbo", ".parcel-cache", ".cache", ".vite",
                    "dist", "build", "out", "coverage", "__pycache__", ".venv", "venv", ".tox", ".mypy_cache", ".pytest_cache", ".ruff_cache",
                    "target", ".gradle"}
WRITE_BACK_FILES = 5000
WRITE_BACK_BYTES = 256 << 20
ID = re.compile(r"^[a-zA-Z0-9_-]{1,96}$")
HASH = re.compile(r"^[0-9a-f]{64}$")
PORT_URL = re.compile(rb"(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1?\]):(\d{2,5})")
DEPENDENCY_FILES = ("requirements.txt", "pyproject.toml", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "uv.lock")
GUEST_PATH = "/opt/node/bin:/root/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"


def safe_path(value: str) -> bool:
    return bool(value) and not value.startswith("/") and not any(x in value for x in ("\\", ":", "\x00")) and ".." not in value.split("/")


def within(root: pathlib.Path, value: str) -> pathlib.Path:
    if not safe_path(value):
        raise ValueError("unsafe relative path")
    path = root / value
    if not path.resolve().is_relative_to(root.resolve()):
        raise ValueError("path escapes workspace")
    return path


class Confined:
    """within() for many writes into one tree: refuses paths whose directory
    resolves outside root, resolving each directory once. Under proot every
    path component costs a traced lstat, so resolving each file of a large
    project took tens of seconds. The last component is not resolved: tree
    writes replace it with a rename and removals unlink it, so a symlink
    there is replaced, never followed. Reads of job output use within()."""

    def __init__(self, root: pathlib.Path):
        self.root = root
        self.real = root.resolve()
        self.directories: dict[pathlib.Path, bool] = {}

    def __call__(self, value: str) -> pathlib.Path:
        if not safe_path(value):
            raise ValueError("unsafe relative path")
        path = self.root / value
        inside = self.directories.get(path.parent)
        if inside is None:
            inside = self.directories[path.parent] = path.parent.resolve().is_relative_to(self.real)
        if not inside:
            raise ValueError("path escapes workspace")
        return path


def atomic(path: pathlib.Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_suffix(path.suffix + ".tmp")
    temp.write_text(json.dumps(value, indent=2), encoding="utf-8")
    os.replace(temp, path)


def version(exe: str) -> str | None:
    if not shutil.which(exe):
        return None
    try:
        result = subprocess.run([exe, "--version"], capture_output=True, text=True, timeout=3)
        found = re.search(r"\d+(?:\.\d+){0,2}", result.stdout + result.stderr)
        return found.group() if found else None
    except (OSError, subprocess.SubprocessError):
        return None


def performance_cores() -> set[int]:
    """Cores outside the slowest cluster ({} on uniform CPUs). Android's
    energy-aware scheduler places mostly waiting processes, such as proot's
    tracer, on efficiency cores; every traced call of a job then waits for
    one. Measured on the S23 Ultra: vitest sample 105 s -> 59 s, warm tsc
    21 s -> 17 s with jobs kept off cores 0-2."""
    speeds = {}
    for path in glob.glob("/sys/devices/system/cpu/cpu[0-9]*/cpufreq/cpuinfo_max_freq"):
        try:
            speeds[int(re.search(r"/cpu(\d+)/cpufreq/", path).group(1))] = int(pathlib.Path(path).read_text())
        except (OSError, ValueError, AttributeError):
            pass
    if len(set(speeds.values())) < 2:
        return set()
    slowest = min(speeds.values())
    return {cpu for cpu, speed in speeds.items() if speed > slowest}


def getprop(name: str) -> str:
    try:
        return subprocess.check_output(["getprop", name], text=True, timeout=2).strip()
    except (OSError, subprocess.SubprocessError):
        return ""


def file_hash(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1 << 16):
            digest.update(chunk)
    return digest.hexdigest()


class Debian:
    """Direct proot invocation mirroring proot-distro's normal login.

    prefix locates Termux's proot-distro container; userland instead names an
    ADB-shell installation (bin/proot, rootfs, sysdata, tmp)."""

    def __init__(self, prefix: str | None, userland: pathlib.Path | None = None):
        if userland:
            self.base = userland
            self.rootfs = userland / "rootfs"
            self.sysdata = userland / "sysdata"
            self.proot = str(userland / "bin/proot") if (userland / "bin/proot").is_file() else None
            self.tmp = str(userland / "tmp")
            self.l2s = str(self.rootfs / ".l2s")
        else:
            self.base = pathlib.Path(prefix or "/nonexistent") / "var/lib/proot-distro/containers/debian"
            self.rootfs = self.base / "rootfs"
            self.sysdata = self.base / "sysdata"
            self.proot = shutil.which("proot")
            self.tmp = os.path.join(os.environ.get("PREFIX", "/tmp"), "tmp")
            self.l2s = str(self.base / ".l2s")
        self._versions: dict = {}
        self._probed = 0.0

    def available(self) -> bool:
        return bool(self.proot) and (self.rootfs / "usr/bin/env").exists()

    def argv(self, argv: list[str], cwd: pathlib.Path, env: dict, binds: list[pathlib.Path]) -> list[str]:
        args = [self.proot, "--kill-on-exit", "--link2symlink", "--sysvipc", "-L", "--change-id=0:0",
                f"--rootfs={self.rootfs}", f"--cwd={cwd}",
                "--bind=/dev", "--bind=/proc", "--bind=/sys",
                "--bind=/dev/urandom:/dev/random", "--bind=/proc/self/fd:/dev/fd",
                f"--bind={self.sysdata}/sys_empty:/sys/fs/selinux",
                f"--bind={self.rootfs}/tmp:/dev/shm"]
        for real, fake in (("/proc/loadavg", "loadavg"), ("/proc/stat", "stat"), ("/proc/uptime", "uptime"),
                           ("/proc/version", "version"), ("/proc/vmstat", "vmstat"),
                           ("/proc/sys/kernel/cap_last_cap", "sysctl_entry_cap_last_cap"),
                           ("/proc/sys/fs/inotify/max_user_watches", "sysctl_inotify_max_user_watches"),
                           ("/proc/sys/kernel/overflowuid", "sysctl_kernel_overflowuid"),
                           ("/proc/sys/kernel/overflowgid", "sysctl_kernel_overflowgid")):
            try:
                with open(real, "rb") as stream:
                    stream.read(1)
            except OSError:
                if (self.sysdata / fake).exists():
                    args.append(f"--bind={self.sysdata / fake}:{real}")
        for bind in binds:
            args.append(f"--bind={bind}")
        guest = {"HOME": "/root", "USER": "root", "LANG": "C.UTF-8", "TERM": "dumb", "TMPDIR": "/tmp", "PATH": GUEST_PATH}
        for key, value in env.items():
            if key in ("PATH", "HOME", "TMPDIR", "PREFIX", "LANG", "LD_PRELOAD", "LD_LIBRARY_PATH", "ANDROID_ROOT", "ANDROID_DATA", "TMP", "TEMP", "SYSTEMROOT", "WINDIR"):
                continue
            guest[key] = value
        if "TIDALBRIDGE_GUEST_PATH" in env:
            guest["PATH"] = env["TIDALBRIDGE_GUEST_PATH"] + ":" + GUEST_PATH
            guest.pop("TIDALBRIDGE_GUEST_PATH", None)
        return args + ["/usr/bin/env", "-i"] + [f"{k}={v}" for k, v in guest.items()] + argv

    def host_env(self, env: dict) -> dict:
        result = {k: v for k, v in env.items() if k != "LD_PRELOAD"}
        result["PROOT_TMP_DIR"] = self.tmp
        result["PROOT_L2S_DIR"] = self.l2s
        return result

    def versions(self) -> dict:
        # Re-probe while empty (provisioning may still be running), else hourly;
        # a stale answer is refreshed in the background (probing takes seconds).
        now = time.monotonic()
        if self._versions and now - self._probed < 3600:
            return self._versions
        if not self._versions and now - self._probed < 60:
            return self._versions
        self._probed = now
        if self._versions:
            threading.Thread(target=self._probe, daemon=True).start()
            return self._versions
        return self._probe()

    def _probe(self) -> dict:
        found = {}
        if self.available():
            probe = "printf 'node=%s\\nnpm=%s\\npython=%s\\nuv=%s\\ngit=%s\\nbash=%s\\n' \"$(node --version 2>/dev/null)\" \"$(npm --version 2>/dev/null)\" \"$(python3 --version 2>/dev/null)\" \"$(uv --version 2>/dev/null)\" \"$(git --version 2>/dev/null)\" \"$(bash --version 2>/dev/null | head -n 1)\""
            try:
                out = subprocess.run(self.argv(["sh", "-c", probe], pathlib.Path("/"), {}, []), capture_output=True, text=True, timeout=30, env=self.host_env(dict(os.environ))).stdout
                for line in out.splitlines():
                    name, _, value = line.partition("=")
                    match = re.search(r"\d+(?:\.\d+){0,2}", value)
                    if match:
                        found[name] = match.group()
            except (OSError, subprocess.SubprocessError):
                pass
        self._versions = found
        return found


SOLD_RAM_GB = (1, 2, 3, 4, 6, 8, 10, 12, 16, 18, 24, 32, 64)


def physical_memory_mb(usable_mb: int) -> int:
    """The memory a device is sold with. Android sees less, because the
    modem, graphics and security chips keep part of it (usually 5-15%). The
    device tree lists the real banks where it is readable; otherwise this is
    the nearest size devices are sold in, at or above what Android sees."""
    banks = 0
    for reg in glob.glob("/proc/device-tree/memory*/reg"):
        try:
            data = pathlib.Path(reg).read_bytes()
        except OSError:
            continue
        # arm64: two 32-bit address cells and two size cells per bank.
        banks += sum(int.from_bytes(data[i + 8:i + 16], "big") for i in range(0, len(data) - 15, 16))
    if usable_mb and usable_mb <= banks >> 20 <= 2 * usable_mb:
        return banks >> 20
    return next((gb * 1024 for gb in SOLD_RAM_GB if gb * 1024 >= usable_mb), usable_mb)


def all_cores() -> None:
    """Native jobs need no pinning (no tracer to misplace), and Node then sees
    the same CPU count through every API: test runners that compare them, such
    as Vitest's per-project worker defaults, would otherwise disagree."""
    try:
        os.sched_setaffinity(0, range(os.cpu_count() or 1))
    except (OSError, AttributeError):
        pass


def ecosystem(spec: dict) -> str:
    """Which locked environment a job needs where a directory has both: a
    Python command gets the Python one, anything else the Node one."""
    chosen = spec.get("env", {}).get("TIDALBRIDGE_ECOSYSTEM")
    if chosen in ("node", "python"):
        return chosen
    argv = spec.get("argv") or [""]
    return "python" if pathlib.PurePosixPath(argv[0]).name in ("python", "python3", "pytest", "ruff", "mypy", "uv") else "node"


def marker_name(runtime: str, kind: str) -> str:
    return f"{runtime}.json" if kind == "node" else f"{runtime}-python.json"


def tunnel_in_use(ports: set, tables: tuple = ("/proc/net/tcp", "/proc/net/tcp6")) -> bool | None:
    """Whether any TCP socket other than a listener has one of these ports at
    either end; None when the kernel tables cannot be read."""
    for table in tables:
        try:
            with open(table) as stream:
                lines = stream.read().splitlines()[1:]
        except OSError:
            return None
        for line in lines:
            fields = line.split()
            if len(fields) < 4 or fields[3] == "0A":  # 0A: listening
                continue
            try:
                if int(fields[1].rsplit(":", 1)[1], 16) in ports or int(fields[2].rsplit(":", 1)[1], 16) in ports:
                    return True
            except (IndexError, ValueError):
                continue
    return False


class ServiceWatch:
    """Notices whether a job reached a laptop service through a tunnel (a
    database or an API on the laptop): the host never reuses such a result,
    since it depends on more than the files. A closed connection stays in the
    kernel table for a minute (TIME_WAIT), so a sample every few seconds
    misses none. `used` is None when the table could not be read."""

    def __init__(self, ports: set, every: float = 3.0):
        self.ports, self.every, self.used = ports, every, False
        self.stopped = threading.Event()
        self.thread = threading.Thread(target=self.watch, daemon=True)

    def sample(self) -> None:
        seen = tunnel_in_use(self.ports)
        if self.used is not True:
            self.used = True if seen else None if seen is None or self.used is None else False

    def watch(self) -> None:
        while not self.stopped.wait(self.every):
            self.sample()

    def __enter__(self):
        if self.ports:
            self.sample()
            self.thread.start()
        return self

    def __exit__(self, *_):
        if self.ports:
            self.stopped.set()
            self.thread.join(timeout=5)
            self.sample()
        return False


TSC_SELF_MANAGED = {"-b", "--build", "-w", "--watch", "-i", "--incremental", "--tsbuildinfofile", "--composite"}


def tsc_incremental(args: list[str], script: str, cwd: pathlib.Path, warm: pathlib.Path) -> list[str]:
    """`tsc --noEmit` keeps its incremental state on the phone, outside the
    project copy (a sync would delete it there), so a repeat check redoes only
    what changed. TypeScript validates that state itself and reports the same
    diagnostics as a full check. Builds, watch mode and commands that already
    choose their own incremental state are left alone."""
    names = {a.split("=", 1)[0].lower() for a in args}
    if "--noemit" not in names or names & TSC_SELF_MANAGED:
        return args
    try:
        package = pathlib.Path(script).resolve().parents[1] / "package.json"
        if int(json.loads(package.read_text())["version"].split(".")[0]) < 4:
            return args  # --incremental with --noEmit needs TypeScript 4
    except (OSError, ValueError, KeyError, IndexError):
        return args
    digest = hashlib.sha256("\0".join([str(cwd), *args]).encode()).hexdigest()[:20]
    (warm / "tsc").mkdir(parents=True, exist_ok=True)
    return [*args, "--incremental", "--tsBuildInfoFile", str(warm / "tsc" / f"{digest}.tsbuildinfo")]


class NativeNode:
    """Runs the userland's Node without proot. Its glibc loader path is
    rewritten to a short link outside the userland, so Node and every child
    Node it starts (test runner workers) execute directly on all cores, with
    no per-call tracing. Only a direct Node program qualifies: a command that
    needs a shell or another program runs under proot instead."""
    INTERP = b"/lib/ld-linux-aarch64.so.1\0"
    LINK = "/data/local/tmp/tb/ld"

    def __init__(self, userland: pathlib.Path):
        self.userland = userland
        self.rootfs = userland / "rootfs"
        self.lib = self.rootfs / "usr/lib/aarch64-linux-gnu"
        self.node = userland / "native/node"
        self.warm: pathlib.Path | None = None  # state the tools keep between runs
        self.lock = threading.Lock()
        self.ok: bool | None = None
        self.checked = 0.0

    def ready(self) -> bool:
        with self.lock:
            if self.ok is None or time.monotonic() - self.checked > 3600:
                self.checked = time.monotonic()
                try:
                    self.prepare()
                    self.ok = True
                except (OSError, ValueError):
                    self.ok = False
            return self.ok

    def prepare(self) -> None:
        source = self.rootfs / "opt/node/bin/node"
        loader = self.lib / "ld-linux-aarch64.so.1"
        link = pathlib.Path(self.LINK)
        link.parent.mkdir(parents=True, exist_ok=True)
        if not link.is_symlink() or os.readlink(link) != str(loader):
            link.unlink(missing_ok=True)
            link.symlink_to(loader)
        info = source.stat()
        stamp, marker = f"{info.st_size}:{info.st_mtime_ns}", self.node.with_name("node.source")
        if self.node.is_file() and marker.is_file() and marker.read_text() == stamp:
            return
        data = source.read_bytes()
        if data.count(self.INTERP) != 1:
            raise ValueError("unexpected Node interpreter")
        replacement = self.LINK.encode() + b"\0"
        self.node.parent.mkdir(parents=True, exist_ok=True)
        temporary = self.node.with_name("node.tmp")
        temporary.write_bytes(data.replace(self.INTERP, replacement + b"\0" * (len(self.INTERP) - len(replacement)), 1))
        temporary.chmod(0o755)
        os.replace(temporary, self.node)
        marker.write_text(stamp)

    def resolve(self, tool: str, cwd: pathlib.Path) -> str | None:
        """The JavaScript file behind a package binary, if it is a Node program."""
        for directory in (cwd, *cwd.parents):
            candidate = directory / "node_modules/.bin" / tool
            if candidate.exists():
                target = candidate.resolve()
                with open(target, "rb") as stream:
                    first = stream.readline(256)
                return str(target) if first.startswith(b"#!") and b"node" in first else None
            if directory == self.userland:
                break
        return None

    def command(self, argv: list[str], cwd: pathlib.Path, env: dict) -> list[str] | None:
        if argv[0] == "node":
            return [str(self.node), *argv[1:]]
        extra: list[str] = []
        if argv[0] == "npm":
            if argv[1:2] == ["test"]:
                name, rest = "test", argv[2:]
            elif argv[1:2] == ["run"] and len(argv) > 2:
                name, rest = argv[2], argv[3:]
            else:
                return None
            if rest[:1] == ["--"]:
                rest = rest[1:]
            elif rest:
                return None
            try:
                body = json.loads((cwd / "package.json").read_text())["scripts"][name]
                words = shlex.split(body)
            except (OSError, ValueError, KeyError, TypeError):
                return None
            if not words or "=" in words[0] or any(w in ("&&", "||", ";", "|", "&", ">", "<") or w.startswith(("$", "`")) or "$(" in w for w in words):
                return None
            env.update(npm_lifecycle_event=name, npm_lifecycle_script=body, npm_package_json=str(cwd / "package.json"), INIT_CWD=str(cwd))
            argv, extra = words, rest
        script = self.resolve(argv[0], cwd)
        if not script:
            return None
        args = [*argv[1:], *extra]
        if argv[0] == "tsc" and self.warm:
            args = tsc_incremental(args, script, cwd, self.warm)
        return [str(self.node), script, *args]

    def env(self, job_env: dict) -> dict:
        result = {k: v for k, v in job_env.items() if k not in ("PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "PREFIX", "TIDALBRIDGE_GUEST_PATH")}
        tree_bins = [p for p in job_env.get("TIDALBRIDGE_GUEST_PATH", "").split(":") if p.startswith(str(self.userland))]
        result.update(PATH=":".join([str(self.node.parent), *tree_bins, "/system/bin", "/system/xbin"]), LD_LIBRARY_PATH=str(self.lib),
                      HOME=str(self.rootfs / "root"), TMPDIR=str(self.userland / "tmp"), LANG="C.UTF-8")
        if self.warm:
            # Node keeps compiled code between runs (checked against each
            # file's source; Node 22 and later), so tools start warm.
            result.setdefault("NODE_COMPILE_CACHE", str(self.warm / "node"))
        return result


class Worker:
    def __init__(self, root: pathlib.Path, token: str, mock: str | None, capacity: int, guest: bool = False, userland: pathlib.Path | None = None):
        # guest: this worker already runs inside the Debian userland (started
        # by the host through the ADB shell), so jobs run without proot.
        # userland: this worker runs natively beside an ADB-shell Debian
        # installation and runs every job in it through proot. Its own file
        # work (sync, logs) then avoids proot's per-call tracing cost.
        self.guest = guest
        self.debian_only = userland is not None
        self.root = root.resolve()
        self.token = token
        self.mock = mock
        self.capacity = capacity
        self.lock = threading.RLock()
        self.sync_lock = threading.Lock()
        self.env_lock = threading.Lock()
        self.tree_locks: dict[str, threading.Lock] = {}
        self.jobs: dict[str, dict] = {}
        self.processes: dict[str, subprocess.Popen] = {}
        # Projects copied one level down, beside files they read from outside
        # their folder: workspace key -> project folder name.
        self.nests: dict[str, str] = {}
        self.cancelled: set[str] = set()
        self.pool = concurrent.futures.ThreadPoolExecutor(max_workers=capacity)
        for name in ("cache/blobs", "workspaces", "jobs", "environments", "logs", "trees"):
            (self.root / name).mkdir(parents=True, exist_ok=True)
        identity = self.root / "identity"
        if not identity.exists():
            identity.write_text("worker-" + secrets.token_hex(12), encoding="ascii")
        self.identity = identity.read_text().strip()
        self.runtimes = {name: v for name in ("python", "node", "git", "npm", "pnpm", "yarn", "sh", "bash") if (v := version(name))}
        names = ("ro.build.version.release", "ro.product.model", "ro.product.manufacturer", "ro.build.version.sdk", "ro.product.cpu.abilist")
        if guest:
            # getprop is an Android binary; the launcher passes the values.
            self.platform_info = {name: os.environ.get("TIDALBRIDGE_PROP_" + name.replace(".", "_"), "") for name in names}
        else:
            self.platform_info = {name: getprop(name) for name in names} if not mock else {}
        if guest and "python" not in self.runtimes and (v := version("python3")):
            self.runtimes["python"] = v
        if guest and (v := version("uv")):
            self.runtimes["uv"] = v  # probed once: every probe is a process start
        for name in ("sh", "bash"):
            if shutil.which(name):
                self.runtimes.setdefault(name, "1.0.0")
        self.debian = Debian(None if mock else os.environ.get("PREFIX"), userland)
        self.native = NativeNode(userland) if userland and not mock and not guest else None
        if self.native:
            self.native.warm = self.root / "warm"
        if self.debian_only:
            self.debian.versions()  # before serving, so capabilities are complete
        self.page = os.sysconf("SC_PAGE_SIZE") if hasattr(os, "sysconf") else 4096
        self.ticks = os.sysconf("SC_CLK_TCK") if hasattr(os, "sysconf") else 100
        for record in (self.root / "jobs").glob("*/record.json"):
            try:
                job = json.loads(record.read_text())
                if job["state"] in ("RUNNING", "QUEUED"):
                    job.update(state="INTERRUPTED", error="Worker restarted; attempt was not replayed.")
                    atomic(record, job)
            except (OSError, ValueError, KeyError):
                pass
        threading.Thread(target=self.collect_garbage, daemon=True).start()

    def toolchain(self, runtime: str) -> dict:
        """Tool versions jobs of this runtime see."""
        if (runtime == "debian" or self.debian_only) and not self.guest:
            return self.debian.versions()
        return dict(self.runtimes)

    def path_key(self, runtime: str) -> str:
        """Environment key for PATH additions visible to the job."""
        return "TIDALBRIDGE_GUEST_PATH" if runtime == "debian" and not self.guest and not self.mock else "PATH"

    # ---------------------------------------------------------------- status
    def resources(self) -> dict:
        total = available = 0
        try:
            mem = dict(re.findall(r"^(\w+):\s+(\d+)", pathlib.Path("/proc/meminfo").read_text(), re.M))
            total = int(mem["MemTotal"]) // 1024
            available = int(mem.get("MemAvailable", mem.get("MemFree", 0))) // 1024
        except (OSError, KeyError):
            if sys.platform == "win32":
                import ctypes
                class Memory(ctypes.Structure):
                    _fields_ = [("length", ctypes.c_ulong), ("load", ctypes.c_ulong)] + [(n, ctypes.c_ulonglong) for n in ("total", "available", "page", "freepage", "virtual", "freevirtual", "extended")]
                memory = Memory()
                memory.length = ctypes.sizeof(memory)
                ctypes.windll.kernel32.GlobalMemoryStatusEx(ctypes.byref(memory))
                total, available = memory.total >> 20, memory.available >> 20
        cpu = None
        if hasattr(os, "getloadavg"):
            try:
                cpu = min(100, os.getloadavg()[0] / max(1, os.cpu_count() or 1) * 100)
            except OSError:
                cpu = None
        result = dict(cpu_percent=cpu or 0, ram_total_mb=total, ram_available_mb=available, ram_physical_mb=physical_memory_mb(total), project_copies=len(list((self.root / "trees").glob("*.json"))),
                      storage_available_mb=shutil.disk_usage(self.root).free >> 20,
                      battery_percent=None, charging=None, thermal="unknown", logical_cores=os.cpu_count() or 1)
        if self.mock:
            size = {"low": (2048, 1100, 2), "mid": (6144, 4300, 6), "high": (12288, 8500, 8)}[self.mock]
            result.update(ram_total_mb=size[0], ram_physical_mb=size[0], ram_available_mb=size[1], logical_cores=size[2], battery_percent=80, charging=True, thermal="nominal")
            override = self.root / "mock-resources.json"
            if override.exists():
                result.update(json.loads(override.read_text()))
        return result

    def capabilities(self) -> dict:
        android = self.platform_info.get("ro.build.version.release", "") if not self.mock else "simulated"
        runtimes = dict(self.runtimes)
        debian = self.debian.available() or self.guest
        if debian:
            for name, value in self.toolchain("debian").items():
                runtimes["debian-" + name] = value
        if self.debian_only:
            # Every job runs in the userland, whatever runtime it names.
            runtimes = {name: value for name, value in runtimes.items() if name.startswith("debian-")}
            runtimes.update({name[len("debian-"):]: value for name, value in runtimes.items()})
            for name in ("sh", "debian-sh"):
                runtimes.setdefault(name, "1.0.0")
        with self.lock:
            services = sum(1 for j in self.jobs.values() if j.get("service") and j["state"] in ("QUEUED", "RUNNING"))
        return dict(protocol_version=PROTOCOL, worker_version=VERSION, stable_id=self.identity,
                    model=("Mock " + self.mock.title()) if self.mock else self.platform_info.get("ro.product.model") or platform.node(),
                    manufacturer="Simulated" if self.mock else self.platform_info.get("ro.product.manufacturer", ""),
                    android_version=android, api_level=int(self.platform_info.get("ro.build.version.sdk") or 0),
                    architecture="arm64" if self.mock else {"aarch64": "arm64", "AMD64": "x86_64"}.get(platform.machine(), platform.machine()),
                    abis=self.platform_info.get("ro.product.cpu.abilist", "").split(",") if android and not self.mock else ["simulated-arm64"],
                    runtimes=runtimes,
                    features=dict(shell_exec=True, workspace_sync=True, browser_qa=False, network_isolation=False,
                                  workspace_trees=True, services=True, runtime_debian=debian,
                                  engine_native=bool(self.native and debian and self.native.ready())),
                    resources=self.resources(), simulated=bool(self.mock), active_services=services)

    def calibration(self) -> dict:
        started = time.perf_counter()
        sum(i * i for i in range(200000))
        python_ms = (time.perf_counter() - started) * 1000
        block = bytes(1024 * 1024)
        started = time.perf_counter()
        for _ in range(8):
            hashlib.sha256(block).digest()
        hash_mbps = 8 / max(0.000001, time.perf_counter() - started)
        node_ms = 0
        bench = ["node", "-e", "let s=0;for(let i=0;i<200000;i++)s+=i*i"]
        if "node" in self.runtimes:
            started = time.perf_counter()
            subprocess.run(bench, timeout=5, capture_output=True)
            node_ms = (time.perf_counter() - started) * 1000
        elif self.debian_only and self.debian.available():
            started = time.perf_counter()
            subprocess.run(self.debian.argv(bench, pathlib.Path("/"), {}, []), env=self.debian.host_env(dict(os.environ)), timeout=30, capture_output=True)
            node_ms = (time.perf_counter() - started) * 1000
        fingerprint = hashlib.sha256(json.dumps([VERSION, self.runtimes, getprop("ro.build.version.release")], sort_keys=True).encode()).hexdigest()
        return dict(fingerprint=fingerprint, timestamp=datetime.now(timezone.utc).isoformat(), python_ms=python_ms, node_ms=node_ms, hash_mbps=hash_mbps, rtt_ms=0, transfer_mbps=0)

    # ------------------------------------------------------------------ sync
    @staticmethod
    def validate(manifest: dict, nested: bool = False) -> None:
        manifest["files"] = manifest.get("files") or []  # an empty workspace may arrive as null
        if not ID.fullmatch(manifest["id"]) or len(manifest["files"]) > 100000:
            raise ValueError("invalid manifest identity or size")
        for entry in manifest["files"]:
            # A nested copy also holds files from one folder up ("../...").
            path = entry["path"][3:] if nested and entry["path"].startswith("../") else entry["path"]
            if not safe_path(path) or not HASH.fullmatch(entry["hash"]) or entry["size"] < 0:
                raise ValueError("invalid file entry")

    def missing(self, manifest: dict) -> list[str]:
        self.validate(manifest, nested=True)  # only the content hashes are used here
        blobs = self.root / "cache/blobs"
        return sorted({entry["hash"] for entry in manifest["files"] if not (blobs / entry["hash"]).is_file()})

    def tree_lock(self, key: str) -> threading.Lock:
        with self.lock:
            return self.tree_locks.setdefault(key, threading.Lock())

    def tree_changes(self, key: str) -> dict:
        """Synced files a write-back job changed or deleted, and files it
        created outside dependency and cache directories. Their content enters
        the blob cache, so the next sync does not upload it again."""
        tree = self.tree(key)
        state_path = self.root / "trees" / (key + ".json")
        synced = json.loads(state_path.read_text()) if state_path.exists() else {}
        blobs = self.root / "cache/blobs"
        blobs.mkdir(parents=True, exist_ok=True)
        changes: dict[str, list] = {"modified": [], "created": [], "deleted": []}
        total = 0

        def entry(path: str, file: pathlib.Path) -> dict:
            nonlocal total
            info = file.stat()
            total += info.st_size
            if total > WRITE_BACK_BYTES or len(changes["modified"]) + len(changes["created"]) >= WRITE_BACK_FILES:
                raise ValueError("the job changed too many files to copy back")
            digest = file_hash(file)
            blob = blobs / digest
            if not blob.exists():
                temporary = blobs / f"{digest}.{secrets.token_hex(4)}.tmp"
                shutil.copyfile(file, temporary)
                os.replace(temporary, blob)
            return {"path": path, "hash": digest, "size": info.st_size, "executable": bool(info.st_mode & 0o100)}

        for path, (digest, _executable, size, mtime) in synced.items():
            if path.startswith("../"):
                continue  # read from beside the project, never written back
            file = tree / path
            try:
                info = file.lstat()
            except FileNotFoundError:
                changes["deleted"].append(path)
                continue
            if not stat.S_ISREG(info.st_mode):
                raise ValueError(f"the job replaced {path} with a link or directory")
            if info.st_size == size and info.st_mtime_ns == mtime:
                continue
            item = entry(path, file)
            if item["hash"] != digest:
                changes["modified"].append(item)
        for directory, dirnames, filenames in os.walk(tree):
            dirnames[:] = [d for d in dirnames if d not in SKIP_CHANGE_DIRS]
            relative = pathlib.Path(directory).relative_to(tree)
            for name in filenames:
                path = (relative / name).as_posix()
                file = pathlib.Path(directory) / name
                if path in synced or name.endswith(".tidalbridge-tmp") or file.is_symlink() or not file.is_file():
                    continue
                changes["created"].append(entry(path, file))
        return {kind: items for kind, items in changes.items() if items}

    def tree(self, key: str) -> pathlib.Path:
        if not ID.fullmatch(key):
            raise ValueError("invalid workspace key")
        base = self.root / "trees" / key
        nest = self.nests.get(key)
        if nest is None:
            marker = self.root / "trees" / (key + ".nest")
            nest = self.nests[key] = marker.read_text().strip() if marker.exists() else ""
        return base / nest if nest else base

    def update_tree(self, key: str, manifest: dict, nest: str = "") -> dict:
        """Bring a persistent tree to the manifest: write changed files, remove
        files this tree received earlier and that left the manifest. Files a
        job created (dependencies, caches, build output) are never touched.
        A nested tree holds the project in folder `nest`, beside the files it
        reads from one folder up."""
        base = self.root / "trees" / key
        if nest:
            if not ID.fullmatch(key) or not ID.fullmatch(nest):
                raise ValueError("invalid project folder name")
            base.mkdir(parents=True, exist_ok=True)
            marker = self.root / "trees" / (key + ".nest")
            if not marker.exists():
                marker.write_text(nest)
            self.nests[key] = nest
        place = (lambda p: p[3:] if p.startswith("../") else f"{nest}/{p}") if nest else (lambda p: p)
        tree = self.tree(key)
        state_path = self.root / "trees" / (key + ".json")
        written = removed = 0
        with self.tree_lock(key):
            previous = json.loads(state_path.read_text()) if state_path.exists() else {}
            current = {}
            tree.mkdir(parents=True, exist_ok=True)
            confined = Confined(base)
            for entry in manifest["files"]:
                path, digest, executable = entry["path"], entry["hash"], bool(entry.get("executable"))
                target = confined(place(path))
                old = previous.get(path)
                try:
                    stat = target.stat()
                except OSError:
                    stat = None
                if old and old[0] == digest and stat and stat.st_size == old[2] and stat.st_mtime_ns == old[3]:
                    current[path] = old
                    continue
                target.parent.mkdir(parents=True, exist_ok=True)
                temporary = target.with_name(target.name + ".tidalbridge-tmp")
                try:
                    source = open(self.root / "cache/blobs" / digest, "rb")
                except FileNotFoundError:
                    # The host retries after re-sending content it assumed present.
                    raise ValueError("manifest refers to missing content") from None
                with source:
                    # Never follow a link a job may have left at this name.
                    temporary.unlink(missing_ok=True)
                    with os.fdopen(os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_BINARY", 0), 0o644), "wb") as copy:
                        shutil.copyfileobj(source, copy, 1 << 20)
                if executable:
                    temporary.chmod(0o700)
                os.replace(temporary, target)
                stat = target.stat()
                current[path] = [digest, executable, stat.st_size, stat.st_mtime_ns]
                written += 1
            for path in previous.keys() - current.keys():
                try:
                    target = confined(place(path))
                    target.unlink()
                    removed += 1
                    parent = target.parent
                    while parent != base and parent != tree and not any(parent.iterdir()):
                        parent.rmdir()
                        parent = parent.parent
                except (OSError, ValueError):
                    pass
            atomic(state_path, current)
        return {"written": written, "removed": removed}

    def apply_manifest(self, manifest: dict, workspace_key: str | None = None, nest: str = "") -> dict:
        # A tree only reads the content of files that changed, and reports
        # absent content itself; checking every blob first doubled the cost.
        if workspace_key:
            self.validate(manifest, bool(nest))
        elif self.missing(manifest):
            raise ValueError("manifest refers to missing content")
        with self.sync_lock:
            directory = self.root / "workspaces" / manifest["id"]
            directory.mkdir(exist_ok=True)
            atomic(directory / "manifest.json", manifest)
        result = {"workspace_id": manifest["id"], "files": len(manifest["files"])}
        if workspace_key:
            result.update(self.update_tree(workspace_key, manifest, nest))
        return result

    def materialize(self, workspace_id: str, dest: pathlib.Path) -> None:
        if not workspace_id:
            dest.mkdir()
            return
        if not ID.fullmatch(workspace_id):
            raise ValueError("invalid workspace identity")
        with self.sync_lock:
            manifest = json.loads((self.root / "workspaces" / workspace_id / "manifest.json").read_text())
        dest.mkdir()
        confined = Confined(dest)
        for entry in manifest["files"]:
            target = confined(entry["path"])
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(self.root / "cache/blobs" / entry["hash"], target)
            if entry.get("executable"):
                target.chmod(0o700)

    # ------------------------------------------------------------------ jobs
    def save(self, job: dict) -> None:
        atomic(self.root / "jobs" / job["id"] / "record.json", job)

    def submit(self, request: dict) -> dict:
        jid = request["id"]
        spec = request["spec"]
        if not ID.fullmatch(jid) or spec.get("protocol_version") != PROTOCOL:
            raise ValueError("invalid job ID/protocol")
        if not spec.get("argv") or not all(isinstance(a, str) for a in spec["argv"]):
            raise ValueError("argv must be a nonempty list of strings")
        if not 1 <= spec.get("timeout_seconds", 0) <= 86400:
            raise ValueError("invalid timeout")
        if spec.get("policy", {}).get("network") == "deny":
            raise ValueError("strict network isolation is unsupported")
        runtime = spec.get("runtime") or "termux"
        if runtime not in ("termux", "debian"):
            raise ValueError("unsupported runtime")
        if self.debian_only:
            runtime = "debian"
        if runtime == "debian" and not self.debian.available() and not self.mock and not self.guest:
            raise ValueError("the debian runtime is not installed on this worker")
        if spec.get("workspace_key") and not ID.fullmatch(spec["workspace_key"]):
            raise ValueError("invalid workspace key")
        service = bool(spec.get("service"))
        with self.lock:
            if jid in self.jobs:
                return dict(self.jobs[jid])
            record_path = self.root / "jobs" / jid / "record.json"
            if record_path.exists():
                return json.loads(record_path.read_text())
            active = [j for j in self.jobs.values() if j["state"] in ("QUEUED", "RUNNING")]
            if service and sum(1 for j in active if j.get("service")) >= MAX_SERVICES:
                raise OverflowError("worker service capacity is occupied")
            if not service and sum(1 for j in active if not j.get("service")) >= self.capacity:
                raise OverflowError("worker capacity is occupied")
            if len(self.jobs) >= 128:
                for key in list(self.jobs):
                    if self.jobs[key]["state"] not in ("QUEUED", "RUNNING"):
                        del self.jobs[key]
                        self.cancelled.discard(key)
                        if len(self.jobs) < 96:
                            break
            # The host maps this prefix in job output back to its own path.
            workspace_path = str(self.tree(spec["workspace_key"]) if spec.get("workspace_key") else self.root / "jobs" / jid / "workspace")
            job = dict(id=jid, state="QUEUED", spec=spec, exit_code=None, error="", duration_ms=0, service=service, ports=[], runtime=runtime, workspace_path=workspace_path)
            self.jobs[jid] = job
            self.save(job)
            if service:
                threading.Thread(target=self.execute, args=(jid,), daemon=True).start()
            else:
                self.pool.submit(self.execute, jid)
            return dict(job)

    def session_processes(self, sid: int) -> list[int]:
        pids = []
        try:
            entries = os.listdir("/proc")
        except OSError:
            return pids
        for name in entries:
            if not name.isdigit():
                continue
            try:
                with open(f"/proc/{name}/stat", "rb") as stream:
                    stat = stream.read()
                fields = stat[stat.rfind(b")") + 2:].split()
                if int(fields[3]) == sid:
                    pids.append(int(name))
            except (OSError, ValueError, IndexError):
                continue
        return pids

    def stop_process(self, proc: subprocess.Popen) -> None:
        try:
            if os.name == "nt":
                subprocess.run(["taskkill", "/PID", str(proc.pid), "/T", "/F"], capture_output=True, timeout=5)
                return
            # Kill the whole session: tools may move children into new
            # process groups (worker pools, detached helpers, proot tracees).
            for pid in self.session_processes(proc.pid):
                try:
                    os.kill(pid, signal.SIGKILL)
                except OSError:
                    pass
            os.killpg(proc.pid, signal.SIGKILL)
        except (OSError, subprocess.SubprocessError):
            try:
                proc.kill()
            except OSError:
                pass

    def cancel(self, jid: str) -> None:
        with self.lock:
            self.cancelled.add(jid)
            proc = self.processes.get(jid)
        if proc:
            self.stop_process(proc)

    def sample(self, jid: str, proc: subprocess.Popen, stop: threading.Event) -> None:
        """Peak RSS of the whole session and its accumulated CPU time."""
        cpu_by_pid: dict[int, int] = {}
        while not stop.is_set():
            if os.name == "nt":
                break
            rss = 0
            for pid in self.session_processes(proc.pid):
                try:
                    with open(f"/proc/{pid}/stat", "rb") as stream:
                        stat = stream.read()
                    fields = stat[stat.rfind(b")") + 2:].split()
                    cpu_by_pid[pid] = max(cpu_by_pid.get(pid, 0), int(fields[11]) + int(fields[12]))
                    rss += int(fields[21]) * self.page
                except (OSError, ValueError, IndexError):
                    continue
            with self.lock:
                job = self.jobs.get(jid)
                if job is not None:
                    job["peak_ram_mb"] = max(job.get("peak_ram_mb") or 0, rss / (1 << 20))
                    job["ram_mb"] = rss / (1 << 20)
                    job["cpu_seconds"] = round(sum(cpu_by_pid.values()) / self.ticks, 3)
            stop.wait(0.5)

    def run_process(self, jid: str, argv: list[str], cwd: pathlib.Path, env: dict, timeout: float | None, prefix: str = "", runtime: str = "termux", binds: list | None = None) -> int:
        directory = self.root / "jobs" / jid
        directory.mkdir(parents=True, exist_ok=True)
        if jid in self.cancelled:
            raise InterruptedError("Job cancelled")
        process_env, preexec = env, None
        if runtime == "native":
            process_env, preexec = self.native.env(env), all_cores
        elif runtime == "debian" and not self.mock and not self.guest:
            argv = self.debian.argv(argv, cwd, env, binds or [self.root])
            process_env = self.debian.host_env({k: v for k, v in os.environ.items() if k in ("PATH", "HOME", "PREFIX", "TMPDIR", "LANG", "LD_LIBRARY_PATH", "ANDROID_ROOT", "ANDROID_DATA")})
        creation = {"start_new_session": True} if os.name != "nt" else {"creationflags": subprocess.CREATE_NEW_PROCESS_GROUP}
        if preexec:
            creation["preexec_fn"] = preexec
        proc = subprocess.Popen(argv, cwd=cwd, env=process_env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **creation)
        with self.lock:
            self.processes[jid] = proc
        def pump(source, target):
            tail = b""
            with target.open("ab") as out:
                while chunk := source.read1(16384) if hasattr(source, "read1") else source.read(16384):
                    if out.tell() + len(chunk) > MAX_LOG:
                        self.stop_process(proc)
                        break
                    out.write(chunk)
                    out.flush()
                    window = tail + chunk
                    ports = {int(m) for m in PORT_URL.findall(window) if 1024 <= int(m) <= 65535}
                    if ports:
                        with self.lock:
                            job = self.jobs.get(jid)
                            if job is not None:
                                job["ports"] = sorted(set(job.get("ports", [])) | ports)
                    tail = window[-64:]
            source.close()
        threads = [threading.Thread(target=pump, args=(proc.stdout, directory / "stdout.log"), daemon=True),
                   threading.Thread(target=pump, args=(proc.stderr, directory / "stderr.log"), daemon=True)]
        for thread in threads:
            thread.start()
        monitor_stop = threading.Event()
        monitor = threading.Thread(target=self.sample, args=(jid, proc, monitor_stop), daemon=True)
        monitor.start()
        try:
            code = self.wait_for(proc, timeout, [directory / "stdout.log", directory / "stderr.log"])
        finally:
            monitor_stop.set()
            monitor.join(timeout=1)
            for thread in threads:
                thread.join(timeout=5)
            with self.lock:
                self.processes.pop(jid, None)
        if jid in self.cancelled:
            raise InterruptedError("Job cancelled")
        return code

    def wait_for(self, proc: subprocess.Popen, timeout: float | None, logs: list[pathlib.Path]) -> int:
        if timeout is None:
            return proc.wait()
        start, limit = time.monotonic(), timeout
        while True:
            try:
                return proc.wait(timeout=max(0.01, start + limit - time.monotonic()))
            except subprocess.TimeoutExpired:
                pass
            elapsed = time.monotonic() - start
            silent = time.time() - max((p.stat().st_mtime for p in logs if p.exists()), default=0.0)
            if silent >= SILENT_LIMIT or elapsed >= timeout * OVERTIME:
                self.stop_process(proc)
                proc.wait()
                raise TimeoutError("Job timed out" if silent >= SILENT_LIMIT else "Job timed out at three times its limit")
            limit = elapsed + max(0.05, min(30.0, SILENT_LIMIT - silent, timeout * OVERTIME - elapsed))

    # ---------------------------------------------------------- environments
    def environment_key(self, hashes: dict) -> str:
        allowed = {"requirements.txt", "pyproject.toml", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock"}
        if not isinstance(hashes, dict) or not hashes or any(name not in allowed or not isinstance(value, str) or not HASH.fullmatch(value) for name, value in hashes.items()):
            raise ValueError("invalid dependency manifest")
        return hashlib.sha256(json.dumps([self.runtimes, platform.machine(), VERSION, sorted(hashes.items())], sort_keys=True).encode()).hexdigest()

    def tree_dependency_key(self, directory: pathlib.Path, runtime: str, wanted: str = "node") -> tuple[str, str] | None:
        """(kind, key) for the dependency inputs in a tree directory."""
        node = (directory / "package-lock.json").is_file() or (directory / "pnpm-lock.yaml").is_file() or (directory / "yarn.lock").is_file()
        python = (directory / "uv.lock").is_file() or (directory / "requirements.txt").is_file()
        if node and not (wanted == "python" and python):
            names = [n for n in ("package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock") if (directory / n).is_file()]
            kind = "node"
        elif (directory / "uv.lock").is_file():
            names, kind = ["pyproject.toml", "uv.lock"], "uv"
        elif (directory / "requirements.txt").is_file():
            names, kind = ["requirements.txt"], "pip"
        else:
            return None
        versions = self.toolchain(runtime)
        digest = hashlib.sha256(json.dumps([kind, runtime, VERSION, versions.get("node"), versions.get("python"), [(n, file_hash(directory / n)) for n in names if (directory / n).is_file()]]).encode()).hexdigest()
        return kind, digest

    def tree_dependency_status(self, key: str, working_directory: str, runtime: str, wanted: str = "node") -> dict:
        tree = self.tree(key)
        directory = within(tree, working_directory) if working_directory else tree
        found = self.tree_dependency_key(directory, runtime, wanted) if directory.is_dir() else None
        if not found:
            return {"ready": True, "kind": None}
        kind, digest = found
        marker = directory / ".tidalbridge-deps" / marker_name(runtime, kind)
        try:
            ready = json.loads(marker.read_text()).get("key") == digest
        except (OSError, ValueError):
            ready = False
        return {"ready": ready, "kind": kind}

    def environment_status(self, body: dict) -> dict:
        if body.get("workspace_key"):
            runtime = "debian" if self.debian_only else body.get("runtime") or "termux"
            wanted = "python" if body.get("ecosystem") == "python" else "node"
            return self.tree_dependency_status(body["workspace_key"], body.get("working_directory", ""), runtime, wanted)
        key = self.environment_key(body.get("dependency_hashes", {}))
        return {"key": key, "ready": (self.root / "environments" / key / "ready").is_file()}

    def prepare_tree(self, jid: str, directory: pathlib.Path, env: dict, deadline: float | None, runtime: str, scripts: bool, wanted: str = "node") -> dict:
        """Install locked dependencies inside a persistent tree when their
        inputs changed. Returns environment additions for the job."""
        found = self.tree_dependency_key(directory, runtime, wanted)
        additions: dict = {}
        if not found:
            return additions
        kind, digest = found
        marker = directory / ".tidalbridge-deps" / marker_name(runtime, kind)
        try:
            ready = json.loads(marker.read_text()).get("key") == digest
        except (OSError, ValueError):
            ready = False
        remaining = None if deadline is None else max(0.01, deadline - time.perf_counter())
        log = self.root / "jobs" / jid / "stderr.log"
        if kind == "node":
            if not ready:
                with log.open("ab") as out:
                    out.write(b"[Tidal Bridge] installing locked Node dependencies on the worker (first run or lockfile change)\n")
                if (directory / "package-lock.json").is_file():
                    command = ["npm", "ci", "--no-audit", "--no-fund", "--prefer-offline"]
                elif (directory / "pnpm-lock.yaml").is_file():
                    command = ["npx", "--yes", "pnpm", "install", "--frozen-lockfile"]
                else:
                    command = ["npx", "--yes", "yarn", "install", "--frozen-lockfile"]
                if not scripts:
                    command.append("--ignore-scripts")
                if self.run_process(jid, command, directory, env, remaining, runtime=runtime):
                    raise RuntimeError("Node dependency installation failed; inspect job stderr")
            bin_dir = directory / "node_modules" / ".bin"
            additions[self.path_key(runtime)] = str(bin_dir)
        elif kind == "uv":
            if not ready:
                with log.open("ab") as out:
                    out.write(b"[Tidal Bridge] creating the locked Python environment on the worker (first run or lockfile change)\n")
                if self.run_process(jid, ["uv", "sync", "--frozen"], directory, dict(env, UV_LINK_MODE="copy"), remaining, runtime=runtime):
                    raise RuntimeError("uv sync failed; inspect job stderr")
            additions["VIRTUAL_ENV"] = str(directory / ".venv")
            additions[self.path_key(runtime)] = str(directory / ".venv" / "bin")
        elif kind == "pip":
            venv = directory / ".venv"
            python = "python3" if runtime == "debian" else sys.executable
            bindir = "Scripts" if os.name == "nt" and runtime != "debian" else "bin"
            if not ready:
                with log.open("ab") as out:
                    out.write(b"[Tidal Bridge] creating the Python environment on the worker (first run or requirements change)\n")
                if self.run_process(jid, [python, "-m", "venv", str(venv)], directory, env, remaining, runtime=runtime):
                    raise RuntimeError("Python virtual environment creation failed")
                if self.run_process(jid, [str(venv / bindir / "python"), "-m", "pip", "install", "-r", "requirements.txt"], directory, env, remaining, runtime=runtime):
                    raise RuntimeError("Python dependency installation failed; inspect job stderr")
            additions["VIRTUAL_ENV"] = str(venv)
            additions[self.path_key(runtime)] = str(venv / bindir)
        if not ready:
            atomic(marker, {"key": digest, "kind": kind, "runtime": runtime, "prepared": datetime.now(timezone.utc).isoformat()})
        return additions

    def provision(self, jid: str, workspace: pathlib.Path, env: dict, remaining: float) -> dict:
        lockfiles = [name for name in ("requirements.txt", "package-lock.json", "pnpm-lock.yaml", "yarn.lock") if (workspace / name).is_file()]
        if (workspace / "pyproject.toml").is_file() and "requirements.txt" not in lockfiles:
            lockfiles.append("pyproject.toml")
        if not lockfiles:
            raise ValueError("Provisioning requires a supported pinned dependency/lock file")
        hashes = {name: hashlib.sha256((workspace / name).read_bytes()).hexdigest() for name in ("requirements.txt", "pyproject.toml", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock") if (workspace / name).is_file()}
        key = self.environment_key(hashes)
        cache = self.root / "environments" / key
        with self.env_lock:
            if not (cache / "ready").exists():
                cache.mkdir(exist_ok=True)
                if "requirements.txt" in lockfiles or "pyproject.toml" in lockfiles:
                    if "requirements.txt" in lockfiles:
                        lines = (workspace / "requirements.txt").read_text().splitlines()
                    else:
                        import tomllib
                        data = tomllib.loads((workspace / "pyproject.toml").read_text())
                        lines = data.get("project", {}).get("dependencies", [])
                        if not isinstance(lines, list) or any(not isinstance(line, str) for line in lines):
                            raise ValueError("PEP 621 project.dependencies must be a list of pinned requirements")
                    if any(line.strip() and not line.lstrip().startswith("#") and not re.fullmatch(r"[A-Za-z0-9_.-]+(?:\[[A-Za-z0-9_,.-]+\])?==[A-Za-z0-9_.+!-]+(?:\s*;[^\r\n]+)?", line.strip()) for line in lines):
                        raise ValueError("Automatic Python provisioning requires pinned == requirements")
                    if self.run_process(jid, [sys.executable, "-m", "venv", str(cache / "venv")], workspace, env, remaining):
                        raise RuntimeError("Python virtual environment creation failed; inspect job stderr")
                    python = cache / "venv" / ("Scripts/python.exe" if os.name == "nt" else "bin/python")
                    pinned = cache / "requirements.txt"
                    pinned.write_text("\n".join(lines) + "\n")
                    if self.run_process(jid, [str(python), "-m", "pip", "install", "--only-binary=:all:", "-r", str(pinned)], workspace, env, remaining):
                        raise RuntimeError("Python dependency provisioning failed; inspect job stderr")
                if any(name in lockfiles for name in ("package-lock.json", "pnpm-lock.yaml", "yarn.lock")):
                    shutil.copyfile(workspace / "package.json", cache / "package.json")
                    for name in lockfiles:
                        if name in ("package-lock.json", "pnpm-lock.yaml", "yarn.lock"):
                            shutil.copyfile(workspace / name, cache / name)
                    if "package-lock.json" in lockfiles:
                        command = ["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"]
                    elif "pnpm-lock.yaml" in lockfiles:
                        command = ["pnpm", "install", "--frozen-lockfile", "--ignore-scripts"]
                    else:
                        command = ["yarn", "install", "--frozen-lockfile", "--ignore-scripts"]
                    if self.run_process(jid, command, cache, env, remaining):
                        raise RuntimeError("Node dependency provisioning failed; inspect job stderr")
                (cache / "ready").touch()
        result = dict(env)
        if (cache / "venv").exists():
            result["VIRTUAL_ENV"] = str(cache / "venv")
            result["PATH"] = str(cache / "venv" / ("Scripts" if os.name == "nt" else "bin")) + os.pathsep + env.get("PATH", "")
        if (cache / "node_modules").exists():
            os.symlink(cache / "node_modules", workspace / "node_modules", target_is_directory=True)
            result["PATH"] = str(cache / "node_modules/.bin") + os.pathsep + result.get("PATH", "")
        return result

    # ------------------------------------------------------------- execution
    def execute(self, jid: str) -> None:
        job = self.jobs[jid]
        started = time.perf_counter()
        spec = job["spec"]
        runtime = job.get("runtime", "termux")
        try:
            with self.lock:
                job["state"] = "RUNNING"
                self.save(job)
            key = spec.get("workspace_key")
            if key:
                # Persistent tree: the host applied the manifest before submit.
                workspace = self.tree(key)
                workspace.mkdir(parents=True, exist_ok=True)
            else:
                workspace = self.root / "jobs" / jid / "workspace"
                self.materialize(spec.get("workspace_id", ""), workspace)
            cwd = within(workspace, spec["working_directory"]) if spec.get("working_directory") else workspace
            # Do not inherit host credentials into mocks or worker session tokens into child jobs.
            env = {key: value for key, value in os.environ.items() if key in ("PATH", "HOME", "USER", "TMP", "TEMP", "TMPDIR", "SYSTEMROOT", "WINDIR", "PREFIX", "LANG", "LD_LIBRARY_PATH", "LD_PRELOAD", "ANDROID_ROOT", "ANDROID_DATA")}
            for name, value in spec.get("env", {}).items():
                if re.search(r"TOKEN|SECRET|PASSWORD|PRIVATE_KEY|CREDENTIAL|API_KEY", name, re.I):
                    raise ValueError("credential environment variables are excluded")
                env[name] = value
            env["TIDALBRIDGE_WORKER"] = "1"
            defaulted_workers = "VITEST_MAX_WORKERS" not in env
            if runtime == "debian" and not self.guest and not self.mock:
                # proot traces every file-system call of all of a job's
                # processes in one thread, so test runners that start a worker
                # per core time out while starting (vitest allows 60 s). A
                # project or caller setting decides when given.
                env.setdefault("VITEST_MAX_WORKERS", str(PROOT_TEST_WORKERS))
            deadline = None if job.get("service") else started + spec["timeout_seconds"]
            policy = spec.get("policy", {})
            if policy.get("provision"):
                if key:
                    for name, value in self.prepare_tree(jid, cwd, env, deadline, runtime, bool(policy.get("provision_scripts")), ecosystem(spec)).items():
                        separator = os.pathsep if name == "PATH" else ":"
                        if name.endswith("PATH"):
                            env[name] = value + (separator + env[name] if env.get(name) else "")
                        else:
                            env[name] = value
                else:
                    env = self.provision(jid, workspace, env, max(0.01, deadline - time.perf_counter()))
            elif key and (runtime == "debian" or self.guest):
                # Locked dependencies already present in the tree stay usable.
                path_key = self.path_key(runtime)
                for name in ("node_modules/.bin", ".venv/bin"):
                    if (cwd / name).is_dir():
                        env[path_key] = str(cwd / name) + (":" + env[path_key] if env.get(path_key) else "")
            argv = list(spec["argv"])
            if runtime == "termux" and argv[0] == "python" and not shutil.which("python", path=env.get("PATH")):
                argv[0] = sys.executable
            if (runtime == "debian" or self.guest) and argv[0] == "python" and not shutil.which("python", path=env.get("PATH")):
                argv[0] = "python3"
            remaining = None if deadline is None else max(0.01, deadline - time.perf_counter())
            engine = runtime
            if runtime == "debian" and spec.get("engine") == "native" and self.native and self.native.ready():
                native = self.native.command(argv, cwd, env)
                if native:
                    argv, engine = native, "native"
                    if defaulted_workers:
                        # The proot limit is not needed; the project's own
                        # worker settings apply.
                        env.pop("VITEST_MAX_WORKERS", None)
            job["engine"] = engine
            ports = {p for p in spec.get("reverse_ports") or [] if isinstance(p, int) and 0 < p < 65536}
            with ServiceWatch(ports) as services:
                code = self.run_process(jid, argv, cwd, env, remaining, runtime=engine)
            if ports:
                job["services_used"] = services.used
            if policy.get("write_back") and key and jid not in self.cancelled:
                # Listed before the exit code: the host reads them together.
                job["changes"] = self.tree_changes(key)
            job["exit_code"] = code
            job["state"] = "COMPLETED" if code == 0 else "FAILED"
            for output in spec.get("expected_outputs", []):
                path = within(workspace, output)
                if path.exists() and not path.is_file():
                    raise ValueError("declared outputs must be regular files")
        except Exception as exc:
            job["state"] = "CANCELLED" if jid in self.cancelled else "FAILED"
            job["error"] = str(exc)
        finally:
            with self.lock:
                job["duration_ms"] = (time.perf_counter() - started) * 1000
                self.save(job)

    def artifact_root(self, record: dict, jid: str) -> pathlib.Path:
        key = record["spec"].get("workspace_key")
        return self.tree(key) if key else self.root / "jobs" / jid / "workspace"

    def record(self, jid: str) -> dict:
        if not ID.fullmatch(jid):
            raise ValueError("invalid job ID")
        with self.lock:
            if jid in self.jobs:
                return dict(self.jobs[jid])
        return json.loads((self.root / "jobs" / jid / "record.json").read_text())

    def collect_garbage(self) -> None:
        while True:
            self.prune()
            time.sleep(3600)

    def storage_floor_mb(self) -> int:
        """Free storage the phone keeps for its owner: 5%, at least 3 GB."""
        return max(3072, shutil.disk_usage(self.root).total // 20 >> 20)

    def prune(self, now: float | None = None) -> list[str]:
        """Keep the newest job directories, and remove project copies and
        cached content nothing has used for two weeks (sooner, least recently
        used first, when storage runs low). Never anything a queued or running
        job uses. Returns the project copies removed."""
        now = now or time.time()
        with self.lock:
            active = [j for j in self.jobs.values() if j["state"] in ("QUEUED", "RUNNING")]
        jobs, keys = {j["id"] for j in active}, {j["spec"].get("workspace_key") for j in active}
        removed = []
        try:
            directories = sorted((p for p in (self.root / "jobs").iterdir() if p.is_dir()), key=lambda p: p.stat().st_mtime)
            for path in directories[:-KEEP_JOB_DIRS]:
                if path.name not in jobs:
                    shutil.rmtree(path, ignore_errors=True)
            # A copy's state file is rewritten each time the laptop syncs it.
            copies = sorted((state.stat().st_mtime, state.stem) for state in (self.root / "trees").glob("*.json") if ID.fullmatch(state.stem) and state.stem not in keys)
            for used, key in copies:
                low = shutil.disk_usage(self.root).free >> 20 < self.storage_floor_mb()
                if (now - used > IDLE_SECONDS or low and now - used > 3600) and self.remove_tree(key):
                    removed.append(key)
            self.prune_cache(now, {j["spec"].get("workspace_id") for j in active})
            self.prune_warm(now)
        except OSError:
            pass
        return removed

    def prune_warm(self, now: float) -> None:
        """Incremental state unused for two weeks goes; Node's code cache is
        dropped whole past 512 MB and refills on the next runs."""
        warm = self.root / "warm"
        for state in (warm / "tsc").glob("*.tsbuildinfo"):
            try:  # another prune or a running check may remove it meanwhile
                if now - state.stat().st_mtime > IDLE_SECONDS:
                    state.unlink(missing_ok=True)
            except OSError:
                continue
        cache = warm / "node"
        size = 0
        for item in cache.rglob("*") if cache.is_dir() else ():
            try:
                size += item.stat().st_size if item.is_file() else 0
            except OSError:
                continue
        if size > 512 << 20:
            shutil.rmtree(cache, ignore_errors=True)

    def remove_tree(self, key: str) -> bool:
        """Move a project copy aside under its lock, then delete it: a sync
        that follows starts from an empty copy, never from a half-deleted one."""
        trash = self.root / "trash"
        trash.mkdir(exist_ok=True)
        with self.tree_lock(key):
            with self.lock:
                if any(j["spec"].get("workspace_key") == key and j["state"] in ("QUEUED", "RUNNING") for j in self.jobs.values()):
                    return False
            (self.root / "trees" / (key + ".json")).unlink(missing_ok=True)
            (self.root / "trees" / (key + ".nest")).unlink(missing_ok=True)
            self.nests.pop(key, None)
            base = self.root / "trees" / key
            if base.exists():
                os.rename(base, trash / f"{key}.{secrets.token_hex(4)}")
        shutil.rmtree(trash, ignore_errors=True)
        return True

    def prune_cache(self, now: float, busy: set) -> None:
        """Remove sync records and cached file content that no project copy
        or recent sync refers to; content the laptop needs again, it sends
        again."""
        workspaces = self.root / "workspaces"
        for directory in workspaces.iterdir():
            if directory.name not in busy and now - directory.stat().st_mtime > IDLE_SECONDS:
                shutil.rmtree(directory, ignore_errors=True)
        referenced = set()
        try:
            for state in (self.root / "trees").glob("*.json"):
                referenced.update(entry[0] for entry in json.loads(state.read_text()).values())
            for manifest in workspaces.glob("*/manifest.json"):
                referenced.update(entry["hash"] for entry in json.loads(manifest.read_text())["files"])
        except (ValueError, KeyError, TypeError, IndexError):
            return  # unsure what is in use: keep everything
        low = shutil.disk_usage(self.root).free >> 20 < self.storage_floor_mb()
        for blob in (self.root / "cache/blobs").iterdir():
            if blob.name not in referenced:
                age = now - blob.stat().st_mtime
                if age > IDLE_SECONDS or low and age > 3600:
                    blob.unlink(missing_ok=True)


class Handler(BaseHTTPRequestHandler):
    server_version = "TidalBridge/0.2"

    def log_message(self, *_):
        pass

    @property
    def worker(self) -> Worker:
        return self.server.worker

    def respond(self, status: int, value: object):
        data = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def body(self) -> dict:
        size = int(self.headers.get("Content-Length", "0"))
        if not 0 < size <= MAX_JSON:
            raise ValueError("request body size is invalid")
        return json.loads(self.rfile.read(size))

    def authorized(self) -> bool:
        return hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + self.worker.token)

    def do_GET(self):
        self.dispatch()

    def do_POST(self):
        self.dispatch()

    def do_PUT(self):
        self.dispatch()

    def dispatch(self):
        if not self.authorized():
            self.respond(401, {"error": "unauthorized"})
            return
        parts = urlsplit(self.path)
        path = unquote(parts.path)
        try:
            if self.command == "GET" and path in ("/v1/health", "/v1/capabilities"):
                self.respond(200, self.worker.capabilities())
            elif self.command == "POST" and path == "/v1/calibrate":
                self.respond(200, self.worker.calibration())
            elif self.command == "POST" and path == "/v1/environments/status":
                self.respond(200, self.worker.environment_status(self.body()))
            elif self.command == "POST" and path == "/v1/sync/missing":
                self.respond(200, {"missing": self.worker.missing(self.body())})
            elif self.command == "POST" and path == "/v1/sync/apply":
                body = self.body()
                self.respond(200, self.worker.apply_manifest(body, body.get("workspace_key"), body.get("nest") or ""))
            elif self.command == "PUT" and path.startswith("/v1/blobs/"):
                digest = path.rsplit("/", 1)[1]
                size = int(self.headers.get("Content-Length", "-1"))
                if not HASH.fullmatch(digest) or not 0 <= size <= 2 * 1024**3:
                    raise ValueError("invalid blob hash/size")
                destination = self.worker.root / "cache/blobs" / digest
                temp = destination.with_suffix("." + secrets.token_hex(8) + ".tmp")
                remaining = size
                check = hashlib.sha256()
                try:
                    with temp.open("wb") as stream:
                        while remaining:
                            chunk = self.rfile.read(min(65536, remaining))
                            if not chunk:
                                raise ValueError("incomplete blob")
                            stream.write(chunk)
                            check.update(chunk)
                            remaining -= len(chunk)
                    if check.hexdigest() != digest:
                        raise ValueError("blob content does not match hash")
                    os.replace(temp, destination)
                finally:
                    temp.unlink(missing_ok=True)
                self.respond(200, {"stored": size})
            elif self.command == "POST" and path == "/v1/jobs":
                self.respond(202, self.worker.submit(self.body()))
            elif path.startswith("/v1/jobs/"):
                segments = path.split("/")
                jid = segments[3]
                record = self.worker.record(jid)
                if len(segments) == 4 and self.command == "GET":
                    self.respond(200, record)
                elif len(segments) == 5 and segments[4] == "cancel" and self.command == "POST":
                    self.worker.cancel(jid)
                    self.respond(200, {"cancelled": True})
                elif len(segments) == 5 and segments[4] == "output" and self.command == "GET":
                    query = parse_qs(parts.query)
                    stream = query.get("stream", ["stdout"])[0]
                    if stream not in ("stdout", "stderr"):
                        raise ValueError("invalid stream")
                    offset = max(0, int(query.get("offset", ["0"])[0]))
                    file = self.worker.root / "jobs" / jid / (stream + ".log")
                    data = b""
                    if file.exists():
                        with file.open("rb") as source:
                            source.seek(offset)
                            data = source.read(65536)
                    self.respond(200, {"data_b64": base64.b64encode(data).decode("ascii"), "next_offset": offset + len(data)})
                elif len(segments) == 5 and segments[4] == "artifact" and self.command == "GET":
                    output = parse_qs(parts.query).get("path", [""])[0]
                    changed = {item["path"] for kind in ("modified", "created") for item in record.get("changes", {}).get(kind, [])}
                    if output not in record["spec"].get("expected_outputs", []) and output not in changed:
                        raise ValueError("artifact was not declared")
                    file = within(self.worker.artifact_root(record, jid), output)
                    size = file.stat().st_size
                    self.send_response(200)
                    self.send_header("Content-Type", "application/octet-stream")
                    self.send_header("Content-Length", str(size))
                    self.end_headers()
                    with file.open("rb") as source:
                        shutil.copyfileobj(source, self.wfile, length=65536)
                else:
                    self.respond(404, {"error": "unknown endpoint"})
            else:
                self.respond(404, {"error": "unknown endpoint"})
        except FileNotFoundError as exc:
            self.respond(404, {"error": str(exc)})
        except OverflowError as exc:
            self.respond(429, {"error": str(exc)})
        except (ValueError, KeyError, TypeError) as exc:
            self.respond(400, {"error": str(exc)})
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as exc:
            self.respond(500, {"error": str(exc)})


class Server(ThreadingHTTPServer):
    daemon_threads = True
    request_queue_size = 16
    def __init__(self, address, worker):
        self.worker = worker
        self.slots = threading.BoundedSemaphore(16)
        super().__init__(address, Handler)
    def process_request(self, request, client_address):
        if not self.slots.acquire(blocking=False):
            request.close()
            return
        super().process_request(request, client_address)
    def process_request_thread(self, request, client_address):
        try:
            request.settimeout(30)
            super().process_request_thread(request, client_address)
        finally:
            self.slots.release()


def main():
    parser = argparse.ArgumentParser(description="Tidal Bridge Worker")
    parser.add_argument("--root", type=pathlib.Path, default=pathlib.Path.home() / "tidalbridge")
    parser.add_argument("--port", type=int, default=47832)
    parser.add_argument("--token-file", type=pathlib.Path)
    parser.add_argument("--mock", choices=("low", "mid", "high"))
    parser.add_argument("--capacity", type=int, default=2, choices=range(1, 9))
    parser.add_argument("--guest", action="store_true", help="Running inside the Debian userland (ADB shell worker)")
    parser.add_argument("--userland", type=pathlib.Path, help="ADB-shell Debian installation that runs every job (worker runs natively)")
    args = parser.parse_args()
    if args.userland and (cores := performance_cores()):
        # Before any thread starts: threads and jobs inherit this. A core
        # Android later pauses has its tasks moved, so jobs never stall.
        try:
            os.sched_setaffinity(0, cores)
        except OSError:
            pass
    token_file = args.token_file or args.root / "worker.token"
    if not token_file.exists():
        parser.error("Create worker.token with a paired random secret before starting the worker.")
    token = token_file.read_text().strip()
    if len(token) < 32:
        parser.error("worker token must be at least 32 characters")
    worker = Worker(args.root, token, args.mock, args.capacity, guest=args.guest, userland=args.userland)
    server = Server(("127.0.0.1", args.port), worker)
    print(json.dumps({"product": "Tidal Bridge Worker", "version": VERSION, "port": args.port, "stable_id": worker.identity, "simulated": bool(args.mock)}), flush=True)
    try:
        server.serve_forever(poll_interval=1)
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
        for jid in list(worker.processes):
            worker.cancel(jid)
        worker.pool.shutdown(wait=True, cancel_futures=True)


if __name__ == "__main__":
    main()
