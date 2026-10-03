# Agent integration and ordinary commands

Tidal Bridge implements the integration layers of section 16 of the original build prompt: CLI/MCP, persistent agent instructions and opt-in command adapters. In approved projects, agents run commands as usual and the host decides where each one runs.

## Installed workflow

**Claude Code** (`scripts/configure-claude.py`, run by `setup-agents.ps1`):

- A SessionStart hook (`tidalbridge hook claude-session-start`) appends the adapter PATH to the session's environment file, which the Bash tool reads.
- A PreToolUse hook on the PowerShell tool (`tidalbridge hook claude-pretooluse`) prefixes each command with the same PATH.
- The MCP server is registered at user scope in `~/.claude.json`, and a short instruction block goes into `~/.claude/CLAUDE.md`.
- The hooks act only when adapters are installed and at least one project is approved. Any error leaves the command untouched.
- Verified in a live session: Bash commands resolved `npm` to the adapter, and a PowerShell `python -m unittest` ran on the phone.

**Codex**: `install-automation.ps1 -EnableCodex` maintains a marked PATH entry in `[shell_environment_policy.set]` of `~/.codex/config.toml` and keeps a backup; `setup-agents.ps1` registers the MCP server and the instruction block in `~/.codex/AGENTS.md`. Windows system and user PATH are unchanged. Start a new session after installation: an existing one keeps its old PATH and tools.

## Universal mode

`tidalbridge automation universal on` makes every project eligible without a task file. The settings live in `%USERPROFILE%\.tidalbridge\automation.json`; nothing is written into projects, and a project's own task file always takes precedence.

