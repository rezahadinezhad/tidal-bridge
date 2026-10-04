package host

import (
	"context"
	"slices"
	"strings"
	"time"

	"tidalbridge/packages/protocol"
	"tidalbridge/packages/scheduler"
	workspacesync "tidalbridge/packages/workspace-sync"
)

// Pre-runs answer an agent's command before it asks. When files in a project
// change and then stay quiet for a moment, the tests and checks agents ran
// there in the last half hour run on an idle phone. When the agent then runs
// one, its result is already waiting (results.go), or it joins the run in
// progress instead of starting another.
//
// Pre-runs use only a phone that is idle, charging, cool and not in
// someone's hand, one at a time, and never the laptop. They skip commands
// that took over three minutes there or reached laptop services (such a
// result is never reused). Real work always comes first: it cancels pre-runs
// in its way, and a newer edit cancels pre-runs of the old files.
const (
	prerunQuiet    = 2 * time.Second
	prerunRecent   = 30 * time.Minute
	prerunLongest  = 3 * time.Minute
	prerunCommands = 6
)

type recentCommand struct {
	spec     protocol.JobSpec
	used     time.Time
	services bool // its last phone run reached a laptop service
	// One finished pre-run per agent run: answered stays set until the agent
	// runs the command again (noteCommand makes a fresh entry). started
	// spaces pre-runs that edits keep cancelling.
	answered bool
	started  time.Time
}

// prerunGap spaces pre-runs of one command while edits keep coming.
const prerunGap = 15 * time.Second

type projectWatch struct {
	commands    []*recentCommand // most recent first
	unsubscribe func()
	timer       *time.Timer
}

// flight is a reusable job running on a phone, which identical commands join.
type flight struct {
	key, watch, node, scope string
	speculative             bool
	joiners                 []string
}

func watchKey(spec protocol.JobSpec) string {
	return strings.ToLower(spec.Workspace) + map[bool]string{true: "|env", false: ""}[spec.Policy.SyncEnvFiles]
}

// reuseKeyFor is the reuse key of spec on the workspace's current files.
func (h *Host) reuseKeyFor(spec protocol.JobSpec, m *protocol.Manifest) string {
	tree, _, _ := h.treeFor(spec.Workspace, m)
	return reuseKey(spec, treeIdentity(tree))
}

// noteCommand remembers a test or check an agent ran, and watches its
// project for edits.
func (h *Host) noteCommand(spec protocol.JobSpec, services bool) {
	if spec.Speculative || !h.reusable(spec) {
		return
	}
	ws, scope := watchKey(spec), reuseScope(spec)
	h.prerunMu.Lock()
	w := h.watches[ws]
	subscribe := w == nil
	if subscribe {
		w = &projectWatch{}
		h.watches[ws] = w
	}
	w.commands = slices.DeleteFunc(w.commands, func(c *recentCommand) bool { return reuseScope(c.spec) == scope })
	w.commands = append([]*recentCommand{{spec: spec, used: time.Now(), services: services}}, w.commands...)
	w.commands = w.commands[:min(len(w.commands), prerunCommands)]
	h.prerunMu.Unlock()
	if !subscribe {
		return
	}
	go func() {
		idx, err := h.workspaceIndex(context.Background(), spec.Workspace, spec.Policy.SyncEnvFiles, -1)
		h.prerunMu.Lock()
		defer h.prerunMu.Unlock()
		if err != nil || h.watches[ws] != w {
			if h.watches[ws] == w {
				delete(h.watches, ws)
			}
			return
		}
		w.unsubscribe = idx.Subscribe(func([]workspacesync.Change) { h.workspaceChanged(ws) })
	}()
}

