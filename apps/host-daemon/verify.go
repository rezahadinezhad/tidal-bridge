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
	classifier "tidalbridge/packages/task-classifier"
	"time"
)

// Phone results are trusted through verification: a failed worker run of a
// detected command, or of a replay-safe task-file command, is rerun locally
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

// verifiable: commands whose phone failures are checked here until trusted.
func verifiable(spec protocol.JobSpec) bool {
	return detected(spec) || strings.HasPrefix(spec.Profile, "automatic:") && spec.Policy.Idempotent && !spec.Service
}

// trustKey identifies a command for trust and quarantine: its project, tool
// versions and options, without the files or folders it names, so checking
// one test file vouches for the runner rather than for that file alone.
func trustKey(spec protocol.JobSpec) string {
	classifier.Normalize(&spec)
	argv := append([]string(nil), spec.Argv[:min(1, len(spec.Argv))]...)
	for _, arg := range spec.Argv[len(argv):] {
		if strings.HasPrefix(arg, "-") || !strings.ContainsAny(arg, `/\.`) {
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
