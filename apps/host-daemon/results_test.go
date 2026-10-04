package host

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"tidalbridge/packages/protocol"
	classifier "tidalbridge/packages/task-classifier"
)

func reuseFixture(t *testing.T) (*Host, protocol.JobSpec, *protocol.Manifest) {
	dir := t.TempDir()
	h, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	workspace := filepath.Join(dir, "project")
	os.MkdirAll(workspace, 0o700)
	spec := protocol.JobSpec{Argv: []string{"vitest", "run", "src/a.test.ts"}, Workspace: workspace, Profile: "detected:vitest", Runtime: "debian"}
	if err := classifier.Normalize(&spec); err != nil { // as Submit and Explain see it
		t.Fatal(err)
	}
	m := &protocol.Manifest{ID: "m1", Files: []protocol.FileEntry{{Path: "src/a.ts", Hash: "h1", Size: 1}, {Path: "src/a.test.ts", Hash: "h2", Size: 1}}}
	return h, spec, m
}

// ran records a finished phone run of spec on tree m, with this output.
func ran(t *testing.T, h *Host, spec protocol.JobSpec, m *protocol.Manifest, code int, used *bool, finished time.Time) string {
	id := "job-" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	a := protocol.Attempt{ID: "a1", Target: "worker-a", Tree: treeIdentity(m), ServicesUsed: used, DurationMS: 40000}
	dir := filepath.Join(h.Dir, "results", id, a.ID)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(" Test Files  1 passed (1)\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "stderr.log"), []byte("warning\n"), 0o600)
	h.rememberResult(id, spec, a, code, "fp-1", finished)
	return id
}

func TestResultsAreReusedOnlyForTheSameCommandAndFiles(t *testing.T) {
	h, spec, m := reuseFixture(t)
	if h.reusedResult(spec, m, false) != nil {
		t.Fatal("nothing has run yet")
	}
	ran(t, h, spec, m, 0, nil, time.Now())
	hit := h.reusedResult(spec, m, true)
	if hit == nil || hit.ExitCode != 0 || hit.Target != "worker-a" || !strings.Contains(hit.Stdout, "1 passed") {
		t.Fatal("an unchanged pass is reused", hit)
	}
	if h.reusedResult(spec, m, true) == nil {
		t.Fatal("a pass is reused again")
	}
	edited := *m
	edited.Files = []protocol.FileEntry{{Path: "src/a.ts", Hash: "h1-edited", Size: 1}, {Path: "src/a.test.ts", Hash: "h2", Size: 1}}
	if h.reusedResult(spec, &edited, false) != nil {
		t.Fatal("a changed file means a fresh run")
	}
	other := spec
	other.Argv = []string{"vitest", "run", "src/b.test.ts"}
	if h.reusedResult(other, m, false) != nil {
		t.Fatal("another command means a fresh run")
	}
	env := spec
	env.Env = map[string]string{"TZ": "UTC"}
	if h.reusedResult(env, m, false) != nil {
		t.Fatal("another environment means a fresh run")
	}
}

func TestAFailureIsReusedOnce(t *testing.T) {
	h, spec, m := reuseFixture(t)
	ran(t, h, spec, m, 1, nil, time.Now())
	if hit := h.reusedResult(spec, m, false); hit == nil || hit.ExitCode != 1 {
		t.Fatal("a preview sees the failure without using it up")
	}
	if h.reusedResult(spec, m, true) == nil {
		t.Fatal("the failure is reused once")
	}
	if h.reusedResult(spec, m, true) != nil {
		t.Fatal("a retry of a failure runs fresh, for flaky tests")
	}
}

func TestRunsThatReachedLaptopServicesAreNeverReused(t *testing.T) {
	h, spec, m := reuseFixture(t)
	spec.ReversePorts = []int{8000}
	yes, no := true, false
	ran(t, h, spec, m, 0, &yes, time.Now())
	if h.reusedResult(spec, m, false) != nil {
		t.Fatal("a run that used the laptop's API depends on more than the files")
	}
	ran(t, h, spec, m, 0, nil, time.Now())
	if h.reusedResult(spec, m, false) != nil {
		t.Fatal("when the phone could not tell, nothing is reused")
	}
	ran(t, h, spec, m, 0, &no, time.Now())
	if h.reusedResult(spec, m, false) == nil {
		t.Fatal("a run that never touched the tunnel is reused")
	}
}

func TestOnlyReadOnlyTestsAndChecksAreReused(t *testing.T) {
	h, spec, m := reuseFixture(t)
	for name, change := range map[string]func(*protocol.JobSpec){
		"write-back":  func(s *protocol.JobSpec) { s.Policy.WriteBack = true },
		"dev server":  func(s *protocol.JobSpec) { s.Service = true },
		"task script": func(s *protocol.JobSpec) { s.Profile = "automatic:deploy"; s.Argv = []string{"node", "deploy.js"} },
		"prepare":     func(s *protocol.JobSpec) { s.Profile = "prepare:x" },
	} {
		s := spec
		change(&s)
		ran(t, h, s, m, 0, nil, time.Now())
		if h.reusedResult(s, m, false) != nil {
			t.Fatal(name, "is never reused")
		}
	}
	h.cfg.NoReuse = true
	ran(t, h, spec, m, 0, nil, time.Now())
	if h.reusedResult(spec, m, false) != nil {
		t.Fatal("no_reuse turns reuse off")
	}
}

func TestReuseExpiresAndRespectsQuarantineAndRuntimeChanges(t *testing.T) {
	h, spec, m := reuseFixture(t)
	ran(t, h, spec, m, 0, nil, time.Now().Add(-7*time.Hour))
	if h.reusedResult(spec, m, false) != nil {
		t.Fatal("a pass older than six hours runs fresh")
	}
	midnight := time.Date(2026, 10, 4, 23, 59, 0, 0, time.Local)
	e := &reuseEntry{Finished: midnight}
	if e.fresh(midnight.Add(2 * time.Minute)) {
		t.Fatal("tests may read the date: nothing is reused across midnight")
	}
	ran(t, h, spec, m, 0, nil, time.Now())
	h.nodes["worker-a"] = &protocol.WorkerNode{ID: "worker-a", Profile: protocol.DeviceProfile{Calibration: protocol.Calibration{Fingerprint: "fp-2"}}}
	if h.reusedResult(spec, m, false) != nil {
		t.Fatal("a worker whose runtime changed runs fresh")
	}
	h.nodes["worker-a"].Profile.Calibration.Fingerprint = "fp-1"
	ran(t, h, spec, m, 0, nil, time.Now())
	h.verify.Quarantine[verifySignature(spec)] = quarantined{Until: time.Now().Add(time.Hour), Reason: "disagreed"}
	if h.reusedResult(spec, m, false) != nil {
		t.Fatal("a quarantined command is never reused")
	}
}

func TestReusedResultsSurviveARestartAndServeLikeARun(t *testing.T) {
	h, spec, m := reuseFixture(t)
	ran(t, h, spec, m, 0, nil, time.Now())
	h.Close()
	h2, err := New(h.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	hit := h2.reusedResult(spec, m, true)
	if hit == nil {
		t.Fatal("stored results survive a restart")
	}
	job, err := h2.Submit(spec)
	if err != nil {
		t.Fatal(err)
	}
	h2.serveReused(job.ID, hit)
	j := h2.jobs[job.ID]
	a := j.Attempts[len(j.Attempts)-1]
	stderr, _ := os.ReadFile(filepath.Join(h2.Dir, "results", job.ID, a.ID, "stderr.log"))
	stdout, _ := os.ReadFile(filepath.Join(h2.Dir, "results", job.ID, a.ID, "stdout.log"))
	if j.State != "COMPLETED" || *j.ExitCode != 0 || a.Target != "REUSED" || !strings.Contains(string(stdout), "1 passed") ||
		!strings.HasPrefix(string(stderr), "[Tidal Bridge] Nothing this command reads changed") || !strings.HasSuffix(string(stderr), "warning\n") {
		t.Fatal("a reused result is served with the same output and exit code", j.State, string(stderr))
	}
	if h2.reuseCount != 1 {
		t.Fatal("today's reuses are counted")
	}
}

func TestASubmittedReusableJobIsServedAtOnce(t *testing.T) {
	h, spec, _ := reuseFixture(t)
	os.WriteFile(filepath.Join(spec.Workspace, "a.test.ts"), []byte("test('a', () => {})\n"), 0o600)
	m, err := h.manifestWithin(t.Context(), spec, -1)
	if err != nil {
		t.Fatal(err)
	}
	ran(t, h, spec, m, 0, nil, time.Now())
	job, err := h.Submit(spec)
	if err != nil {
		t.Fatal(err)
	}
	h.mu.RLock()
	j, queued := h.jobs[job.ID], slices.Contains(h.pending, job.ID)
	h.mu.RUnlock()
	if queued || j.State != "COMPLETED" || j.Attempts[len(j.Attempts)-1].Target != "REUSED" {
		t.Fatal("a reusable job is served on submission, never queued", j.State, queued)
	}
}
