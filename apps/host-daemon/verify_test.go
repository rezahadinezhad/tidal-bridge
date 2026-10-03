package host

import (
	"os"
	"path/filepath"
	"testing"

	"tidalbridge/packages/protocol"
)

func TestConfirmationsBuildTrustOrQuarantine(t *testing.T) {
	dir := t.TempDir()
	h, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	spec := protocol.JobSpec{Argv: []string{"npm", "run", "typecheck"}, Workspace: dir, Profile: "detected:typecheck", Runtime: "debian"}
	if !h.needsConfirmation(spec) {
		t.Fatal("a new detected command needs confirmation")
	}
	explicit := spec
	explicit.Profile = "automatic:typecheck"
	if h.needsConfirmation(explicit) || h.RecordConfirmation(protocol.Confirmation{Spec: explicit, RemoteExitCode: 1, LocalExitCode: 1}) == nil {
		t.Fatal("a task-file command that is neither replay-safe nor a test or check is not repeated")
	}
	runner := explicit
	runner.Argv = []string{"vitest", "run", "src/a.test.ts"}
	if !h.needsConfirmation(runner) {
		t.Fatal("a task-file test runner is verified like a detected one")
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts": {"typecheck": "tsc --noEmit"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !h.needsConfirmation(explicit) {
		t.Fatal("a task-file npm script that runs a read-only check is verified")
	}
	for range 2 {
		if err := h.RecordConfirmation(protocol.Confirmation{Spec: spec, RemoteExitCode: 2, LocalExitCode: 2}); err != nil {
			t.Fatal(err)
		}
	}
	if h.needsConfirmation(spec) {
		t.Fatal("two agreeing failures make the phone's failures trusted")
	}
	other := spec
	other.Argv = []string{"npm", "test"}
	if err := h.RecordConfirmation(protocol.Confirmation{Spec: other, RemoteExitCode: 1, LocalExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if in := reloaded.input(other, nil); in.Quarantine == "" {
		t.Fatal("a disagreement quarantines the command across restarts")
	}
	if reloaded.needsConfirmation(spec) {
		t.Fatal("agreements survive restarts")
	}
}

func TestCriticalHeatEvacuatesRunningJobs(t *testing.T) {
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	h.jobs["on-phone"] = &protocol.Job{ID: "on-phone", State: "RUNNING", Attempts: []protocol.Attempt{{Target: "worker-a"}}}
	h.jobs["elsewhere"] = &protocol.Job{ID: "elsewhere", State: "RUNNING", Attempts: []protocol.Attempt{{Target: "LOCAL"}}}
	h.evacuate("worker-a")
	if j := h.jobs["on-phone"]; !j.Evacuated || j.Finished == nil || j.State != "CANCELLED" {
		t.Fatal("a critically hot phone's job must stop there and move", j)
	}
	if j := h.jobs["elsewhere"]; j.Evacuated || j.Finished != nil {
		t.Fatal("other jobs are untouched", j)
	}
}
