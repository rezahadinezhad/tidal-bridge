#!/data/data/com.termux/files/usr/bin/bash
set -eu
# Run once inside Termux; all runtime files remain in this dedicated directory.
ROOT="$HOME/tidalbridge"
mkdir -p "$ROOT/worker" "$ROOT/logs"
chmod 700 "$ROOT"
pkg install -y python git nodejs termux-services
if [ ! -f "$ROOT/worker/worker.py" ] || [ ! -f "$ROOT/worker.token" ]; then
  echo 'The host pairing script must stage worker.py and worker.token first.' >&2
  exit 1
fi
chmod 600 "$ROOT/worker.token"
SERVICE="$PREFIX/var/service/tidalbridge"
mkdir -p "$SERVICE"
cat > "$SERVICE/run" <<'EOF'
#!/data/data/com.termux/files/usr/bin/sh
exec python "$HOME/tidalbridge/worker/worker.py" --root "$HOME/tidalbridge" >> "$HOME/tidalbridge/logs/worker.log" 2>&1
EOF
chmod 700 "$SERVICE/run"
touch "$SERVICE/down"
source "$PREFIX/etc/profile.d/start-services.sh"
rm -f "$SERVICE/down"
for attempt in $(seq 1 15); do
  if [ -p "$SERVICE/supervise/ok" ]; then break; fi
  sleep 1
done
sv up "$SERVICE"
sv status "$SERVICE"
echo 'Tidal Bridge Worker installed. Termux services will restart the worker while Termux is alive.'
echo 'Allow Termux unrestricted battery use. Termux:Boot is optional for device reboot startup.'
