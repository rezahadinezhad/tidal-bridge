"""Register Tidal Bridge with Claude Code without the model having to know.

* user-scope MCP server in ~/.claude.json (explicit tools stay available)
* SessionStart hook: puts the command adapters first on the Bash tool's PATH
  through $CLAUDE_ENV_FILE, without changing any command text
* PreToolUse hook (PowerShell tool only, which has no env file): prefixes
  commands that call an adapted tool with the same PATH change
* a short marked block in ~/.claude/CLAUDE.md

Every edit is marked, idempotent and reversible with --remove. Unrelated
settings are preserved; files are replaced atomically.
"""
import argparse
import json
import os
import pathlib
import re
import time

ROOT = pathlib.Path(__file__).resolve().parents[1]
BINARY = ROOT / "bin" / "tidalbridge.exe"
HOME = pathlib.Path.home()
MARK = "tidalbridge"

parser = argparse.ArgumentParser()
parser.add_argument("--data-dir", type=pathlib.Path, default=HOME / ".tidalbridge")
parser.add_argument("--claude-home", type=pathlib.Path, default=HOME / ".claude")
parser.add_argument("--claude-json", type=pathlib.Path, default=HOME / ".claude.json")
parser.add_argument("--remove", action="store_true")
args = parser.parse_args()


def load(path: pathlib.Path) -> dict:
    if not path.exists():
        return {}
    text = path.read_text(encoding="utf-8")
    return json.loads(text) if text.strip() else {}


def save(path: pathlib.Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tidalbridge-tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")
    for attempt in range(20):
        try:
            os.replace(temporary, path)
            return
        except PermissionError:  # another process holds the file briefly
            time.sleep(0.1 * (attempt + 1))
    raise SystemExit(f"Could not replace {path}; close Claude Code and retry")


def hook_command(name: str) -> str:
    return f'"{BINARY.as_posix()}" --data-dir "{args.data_dir.as_posix()}" hook {name}'


def ours(entry: dict) -> bool:
    return any("tidalbridge" in h.get("command", "") and " hook claude-" in h.get("command", "") for h in entry.get("hooks", []))


# 1. MCP server (user scope).
claude_json = load(args.claude_json)
servers = claude_json.setdefault("mcpServers", {})
if args.remove:
    servers.pop(MARK, None)
else:
    servers[MARK] = {"type": "stdio", "command": str(BINARY), "args": ["--data-dir", str(args.data_dir), "mcp"], "env": {}}
if not servers:
    claude_json.pop("mcpServers", None)
save(args.claude_json, claude_json)

# 2. Hooks.
settings_path = args.claude_home / "settings.json"
settings = load(settings_path)
hooks = settings.setdefault("hooks", {})
for event in ("SessionStart", "PreToolUse"):
    hooks[event] = [entry for entry in hooks.get(event, []) if not ours(entry)]
if not args.remove:
    hooks["SessionStart"].append({"hooks": [{"type": "command", "command": hook_command("claude-session-start"), "timeout": 10}]})
    hooks["PreToolUse"].append({"matcher": "PowerShell", "hooks": [{"type": "command", "command": hook_command("claude-pretooluse"), "timeout": 10}]})
for event in list(hooks):
    if not hooks[event]:
        del hooks[event]
if not hooks:
    settings.pop("hooks", None)
save(settings_path, settings)

# 3. Persistent instructions.
block = f"""<!-- tidalbridge:start -->
Tidal Bridge offloads work from this laptop to the user's USB-connected Android
phone. It is automatic: in enabled projects, supported commands (tests,
type-checks, lint, dev servers) are routed by command adapters. Run commands
normally. A stderr line "[Tidal Bridge] ... -> worker-..." means the command
ran on the phone; its output and exit code are the real results. Do not bypass
adapters with absolute runtime paths. Tidal Bridge alone decides where a
command runs and re-checks doubtful phone failures on this laptop itself, so a
failure it reports is real; do not set TIDALBRIDGE_* variables. Status:
& '{BINARY}' status
Communicate with this user in English.
<!-- tidalbridge:end -->"""
memory = args.claude_home / "CLAUDE.md"
content = memory.read_text(encoding="utf-8") if memory.exists() else ""
content = re.sub(r"\s*<!-- tidalbridge:start -->.*?<!-- tidalbridge:end -->", "", content, flags=re.S).rstrip()
if not args.remove:
    content = (content + "\n\n" if content else "") + block
memory.parent.mkdir(parents=True, exist_ok=True)
memory.write_text(content + "\n", encoding="utf-8")
print("Removed Tidal Bridge from Claude Code." if args.remove else "Registered Tidal Bridge with Claude Code (new sessions load it).")
