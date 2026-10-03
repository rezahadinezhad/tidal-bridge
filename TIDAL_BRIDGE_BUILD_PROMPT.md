# Tidal Bridge — Build Prompt for Codex

> Specification revision: 3 — Tidal Bridge identity + device-agnostic adaptive local compute pool
> Initial reference hardware: Samsung Galaxy S23 Ultra, but no core behavior may depend on that model.

## Role

You are the principal systems engineer responsible for designing and implementing **Tidal Bridge**, a Windows-to-Android compute offloading system.

Do not stop at architecture, pseudocode, or a proposal. Build a working end-to-end MVP, test it, benchmark it, document it, and then iterate toward the fuller architecture described below.

When a design decision is uncertain, prefer:
1. reliability,
2. low host CPU/RAM overhead,
3. transparent fallback,
4. debuggability,
5. security,
6. maintainability,
7. performance.

Do not pretend that Android RAM can be physically pooled into Windows RAM. Tidal Bridge must create the *practical effect* of adding compute capacity by moving eligible whole processes/jobs, their working sets, and their memory consumption to Android worker devices.

Tidal Bridge must be **device-agnostic from the core architecture**. The Samsung Galaxy S23 Ultra is the first real reference device for development and validation, not a hard-coded target. The same host daemon, protocol, scheduler, capability model, and workspace system must support other Android phones and tablets without model-specific branching unless a documented adapter is genuinely required.

Treat the long-term product as an **adaptive local heterogeneous compute pool for a developer workstation**, not as an S23-specific accessory and not as a GitHub/CI runner.

## Product Identity — Tidal Bridge

The product name is **Tidal Bridge**. The name is inspired by tidal bridges seen in interacting astronomical systems: material can form a visible bridge between otherwise separate bodies under mutual gravitational interaction. That metaphor matches the product goal: independent compute nodes remain distinct systems, while Tidal Bridge creates a practical execution path that lets useful work flow between them.

Use this naming consistently:

- Product/display name: `Tidal Bridge`
- CLI / binary / package slug: `tidalbridge`
- Environment variable prefix: `TIDALBRIDGE_`
- Ignore file: `.tidalbridgeignore`
- Host service display name: `Tidal Bridge Host Service`
- Android worker display name: `Tidal Bridge Worker`
- MCP server/tool namespace: `tidalbridge`

Do not reintroduce the previous NovaBridge name in UI, docs, code comments, service names, config examples, package metadata, or generated assets unless documenting migration history.

---

# 1. Context

The Windows laptop is resource-constrained:

- CPU: Intel Core i7-10510U
- RAM: 16 GB
- No discrete GPU
- During multi-agent coding workflows, host CPU commonly reaches ~90–100%
- Host RAM is commonly nearly exhausted
- Claude Code and Codex may run multiple coding/polish/fix tasks in parallel
- Browser rendering, screenshots, tests, builds, linters, TypeScript, Python, Node processes, repo analysis, and agent subprocesses can make the laptop stall

The initial reference worker device is:

- Samsung Galaxy S23 Ultra
- 12 GB RAM
- ARM64 Android device
- Commonly several GB of RAM are free during development work
- Connected to the laptop primarily by USB/ADB

However, **none of those exact hardware facts may be assumed by the product architecture**. The system must discover the actual connected Android device or devices at runtime and build a capability/performance profile for each one.

Tidal Bridge must ultimately support:

- different Android manufacturers and models,
- phones and tablets,
- different Android/API levels,
- ARM64 and any future supported ABI exposed by Android tooling,
- different RAM capacities and thermal behavior,
- different runtime/toolchain availability,
- one or multiple simultaneously attached Android workers.

The goal is to make available Android devices act as **general-purpose auxiliary compute nodes** for the local workstation whenever doing so produces real practical benefit.

Tidal Bridge is not only for Claude Code or Codex. It must expose a reusable local compute layer that other tools can use too, and it should be suitable for eventual open-source publication.

---

# 2. Desired User Experience

The desired experience is:

1. Windows starts.
2. Tidal Bridge starts automatically in the background with very low overhead.
3. The user connects one or more authorized Android devices by USB.
4. Tidal Bridge detects each device automatically.
5. It establishes or repairs the ADB transport automatically.
6. It identifies the model, Android version, API level, ABI/architecture, CPU topology, available RAM, storage, browser/debugging support, runtime/toolchain support, battery/charging state, thermal state, and worker version.
7. For a new or materially changed device, Tidal Bridge runs a short, low-risk calibration benchmark and creates a persistent **Device Performance Profile**.
8. It starts/reconnects the Android worker automatically and marks the device READY only after health and compatibility checks pass.
9. Every READY device joins a local compute pool.
10. Eligible workloads are automatically routed to the best node only when the estimated benefit exceeds transfer/setup/risk cost.
11. If a device disconnects, becomes hot, becomes resource constrained, a job is incompatible, or the worker crashes, Tidal Bridge gracefully retries, requeues, migrates when safe, falls back locally, or reports a clear recoverable failure according to policy.
12. Claude Code and Codex can see that Tidal Bridge exists and can deliberately use it without the user reminding them in every chat.
13. The user can inspect what is running locally vs on each Android worker, why the scheduler made that decision, how much data moved, and how much host pressure was avoided.
14. Tidal Bridge learns from completed jobs so future scheduling reflects **measured performance on this exact host/device combination**, not generic hardware assumptions.

The normal workflow should become:

> Start Windows → connect any supported Android device(s) → work normally → Tidal Bridge discovers, calibrates, monitors, and uses them when useful.

There must not be a recurring manual "send this to the phone" step.

The system should default to conservative automation: if offloading is not clearly compatible and beneficial, stay local.

---

# 3. Non-Negotiable Technical Reality

Design around these facts.

## 3.1 Do not implement fake RAM pooling

Windows cannot directly allocate ordinary process pages into Android RAM over USB as if the phone were another DIMM.

Therefore:

- Never market or represent this as literal shared RAM.
- Do not create a remote swap-file hack as the primary design.
- Do not attempt to expose Android storage as Windows paging memory.
- Do not build fragile kernel-level memory tricks.

Instead, move **entire eligible jobs/processes** to Android so that their CPU time and process working set live on the phone.

Example:

```text
BAD mental model:
Windows process -> 4 GB Windows RAM + 3 GB Android RAM

CORRECT mental model:
Windows orchestrator -> remote job on Android -> job consumes Android CPU/RAM
```

## 3.2 Architecture differences matter

The laptop is x86-64 and the phone is ARM64.

Never assume arbitrary binaries can move between them.

Classify work into:

- architecture-independent,
- ARM64-compatible,
- host-only,
- unknown.

Do not return ARM64 native build artifacts when the caller expects an x86-64 Windows artifact.

## 3.3 Android is not a desktop Linux server

Android may throttle or kill background/high-CPU processes.

Tidal Bridge must:

- detect worker death,
- reconnect,
- restart safely,
- checkpoint or retry where possible,
- enforce thermal/resource limits,
- never assume a forever-running Termux process is immortal.

No root should be required for the MVP.

---

# 4. Product Name and Repository

Use:

```text
Tidal Bridge
```

Suggested repository layout:

