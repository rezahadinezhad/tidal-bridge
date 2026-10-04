package host

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"tidalbridge/packages/protocol"
	"tidalbridge/packages/transport"
)

// Content goes to a phone in packs: many files per request, compressed as a
// whole unless they are already compressed (images, archives, fonts, media).
// Over a USB 2 tunnel the per-request round trip, not bandwidth, limited a
// first sync of many small source files, and text shrinks several times.
// Files over 32 MB keep their own streamed request; a worker without packs
// gets one request per file, as before.
const (
	packBytes    = 8 << 20
	packFileMax  = 32 << 20
	packParallel = 3
)

var precompressed = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".avif": true,
	".ico": true, ".zip": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true, ".zst": true, ".7z": true, ".rar": true,
	".br": true, ".woff": true, ".woff2": true, ".mp4": true, ".mov": true, ".webm": true, ".mp3": true, ".ogg": true,
	".m4a": true, ".pdf": true, ".jar": true, ".apk": true, ".aab": true, ".whl": true}

type pack struct {
	files    []protocol.FileEntry
	compress bool
}

// packsOf groups files into packs of up to 8 MB, compressible and
// already-compressed content apart; files over 32 MB are returned alone.
func packsOf(files []protocol.FileEntry) (packs []pack, large []protocol.FileEntry) {
	current := [2]pack{{compress: true}, {}}
	var size [2]int64
	flush := func(i int) {
		if len(current[i].files) > 0 {
			packs = append(packs, current[i])
			current[i], size[i] = pack{compress: i == 0}, 0
		}
	}
	for _, f := range files {
		if f.Size > packFileMax {
			large = append(large, f)
			continue
		}
		i := 0
		if precompressed[strings.ToLower(filepath.Ext(f.Path))] {
			i = 1
		}
		if size[i] > 0 && size[i]+f.Size > packBytes {
			flush(i)
		}
		current[i].files = append(current[i].files, f)
		size[i] += f.Size
	}
	flush(0)
	flush(1)
	return packs, large
}

// sendContent uploads files' content and returns the bytes sent; it stops at
// the first failure.
func (h *Host) sendContent(ctx context.Context, node protocol.WorkerNode, workspace string, files []protocol.FileEntry) (int64, error) {
	if !node.Profile.Capabilities.Features["blob_pack"] || len(files) == 0 {
		return h.uploadAll(ctx, node, workspace, files)
	}
	packs, large := packsOf(files)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan pack)
	var (
		mu    sync.Mutex
		sent  int64
		first error
		wg    sync.WaitGroup
	)
	for i := 0; i < packParallel && i < len(packs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range work {
				n, err := h.sendPack(ctx, node, workspace, p)
				mu.Lock()
				if err == nil {
					sent += n
				} else if first == nil {
					first = err
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, p := range packs {
		select {
		case work <- p:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	if first == nil {
		first = ctx.Err()
	}
	if first != nil {
		return sent, first
	}
	n, err := h.uploadAll(ctx, node, workspace, large)
	return sent + n, err
}

// sendPack builds one pack: per file a 64-hex hash, a 16-hex length and the
// content (store_pack in the worker reads it).
func (h *Host) sendPack(ctx context.Context, node protocol.WorkerNode, workspace string, p pack) (int64, error) {
	var body bytes.Buffer
	var w io.Writer = &body
	var z *zlib.Writer
	if p.compress {
		z, _ = zlib.NewWriterLevel(&body, zlib.BestSpeed)
		w = z
	}
	var n int64
	for _, f := range p.files {
		content, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(f.Path)))
		if err != nil {
			return 0, fmt.Errorf("%s: %w", f.Path, err)
		}
		if int64(len(content)) != f.Size {
			// Changed since it was indexed; the sync re-reads such files.
			return 0, fmt.Errorf("%s: invalid blob: changed while syncing", f.Path)
		}
		fmt.Fprintf(w, "%s%016x", f.Hash, len(content))
		w.Write(content)
		n += f.Size
	}
	encoding := "identity"
	if z != nil {
		z.Close()
		encoding = "zlib"
	}
	err := transport.UploadPack(ctx, h.endpointOf(node), node.Token, body.Bytes(), encoding)
	if err != nil && isLinkError(err) && ctx.Err() == nil {
		if endpoint, repairErr := h.repairLink(ctx, node.ID); repairErr == nil {
			err = transport.UploadPack(ctx, endpoint, node.Token, body.Bytes(), encoding)
		}
	}
	return n, err
}
