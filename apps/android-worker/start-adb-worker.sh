#!/system/bin/sh
# Start the Tidal Bridge worker as the ADB shell user. Android places app
# processes (Termux) on the efficiency cores and lets the low-memory killer
# reclaim them; shell processes may use every core and are exempt.
#
# The worker runs natively with the Debian userland's Python, started through
# glibc's dynamic loader: its own file work (workspace sync, logs) is not
# traced by proot, which costs about half a millisecond per file-system call.
# Every job runs inside the userland through proot. Everything runs at reduced
# priority so the phone stays responsive. Idempotent: exits when the worker is
# already running.
B=/data/local/tmp/tidalbridge
PORT=47833
PIDFILE=$B/worker.pid
RUNNING=$(pgrep -f "worker[.]py --root $B/state" | head -n 1)
if [ -n "$RUNNING" ]; then
  echo "$RUNNING" > "$PIDFILE"
  echo "running $RUNNING"
  exit 0
fi
R=$B/rootfs
LIB=$R/usr/lib/aarch64-linux-gnu
PY=$(ls "$R"/usr/bin/python3.* 2>/dev/null | grep -E '/python3\.[0-9]+$' | sort | tail -n 1)
if [ -z "$PY" ] || [ ! -f "$LIB/ld-linux-aarch64.so.1" ]; then
  echo "the Debian userland is not provisioned" >&2
  exit 1
fi
mkdir -p "$B/logs" "$B/tmp" "$B/state" "$R/tmp" "$R/.l2s" "$B/sysdata/sys_empty"
chmod 1777 "$R/tmp" 2>/dev/null
cd "$B" || exit 1
setsid nohup nice -n 10 env -i HOME="$B/state" LANG=C.UTF-8 TMPDIR="$B/tmp" PATH=/system/bin:/system/xbin \
  PYTHONHOME="$R/usr" PYTHONDONTWRITEBYTECODE=1 \
  "$LIB/ld-linux-aarch64.so.1" --library-path "$LIB" "$PY" "$B/worker/worker.py" \
  --root "$B/state" --port "$PORT" --userland "$B" --capacity 8 \
  >> "$B/logs/worker.log" 2>&1 < /dev/null &
echo $! > "$PIDFILE"
echo "started $!"
