# Troubleshooting

| Symptom | Check and action |
|---|---|
| Host unavailable | `tidalbridge service status`, then `tidalbridge service start` and `doctor`. Logs: `%USERPROFILE%\.tidalbridge\service.log` and `service.crash.log`. |
| Agents see a different state than the dashboard | Packaged apps (Claude, Codex) virtualize AppData. Every component uses `%USERPROFILE%\.tidalbridge`; rerun `setup-agents.ps1` and `install-automation.ps1 -EnableCodex` if a configuration still names `%LOCALAPPDATA%\TidalBridge`. |
| `build.ps1` refuses to run | Jobs are active. Wait, or cancel them with `tidalbridge job ID` / `cancel ID`. |
| ADB_UNAUTHORIZED | Unlock the phone and accept this computer's USB debugging key. |
| DISCONNECTED | Check the cable, USB debugging and `adb devices -l`. A single slow `adb` call no longer marks the phone disconnected; three in a row do. |
| DEGRADED (ADB-shell worker) | The host restarts it through `adb shell` within about a minute. Manually: `adb shell sh /data/local/tmp/tidalbridge/start-worker.sh`; log: `/data/local/tmp/tidalbridge/logs/worker.log`. |
| THERMALLY_LIMITED | The phone reported severe heat (Android thermal status 3+). Nothing is routed to it until it cools; charging adds heat. Long back-to-back benchmarks caused this once during testing. |
| Worker installer: `proot ... execve: Permission denied` | A `proot` that looks for files inside Termux's private directory. The installer rewrites Termux's paths; do not substitute another `proot` build. |
| Worker installer: "worker did not start" | Read the worker log. "Address already in use" means an old worker still holds port 47833; rerun the installer, which stops workers by command line. |
| Commands in an approved project still run locally | `route` shows why: no measured history yet (new commands are assumed slower until measured; two `TIDALBRIDGE_FORCE_REMOTE=1` runs seed it), "too small to relieve the laptop", a cold dependency environment (being prepared in the background), or the laptop not busy enough. |
| Ordinary commands never reach the adapters | Start a new agent session after installation. Check that `npm`/`python` resolve to `%USERPROFILE%\.tidalbridge\shims`. Absolute runtime paths and aliases bypass adapters. |
| Remote test run fails with "Timeout waiting for worker to respond" | vitest gives a worker 60 seconds to start, and module loading is slow under `proot`. The worker sets `VITEST_MAX_WORKERS=2`; a lower value in the project's environment also works. |
| A remote job cannot find generated files | A `.gitignore` excludes them from sync. Add `!path/` to the project's `.tidalbridgeignore`. |
| Remote command fails only remotely | Nothing to do: a command's first phone failures are re-run on this laptop, and one that passes here but fails on the phone stays here for a week. A test that reads a file from outside the project folder fails on the phone once, runs here, and the phone gets that folder from then on. To keep a project here, list it under "Never offloaded" in the dashboard. |
| Dev server stays local | Its port is busy on the laptop (another dev server is running), or the mode is CONSERVATIVE/BATTERY_SAVER. Stop the local server first. |
| Browser jobs rejected | Open Chrome on the unlocked phone, then `refresh`; verify `browser_qa` is true. |
| Capture cannot reach the laptop server | Use `http://127.0.0.1:PORT` or `localhost:PORT` with a port of 1024 or more, and keep the server running. |
| Forced remote command rejected | Inspect `route`; forcing does not bypass OS, architecture, thermal, battery, RAM or capacity gates. |
| MCP tools absent in a session | Start a new session after registration; the CLI remains available. |

If a transport drops during a job, inspect its attempts with `job ID`. Only replay-safe policies allow retry or fallback. Check a side-effecting command's previous effects before rerunning it. A host restart reports unfinished work as INTERRUPTED.