```text
tidalbridge/
  apps/
    host-daemon/
    cli/
    mcp-server/
    dashboard/
    android-worker/
  packages/
    protocol/
    scheduler/
    workspace-sync/
    telemetry/
    task-classifier/
    device-profiler/
    calibration/
    environment-manager/
    transport/
    history-store/
  scripts/
    setup-windows.ps1
    setup-android.sh
    install-startup.ps1
    uninstall.ps1
    dev.ps1
  configs/
    default.yaml
    command-profiles.yaml
  tests/
    integration/
    e2e/
    fixtures/
  docs/
    ARCHITECTURE.md
    SETUP.md
    SECURITY.md
    TROUBLESHOOTING.md
    BENCHMARKS.md
    DEVICE_PROFILES.md
    SCHEDULER_POLICY.md
    MULTI_DEVICE.md
    AGENT_INTEGRATION.md
  AGENTS.md
  CLAUDE.md
  README.md
```

Adjust the structure if a cleaner implementation emerges, but preserve clear component boundaries.

---

# 5. Phase 0 — Inspect Before Building

Before selecting libraries or assuming current capabilities:

1. Inspect the Windows development environment.
2. Check whether `adb`, Git, Node, Python, Go/Rust, Claude Code, and Codex are installed.
3. Check the current Codex MCP configuration mechanism from official OpenAI documentation.
4. Check the current Claude Code MCP configuration mechanism from official Anthropic documentation.
5. Check the current stable MCP SDKs and select a maintained implementation.
6. Detect whether WSL is installed, but **do not require WSL** unless there is a compelling reason.
7. Detect all Android devices with `adb devices -l`; do not assume exactly one device.
8. Record model, manufacturer, Android/API level, ABI list, CPU topology where safely available, RAM, storage, battery state, thermal capability, transport details, browser/debugging capabilities, and available runtimes.
9. Generate a stable device ID/fingerprint that does not depend only on a transient ADB serial.
10. If a device is new, run the minimum calibration suite needed to classify likely useful workloads.
11. If no physical Android device is connected yet, build against mock workers representing low-, mid-, and high-capability devices. Do not block software development waiting for the reference phone.

Use official/current documentation rather than assumptions where CLI configuration may have changed.

---

# 6. Core Architecture

Build five principal layers around a **host-controlled multi-device worker pool**.

```text
┌──────────────────────────────────────────────────────────────┐
│                Claude Code / Codex / CLI / IDE              │
└──────────────────────────────┬───────────────────────────────┘
                               │
                    MCP / local API / CLI / shims
                               │
                               ▼
┌──────────────────────────────────────────────────────────────┐
│                    Tidal Bridge Host Service                   │
│                                                              │
│ Device Manager | Profiler | Scheduler | Policy Engine        │
│ Telemetry      | History  | Task Classifier | Job Queue      │
│ Workspace Sync | Cache    | Environment Manager | Fallback    │
└───────────────┬──────────────────┬───────────────────────────┘
                │                  │
          USB/ADB node A      USB/ADB node B ...
                │                  │
                ▼                  ▼
┌────────────────────────┐  ┌────────────────────────┐
│ Android Worker A       │  │ Android Worker B       │
│ Executor               │  │ Executor               │
│ Resource Monitor       │  │ Resource Monitor       │
│ Workspace Cache        │  │ Workspace Cache        │
│ Browser QA             │  │ Browser QA             │
│ Runtime Environments   │  │ Runtime Environments   │
└────────────────────────┘  └────────────────────────┘
```

The host is always the control plane. Android devices are worker/data-plane nodes.

Core abstractions must use terms such as `WorkerNode`, `DeviceProfile`, `CapabilitySet`, and `ExecutionTarget`; do not make `S23`, `Samsung`, or even the word `phone` part of core scheduling types.

The initial implementation may support one real device first, but all APIs and schemas must allow a collection of workers from day one.

---

# 7. Windows Host Daemon

Build a lightweight daemon/service whose idle overhead is minimal.

Prefer a compiled, low-overhead implementation for the always-running host component. Go is a strong default unless environment inspection reveals a better option.

Responsibilities:

- launch automatically at Windows login/startup,
- discover ADB,
- detect USB attach/detach,
- identify approved device serials and stable device identities,
- discover and manage multiple Android workers concurrently,
- maintain an independent state machine per worker,
- generate/update persistent device capability and performance profiles,
- establish `adb forward` to the worker,
- establish `adb reverse` when the phone needs access to a host dev server,
- health-check the worker,
- collect telemetry,
- maintain a job queue,
- classify and schedule jobs across local execution and all READY workers,
- estimate offload benefit before moving work,
- record explainable scheduling decisions and execution history,
- sync required files,
- stream stdout/stderr,
- collect results/artifacts/diffs,
- retry eligible jobs,
- fall back safely,
- expose a localhost API,
- expose status to CLI/dashboard/MCP,
- keep structured rotating logs.

Suggested device states:

```text
ABSENT
ADB_UNAUTHORIZED
ADB_CONNECTED
BOOTSTRAPPING
WORKER_STARTING
READY
BUSY
THERMALLY_LIMITED
RESOURCE_LIMITED
DEGRADED
DISCONNECTED
```

Transitions must be logged and observable.

---

# 8. Android Worker

The Android side must be device-agnostic. Model-specific workarounds belong behind documented adapters/capability probes, never in the generic execution path.

## 8.1 MVP runtime

Start with a **Termux-based worker** for the fastest vertical slice because it provides a practical Linux-like user space without root. Keep the protocol independent of Termux so the worker runtime can later migrate to a native Android app with an embedded/provisioned Linux user space (for example a PRoot-based environment) without changing host-side job semantics.

The one-time setup should install only necessary packages, likely including some subset of:

```text
git
python
nodejs
openssh
curl
rsync or equivalent if viable
clang/build-essential only when needed
```

Do not blindly install large toolchains.

Implement a worker process with:

- authenticated localhost-facing RPC/HTTP/gRPC transport tunneled through ADB,
- job execution,
- process cancellation,
- timeout handling,
- stdout/stderr streaming,
- exit-code reporting,
- environment profiles,
- workspace cache,
- artifact collection,
- metrics endpoint,
- capability endpoint,
- version negotiation,
- heartbeat.

The worker must use a dedicated directory, for example:

```text
~/tidalbridge/
  worker/
  workspaces/
  cache/
  artifacts/
  logs/
```

Never execute jobs in arbitrary personal phone directories.

## 8.2 Future native Android worker

Keep the protocol independent of Termux so that a later native Android app can replace or complement the Termux worker.

A later native app may provide:

- foreground/service lifecycle integration appropriate to the detected Android/API level,
- an embedded or provisioned PRoot-style Linux environment where technically appropriate,
- better lifecycle resilience,
- richer thermal APIs,
- persistent notification,
- WebView-based screenshot worker,
- device telemetry,
- auto-start assistance,
- a polished phone-side status UI.

Do not make the MVP wait for this native application.

---

# 9. ADB Transport

Use USB ADB as the default transport, but define a transport abstraction so the scheduler/job layer is not coupled to USB.

Requirements:

- USB first; no Wi-Fi dependency for MVP.
- The transport layer must identify per-device bandwidth/latency characteristics because transfer cost is part of scheduling.
- Preserve extension points for authenticated Wireless ADB or other future local transports without weakening default security.
- Device must be explicitly authorized by Android.
- Support only allow-listed device serials.
- Bind host services to loopback by default.
- Prefer ADB tunnels rather than exposing worker ports to the LAN.

Use both directions where appropriate:

```text
Host -> Phone:
adb forward tcp:<host-port> tcp:<phone-worker-port>

Phone -> Host local dev server:
adb reverse tcp:<phone-visible-port> tcp:<host-dev-server-port>
```

