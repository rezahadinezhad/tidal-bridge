package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
)

type Input struct {
	Spec            protocol.JobSpec
	Config          config.Config
	Host            protocol.Resources
	Nodes           []protocol.WorkerNode
	MissingBytes    map[string]int64
	EnvironmentWarm map[string]bool
	History         []protocol.HistorySample
	LocalBusy       bool
	// PortsBusy lists service ports already in use on the host loopback.
	PortsBusy []int
	// Quarantine, when set, says why this command's worker results are not
	// trusted: they differed from the laptop's.
	Quarantine string
}

func Signature(s protocol.JobSpec) string {
	b, _ := json.Marshal(struct {
		Argv                                       []string
		Workspace, Profile, EnvironmentFingerprint string
		Requirements                               protocol.Requirements
		WorkingDirectory                           string `json:"WorkingDirectory,omitempty"`
		Provision                                  bool
		Runtime                                    string `json:"Runtime,omitempty"`
		TimingSchema                               int
		Engine                                     string `json:"Engine,omitempty"`
	}{s.Argv, s.Workspace, s.Profile, s.EnvironmentFingerprint, s.Requirements, s.WorkingDirectory, s.Policy.Provision, s.Runtime, 2, s.Engine})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}
func measured(history []protocol.HistorySample, sig, target string, warm bool, fallback float64, fingerprint ...string) float64 {
	value := fallback
	n := 0
	for _, h := range history {
		if len(fingerprint) > 0 && h.DeviceFingerprint != fingerprint[0] {
			continue
		}
		if h.Signature == sig && h.Target == target && h.CacheWarm == warm && h.Success {
			if n == 0 {
				value = h.DurationMS
			} else {
				value = 0.75*value + 0.25*h.DurationMS
			}
			n++
		}
	}
	if n < 2 {
		return fallback
	}
	return value
}
func VersionMatches(actual, required string) bool {
	if required == "" || required == "*" {
		return actual != ""
	}
	op := "="
	for _, p := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(required, p) {
			op = p
			required = strings.TrimSpace(strings.TrimPrefix(required, p))
			break
		}
	}
	parse := func(s string) ([3]int, bool) {
		var out [3]int
		s = strings.TrimPrefix(strings.TrimSpace(s), "v")
		parts := strings.Split(s, ".")
		if len(parts) > 3 {
			return out, false
		}
		for i, p := range parts {
			v, e := strconv.Atoi(p)
			if e != nil {
				return out, false
			}
			out[i] = v
		}
		return out, true
	}
	a, ok := parse(actual)
	b, ok2 := parse(required)
	if !ok || !ok2 {
		return false
	}
	cmp := 0
	for i := 0; i < 3; i++ {
		if a[i] > b[i] {
			cmp = 1
			break
		}
		if a[i] < b[i] {
			cmp = -1
			break
		}
	}
	switch op {
	case ">=":
		return cmp >= 0
	case "<=":
		return cmp <= 0
	case ">":
		return cmp > 0
	case "<":
		return cmp < 0
	default:
		return cmp == 0
	}
}

// ColdEnvironmentMS prices preparing locked dependencies on a worker for the
// first time (npm ci, uv sync): minutes, not seconds. While a worker
// environment is cold, work normally stays local and the host prepares the
// environment in the background.
const ColdEnvironmentMS = 120000

// Pressure is the host's resource pressure in [0,1]: the higher of CPU and
// memory utilisation.
func Pressure(host protocol.Resources) float64 {
	pressure := host.CPUPercent / 100
	if host.RAMTotalMB > 0 {
		ram := 1 - float64(host.RAMAvailableMB)/float64(host.RAMTotalMB)
		if ram > pressure {
			pressure = ram
		}
	}
	return min(1, max(0, pressure))
}

// Tolerance is how much slower a worker may finish a job and still take it.
// Relieving the laptop is the objective: when it is busy, a slower run on a
// worker is better than competing with the user's interactive work.
func Tolerance(mode string, pressure float64) float64 {
	t := 1.1
	switch {
	case pressure >= 0.95:
		t = 6
	case pressure >= 0.85:
		t = 4
	case pressure >= 0.7:
		t = 2.5
	case pressure >= 0.5:
		t = 1.5
	}
	if mode == "PERFORMANCE" {
		t = max(t, 4)
	}
	return t
}

