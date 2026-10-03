package host

import (
	"math"
	"testing"
	"time"

	"tidalbridge/packages/protocol"
	"tidalbridge/packages/scheduler"
)

func TestSparedPrefersTheLaptopsOwnMeasurement(t *testing.T) {
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	spec := protocol.JobSpec{Argv: []string{"npm", "test"}, Workspace: h.Dir, Profile: "detected:test"}
	phoneCPU, phoneRAM := 90.0, 700.0
	job := protocol.Job{Spec: spec, State: "COMPLETED", Attempts: []protocol.Attempt{{Target: "worker-1", WorkerCPUSeconds: &phoneCPU, WorkerPeakRAMMB: &phoneRAM}}}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.sparedLocked(&job); r == nil || r.Measured || r.CPUSeconds != 90 || r.RAMMB != 700 {
		t.Fatalf("without a laptop run the phone's measurement stands: %+v", r)
	}
	for _, cpu := range []float64{100, 120} {
		h.recordCostLocked(protocol.HistorySample{Signature: scheduler.Signature(spec), Target: "LOCAL", Success: true, DurationMS: 50000, HostCPUSeconds: cpu, HostPeakRAMMB: 900, Timestamp: time.Now()})
	}
	if r := h.sparedLocked(&job); r == nil || !r.Measured || r.CPUSeconds != 110 || r.RAMMB != 900 || r.LaptopSeconds != 50 {
		t.Fatalf("the laptop's measured cost: %+v", r)
	}
	job.State = "FAILED"
	if r := h.sparedLocked(&job); r == nil || r.Measured {
		t.Fatalf("a failed run counts only what the phone did: %+v", r)
	}
	job.Evacuated = true
	if h.sparedLocked(&job) != nil {
		t.Fatal("the laptop ran an evacuated job after all")
	}
	job.Evacuated = false
	job.Attempts[0].Target = "LOCAL"
	if h.sparedLocked(&job) != nil {
		t.Fatal("a laptop run spares nothing")
	}
}

func TestCarryingSumsWhatPhonesHoldNow(t *testing.T) {
	h, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if h.carrying(8) != nil {
		t.Fatal("nothing runs on a phone")
	}
	ram, cpu := 500.0, 10.0
	h.mu.Lock()
	h.trackUsageLocked("a", &ram, &cpu)
	h.live["a"].at = h.live["a"].at.Add(-4 * time.Second)
	cpu = 18
	h.trackUsageLocked("a", &ram, &cpu)
	h.mu.Unlock()
	c := h.carrying(8)
	if c["ram_mb"] != 500.0 || math.Abs(c["cores"].(float64)-2) > 0.01 || math.Abs(c["laptop_percent"].(float64)-25) > 0.2 || h.peakHeldMB != 500 {
		t.Fatalf("two phone cores of eight laptop ones: %v", c)
	}
}
