package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tidalbridge/packages/protocol"
	"tidalbridge/packages/transport"
	workspacesync "tidalbridge/packages/workspace-sync"
)

// linkGrace is how long a running remote attempt survives a broken USB/ADB
// tunnel. An ADB server restart drops every forward; the worker keeps the
// process running, so the host repairs the tunnel and resumes polling.
const linkGrace = 90 * time.Second

// serviceLease stops a terminal-attached service whose client vanished
// (killed without cancelling) instead of leaving it running on the worker.
const serviceLease = 20 * time.Second

// treeKey names the worker's persistent copy of a host workspace.
func treeKey(workspace string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(workspace))))
	return "ws-" + hex.EncodeToString(sum[:12])
}

func isLinkError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"connection refused", "actively refused", "forcibly closed", "connection reset", "broken pipe", "eof"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (h *Host) endpointOf(node protocol.WorkerNode) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if n := h.nodes[node.ID]; n != nil && n.Endpoint != "" {
		return n.Endpoint
	}
	return node.Endpoint
}

// repairLink re-creates the ADB forward for a real worker and returns the
// new local endpoint. Mock workers have fixed endpoints.
func (h *Host) repairLink(ctx context.Context, id string) (string, error) {
	h.linkMu.Lock()
	defer h.linkMu.Unlock()
	h.mu.RLock()
	n := h.nodes[id]
	if n == nil {
		h.mu.RUnlock()
		return "", fmt.Errorf("unknown worker")
	}
	serial, endpoint := n.Serial, n.Endpoint
	cfg := h.cfg
	h.mu.RUnlock()
	port := 0
	for _, d := range cfg.Approved {
		if d.Serial == serial {
			if d.Mock {
				return endpoint, nil
			}
			port = d.Port
		}
	}
	if port == 0 {
		return endpoint, fmt.Errorf("worker is not approved")
	}
	local, err := transport.EnsureForward(ctx, transport.FindADB(cfg.ADB), serial, port)
	if err != nil {
		return endpoint, err
	}
	endpoint = "http://127.0.0.1:" + local
	h.mu.Lock()
	if n := h.nodes[id]; n != nil {
		n.Endpoint = endpoint
	}
	h.mu.Unlock()
	return endpoint, nil
}

// call performs a worker request, repairing the tunnel once on link errors.
func (h *Host) call(ctx context.Context, node protocol.WorkerNode, method, path string, in, out any) error {
	err := transport.Request(ctx, h.endpointOf(node), node.Token, method, path, in, out)
	if err != nil && isLinkError(err) && ctx.Err() == nil {
		if endpoint, repairErr := h.repairLink(ctx, node.ID); repairErr == nil {
			return transport.Request(ctx, endpoint, node.Token, method, path, in, out)
		}
	}
	return err
}

func (h *Host) upload(ctx context.Context, node protocol.WorkerNode, hash, path string, size int64) error {
	err := transport.Upload(ctx, h.endpointOf(node), node.Token, hash, path, size)
	if err != nil && isLinkError(err) && ctx.Err() == nil {
		if endpoint, repairErr := h.repairLink(ctx, node.ID); repairErr == nil {
			return transport.Upload(ctx, endpoint, node.Token, hash, path, size)
		}
	}
	return err
}

// uploadParallelism keeps several blob uploads in flight: on a first sync
// the per-request latency of the ADB tunnel, not bandwidth, dominates for
// thousands of small source files. The worker serves requests on threads.
const uploadParallelism = 6

