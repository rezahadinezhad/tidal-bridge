package host

import (
	"os"
	"path/filepath"
	"testing"

	"tidalbridge/packages/protocol"
)

func TestWriteBackNeverOverwritesLaptopEdits(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.ts"), []byte("const a=1"), 0o644)
	os.WriteFile(filepath.Join(root, "b.ts"), []byte("const b=1"), 0o644)
	hashA, _ := hashFile(filepath.Join(root, "a.ts"))
	hashB, _ := hashFile(filepath.Join(root, "b.ts"))
	manifest := &protocol.Manifest{Files: []protocol.FileEntry{{Path: "a.ts", Hash: hashA}, {Path: "b.ts", Hash: hashB}}}
	changes := protocol.WorkerChanges{
		Modified: []protocol.FileEntry{{Path: "a.ts", Hash: "new"}},
		Created:  []protocol.FileEntry{{Path: "src/gen.ts", Hash: "gen"}, {Path: ".eslintcache", Hash: "cache"}},
		Deleted:  []string{"b.ts"},
	}
	included := func(path string) bool { return path != ".eslintcache" }
	writes, deletes, conflict := planWriteBack(root, manifest, changes, included)
	if conflict != "" || len(writes) != 2 || len(deletes) != 1 {
		t.Fatal("unchanged laptop files take the phone's changes; ignored caches stay behind", writes, deletes, conflict)
	}
	os.WriteFile(filepath.Join(root, "a.ts"), []byte("const a=2 // edited meanwhile"), 0o644)
	if writes, _, conflict = planWriteBack(root, manifest, changes, included); conflict == "" || writes != nil {
		t.Fatal("an edit made on the laptop meanwhile must stop the whole write-back", writes)
	}
	os.WriteFile(filepath.Join(root, "a.ts"), []byte("const a=1"), 0o644)
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, "src", "gen.ts"), []byte("local"), 0o644)
	if _, _, conflict = planWriteBack(root, manifest, changes, included); conflict == "" {
		t.Fatal("a file created on the laptop meanwhile is never replaced")
	}
	if _, _, conflict = planWriteBack(root, manifest, protocol.WorkerChanges{Modified: []protocol.FileEntry{{Path: "../x", Hash: "x"}}}, included); conflict == "" {
		t.Fatal("paths outside the workspace are refused")
	}
}

func TestRefreshEntriesFollowsFilesChangedDuringSync(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "edited.py"), []byte("x = 2"), 0o644)
	m := &protocol.Manifest{Files: []protocol.FileEntry{{Path: "edited.py", Hash: "old", Size: 5}, {Path: "gone.py", Hash: "g", Size: 1}, {Path: "same.py", Hash: "s", Size: 3}}}
	batch := []protocol.FileEntry{m.Files[0], m.Files[1]}
	if !refreshEntries(root, m, batch) || len(m.Files) != 2 || m.Files[0].Hash == "old" || m.Files[1].Path != "same.py" || m.TotalBytes != 8 {
		t.Fatal("changed files get their current hash, deleted ones leave the manifest", m.Files, m.TotalBytes)
	}
}
