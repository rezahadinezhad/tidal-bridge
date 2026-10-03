"""Maintain only Tidal Bridge's explicit Codex environment entries, preserving
unrelated settings. No agent/model is launched, and no system PATH is changed.
"""
import argparse
import json
import os
import pathlib
import re
import tomllib

parser = argparse.ArgumentParser()
parser.add_argument('--shim-dir', type=pathlib.Path)
parser.add_argument('--remove', action='store_true')
parser.add_argument('--codex-home', type=pathlib.Path, help='Explicit configuration directory; useful for isolated installer validation')
args = parser.parse_args()


def registry_path():
    """Machine + user PATH as a fresh process would see it, minus adapters."""
    try:
        import winreg
    except ImportError:
        return None
    parts = []
    for hive, key in ((winreg.HKEY_LOCAL_MACHINE, r'SYSTEM\CurrentControlSet\Control\Session Manager\Environment'), (winreg.HKEY_CURRENT_USER, 'Environment')):
        try:
            with winreg.OpenKey(hive, key) as handle:
                value, _ = winreg.QueryValueEx(handle, 'Path')
                parts.extend(os.path.expandvars(value).split(os.pathsep))
        except OSError:
            continue
    clean, seen = [], set()
    for part in parts:
        part = part.strip()
        lower = part.lower().replace('/', '\\').rstrip('\\')
        transient = '\\.codex\\tmp\\arg0\\' in lower + '\\'
        if not part or lower in seen or transient or os.path.exists(os.path.join(part, 'shims.json')):
            continue
        seen.add(lower)
        clean.append(part)
    return os.pathsep.join(clean)


root = args.codex_home or pathlib.Path(os.environ.get('CODEX_HOME', pathlib.Path.home() / '.codex'))
path = root / 'config.toml'
text = path.read_text(encoding='utf-8') if path.exists() else ''
parsed = tomllib.loads(text)
environment = parsed.get('shell_environment_policy', {}).get('set', {})
marker = '# tidalbridge:command-adapters'
state_path = root / 'tidalbridge-automation.json'
previous_state = json.loads(state_path.read_text()) if state_path.exists() else None
if args.remove:
    if not previous_state:
        print('No Tidal Bridge command environment is registered.')
        raise SystemExit(0)
    if environment.get('PATH') != previous_state['installed_path']:
        raise SystemExit('PATH was edited after registration. Preserve it and remove the Tidal Bridge prefix manually.')
    value = previous_state['previous_path']
else:
    if not args.shim_dir or not args.shim_dir.is_dir():
        raise SystemExit('Existing shim directory required.')
    if previous_state and environment.get('PATH') != previous_state['installed_path']:
        raise SystemExit('The Codex PATH override changed after registration; preserve the new settings and reconcile the adapter prefix first.')
    # Rebuild the base from the registry on every run: a copy of some
    # process's PATH freezes session-specific directories (Codex's arg0 helper
    # directory changes per session) and misses later tool installations.
    # Keep Codex's own runtime directories from the earlier base, drop
    # per-session directories and old adapter locations, then append entries
    # installed since (from the registry).
    earlier = previous_state.get('base_path') if previous_state else environment.get('PATH', os.environ.get('PATH', ''))
    merged, seen = [], set()
    for part in (earlier or os.environ.get('PATH', '')).split(os.pathsep) + (registry_path() or '').split(os.pathsep):
        lower = part.strip().lower().replace('/', '\\').rstrip('\\')
        if not lower or lower in seen or '\\.codex\\tmp\\arg0\\' in lower + '\\' or lower.endswith('\\tidalbridge\\shims') or lower.endswith('\\.tidalbridge\\shims'):
            continue
        seen.add(lower)
        merged.append(part.strip())
    base = os.pathsep.join(merged)
    value = str(args.shim_dir.resolve()) + os.pathsep + base
    # A missing PATH override is restored as missing on removal.
    state = {'previous_path': previous_state['previous_path'] if previous_state else environment.get('PATH'), 'base_path': base, 'installed_path': value}

# Existing managed lines or PATH entries within the exact table are replaced;
# all other text (including comments, profiles, MCPs and trusted env) survives.
section = re.search(r'(?m)^\[shell_environment_policy\.set\]\s*$', text)
if section:
    start = section.end()
    next_section = re.search(r'(?m)^\[', text[start:])
    end = start + next_section.start() if next_section else len(text)
    body = text[start:end]
    body = re.sub(r'(?m)^# tidalbridge:command-adapters\s*\n?', '', body)
    body = re.sub(r'(?m)^PATH\s*=.*(?:\n|$)', '', body)
    replacement = '\n' + (marker + '\nPATH = ' + json.dumps(value, ensure_ascii=True) + '\n' if value is not None else '') + body.lstrip('\n')
    updated = text[:start] + replacement + text[end:]
else:
    if 'set' in parsed.get('shell_environment_policy', {}):
        raise SystemExit('The existing environment uses an inline table; preserve it and configure adapters through an agent launcher instead.')
    updated = text.rstrip() + '\n\n[shell_environment_policy.set]\n' + marker + '\nPATH = ' + json.dumps(value, ensure_ascii=True) + '\n'
new_parsed = tomllib.loads(updated)
assert new_parsed.get('shell_environment_policy', {}).get('set', {}).get('PATH') == value
root.mkdir(parents=True, exist_ok=True)
backup = root / 'config.toml.tidalbridge-before-automation'
if not backup.exists():
    backup.write_text(text, encoding='utf-8')
temporary = path.with_suffix('.toml.tidalbridge-tmp')
temporary.write_text(updated, encoding='utf-8')
temporary.replace(path)
if args.remove:
    state_path.unlink()
    print('Restored the original Codex command environment.')
else:
    state_path.write_text(json.dumps(state, indent=2), encoding='utf-8')
    print('Registered Tidal Bridge command adapters in the Codex command environment.')
