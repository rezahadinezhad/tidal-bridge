package scheduler

import (
	"testing"
	"time"

	"tidalbridge/packages/protocol"
)

// The objective is relieving the laptop, not winning races: a slower worker
// takes the job when the host is under pressure, never when it is idle.
func TestReliefToleranceGrowsWithPressure(t *testing.T) {
	in := fixture()
	sig := Signature(in.Spec)
	for range 2 {
		// The worker measured 2.5x slower than the 10 s local estimate.
		in.History = append(in.History, protocol.HistorySample{Signature: sig, Target: "worker-a", CacheWarm: true, DurationMS: 25000, Success: true})
	}
	in.Host = protocol.Resources{CPUPercent: 20, RAMTotalMB: 16000, RAMAvailableMB: 12000}
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal("idle laptop must not take a 2.5x slowdown", d.Explanation)
	}
	in.Host = protocol.Resources{CPUPercent: 60, RAMTotalMB: 16000, RAMAvailableMB: 1000} // 94% RAM
	if d := Route(in); d.Target != "worker-a" {
		t.Fatal("loaded laptop should accept the slowdown", d.Explanation, d.Candidates)
	}
}

func TestTinyCommandsStayLocal(t *testing.T) {
	in := fixture()
	in.Host = protocol.Resources{CPUPercent: 99, RAMTotalMB: 16000, RAMAvailableMB: 500}
	sig := Signature(in.Spec)
	for range 2 {
		in.History = append(in.History, protocol.HistorySample{Signature: sig, Target: "LOCAL", CacheWarm: true, DurationMS: 150, Success: true, HostCPUSeconds: 0.05, HostPeakRAMMB: 20})
	}
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal("moving a 0.05 CPU-second command relieves nothing", d.Explanation)
	}
}

func TestServicesMoveWheneverEligible(t *testing.T) {
	in := fixture()
	in.Spec.Service = true
	in.Nodes[0].Profile.Capabilities.Features = map[string]bool{"services": true}
	if d := Route(in); d.Target != "worker-a" {
		t.Fatal("a dev server should move even on an idle laptop", d.Explanation)
	}
	in.PortsBusy = []int{3100}
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal("a busy local port must keep the service local", d.Explanation)
	}
	in.PortsBusy = nil
	in.Config.Mode = "CONSERVATIVE"
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal("conservative mode keeps services local", d.Explanation)
	}
}

func TestRemoteRegressionKeepsCommandLocal(t *testing.T) {
	in := fixture()
	in.Host = protocol.Resources{CPUPercent: 99, RAMTotalMB: 16000, RAMAvailableMB: 500}
	sig := Signature(in.Spec)
	now := time.Now()
	in.History = []protocol.HistorySample{
		{Signature: sig, Target: "LOCAL", DurationMS: 10000, Success: true, CacheWarm: true, Timestamp: now.Add(-3 * time.Minute), HostCPUSeconds: 9},
		{Signature: sig, Target: "worker-a", DurationMS: 4000, Success: false, CacheWarm: true, Timestamp: now.Add(-2 * time.Minute)},
		{Signature: sig, Target: "worker-a", DurationMS: 4000, Success: false, CacheWarm: true, Timestamp: now.Add(-time.Minute)},
	}
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal("repeated worker-only failures must keep the command local", d.Explanation)
	}
	in.Spec.Policy.ForceRemote = true
	if d := Route(in); d.Target != "worker-a" {
		t.Fatal("an explicit request may still retry the worker", d.Explanation)
	}
}

func TestDebianRuntimeRequirements(t *testing.T) {
	in := fixture()
	in.Spec.Runtime = "debian"
	in.Spec.Policy.ForceRemote = true
	in.Spec.Requirements.Runtimes = map[string]string{"node": ">=20"}
	if d := Route(in); d.Target != "REJECT" {
		t.Fatal("debian jobs need the debian runtime", d.Explanation)
	}
	in.Nodes[0].Profile.Capabilities.Features = map[string]bool{"runtime_debian": true}
	in.Nodes[0].Profile.Capabilities.Runtimes["debian-node"] = "22.20.0"
	if d := Route(in); d.Target != "worker-a" {
		t.Fatal("debian node satisfies the requirement", d.Explanation, d.Candidates)
	}
}
