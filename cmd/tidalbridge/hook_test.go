package main

import (
	"strings"
	"testing"
)

func TestRewriteCommandOnlyTouchesAdaptedTools(t *testing.T) {
	shims := `C:\Users\me\.tidalbridge\shims`
	cases := []struct {
		tool, command string
		rewrite       bool
	}{
		{"PowerShell", "npm run typecheck", true},
		{"PowerShell", "cd frontend; npx tsc --noEmit", true},
		{"Bash", "cd frontend && python -m pytest -q | tail -5", true},
		{"Bash", "./node_modules/.bin/vitest run", true},
		{"PowerShell", "Get-ChildItem", false},
		{"Bash", "git status", false},
		{"Bash", "echo nodejs-config", false},
		{"Bash", "export TIDALBRIDGE_ADAPTERS=1 PATH=x:$PATH; npm test", false},
		{"Read", "npm test", false},
		{"Bash", "", false},
	}
	for _, c := range cases {
		got := RewriteCommand(c.tool, c.command, shims)
		if (got != "") != c.rewrite {
			t.Fatalf("%s %q: rewrite=%v, got %q", c.tool, c.command, c.rewrite, got)
		}
		if got != "" && !strings.HasSuffix(got, c.command) {
			t.Fatalf("original command must be preserved verbatim: %q", got)
		}
	}
	if got := RewriteCommand("Bash", "npm test", shims); !strings.Contains(got, "'/c/Users/me/.tidalbridge/shims'") {
		t.Fatalf("Bash prefix must use a POSIX path: %q", got)
	}
	if got := RewriteCommand("PowerShell", "npm test", `C:\it's\shims`); !strings.Contains(got, `'C:\it''s\shims;'`) {
		t.Fatalf("PowerShell quoting: %q", got)
	}
}

func TestVirtualEnvironmentsStayBehindTheAdapters(t *testing.T) {
	shims := `C:\tb\shims`
	got := RewriteCommandIn("PowerShell", `.\.venv\Scripts\Activate.ps1; python manage.py test apps.orders`, `R:\shop\backend`, shims)
	if !strings.Contains(got, `Activate.ps1; $env:PATH = 'C:\tb\shims;' + $env:PATH; python manage.py test apps.orders`) {
		t.Fatalf("activation must be followed by the adapters again: %q", got)
	}
	got = RewriteCommandIn("PowerShell", `& "R:\shop\backend\.venv\Scripts\python.exe" manage.py test`, ``, shims)
	if !strings.HasSuffix(got, "; python manage.py test") || !strings.Contains(got, `'C:\tb\shims;R:\shop\backend\.venv\Scripts;'`) {
		t.Fatalf("interpreter path becomes python with its directory after the adapters: %q", got)
	}
	got = RewriteCommandIn("PowerShell", `.venv\Scripts\python.exe -m pytest -q`, `R:\shop\backend`, shims)
	if !strings.Contains(got, `R:\shop\backend\.venv\Scripts;'`) || !strings.HasSuffix(got, "python -m pytest -q") {
		t.Fatalf("relative environments resolve against the working directory: %q", got)
	}
	got = RewriteCommandIn("Bash", `source .venv/Scripts/activate && pytest -q`, `R:\shop\backend`, shims)
	if !strings.Contains(got, `activate; export PATH='/c/tb/shims':"$PATH" && pytest -q`) {
		t.Fatalf("bash activation: %q", got)
	}
	if got = RewriteCommandIn("PowerShell", `python manage.py test`, `R:\x`, shims); !strings.HasSuffix(got, "; python manage.py test") || strings.Contains(got, ".venv") {
		t.Fatalf("commands without environments are unchanged: %q", got)
	}
}
