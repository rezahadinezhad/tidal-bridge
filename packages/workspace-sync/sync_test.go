package workspacesync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestManifestSecretsAndChanges(t *testing.T) {
	dir := t.TempDir()
	for path, text := range map[string]string{"a.py": "print(1)", ".env": "secret", "id_rsa": "secret", "node_modules/foo": "native", "ignored.tmp": "skip", ".tidalbridgeignore": "*.tmp\n"} {
		full := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(full), 0700)
		os.WriteFile(full, []byte(text), 0600)
	}
	a, e := Scan(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	if len(a.Files) != 2 {
		t.Fatal(a)
	}
	os.WriteFile(filepath.Join(dir, "a.py"), []byte("print(2)"), 0600)
	b, e := Scan(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, file := range a.Files {
		if file.Path == "a.py" {
			for _, other := range b.Files {
				if other.Path == "a.py" && other.Hash == file.Hash {
					t.Fatal("content change missed")
				}
			}
		}
	}
	for _, p := range []string{"../a", "C:/a", "a\\b", "a/../../b"} {
		if SafePath(p) {
			t.Fatal(p)
		}
	}
}