// localRelief estimates what running the job here costs the laptop, from
// measured local observations when available.
func localRelief(history []protocol.HistorySample, sig string, localMS float64) (cpuSeconds, ramMB float64, measured bool) {
	n := 0
	for _, h := range history {
		if h.Signature == sig && h.Target == "LOCAL" && h.Success && h.HostCPUSeconds > 0 {
			cpuSeconds += h.HostCPUSeconds
			ramMB = max(ramMB, h.HostPeakRAMMB)
			n++
		}
	}
	if n > 0 {
		return cpuSeconds / float64(n), ramMB, true
	}
	// Never measured here: the workers' own measurement of the same work.
	var worker []float64
	for _, h := range history {
		if h.Signature == sig && h.Target != "LOCAL" && h.Success && h.WorkerCPUSeconds > 0 {
			worker = append(worker, h.WorkerCPUSeconds)
			ramMB = max(ramMB, h.WorkerPeakRAMMB)
		}
	}
	if len(worker) > 0 {
		return median(worker), ramMB, false
	}
	return localMS / 1000, 0, false
}

// defaultEstimateMS is the adapter's estimate for a command whose project
// gives none.
const defaultEstimateMS = 1000

// coldEstimate is the laptop time of a command measured fewer than twice
// here: its latest local run, else its worker runs scaled by how this laptop
// compares with the workers on commands that ran on both, else the prior.
func coldEstimate(history []protocol.HistorySample, sig string, prior float64) float64 {
	local, remote := runTimes(history)
	if l := local[sig]; len(l) > 0 {
		return l[len(l)-1]
	}
	r := remote[sig]
	if len(r) == 0 {
		return prior
	}
	var ratios []float64
	for other, l := range local {
		if w := remote[other]; len(w) > 0 {
			ratios = append(ratios, median(l)/max(1, median(w)))
		}
	}
	if len(ratios) < 2 {
		// Not calibrated yet: the phone's time replaces only the default
		// guess, not an estimate the project gave.
		if prior > defaultEstimateMS {
			return prior
		}
		return median(r)
	}
	return median(r) * min(4, max(0.25, median(ratios)))
}

// runTimes groups warm, successful run times by command, here and on workers.
func runTimes(history []protocol.HistorySample) (local, remote map[string][]float64) {
	local, remote = map[string][]float64{}, map[string][]float64{}
	for _, h := range history {
		if !h.Success || !h.CacheWarm {
			continue
		}
		if h.Target == "LOCAL" {
			local[h.Signature] = append(local[h.Signature], h.DurationMS)
		} else {
			remote[h.Signature] = append(remote[h.Signature], h.DurationMS)
		}
	}
	return local, remote
}

