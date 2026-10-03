package host

import (
	"testing"
	"time"

	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
	"tidalbridge/packages/scheduler"
)

// A task-file command's phone failures are checked here too, and trust in a
// runner covers the files it is pointed at.
func TestTaskFileFailuresAreCheckedPerRunner(t *testing.T) {
	dir := t.TempDir()
	h, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	spec := func(file string) protocol.JobSpec {
		return protocol.JobSpec{Argv: []string{"vitest", "run", file}, Workspace: dir, Profile: "automatic:vitest", Runtime: "debian", Policy: protocol.Policy{Idempotent: true}}
	}
	if !h.needsConfirmation(spec("src/a.test.ts")) {
		t.Fatal("a task-file command's first phone failures are checked")
	}
	for _, file := range []string{"src/a.test.ts", "src/b.test.ts"} {
		if err := h.RecordConfirmation(protocol.Confirmation{Spec: spec(file), RemoteExitCode: 1, LocalExitCode: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if h.needsConfirmation(spec("src/c.test.ts")) {
		t.Fatal("two agreements on any files trust the runner")
	}
	other := spec("src/c.test.ts")
	other.Argv = append(other.Argv, "--reporter=verbose")
	service := spec("src")
	service.Service = true
	if !h.needsConfirmation(other) || h.needsConfirmation(service) {
		t.Fatal("other options are another command; services are never rerun")
	}
}

func TestSplitNeedsAVerifiedSuiteSize(t *testing.T) {
	dir := t.TempDir()
	h, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	spec := protocol.JobSpec{Argv: []string{"npm", "test"}, Workspace: dir, Profile: "detected:test", Runtime: "debian", Shard: "vitest"}
	in := scheduler.Input{Spec: spec, Config: config.Default(), Host: protocol.Resources{CPUPercent: 10, RAMAvailableMB: 8000}}
	candidates := []protocol.Candidate{{Target: "worker-a", TotalCostMS: 90000}}
	try := func() protocol.Decision {
		d := protocol.Decision{Target: "worker-a", Candidates: candidates}
		h.split(in, &d)
		return d
	}
	if try().Split != 0 {
		t.Fatal("no known suite size, no split")
	}
	if err := h.RecordSuite(protocol.SuiteObservation{Spec: spec, Files: 120}); err != nil {
		t.Fatal(err)
	}
	if d := try(); d.Split != 2 || d.SplitFiles != 120 {
		t.Fatal("a known size and room here: split", d)
	}
	// New test files: the whole run after the split matched its parts.
	if err := h.RecordSuite(protocol.SuiteObservation{Spec: spec, Files: 122, SplitSum: 122}); err != nil {
		t.Fatal(err)
	}
	if d := try(); d.Split != 2 || d.SplitFiles != 122 {
		t.Fatal("the size follows the suite", d)
	}
	// Parts that covered 118 of the whole run's 122 files turn splitting off.
	if err := h.RecordSuite(protocol.SuiteObservation{Spec: spec, Files: 122, SplitSum: 118}); err != nil {
		t.Fatal(err)
	}
	if try().Split != 0 {
		t.Fatal("splitting stays off after its parts missed files")
	}
}

func TestDetectedLocalRunsAreMeasured(t *testing.T) {
	dir := t.TempDir()
	h, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	peak := 800.0
	spec := protocol.JobSpec{Argv: []string{"npm", "run", "typecheck"}, Workspace: dir, Profile: "detected:typecheck", Runtime: "debian"}
	if _, err := h.RecordLocal(protocol.LocalObservation{Spec: spec, Started: time.Now().Add(-time.Minute), DurationMS: 20000, CPUSeconds: 30, PeakRAMMB: &peak}); err != nil {
		t.Fatal("a detected command's laptop run is measured", err)
	}
	if c := h.costs[verifySignature(spec)]; c == nil || c.CPUSeconds != 30 || c.RAMMB != 800 {
		t.Fatal("and its cost kept", c)
	}
}
