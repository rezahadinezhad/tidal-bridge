package scheduler

import (
	"testing"

	"tidalbridge/packages/protocol"
)

// A command never measured here is estimated from its phone runs, scaled by
// how this laptop compares with the phone on commands that ran on both.
func TestColdEstimateLearnsFromWorkerRuns(t *testing.T) {
	sample := func(sig, target string, ms float64) protocol.HistorySample {
		return protocol.HistorySample{Signature: sig, Target: target, DurationMS: ms, CacheWarm: true, Success: true}
	}
	history := []protocol.HistorySample{sample("new", "worker-a", 9000)}
	if got := coldEstimate(history, "new", 1000); got != 9000 {
		t.Fatal("without comparable commands the phone's time replaces the default guess", got)
	}
	if got := coldEstimate(history, "new", 10000); got != 10000 {
		t.Fatal("but not the project's own estimate", got)
	}
	history = append(history, sample("a", "LOCAL", 5000), sample("a", "worker-a", 10000), sample("b", "LOCAL", 6000), sample("b", "worker-a", 12000))
	if got := coldEstimate(history, "new", 1000); got != 4500 {
		t.Fatal("this laptop takes half the phone's time", got)
	}
	if got := coldEstimate(append(history, sample("new", "LOCAL", 3000)), "new", 1000); got != 3000 {
		t.Fatal("one local run beats any estimate", got)
	}
	if got := coldEstimate(history, "unknown", 1000); got != 1000 {
		t.Fatal("no runs anywhere keep the prior", got)
	}
}

func TestReliefFromTheWorkersMeasurement(t *testing.T) {
	history := []protocol.HistorySample{{Signature: "s", Target: "worker-a", Success: true, CacheWarm: true, WorkerCPUSeconds: 40, WorkerPeakRAMMB: 900}}
	if cpu, ram, measured := localRelief(history, "s", 1000); cpu != 40 || ram != 900 || measured {
		t.Fatal(cpu, ram, measured)
	}
}

func TestForcedLocalNotesWhatThePhoneCouldTake(t *testing.T) {
	in := fixture()
	in.Host.CPUPercent = 95
	in.Spec.Policy.ForceLocal = true
	if d := Route(in); d.Target != "LOCAL" || !d.ForcedLocal {
		t.Fatal("forced here although the phone could take it", d)
	}
	in.Nodes[0].Draining = true
	if d := Route(in); d.ForcedLocal {
		t.Fatal("the phone could not have taken it", d)
	}
}

func TestSplitNeedsRoomOnTheLaptop(t *testing.T) {
	in := fixture()
	d := protocol.Decision{Target: "worker-a", Candidates: []protocol.Candidate{{Target: "worker-a", TotalCostMS: 90000}}}
	if ok, _ := SplitWorthIt(in, d, 1500); !ok {
		t.Fatal("an idle laptop with memory to spare takes half")
	}
	busy, short, phoneOnly := in, in, in
	busy.Host.CPUPercent = 70
	short.Host.RAMAvailableMB = 2000
	phoneOnly.Config.Mode = "PERFORMANCE"
	for _, c := range []Input{busy, short, phoneOnly} {
		if ok, _ := SplitWorthIt(c, d, 1500); ok {
			t.Fatal("no room here, or Max mode", c.Host, c.Config.Mode)
		}
	}
	d.Candidates[0].TotalCostMS = 20000
	if ok, _ := SplitWorthIt(in, d, 1500); ok {
		t.Fatal("a short suite is not worth splitting")
	}
}