For repository/file movement, evaluate:

- incremental content-addressed sync,
- `adb push/pull`,
- Git-based sync,
- rsync-like delta sync if stable in the selected environment.

Do not copy an entire large repository for every job.

---

# 10. Capability Discovery and Device Profiling

The worker and host must build capabilities dynamically for **every connected device**. Never infer suitability solely from the marketing model name or SoC name.

Collect a factual capability snapshot where safely available:

```json
{
  "device": {
    "stable_id": "...",
    "adb_serial": "...",
    "manufacturer": "Samsung",
    "model": "Galaxy S23 Ultra",
    "android_version": "...",
    "api_level": 0,
    "abis": ["arm64-v8a"],
    "worker_version": "...",
    "transport": "usb_adb"
  },
  "resources": {
    "cpu_logical_cores": 8,
    "cpu_load": 0.22,
    "ram_total_mb": 12000,
    "ram_available_mb": 6100,
    "storage_available_mb": 42000,
    "battery_percent": 71,
    "charging": true,
    "thermal_status": "nominal"
  },
  "runtimes": {
    "python": "...",
    "node": "...",
    "git": "...",
    "java": null,
    "go": null,
    "rust": null
  },
  "features": {
    "shell_exec": true,
    "workspace_sync": true,
    "browser_qa": true,
    "chrome_devtools": true,
    "image_processing": true
  },
  "calibration_profile": {
    "version": 1,
    "node_score": null,
    "python_score": null,
    "git_score": null,
    "browser_score": null,
    "storage_score": null,
    "usb_transfer_score": null
  }
}
```

Never hard-code the reference phone's currently available RAM, core count, browser support, or runtime list as permanent values.

## 10.1 Stable Device Profile

Persist a `DeviceProfile` for each approved worker. It should contain:

- stable identity/fingerprint,
- current capabilities,
- historical capabilities that changed after OS/app updates,
- benchmark/calibration results,
- runtime/environment inventory,
- observed reliability,
- observed thermal behavior,
- average transport bandwidth/latency,
- per-workload historical performance.

Version the profile schema. Invalidate only affected benchmark entries when the Android version, worker version, runtime version, or major environment changes.

## 10.2 Automatic Qualification / Calibration

When a new device is first approved, run a short calibration suite that is deliberately bounded so onboarding itself does not overheat the phone. Measure representative capabilities such as:

- process startup overhead,
- small Python CPU workload,
- small Node/TypeScript workload if available,
- Git/repository scan workload,
- filesystem read/write/hash performance,
- compression/decompression,
- ADB transfer throughput and round-trip latency,
- browser page load/screenshot capability if browser QA is supported.

Calibration should classify capability, not produce a vanity benchmark score. The scheduler should use raw/normalized measurements and historical real-job observations.

Recalibrate selectively when:

- worker/runtime versions materially change,
- Android OS updates,
- repeated real-job measurements diverge significantly from the stored profile,
- the user explicitly runs `tidalbridge benchmark --device <id>`.

---

# 11. Job Model

Create a structured job contract.

A job should include fields conceptually similar to:

```yaml
id: uuid
type: shell
command: "pytest -q"
working_directory: repo/subdir
env: {}
timeout_seconds: 600

requirements:
  architecture: any
  runtime:
    python: ">=3.11"
  min_available_ram_mb: 1500
  min_available_storage_mb: 500

policy:
  remote_preferred: true
  local_fallback: true
  cache_workspace: true
  allow_network: false
  idempotent: true
  retryable: true
  resumable: false
  expected_outputs: []

estimates:
  expected_duration_ms: null
  expected_input_bytes: null
  expected_output_bytes: null
  workload_signature: null

safety:
  destructive: false
  requires_confirmation: false
```

Design a stable versioned schema.

---

# 12. Scheduler and Task Router

This is the core of the product.

The scheduler must choose between:

```text
LOCAL
REMOTE_DEVICE:<stable-device-id>
WAIT
REJECT
```

Do not encode a single `REMOTE_PHONE` target into the core model. Even if the MVP has only one physical device, the scheduler API must accept a set of candidate workers.

Use a weighted decision based on:

- worker online/READY state,
- architecture and ABI compatibility,
- runtime/toolchain compatibility,
- available RAM on each node,
- CPU pressure on host and workers,
- thermal state/headroom,
- battery and charging state,
- available storage,
- current per-node concurrency,
- transfer bandwidth/latency,
- bytes that actually need to sync,
- warm/cold dependency cache state,
- estimated task duration,
- architecture-specific outputs,
- Windows/API/GUI requirements,
- security policy,
- reliability history,
- historical execution performance for the same or similar workload signature.

## 12.1 Offload Benefit Model

Do not offload merely because a device is idle or faster in a synthetic benchmark. Estimate the **total expected cost**.

Conceptually:

```text
remote_total_cost =
    sync/setup cost
  + expected remote execution cost
  + expected result-transfer cost
  + reliability/risk penalty
  + thermal penalty

local_total_cost =
    expected local execution cost
  + host-pressure penalty

offload_benefit = local_total_cost - remote_total_cost
```

A job should normally be offloaded only when compatibility is proven and `offload_benefit` exceeds a configurable safety margin.

Host responsiveness is a first-class objective. A remote run may be chosen even when wall-clock time is slightly slower if it materially reduces host CPU/RAM pressure and enables other interactive/agent work. That trade-off must be explicit and inspectable.

## 12.2 Explainable Scheduling

Every scheduling decision must produce machine-readable reasons and a concise human explanation. Example:

```text
pytest -> REMOTE_DEVICE:pixel-10-pro-a1b2

Reasons:
+ host RAM pressure 94%
+ host CPU pressure 91%
+ worker has 5.8 GB RAM available
+ Python runtime compatible
+ workspace cache is warm; only 14 MB changed
+ historical remote speedup 1.7x
- estimated sync cost 0.8 s
- thermal headroom moderate

Expected benefit: HIGH
Decision score: 0.86
```

Do not silently route incompatible tasks.

## 12.3 Adaptive Scheduling From Real History

Use calibration only as an initial prior. After real jobs run, prefer measured history on the **exact host + device + workload signature**.

Track moving statistics such as:

- local duration distribution,
- remote duration distribution per device,
- sync/setup duration,
- bytes transferred,
- peak/average RAM,
- host CPU/RAM relief,
- failure/retry rate,
- thermal impact,
- cold-cache vs warm-cache performance.

Do not build ML in the initial implementation. Use transparent moving averages, percentiles, and heuristics. The architecture may expose a future policy plugin interface.

---

# 13. Workloads Tidal Bridge Should Support

The system must not be artificially limited to only lint/tests/screenshots.

Implement a **generic remote command execution primitive**, then build profiles on top of it.

## 13.1 High-priority profiles

Support and test:

### Python
- scripts,
- pytest,
- data parsing,
- static analysis,
- formatting,
- code generation,
- CPU-heavy pure-Python jobs when practical.

### Node / JavaScript / TypeScript
- npm/pnpm/yarn scripts where dependencies support ARM64,
- ESLint,
- Prettier,
- TypeScript type-checking,
- Vitest/Jest where compatible,
- code generation,
- AST analysis,
- bundling when dependencies are ARM64-safe and output semantics are acceptable.

### Git / source analysis
- `git status`,
- diffs,
- searches,
- repository scans,
- code statistics,
- non-destructive analysis.

### File/data work
- compression/decompression,
- hashing,
- JSON/CSV processing,
- text transforms,
- image preprocessing,
- image comparison,
- artifact packaging.

