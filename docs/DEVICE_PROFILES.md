# Device profiles and multiple devices

Every paired serial maps to a persistent worker identity. Profiles are written as schema-versioned JSON under the host `profiles` directory. Discovery follows ADB connection state and authenticated worker health, then augments capability data with battery, thermal state, and Chrome availability. A valid stored calibration is reused on reconnect; worker/runtime/Android changes invalidate its fingerprint and trigger a short CPU/hash/transport calibration.

States include ADB_CONNECTED, ADB_UNAUTHORIZED, READY, BUSY, DISCONNECTED, DEGRADED, RESOURCE_LIMITED, and THERMALLY_LIMITED. Drain is independent of health. A stale tunnel is repaired after health failure. Discovery runs every 30 seconds; active job transport loss is noticed sooner through job polling. A physically disconnected phone may still display its last resources until the next discovery pass.

Add devices by repeating pairing with each explicit serial. Each device has a separate secret, stable ID, forward port, resource gates, and execution slots. Core code contains no Samsung/S23-specific branch. Select a device with `--device` or allow candidate comparison. A single real phone plus the three explicitly labeled mock tiers verified the generic scheduling design; concurrent physical-device throughput still needs two real devices.

```powershell
.\bin\tidalbridge.exe devices
.\bin\tidalbridge.exe device WORKER_ID
.\bin\tidalbridge.exe drain WORKER_ID
.\bin\tidalbridge.exe enable WORKER_ID
.\bin\tidalbridge.exe sync --workspace '.\tests\fixtures\small-project' --device WORKER_ID
.\bin\tidalbridge.exe benchmark --device WORKER_ID
```

Available RAM/storage are measured. Android CPU is currently a normalized load-average estimate, not a sampled CPU utilization counter. Unsupported battery/thermal signals remain unknown; mocks inject clearly labeled synthetic properties. Worker per-attempt peak RSS, when available, samples only the root process every 500 ms and does not sum descendants or shared pages. Short-lived jobs may evade that sample. Windows daemon CPU/RSS are measured separately.
