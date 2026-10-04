package host

import (
	"tidalbridge/packages/protocol"
)

// Identical commands share one run. When a test or check is already running
// on a phone on the same files (two agents, or one agent retrying), a second
// copy joins it instead of starting again, and gets its result.
//
// The host never runs a command nobody asked for. Running recent tests after
// edits went quiet ("pre-runs") was tried and removed: an agent edits in
// steps, so those runs tested half-finished work, failed for nothing and
// loaded the phone and the laptop, while an agent almost never asked for one
// of their answers. Reusing the agent's own results (results.go) gives the
// speed-up without extra runs.

// flight is a reusable job running on a phone, which identical commands join.
type flight struct {
	key, node string
	joiners   []string
}

// reuseKeyFor is the reuse key of spec on the workspace's current files.
func (h *Host) reuseKeyFor(spec protocol.JobSpec, m *protocol.Manifest) string {
	tree, _, _ := h.treeFor(spec.Workspace, m)
	return reuseKey(spec, treeIdentity(tree))
}

// flightFor is the job running the command with this reuse key, if any
// (guarded by flightMu).
func (h *Host) flightFor(key string) string {
	for id, f := range h.flights {
		if f.key == key {
			return id
		}
	}
	return ""
}

// startFlight registers a reusable job starting on a phone.
func (h *Host) startFlight(id string, spec protocol.JobSpec, m *protocol.Manifest, node string) {
	if !h.reusable(spec) || m == nil {
		return
	}
	key := h.reuseKeyFor(spec, m)
	h.flightMu.Lock()
	h.flights[id] = &flight{key: key, node: node}
	h.flightMu.Unlock()
}

// join attaches a job to an identical run in progress; false if none.
func (h *Host) join(id, key string) bool {
	h.flightMu.Lock()
	defer h.flightMu.Unlock()
	running := h.flightFor(key)
	if running == "" {
		return false
	}
	f := h.flights[running]
	f.joiners = append(f.joiners, id)
	return true
}

// landFlight ends a run: the jobs that joined it get its stored result, or
// run themselves when there is none (cancelled, failed to start, or not
// reusable after all).
func (h *Host) landFlight(id string) {
	h.flightMu.Lock()
	f := h.flights[id]
	delete(h.flights, id)
	h.flightMu.Unlock()
	if f == nil || len(f.joiners) == 0 {
		// Unjoined, a stored failure stays: it answers the agent's next
		// identical run once.
		return
	}
	h.reuseMu.Lock()
	var e *reuseEntry
	if stored := h.reuse[f.key]; stored != nil {
		found := *stored
		e = &found
		if e.ExitCode != 0 {
			h.dropReuseLocked(stored) // the joiners were its one reuse
		}
	}
	h.reuseMu.Unlock()
	for _, joiner := range f.joiners {
		if e != nil {
			h.serveReused(joiner, e)
			continue
		}
		h.mu.Lock()
		if j := h.jobs[joiner]; j != nil && j.Finished == nil && j.State != "CANCELLED" {
			j.State = "QUEUED"
			h.pending = append(h.pending, joiner)
		}
		h.mu.Unlock()
	}
	h.signal()
}
