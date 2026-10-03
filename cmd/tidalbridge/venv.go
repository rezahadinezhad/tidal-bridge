package main

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Virtual environments: an activation script, or an interpreter called by its
// path, puts the environment's own Python before the adapters. The rewrite
// keeps that interpreter (its directory follows the adapters on PATH, so an
// adapter running locally resolves exactly the same python.exe) and lets the
// adapter decide where the command runs.
var (
	venvInterpreter = regexp.MustCompile(`(?i)(^|[\s;&|(])(?:&\s*)?(["']?)((?:[a-z]:)?[^\s"';&|()]*?\.venv[\\/](?:scripts|bin))[\\/]python3?(?:\.exe)?(["']?)(\s|$)`)
	venvActivation  = regexp.MustCompile(`(?i)(["']?)[^\s"';&|()]*\.venv[\\/](?:scripts|bin)[\\/]activate(?:\.ps1)?(["']?)`)
)

// rewriteVenv returns the command with virtual-environment interpreters
// replaced by "python" and the directories that must follow the adapters on
// PATH; prefix re-adds the adapters after an activation script.
func rewriteVenv(command, cwd, prefix string) (string, []string) {
	var dirs []string
	command = venvInterpreter.ReplaceAllStringFunc(command, func(match string) string {
		m := venvInterpreter.FindStringSubmatch(match)
		if m[2] != m[4] {
			return match
		}
		dir := m[3]
		if !filepath.IsAbs(dir) && !strings.HasPrefix(dir, "/") && cwd != "" {
			dir = filepath.Join(cwd, dir)
		}
		dirs = append(dirs, dir)
		return m[1] + "python" + m[5]
	})
	if prefix != "" {
		command = venvActivation.ReplaceAllStringFunc(command, func(match string) string {
			return match + "; " + prefix
		})
	}
	return command, dirs
}
