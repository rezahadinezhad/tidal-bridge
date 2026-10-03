"""Validate configuration preservation and reversibility in isolated directories."""
import json
import pathlib
import subprocess
import sys
import tempfile
import tomllib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]

class InstallerTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='tidalbridge-config-test-')
        self.addCleanup(temporary.cleanup)
        self.home = pathlib.Path(temporary.name)
        self.shims = self.home / 'shims'
        self.shims.mkdir()
        self.config = self.home / 'config.toml'
        self.original = '# preserve this comment\nmodel="existing-model"\n[mcp_servers.other]\ncommand="existing.exe"\n[shell_environment_policy.set]\nOTHER="keep-me"\n'

    def run_installer(self, *args):
        return subprocess.run([sys.executable, str(ROOT / 'scripts/configure-agent-automation.py'), '--codex-home', str(self.home), *args], capture_output=True, text=True, timeout=10)

    def test_install_repeat_remove_preserves_unrelated_settings(self):
        self.config.write_text(self.original)
        expected = tomllib.loads(self.original)
        for _ in range(2):
            result = self.run_installer('--shim-dir', str(self.shims))
            self.assertEqual(result.returncode, 0, result.stderr)
        text = self.config.read_text()
        self.assertEqual(text.count('# tidalbridge:command-adapters'), 1)
        parsed = tomllib.loads(text)
        self.assertTrue(parsed['shell_environment_policy']['set'].pop('PATH').startswith(str(self.shims)))
        self.assertEqual(parsed, expected)
        self.assertIn('# preserve this comment', text)
        result = self.run_installer('--remove')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(tomllib.loads(self.config.read_text()), expected)
        self.assertFalse((self.home / 'tidalbridge-automation.json').exists())

    def test_existing_path_restored_exactly(self):
        self.config.write_text(self.original + 'PATH="original-path"\n')
        result = self.run_installer('--shim-dir', str(self.shims))
        self.assertEqual(result.returncode, 0, result.stderr)
        result = self.run_installer('--remove')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(tomllib.loads(self.config.read_text())['shell_environment_policy']['set']['PATH'], 'original-path')

    def test_later_user_path_edit_is_preserved(self):
        self.config.write_text(self.original)
        self.assertEqual(self.run_installer('--shim-dir', str(self.shims)).returncode, 0)
        text = self.config.read_text()
        state = json.loads((self.home / 'tidalbridge-automation.json').read_text())
        changed = text.replace(json.dumps(state['installed_path']), '"user-edited-path"')
        self.config.write_text(changed)
        self.assertNotEqual(self.run_installer('--remove').returncode, 0)
        self.assertEqual(self.config.read_text(), changed)

    def test_inline_environment_is_preserved(self):
        original = '[shell_environment_policy]\nset={PATH="inline",OTHER="keep"}\n'
        self.config.write_text(original)
        self.assertNotEqual(self.run_installer('--shim-dir', str(self.shims)).returncode, 0)
        self.assertEqual(self.config.read_text(), original)

if __name__ == '__main__':
    unittest.main(verbosity=2)
