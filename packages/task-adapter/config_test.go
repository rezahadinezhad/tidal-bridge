package adapter

import (
	"os"
	"path/filepath"
	"testing"
	"tidalbridge/packages/config"
)

func TestProjectOptInAndDisabledChild(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	os.MkdirAll(sub, 0700)
	p := Project{Version: 1, Enabled: true, Tasks: []Task{{Name: "tests", Command: []string{"python", "-m", "unittest"}}}}
	config.SaveJSON(filepath.Join(root, ".tidalbridge", FileName), p)
	got, found, e := Find(sub)
	if e != nil || found != root || !got.Enabled {
		t.Fatal(got, found, e)
	}
	config.SaveJSON(filepath.Join(sub, ".tidalbridge", FileName), Project{Version: 1, Enabled: false})
	got, found, e = Find(sub)
	if e != nil || found != sub || got.Enabled {
		t.Fatal("disabled child inherited parent approval", got, found, e)
	}
}
func TestPortableTaskGates(t *testing.T) {
	root := t.TempDir()
	p := Project{Version: 1, Enabled: true, Tasks: []Task{{Name: "types", Command: []string{"tsc", "--noEmit"}}, {Name: "tests", Command: []string{"python", "-m", "unittest"}}}}
	for _, args := range [][]string{{"tsc", "--noEmit"}, {"tsc", "--noEmit", "--pretty", "false"}, {"python", "-m", "unittest", "discover", "-v"}} {
		s, ok := Spec(p, root, root, args)
		if !ok || s.Policy.Idempotent || s.Profile == "" || s.Workspace != root {
			t.Fatal(s, ok)
		}
	}
	for _, args := range [][]string{{"tsc"}, {"tsc", "--noEmit", "false"}, {"tsc", "--noEmit", "--watch"}, {"tsc", "--noEmit", "--outDir", "out"}, {"python", "-m", "unittest", "..\\private"}, {"python", "-m", "unittest", filepath.Join(root, "test.py")}, {"python", "unapproved.py"}, {"python", "-m", "unittest", "--fix"}} {
		if _, ok := Spec(p, root, root, args); ok {
			t.Fatal("unsafe command routed", args)
		}
	}
	p.Enabled = false
	if _, ok := Spec(p, root, root, []string{"tsc", "--noEmit"}); ok {
		t.Fatal("disabled project routed")
	}
}
func TestNodeEntryAndOriginalResolution(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "node_modules", "typescript", "bin", "tsc")
	os.MkdirAll(filepath.Dir(entry), 0700)
	os.WriteFile(entry, []byte(""), 0600)
	args := Canonical("node", []string{entry, "--noEmit"})
	if len(args) != 2 || args[0] != "tsc" {
		t.Fatal(args)
	}
	inst := Installation{LocalCommands: map[string][]string{"node": {"real-node"}, "tsc": {"global-node", "global-tsc"}}}
	local, e := ResolveLocal(inst, "tsc", root, []string{"--noEmit"})
	if e != nil || len(local) != 3 || local[1] != entry {
		t.Fatal(local, e)
	}
	if _, e = ResolveLocal(inst, "unknown", root, nil); e == nil {
		t.Fatal("missing original should fail")
	}
}
