# Original specification acceptance review

Reviewed against `TIDAL_BRIDGE_BUILD_PROMPT.md` (identical to the original Downloads file), updated 2026-10-01 for version 0.1.3. The requested product is a distributed developer compute pool: boot, connect an authorized device, work normally, and route eligible costly jobs when useful. The original explicitly asks for opt-in known-command shims and excludes arbitrary Windows process interception from the MVP.

The earlier implementation connected a worker and executed submitted CLI/MCP work, but lacked the ordinary-command layer. Agent instructions alone did not fulfill that behavior. This revision implements compiled adapters, project approval, Codex command-environment registration, exact local fallback, measured observations, dependency-cache readiness and comparable timing history.

| Original requirement | Implementation/evidence | Current status |
|---|---|---|
| §2 / §16 ordinary development commands | Approved project PATH adapters ask the scheduler and execute remotely or invoke original local argv. Real project TypeScript/Python checks routed without force flags. | Implemented; new Codex session loads registered environment. |
| §16 exit status, output, recursion and bypass | Go argument/runtime tests; seven executable/HTTP integration cases, including ambiguous acceptance and failed polling without local duplication. | Passed; simulated HTTP peer establishes semantics, not hardware benefit. |
| §15 agent integration | Ten official-SDK MCP tools, live stdio handshake/status, persistent instructions, CLI fallback, scoped launcher. | Codex configured; Claude client unavailable for live verification. |
| §9–12 device profile and economics | Real authorized ARM64 device, capability/runtime calibration, thermal/RAM gates, AUTO modes, fingerprints, moving averages. Final AUTO worker tests used comparable learned history after labelled qualification. | Real first node and heterogeneous mock pool verified. Predictions remain estimates. |
| §20–21 workspace/dependencies | Content-addressed delta sync; private attempts; secret/native-dependency exclusions; pinned requirements/PEP 621 main dependencies and lockfile installs. Independent dependency readiness. | Real Python/npm provisioning verified; cache/unit contracts passed. uv/Poetry/dev groups pending. |
| §22–24 results and failures | Streaming, exit codes, declared artifacts, cancellation/deadlines, durable attempts, bounded idempotent retries. | End-to-end mock failure/retry checks passed. Physical unplug during work not yet tested. |
| §14 / §31 Android QA | Owned Chrome tab through browser protocol, USB reverse, labeled PNG/metadata, per-device browser capacity. | One real 390×844 capture passed. Sustained matrix currently affected by Chrome responsiveness; no Windows-fidelity claim. |
| §7 / §27 app and startup | GUI launcher/shortcuts; authenticated readiness retry; hidden login startup; build refuses replacement during active jobs. | Repeated host restart/launcher checks passed. Actual Windows reboot/logout pending. |
| §19 multi-device pool | Independent generic nodes, concurrency/drain/reroute checks across three simulated tiers. | Passed with mocks; second physical device unavailable. |
| §25 / §29 / §32 resource evidence | Measured local root CPU/RSS, remote adapter CPU/RSS and Android root RSS; prior bounded daemon idle sample. | Per-process placement evidence available. Sustained whole-host/energy and descendant-aware measurements remain open. |
| §26 dashboard | Approved project controls, routing explanations, activity, telemetry, separate node memory, themes/responsive layout. | Enable/disable tested; 1280×800 and 390×844 have no document overflow. |
| §8.2 native Android service / future layers | Protocol is executor-independent; Termux supervised worker is implemented. | Explicit later milestone, not claimed implemented. |

See [benchmarks](BENCHMARKS.md), [agent workflow](AGENT_INTEGRATION.md), and [remaining engineering work](ROADMAP.md). Whole-laptop acceleration is not inferred from a successful hello command. A slower phone may still place a working set off Windows, but AUTO should learn to avoid an uneconomic route. No universal speedup, physical RAM pooling, completed reboot validation or fully reliable browser farm is claimed.
