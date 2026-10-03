package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DataDir is deliberately outside AppData. Claude and Codex ship as MSIX
// packages on Windows, and MSIX redirects AppData writes from their child
// processes into a private per-app copy. State kept in AppData therefore
// splits into one invisible copy per agent. The user profile root is shared.
func DataDir() string {
	if v := os.Getenv("TIDALBRIDGE_DATA_DIR"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".tidalbridge")
	}
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "tidalbridge")
}

// LegacyDirs lists earlier AppData locations, including the private copies
// that MSIX-packaged agents created, newest configuration first.
func LegacyDirs() []string {
	var candidates []string
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		candidates = append(candidates, filepath.Join(local, "TidalBridge"))
		if matches, _ := filepath.Glob(filepath.Join(local, "Packages", "*", "LocalCache", "Local", "TidalBridge")); len(matches) > 0 {
			candidates = append(candidates, matches...)
		}
	}
	type dated struct {
		dir     string
		devices int
		mod     int64
	}
	var found []dated
	for _, dir := range candidates {
		info, err := os.Stat(filepath.Join(dir, "config.yaml"))
		if err != nil {
			continue
		}
		c, err := Load(dir)
		if err != nil {
			continue
		}
		found = append(found, dated{dir, len(c.Approved), info.ModTime().UnixNano()})
	}
	// Prefer a copy that actually holds paired devices, then the newest one.
	sort.SliceStable(found, func(i, j int) bool {
		if (found[i].devices > 0) != (found[j].devices > 0) {
			return found[i].devices > 0
		}
		return found[i].mod > found[j].mod
	})
	out := make([]string, len(found))
	for i, f := range found {
		out[i] = f.dir
	}
	return out
}

// MigrateLegacy copies pairing, profiles and history from the best legacy
// directory into dir when dir has never been configured. Job transcripts and
// result files stay behind; they are diagnostics, not state.
func MigrateLegacy(dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err == nil {
		return "", nil
	}
	legacy := LegacyDirs()
	if len(legacy) == 0 {
		return "", nil
	}
	source := legacy[0]
	if strings.EqualFold(filepath.Clean(source), filepath.Clean(dir)) {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	files := []string{"config.yaml", "host.token", "history.json", "automation-projects.json"}
	tokens, _ := filepath.Glob(filepath.Join(source, "*.token"))
	for _, t := range tokens {
		files = append(files, filepath.Base(t))
	}
	profiles, _ := filepath.Glob(filepath.Join(source, "profiles", "*.json"))
	for _, p := range profiles {
		files = append(files, filepath.Join("profiles", filepath.Base(p)))
	}
	seen := map[string]bool{}
	for _, name := range files {
		if seen[name] {
			continue
		}
		seen[name] = true
		if err := copyFile(filepath.Join(source, name), filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return source, fmt.Errorf("migrate %s: %w", name, err)
		}
	}
	return source, nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return err
	}
	tmp := to + ".migrating"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, to)
}

// ReadSecret returns the host credential without creating one. Clients must
// never mint a credential: a client that guessed the wrong directory would
// otherwise create a token the running host does not know.
func ReadSecret(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "host.token"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("Tidal Bridge host is not initialized in %s (start the host service first)", dir)
		}
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 32 {
		return "", fmt.Errorf("host credential in %s is invalid", dir)
	}
	return token, nil
}
