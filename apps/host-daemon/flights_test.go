package host

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
)

func flightFixture(t *testing.T) (*Host, protocol.JobSpec, *protocol.Manifest) {
	h, spec, _ := reuseFixture(t)
	os.WriteFile(filepath.Join(spec.Workspace, "a.test.ts"), []byte("test('a', () => {})\n"), 0o600)
	m, err := h.manifestWithin(t.Context(), spec, -1)
	if err != nil {
		t.Fatal(err)
	}
	h.nodes["worker-a"] = &protocol.WorkerNode{ID: "worker-a", State: "READY"}
	return h, spec, m
}

func TestIdenticalCommandsJoinTheRunInProgress(t *testing.T) {
	h, spec, m := flightFixture(t)
	first, err := h.Submit(spec)
	if err != nil {
		t.Fatal(err)
	}
	h.startFlight(first.ID, spec, m, "worker-a")
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
	h.landFlight(first.ID)
	if j := h.jobs[joined.ID]; j.State != "COMPLETED" || j.Attempts[len(j.Attempts)-1].Target != "REUSED" {
		t.Fatal("the joined job gets the run's result", j.State)
	}

	other := spec
	other.Argv = []string{"vitest", "run", "src/b.test.ts"}
	lost, _ := h.Submit(other)
	h.startFlight(lost.ID, other, m, "worker-a")
	waiting, _ := h.Submit(other)
	h.landFlight(lost.ID) // cancelled or failed: no stored result
	h.mu.RLock()
	requeued := slices.Contains(h.pending, waiting.ID) && h.jobs[waiting.ID].State == "QUEUED"
	h.mu.RUnlock()
	if !requeued {
		t.Fatal("when the joined run leaves no result, the job runs itself")
	}
}

func TestAnUnjoinedFailureAnswersTheNextIdenticalRunOnce(t *testing.T) {
	h, spec, m := flightFixture(t)
	running, _ := h.Submit(spec)
	h.startFlight(running.ID, spec, m, "worker-a")
	ran(t, h, spec, m, 1, nil, time.Now())
	h.landFlight(running.ID) // nobody joined it
	if hit := h.reusedResult(spec, m, true); hit == nil || hit.ExitCode != 1 {
		t.Fatal("the failure answers the agent's next identical run")
	}
	if h.reusedResult(spec, m, true) != nil {
		t.Fatal("a failure is reused once")
	}
}

func TestDependencyQuestionsGoToTheTreeJobsUse(t *testing.T) {
	h, spec, m := flightFixture(t)
	if _, key, _ := h.treeFor(spec.Workspace, m); h.treeKeyFor(spec.Workspace) != key {
		t.Fatal("a project's own tree")
	}
	h.mu.Lock()
	h.outside[outsideKey(spec.Workspace)] = []string{"../planning"}
	h.mu.Unlock()
	if _, key, _ := h.treeFor(spec.Workspace, m); h.treeKeyFor(spec.Workspace) != key || key == treeKey(spec.Workspace) {
		t.Fatal("a project that reads files beside it is asked about in its nested tree, not the flat one left behind", key)
	}
}

func TestPreRunRecordsAndTimingsAreRemoved(t *testing.T) {
	dir := t.TempDir()
	finished := time.Date(2026, 10, 4, 15, 24, 12, 235000000, time.Local)
	code := 1
	prerun := "b" + string(slices.Repeat([]byte("1"), 47))
	agent := "a" + string(slices.Repeat([]byte("2"), 47))
	os.MkdirAll(filepath.Join(dir, "jobs"), 0o700)
	os.MkdirAll(filepath.Join(dir, "results", prerun, "a1"), 0o700)
	os.WriteFile(filepath.Join(dir, "jobs", prerun+".json"), []byte(`{"id":"`+prerun+`","state":"FAILED","exit_code":1,"finished":"`+finished.Format(time.RFC3339Nano)+`","attempts":[{"id":"a1","target":"worker-a"}],"spec":{"argv":["vitest","run"],"speculative":true}}`), 0o600)
	config.SaveJSON(filepath.Join(dir, "jobs", agent+".json"), protocol.Job{ID: agent, State: "FAILED", ExitCode: &code, Finished: &finished, Spec: protocol.JobSpec{Argv: []string{"vitest", "run"}}})
	config.SaveJSON(filepath.Join(dir, "history.json"), []protocol.HistorySample{
		{Signature: "s", Target: "worker-a", Timestamp: finished},
		{Signature: "s", Target: "LOCAL", Timestamp: finished, Success: true},
	})
	h, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if h.jobs[prerun] != nil || h.jobs[agent] == nil {
		t.Fatal("pre-run records go; the agent's own jobs stay")
	}
	if _, err := os.Stat(filepath.Join(dir, "results", prerun)); !os.IsNotExist(err) {
		t.Fatal("a pre-run's logs go with it")
	}
	if len(h.history) != 1 || h.history[0].Target != "LOCAL" {
		t.Fatal("a pre-run's phone timing no longer counts against the phone", h.history)
	}
}
