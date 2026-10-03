package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	adapter "tidalbridge/packages/task-adapter"
)

// Claude Code integration. Agents should not need to know about Tidal Bridge:
// these hooks only put the command-adapter directory first on PATH, and the
// adapters themselves decide per invocation whether an approved, compatible
// command runs on a worker or exactly as before on Windows.
//
//	claude-session-start  writes a PATH export to $CLAUDE_ENV_FILE (Bash tool)
//	claude-pretooluse     prefixes PowerShell/Bash commands that call a tool
//	                      the adapters know (the PowerShell tool has no env file)
//
// Any error leaves the command untouched: a hook must never break the agent.

var adaptedTools = regexp.MustCompile(`(?i)(^|[\s;&|(` + "`" + `"'/\\=])(python3?|node|npm|npx|pytest|ruff|mypy|tsc|eslint|prettier|vitest|jest)(\.exe)?($|[\s;&|)"'])`)

const hookMarker = "TIDALBRIDGE_ADAPTERS"

func shimDir(dir string) (string, bool) {
	shims := filepath.Join(dir, "shims")
	if _, err := os.Stat(filepath.Join(shims, "shims.json")); err != nil {
		return "", false
	}
	return shims, true
}

// anyProjectEnabled avoids touching commands when automation is off entirely.
func anyProjectEnabled(dir string) bool {
	var roots []string
	if adapter.ReadJSON(filepath.Join(dir, "automation-projects.json"), &roots) != nil {
		return false
	}
	for _, root := range roots {
		var project adapter.Project
		if adapter.ReadJSON(filepath.Join(root, ".tidalbridge", adapter.FileName), &project) == nil && project.Enabled {
			return true
		}
	}
	return false
}

func posixPath(p string) string {
	p = filepath.ToSlash(p)
	if len(p) >= 2 && p[1] == ':' {
		p = "/" + strings.ToLower(p[:1]) + p[2:]
	}
	return p
}

func bashPrefix(shims string) string {
	return "export " + hookMarker + "=1 PATH='" + strings.ReplaceAll(posixPath(shims), "'", `'\''`) + `':"$PATH"; `
}

func powershellPrefix(shims string) string {
	return "$env:" + hookMarker + " = '1'; $env:PATH = '" + strings.ReplaceAll(shims, "'", "''") + ";' + $env:PATH; "
}

// RewriteCommand returns the command with the adapter PATH prefix, or "" when
// it should be left alone.
func RewriteCommand(tool, command, shims string) string {
	return RewriteCommandIn(tool, command, "", shims)
}

// RewriteCommandIn also keeps virtual-environment interpreters behind the
// adapters, resolving relative environment paths against cwd.
func RewriteCommandIn(tool, command, cwd, shims string) string {
	if command == "" || strings.Contains(command, hookMarker) || !adaptedTools.MatchString(command) {
		return ""
	}
	switch tool {
	case "Bash":
		again := "export PATH='" + strings.ReplaceAll(posixPath(shims), "'", `'\''`) + `':"$PATH"`
		rewritten, dirs := rewriteVenv(command, cwd, again)
		path := posixPath(shims)
		for _, dir := range dirs {
			path += ":" + posixPath(dir)
		}
		return "export " + hookMarker + "=1 PATH='" + strings.ReplaceAll(path, "'", `'\''`) + `':"$PATH"; ` + rewritten
	case "PowerShell":
		again := "$env:PATH = '" + strings.ReplaceAll(shims, "'", "''") + ";' + $env:PATH"
		rewritten, dirs := rewriteVenv(command, cwd, again)
		path := shims
		for _, dir := range dirs {
			path += ";" + dir
		}
		return "$env:" + hookMarker + " = '1'; $env:PATH = '" + strings.ReplaceAll(path, "'", "''") + ";' + $env:PATH; " + rewritten
	}
	return ""
}

func hookCommand(dir string, args []string) (int, error) {
	if len(args) == 0 {
		return 1, fmt.Errorf("hook requires claude-session-start or claude-pretooluse")
	}
	if os.Getenv("TIDALBRIDGE_DISABLE") == "1" {
		return 0, nil
	}
	input, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	shims, ok := shimDir(dir)
	if !ok || !anyProjectEnabled(dir) && !adapter.LoadSettings(dir).Universal {
		return 0, nil
	}
	switch args[0] {
	case "claude-session-start":
		path := os.Getenv("CLAUDE_ENV_FILE")
		if path == "" {
			return 0, nil
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return 0, nil
		}
		defer f.Close()
		fmt.Fprintln(f, strings.TrimSuffix(bashPrefix(shims), "; "))
		return 0, nil
	case "claude-pretooluse":
		var event struct {
			ToolName  string         `json:"tool_name"`
			ToolInput map[string]any `json:"tool_input"`
			Cwd       string         `json:"cwd"`
		}
		if json.Unmarshal(input, &event) != nil || event.ToolInput == nil {
			return 0, nil
		}
		command, _ := event.ToolInput["command"].(string)
		rewritten := RewriteCommandIn(event.ToolName, command, event.Cwd, shims)
		if rewritten == "" {
			return 0, nil
		}
		updated := map[string]any{}
		for k, v := range event.ToolInput {
			updated[k] = v
		}
		updated["command"] = rewritten
		json.NewEncoder(os.Stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "updatedInput": updated}})
		return 0, nil
	}
	return 1, fmt.Errorf("unknown hook %q", args[0])
}
