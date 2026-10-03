package workspacesync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func manifestPaths(t *testing.T, x *Index) map[string]string {
	t.Helper()
	m, err := x.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range m.Files {
		out[f.Path] = f.Hash
	}
	return out
}

func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for", what)
}

func TestEmptyWorkspaceManifestHasFileList(t *testing.T) {
	x, err := OpenIndex(context.Background(), t.TempDir(), Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	m, err := x.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(m)
	if !strings.Contains(string(encoded), `"files":[]`) {
		t.Fatalf("empty workspace must send an empty file list, got %s", encoded)
	}
}

func TestIndexFollowsEditsWithoutRescanning(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"src/a.ts": "a", "src/b.ts": "b"})
	x, err := OpenIndex(context.Background(), root, Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if !x.Watching() {
		t.Skip("no native watcher on this platform")
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	cancel := x.Subscribe(func(changes []Change) {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range changes {
			seen[c.Path] = c.Entry != nil
		}
	})
	defer cancel()
	before := manifestPaths(t, x)
	writeTree(t, root, map[string]string{"src/a.ts": "changed", "src/new/c.ts": "c"})
	os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0700)
	os.WriteFile(filepath.Join(root, "node_modules", "pkg", "index.js"), []byte("x"), 0600)
	eventually(t, "edit, new directory", func() bool {
		after := manifestPaths(t, x)
		_, hasNew := after["src/new/c.ts"]
		return hasNew && after["src/a.ts"] != before["src/a.ts"]
	})
	if _, ok := manifestPaths(t, x)["node_modules/pkg/index.js"]; ok {
		t.Fatal("ignored dependency directory entered the index")
	}
	os.RemoveAll(filepath.Join(root, "src", "new"))
	os.Remove(filepath.Join(root, "src", "b.ts"))
	eventually(t, "removals", func() bool {
		after := manifestPaths(t, x)
		_, c := after["src/new/c.ts"]
		_, b := after["src/b.ts"]
		return !c && !b
	})
	mu.Lock()
	defer mu.Unlock()
	if !seen["src/a.ts"] {
		t.Fatal("subscriber missed the edit", seen)
	}
	if present, ok := seen["src/b.ts"]; !ok || present {
		t.Fatal("subscriber missed the removal", seen)
	}
}
