package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
	"tidalbridge/packages/scheduler"
	classifier "tidalbridge/packages/task-classifier"
)

// Test suites an adapter may split between this laptop and a phone: how many
// test files a whole run covers, which the parts of a split run must add up
// to, and when splitting is off for a suite whose parts did not.
const suitesFile = "suites.json"

type suiteRecord struct {
	Files        int       `json:"files"`
	Updated      time.Time `json:"updated"`
	NoSplitUntil time.Time `json:"no_split_until,omitempty"`
}

func (h *Host) loadSuites() {
	h.suites = map[string]*suiteRecord{}
	b, _ := os.ReadFile(filepath.Join(h.Dir, suitesFile))
	json.Unmarshal(b, &h.suites)
	if h.suites == nil {
		h.suites = map[string]*suiteRecord{}
	}
}

// split lets a long test suite routed to a phone also run half here when this
// laptop has room. It needs the suite's size from a whole run in the last two
// weeks, to check the parts against.
func (h *Host) split(in scheduler.Input, d *protocol.Decision) {
	sig := verifySignature(in.Spec)
	h.mu.RLock()
	s, peak := h.suites[sig], 0.0
	if c := h.costs[sig]; c != nil {
		peak = c.RAMMB
	}
	h.mu.RUnlock()
	if s == nil || s.Files < 4 || time.Since(s.Updated) > 14*24*time.Hour || time.Now().Before(s.NoSplitUntil) {
		return
	}
	if ok, why := scheduler.SplitWorthIt(in, *d, peak); ok {
		d.Split, d.SplitFiles = 2, s.Files
		d.Explanation += " " + why
	}
}

// RecordSuite keeps a suite's size from a whole run. When the parts of a
// split run covered a different number of test files than the whole run that
// followed, splitting stays off for that suite for a month.
func (h *Host) RecordSuite(input protocol.SuiteObservation) error {
	if err := classifier.Normalize(&input.Spec); err != nil {
		return err
	}
	if input.Files < 1 || input.Files > 1000000 || input.SplitSum < -1 {
		return fmt.Errorf("invalid suite observation")
	}
	sig := scheduler.Signature(input.Spec)
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.suites[sig]
	if s == nil {
		s = &suiteRecord{}
		h.suites[sig] = s
	}
	if input.SplitSum != 0 && input.SplitSum != input.Files {
		s.NoSplitUntil = time.Now().Add(30 * 24 * time.Hour)
		h.log.Info("split_disabled", "workspace", input.Spec.Workspace, "files", input.Files, "split_sum", input.SplitSum)
	}
	s.Files, s.Updated = input.Files, time.Now()
	if len(h.suites) > 256 {
		oldest := ""
		for key, v := range h.suites {
			if oldest == "" || v.Updated.Before(h.suites[oldest].Updated) {
				oldest = key
			}
		}
		delete(h.suites, oldest)
	}
	return config.SaveJSON(filepath.Join(h.Dir, suitesFile), h.suites)
}
