# Measured benchmarks and acceptance status

## Native Node engine (2026-10-02, evening)

A Next.js 16 frontend with a Vitest 4 suite (123 files, 1,097 tests), detected by universal mode with no task file; same commands and project settings on both sides.

| Command | Laptop | Phone, `proot` | Phone, native |
|---|---|---|---|
| `npm test`: 1,097 tests, one worker as configured | 123 s | 166 s | 104 s |
| `tsc --noEmit`, non-incremental | | 27.1 s | 21.6 s |
| `npm run typecheck`, incremental, warm | | | 6 s |

`proot` cost the test run 61 s of system time; natively it was 7 s.

Sustained load is the phone's limit. Running a larger project's suite of 621 test files natively, the phone reached Android's critical thermal level after about 20 minutes (2,527 tests done, prime core throttled from 2.7 to 1.2 GHz) and the run was stopped. A phone without a fan suits bursts; the scheduler now backs off at the moderate level, cools down for five minutes after the severe level, and moves running jobs to the laptop at the critical level. With four Vitest workers instead of the configured one, the phone ran the suite in 39 s (the worker now leaves the project's setting alone).

## ADB-shell worker on a real project (2026-10-02)

Laptop: i7-10510U, 16 GB, Windows 11, typically 65–100% loaded by other agents, a Next dev server, Docker and browsers. Phone: SM-S918B (Snapdragon 8 Gen 2), Android 16, connected by USB and charging; ADB-shell worker 0.2.0 with Debian trixie, Node 22.23.3, Python 3.13.5. Workload: a production Next.js frontend (Next 16, React 19, TypeScript 6, ESLint 9, Vitest 4; 2,620 synced files, 50 MB; `node_modules` installed on the worker). Single runs, wall time end to end unless noted; laptop load varied between runs.

| Command | Laptop | Phone | Notes |
|---|---:|---:|---|
| First sync + `npm ci` (607 packages) | — | 245 s | once per project and lockfile |
| `tsc --noEmit`, cold (no incremental state) | — | 200 s | once; the laptop's `.tsbuildinfo` is not synced |
| `npm run typecheck`, warm | 20–38 s | 16–21 s | identical output; laptop run used 30 CPU-s and 1.7 GB peak (whole tree) |
| `npm run lint` | 527 s | 387 s | identical 853 findings after syncing `crypto/messenger/pkg` |
| `vitest run`, 15 files, 168 tests | 30 s | 80 s | all passed on both |
| Next dev server, first compile of `/en` | 3.5 s (partly cached) | 10.6 s | served to the laptop through `adb forward` |
| Next dev server, compiled page | 0.35 s | 0.39 s | phone RSS about 700 MB after one route |

An ordinary `npm run typecheck` (no flags) in the approved project routed to the phone under AUTO once it had measured history: [Tidal Bridge] `npm run typecheck -> worker-44fa3c90…`, 21 s, exit 0.

What made the phone fast enough, measured on the device:

- Android keeps app processes (Termux) on the three efficiency cores: the same single-threaded loop took 3,742 ms in Termux and 834 ms from `adb shell`.
- `proot` traces each file-system call: about 430 µs per `stat` inside it versus 7 µs natively. Running the worker itself natively, with only jobs in `proot`, cut a no-change sync of 2,620 files from 15 s to 0.44 s.
- Keeping jobs off the efficiency cores cut traced-call cost and job time: vitest sample 105 s to 59 s, warm `tsc` 21 s to 17 s.

Limits: single samples under varying load, no confidence intervals. Test runners stay slower on the phone because every test worker loads thousands of modules through `proot`; the scheduler moves them only when the laptop is busy. Sustained heavy use while charging drove the phone to Android's "severe" thermal state once; the scheduler stopped routing to it until it cooled.

## Termux worker (2026-10-01)

Measurements were taken on 2026-10-01 using the Windows workstation and one real SM-S918B phone running Android 16/API 36 and the Termux worker. The phone reported ARM64, Python 3.13.13, Node 26.3.1, and Git 2.54.0. Other mock tiers execute on Windows and cannot establish physical offload benefits.

