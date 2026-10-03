# Scheduler policy

The scheduler first rejects incompatible or unsafe placement, then decides whether moving a job relieves the laptop at an acceptable cost. The source is `packages/scheduler/scheduler.go`; every decision's candidates, reasons, transfer bytes and costs are visible through `route` and the job record.

## Gates

A worker is ineligible when it is unhealthy, draining, or paused. It is also ineligible on a mismatch of architecture, runtime version or Debian availability, or when the job needs a browser it lacks. Resource gates: less than 1 GB free RAM (plus the job's declared need) after the reserve, insufficient storage, severe or worse thermal state (and for five minutes after the phone was last seen at it, so it cools down instead of bouncing at the limit), or battery below 15% while not charging. At the critical level its running jobs are stopped and their adapters run them on the laptop (dev servers through failover). How many jobs a phone takes at once adapts to it (the dashboard's **Jobs at once** and **Adapt** settings, `worker_concurrency` and `fixed_capacity` in `config.yaml`): the chosen maximum (3 by default), lowered to 1 while the screen is on (unless `phone_use: max`), at the moderate thermal level or on battery, to 2 at the light thermal level, and to the number of 1.2 GB slots free after the memory reserve. With Adapt off the maximum always applies; the heat, battery and memory gates above still do. BATTERY_SAVER additionally requires charging. A service also needs its ports free on the laptop. Forced-remote jobs obey every gate. Windows-specific executables, native output hints and unknown commands stay on the host.

## Relief objective (AUTO and PERFORMANCE)

Host pressure is the larger of CPU use and RAM use. The busier the laptop, the slower a worker may be and still take the job:

| Host pressure | Tolerated slowdown |
|---|---|
| below 50% | 1.1x |
| 50–70% | 1.5x |
| 70–85% | 2.5x |
| 85–95% | 4x |
| 95% and above | 6x |

PERFORMANCE tolerates at least 4x. A job moves when the worker's expected total (measured execution history, transfer at measured throughput, setup, and a risk allowance that grows when the phone is warm) fits within `local time × tolerance`. It must also relieve something meaningful: at least 0.3 CPU-seconds or 100 MB, from measured local runs of the whole process tree. Tiny commands stay local.

A dependency environment that is not ready adds a two-minute setup penalty; the first command that finds it cold runs locally while the host prepares it in the background. A command that failed twice on a worker after succeeding locally stays local for a day.

Commands detected by universal mode have two extra rules. A detected command that takes at least 3 seconds locally (from two measured runs) is measured on an idle, cool worker with a warm environment up to twice, since without worker history it would otherwise stay local. A detected command whose worker failure the laptop did not reproduce is quarantined for a week.

Services (dev servers) move in AUTO and PERFORMANCE whenever a worker can run them: they hold gigabytes for hours, so a slower first compile is worth it.

## CONSERVATIVE and BATTERY_SAVER

CONSERVATIVE uses time alone: a worker must be predicted at least 40% faster than local, including transfer and setup. BATTERY_SAVER applies the relief objective but only to charging workers.

## History

Successful observations feed a moving average after two matching samples. Signatures include the command, workspace, profile, requirements, working directory, provisioning and runtime; device fingerprints include the worker, Android version, architecture and runtimes, so a changed toolchain does not reuse old timings. Cold and warm dependency histories are separate, and sync time is subtracted from execution time.

A command measured fewer than twice on this laptop gets a first estimate:
- its one local run;
- otherwise its phone runs, scaled by how this laptop compares with the phone on commands that ran on both (the median, once two such commands exist);
- otherwise the project's estimate.

Until that comparison exists, phone runs replace only the default one-second guess. A command's laptop savings are estimated the same way, from the phone's own CPU and memory measurements. Without any history, a worker is assumed 15% slower than the local estimate.

## Trust

A failed phone run is re-run on this laptop until the phone and laptop agree twice. This covers detected commands and replay-safe task-file commands. That exact command, when it passes here but fails on the phone, stays here for a week, and its runner is checked again. Trust belongs to the tool and its options, not to the files it is pointed at, so two agreeing checks of a runner cover all its test files.

A phone failure where a test ran out of time is always re-checked here, because the phone is slower; if the laptop passes, that exact command stays here for a week. A phone failure caused by a missing file from outside the project folder is always re-run here, whatever the trust. Tidal Bridge then copies that file's folder beside the project on the phone (the file alone above 2,000 files or 64 MB), one level up as on the laptop. Command adapters ignore `TIDALBRIDGE_FORCE_LOCAL`; only the CLI and MCP can still force this laptop, and such runs are marked when the phone could have taken them.

## Splitting test suites

A whole Vitest (1+) or Jest (28+) suite routed to a phone may run in two halves at once: `--shard=1/2` on this laptop and `--shard=2/2` on the phone. Both runners assign files the same way on every machine. All of these must hold:
- the suite takes at least 30 s on the phone;
- the laptop's CPU is at most half busy;
- the laptop has free memory for the suite's peak plus the reserve;
- the mode is not Max.

The command must not involve coverage, report files, watch mode, module state shared across files, or a database, cache or queue client. Both halves must pass and together cover as many test files as the suite's last whole run. Otherwise the whole suite runs on this laptop. If the halves missed files, splitting stays off for that suite for a month.

## Limits

If all slots are busy, jobs wait in the bounded admission queue. Replay requires both idempotent and retryable policy, with at most three attempts; cancellation and timeout are not transport failures. Moving a job moves its process and working set; it does not add RAM to Windows. Use `route` before heavy work, and declare RAM needs through MCP or HTTP for memory-heavy jobs.
