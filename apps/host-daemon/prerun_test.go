package host

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"tidalbridge/packages/protocol"
	"tidalbridge/packages/scheduler"
)

func prerunFixture(t *testing.T) (*Host, protocol.JobSpec, *protocol.Manifest) {
	h, spec, _ := reuseFixture(t)
	os.WriteFile(filepath.Join(spec.Workspace, "a.test.ts"), []byte("test('a', () => {})\n"), 0o600)
	m, err := h.manifestWithin(t.Context(), spec, -1)
	if err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	h.nodes["worker-a"] = &protocol.WorkerNode{ID: "worker-a", State: "READY",
		Profile: protocol.DeviceProfile{Capabilities: protocol.CapabilitySet{Resources: protocol.Resources{Charging: &yes, InUse: &no, Thermal: "nominal"}}}}
	return h, spec, m
}

func preruns(h *Host) []*protocol.Job {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []*protocol.Job
	for _, j := range h.jobs {
		if j.Spec.Speculative {
			out = append(out, j)
		}
	}
	return out
}

func TestPreRunsStartOnlyOnAnIdleCoolChargingPhone(t *testing.T) {
	h, spec, _ := prerunFixture(t)
	h.noteCommand(spec, false)
	h.prerun(watchKey(spec))
	jobs := preruns(h)
	if len(jobs) != 1 || !jobs[0].Spec.Policy.ForceRemote || jobs[0].Spec.Policy.DeviceID != "worker-a" {
		t.Fatal("a recent command is pre-run on the idle phone, never on the laptop", jobs)
	}
	resources := &h.nodes["worker-a"].Profile.Capabilities.Resources
	yes, no := true, false
	for name, unfit := range map[string]func(){
		"in your hand": func() { resources.InUse = &yes },
		"warm":         func() { resources.Thermal = "warm" },
		"on battery":   func() { resources.Charging = &no },
		"unknown":      func() { resources.Charging = nil },
		"busy":         func() { h.nodes["worker-a"].ActiveJobs = 1 },
	} {
		h2, spec2, _ := prerunFixture(t)
		h, resources = h2, &h2.nodes["worker-a"].Profile.Capabilities.Resources
		unfit()
		h2.noteCommand(spec2, false)
		h2.prerun(watchKey(spec2))
		if len(preruns(h2)) != 0 {
			t.Fatal("no pre-run on a phone that is", name)
		}
	}
}

func TestPreRunsSkipLongRunsServicesAndAnsweredCommands(t *testing.T) {
	h, spec, m := prerunFixture(t)
	h.history = append(h.history, protocol.HistorySample{Signature: scheduler.Signature(spec), Target: "worker-a", DurationMS: 300000, Success: true})
	h.noteCommand(spec, false)
	h.prerun(watchKey(spec))
	if len(preruns(h)) != 0 {
		t.Fatal("a command that took five minutes on the phone is not pre-run")
	}
	h.history = nil
	h.noteCommand(spec, true)
	h.prerun(watchKey(spec))
	if len(preruns(h)) != 0 {
		t.Fatal("a command that reached laptop services is not pre-run: its result is never reused")
	}
	h.noteCommand(spec, false)
	ran(t, h, spec, m, 0, nil, time.Now())
	h.prerun(watchKey(spec))
	if len(preruns(h)) != 0 {
		t.Fatal("a command whose answer is already stored is not run again")
	}
}