### Build/compile tasks
Allow only after compatibility checks.

Do not use ARM64 output as a substitute for required Windows x86-64 binaries.

### Custom shell jobs
Permit generic jobs when the scheduler can validate the environment and policy allows them.

---

# 14. Browser / UI Polish / Screenshot Matrix

This use case is especially important.

The user's coding agents often:

1. open a web platform,
2. render the same screen at many viewport sizes,
3. capture screenshots,
4. inspect visual/responsive issues,
5. modify code,
6. repeat.

Tidal Bridge should offload as much of this pipeline as is technically sound.

## 14.1 Target screenshot matrix

Support a configurable matrix such as:

```yaml
viewports:
  - {name: phone-small, width: 360, height: 800, dpr: 1}
  - {name: phone, width: 390, height: 844, dpr: 1}
  - {name: phone-large, width: 430, height: 932, dpr: 1}
  - {name: tablet-portrait, width: 768, height: 1024, dpr: 1}
  - {name: tablet-landscape, width: 1024, height: 768, dpr: 1}
  - {name: laptop-13, width: 1280, height: 800, dpr: 1}
  - {name: laptop-14, width: 1440, height: 900, dpr: 1}
  - {name: laptop-15, width: 1536, height: 864, dpr: 1}
  - {name: laptop-16, width: 1728, height: 1117, dpr: 1}
  - {name: desktop-fhd, width: 1920, height: 1080, dpr: 1}
```

Allow up to at least 16 named viewport presets.

## 14.2 Important rendering rule

Do not falsely claim that Android Chrome/WebView is pixel-identical to desktop Chrome on Windows.

Implement two quality tiers:

### Tier A — Android offload
Use the Android device for:
- responsive layout checks,
- viewport emulation where supported,
- mobile/Android rendering,
- screenshot capture,
- image diff preprocessing,
- DOM/layout metadata collection,
- network/console diagnostics where practical.

This is useful for finding responsive defects and removing substantial work from the host.

### Tier B — Desktop fidelity validation
For final Windows-desktop pixel fidelity:
- keep one or more key desktop viewport validations on the host, or
- later support a separate desktop/cloud worker.

Tidal Bridge should clearly label which rendering engine produced each screenshot.

## 14.3 Local development server access

If the app is running on the laptop at, for example, port 3000, automatically create an ADB reverse mapping so the phone can reach it without exposing the dev server to the LAN.

Example concept:

```text
phone localhost:3000 -> adb reverse -> laptop localhost:3000
```

Manage these mappings automatically per job.

## 14.4 Screenshot result bundle

Return:

```text
screenshots/
  phone-small.png
  phone.png
  tablet-portrait.png
  laptop-13.png
  ...
manifest.json
console.json
network-summary.json
layout-metadata.json
```

where supported.

---

## 14.5 Multi-Device Browser Farm

When multiple Android workers are connected and browser-capable, Tidal Bridge should be able to fan out UI checks across **real devices** rather than emulating every target on one host browser.

Examples:

```text
Samsung phone  -> Android Chrome / mobile viewport set
Pixel phone    -> Android Chrome / second hardware/browser profile
Android tablet -> tablet viewport set
Host desktop   -> Windows desktop fidelity checks
```

Requirements:

- schedule independent viewport/browser cases in parallel when safe,
- record the exact physical device, Android version, browser version, DPR, viewport, orientation, and capture timestamp with every screenshot,
- distinguish **real-device rendering** from viewport emulation,
- never claim Android Chrome results prove Windows desktop Chrome pixel fidelity,
- support a matrix that combines real-device checks and host desktop checks,
- avoid running redundant captures when a device is thermally constrained.

This feature should become a personal local browser farm for polish/QA workflows.

# 15. Agent Integration — Codex and Claude Code

Both agents need to know Tidal Bridge exists without the user re-explaining it in every conversation.

Implement an MCP server named:

```text
tidalbridge
```

Expose tools conceptually similar to:

```text
tidalbridge_status
tidalbridge_capabilities
tidalbridge_explain_route
tidalbridge_run
tidalbridge_run_profile
tidalbridge_sync_workspace
tidalbridge_render_matrix
tidalbridge_cancel_job
tidalbridge_job_status
tidalbridge_benchmark
```

The MCP server should talk to the local host daemon rather than implementing scheduling itself.

## 15.1 Agent policy

Generate appropriate persistent/project instructions for Codex and Claude Code that say, in substance:

> Tidal Bridge is available as an auxiliary compute worker. Before launching resource-heavy shell, test, lint, type-check, build, data-processing, screenshot, browser-QA, or parallelizable jobs locally, check Tidal Bridge availability. Prefer Tidal Bridge when the scheduler reports that remote execution is compatible and beneficial. Do not use it for architecture-incompatible or Windows-specific jobs. If Tidal Bridge is unavailable, continue locally without blocking.

Use the current officially supported configuration mechanism for each product.

Do not rely only on natural-language instructions; the host layer must still provide automation and routing for integrations that call it.

---

# 16. Transparent / Automatic Offloading

The end-state should require minimal agent cooperation.

Implement automation in layers.

## Layer 1 — Explicit Tidal Bridge CLI/MCP
Always available and safest.

Example:

```text
tidalbridge run -- npm test
tidalbridge run -- pytest
tidalbridge render-matrix --url http://localhost:3000/dashboard
```

## Layer 2 — Agent-aware preference
Codex and Claude Code receive persistent configuration/instructions to prefer Tidal Bridge for suitable expensive jobs.

## Layer 3 — Optional command shims
Provide an **opt-in** transparent mode for known safe commands.

Potential profiles:

```text
pytest
ruff
mypy
eslint
prettier
tsc
vitest
jest
selected npm/pnpm/yarn scripts
selected Python scripts
```

Do not silently replace every executable on the system.

The shim must:

1. ask the local scheduler,
2. execute remotely only if eligible,
3. otherwise execute the real local binary,
4. preserve exit codes,
5. preserve stdout/stderr behavior,
6. avoid recursion,
7. support a bypass environment variable such as:

```text
TIDALBRIDGE_DISABLE=1
```

Also provide:

```text
TIDALBRIDGE_FORCE_LOCAL=1
TIDALBRIDGE_FORCE_REMOTE=1
```

Force-remote should still reject unsafe/incompatible execution rather than doing something invalid.

## Layer 4 — Future integrations
Design extension points for IDEs, CI workers, additional Android phones/tablets, other laptops, or cloud/VPS workers. Android workers are the primary scope, but the scheduler abstractions should not preclude future node types.

---

# 17. General Laptop Acceleration Beyond Agents

Tidal Bridge is a generic job execution platform, not merely an AI-agent plugin.

Provide:

- CLI,
- local API,
- MCP,
- documented SDK/protocol.

This allows scripts and future applications to submit eligible jobs.

Do not attempt OS-wide interception of arbitrary Windows processes in the MVP.

That would be brittle and unsafe.

Instead, make the offload layer easy enough that development tools can use it explicitly or through targeted shims.

---

# 18. Resource, Battery, Thermal, and Health Policy

Protect every worker device. Policies are evaluated **per device**, not globally.

Create configurable defaults such as:

```yaml
resource_policy:
  reserve_device_ram_mb: 2000
  default_max_parallel_jobs_per_device: 2
  require_charging_for_long_jobs: false
  pause_on_critical_thermal_state: true
  reject_on_low_battery_percent: 15
  max_storage_utilization_percent: 90
```