func median(values []float64) float64 {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// SplitWorthIt says whether a test suite routed to a worker should also run
// half here at the same time: only when it is long enough on the worker for
// that to matter and this laptop has room for its half (CPU at most half
// busy, free memory for the suite's peak plus the reserve). Max mode keeps
// the whole suite on the phone.
func SplitWorthIt(in Input, d protocol.Decision, peakRAMMB float64) (bool, string) {
	if in.Config.Mode == "PERFORMANCE" || in.LocalBusy {
		return false, ""
	}
	expected := 0.0
	for _, c := range d.Candidates {
		if c.Target == d.Target {
			expected = c.TotalCostMS
		}
	}
	if peakRAMMB <= 0 {
		sig := Signature(in.Spec)
		for _, h := range in.History {
			if h.Signature == sig {
				peakRAMMB = max(peakRAMMB, h.HostPeakRAMMB, h.WorkerPeakRAMMB)
			}
		}
	}
	need := uint64(max(512, peakRAMMB)) + in.Config.ReserveRAMMB
	if expected < 30000 || in.Host.CPUPercent > 50 || in.Host.RAMAvailableMB < need {
		return false, ""
	}
	return true, fmt.Sprintf("Split in two: this laptop has room (CPU %.0f%%, %d MB free), so half the suite runs here and the result comes sooner.", in.Host.CPUPercent, in.Host.RAMAvailableMB)
}

// remoteRegressed reports a command that recently failed on a worker after
// succeeding locally: an environment difference rather than a code failure.
func remoteRegressed(history []protocol.HistorySample, sig, target string) bool {
	var lastLocalSuccess, lastRemoteSuccess time.Time
	for _, h := range history {
		if h.Signature != sig || !h.Success {
			continue
		}
		if h.Target == "LOCAL" && h.Timestamp.After(lastLocalSuccess) {
			lastLocalSuccess = h.Timestamp
		}
		if h.Target == target && h.Timestamp.After(lastRemoteSuccess) {
			lastRemoteSuccess = h.Timestamp
		}
	}
	if lastLocalSuccess.IsZero() || !lastLocalSuccess.After(lastRemoteSuccess) {
		return false
	}
	failures := 0
	for _, h := range history {
		if h.Signature == sig && h.Target == target && !h.Success && h.Timestamp.After(lastRemoteSuccess) && time.Since(h.Timestamp) < 24*time.Hour {
			failures++
		}
	}
	return failures >= 2
}

func Route(in Input) protocol.Decision {
	s := in.Spec
	sig := Signature(s)
	local := measured(in.History, sig, "LOCAL", true, coldEstimate(in.History, sig, s.EstimatedDurationMS))
	pressure := Pressure(in.Host)
	localCost := local * (1 + max(0, pressure-0.65)*3)
	d := protocol.Decision{Target: "LOCAL", Explanation: "Local execution is the conservative default.", LocalCostMS: localCost}
	if s.Policy.ForceLocal {
		d.Explanation = "Local execution explicitly requested."
		alternative := in
		alternative.Spec.Policy.ForceLocal = false
		if a := Route(alternative); a.Target != "LOCAL" && a.Target != "REJECT" && a.Target != "WAIT" {
			d.Explanation = "Local execution explicitly requested; the phone could have taken it."
			d.ForcedLocal = true
		}
		return d
	}
	tolerance := Tolerance(in.Config.Mode, pressure)
	reliefCPU, reliefRAM, reliefMeasured := localRelief(in.History, sig, local)
	timeBased := in.Config.Mode == "CONSERVATIVE"
	threshold := in.Config.MinimumBenefit
	if timeBased {
		threshold = max(threshold, 0.4)
	}
	best := -1e18
	busyEligible := false
	recovering := false
	for _, n := range in.Nodes {
		c := protocol.Candidate{Target: n.ID, Eligible: true, Reasons: []string{}, SyncBytes: in.MissingBytes[n.ID]}
		c.EnvironmentWarm = in.EnvironmentWarm[n.ID]
		r := n.Profile.Capabilities.Resources
		deny := func(reason string) { c.Eligible = false; c.Reasons = append(c.Reasons, reason) }
		if in.Config.Paused {
			deny("Offloading is paused.")
		}
		if s.Requirements.HostOnly {
			deny("Host-only or unproven command.")
		}
		if n.State != "READY" && n.State != "BUSY" {
			deny("Worker is not healthy: " + n.State)
			if n.State == "DEGRADED" || n.State == "ADB_CONNECTED" || n.State == "WORKER_STARTING" {
				recovering = true
			}
		}
		if n.Draining {
			deny("Worker is draining.")
		}
		if s.Policy.DeviceID != "" && n.ID != s.Policy.DeviceID {
			deny("Different worker requested.")
		}
		if s.Requirements.Architecture != "any" && s.Requirements.Architecture != n.Profile.Capabilities.Architecture {
			deny("Architecture mismatch.")
		}
		debian := s.Runtime == "debian"
		if debian && !n.Profile.Capabilities.Features["runtime_debian"] {
			deny("Debian runtime is not installed on this worker.")
		}
		for runtime, version := range s.Requirements.Runtimes {
			name := runtime
			actual := n.Profile.Capabilities.Runtimes[runtime]
			if debian {
				switch runtime {
				case "sh", "bash":
					actual = "1.0.0"
				case "python":
					// The Debian guest provides python3; uv projects bring their own.
					name, version = "debian-python", "*"
					actual = n.Profile.Capabilities.Runtimes[name]
				default:
					name = "debian-" + runtime
					actual = n.Profile.Capabilities.Runtimes[name]
				}
			}
			if !VersionMatches(actual, version) {
				deny(fmt.Sprintf("Runtime %s %q does not satisfy %s.", name, actual, version))
			} else {
				c.Reasons = append(c.Reasons, fmt.Sprintf("%s %s is compatible.", name, actual))
			}
		}
		if s.Requirements.Browser && !n.Profile.Capabilities.Features["browser_qa"] {
			deny("Browser capture is unavailable.")
		}
		if s.Service {
			if !n.Profile.Capabilities.Features["services"] {
				deny("Worker does not run services.")
			}
			for _, port := range in.PortsBusy {
				deny(fmt.Sprintf("Port %d is already in use on this laptop.", port))
			}
		}
		if r.RAMAvailableMB < in.Config.ReserveRAMMB+s.Requirements.RAMMB {
			deny("Insufficient RAM after the worker reserve.")
		}
		if r.StorageAvailableMB < s.Requirements.StorageMB+uint64(max(0, c.SyncBytes))/(1<<20)+256 {
			deny("Insufficient storage headroom.")
		}
		if r.Thermal == "severe" || r.Thermal == "critical" || r.Thermal == "emergency" {
			deny("Thermal safety limit.")
		} else if n.CoolingUntil != nil && time.Now().Before(*n.CoolingUntil) {
			deny("Cooling down after overheating until " + n.CoolingUntil.Format("15:04") + ".")
		}
		if r.BatteryPercent != nil && *r.BatteryPercent < 15 && (r.Charging == nil || !*r.Charging) {
			deny("Battery is below 15%.")
		}
		if in.Config.Mode == "BATTERY_SAVER" && (r.Charging == nil || !*r.Charging) {
			deny("Battery saver requires charging.")
		}
		if !s.Policy.ForceRemote && remoteRegressed(in.History, sig, n.ID) {
			deny("Recently failed on this worker but succeeded locally; keeping it local.")
		}
		if in.Quarantine != "" && !s.Policy.ForceRemote {
			deny(in.Quarantine)
		}
		capacity, why := Capacity(n, in.Config)
		if why != "" {
			c.Reasons = append(c.Reasons, fmt.Sprintf("Up to %d job(s) now: %s.", capacity, why))
		}
		if s.Requirements.Browser {
			capacity = 1
		}
		if c.Eligible && !s.Service && n.ActiveJobs >= capacity {
			busyEligible = true
			deny("Worker capacity is occupied.")
		}
		speed := n.Profile.Calibration.TransferMBPS
		if speed <= 0 {
			speed = 10
		}
		syncMS := float64(c.SyncBytes) / (speed * 1e6) * 1000
		// Sync is priced separately and subtracted from history. Source edits
		// must not discard comparable execution timings or reuse cold installs.
		warm := !s.Policy.Provision || c.EnvironmentWarm
		remote := measured(in.History, sig, n.ID, warm, local*1.15, n.Profile.Calibration.Fingerprint)
		risk := 0.05 * remote
		if r.Thermal == "warm" || r.Thermal == "moderate" {
			risk += remote * 0.3
		}
		setup := max(20, n.Profile.Calibration.RTTMS) * 2
		if s.Policy.Provision && !c.EnvironmentWarm {
			setup += ColdEnvironmentMS
		}
		if s.Policy.Provision {
			c.Reasons = append(c.Reasons, fmt.Sprintf("Dependency environment warm: %t (independent of changed source files).", c.EnvironmentWarm))
		}
		c.TotalCostMS = remote + syncMS + setup + risk
		worthIt := false
		if timeBased {
			c.Benefit = (localCost - c.TotalCostMS) / max(1, localCost)
			worthIt = c.Benefit > threshold
		} else {
			// Relief objective: the share of the tolerated time budget left.
			budget := local * tolerance
			c.Benefit = (budget - c.TotalCostMS) / max(1, budget)
			meaningful := reliefCPU >= 0.3 || reliefRAM >= 100
			if !meaningful {
				c.Reasons = append(c.Reasons, fmt.Sprintf("Too small to relieve the laptop (%.2f CPU-seconds).", reliefCPU))
			}
			worthIt = c.Benefit >= 0 && meaningful
		}
		exploring := false
		if !worthIt && !timeBased && !s.Service && strings.HasPrefix(s.Profile, "detected:") && reliefCPU >= 1 && local >= 3000 &&
			n.ActiveJobs == 0 && warm && (r.Thermal == "" || r.Thermal == "nominal") && remoteSamples(in.History, sig, n.ID, n.Profile.Calibration.Fingerprint) < 2 {
			// Universal mode measures a new command once or twice on an idle
			// worker; from then on its measured history decides.
			worthIt, exploring = true, true
		}
		if s.Service {
			// Long-running dev servers hold gigabytes for hours.
			worthIt = in.Config.Mode == "AUTO" || in.Config.Mode == "PERFORMANCE"
			if strings.HasPrefix(s.Profile, "detected:") && s.Policy.Provision && !c.EnvironmentWarm {
				// Never make a dev server wait minutes for a first install.
				worthIt = false
				c.Reasons = append(c.Reasons, "Dependencies are still being prepared on the phone; the service starts locally.")
			}
			c.Benefit = 1
		}
		c.Reasons = append(c.Reasons, fmt.Sprintf("Host pressure %.0f%%; tolerated slowdown %.1fx; transfer %d bytes; expected %.0f ms vs %.0f ms locally.", pressure*100, tolerance, c.SyncBytes, c.TotalCostMS, local))
		if c.Eligible && ((worthIt && c.Benefit > best) || (s.Policy.ForceRemote && d.Target == "LOCAL")) {
			best = c.Benefit
			d.Target = n.ID
			switch {
			case s.Service:
				d.Explanation = fmt.Sprintf("%s: long-running service moved off the laptop.", n.ID)
			case exploring:
				d.Explanation = fmt.Sprintf("%s: measuring this command on the phone (%.1fs locally) so later runs are placed by history.", n.ID, local/1000)
			case timeBased:
				d.Explanation = fmt.Sprintf("%s: estimated benefit %.0f%%, including transfer/setup and host pressure.", n.ID, 100*c.Benefit)
			default:
				relief := fmt.Sprintf("about %.1f CPU-seconds", reliefCPU)
				if reliefRAM > 0 {
					relief += fmt.Sprintf(" and %.0f MB", reliefRAM)
				}
				if !reliefMeasured {
					relief += " (estimated)"
				}
				d.Explanation = fmt.Sprintf("%s: relieves the laptop of %s; expected %.1fs vs %.1fs locally (tolerated %.1fx at %.0f%% pressure).", n.ID, relief, c.TotalCostMS/1000, local/1000, tolerance, pressure*100)
			}
		}
		d.Candidates = append(d.Candidates, c)
	}
	if d.Target == "LOCAL" && s.Policy.ForceRemote {
		d.Target = "REJECT"
		d.Explanation = "No compatible, safe remote worker is available."
		if busyEligible || recovering {
			d.Target = "WAIT"
			d.Explanation = "Waiting for compatible worker capacity."
			if recovering {
				d.Explanation = RecoveringExplanation
			}
		}
	}
	if d.Target == "LOCAL" && in.LocalBusy {
		d.Target = "WAIT"
		d.Explanation = "Waiting for local capacity; remote benefit is insufficient."
	}
	return d
}

func remoteSamples(history []protocol.HistorySample, sig, target, fingerprint string) int {
	n := 0
	for _, h := range history {
		if h.Signature == sig && h.Target == target && h.DeviceFingerprint == fingerprint {
			n++
		}
	}
	return n
}

// Capacity is how many jobs a worker takes now: the configured maximum,
// lowered while the phone is in use, warm, on battery or short of memory,
// unless the capacity is fixed. The reason names the tightest limit.
func Capacity(n protocol.WorkerNode, cfg config.Config) (int, string) {
	capacity := cfg.WorkerConcurrency
	if capacity < 1 {
		capacity = max(1, n.Capacity)
	}
	if cfg.FixedCapacity {
		return capacity, ""
	}
	r := n.Profile.Capabilities.Resources
	reason := ""
	limit := func(most int, why string) {
		if most = max(1, most); most < capacity {
			capacity, reason = most, why
		}
	}
	if r.InUse != nil && *r.InUse && cfg.PhoneUse != "max" {
		limit(1, "you are using the phone")
	}
	switch r.Thermal {
	case "warm":
		limit(2, "the phone is getting warm")
	case "moderate":
		limit(1, "the phone is warm")
	}
	if r.Charging != nil && !*r.Charging {
		limit(1, "the phone is on battery")
	}
	if r.RAMAvailableMB > 0 {
		limit((int(r.RAMAvailableMB)-int(cfg.ReserveRAMMB))/1200, "the phone is short of memory")
	}
	return capacity, reason
}

// RecoveringExplanation marks a forced-remote job waiting for a worker whose
// connection is being repaired; the host bounds this wait.
const RecoveringExplanation = "Waiting for the worker connection to recover."
