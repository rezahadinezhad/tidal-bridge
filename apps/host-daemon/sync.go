package host

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"tidalbridge/packages/protocol"
)

// Sync brings a worker's persistent tree for root up to date without running
// anything (warm-up before routing, or an explicit MCP/CLI request).
func (h *Host) Sync(ctx context.Context, root, id string) (any, error) {
	h.mu.RLock()
	node := h.nodes[id]
	if node == nil {
		h.mu.RUnlock()
		return nil, fmt.Errorf("known stable device ID required")
	}
	n := *node
	h.mu.RUnlock()
	if n.State != "READY" && n.State != "BUSY" || n.Draining {
		return nil, fmt.Errorf("worker is not accepting sync")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	m, err := h.manifestFor(ctx, protocol.JobSpec{Workspace: abs})
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("workspace required")
	}
	m, key, nest := h.treeFor(abs, m)
	sent, _, err := h.syncTree(ctx, n, abs, key, nest, m)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workspace_id": m.ID, "workspace_key": key, "device_id": id, "files": len(m.Files), "bytes_sent": sent, "cache_warm": sent == 0, "duration_ms": time.Since(started).Milliseconds()}, nil
}
