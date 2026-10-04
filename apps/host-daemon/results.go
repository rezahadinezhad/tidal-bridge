package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
)

// A test or read-only check whose inputs did not change since a phone ran it
// gets that result back at once instead of running again. Its inputs are the
// command, its options and environment, the runtime, and the exact files the
// phone ran with: the project, the files it reads from outside its folder and
// shared .env files, compared by content.
//
// A pass is reused for up to six hours and never across midnight, since tests
// may read the date. A failure is reused once, so retrying a flaky test runs
// it again. A run that connected to a laptop service (a database, an API)
// through a tunnel is never reused, because its result depends on more than
// the files; neither are commands that change files, dev servers, quarantined
// commands, or output over 1 MB.
const (
	reuseDir     = "reuse"
	reuseWindow  = 6 * time.Hour
	reuseOutput  = 1 << 20
	reuseEntries = 256
)

type reuseEntry struct {
	Key         string    `json:"key"`
	Scope       string    `json:"scope"`
	Job         string    `json:"job"`
	Target      string    `json:"target"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	ExitCode    int       `json:"exit_code"`
	Stdout      string    `json:"stdout"`
	Stderr      string    `json:"stderr"`
	Finished    time.Time `json:"finished"`
	DurationMS  float64   `json:"duration_ms"`
}

func (e *reuseEntry) fresh(now time.Time) bool {
	y1, m1, d1 := e.Finished.Local().Date()
	y2, m2, d2 := now.Local().Date()
	return !now.Before(e.Finished) && now.Sub(e.Finished) < reuseWindow && y1 == y2 && m1 == m2 && d1 == d2
}

// treeIdentity names exactly the files a run sees: each path, content hash
// and executable bit, in path order.
func treeIdentity(m *protocol.Manifest) string {
	files := slices.Clone(m.Files)
	slices.SortFunc(files, func(a, b protocol.FileEntry) int { return strings.Compare(a.Path, b.Path) })
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%s\x00%t\n", f.Path, f.Hash, f.Executable)
	}
	return hex.EncodeToString(h.Sum(nil))[:40]
}

// reuseScope groups one command's entries in one workspace, so a command
// with none skips computing its tree identity.
func reuseScope(spec protocol.JobSpec) string {
	b, _ := json.Marshal([]any{strings.ToLower(spec.Workspace), spec.WorkingDirectory, spec.Argv})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

func reuseKey(spec protocol.JobSpec, tree string) string {
	b, _ := json.Marshal([]any{reuseScope(spec), spec.Env, runtimeOf(spec), spec.Engine, spec.EnvironmentFingerprint, spec.Policy.SyncEnvFiles, spec.ReversePorts, tree})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:20])
}

func (h *Host) reusable(spec protocol.JobSpec) bool {
	return !h.Config().NoReuse && spec.Workspace != "" && verifiable(spec) && !spec.Service && !spec.Policy.WriteBack &&
		spec.Render == nil && len(spec.ExpectedOutputs) == 0
}

// reusedResult returns a stored result the command may reuse, or nil. take
// consumes a failure, which is reused once.
func (h *Host) reusedResult(spec protocol.JobSpec, m *protocol.Manifest, take bool) *reuseEntry {
	if m == nil || !h.reusable(spec) {
		return nil
	}
	h.reuseMu.Lock()
	known := h.reuseScopes[reuseScope(spec)] > 0
	h.reuseMu.Unlock()
	if !known {
		return nil
	}
	tree, _, _ := h.treeFor(spec.Workspace, m)
	key := reuseKey(spec, treeIdentity(tree))
	h.mu.RLock()
	quarantined := h.quarantineLocked(verifySignature(spec)) != ""
	fingerprints := map[string]string{}
	for id, n := range h.nodes {
		fingerprints[id] = n.Profile.Calibration.Fingerprint
	}
	h.mu.RUnlock()
	h.reuseMu.Lock()
	defer h.reuseMu.Unlock()
	e := h.reuse[key]
	if e == nil {
		return nil
	}
	// A worker whose runtime changed since may give a different result.
	changed := e.Fingerprint != "" && fingerprints[e.Target] != "" && fingerprints[e.Target] != e.Fingerprint
	if quarantined || changed || !e.fresh(time.Now()) {
		h.dropReuseLocked(e)
		return nil
	}
	if take && e.ExitCode != 0 {
		h.dropReuseLocked(e)
	}
	found := *e
	return &found
}

// rememberResult keeps a finished phone run's result for reuse when it
// qualifies. It reads the complete output from the job's logs.
func (h *Host) rememberResult(id string, spec protocol.JobSpec, a protocol.Attempt, exitCode int, fingerprint string, finished time.Time) {
	if a.Tree == "" || !h.reusable(spec) || len(spec.ReversePorts) > 0 && (a.ServicesUsed == nil || *a.ServicesUsed) {
		return
	}
	dir := filepath.Join(h.Dir, "results", id, a.ID)
	stdout, errOut := readSmall(filepath.Join(dir, "stdout.log"))
	stderr, errErr := readSmall(filepath.Join(dir, "stderr.log"))
	if errOut != nil || errErr != nil {
		return
	}
	e := &reuseEntry{Key: reuseKey(spec, a.Tree), Scope: reuseScope(spec), Job: id, Target: a.Target, Fingerprint: fingerprint,
		ExitCode: exitCode, Stdout: stdout, Stderr: stderr, Finished: finished, DurationMS: a.DurationMS}
	h.reuseMu.Lock()
	defer h.reuseMu.Unlock()
	if old := h.reuse[e.Key]; old != nil {
		h.dropReuseLocked(old)
	}
	h.reuse[e.Key] = e
	h.reuseScopes[e.Scope]++
	config.SaveJSON(filepath.Join(h.Dir, "cache", reuseDir, e.Key+".json"), e)
	h.trimReuseLocked(time.Now())
}

func readSmall(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > reuseOutput {
		return "", fmt.Errorf("output too large to reuse")
	}
	b, err := os.ReadFile(path)
	return string(b), err
}

func (h *Host) dropReuseLocked(e *reuseEntry) {
	if h.reuse[e.Key] == nil {
		return
	}
	delete(h.reuse, e.Key)
	if h.reuseScopes[e.Scope]--; h.reuseScopes[e.Scope] <= 0 {
		delete(h.reuseScopes, e.Scope)
	}
	os.Remove(filepath.Join(h.Dir, "cache", reuseDir, e.Key+".json"))
}

// trimReuseLocked drops expired entries, then the oldest beyond the limit.
func (h *Host) trimReuseLocked(now time.Time) {
	entries := make([]*reuseEntry, 0, len(h.reuse))
	for _, e := range h.reuse {
		if e.fresh(now) {
			entries = append(entries, e)
		} else {
			h.dropReuseLocked(e)
		}
	}
	slices.SortFunc(entries, func(a, b *reuseEntry) int { return a.Finished.Compare(b.Finished) })
	for len(entries) > reuseEntries {
		h.dropReuseLocked(entries[0])
		entries = entries[1:]
	}
}

func (h *Host) loadReuse() {
	h.reuse, h.reuseScopes = map[string]*reuseEntry{}, map[string]int{}
	dir := filepath.Join(h.Dir, "cache", reuseDir)
	os.MkdirAll(dir, 0o700)
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, path := range files {
		var e reuseEntry
		if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &e) != nil || e.Key+".json" != filepath.Base(path) {
			os.Remove(path)
			continue
		}
		h.reuse[e.Key] = &e
		h.reuseScopes[e.Scope]++
	}
	h.trimReuseLocked(time.Now())
}

// serveReused completes a job with a stored result: the same output and
// exit code, after one line saying where it comes from.
func (h *Host) serveReused(id string, e *reuseEntry) {
	note := fmt.Sprintf("[Tidal Bridge] Nothing this command reads changed since the phone ran it at %s; reusing that result.\n", e.Finished.Local().Format("15:04"))
	h.mu.Lock()
	j := h.jobs[id]
	if j == nil || j.State == "CANCELLED" {
		h.mu.Unlock()
		return
	}
	a := protocol.Attempt{ID: config.Random(), Target: "REUSED", Started: time.Now(), Reused: e.Job}
	dir := filepath.Join(h.Dir, "results", id, a.ID)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(e.Stdout), 0o600)
	os.WriteFile(filepath.Join(dir, "stderr.log"), []byte(note+e.Stderr), 0o600)
	code := e.ExitCode
	now := time.Now()
	j.Attempts = append(j.Attempts, a)
	j.Decision = protocol.Decision{Target: e.Target, Explanation: fmt.Sprintf("Reused the result of %s from %s: nothing it reads changed.", e.Job, e.Finished.Local().Format("15:04:05"))}
	j.ExitCode, j.State, j.Error, j.Finished = &code, "COMPLETED", "", &now
	if code != 0 {
		j.State = "FAILED"
	}
	j.Stdout, j.Stderr = clip(e.Stdout), clip(note+e.Stderr)
	j.OutputBytes = int64(len(e.Stdout) + len(note) + len(e.Stderr))
	j.Truncated = len(e.Stdout) > 128<<10 || len(note)+len(e.Stderr) > 128<<10
	if day := now.Format(time.DateOnly); h.reuseDay != day {
		h.reuseDay, h.reuseCount, h.reuseSeconds = day, 0, 0
	}
	h.reuseCount++
	h.reuseSeconds += e.DurationMS / 1000
	h.save(j)
	h.mu.Unlock()
	h.log.Info("job_reused", "job", id, "from", e.Job)
	h.signal()
}

func clip(s string) string { return s[:min(len(s), 128<<10)] }
