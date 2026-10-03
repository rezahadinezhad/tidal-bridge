package scheduler

import (
	"testing"
	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
)

func fixture() Input {
	c := config.Default()
	s := protocol.JobSpec{Argv: []string{"python", "task.py"}, Requirements: protocol.Requirements{Architecture: "any", Runtimes: map[string]string{"python": ">=3.11"}}, EstimatedDurationMS: 10000}
	n := protocol.WorkerNode{ID: "worker-a", State: "READY", Capacity: 2, Profile: protocol.DeviceProfile{Capabilities: protocol.CapabilitySet{Architecture: "arm64", Runtimes: map[string]string{"python": "3.11.9"}, Resources: protocol.Resources{RAMAvailableMB: 8000, StorageAvailableMB: 40000, Thermal: "nominal"}}, Calibration: protocol.Calibration{TransferMBPS: 20, RTTMS: 2}}}
	return Input{Spec: s, Config: c, Host: protocol.Resources{CPUPercent: 20, RAMTotalMB: 16000, RAMAvailableMB: 10000}, Nodes: []protocol.WorkerNode{n}, MissingBytes: map[string]int64{}}
}
func TestEconomics(t *testing.T) {
	in := fixture()
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal(d)
	}
	in.Host.CPUPercent = 95
	if d := Route(in); d.Target != "worker-a" {
		t.Fatal(d)
	}
	in.MissingBytes["worker-a"] = 2e9
	if d := Route(in); d.Target != "LOCAL" {
		t.Fatal("large cold transfer should stay local", d)
	}
	in.MissingBytes["worker-a"] = 0
	if d := Route(in); d.Target != "worker-a" {
		t.Fatal("warm cache should qualify", d)
	}
}
func TestSafetyNeverBypassed(t *testing.T) {
	for _, kind := range []string{"host", "architecture", "runtime", "thermal", "battery", "memory", "drain", "offline", "pause"} {
		t.Run(kind, func(t *testing.T) {
			in := fixture()
			in.Spec.Policy.ForceRemote = true
			n := &in.Nodes[0]
			switch kind {
			case "host":
				in.Spec.Requirements.HostOnly = true
			case "architecture":
				in.Spec.Requirements.Architecture = "x86_64"
			case "runtime":
				in.Spec.Requirements.Runtimes["python"] = ">=4"
			case "thermal":
				n.Profile.Capabilities.Resources.Thermal = "severe"
			case "battery":
				v := 4
				charging := false
				n.Profile.Capabilities.Resources.BatteryPercent = &v
				n.Profile.Capabilities.Resources.Charging = &charging
			case "memory":
				n.Profile.Capabilities.Resources.RAMAvailableMB = 200
			case "drain":
				n.Draining = true
			case "offline":
				n.State = "DISCONNECTED"
			case "pause":
				in.Config.Paused = true
			}
			if d := Route(in); d.Target != "REJECT" {
				t.Fatalf("%s bypassed: %+v", kind, d)
			}
		})
	}
}
func TestMultiDeviceAndBackpressure(t *testing.T) {
	in := fixture()
	in.Config.WorkerConcurrency = 2
	in.Spec.Policy.ForceRemote = true
	in.Nodes[0].ActiveJobs = 2
	n := in.Nodes[0]
	n.ID = "worker-b"
	n.ActiveJobs = 0
	in.Nodes = append(in.Nodes, n)
	if d := Route(in); d.Target != "worker-b" {
		t.Fatal(d)
	}
	in.Nodes[1].ActiveJobs = 2
	if d := Route(in); d.Target != "WAIT" {
		t.Fatal(d)
	}
}
func TestHistoryAndVersion(t *testing.T) {
	in := fixture()
	sig := Signature(in.Spec)
	for i := 0; i < 3; i++ {
		in.History = append(in.History, protocol.HistorySample{Signature: sig, Target: "worker-a", CacheWarm: true, DurationMS: 1000, Success: true})
	}
	if d := Route(in); d.Target != "worker-a" {
		t.Fatal(d)
	}
	for _, test := range []struct {
		a, b string
		want bool
	}{{"3.11.9", ">=3.11", true}, {"3.9.1", ">=3.11", false}, {"v22.16.0", ">=18", true}, {"3.11.9", "^3.11", false}, {"", "*", false}} {
		if got := VersionMatches(test.a, test.b); got != test.want {
			t.Fatalf("%s %s: %v", test.a, test.b, got)
		}
	}
}
func TestSourceAndDependencyCacheHaveIndependentCosts(t *testing.T) {
	in := fixture()
	in.Spec.Policy.Provision = true
	in.Host.CPUPercent = 95
	in.MissingBytes["worker-a"] = 1000
	cold := Route(in).Candidates[0].TotalCostMS
	in.EnvironmentWarm = map[string]bool{"worker-a": true}
	warm := Route(in).Candidates[0].TotalCostMS
	if diff := cold - warm - ColdEnvironmentMS; diff > 1e-6 || diff < -1e-6 || !Route(in).Candidates[0].EnvironmentWarm {
		t.Fatal(cold, warm)
	}
}
func TestRuntimeUpgradeInvalidatesOldDeviceTimings(t *testing.T) {
	in := fixture()
	in.Nodes[0].Profile.Calibration.Fingerprint = "new-runtime"
	for i := 0; i < 3; i++ {
		in.History = append(in.History, protocol.HistorySample{Signature: Signature(in.Spec), Target: "worker-a", CacheWarm: true, DurationMS: 1, Success: true, DeviceFingerprint: "old-runtime"})
	}
	if Route(in).Target != "LOCAL" {
		t.Fatal("old runtime observations used after upgrade")
	}
}

func TestColdDependenciesCannotUseWarmMeasurements(t *testing.T) {
	in := fixture()
	in.Spec.Policy.Provision = true
	for range 2 {
		in.History = append(in.History, protocol.HistorySample{Signature: Signature(in.Spec), Target: "worker-a", CacheWarm: true, DurationMS: 1, Success: true})
	}
	cold := Route(in).Candidates[0].TotalCostMS
	in.EnvironmentWarm = map[string]bool{"worker-a": true}
	warm := Route(in).Candidates[0].TotalCostMS
	if cold < 5000 || warm > 1000 {
		t.Fatal("mixed cold and warm environments", cold, warm)
	}
}

func TestDifferentWorkingDirectoriesHaveDifferentWorkloadSignatures(t *testing.T) {
	in := fixture()
	first := Signature(in.Spec)
	in.Spec.WorkingDirectory = "different-project"
	if Signature(in.Spec) == first {
		t.Fatal("subproject reused unrelated timings")
	}
}

func TestSourceEditsReuseWarmExecutionHistory(t *testing.T) {
	in := fixture()
	in.Spec.Policy.Provision = true
	in.EnvironmentWarm = map[string]bool{"worker-a": true}
	for range 2 {
		in.History = append(in.History, protocol.HistorySample{Signature: Signature(in.Spec), Target: "worker-a", DurationMS: 1000, CacheWarm: true, Success: true})
	}
	in.MissingBytes["worker-a"] = 1 << 20
	if cost := Route(in).Candidates[0].TotalCostMS; cost > 2000 {
		t.Fatal("source edit discarded warm execution timings", cost)
	}
}
