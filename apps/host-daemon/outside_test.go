package host

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tidalbridge/packages/protocol"
)

func TestMissingOutsideFindsSiblingFolders(t *testing.T) {
	failure := "Error: ENOENT: no such file or directory, open '/w/state/trees/planning/studio/content/words.json'\n    at readFileSync"
	if rel, ok := missingOutside(failure, "/w/state/trees/ws-abc"); !ok || rel != "../planning/studio/content/words.json" {
		t.Fatal(rel, ok)
	}
	nested := "FileNotFoundError: [Errno 2] No such file or directory: '/w/state/trees/wo-abc/shared/x.json'"
	if rel, ok := missingOutside(nested, "/w/state/trees/wo-abc/frontend"); !ok || rel != "../shared/x.json" {
		t.Fatal(rel, ok)
	}
	for _, other := range []string{"AssertionError: expected 2 to be 3", "ENOENT: no such file, open 'R:/app/src/missing.ts'"} {
		if _, ok := missingOutside(other, "/w/state/trees/ws-abc"); ok {
			t.Fatal("not a file from outside the copy:", other)
		}
	}
}

func TestSlowFailuresAreRecognized(t *testing.T) {
	for _, line := range []string{"Error: Test timed out in 5000ms.", "thrown: \"Exceeded timeout of 5000 ms for a test.", "Error: Hook timed out in 10000ms."} {
		if !slowFailure.MatchString(line) {
			t.Error(line)
		}
	}
	if slowFailure.MatchString("AssertionError: expected 2 to be 3") {
		t.Error("an ordinary failure")
	}
}

func TestOutsideFoldersAreCopiedWithTheProject(t *testing.T) {
	dir := t.TempDir()
	h, err := New(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	project := filepath.Join(dir, "repo", "frontend")
	content := filepath.Join(dir, "repo", "planning", "content")
	for _, d := range []string{project, content} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(content, "words.json"), []byte(`{"a":1}`), 0o644)
	os.WriteFile(filepath.Join(content, ".env"), []byte("SECRET=1"), 0o644)
	failure := "Error: ENOENT: no such file or directory, open '/w/trees/planning/content/words.json'"
	if !h.learnOutside(project, "/w/trees/ws-abc", failure) || !h.learnOutside(project, "/w/trees/ws-abc", failure) {
		t.Fatal("a file that exists here is learned, once")
	}
	if got := h.outside[outsideKey(project)]; !slices.Equal(got, []string{"../planning/content"}) {
		t.Fatal(got)
	}
	m := &protocol.Manifest{ID: "m1", Files: []protocol.FileEntry{{Path: "a.ts", Hash: strings.Repeat("0", 64), Size: 1}}}
	extended, key, nest := h.treeFor(project, m)
	paths := []string{}
	for _, f := range extended.Files {
		paths = append(paths, f.Path)
	}
	if !slices.Equal(paths, []string{"a.ts", "../planning/content/words.json"}) || nest != "frontend" || !strings.HasPrefix(key, "wo-") || extended.ID == m.ID {
		t.Fatal("the folder travels beside the project, secrets never", paths, key, nest, extended.ID)
	}
	if _, key, _ := h.treeFor(filepath.Join(dir, "repo", "other"), m); !strings.HasPrefix(key, "ws-") {
		t.Fatal("other projects keep their flat copy", key)
	}
	if h.learnOutside(project, "/w/trees/ws-abc", "Error: ENOENT: no such file or directory, open '/w/trees/planning/content/gone.json'") {
		t.Fatal("a file missing here too is a real failure")
	}
}
