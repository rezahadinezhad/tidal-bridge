package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateLegacyPrefersPairedPackageCopy(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	// An unpaired real copy and a paired copy written through an MSIX package.
	unpaired := filepath.Join(local, "TidalBridge")
	paired := filepath.Join(local, "Packages", "Agent_123", "LocalCache", "Local", "TidalBridge")
	if err := Save(unpaired, Default()); err != nil {
		t.Fatal(err)
	}
	c := Default()
	c.Approved = []Device{{Serial: "SERIAL1", Token: strings.Repeat("t", 48), Port: 47832}}
	if err := Save(paired, c); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(paired, "host.token"), []byte(strings.Repeat("h", 48)), 0600)
	os.MkdirAll(filepath.Join(paired, "profiles"), 0700)
	os.WriteFile(filepath.Join(paired, "profiles", "worker-a.json"), []byte("{}"), 0600)
	os.WriteFile(filepath.Join(paired, "history.json"), []byte("[]"), 0600)
	target := filepath.Join(t.TempDir(), ".tidalbridge")
	from, err := MigrateLegacy(target)
	if err != nil || from != paired {
		t.Fatalf("migrated from %q, err %v", from, err)
	}
	got, err := Load(target)
	if err != nil || len(got.Approved) != 1 || got.Approved[0].Serial != "SERIAL1" {
		t.Fatal("pairing not migrated", got, err)
	}
	for _, name := range []string{"host.token", "history.json", filepath.Join("profiles", "worker-a.json")} {
		if _, err := os.Stat(filepath.Join(target, name)); err != nil {
			t.Fatal("missing migrated", name)
		}
	}
	// A configured directory is never overwritten.
	if from, err = MigrateLegacy(target); err != nil || from != "" {
		t.Fatal("second migration must be a no-op", from, err)
	}
}

func TestReadSecretNeverCreates(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadSecret(dir); err == nil {
		t.Fatal("expected an error for a missing credential")
	}
	if _, err := os.Stat(filepath.Join(dir, "host.token")); !os.IsNotExist(err) {
		t.Fatal("a client must never mint a host credential")
	}
}