Do not rely on guessed temperature sensor paths. Use the most reliable Android/ADB/native APIs available on the detected Android/API level and degrade gracefully when a metric is unavailable.

Each device should expose a health state such as:

```text
HEALTHY
WARM
THERMALLY_LIMITED
LOW_BATTERY
LOW_MEMORY
LOW_STORAGE
UNSTABLE
DRAINING
OFFLINE
```

When thermal or resource state becomes unsafe:

- stop accepting new heavy jobs on that device,
- allow lightweight jobs only if policy permits,
- optionally allow the active job to finish if safe,
- otherwise cancel/requeue/checkpoint according to job policy,
- route new work to another compatible node or local execution,
- explain the reason in status/dashboard.

# 19. Parallelism and Multi-Device Pooling

Parallelism must be adaptive at two levels:

1. **inside each worker**, based on its real resource/thermal headroom, and
2. **across workers**, allowing independent jobs to fan out to multiple devices.

Start conservatively. Example per-device policy:

- one heavy job at a time,
- up to two light jobs if headroom is good,
- dynamic reduction when thermal state worsens,
- configurable concurrency ceiling,
- refine from benchmark and execution history.

Do not create N workers merely because a device reports N logical cores.

When multiple devices are READY, the scheduler should compare all candidates and may run jobs concurrently across them. This must support scenarios such as:

```text
Laptop       -> Windows-specific build
Android A    -> pytest suite
Android B    -> TypeScript type-check
Android Tab  -> browser screenshot matrix
```

A disconnect or drain event for one worker must not block unrelated jobs on the rest of the pool.

---

# 20. Workspace Sync and Dependency Caching

Repeated full repository copies will destroy the benefit of offloading.

Implement a **content-addressed workspace cache** per device:

- workspace identity,
- file manifest,
- content hashes,
- per-device cache index,
- ignore rules,
- incremental sync,
- cached dependency directories when safe,
- task-specific input sets where possible,
- changed-files-only transfer after the first sync,
- cache hit/miss accounting so transfer cost can feed scheduler decisions.

Respect:

```text
.gitignore
.tidalbridgeignore
```

Provide a default `.tidalbridgeignore` that excludes obvious host-only/heavy folders unless needed.

Be careful with:

```text
node_modules
.venv
dist
build
.next
coverage
large media
IDE folders
secrets
```

Do not copy Windows `node_modules` or a Windows virtualenv to ARM64 Android.

Install/cache platform-specific dependencies on the worker.

---

# 21. Dependency Environment Profiles

Create reproducible remote environments.

For Node projects:

- detect package manager and lockfile,
- maintain an ARM64 worker dependency cache keyed by lockfile hash,
- do not reuse host-native dependency trees.

For Python projects:

- create worker-side virtualenv/environment,
- key by dependency file hash + Python version + architecture.

Support at least:

```text
requirements.txt
pyproject.toml
package-lock.json
pnpm-lock.yaml
yarn.lock
```

Gracefully report packages that have no compatible Android/ARM64 path.

## 21.1 Automatic Environment Provisioning

The user should not have to manually open Termux and install routine dependencies for each project. The environment manager should be able to provision **approved, reproducible, project-scoped worker environments** when a job requires them.

Conceptual flow:

```text
job requirements
    -> environment lookup
    -> cache hit: reuse
    -> cache miss: provision compatible runtime/dependencies
    -> verify versions
    -> execute
    -> retain environment for future compatible jobs
```

Requirements:

- never install arbitrary untrusted packages outside the worker sandbox without clear policy,
- key environments by architecture + runtime version + lock/dependency hash,
- include provisioning time in offload cost,
- make provisioning cancellable and observable,
- invalidate only when inputs/runtime/worker version require it.

---

# 22. Result Semantics

A remote task can produce:

- exit code,
- stdout,
- stderr,
- timing,
- CPU/RAM metrics,
- files/artifacts,
- a patch/diff,
- screenshots,
- structured JSON.

Do not blindly overwrite the host repository from the worker.

For source modifications, prefer:

1. return a patch/diff,
2. validate it,
3. apply on host,
4. detect conflicts.

For generated non-source artifacts, sync only declared outputs.

---

# 23. Security Requirements

This project can execute code, so security is not optional.

Implement:

- ADB-authorized USB device only by default,
- explicit device allow-list,
- localhost binding,
- worker authentication token/session secret,
- no LAN exposure by default,
- command audit log,
- job IDs,
- timeouts,
- cancellation,
- working-directory isolation,
- path traversal protection,
- secret-file exclusions,
- configurable command deny rules,
- no root requirement,
- no arbitrary access to personal phone storage,
- no execution from untrusted network clients.

Do not transmit `.env`, SSH keys, browser profiles, credential files, or other obvious secrets unless explicitly allowed.

Create `SECURITY.md`.

---

# 24. Failure Handling

Handle at least:

- cable unplugged during job,
- ADB daemon restart,
- device unauthorized,
- worker killed by Android,
- phone locked,
- phone reboot,
- Termux killed,
- dependency install failure,
- architecture mismatch,
- out-of-memory,
- low storage,
- thermal throttling,
- worker version mismatch,
- host shutdown,
- stale port forwarding,
- stale workspace,
- command timeout,
- app dev server not reachable,
- conflicting host edits while a remote job runs.

Never leave the system in a state where the user cannot tell whether a job ran locally, remotely, or failed.

## 24.1 Graceful Requeue / Migration

When a worker disappears mid-job, do not blindly mark everything failed. Use job semantics:

- if `idempotent && retryable`, requeue to another compatible worker or local execution,
- if outputs may be partially written, discard/validate the remote attempt before retry,
- if `resumable`, resume only from a verified checkpoint,
- if destructive or non-idempotent, stop and report rather than duplicating side effects.

Every migration/retry must preserve the original job ID plus an attempt ID and audit trail.

---

# 25. Observability, Monitoring, and Telemetry

Monitoring is a **core control input to the scheduler**, not decoration. Build three telemetry layers.

## 25.1 Host Monitoring

Track at minimum:

- total and per-core CPU pressure where practical,
- RAM used/available and memory pressure,
- pagefile/swap pressure,
- disk I/O pressure,
- host queue depth,
- Tidal Bridge daemon overhead,
- optionally the major development processes contributing to pressure.

## 25.2 Worker Monitoring

For every Android device, track when available:

- online/transport state,
- worker health/heartbeat,
- CPU load,
- RAM total/available,
- storage available,
- battery percent, charging state,
- thermal status/headroom,
- active job count,
- transport throughput/latency,
- worker/runtime versions.

## 25.3 Job Telemetry

For every job/attempt, record:

- selected execution target,
- scheduling explanation,
- start/end/duration,
- exit code/status,
- stdout/stderr references,
- input/output bytes transferred,
- sync/setup duration,
- CPU/RAM observations where available,
- retry/migration/fallback reason,
- cache hit/miss state,
- estimated vs actual performance.

Provide:

```text
tidalbridge status
tidalbridge devices
tidalbridge device <id>
tidalbridge jobs
tidalbridge job <id>
tidalbridge explain <job-id>
tidalbridge doctor
tidalbridge benchmark
tidalbridge logs
```

`tidalbridge status` should be immediately useful and multi-device aware, e.g.:

