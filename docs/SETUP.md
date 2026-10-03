# Setup and lifecycle

## Windows

`pwsh -File scripts\install.ps1` performs every step below (and the phone's), and is safe to run again; `scripts\update.ps1` pulls, rebuilds and updates the phone. The individual steps:

Use PowerShell 7, Python 3.11+, Git and Android SDK Platform Tools (the installer downloads them into `%USERPROFILE%\.tidalbridge\platform-tools` when `adb` is missing). Node is needed for local Node jobs. The project-local Go bootstrap is used only for building.

```powershell
.\scripts\build.ps1              # build and (re)start the service
.\scripts\install-startup.ps1    # register the "Tidal Bridge Host" logon task
.\scripts\install-desktop.ps1    # Desktop and Start menu shortcuts
.\scripts\setup-agents.ps1       # Claude Code hooks + MCP, Codex MCP, agent instructions
.\scripts\install-automation.ps1 -EnableCodex   # Codex command environment
.\bin\tidalbridge.exe doctor
```

The host runs as the per-user Task Scheduler task **Tidal Bridge Host**, started at logon and restarted on failure, without administrator rights. `tidalbridge service install|uninstall|start|stop|status` manages it. State is in `%USERPROFILE%\.tidalbridge` (`config.yaml`, `host.token`, history, job records, results, `service.log`). `TIDALBRIDGE_DATA_DIR` or a leading `--data-dir PATH` selects a separate instance. The service binds only to `127.0.0.1:47831`. Keep this checkout and its drive available at logon.

`scripts/build.ps1` refuses to run while jobs are active, swaps binaries that are in use by renaming them, refreshes the command shims in `%USERPROFILE%\.tidalbridge\shims` and restarts the service. Configuration changes outside the dashboard need a service restart; `mode`, `pause`, `resume`, `drain` and `enable` act on the running service.

Approve a project once, from the dashboard's **Automation** page or with `tidalbridge automation enable --workspace PATH`. Approval is saved in the project's `.tidalbridge/tasks.json`; see [agent integration](AGENT_INTEGRATION.md) for its fields. A new agent session picks up the adapters.

The Desktop and Start menu shortcuts launch `bin/tidalbridge-desktop.exe`, which starts the host if needed and opens the dashboard in an Edge app window. `scripts/uninstall.ps1` removes the shortcuts, the logon task and the agent integration, keeping state and results; `-RemovePhone` also deletes the worker, its userland and the project copies (including shared `.env` files) from every connected phone.

## Android: Termux worker (pairing)

1. Install Termux from an official source and open it. Do not mix APKs with different signing keys ([official installation notes](https://github.com/termux/termux-app#installation)).
2. Enable developer options and USB debugging; accept this computer's debugging key on the phone. Find the serial with `adb devices -l`.
3. Run the one-time bootstrap and follow its printed instruction inside Termux:

```powershell
python scripts/onboard-device.py --adb "$env:LOCALAPPDATA\Android\Sdk\platform-tools\adb.exe" --serial YOUR_SERIAL
```

The bootstrap stages the worker and a private pairing secret through an ADB reverse tunnel, verifies file hashes, installs Python, Node, Git and `proot`, and enables a supervised service on Android loopback port 47832. It expires after an hour. Root is never required. `python scripts/update-worker.py --device WORKER_ID` updates this worker.

## Android: ADB-shell worker (recommended)

No Termux is needed; enable USB debugging and allow this computer on the phone, then:

```powershell
python scripts/install-adb-worker.py
.\bin\tidalbridge.exe service stop; .\bin\tidalbridge.exe service start
```

The installer downloads `proot` and the two libraries it needs from Termux's package repository, checks each package against the repository index's SHA-256, and rewrites proot's built-in Termux paths (`--kit-from-termux` copies them from an installed Termux instead). It downloads the official Debian arm64 image (verified by digest) and unpacks it under `/data/local/tmp/tidalbridge`. It provisions Node 22, uv, git and a C toolchain (several minutes, detached on the phone), starts the worker on Android loopback port 47833 and approves it. The approval replaces the phone's Termux entry. The host restarts the worker through `adb shell` whenever it stops answering, for example after a phone reboot. Re-run the installer with `--skip-provision` to push worker updates (about 15 seconds). Its token is `%USERPROFILE%\.tidalbridge\adb-worker-SERIAL.token`.

**Several phones**: run the installer once per phone with `--serial`; the scheduler places work on all approved phones. **Wireless debugging** (Android 11+): pair with `adb pair HOST:PORT`, connect with `adb connect HOST:PORT`, and install with `--serial HOST:PORT`; the address changes when debugging is toggled, so the phone must be installed again then. Both are untested on this workstation.

**Linux and macOS laptops** (experimental): the code builds for both, but the background service (Task Scheduler) and live file watching are Windows-only; elsewhere run the host in the foreground (`tidalbridge serve`) and the index rescans on demand. The installer scripts are PowerShell.

Keep USB debugging enabled and the cable connected. Android may revoke debugging authorization after a while without connecting; accept the prompt again when it appears.

## Rendering

Open Chrome on the unlocked phone, keep a laptop development server running, then submit:

```powershell
.\bin\tidalbridge.exe render-matrix --url http://127.0.0.1:3000
```

The default matrix has ten viewports from 360 to 1920 pixels; `--matrix path.json` supplies 1–16 viewports with `name`, `width`, `height` and `dpr`. Files are under `results/ID/ATTEMPT_ID`. Captures are Android renderings even at desktop sizes; validate Windows desktop fidelity separately.

## Removal

```powershell
.\scripts\uninstall.ps1
```

This removes the logon task, the shortcuts, the Codex command environment and the Claude Code integration (hooks, MCP entry, instruction block), and stops the service, keeping state and results. Remove Codex's MCP entry with `codex mcp remove tidalbridge` and its instruction block from `%USERPROFILE%\.codex\AGENTS.md`. On the phone, `adb shell rm -rf /data/local/tmp/tidalbridge` removes the ADB-shell worker; in Termux, `sv down "$PREFIX/var/service/tidalbridge"` stops the Termux worker.
