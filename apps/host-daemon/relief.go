package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
	"tidalbridge/packages/scheduler"
)

// laptopCost is what a command measurably costs this laptop, from its
// successful runs here. It is kept apart from the routing history, which
// rolls over, so the figures stay once the phone runs the command.
type laptopCost struct {
	CPUSeconds float64   `json:"cpu_seconds"`
	RAMMB      float64   `json:"ram_mb"`
	Seconds    float64   `json:"seconds"`
	Runs       int       `json:"runs"`
	Updated    time.Time `json:"updated"`
}

// liveUsage is what a job running on a phone holds now, read from the
// worker with every poll.
type liveUsage struct {
	RAMMB float64 `json:"ram_mb"`
	Cores float64 `json:"cores"`
	cpu   float64
	at    time.Time
}

func (h *Host) loadCosts() {
	h.costs = map[string]*laptopCost{}
	if b, err := os.ReadFile(filepath.Join(h.Dir, "laptop-costs.json")); err == nil {
		json.Unmarshal(b, &h.costs)
		return
	}
	for _, s := range h.history {
		h.recordCostLocked(s)
	}
	if len(h.costs) > 0 {
		config.SaveJSON(filepath.Join(h.Dir, "laptop-costs.json"), h.costs)
	}
}

// recordCostLocked folds a measured, successful local run into its
// command's cost: an average that follows the last few runs. h.mu held.
func (h *Host) recordCostLocked(s protocol.HistorySample) {
	if s.Target != "LOCAL" || !s.Success || s.HostCPUSeconds <= 0 {
		return
	}
	c := h.costs[s.Signature]
	if c == nil {
		c = &laptopCost{}
		h.costs[s.Signature] = c
	}
	c.Runs++
	weight := 1 / float64(min(c.Runs, 5))
	c.CPUSeconds += (s.HostCPUSeconds - c.CPUSeconds) * weight
	c.Seconds += (s.DurationMS/1000 - c.Seconds) * weight
	if c.RAMMB == 0 {
		c.RAMMB = s.HostPeakRAMMB
	} else if s.HostPeakRAMMB > 0 {
		c.RAMMB += (s.HostPeakRAMMB - c.RAMMB) * weight
	}
	c.Updated = s.Timestamp
	if len(h.costs) > 256 {
		oldest := ""
		for sig, v := range h.costs {
			if oldest == "" || v.Updated.Before(h.costs[oldest].Updated) {
				oldest = sig
			}
		}
		delete(h.costs, oldest)
	}
}

// sparedLocked is what running a job on a phone spared this laptop: the
// command's measured cost here when it completed and has run here before,
// otherwise the phone's own measurement of the work. Nil when the phone did
// no measurable work or the laptop ran the command after all. h.mu held.
func (h *Host) sparedLocked(j *protocol.Job) *protocol.Relief {
	n := len(j.Attempts)
	if n == 0 || strings.HasPrefix(j.Spec.Profile, "prepare:") || j.Spec.Speculative || j.Evacuated || (j.WriteBack != nil && j.WriteBack.Conflict != "") || (j.Decision.Confirm && j.State == "FAILED") {
		return nil
	}
	a := j.Attempts[n-1]
	if a.Target == "LOCAL" || a.WorkerCPUSeconds == nil || *a.WorkerCPUSeconds <= 0 {
		return nil
	}
	r := &protocol.Relief{CPUSeconds: *a.WorkerCPUSeconds}
	if a.WorkerPeakRAMMB != nil {
		r.RAMMB = *a.WorkerPeakRAMMB
	}
	if c := h.costs[scheduler.Signature(j.Spec)]; c != nil && j.State == "COMPLETED" && !j.Spec.Service {
		r.CPUSeconds, r.LaptopSeconds, r.Measured = c.CPUSeconds, c.Seconds, true
		if c.RAMMB > 0 {
			r.RAMMB = c.RAMMB
		}
	}
	return r
}

// trackUsageLocked keeps a running phone job's memory and processor load,
// and the most the phones held at once today. h.mu held.
func (h *Host) trackUsageLocked(id string, ramMB, cpuSeconds *float64) {
	if ramMB == nil || cpuSeconds == nil {
		return
	}
	now := time.Now()
	u := h.live[id]
	if u == nil {
		u = &liveUsage{cpu: *cpuSeconds, at: now}
		h.live[id] = u
	}
	u.RAMMB = *ramMB
	if dt := now.Sub(u.at).Seconds(); dt >= 2 {
		u.Cores = max(0, *cpuSeconds-u.cpu) / dt
		u.cpu, u.at = *cpuSeconds, now
	}
	held := 0.0
	for _, v := range h.live {
		held += v.RAMMB
	}
	if day := now.Format(time.DateOnly); day != h.peakDay {
		h.peakDay, h.peakHeldMB = day, 0
	}
	h.peakHeldMB = max(h.peakHeldMB, held)
}

// carrying is what the phones hold for this laptop right now, and about how
// much of its processor the same work would take here: the command's
// measured load here when known, otherwise the phone's own load.
func (h *Host) carrying(laptopCores int) map[string]any {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.live) == 0 {
		return nil
	}
	jobs := map[string]liveUsage{}
	ram, cores, here := 0.0, 0.0, 0.0
	for id, u := range h.live {
		jobs[id] = *u
		ram += u.RAMMB
		cores += u.Cores
		load := u.Cores
		if j := h.jobs[id]; j != nil && !j.Spec.Service {
			if c := h.costs[scheduler.Signature(j.Spec)]; c != nil && c.Seconds > 0 {
				load = c.CPUSeconds / c.Seconds
			}
		}
		here += load
	}
	return map[string]any{"ram_mb": ram, "cores": cores, "laptop_percent": min(100, 100*here/float64(max(1, laptopCores))), "jobs": jobs}
}