```text
Tidal Bridge: READY

Host
  CPU: 91%
  RAM: 15.4 / 15.8 GB
  Queue: 4

Workers
  s23-ultra-a1   READY   CPU 24%   RAM free 5.9 GB   thermal nominal
  pixel-b2       BUSY    CPU 63%   RAM free 3.1 GB   thermal warm

Scheduler
  Local jobs: 1
  Remote jobs: 2
  Queued: 1
  Offload ratio this session: 58%
  Host RAM avoided: estimated 2.3 GB
```

Clearly label estimates as estimates. Use bounded retention and low-overhead sampling.

# 26. Dashboard

After CLI/core stability, build a **polished, modern, visually excellent dashboard without turning the UI into a permanent resource tax**. A low-power host is a primary target machine, so aesthetics and efficiency are both hard requirements.

The dashboard lifecycle must be decoupled from the always-on host daemon:

- the daemon must work fully with the dashboard closed,
- closing the dashboard must release almost all UI-specific memory/CPU,
- never require an always-running Electron/Chromium shell,
- do not keep a hidden browser window alive merely for tray/status behavior,
- prefer a small native shell or an on-demand localhost web UI opened in the user's existing browser, whichever produces the best measured idle footprint and maintainability,
- if a native shell is selected, evaluate lightweight options such as Tauri or a native Windows UI rather than Electron,
- dashboard polling must not drive host overhead; use push/event streams where practical and dynamically reduce update frequency when the page is not visible.

The UI must still feel like a real product, not a developer debug panel. Apply a coherent design system with:

- clean information hierarchy and strong typography,
- excellent dark and light themes,
- compact but readable resource cards,
- tasteful micro-interactions only where they communicate state,
- smooth but restrained transitions,
- clear ONLINE / DEGRADED / THROTTLED / OFFLINE / DRAINING states,
- small real-time CPU/RAM/thermal/job-throughput visualizations,
- responsive layouts from laptop to large desktop widths,
- keyboard accessibility and sensible focus states,
- no decorative animation, blur, canvas effect, or chart refresh that measurably harms weak laptops,
- virtualized/bounded rendering for long job/history lists,
- lazy-load secondary views and heavy visualization code.

The visual goal is **premium and calm, not flashy**. Prefer precision, density control, useful whitespace, and immediate state comprehension over ornamental effects.

## Tidal Bridge visual system

The visual identity should communicate **two independent nodes connected by an energetic bridge**. The UI should feel astronomical and technical without becoming a sci-fi game interface. Use color primarily to clarify topology, state, and flow.

### Core dark-theme palette

- **Void Navy** `#070B18` — main application background.
- **Deep Orbit** `#0D1630` — cards, panels, elevated surfaces.
- **Tidal Cyan** `#42D3FF` — source-node / discovery / active-link accent.
- **Bridge Violet** `#8B5CF6` — destination-node / orchestration / scheduler accent.
- **Tidal Gradient** `#42D3FF → #8B5CF6` — use sparingly for the visual bridge between nodes, selected topology paths, and primary brand moments.
- **Stellar White** `#F5F7FF` — primary text on dark surfaces.
- **Nebula Slate** `#8C98B8` — secondary text and muted telemetry.
- **Orbit Border** `#24304D` — subtle borders/dividers.

### Semantic colors

- **Healthy / Ready** `#42D392`
- **Warning / Thermal pressure** `#F5C451`
- **Critical / Failed** `#FF667A`
- **Offline / Disabled** `#667085`

### Light-theme counterparts

- **Cloud Field** `#F7F9FF` — app background.
- **Surface White** `#FFFFFF` — cards and elevated surfaces.
- **Deep Ink** `#111827` — primary text.
- **Muted Orbit** `#667085` — secondary text.
- **Tidal Blue** `#0A84FF` — accessible light-theme source accent.
- **Bridge Violet** `#6D5EF9` — accessible light-theme destination/orchestration accent.
- **Light Border** `#DDE3F0` — dividers and card borders.

### Color semantics and topology

- represent the workstation/source side primarily with **Tidal Cyan**,
- represent Android/remote worker nodes primarily with **Bridge Violet**,
- represent an active or selected route with the restrained cyan-to-violet **Tidal Gradient**,
- use semantic status colors only for health/state; do not overload cyan/violet as success/failure colors,
- keep most surfaces neutral/dark so telemetry and topology remain readable,
- charts should inherit the same system rather than introducing a rainbow palette.

### Brand motif

The recurring motif is **two compact nodes joined by a narrow luminous bridge/arc**. This can inform the logo, empty states, topology view, loading state, and selected-route indicator. It must stay abstract and minimal. Avoid literal galaxy illustrations, star-field wallpapers, excessive glow, animated particles, parallax, heavy blur, and large continuously animated gradients.

### Performance requirement for visual effects

Brand styling must not violate the low-overhead requirement. Prefer static CSS gradients, simple vector/SVG geometry, opacity/transform transitions, and compositor-friendly motion. Any glow/blur must be subtle, bounded, and disabled or reduced when measured GPU/CPU cost is non-trivial. The dashboard must remain visually strong even with all non-essential animation disabled.

Dashboard should show:

- all worker devices and states,
- model/Android/API/ABI/transport metadata,
- host CPU/RAM/disk pressure,
- per-device CPU/RAM/battery/thermal/storage,
- active/queued/recent jobs,
- local vs per-device routing,
- scheduling explanations,
- transfer volume and cache hit rate,
- execution duration and historical comparison,
- estimated host CPU/RAM relief,
- failures/retries/migrations,
- controls to pause all offloading,
- set `AUTO`, `CONSERVATIVE`, `PERFORMANCE`, or `BATTERY_SAVER` mode,
- drain/disable an individual worker,
- cancel jobs,
- force local for a job/session,
- view calibration/profile information.

A Windows tray icon is optional after core functionality works.

---

# 27. Windows Auto-Start

Provide a robust installer script.

The host daemon must start automatically.

Evaluate:

- Windows service,
- Task Scheduler at login,
- Startup registration.

Choose the least fragile approach that does not require unnecessary privilege.

The setup should be idempotent.

Provide:

```text
scripts/install-startup.ps1
scripts/uninstall.ps1
```

---

# 28. Android Device Bootstrap UX

Each Android device may require a **one-time** manual process because Android requires user authorization for USB debugging. The wizard must work per device and must not assume Samsung-specific menus except as optional help text.

Provide a precise setup wizard/checklist:

1. enable Developer Options,
2. enable USB debugging,
3. connect cable,
4. approve this computer's RSA key,
5. install/configure Termux from a maintained source,
6. install worker dependencies,
7. configure worker,
8. exempt from aggressive battery restrictions if required,
9. run a connectivity test.

After initial setup for a device, normal reconnection should be automatic and its saved Device Profile should be reused when still valid.

Do not require the user to open Termux and type commands every development session if it can be avoided.

---

# 29. Benchmarking

We need evidence that Tidal Bridge helps rather than merely moving overhead around.

Build two benchmark layers:

1. a **short calibration suite** used to qualify a new/changed device, and
2. a **deeper comparative benchmark suite** used for tuning and evidence.

Compare local vs each candidate Android device for representative jobs:

- Python CPU task,
- Python test suite,
- TypeScript type-check,
- ESLint,
- unit tests,
- repository search,
- compression,
- image processing,
- screenshot matrix,
- dependency-cached second run.

Measure:

```text
wall time
host CPU average/peak
host RAM average/peak
phone CPU average/peak where available
phone RAM use
bytes transferred
setup/sync overhead
thermal change
job success
cold-cache vs warm-cache behavior
transport bandwidth/latency
```

Primary goal is not always lower wall-clock latency.