func TestIdenticalCommandsJoinTheRunInProgress(t *testing.T) {
	h, spec, m := prerunFixture(t)
	speculative := spec
	speculative.Speculative = true
	pre, err := h.Submit(speculative)
	if err != nil {
		t.Fatal(err)
	}
	h.startFlight(pre.ID, speculative, m, "worker-a")
	if d, _ := h.Explain(t.Context(), spec); d.Target != "worker-a" || d.Explanation != "Joining the identical run already in progress." {
		t.Fatal("the adapter is told to join, not to run it again on the laptop", d)
	}
	joined, err := h.Submit(spec)
	if err != nil {
		t.Fatal(err)
	}
	h.mu.RLock()
	queued := slices.Contains(h.pending, joined.ID)
	h.mu.RUnlock()
	if queued || h.jobs[joined.ID].State != "RUNNING" {
		t.Fatal("an identical command joins the run instead of starting another")
	}
	ran(t, h, spec, m, 0, nil, time.Now())
	h.landFlight(pre.ID)
	if j := h.jobs[joined.ID]; j.State != "COMPLETED" || j.Attempts[len(j.Attempts)-1].Target != "REUSED" {
		t.Fatal("the joined job gets the run's result", j.State)
	}

	other := spec
	other.Argv = []string{"vitest", "run", "src/b.test.ts"}
	other.Speculative = true
	lost, _ := h.Submit(other)
	h.startFlight(lost.ID, other, m, "worker-a")
	other.Speculative = false
	waiting, _ := h.Submit(other)
	h.landFlight(lost.ID) // cancelled or failed: no stored result
	h.mu.RLock()
	requeued := slices.Contains(h.pending, waiting.ID) && h.jobs[waiting.ID].State == "QUEUED"
	h.mu.RUnlock()
	if !requeued {
		t.Fatal("when the joined run leaves no result, the job runs itself")
	}
}

func TestRealWorkPreemptsPreRunsAndEditsCancelThem(t *testing.T) {
	h, spec, m := prerunFixture(t)
	speculative := spec
	speculative.Speculative = true
	pre, _ := h.Submit(speculative)
	h.startFlight(pre.ID, speculative, m, "worker-a")
	h.nodes["worker-a"].ActiveJobs, h.nodes["worker-a"].State = 1, "BUSY"
	in := h.input(spec, nil)
	if in.Nodes[0].ActiveJobs != 0 || in.Nodes[0].State != "READY" {
		t.Fatal("routing sees a phone held only by a pre-run as free", in.Nodes[0])
	}
	h.preemptOn("worker-a")
	if h.jobs[pre.ID].State != "CANCELLED" {
		t.Fatal("real work on the phone cancels its pre-run")
	}

	again, _ := h.Submit(speculative)
	h.startFlight(again.ID, speculative, m, "worker-a")
	h.noteCommand(spec, false)
	h.workspaceChanged(watchKey(spec))
	deadline := time.Now().Add(2 * time.Second)
	for h.Job(again.ID); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if j, _ := h.Job(again.ID); j.State == "CANCELLED" {
			return
		}
	}
	t.Fatal("a newer edit cancels a pre-run of the old files")
}

func TestAnEditStartsAPreRunAfterAQuietMoment(t *testing.T) {
	h, spec, _ := prerunFixture(t)
	h.noteCommand(spec, false)
	deadline := time.Now().Add(5 * time.Second)
	for watching := false; !watching; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the project is watched once a command ran there")
		}
		h.prerunMu.Lock()
		watching = h.watches[watchKey(spec)] != nil && h.watches[watchKey(spec)].unsubscribe != nil
		h.prerunMu.Unlock()
	}
	os.WriteFile(filepath.Join(spec.Workspace, "a.test.ts"), []byte("test('a', () => { expect(1).toBe(1) })\n"), 0o600)
	for deadline = time.Now().Add(prerunQuiet + 5*time.Second); len(preruns(h)) == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("an edit followed by a quiet moment starts a pre-run")
		}
	}
}

func TestEditsMadeWhileTheyRanArePreRunWhenTheRunEnds(t *testing.T) {
	h, spec, m := prerunFixture(t)
	running, _ := h.Submit(spec)
	h.startFlight(running.ID, spec, m, "worker-a")
	h.noteCommand(spec, false)
	ran(t, h, spec, m, 0, nil, time.Now()) // the run ended with a result for the old files
	os.WriteFile(filepath.Join(spec.Workspace, "a.test.ts"), []byte("test('edited', () => {})\n"), 0o600)
	h.manifestWithin(t.Context(), spec, -1)
	time.Sleep(300 * time.Millisecond) // the index has the edit; no subscriber saw it
	h.landFlight(running.ID)
	for deadline := time.Now().Add(3 * time.Second); len(preruns(h)) == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("when a run ends, edits made meanwhile are pre-run")
		}
	}
}
