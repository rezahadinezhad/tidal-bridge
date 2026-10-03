package main

import (
	"flag"
	"fmt"
	"path/filepath"

	"tidalbridge/packages/config"
	adapter "tidalbridge/packages/task-adapter"
)

var settingsCommands = map[string]bool{"universal": true, "exclude": true, "include": true, "share-env": true, "keep-env": true}

// automationSettings edits universal mode without the host: adapters and
// hooks read the settings file on every command.
func automationSettings(dir string, args []string) (int, error) {
	s := adapter.LoadSettings(dir)
	s.Version = 1
	if args[0] == "universal" {
		if len(args) != 2 || args[1] != "on" && args[1] != "off" {
			return 1, fmt.Errorf("usage: automation universal on|off")
		}
		s.Universal = args[1] == "on"
	} else {
		f := flag.NewFlagSet(args[0], flag.ContinueOnError)
		workspace := f.String("workspace", "", "Absolute project directory")
		if e := f.Parse(args[1:]); e != nil {
			return 1, e
		}
		if !filepath.IsAbs(*workspace) {
			return 1, fmt.Errorf("an absolute --workspace is required")
		}
		path := filepath.Clean(*workspace)
		switch args[0] {
		case "exclude":
			s.Excluded = withPath(s.Excluded, path)
		case "include":
			s.Excluded = withoutPath(s.Excluded, path)
		case "share-env":
			s.ShareEnv = withPath(s.ShareEnv, path)
		case "keep-env":
			s.ShareEnv = withoutPath(s.ShareEnv, path)
		}
	}
	if e := config.SaveJSON(filepath.Join(dir, adapter.SettingsFile), s); e != nil {
		return 1, e
	}
	printJSON(s)
	return 0, nil
}

func withPath(list []string, path string) []string {
	return append(withoutPath(list, path), path)
}

func withoutPath(list []string, path string) []string {
	kept := []string{}
	for _, item := range list {
		if !adapter.Within(item, path) || !adapter.Within(path, item) {
			kept = append(kept, item)
		}
	}
	return kept
}
