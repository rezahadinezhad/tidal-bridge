package scheduler

import (
	"testing"
	"time"

	"tidalbridge/packages/protocol"
)

func detectedFixture() Input {
	in := fixture()
	in.Spec.Profile = "detected:typecheck"
	sig := Signature(in.Spec)
	for range 2 {
		in.History = append(in.History, protocol.HistorySample{Signature: sig, Target: "LOCAL", CacheWarm: true, DurationMS: 20000, Success: true, HostCPUSeconds: 30})
	}
	return in
}

// An idle laptop keeps a detected command local by economics alone, but
// universal mode measures it on an idle phone twice so history can decide.
func TestDetectedCommandsAreMeasuredOnAnIdleWorker(t *testing.T) {
	in := detectedFixture()
	if d := Route(in); d.Target != "worker-a" {
		t.Fatal("detected command should be measured on the idle worker", d.Explanation)
	}
	sig := Signature(in.Spec)
	for range 2 {
		in.History = append(in.History, protocol.HistorySample{Signature: sig, Target: "worker-a", CacheWarm: true, DurationMS: 60000, Success: true})
	}
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal("measured 3x slower on an idle laptop: stays local", d.Explanation)
	}
	in = detectedFixture()
	in.Nodes[0].ActiveJobs = 1
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal("never measure on a busy worker", d.Explanation)
	}
	in = detectedFixture()
	in.Spec.Profile = "automatic:typecheck"
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal("explicit task files keep their previous behavior", d.Explanation)
	}
}

func TestQuarantineKeepsCommandLocal(t *testing.T) {
	in := detectedFixture()
	in.Host.CPUPercent = 99
	in.Quarantine = "Failed on the phone but passed on this laptop."
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal("quarantined command moved", d.Explanation)
	}
	in.Spec.Policy.ForceRemote = true
	if d := Route(in); d.Target != "worker-a" {
		t.Fatal("an explicit force-remote request still reaches the worker", d.Explanation)
	}
}

func TestPhoneInUseRunsOneJobAtATime(t *testing.T) {
	in := fixture()
	in.Host.CPUPercent = 95
	inUse := true
	in.Nodes[0].ActiveJobs = 1
	in.Nodes[0].Profile.Capabilities.Resources.InUse = &inUse
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal("a second job while the phone is in use", d.Explanation)
	}
	in.Config.PhoneUse = "max"
	if d := Route(in); d.Target != "worker-a" {
		t.Fatal("phone_use max keeps full concurrency", d.Explanation)
	}
}

func TestHeatPacing(t *testing.T) {
	in := fixture()
	in.Host.CPUPercent = 95
	in.Nodes[0].ActiveJobs = 1
	in.Nodes[0].Profile.Capabilities.Resources.Thermal = "moderate"
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal("a warm phone runs one job at a time", d.Explanation)
	}
	in.Nodes[0].ActiveJobs = 0
	in.Nodes[0].Profile.Capabilities.Resources.Thermal = "nominal"
	until := time.Now().Add(time.Minute)
	in.Nodes[0].CoolingUntil = &until
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal("no new work while cooling down after overheating", d.Explanation)
	}
}

func TestCapacityAdaptsToThePhone(t *testing.T) {
	cfg := fixture().Config
	cfg.WorkerConcurrency = 3
	yes, no := true, false
	node := func(change func(*protocol.Resources)) protocol.WorkerNode {
		n := fixture().Nodes[0]
		n.Profile.Capabilities.Resources.Charging = &yes
		change(&n.Profile.Capabilities.Resources)
		return n
	}
	cases := []struct {
		name   string
		change func(*protocol.Resources)
		want   int
	}{
		{"cool, charging, plenty of memory", func(r *protocol.Resources) {}, 3},
		{"in use", func(r *protocol.Resources) { r.InUse = &yes }, 1},
		{"getting warm", func(r *protocol.Resources) { r.Thermal = "warm" }, 2},
		{"warm", func(r *protocol.Resources) { r.Thermal = "moderate" }, 1},
		{"on battery", func(r *protocol.Resources) { r.Charging = &no }, 1},
		{"short of memory", func(r *protocol.Resources) { r.RAMAvailableMB = 3500 }, 2},
	}
	for _, c := range cases {
		if got, why := Capacity(node(c.change), cfg); got != c.want || (c.want < 3) != (why != "") {
			t.Errorf("%s: %d (%q), want %d", c.name, got, why, c.want)
		}
	}
	cfg.FixedCapacity = true
	if got, _ := Capacity(node(func(r *protocol.Resources) { r.InUse = &yes; r.Thermal = "moderate" }), cfg); got != 3 {
		t.Error("a fixed capacity ignores the phone's state", got)
	}
}