### Daemon overhead

A 30-second idle sample on version 0.1.2, with one phone connected and the dashboard open, measured average RSS **19.04 MB**, peak RSS **19.12 MB**, and average interval CPU **0.52% of one CPU core**. Telemetry samples every ten seconds, so this is a short observation with correlated snapshots, not a long-run percentile or a browser-memory measurement. It excludes ADB server and browser process resource use. Earlier startup/lifetime-average CPU numbers should not be compared directly with the revised interval sampler.

A final 30-second idle window on **0.1.3** measured **21.06 MB** average RSS, **21.41 MB** peak RSS and **0.31% of one core** average sampled CPU, again with one connected phone and the dashboard open. The same scope and short-window limits apply. [Raw daemon observations](daemon-evidence.json).

### Comparative jobs

The following are a single final run's attempt wall times. They include process start and remote coordination, but exclude admission waiting. The host was already heavily loaded by unrelated work; CPU pressure varied during sequential measurements. Both targets returned correct results and exit status.

| Workload | Windows local | Real Android |
|---|---:|---:|
| Python sum, 4 million iterations | 2600 ms | 2587 ms |
| Python fixture, 2 unit tests | 445 ms | 682 ms |
| Node sum, 8 million iterations | 529 ms | 1441 ms |
| Compress repeated 11 MB payload | 515 ms | 410 ms |
| JSON encode/decode 500,000 integers | 435 ms | 966 ms |
| Allocate 256 MB and hold for 3 seconds | 3742 ms | 3672 ms |

Previous samples had different contention and frequently favored Windows even for the Python/compression cases. These measurements support conservative routing, not a general speedup claim. CPU work, startup cost, transport latency, thermal state, and input size materially affect benefit. The benchmark is reproducible with `scripts/benchmark.py`; raw reports remain under the ignored `benchmark-results` directory, and the sanitized final summary is [benchmark-summary.json](benchmark-summary.json).

The memory test measured local process working set **270.99 MB**, versus Android root-process RSS **267.93 MB**. Its allocation ran on the selected node. This establishes that the job's working set can reside on Android instead of Windows. It does not establish net whole-machine CPU/energy relief or physically shared RAM. The worker samples root RSS every 500 ms; descendant memory and fast transient peaks are not included.

### Functional evidence

- Real Python/Node/Git hello commands, real two-test fixture, stdout/stderr, and explicit exit code 7 were verified.
- A small workspace's first sync transferred 269 bytes; subsequent unchanged sync transferred zero. The automated suite also changes one file and verifies only that file's required bytes transfer.
- Real Python `packaging==25.0` and Node `is-number@7.0.0` provisioned from their registries. Cached second runs imported them without repeating installation.
- Go tests, Go vet, JavaScript syntax, Python compile checks, and the real executable's MCP stdio initialize/list/call passed. Ten tools were listed, and live host status returned.
- Three heterogeneous simulated workers passed independent concurrency, draining, pressure/economic decisions, Windows/architecture/runtime incompatibility, cancellation, timeout, disconnect recovery, preserved attempts, conservative non-replay, authentication, and traversal checks.
- Dashboard checked at 1280×800 and 390×844 with no horizontal overflow; both themes were inspected. [Desktop preview](assets/dashboard-desktop.jpg), [mobile preview](assets/dashboard-mobile.jpg), [light preview](assets/dashboard-light.jpg).
- The host login entry is installed. Actual Windows logout/reboot and physical phone unplug/replug remain untested. Restarting the host and supervised worker was verified.

### Ordinary-command integration, version 0.1.3

The real dashboard source is checked through pinned TypeScript 5.8.3 (`allowJs`, `checkJs`, `noEmit`). Ordinary `tsc --noEmit` invocations selected the real Windows runtime initially; an AUTO run selected Android without a force flag. Additional final checks measured local wall time 1730–2170 ms, local root CPU 3.08–3.14 seconds and root working-set peak 119–133 MB. The earlier warm AUTO Android run took 4962 ms and sampled 163 MB Android root RSS: it was slower in wall time, despite moving the compiler process to Android. It does not demonstrate a universal speedup.