// uploadAll sends files' content and returns the bytes sent; it stops at the
// first failure.
func (h *Host) uploadAll(ctx context.Context, node protocol.WorkerNode, workspace string, files []protocol.FileEntry) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan protocol.FileEntry)
	var (
		mu    sync.Mutex
		sent  int64
		first error
		wg    sync.WaitGroup
	)
	for i := 0; i < uploadParallelism && i < len(files); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range work {
				err := h.upload(ctx, node, f.Hash, filepath.Join(workspace, filepath.FromSlash(f.Path)), f.Size)
				mu.Lock()
				if err == nil {
					sent += f.Size
				} else if first == nil {
					first = fmt.Errorf("%s: %w", f.Path, err)
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, f := range files {
		select {
		case work <- f:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	if first == nil {
		first = ctx.Err()
	}
	return sent, first
}

func (h *Host) download(ctx context.Context, node protocol.WorkerNode, path, destination string) error {
	err := transport.Download(ctx, h.endpointOf(node), node.Token, path, destination)
	if err != nil && isLinkError(err) && ctx.Err() == nil {
		if endpoint, repairErr := h.repairLink(ctx, node.ID); repairErr == nil {
			return transport.Download(ctx, endpoint, node.Token, path, destination)
		}
	}
	return err
}

// unknownContent returns manifest entries whose content this host has not
// yet confirmed on the worker, deduplicated by hash.
func (h *Host) unknownContent(nodeID string, m *protocol.Manifest) []protocol.FileEntry {
	h.knownMu.Lock()
	defer h.knownMu.Unlock()
	known := h.known[nodeID]
	seen := map[string]bool{}
	var out []protocol.FileEntry
	for _, f := range m.Files {
		if known[f.Hash] || seen[f.Hash] {
			continue
		}
		seen[f.Hash] = true
		out = append(out, f)
	}
	return out
}

func (h *Host) markKnown(nodeID string, hashes []string) {
	h.knownMu.Lock()
	defer h.knownMu.Unlock()
	set := h.known[nodeID]
	if set == nil || len(set) > 250000 {
		set = map[string]bool{}
		h.known[nodeID] = set
	}
	for _, hash := range hashes {
		set[hash] = true
	}
}

func (h *Host) forgetKnown(nodeID string) {
	h.knownMu.Lock()
	delete(h.known, nodeID)
	h.knownMu.Unlock()
}

// missingBytes asks the worker about unknown content only and returns the
// bytes still to transfer. Content reported present becomes known.
func (h *Host) missingBytes(ctx context.Context, node protocol.WorkerNode, m *protocol.Manifest) (int64, error) {
	unknown := h.unknownContent(node.ID, m)
	if len(unknown) == 0 {
		return 0, nil
	}
	var missing struct {
		Missing []string `json:"missing"`
	}
	if err := h.call(ctx, node, "POST", "/v1/sync/missing", protocol.Manifest{ID: m.ID, Files: unknown}, &missing); err != nil {
		return 0, err
	}
	absent := map[string]bool{}
	for _, hash := range missing.Missing {
		absent[hash] = true
	}
	var bytes int64
	var present []string
	for _, f := range unknown {
		if absent[f.Hash] {
			bytes += f.Size
		} else {
			present = append(present, f.Hash)
		}
	}
	h.markKnown(node.ID, present)
	return bytes, nil
}

// refreshEntries re-reads the manifest entries of files that were being
// uploaded, after one turned out to differ from its indexed hash. Files that
// disappeared leave the manifest. It reports whether anything changed.
func refreshEntries(workspace string, m *protocol.Manifest, batch []protocol.FileEntry) bool {
	stale := map[string]bool{}
	for _, f := range batch {
		stale[f.Path] = true
	}
	changed := false
	kept := m.Files[:0]
	var total int64
	for _, f := range m.Files {
		if stale[f.Path] {
			path := filepath.Join(workspace, filepath.FromSlash(f.Path))
			info, err := os.Stat(path)
			hash := ""
			if err == nil {
				hash, err = hashFile(path)
			}
			if err != nil {
				changed = true
				continue
			}
			if hash != f.Hash || info.Size() != f.Size {
				f.Hash, f.Size, changed = hash, info.Size(), true
			}
		}
		total += f.Size
		kept = append(kept, f)
	}
	m.Files, m.TotalBytes = kept, total
	return changed
}

// syncTree brings the worker's persistent tree for this workspace to m,
// uploading only content the worker lacks.
func (h *Host) syncTree(ctx context.Context, node protocol.WorkerNode, workspace, key, nest string, m *protocol.Manifest) (sent int64, elapsedMS float64, err error) {
	started := time.Now()
	for attempt := 0; attempt < 2; attempt++ {
		unknown := h.unknownContent(node.ID, m)
		if len(unknown) > 0 {
			var missing struct {
				Missing []string `json:"missing"`
			}
			if err = h.call(ctx, node, "POST", "/v1/sync/missing", protocol.Manifest{ID: m.ID, Files: unknown}, &missing); err != nil {
				return
			}
			byHash := map[string]protocol.FileEntry{}
			for _, f := range unknown {
				byHash[f.Hash] = f
			}
			var batch []protocol.FileEntry
			for _, hash := range missing.Missing {
				if f, ok := byHash[hash]; ok {
					batch = append(batch, f)
				}
			}
			var n int64
			n, err = h.uploadAll(ctx, node, workspace, batch)
			sent += n
			if err != nil {
				if attempt == 0 && strings.Contains(err.Error(), "invalid blob") && refreshEntries(workspace, m, batch) {
					// A file changed after it was indexed (an editor or another
					// agent was writing it): sync what is on disk now.
					continue
				}
				return
			}
			hashes := make([]string, 0, len(unknown))
			for _, f := range unknown {
				hashes = append(hashes, f.Hash)
			}
			h.markKnown(node.ID, hashes)
		}
		body := struct {
			protocol.Manifest
			WorkspaceKey string `json:"workspace_key"`
			Nest         string `json:"nest,omitempty"`
		}{*m, key, nest}
		err = h.call(ctx, node, "POST", "/v1/sync/apply", body, nil)
		if err != nil && strings.Contains(err.Error(), "missing content") && attempt == 0 {
			// The worker pruned content this host assumed present.
			h.forgetKnown(node.ID)
			continue
		}
		break
	}
	elapsedMS = float64(time.Since(started).Microseconds()) / 1000
	return
}

type remoteState struct {
	State         string   `json:"state"`
	ExitCode      *int     `json:"exit_code"`
	Error         string   `json:"error"`
	PeakRAMMB     *float64 `json:"peak_ram_mb"`
	RAMMB         *float64 `json:"ram_mb"`
	CPUSecs       *float64 `json:"cpu_seconds"`
	Ports         []int    `json:"ports"`
	WorkspacePath string   `json:"workspace_path"`
	// Changes made by a write-back job, listed before it reports its exit.
	Changes *protocol.WorkerChanges `json:"changes"`
	// ServicesUsed: whether the job reached a laptop service tunnel.
	ServicesUsed *bool `json:"services_used"`
}

// pathRewriter replaces the worker's copy of the workspace with the host
// path in streamed output, so file paths in compiler, linter and test output
// open on Windows. A match may span writes: the bytes that could begin one
// are held until the next write or Flush.
type pathRewriter struct {
	w        io.Writer
	from, to []byte
	pending  []byte
}

func newPathRewriter(w io.Writer, from, to string) *pathRewriter {
	return &pathRewriter{w: w, from: []byte(from), to: []byte(to)}
}

func (r *pathRewriter) Write(p []byte) (int, error) {
	data := append(r.pending, p...)
	r.pending = nil
	data = bytes.ReplaceAll(data, r.from, r.to)
	// Hold back a tail that is a proper prefix of the path.
	keep := 0
	for n := min(len(r.from)-1, len(data)); n > 0; n-- {
		if bytes.HasSuffix(data, r.from[:n]) {
			keep = n
			break
		}
	}
	r.pending = append(r.pending, data[len(data)-keep:]...)
	if _, err := r.w.Write(data[:len(data)-keep]); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (r *pathRewriter) Flush() {
	if len(r.pending) > 0 {
		r.w.Write(r.pending)
		r.pending = nil
	}
}

func (h *Host) runRemote(ctx context.Context, id string, spec protocol.JobSpec, node protocol.WorkerNode, m *protocol.Manifest, a protocol.Attempt, out, errout io.Writer) (int, protocol.Attempt, error, bool) {
	a.CacheWarm = m == nil
	remoteSpec := map[string]any{}
	b, _ := json.Marshal(spec)
	json.Unmarshal(b, &remoteSpec)
	remoteSpec["workspace"] = ""
	delete(remoteSpec, "local_argv") // Windows runtime paths never leave the host.
	delete(remoteSpec, "speculative")
	key := ""
	if m != nil {
		var nest string
		m, key, nest = h.treeFor(spec.Workspace, m)
		sent, syncMS, err := h.syncTree(ctx, node, spec.Workspace, key, nest, m)
		if err != nil {
			return 0, a, err, true
		}
		a.BytesSent, a.SyncMS, a.CacheWarm = sent, syncMS, sent == 0
		a.Tree = treeIdentity(m)
		remoteSpec["workspace_id"] = m.ID
		remoteSpec["workspace_key"] = key
	}
	if spec.Service {
		remoteSpec["timeout_seconds"] = 86400
	}
	for _, port := range spec.ReversePorts {
		if err := h.reverse(ctx, node, port); err != nil {
			fmt.Fprintf(errout, "[Tidal Bridge] could not expose laptop port %d to the worker: %v\n", port, err)
		}
		defer h.unreverse(node, port)
	}
	var state remoteState
	remoteID := a.ID
	defer func() {
		h.mu.Lock()
		delete(h.live, id)
		h.mu.Unlock()
	}()
	if err := h.call(ctx, node, "POST", "/v1/jobs", map[string]any{"id": remoteID, "spec": remoteSpec}, &state); err != nil {
		return 0, a, err, true
	}
	// The job's own output, kept as the worker sent it, to read after it ends.
	logOut, logErr := out, errout
	if local, err := filepath.Abs(spec.Workspace); err == nil && spec.Workspace != "" && strings.HasPrefix(state.WorkspacePath, "/") {
		rewriteOut := newPathRewriter(out, state.WorkspacePath, filepath.ToSlash(local))
		rewriteErr := newPathRewriter(errout, state.WorkspacePath, filepath.ToSlash(local))
		out, errout = rewriteOut, rewriteErr
		defer rewriteOut.Flush()
		defer rewriteErr.Flush()
	}
	if spec.Service && m != nil {
		stop := h.liveSync(ctx, id, node, spec, key)
		defer stop()
	}
	forwarded := map[int]bool{}
	defer func() {
		for port := range forwarded {
			h.unforward(node, port)
		}
		h.setForwarded(id, nil)
	}()
	offsets := map[string]int64{"stdout": 0, "stderr": 0}
	readOutput := func() error {
		for _, stream := range []string{"stdout", "stderr"} {
			for {
				var chunk struct {
					Data       string `json:"data_b64"`
					Text       string `json:"text"`
					NextOffset int64  `json:"next_offset"`
				}
				if e := h.call(ctx, node, "GET", fmt.Sprintf("/v1/jobs/%s/output?stream=%s&offset=%d", remoteID, stream, offsets[stream]), nil, &chunk); e != nil {
					return e
				}
				if chunk.NextOffset == offsets[stream] {
					break
				}
				offsets[stream] = chunk.NextOffset
				bytes, decodeErr := base64Decode(chunk.Data)
				if decodeErr != nil {
					return decodeErr
				}
				if chunk.Data == "" && chunk.Text != "" {
					bytes = []byte(chunk.Text)
				} // v1 legacy workers; upgrade to lossless base64 chunks when possible.
				if stream == "stdout" {
					out.Write(bytes)
				} else {
					errout.Write(bytes)
				}
				if len(bytes) < 65536 {
					break
				}
			}
		}
		return nil
	}
	interval := 300 * time.Millisecond
	if spec.Service {
		interval = 250 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var linkDown time.Time
	// Replay-safe work with a local fallback should move on quickly; work
	// that cannot be replayed is worth waiting for.
	grace := linkGrace
	if spec.Policy.Idempotent && spec.Policy.Retryable && spec.Policy.LocalFallback {
		grace = 6 * time.Second
	}
	for {
		select {
		case <-ctx.Done():
			cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			h.call(cancelCtx, node, "POST", "/v1/jobs/"+remoteID+"/cancel", map[string]any{}, nil)
			cancel()
			return 0, a, ctx.Err(), false
		case <-ticker.C:
			err := h.call(ctx, node, "GET", "/v1/jobs/"+remoteID, nil, &state)
			if err == nil {
				err = readOutput()
			}
			if err != nil {
				if ctx.Err() == nil && isLinkError(err) {
					// The process keeps running on the worker; wait for the
					// tunnel to come back before giving up on the attempt.
					if linkDown.IsZero() {
						linkDown = time.Now()
						fmt.Fprintln(errout, "[Tidal Bridge] connection to the worker interrupted; reconnecting...")
					}
					if time.Since(linkDown) < grace {
						continue
					}
				}
				if ctx.Err() != nil {
					// Cancelled while a poll was in flight: the worker must
					// still stop the job (a dev server would keep its port).
					cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					h.call(cancelCtx, node, "POST", "/v1/jobs/"+remoteID+"/cancel", map[string]any{}, nil)
					cancel()
					return 0, a, ctx.Err(), false
				}
				return 0, a, err, true
			}
			if !linkDown.IsZero() {
				linkDown = time.Time{}
				fmt.Fprintln(errout, "[Tidal Bridge] reconnected to the worker")
			}
			h.mu.Lock()
			if state.State == "RUNNING" {
				h.trackUsageLocked(id, state.RAMMB, state.CPUSecs)
			}
			if j := h.jobs[id]; j != nil {
				if v, ok := out.(*boundedLog); ok {
					j.Stdout = v.text.String()
				}
				if v, ok := errout.(*boundedLog); ok {
					j.Stderr = v.text.String()
				}
			}
			h.mu.Unlock()
			if spec.Service {
				h.forwardPorts(ctx, node, id, spec, state.Ports, forwarded, errout)
				if spec.AttachedClient && time.Since(h.lastSeen(id)) > serviceLease {
					// The terminal that started it was closed or killed.
					fmt.Fprintln(errout, "[Tidal Bridge] the attached terminal went away; stopping the service")
					cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					h.call(cancelCtx, node, "POST", "/v1/jobs/"+remoteID+"/cancel", map[string]any{}, nil)
					cancel()
					return 0, a, fmt.Errorf("service stopped: its terminal stopped polling"), false
				}
			}
			if state.State != "RUNNING" && state.State != "QUEUED" {
				a.WorkerPeakRAMMB = state.PeakRAMMB
				a.WorkerCPUSeconds = state.CPUSecs
				if len(spec.ReversePorts) > 0 {
					a.ServicesUsed = state.ServicesUsed
				}
				if state.Error != "" {
					return 0, a, fmt.Errorf("worker: %s", state.Error), false
				}
				if state.ExitCode == nil {
					return 0, a, fmt.Errorf("worker ended without exit code: %s", state.State), false
				}
				if spec.Policy.WriteBack && m != nil && state.Changes != nil {
					result := h.writeBack(ctx, node, remoteID, spec, m, *state.Changes, errout)
					h.mu.Lock()
					if j := h.jobs[id]; j != nil {
						j.WriteBack = result
					}
					h.mu.Unlock()
				}
				if *state.ExitCode != 0 && key != "" {
					output := ""
					for _, w := range []io.Writer{logOut, logErr} {
						if v, ok := w.(*boundedLog); ok {
							v.mu.Lock()
							output += v.text.String()
							v.mu.Unlock()
						}
					}
					reason := ""
					if h.learnOutside(spec.Workspace, state.WorkspacePath, output) {
						reason = "outside"
					} else if slowFailure.MatchString(output) {
						reason = "timeout"
					}
					if reason != "" {
						h.mu.Lock()
						if j := h.jobs[id]; j != nil {
							j.Recheck = reason
						}
						h.mu.Unlock()
					}
				}
				for _, p := range spec.ExpectedOutputs {
					destination := filepath.Join(h.Dir, "results", id, a.ID, "artifacts", filepath.FromSlash(p))
					if e := h.download(ctx, node, "/v1/jobs/"+remoteID+"/artifact?path="+url.QueryEscape(p), destination); e != nil {
						return 0, a, e, false
					}
				}
				return *state.ExitCode, a, nil, false
			}
		}
	}
}

// errIndexing means a workspace's first index is still being built.
var errIndexing = errors.New("indexing the workspace for the first time")

type indexSlot struct {
	idx   *workspacesync.Index
	err   error
	ready chan struct{}
}

// workspaceIndex returns the live index for a workspace. The first scan of
// a large project can take minutes on a loaded laptop, so it runs in the
// background, independent of the request that triggered it; callers wait at
// most `wait` and otherwise get errIndexing (and run locally meanwhile).
func (h *Host) workspaceIndex(ctx context.Context, root string, includeEnv bool, wait time.Duration) (*workspacesync.Index, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	key := strings.ToLower(abs) + fmt.Sprintf("|env=%t", includeEnv)
	h.indexMu.Lock()
	slot := h.indexes[key]
	if slot == nil {
		slot = &indexSlot{ready: make(chan struct{})}
		h.indexes[key] = slot
		base := h.ctx
		if base == nil {
			base = context.Background()
		}
		go func() {
			idx, err := workspacesync.OpenIndex(base, abs, workspacesync.Options{CacheDir: filepath.Join(h.Dir, "cache", "hashes"), IncludeEnv: includeEnv})
			slot.idx, slot.err = idx, err
			close(slot.ready)
			if err != nil {
				h.indexMu.Lock()
				if h.indexes[key] == slot {
					delete(h.indexes, key) // retry on the next request
				}
				h.indexMu.Unlock()
			} else {
				h.log.Info("workspace_indexed", "workspace", abs, "watching", idx.Watching())
			}
		}()
	}
	h.indexMu.Unlock()
	// A ready index wins over an expired wait: with wait 0, a select would
	// otherwise pick either case at random.
	select {
	case <-slot.ready:
		return slot.idx, slot.err
	default:
	}
	var timeout <-chan time.Time
	if wait >= 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case <-slot.ready:
		return slot.idx, slot.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timeout:
		return nil, errIndexing
	}
}

// manifestFor returns the current workspace snapshot for a job.
func (h *Host) manifestFor(ctx context.Context, spec protocol.JobSpec) (*protocol.Manifest, error) {
	return h.manifestWithin(ctx, spec, -1)
}

func (h *Host) manifestWithin(ctx context.Context, spec protocol.JobSpec, wait time.Duration) (*protocol.Manifest, error) {
	if spec.Workspace == "" {
		return nil, nil
	}
	idx, err := h.workspaceIndex(ctx, spec.Workspace, spec.Policy.SyncEnvFiles, wait)
	if err != nil {
		return nil, err
	}
	m, err := idx.Manifest(ctx)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (h *Host) pruneIndexes() {
	h.indexMu.Lock()
	defer h.indexMu.Unlock()
	for key, slot := range h.indexes {
		select {
		case <-slot.ready:
		default:
			continue
		}
		if idx := slot.idx; idx != nil && idx.Listeners() == 0 && time.Since(idx.LastUse()) > 30*time.Minute {
			idx.Close()
			delete(h.indexes, key)
		}
	}
}
