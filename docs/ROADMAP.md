# Roadmap

The phone is an optional helper: when it is connected the laptop gets lighter, and when it is not, every project works exactly as before. Databases, queues and anything else that holds state never move to the phone.

| Phase | Contents | Status |
|---|---|---|
| 0. Baseline | Tag `v0.1-baseline` before changing behavior | Done |
| 1. Universal | Checks and tests detected in every project, settings outside projects, trust by verification, measuring new commands | Done for Node and Python |
| 2. Resilience | Dev servers fail over to the laptop, phone-friendly limits (fewer jobs while the phone is in use), `doctor` | Next |
| 3. Speed and isolation | Native Node instead of `proot` for Node tools (a 1,097-test Vitest suite went from 166 s to 104 s); jobs isolated from the phone's own data | Speed done; isolation planned |
| 4. More work types | Copy-back for formatters and fixers (done; code generators through task files); builds; Go, Rust, Java, .NET | Copy-back done |
| 5. Agent-side services | Forgotten-process cleaner (done); headless browser and MCP relay; optional local-model endpoint | Cleaner done |
| 6. Product | One-command install and update, no Termux needed (done); several phones and wireless debugging (supported, untested); macOS and Linux laptops (build; service and file watching to do) | Install done |

## Known boundaries

| Area | Boundary / next step |
|---|---|
| Phone executor | Node tools run natively; Python tools, installs and scripts with shell features still pay `proot`'s ~0.4 ms per traced file-system call. Keep the Termux worker as the sandboxed alternative. |
| Sustained load | A phone without a fan throttles after minutes of full load; long suites belong on the laptop unless it is busy. Heat pacing and evacuation protect the phone; measured history keeps slow, hot commands local. |
| Phone lifecycle | The host restarts the ADB-shell worker through `adb shell`; it needs USB debugging and the cable. Wireless debugging and a second phone are untested. |
| Interception | Adapters reach Claude Code (hooks) and Codex (command environment). Windows puts the system PATH before the user PATH, so other agents need their own integration or a terminal whose PATH starts with the adapter directory. Absolute runtime paths bypass adapters. |
| Scheduler | Add percentiles, adaptive worker concurrency and memory-aware placement across concurrent projects. |
| Cache lifecycle | Workers keep 200 job directories and persistent trees indefinitely. Add disk quotas and pruning of unused projects. |
| Transfer | First syncs upload one file per request (six in flight). A batched upload would shorten first syncs of large projects. |
| Dependency environments | npm/pnpm/yarn lock files, `uv.lock` and requirements files. Add Poetry and mirroring of an existing virtual environment's exact versions. |
| Browser farm | Real Android capture works; a headless Chromium in the userland renders like Linux, not Windows. |
| Security | Universal mode runs more project code on the phone. `.env` files stay on the laptop unless shared per project; stronger job isolation is part of phase 3. |
| Native builds | Never route Windows artifacts to Android by heuristic. |
