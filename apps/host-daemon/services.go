package host

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"time"

	"tidalbridge/packages/protocol"
	"tidalbridge/packages/transport"
	workspacesync "tidalbridge/packages/workspace-sync"
)

func base64Decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// adbTarget returns the ADB binary and serial for a real worker; mock
// workers share the host's loopback and need no port mapping.
func (h *Host) adbTarget(node protocol.WorkerNode) (adb, serial string, mock bool) {
	h.mu.RLock()
	cfg := h.cfg
	serial = node.Serial
	if n := h.nodes[node.ID]; n != nil && n.Serial != "" {
		serial = n.Serial
	}
	h.mu.RUnlock()
	for _, d := range cfg.Approved {
		if d.Serial == serial && d.Mock {
			return "", serial, true
		}
	}
	return transport.FindADB(cfg.ADB), serial, serial == ""
}

// PortFree reports whether nothing on the host serves a loopback TCP port.
// Binding alone is not enough on Windows: a server listening on the
// wildcard address ([::] or 0.0.0.0) does not stop another socket from
// binding 127.0.0.1 on the same port, and that more specific forward would
// then take the server's IPv4 localhost traffic. So any listener that
// accepts a connection counts as busy.
func PortFree(port int) bool {
	for _, address := range []string{"127.0.0.1", "[::1]"} {
		if conn, err := net.DialTimeout("tcp", address+":"+strconv.Itoa(port), 300*time.Millisecond); err == nil {
			conn.Close()
			return false
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// reverse makes a laptop loopback port reachable from the worker
// (worker 127.0.0.1:port -> laptop 127.0.0.1:port), reference counted.
func (h *Host) reverse(ctx context.Context, node protocol.WorkerNode, port int) error {
	adb, serial, mock := h.adbTarget(node)
	if mock {
		return nil
	}
	h.portMu.Lock()
	defer h.portMu.Unlock()
	key := serial + ":" + strconv.Itoa(port)
	if h.reverseRefs[key] > 0 {
		h.reverseRefs[key]++
		return nil
	}
	mapping := "tcp:" + strconv.Itoa(port)
	if _, err := transport.ADB(ctx, adb, serial, "reverse", mapping, mapping); err != nil {
		return err
	}
	h.reverseRefs[key] = 1
	return nil
}

func (h *Host) unreverse(node protocol.WorkerNode, port int) {
	adb, serial, mock := h.adbTarget(node)
	if mock {
		return
	}
	h.portMu.Lock()
	defer h.portMu.Unlock()
	key := serial + ":" + strconv.Itoa(port)
	if h.reverseRefs[key] == 0 {
		return
	}
	h.reverseRefs[key]--
	if h.reverseRefs[key] == 0 {
		delete(h.reverseRefs, key)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		transport.ADB(ctx, adb, serial, "reverse", "--remove", "tcp:"+strconv.Itoa(port))
	}
}

// forwardPorts serves a service's ports on the laptop's loopback. Declared
// ports are forwarded; without declarations, announced ports are forwarded
// unless something on the laptop already uses them.
func (h *Host) forwardPorts(ctx context.Context, node protocol.WorkerNode, jobID string, spec protocol.JobSpec, announced []int, attempted map[int]bool, errout io.Writer) {
	want := spec.Ports
	if len(want) == 0 {
		reversed := map[int]bool{}
		for _, p := range spec.ReversePorts {
			reversed[p] = true
		}
		for _, p := range announced {
			if !reversed[p] {
				want = append(want, p)
			}
		}
	}
	adb, serial, mock := h.adbTarget(node)
	changed := false
	for _, port := range want {
		if _, done := attempted[port]; done {
			continue
		}
		changed = true
		if mock {
			attempted[port] = true
			continue
		}
		if len(spec.Ports) == 0 && !PortFree(port) {
			attempted[port] = false
			continue
		}
		mapping := "tcp:" + strconv.Itoa(port)
		if _, err := transport.ADB(ctx, adb, serial, "forward", mapping, mapping); err != nil {
			attempted[port] = false
			fmt.Fprintf(errout, "[Tidal Bridge] could not serve port %d on this laptop: %v\n", port, err)
			continue
		}
		attempted[port] = true
		fmt.Fprintf(errout, "[Tidal Bridge] http://localhost:%d is served by the worker\n", port)
	}
	if changed {
		var ports []int
		for port, ok := range attempted {
			if ok {
				ports = append(ports, port)
			}
		}
		sort.Ints(ports)
		h.setForwarded(jobID, ports)
	}
}

func (h *Host) unforward(node protocol.WorkerNode, port int) {
	adb, serial, mock := h.adbTarget(node)
	if mock {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transport.ADB(ctx, adb, serial, "forward", "--remove", "tcp:"+strconv.Itoa(port))
}

func (h *Host) setForwarded(jobID string, ports []int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if j := h.jobs[jobID]; j != nil {
		j.Forwarded = ports
	}
}

// liveSync pushes workspace edits to the worker while a service runs, so a
// dev server on the worker hot-reloads exactly as it would locally.
func (h *Host) liveSync(ctx context.Context, jobID string, node protocol.WorkerNode, spec protocol.JobSpec, key string) func() {
	idx, err := h.workspaceIndex(ctx, spec.Workspace, spec.Policy.SyncEnvFiles, -1)
	if err != nil {
		return func() {}
	}
	trigger := make(chan struct{}, 1)
	unsubscribe := idx.Subscribe(func([]workspacesync.Change) {
		select {
		case trigger <- struct{}{}:
		default:
		}
	})
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-trigger:
				m, err := idx.Manifest(ctx)
				if err != nil {
					continue
				}
				synced, k, nest := h.treeFor(spec.Workspace, &m)
				if k != key {
					synced, nest = &m, ""
				}
				if _, _, err := h.syncTree(ctx, node, spec.Workspace, key, nest, synced); err != nil {
					h.log.Error("live_sync", "job", jobID, "error", err.Error())
				}
			}
		}
	}()
	return func() {
		unsubscribe()
		close(done)
	}
}

// stopServices waits for active attempts (including services) to release
// their worker processes and port mappings after Shutdown cancelled them.
func (h *Host) stopServices(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		h.running.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