// workspaceChanged waits for edits to settle, and drops pre-runs of the
// files as they were (unless a real command joined one).
func (h *Host) workspaceChanged(ws string) {
	h.prerunMu.Lock()
	defer h.prerunMu.Unlock()
	w := h.watches[ws]
	if w == nil {
		return
	}
	h.log.Debug("prerun_edit", "workspace", ws)
	for id, f := range h.flights {
		if f.speculative && f.watch == ws && len(f.joiners) == 0 {
			go h.cancelBecause(id, "Pre-run stopped: the files changed again.")
		}
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(prerunQuiet, func() { h.prerun(ws) })
}

// prerun starts the most recent eligible command of a project on an idle
// phone, unless its answer is already known or being computed.
func (h *Host) prerun(ws string) {
	h.prerunMu.Lock()
	w := h.watches[ws]
	if w == nil {
		h.prerunMu.Unlock()
		return
	}
	now := time.Now()
	w.commands = slices.DeleteFunc(w.commands, func(c *recentCommand) bool { return now.Sub(c.used) > prerunRecent })
	if len(w.commands) == 0 {
		if w.unsubscribe != nil {
			w.unsubscribe()
		}
		delete(h.watches, ws)
		h.prerunMu.Unlock()
		return
	}
	busy := false
	for _, f := range h.flights {
		busy = busy || f.speculative
	}
	candidates := slices.Clone(w.commands)
	h.prerunMu.Unlock()
	if busy || h.Config().Paused {
		h.log.Debug("prerun_skip", "workspace", ws, "busy", busy)
		return
	}
	node := h.idlePhone()
	if node == "" {
		h.log.Debug("prerun_skip", "workspace", ws, "reason", "no idle, cool, charging phone")
		return
	}
	for _, c := range candidates {
		h.prerunMu.Lock()
		spent := c.answered || now.Sub(c.started) < prerunGap
		h.prerunMu.Unlock()
		if spent {
			continue
		}
		if c.services || h.phoneTakes(c.spec) > prerunLongest {
			h.log.Debug("prerun_skip", "argv", c.spec.Argv, "reason", "long or uses laptop services")
			continue
		}
		m, err := h.manifestWithin(context.Background(), c.spec, 0)
		if err != nil || h.reusedResult(c.spec, m, false) != nil {
			h.log.Debug("prerun_skip", "argv", c.spec.Argv, "reason", "answered or indexing")
			continue
		}
		key := h.reuseKeyFor(c.spec, m)
		h.prerunMu.Lock()
		running := h.flightFor(key) != ""
		h.prerunMu.Unlock()
		if running {
			continue
		}
		spec := c.spec
		spec.Speculative = true
		spec.Policy.ForceRemote, spec.Policy.ForceLocal, spec.Policy.DeviceID = true, false, node
		h.prerunMu.Lock()
		c.started = now
		h.prerunMu.Unlock()
		if _, err := h.Submit(spec); err != nil {
			h.log.Error("prerun_submit", "error", err.Error())
		} else {
			h.log.Info("prerun", "workspace", spec.Workspace, "argv", spec.Argv)
		}
		return // one at a time; the next follows when this one ends
	}
}

// idlePhone is a phone fit for a pre-run: ready and idle, charging, cool and
// not in use. Unknown readings count against it.
func (h *Host) idlePhone() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for id, n := range h.nodes {
		r := n.Profile.Capabilities.Resources
		cooling := n.CoolingUntil != nil && time.Now().Before(*n.CoolingUntil)
		if n.State == "READY" && n.ActiveJobs == 0 && !cooling && r.Charging != nil && *r.Charging && r.InUse != nil && !*r.InUse && r.Thermal == "nominal" {
			return id
		}
	}
	return ""
}

// phoneTakes is how long the command usually runs on a phone (0 if unknown).
func (h *Host) phoneTakes(spec protocol.JobSpec) time.Duration {
	signature := scheduler.Signature(spec)
	h.mu.RLock()
	var runs []float64
	for _, s := range h.history {
		if s.Signature == signature && strings.HasPrefix(s.Target, "worker-") && s.Success {
			runs = append(runs, s.DurationMS)
		}
	}
	h.mu.RUnlock()
	if len(runs) == 0 {
		return 0
	}
	slices.Sort(runs)
	return time.Duration(runs[len(runs)/2]) * time.Millisecond
}

// flightFor is the job running the command with this reuse key, if any
// (guarded by prerunMu).
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
	h.prerunMu.Lock()
	h.flights[id] = &flight{key: key, watch: watchKey(spec), node: node, scope: reuseScope(spec), speculative: spec.Speculative}
	h.prerunMu.Unlock()
}

// join attaches a job to an identical run in progress; false if none.
func (h *Host) join(id, key string) bool {
	h.prerunMu.Lock()
	defer h.prerunMu.Unlock()
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
	h.mu.RLock()
	finished := h.jobs[id] != nil && h.jobs[id].ExitCode != nil
	h.mu.RUnlock()
	h.prerunMu.Lock()
	f := h.flights[id]
	delete(h.flights, id)
	if f != nil && f.speculative && finished {
		// Answered for this round: the next pre-run waits for the agent to
		// run the command again (stopped pre-runs computed nothing).
		if w := h.watches[f.watch]; w != nil {
			for _, c := range w.commands {
				if reuseScope(c.spec) == f.scope {
					c.answered = true
				}
			}
		}
	}
	h.prerunMu.Unlock()
	if f == nil {
		return
	}
	h.reuseMu.Lock()
	var e *reuseEntry
	if stored := h.reuse[f.key]; stored != nil {
		found := *stored
		e = &found
		if e.ExitCode != 0 && len(f.joiners) > 0 {
			// The joiners were its one reuse. Unjoined, it stays: it answers
			// the agent's next identical run, and no pre-run repeats it.
			h.dropReuseLocked(stored)
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
	// The phone is free again: answer what the latest edits call for (the
	// next command for these files, or edits made while this one ran).
	go h.prerun(f.watch)
}

// preemptOn cancels pre-runs on a phone that real work needs.
func (h *Host) preemptOn(node string) {
	h.prerunMu.Lock()
	var victims []string
	for id, f := range h.flights {
		if f.speculative && f.node == node && len(f.joiners) == 0 {
			victims = append(victims, id)
		}
	}
	h.prerunMu.Unlock()
	for _, id := range victims {
		h.cancelBecause(id, "Pre-run stopped: real work needed the phone.")
	}
}

// cancelBecause stops a job, recording why.
func (h *Host) cancelBecause(id, reason string) {
	h.mu.Lock()
	if j := h.jobs[id]; j != nil && j.Finished == nil {
		j.Error = reason
	}
	h.mu.Unlock()
	h.Cancel(id)
}

// speculativeOn counts the pre-runs holding each phone, which routing treats
// as free capacity.
func (h *Host) speculativeOn() map[string]int {
	h.prerunMu.Lock()
	defer h.prerunMu.Unlock()
	counts := map[string]int{}
	for _, f := range h.flights {
		if f.speculative && len(f.joiners) == 0 {
			counts[f.node]++
		}
	}
	return counts
}