The primary practical goal is often:

> Keep the laptop responsive and create capacity for multiple coding agents.

Therefore report both:

```text
speed benefit
host resource relief
```

---

# 30. Scheduling Benchmark History

Persist lightweight historical data so the scheduler can later learn:

```text
task signature
host fingerprint
device stable ID/profile version
local duration
remote duration
sync cost
bytes transferred
cache state
failure/retry rate
host RAM relieved
worker thermal impact
```

Do not build ML initially.

Use historical moving averages/percentiles/heuristics first. Separate cold-cache and warm-cache observations and do not let one unusually fast/slow run dominate routing.

---

# 31. MVP Acceptance Tests

The first end-to-end milestone is complete only when all feasible tests below pass.

## Connection and device profiling

- Start Windows host daemon.
- Connect the authorized S23 Ultra reference device by USB.
- Tidal Bridge discovers it automatically without hard-coded model logic.
- A stable Device Profile is created.
- A bounded calibration run populates initial performance/capability data.
- Worker reaches READY without repetitive manual commands.
- Unplugging updates state correctly.
- Replugging reconnects automatically and reuses the valid profile.
- A mock or second real Android device with different capabilities is handled through the same generic code path.

## Multi-device

- With two READY workers (real or one real + mock), scheduler can choose either by capability/performance.
- Independent eligible jobs can execute concurrently on different workers.
- Draining/disconnecting one worker does not stop unrelated jobs on the other.
- Core scheduling code contains no Samsung/S23-specific branch.

## Remote execution

Run on Android through Tidal Bridge:

```text
python -c "print('hello from phone')"
node -e "console.log('hello from phone')"
git --version
```

Return correct stdout, stderr, exit code, and timing.

## Workspace

- Sync a small test repository incrementally.
- Change one host file.
- Second sync transfers only required changes.
- Execute tests remotely.
- Return result.

## Fallback

- Disconnect phone.
- Submit an eligible job with local fallback.
- It executes locally and clearly reports fallback.

## Compatibility

- Submit a Windows-only job.
- Scheduler must not incorrectly run it on Android.

## Resource pressure and offload economics

- Simulate host high RAM/CPU pressure.
- Verify scheduler preference changes toward a compatible worker.
- Verify a tiny job with a large cold sync requirement stays local when transfer cost outweighs benefit.
- Verify the same job may become remote after the workspace/dependency cache becomes warm.
- Verify thermal/resource pressure can divert a job from one Android worker to another or local execution.

## Screenshot QA / browser farm

- Start a local test web app on host.
- Automatically create required reverse port(s).
- Render/capture multiple Android-side viewport checks where supported.
- If multiple browser-capable devices are present, distribute independent cases across them.
- Return a labeled screenshot bundle with device/browser/viewport/DPR metadata.
- Keep desktop-fidelity validation separate.

## MCP

- Codex can list/use Tidal Bridge MCP tools.
- Claude Code can list/use Tidal Bridge MCP tools.
- Agent instructions encourage appropriate use.
- If the phone is absent, agents continue normally.

## Startup

- Reboot/log out as appropriate.
- Tidal Bridge host component starts automatically.

---

# 31.1 User Scheduling Modes

Expose simple high-level modes instead of forcing the user to tune dozens of knobs:

```text
AUTO
  Balanced adaptive scheduling using measured benefit.

CONSERVATIVE
  Offload only when benefit/compatibility confidence is clearly high.

PERFORMANCE
  Use compatible Android workers aggressively while respecting safety limits.

BATTERY_SAVER
  Prefer local execution unless devices are charging or policy explicitly permits the job.
```

Modes adjust policy weights/thresholds; they must never bypass compatibility, security, thermal, or destructive-job safeguards.

# 32. Performance Guardrails

The cure must not become another source of laptop pressure. Treat host overhead as a first-class acceptance criterion and continuously benchmark Tidal Bridge itself.

## 32.1 Constrain orchestration overhead, not useful compute

Resource limits must apply primarily to the **control plane** (scheduler, device discovery, queueing, telemetry, sync coordination, cache metadata, and the local API), not to useful remote work running on Android devices. Do **not** impose a simplistic global hard cap that causes Tidal Bridge to stall or reject useful work under load.

Use **resource budgets + adaptive guardrails**:

- idle RAM target: **< 100 MB**,
- normal daemon/control-plane RAM target: **< 200 MB** when practical,
- sustained **250–300 MB** should trigger a warning/diagnostic path rather than an immediate failure,
- temporary short-lived bursts above the normal budget are allowed when justified by sync, indexing, compression, or recovery,
- **~500 MB attributable to Tidal Bridge should be treated as a regression, runaway condition, or exceptional diagnostic state — not a normal operating target**,
- long-run idle CPU average should be approximately **0%**, allowing only short event/health-check spikes,
- no high-frequency polling loops when idle,
- prefer event-driven device/process notifications where practical,
- adaptive telemetry frequency: slow when idle, faster only while jobs or thermal transitions are active.

The daemon must remain lightweight under load by **queueing, streaming, caching, and backpressure**, not by refusing useful work merely because of an arbitrary fixed memory ceiling.

## 32.2 Queueing and backpressure

Incoming jobs must enter a bounded scheduler queue. Concurrency is capacity-aware per node and per workload class. If 20 jobs arrive simultaneously, Tidal Bridge must not spawn 20 heavy orchestration paths. It should admit work progressively according to available device capacity, host pressure, historical memory use, and policy.

Required behavior:

- bounded in-memory queue with persistent recovery metadata where needed,
- configurable concurrency per device and workload class,
- backpressure when host or worker capacity is saturated,
- no unbounded thread/process/task creation,
- idempotent jobs may be safely requeued or retried after interruption,
- non-idempotent/destructive jobs require explicit policy and must never be blindly replayed.

## 32.3 Streaming and bounded memory

Never load large payloads into daemon memory when streaming is possible. File sync, logs, stdout/stderr, archives, screenshots, artifacts, and large repository data should use chunked/streaming I/O with bounded buffers.

Examples:

```text
CORRECT
Disk -> small bounded chunk -> USB/Wi-Fi transport -> device

WRONG
4 GB file -> 4 GB host RAM buffer -> device
```

Large histories and telemetry must live primarily in SQLite or compact on-disk storage. Keep only the active working set in RAM. Raw high-frequency telemetry should be downsampled/aggregated over time rather than retained indefinitely.

## 32.4 Graceful degradation under host pressure

When the host is under memory or CPU pressure, Tidal Bridge must protect responsiveness by degrading optional control-plane work before essential routing:

1. reduce telemetry sampling frequency,
2. pause non-critical background analysis/benchmarking,
3. defer low-priority cache maintenance and compaction,
4. reduce scheduler concurrency,
5. preserve device heartbeat, queue integrity, job routing, and recovery state.

The host daemon should remain operational even when the laptop is already memory-constrained.

### Learn once, reuse aggressively

Do not repeatedly spend CPU re-solving decisions already learned. Persist compact device profiles, calibration results, workload fingerprints, scheduler outcomes, environment fingerprints, and performance history in a lightweight local store such as SQLite.

Use a scheduler **fast path** for a known workload on known devices: validate only volatile constraints (availability, free RAM, thermal/battery state, changed inputs/environment) and reuse the cached routing decision when still valid. Use the full cost/capability analysis only on the **slow path** for new, changed, low-confidence, or contradicted cases.

Recompute when evidence changes; do not recompute merely because another similar job arrived.

### UI budget

