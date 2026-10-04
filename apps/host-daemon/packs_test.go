package host

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"tidalbridge/packages/protocol"
)

func TestPacksGroupBySizeAndCompressibility(t *testing.T) {
	var files []protocol.FileEntry
	for i := range 20 {
		files = append(files, protocol.FileEntry{Path: "src/m" + strconv.Itoa(i) + ".ts", Size: 1 << 20})
	}
	files = append(files, protocol.FileEntry{Path: "logo.PNG", Size: 1000}, protocol.FileEntry{Path: "video.bin", Size: 40 << 20})
	packs, large := packsOf(files)
	if len(large) != 1 || large[0].Path != "video.bin" {
		t.Fatal("files over 32 MB keep their own request", large)
	}
	var compressed, plain, count int
	for _, p := range packs {
		var size int64
		for _, f := range p.files {
			size += f.Size
			count++
		}
		if size > packBytes {
			t.Fatal("a pack holds at most 8 MB", size)
		}
		if p.compress {
			compressed++
		} else {
			plain++
		}
	}
	if count != 21 || compressed != 3 || plain != 1 {
		t.Fatal("20 MB of source in three compressed packs, the image apart", compressed, plain, count)
	}
}

// fakeWorker stores blobs from packs and single uploads, checking each hash.
func fakeWorker(t *testing.T) (*httptest.Server, map[string]bool, *int) {
	stored, requests := map[string]bool{}, 0
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		requests++
		if r.URL.Path == "/v1/blobs/pack" {
			if r.Header.Get("X-Pack-Encoding") == "zlib" {
				z, err := zlib.NewReader(bytes.NewReader(body))
				if err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				body, _ = io.ReadAll(z)
			}
			for len(body) > 0 {
				digest, size, _ := string(body[:64]), body[64:80], 0
				n, _ := strconv.ParseInt(string(size), 16, 64)
				content := body[80 : 80+n]
				sum := sha256.Sum256(content)
				if hex.EncodeToString(sum[:]) != digest {
					http.Error(w, "invalid blob: content does not match hash", 400)
					return
				}
				stored[digest] = true
				body = body[80+n:]
			}
			w.Write([]byte(`{}`))
			return
		}
		sum := sha256.Sum256(body)
		stored[hex.EncodeToString(sum[:])] = strings.HasSuffix(r.URL.Path, hex.EncodeToString(sum[:]))
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server, stored, &requests
}

func TestContentIsSentInVerifiedPacks(t *testing.T) {
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	workspace := t.TempDir()
	var files []protocol.FileEntry
	var total int64
	for i := range 300 {
		content := []byte(strings.Repeat("export const value"+strconv.Itoa(i)+" = 1;\n", 40))
		path := filepath.Join(workspace, "src", "m"+strconv.Itoa(i)+".ts")
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, content, 0o600)
		sum := sha256.Sum256(content)
		files = append(files, protocol.FileEntry{Path: "src/m" + strconv.Itoa(i) + ".ts", Hash: hex.EncodeToString(sum[:]), Size: int64(len(content))})
		total += int64(len(content))
	}
	server, stored, requests := fakeWorker(t)
	node := protocol.WorkerNode{ID: "worker-a", Endpoint: server.URL, Profile: protocol.DeviceProfile{Capabilities: protocol.CapabilitySet{Features: map[string]bool{"blob_pack": true}}}}
	sent, err := h.sendContent(t.Context(), node, workspace, files)
	if err != nil || sent != total || len(stored) != 300 || *requests != 1 {
		t.Fatal("300 small files arrive verified in one compressed request", err, sent, len(stored), *requests)
	}

	old := node
	old.Profile.Capabilities.Features = map[string]bool{}
	server2, stored2, requests2 := fakeWorker(t)
	old.Endpoint = server2.URL
	if _, err := h.sendContent(t.Context(), old, workspace, files[:5]); err != nil || len(stored2) != 5 || *requests2 != 5 {
		t.Fatal("a worker without packs gets one request per file", err, len(stored2), *requests2)
	}

	os.WriteFile(filepath.Join(workspace, "src", "m0.ts"), []byte("changed"), 0o600)
	if _, err := h.sendContent(t.Context(), node, workspace, files[:1]); err == nil || !strings.Contains(err.Error(), "invalid blob") {
		t.Fatal("a file changed since indexing is reported so the sync re-reads it", err)
	}
}