A separate **forced qualification** after the dependency-cache version update installed the pinned compiler and passed in 11231 ms including cold setup/sync. It sampled 169 MB worker root RSS, while the Windows adapter used 0.03125 CPU seconds and 12.32 MB peak RSS. The compiler did not execute on Windows in that run. These are different process scopes; daemon, ADB, browser, descendants and energy are excluded. A later route preview with changed documentation reported `environment_warm: true`, 34080 source bytes to transfer, and LOCAL because predicted benefit was only 8.5%. Correct routing can leave a READY phone idle.

An ordinary `python -m unittest discover -s tests/worker -v` command also routed to the physical phone under **AUTO**, with `force_remote: false`. The three actual worker cache/provisioning contract tests passed in 3.553 seconds inside Python; the remote attempt took 4772 ms including sync/coordination, versus a separately measured Windows suite time of 10.749 seconds. The prior estimate was based on that measured local time. Worker root peak RSS was 25.20 MB; the Windows adapter used 0.046875 CPU seconds and 47.96 MB peak RSS. This test fixture mocks package installation rather than downloading packages, but its test process actually ran on Android. It establishes automatic submission and useful execution, not sustained net laptop memory/energy savings. The samples were sequential under changing unrelated host load, with no statistical confidence interval.

Sanitized job evidence, explicit force flags and measurement scopes are in [automation-evidence.json](automation-evidence.json). Seven executable/HTTP adapter cases passed: exact local output/exit, remote streaming/exit, unavailable daemon, disabled/unknown/recursive commands, ambiguous submission, and accepted-job poll failure without duplicate local execution. Four isolated installer tests passed preservation/repetition/restoration checks. These semantic peers are simulated and offer no hardware relief.

The final warm **AUTO TypeScript check** on 0.1.3 (`3503303b…`, no force flag) passed on Android in **5955 ms**, including 209 ms sync for 150380 changed input bytes. Dependency readiness remained true across those source/documentation changes. Worker root peak RSS was **170.64 MB**; the Windows adapter used **0.046875 CPU seconds** and **11.86 MB** peak RSS. This directly verifies ordinary-command routing and compiler placement while again showing a slower remote wall time than the local samples. Predictions are not actual speedup measurements. A final timing-model revision separates input-sync cost from dependency warmth and invalidates earlier incomparable history classifications.

After that revision, a local ordinary worker-contract run passed in **17.180 seconds** inside Python under higher contention. Two explicitly forced qualification runs populated comparable remote history (test-runner times **4.403** and **3.676 seconds**); their force flags are retained in the evidence. The next ordinary **AUTO** command (`d68154ac…`, `force_remote:false`) then selected the real phone using that history. It passed in **3.660 seconds** inside Python / **4794 ms** per remote attempt, with **25.21 MB** worker root RSS and **0.015625 CPU seconds / 11.94 MB** for the Windows adapter. The reported 59% routing benefit was a prediction, not a statistically measured whole-machine speedup.

### Android browser evidence

The desktop HTTP `/json/new` endpoint failed with HTTP 500 on the tested Chrome. The adapter now creates and closes only its owned tab through the browser CDP Target domain. A physical Android **390×844, DPR 1** capture passed, with Chrome 152.0.7977.82, SM-S918B, Android 16, device/viewport metadata, console/network summaries and automatic mapping cleanup. Its first attempt took 70939 ms. The [actual phone capture](assets/android-phone.png) was visually inspected and its DOM reported no horizontal overflow. Android's reduced user-agent OS string is not the actual OS version; device metadata reports Android 16.

The subsequent ten-case matrix encountered Chrome responsiveness timeouts and was cancelled to release capacity. Sustained matrix reliability is not accepted yet; Chrome must remain responsive and foreground on this device. Do not treat dashboard previews as Android proof, or an Android desktop-sized viewport as Windows desktop fidelity. Claude Code live verification and a second physical Android worker remain pending because neither is available in this setup.