The dashboard is on-demand. When it is closed, UI-specific CPU should be zero and UI-specific memory should be released. When open, it must remain responsive and visually polished without materially affecting scheduler measurements or competing with developer workloads. Measure dashboard overhead separately from daemon overhead in benchmarks.

Use bounded queues.

Avoid Node/Electron for an always-on desktop shell unless there is an extraordinary, measured justification.

---

# 33. Implementation Order

Work in this exact spirit: prove transport, profiling, and scheduling early; then expand.

## Milestone 1 — Vertical slice

Build:

- host daemon,
- generic ADB device discovery,
- worker bootstrap,
- heartbeat,
- versioned capability discovery,
- one generic remote shell job,
- CLI status/run,
- fallback behavior.

Prove it on the reference S23 Ultra if available, but do not encode model assumptions.

## Milestone 2 — Device profiling and calibration

Add:

- stable device identity,
- Device Profile persistence,
- bounded calibration suite,
- transport measurement,
- runtime/capability inventory,
- mock profiles for heterogeneous devices.

## Milestone 3 — Workspace and environment execution

Add:

- content-addressed incremental sync,
- isolated worker workspace,
- Python/Node profiles,
- automatic environment provisioning,
- dependency caches,
- output/artifact handling.

## Milestone 4 — Adaptive scheduler

Add:

- host telemetry,
- per-device telemetry,
- compatibility detection,
- offload-benefit model,
- route explanation,
- history store,
- cold/warm cache awareness.

## Milestone 5 — Codex / Claude integration

Add:

- MCP server,
- current supported configs,
- AGENTS.md/CLAUDE.md guidance,
- end-to-end MCP tests.

## Milestone 6 — UI polish / browser worker

Add:

- host dev-server reverse mapping,
- Android browser/WebView/Chrome QA mechanism,
- viewport matrix,
- screenshots,
- metadata,
- image diff support.

Do not misrepresent Android rendering as Windows desktop rendering.

## Milestone 7 — Multi-device pool

Add:

- multiple concurrent READY workers,
- cross-device candidate scoring,
- per-device drain/disable,
- job fan-out,
- multi-device browser farm,
- disconnect isolation.

## Milestone 8 — Automation and UX

Add:

- Windows auto-start,
- automatic worker reconnect/start,
- opt-in known-command shims,
- scheduling modes,
- dashboard,
- tray/status enhancements if useful.

## Milestone 9 — Native Android worker path

After the Termux MVP is proven, evaluate and implement a native Android worker/runtime path if it materially improves lifecycle, telemetry, provisioning, or user experience. Keep the host protocol stable.

## Milestone 10 — Optimization and public readiness

Benchmark and improve:

- transfer strategy,
- dependency caching,
- concurrency,
- scheduling heuristics,
- reliability,
- thermal handling,
- onboarding on multiple manufacturers/models,
- documentation and contribution guidelines for open-source release.

---

# 34. Coding Standards

Use:

- strict typing,
- clear interfaces,
- structured errors,
- structured logs,
- unit tests for scheduling/policy,
- integration tests for protocol,
- end-to-end tests with a mock Android worker,
- real-device test scripts,
- versioned protocol,
- no hidden magic.

Keep platform-specific code isolated.

Do not bury critical behavior in shell scripts when it belongs in tested application logic.

---

# 35. Configuration

Provide a user-editable config, likely YAML/TOML. Keep the common path simple while allowing per-device overrides.

Example:

```yaml
mode: auto

devices:
  auto_connect: true
  allow_multiple: true
  approved: []

scheduler:
  enabled: true
  remote_preferred_under_host_pressure: true
  host_cpu_pressure_percent: 80
  host_ram_pressure_percent: 85
  minimum_offload_benefit_score: 0.15
  history_enabled: true

worker_defaults:
  reserve_ram_mb: 2000
  max_parallel_jobs: 2
  min_battery_percent: 15
  heavy_jobs_prefer_charging: true
  stop_new_heavy_jobs_on_thermal_limit: true

per_device_overrides: {}

sync:
  incremental: true
  content_addressed_cache: true
  respect_gitignore: true

environments:
  auto_provision: true

automation:
  command_shims: false

dashboard:
  enabled: true
  bind: "127.0.0.1"
```

Validate config and provide sane defaults. The user should not need per-model configuration for ordinary supported Android devices.

---

# 36. Developer Commands

Create a simple developer experience, e.g.:

```text
tidalbridge doctor
tidalbridge setup device
tidalbridge setup agents
tidalbridge devices
tidalbridge device <id>
tidalbridge benchmark --device <id>
tidalbridge explain <job-id>
tidalbridge service install
tidalbridge service start
tidalbridge status
tidalbridge benchmark
```

The exact CLI may differ, but keep it coherent.

---

# 37. What You Must NOT Do

Do not:

- hard-code Samsung/S23 behavior into core logic,
- assume a single Android worker exists forever,
- offload merely because a worker is connected,
- ignore transfer/setup/cache cost,
- fake shared RAM,
- claim the phone is a normal Windows co-processor,
- require root for MVP,
- expose an unauthenticated shell over Wi-Fi/LAN,
- silently transfer secrets,
- copy Windows-native dependency directories to ARM64,
- execute Windows-specific binaries on Android,
- assume Android Chrome equals desktop Chrome,
- require manual per-chat prompting,
- make Electron the always-on core,
- copy whole repos on every job,
- spin continuously at high CPU while idle,
- abandon local fallback,
- stop after writing a design document.

---

# 38. Definition of Success

Tidal Bridge succeeds if the user can:

1. boot Windows,
2. connect any supported authorized Android device,
3. see it discovered, profiled, calibrated when needed, and become READY automatically,
4. connect a second supported device and see it join the same worker pool without redesign or model-specific configuration,
5. open Codex or Claude Code,
6. work normally,
7. have suitable expensive workloads routed to the best compatible execution target,
8. continue working if one or all Android workers disappear,
9. inspect exactly what was offloaded and why,
10. observe lower host CPU/RAM pressure during parallel agent workflows,
11. see scheduling improve from real execution history,
12. use real Android devices as a local browser/QA farm where appropriate.

The system should feel like connected Android devices **add usable compute headroom** to the workstation, while remaining technically honest that it does this through distributed execution rather than physical CPU/RAM pooling.

The reference S23 Ultra must be a successful first node, not a special case required by the product.

---

# 39. First Action — Start Building Now

Do not reply with only a plan.

Begin by:

1. inspecting the current machine/toolchain,
2. creating the repository,
3. writing a concise architecture decision record,
4. implementing generic multi-device discovery and the host-worker handshake,
5. defining/versioning `WorkerNode`, `DeviceProfile`, `CapabilitySet`, and job schemas,
6. implementing mock workers with different capabilities if a real device is not yet connected,
7. implementing a real ADB/Termux path when available,
8. creating `tidalbridge doctor`,
9. creating `tidalbridge status`,
10. successfully running one remote command end-to-end,
11. persisting the first real Device Profile and calibration result,
12. proving the code path has no S23-specific scheduler assumption,
13. committing/testing the vertical slice before expanding.

When a manual user action is unavoidable (for example approving USB debugging or installing Termux), stop only at that boundary, tell the user the exact minimal action required, and continue automatically once the prerequisite is satisfied.

At each milestone:

- run tests,
- report what works,
- report what is still simulated,
- report measured host overhead,
- update the documentation,
- continue to the next milestone unless genuinely blocked.

The goal is a real working system, not a concept demo.
