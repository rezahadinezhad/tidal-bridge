package workspacesync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, text := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func paths(t *testing.T, root string, opts Options) map[string]string {
	t.Helper()
	m, err := ScanWith(context.Background(), root, opts)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range m.Files {
		out[f.Path] = f.Hash
	}
	return out
}

func TestNonGitWorkspaceHonorsNestedIgnoresAndDefaults(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/app.ts":                       "app",
		"dev-server.log":                   "noise",
		"tsconfig.tsbuildinfo":             "cache",
		".gitignore":                       "/coverage\n*.tmp\n",
		"keep.tmp":                         "ignored by root",
		"crypto/messenger/Cargo.toml":      "[package]",
		"crypto/messenger/src/lib.rs":      "fn x(){}",
		"crypto/messenger/target/big.rlib": "build output",
		"crypto/messenger/pkg/x.wasm":      "wasm",
		"tools/target/notes.md":            "not a rust target",
		"pkg/.gitignore":                   "generated/\n!generated/keep.txt\n",
		"pkg/generated/drop.txt":           "x",
		"pkg/other.txt":                    "y",
		".turbo/cache.bin":                 "z",
		".env.local":                       "SECRET=1",
		"certs/server.key":                 "key",
		"src/lib/api/coach-credentials.ts": "export const x = 1",
		"src/lib/credentials/present.ts":   "export const y = 2",
		"config/credentials.json":          "{\"secret\":1}",
		".git-credentials":                 "https://user:pass@host",
	})
	got := paths(t, root, Options{})
	for _, want := range []string{"src/app.ts", ".gitignore", "crypto/messenger/Cargo.toml", "crypto/messenger/src/lib.rs", "crypto/messenger/pkg/x.wasm", "tools/target/notes.md", "pkg/other.txt", "pkg/.gitignore", "src/lib/api/coach-credentials.ts", "src/lib/credentials/present.ts"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	for _, unwanted := range []string{"dev-server.log", "tsconfig.tsbuildinfo", "keep.tmp", "crypto/messenger/target/big.rlib", "pkg/generated/drop.txt", ".turbo/cache.bin", ".env.local", "certs/server.key", "config/credentials.json", ".git-credentials"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("unexpectedly included %s", unwanted)
		}
	}
	withEnv := paths(t, root, Options{IncludeEnv: true})
	if _, ok := withEnv[".env.local"]; !ok {
		t.Error("explicit env opt-in did not include .env.local")
	}
	if _, ok := withEnv["certs/server.key"]; ok {
		t.Error("env opt-in must never include key files")
	}
}

func TestProjectRulesOverrideGitignores(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"crypto/messenger/.gitignore":        "target/\npkg/\n",
		"crypto/messenger/pkg/adapter.js":    "generated",
		"crypto/messenger/pkg/adapter.log":   "noise",
		"crypto/messenger/target/x.rlib":     "build",
		"crypto/messenger/other/.gitignore":  "*.gen\n",
		"crypto/messenger/other/kept.txt":    "kept",
		"crypto/messenger/other/dropped.gen": "dropped",
		".tidalbridgeignore":                 "!crypto/messenger/pkg/\n",
		"src/app.ts":                         "app",
	})
	got := paths(t, root, Options{})
	for _, want := range []string{"crypto/messenger/pkg/adapter.js", "crypto/messenger/other/kept.txt", "src/app.ts"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	for _, unwanted := range []string{"crypto/messenger/target/x.rlib", "crypto/messenger/other/dropped.gen", "crypto/messenger/pkg/adapter.log"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("unexpectedly included %s", unwanted)
		}
	}
	x, err := OpenIndex(context.Background(), root, Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.included("crypto/messenger/pkg/new.js") || x.included("crypto/messenger/target/y.rlib") || x.included("crypto/messenger/other/new.gen") {
		t.Error("live index disagrees with the scan about project overrides")
	}
}

func TestHashCacheSeesChangesAndForgetsDeletedFiles(t *testing.T) {
	root := t.TempDir()
	cache := t.TempDir()
	writeTree(t, root, map[string]string{"a.txt": "one", "b.txt": "two"})
	first := paths(t, root, Options{CacheDir: cache})
	entries, _ := os.ReadDir(cache)
	if len(entries) != 1 {
		t.Fatalf("expected one cache file, got %d", len(entries))
	}
	// Same size, new content and time: the cache must not hide the change.
	time.Sleep(20 * time.Millisecond)
	writeTree(t, root, map[string]string{"a.txt": "ONE"})
	os.Remove(filepath.Join(root, "b.txt"))
	second := paths(t, root, Options{CacheDir: cache})
	if second["a.txt"] == first["a.txt"] {
		t.Fatal("cached hash hid a content change")
	}
	if _, ok := second["b.txt"]; ok {
		t.Fatal("deleted file still listed")
	}
}

func TestGitignoreNeverHidesPythonPackagesOutsideGit(t *testing.T) {
	root := t.TempDir()
	write := func(path, content string) {
		full := filepath.Join(root, filepath.FromSlash(path))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(content), 0o644)
	}
	write(".gitignore", "media/\n")
	write("apps/media/__init__.py", "")
	write("apps/media/views.py", "x = 1")
	write("media/upload.jpg", "image")
	write(".venv/lib/pkg/__init__.py", "")
	m, err := ScanWith(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range m.Files {
		got[f.Path] = true
	}
	if !got["apps/media/views.py"] || !got["apps/media/__init__.py"] {
		t.Fatal("a Python package hidden only by .gitignore must sync", got)
	}
	if got["media/upload.jpg"] || got[".venv/lib/pkg/__init__.py"] {
		t.Fatal("data directories and built-in exclusions stay out", got)
	}
	x, err := OpenIndex(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if !x.Included("apps/media/views.py") || x.Included("media/upload.jpg") {
		t.Fatal("the live index must agree with the scan")
	}
}
