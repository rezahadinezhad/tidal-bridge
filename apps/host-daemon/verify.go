package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
	"tidalbridge/packages/scheduler"
	adapter "tidalbridge/packages/task-adapter"
	classifier "tidalbridge/packages/task-classifier"
	"time"
)

// Phone results are trusted through verification: a failed worker run of a
// detected command, or of a task-file test, check or replay-safe command, is rerun locally
// until both sides have agreed twice, and a disagreement keeps the command
// local for a week.
const (
	verificationFile = "verification.json"
	quarantineFor    = 7 * 24 * time.Hour
	trustedAfter     = 2
)

type verification struct {
	Quarantine map[string]quarantined `json:"quarantine"`
	Agreements map[string]int         `json:"agreements"`
}

type quarantined struct {
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

func detected(spec protocol.JobSpec) bool { return strings.HasPrefix(spec.Profile, "detected:") }

// verifiable: commands whose phone failures are checked here until trusted:
// detected commands, and task-file commands that are replay-safe or run the
// test runners and read-only checks that detection verifies too. Other
// task-file commands may have side effects and are not repeated.
func verifiable(spec protocol.JobSpec) bool {
	if detected(spec) {
		return true
	}
	return strings.HasPrefix(spec.Profile, "automatic:") && !spec.Service && (spec.Policy.Idempotent || checksOrTests(spec))
}

// checksOrTests: a test runner or a read-only check, run directly or as the
// project's npm script.
func checksOrTests(spec protocol.JobSpec) bool {
	argv := spec.Argv
	if len(argv) >= 2 && argv[0] == "npm" && (argv[1] == "test" || argv[1] == "run" && len(argv) >= 3) {
		name := "test"
		if argv[1] == "run" {
			name = argv[2]
		}
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		b, err := os.ReadFile(filepath.Join(spec.Workspace, "package.json"))
		if err != nil || json.Unmarshal(b, &pkg) != nil {
			return false
		}
		argv = strings.Fields(pkg.Scripts[name])
	}
	if kind := adapter.ScriptKind(strings.Join(argv, " ")); kind == "check" || kind == "test" {
		return true
	}
	if len(argv) > 0 && argv[0] == "pytest" {
		return true
	}
	return len(argv) >= 3 && argv[0] == "python" && (argv[1] == "-m" && (argv[2] == "pytest" || argv[2] == "unittest" || argv[2] == "mypy") || argv[1] == "manage.py" && argv[2] == "test")
}

// testNameFilters take the names of the tests to run, which say no more
// about the runner than the file names do.
var testNameFilters = map[string]bool{"-t": true, "--testNamePattern": true, "-k": true, "--grep": true}

// trustKey identifies a command for trust and quarantine: its project, tool
// versions and options, without the files, folders or test names it selects,
// so checking one test vouches for the runner rather than for that test alone.
func trustKey(spec protocol.JobSpec) string {
	classifier.Normalize(&spec)
	argv := append([]string(nil), spec.Argv[:min(1, len(spec.Argv))]...)
	filterValue := false
	for _, arg := range spec.Argv[len(argv):] {
		name, _, _ := strings.Cut(arg, "=")
		switch {
		case filterValue:
			filterValue = false
		case testNameFilters[name]:
			argv = append(argv, name)
			filterValue = name == arg
		case strings.HasPrefix(arg, "-") || !strings.ContainsAny(arg, `/\.`):
			argv = append(argv, arg)
		}
	}
	spec.Argv = argv
	return scheduler.Signature(spec)
}

// verifySignature matches the scheduler's signature of the normalized spec
// that Explain routes, whichever form the caller holds.
func verifySignature(spec protocol.JobSpec) string {
	classifier.Normalize(&spec)
	return scheduler.Signature(spec)
}

func (h *Host) loadVerification() {
	h.verify = verification{Quarantine: map[string]quarantined{}, Agreements: map[string]int{}}
	b, _ := os.ReadFile(filepath.Join(h.Dir, verificationFile))
	json.Unmarshal(b, &h.verify)
	if h.verify.Quarantine == nil {
		h.verify.Quarantine = map[string]quarantined{}
	}
	if h.verify.Agreements == nil {
		h.verify.Agreements = map[string]int{}
	}
}

// quarantineLocked needs h.mu held.
func (h *Host) quarantineLocked(sig string) string {
	if q, ok := h.verify.Quarantine[sig]; ok && time.Now().Before(q.Until) {
		return q.Reason
	}
	return ""
}

func (h *Host) needsConfirmation(spec protocol.JobSpec) bool {
	if !verifiable(spec) || spec.Policy.ForceRemote {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.verify.Agreements[trustKey(spec)] < trustedAfter
}

func (h *Host) RecordConfirmation(input protocol.Confirmation) error {
	if err := classifier.Normalize(&input.Spec); err != nil {
		return err
	}
	if !verifiable(input.Spec) || input.RemoteExitCode == 0 {
		return fmt.Errorf("confirmations compare a failed run of a verifiable command")
	}
	sig := trustKey(input.Spec)
	h.mu.Lock()
	defer h.mu.Unlock()
	if input.LocalExitCode == 0 {
		// Only this exact command stays here; the runner is checked again.
		h.verify.Quarantine[verifySignature(input.Spec)] = quarantined{Until: time.Now().Add(quarantineFor),
			Reason: fmt.Sprintf("Failed on the phone (exit %d) but passed on this laptop; kept local for a week.", input.RemoteExitCode)}
		delete(h.verify.Agreements, sig)
	} else {
		h.verify.Agreements[sig]++
	}
	for key, q := range h.verify.Quarantine {
		if time.Now().After(q.Until) {
			delete(h.verify.Quarantine, key)
		}
	}
	if len(h.verify.Agreements) > 2048 {
		h.verify.Agreements = map[string]int{sig: h.verify.Agreements[sig]}
	}
	h.log.Info("confirmation", "profile", input.Spec.Profile, "workspace", input.Spec.Workspace, "remote_exit", input.RemoteExitCode, "local_exit", input.LocalExitCode)
	return config.SaveJSON(filepath.Join(h.Dir, verificationFile), h.verify)
}