- **Project root**: the nearest directory with `package.json` and a Node lock file, or with a Python marker (`pyproject.toml`, `requirements.txt`, `setup.py`, `setup.cfg`, `uv.lock`, `manage.py`, `pytest.ini`), without leaving the repository. Drive roots and the user's home never count.
- **Detected commands**: read-only checks everywhere (`tsc --noEmit`, `vue-tsc --noEmit`, `eslint`, `prettier --check`, `biome check`, `ruff check`, `mypy`, and package scripts that run one of them). JavaScript tests (`vitest run`, `jest`, `mocha`, `node --test`) everywhere: these runners do not load `.env` files (Vite exposes only `VITE_` variables), and verification catches a failure a missing file causes. Python tests (`pytest`, `unittest`, Django's `manage.py test`) and Next.js/Vite dev servers only when the project has no private `.env` file or shares them with the phone. Builds, code generators and commands that edit files are never detected.
- **Environment**: jobs run in the Debian userland; locked dependencies are installed on the phone without install scripts.
- **Trust by verification**: when a detected command fails on the phone, the adapter reruns it on the laptop and the laptop's result stands. After two agreeing failures the phone's failures are trusted; a disagreement keeps the command local for a week (`verification.json`).
- **Commands that change files**: formatters and fixers (`eslint --fix`, `prettier --write`, `biome check --write`, `ruff check --fix`, `ruff format`, and package scripts that run them) move with write-back: the files they change on the phone are copied back. If any of those files changed on the laptop meanwhile, nothing is copied and the command runs on the laptop.
- **Laptop services**: when a project shares its `.env` files, its jobs reach on `127.0.0.1` the ports those files name (URLs such as `redis://localhost:6381` and settings such as `POSTGRES_PORT=5433`, `.env.example` included) and the well-known database, cache and queue ports listening on the laptop (5432, 5433, 3306, 6379–6381, 27017, 5672, 9000, 11211). The services themselves stay on the laptop.
- **Virtual environments**: activating a virtual environment, or calling its interpreter by path, would put that Python before the adapters. The Claude Code hook keeps the adapters first: `.venv\Scripts\Activate.ps1` is followed by the adapter PATH again, and `.venv\Scripts\python.exe` becomes `python` with the environment's directory right after the adapters, so a local run uses exactly the same interpreter.
- **Nested projects**: inside a project whose task file does not cover a command, a nested project with its own lock file is detected as usual. A task file with `"enabled": false` still keeps everything below it local.
- **Measuring**: a detected command that takes at least 3 seconds locally is measured on an idle phone up to twice; afterwards its history decides like any other command's.

```powershell
tidalbridge automation universal on|off
tidalbridge automation exclude --workspace PATH     # include --workspace PATH undoes it
tidalbridge automation share-env --workspace PATH   # keep-env --workspace PATH undoes it
```

## Forgotten dev processes

Agents start dev servers and watchers in the background and often never stop them. `tidalbridge cleanup` lists dev servers, watchers and workers (Next.js, Vite, Vitest and Jest watch modes, `tsc --watch`, nodemon, webpack serve, Django `runserver`, uvicorn `--reload`, Celery workers, Daphne) with the memory of each process tree, marked "forgotten" when the terminal or agent that started it has exited. `tidalbridge cleanup PID` stops a tree and `--stop-forgotten` stops every forgotten one; nothing is stopped otherwise. `doctor` reports forgotten trees.

## Approved projects

Approve a project from the dashboard's **Automation** page or with `tidalbridge automation enable --workspace PATH [--profiles a,b] [--provision]`. Approval lives in the project's `.tidalbridge/tasks.json`; a disabled nested project stops approval inherited from a parent.

## Adapters

Adapters cover `python`, `python3`, `node`, `npm`, `npx`, `pytest`, `ruff`, `mypy`, `tsc`, `eslint`, `prettier`, `vitest` and `jest`. Each is a copy of `tidalbridge-task.exe` that resolves the real tool from the live PATH (project `node_modules` first). The real local tool must be installed for local runs. Known Node entry points and `npx` forms map to the same command signature.

Unknown commands, absolute host paths, directory escapes, watch and interactive modes, and file-editing or build flags stay local. Package scripts run through npm are checked for Windows-only or chained commands. Shell aliases and absolute runtime paths (`"C:\Program Files\nodejs\node.exe" …`) bypass the adapters.

A local run of an approved command is measured over its whole process tree (a Windows job object, used for accounting only), so `npm run typecheck` reports the compiler's CPU time and memory, not just npm's.

## Task files

```json
{
  "version": 1,
  "enabled": true,
  "runtime": "debian",
  "sync_env_files": true,
  "reverse_ports": [8000, 9000],
  "tasks": [
    { "name": "typecheck", "command": ["npm", "run", "typecheck"], "provision": true, "estimated_duration_ms": 30000, "timeout_seconds": 1800 },
    { "name": "lint", "command": ["npm", "run", "lint"], "provision": true },
    { "name": "vitest", "command": ["vitest", "run"], "provision": true },
    { "name": "dev", "command": ["npm", "run", "dev"], "provision": true, "service": true, "ports": [3100], "remote_args": ["--", "-H", "127.0.0.1"] }
  ]
}
```

Project fields:

- `runtime`: `debian` runs jobs in the worker's Debian userland (glibc Node and Python, matching `linux-arm64-gnu` packages in lockfiles); the default is the worker's native runtime.
- `sync_env_files`: includes the project's `.env` files in the worker copy. Only use it when they hold no secrets the phone should not have.
- `reverse_ports`: laptop ports that jobs reach on `127.0.0.1`, such as a backend API.

Task fields: `command` is an exact prefix; the longest match wins. Other fields:

- `provision`: approves installing locked dependencies on the worker, without install scripts unless `provision_scripts` is set.
- `estimated_duration_ms`: an initial guess until measured history replaces it.
- `idempotent`: allows replay after a transport loss. Default false; set it only when replay is safe.
- `expected_outputs`: files copied back after the job.
- `runtime_requirements`: version constraints.
- `write_back`: the command may change files (formatters, fixers, code generators); `--fix` and `--write` are accepted, and changed, created and deleted files are copied back as described above.
- `engine`: `native` runs Node tools on the phone without `proot` when the command allows it (detected commands use it by default).
- `service`, `ports`, `remote_args`: run a dev server. It moves only when its ports are free on the laptop. `remote_args` are appended on the worker, for example to bind loopback.

`.tidalbridgeignore` at the project root adds exclusions, and decides after every `.gitignore`: `!crypto/messenger/pkg/` syncs generated output that a `.gitignore` excludes but a remote job needs.

## Result semantics

The adapter previews the route. A worker route streams stdout and stderr, preserves the exit code and prints `[Tidal Bridge] <command> -> worker-…` on stderr. Paths of the worker's copy are rewritten to the laptop path. A LOCAL, WAIT or REJECT preview, or an unavailable host, runs the original local command with the original arguments and stdio; no shell re-evaluates the command text.

Once a submission may have been accepted, a transport or polling failure never starts a duplicate local run; the host owns any permitted retry or fallback. Recursion guards stop nested commands from submitting themselves again. Remote CPU and memory are recorded for the whole job on the worker. When a worker run fails twice after the same command succeeded locally, it stays local.

`TIDALBRIDGE_DISABLE=1` bypasses the adapters; `TIDALBRIDGE_FORCE_REMOTE=1` requires a compatible worker and fails otherwise (for testing). Command adapters ignore `TIDALBRIDGE_FORCE_LOCAL`: Tidal Bridge, not the agent, decides where a command runs. To keep work on this laptop, pause offloading or list the folder under "Never offloaded" in the dashboard. Force flags never override safety gates. `TIDALBRIDGE_ENGINE=native|proot` overrides a command's worker engine for one run. Two forced runs are a quick way to give a new command measured worker history.

## CLI/MCP and agent instructions

The same executable serves stdio MCP without starting another host. Ten tools cover status and capabilities, submit and profile, route preview, job status and cancel, workspace sync, render matrix and a bounded benchmark. Submission is asynchronous: poll the returned ID.

```powershell
codex mcp add tidalbridge -- 'R:\Tidal Bridge\bin\tidalbridge.exe' --data-dir "$env:USERPROFILE\.tidalbridge" mcp
claude mcp add --transport stdio --scope user tidalbridge -- 'R:\Tidal Bridge\bin\tidalbridge.exe' --data-dir "$env:USERPROFILE\.tidalbridge" mcp
```

The instruction blocks tell agents to run commands normally in approved projects, to read a `[Tidal Bridge] … -> worker-…` line as "ran on the phone, results are real", not to bypass adapters with absolute runtime paths, and that Tidal Bridge alone decides where a command runs: it re-checks doubtful phone failures on this laptop itself, so a failure it reports is real, and agents set no `TIDALBRIDGE_*` variables. Simulated workers provide no physical relief. No API key is required.

Route previews identify remote placement by worker ID in `target`; other targets are `LOCAL`, `WAIT` and `REJECT`. See the [Codex MCP instructions](https://developers.openai.com/codex/mcp) and [Claude Code MCP instructions](https://code.claude.com/docs/en/mcp).
