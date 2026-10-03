package host

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"tidalbridge/packages/browser"
	historystore "tidalbridge/packages/history-store"
	"time"

	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
	"tidalbridge/packages/scheduler"
	adapter "tidalbridge/packages/task-adapter"
	classifier "tidalbridge/packages/task-classifier"
	"tidalbridge/packages/telemetry"
	"tidalbridge/packages/transport"
)

type Host struct {
	// changes counts job, worker and setting updates; the dashboard stream
	// sends the full status only when it moved.
	changes atomic.Uint64
	// workerChanges counts phone readings refreshed between full refreshes;
	// the dashboard gets just the workers for those.
	workerChanges atomic.Uint64
	// watchedUntil: a dashboard is open; phones are read more often then.
	watchedUntil             atomic.Int64
	mu                       sync.RWMutex
	refreshMu                sync.Mutex
	cfg                      config.Config
	Dir, Token               string
	nodes                    map[string]*protocol.WorkerNode
	jobs                     map[string]*protocol.Job
	pending                  []string
	cancel                   map[string]context.CancelFunc
	history                  []protocol.HistorySample
	verify                   verification
	wake                     chan struct{}
	localActive, totalActive int
	monitor                  telemetry.Monitor
	log                      *slog.Logger
	audit                    *historystore.RollingWriter
	restart                  map[string]time.Time
	adbFailures              int // consecutive failed device listings (guarded by refreshMu)
	ctx                      context.Context

	// costs: what commands measurably cost this laptop (laptop-costs.json).
	costs map[string]*laptopCost
	// live: what jobs running on phones hold now; peakHeldMB: the most held
	// at once on peakDay.
	live       map[string]*liveUsage
	peakDay    string
	peakHeldMB float64
	// suites: test-suite sizes for split runs (suites.json).
	suites map[string]*suiteRecord
	// outside: folders projects read from outside themselves (outside.json).
	outside map[string][]string

	linkMu      sync.Mutex
	knownMu     sync.Mutex
	known       map[string]map[string]bool // worker ID -> content hashes present
	indexMu     sync.Mutex
	indexes     map[string]*indexSlot
	portMu      sync.Mutex
	reverseRefs map[string]int
	prepareMu   sync.Mutex
	preparing   map[string]time.Time
	running     sync.WaitGroup
	seen        map[string]time.Time // last client poll per job (service leases)

	probedPower, probedBrowser map[string]time.Time // per serial; guarded by refreshMu
}

// touch records that a client is still attached to a job.
func (h *Host) touch(id string) {
	h.mu.Lock()
	if _, ok := h.jobs[id]; ok {
		h.seen[id] = time.Now()
	}
	h.mu.Unlock()
}

func (h *Host) lastSeen(id string) time.Time {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.seen[id]
}

func New(dir string) (*Host, error) {
	c, e := config.Load(dir)
	if e != nil {
		return nil, e
	}
	token, e := config.Secret(dir)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Join(dir, "jobs"), 0700); e != nil {
		return nil, e
	}
	logpath := filepath.Join(dir, "audit.jsonl")
	f := &historystore.RollingWriter{Path: logpath, Limit: 8 << 20}
	h := &Host{cfg: c, Dir: dir, Token: token, nodes: map[string]*protocol.WorkerNode{}, jobs: map[string]*protocol.Job{}, live: map[string]*liveUsage{}, cancel: map[string]context.CancelFunc{}, wake: make(chan struct{}, 1), restart: map[string]time.Time{}, log: slog.New(slog.NewJSONHandler(f, nil)),
		known: map[string]map[string]bool{}, indexes: map[string]*indexSlot{}, reverseRefs: map[string]int{}, preparing: map[string]time.Time{}, seen: map[string]time.Time{}, probedPower: map[string]time.Time{}, probedBrowser: map[string]time.Time{}}
	h.audit = f
	files, _ := filepath.Glob(filepath.Join(dir, "jobs", "*.json"))
	sort.Strings(files)
	for _, path := range files {
		b, e := os.ReadFile(path)
		if e != nil {
			continue
		}
		var j protocol.Job
		if json.Unmarshal(b, &j) != nil {
			continue
		}
		if j.State == "RUNNING" || j.State == "QUEUED" || j.State == "WAIT" {
			j.State = "INTERRUPTED"
			j.Error = "Host restarted; attempt requires explicit resubmission."
			now := time.Now()
			j.Finished = &now
			config.SaveJSON(path, j)
		}
		if !regexp.MustCompile(`^[a-f0-9]{48}$`).MatchString(j.ID) {
			continue
		}
		h.jobs[j.ID] = &j
		if len(h.jobs) > 40 {
			var oldest string
			for id, candidate := range h.jobs {
				if oldest == "" || candidate.Created.Before(h.jobs[oldest].Created) {
					oldest = id
				}
			}
			delete(h.jobs, oldest)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "history.json"))
	json.Unmarshal(b, &h.history)
	if len(h.history) > 512 {
		h.history = h.history[len(h.history)-512:]
	}
	h.loadVerification()
	h.loadCosts()
	h.loadSuites()
	h.loadOutside()
	return h, nil
}
func (h *Host) Config() config.Config { h.mu.RLock(); defer h.mu.RUnlock(); return h.cfg }
func (h *Host) Close() error          { return h.audit.Close() }

// Shutdown cancels active attempts so remote processes are stopped and
// forwarded ports released before the host exits.
func (h *Host) Shutdown(ctx context.Context) {
	h.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(h.cancel))
	for _, cancel := range h.cancel {
		cancels = append(cancels, cancel)
	}
	h.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	h.stopServices(ctx)
}
func (h *Host) signal() {
	h.changes.Add(1)
	select {
	case h.wake <- struct{}{}:
	default:
	}
}
func (h *Host) save(j *protocol.Job) {
	h.changes.Add(1)
	if e := config.SaveJSON(filepath.Join(h.Dir, "jobs", j.ID+".json"), j); e != nil {
		h.log.Error("persist_job", "error", e.Error())
	}
}
func (h *Host) Start(ctx context.Context) {
	h.ctx = ctx
	// Own the ADB server: one started from an agent session lives in that
	// app's container and dies with it, dropping every worker tunnel.
	if adb := transport.FindADB(h.Config().ADB); adb != "" {
		startCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		transport.ADB(startCtx, adb, "", "start-server")
		cancel()
	}
	go h.monitor.Run(ctx)
	go h.watchPhones(ctx)
	go h.discover(ctx)
	go h.dispatch(ctx)
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		rounds := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.pruneIndexes()
				if rounds++; rounds%6 != 0 {
					continue
				}
				h.mu.RLock()
				active := map[string]bool{}
				for id, j := range h.jobs {
					if j.Finished == nil {
						active[id] = true
					}
				}
				h.mu.RUnlock()
				if e := historystore.Prune(h.Dir, 1000, active); e != nil {
					h.log.Error("retention_failed", "error", e.Error())
				}
			}
		}
	}()
}

// workersLocked lists the workers with their current capacity; h.mu held.
func (h *Host) workersLocked() []protocol.WorkerNode {
	nodes := []protocol.WorkerNode{}
	for _, n := range h.nodes {
		node := *n
		node.EffectiveCapacity, node.CapacityReason = scheduler.Capacity(node, h.cfg)
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	return nodes
}

func (h *Host) Status() any {
	h.mu.RLock()
	defer h.mu.RUnlock()
	nodes := h.workersLocked()
	jobs := []protocol.Job{}
	for _, j := range h.jobs {
		copy := *j
		copy.Stdout = ""
		copy.Stderr = ""
		jobs = append(jobs, copy)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Created.After(jobs[j].Created) })
	// Relief today: what the phones ran and what that spared the laptop.
	year, month, day := time.Now().Date()
	midnight := time.Date(year, month, day, 0, 0, 0, 0, time.Local)
	phoneJobs, phoneSeconds, cpuSeconds, peakMB, measured := 0, 0.0, 0.0, 0.0, 0
	forced, forcedCPU := 0, 0.0
	for _, j := range jobs {
		if !j.Created.After(midnight) {
			continue
		}
		if n := len(j.Attempts); j.Decision.ForcedLocal && n > 0 {
			forced++
			if c := j.Attempts[n-1].HostCPUSeconds; c != nil {
				forcedCPU += *c
			}
		}
		if r := h.sparedLocked(&j); r != nil {
			phoneJobs++
			phoneSeconds += j.Attempts[len(j.Attempts)-1].DurationMS / 1000
			cpuSeconds += r.CPUSeconds
			peakMB = max(peakMB, r.RAMMB)
			if r.Measured {
				measured++
			}
		}
	}
	if h.peakDay == time.Now().Format(time.DateOnly) {
		peakMB = max(peakMB, h.peakHeldMB)
	}
	if len(jobs) > 40 {
		jobs = jobs[:40]
	}
	// The live list needs no routing candidates or environments; a job's own
	// record (GET /v1/jobs/ID) keeps them.
	for i := range jobs {
		jobs[i].Decision.Candidates = nil
		jobs[i].Spec.Env = nil
		jobs[i].Spec.LocalArgv = nil
		jobs[i].Spared = h.sparedLocked(&jobs[i])
	}
	return map[string]any{"automation": adapter.LoadSettings(h.Dir), "relief": map[string]any{"phone_jobs_today": phoneJobs, "phone_seconds_today": phoneSeconds, "cpu_seconds_today": cpuSeconds, "measured_today": measured, "peak_ram_mb_today": peakMB, "forced_today": forced, "forced_cpu_seconds_today": forcedCPU},
		"capacity": map[string]any{"max": h.cfg.WorkerConcurrency, "fixed": h.cfg.FixedCapacity}, "product": "Tidal Bridge", "version": protocol.WorkerVersion, "protocol_version": protocol.Version, "mode": h.cfg.Mode, "paused": h.cfg.Paused, "host": h.monitor.Snapshot(), "workers": nodes, "jobs": jobs, "queue_depth": len(h.pending), "active_jobs": h.totalActive, "local_active": h.localActive}
}

// discover refreshes workers when ADB reports a device change (event
// driven through `adb track-devices`) and otherwise once a minute.
func (h *Host) discover(ctx context.Context) {
	h.refresh(ctx)
	changed := make(chan struct{}, 1)
	if adb := transport.FindADB(h.Config().ADB); adb != "" {
		go transport.TrackDevices(ctx, adb, func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		})
	}
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-changed:
			// Let the device settle (authorization, transport) before probing.
			select {
			case <-ctx.Done():
				return
			case <-time.After(700 * time.Millisecond):
			}
			h.refresh(ctx)
		case <-ticker.C:
			h.refresh(ctx)
		}
	}
}
func propMap(text string) map[string]string {
	out := map[string]string{}
	re := regexp.MustCompile(`\[([^]]+)\]: \[([^]]*)\]`)
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		out[m[1]] = m[2]
	}
	return out
}
func (h *Host) refresh(ctx context.Context) {
	h.refreshMu.Lock()
	defer h.refreshMu.Unlock()
	cfg := h.Config()
	adb := transport.FindADB(cfg.ADB)
	output, err := transport.ADB(ctx, adb, "", "devices", "-l")
	if err != nil && ctx.Err() == nil {
		// A slow or restarting adb server is not a disconnected phone: keep
		// the last known states unless adb keeps failing.
		if h.adbFailures++; h.adbFailures < 3 {
			return
		}
	} else {
		h.adbFailures = 0
	}
	connected := map[string]string{}
	for _, d := range transport.ParseDevices(output) {
		connected[d.Serial] = d.State
	}
	for _, approved := range cfg.Approved {
		h.mu.RLock()
		var previous *protocol.WorkerNode
		for _, n := range h.nodes {
			if n.Serial == approved.Serial {
				copy := *n
				previous = &copy
				break
			}
		}
		h.mu.RUnlock()
		n := protocol.WorkerNode{ID: approved.Serial, Serial: approved.Serial, Token: approved.Token, Capacity: cfg.WorkerConcurrency, State: "ADB_CONNECTED", Transport: "usb_adb"}
		if previous != nil {
			n = *previous
			n.Token = approved.Token
			n.Capacity = cfg.WorkerConcurrency
		}
		if approved.Mock {
			n.Endpoint = approved.Endpoint
			n.Transport = "mock_loopback"
		} else if connected[approved.Serial] != "device" {
			n.State = "DISCONNECTED"
			if connected[approved.Serial] == "unauthorized" {
				n.State = "ADB_UNAUTHORIZED"
			}
			h.publishNode(n)
			continue
		} else if n.Endpoint == "" {
			port, e := transport.EnsureForward(ctx, adb, approved.Serial, approved.Port)
			if e != nil {
				n.State = "DEGRADED"
				n.Error = e.Error()
				h.publishNode(n)
				continue
			}
			n.Endpoint = "http://127.0.0.1:" + port
		}
		var caps protocol.CapabilitySet
		started := time.Now()
		e := transport.Request(ctx, n.Endpoint, n.Token, "GET", "/v1/capabilities", nil, &caps)
		if e != nil {
			n.State = "DEGRADED"
			n.Error = e.Error()
			if !approved.Mock {
				n.Endpoint = "" // the next refresh repairs a stale ADB forward
				if approved.Launcher == "adb-shell" {
					// Idempotent: the launcher exits if the worker is running.
					if time.Since(h.restart[approved.Serial]) > 20*time.Second {
						transport.ADB(ctx, adb, approved.Serial, "shell", "sh /data/local/tmp/tidalbridge/start-worker.sh")
						h.restart[approved.Serial] = time.Now()
					}
				} else if time.Since(h.restart[approved.Serial]) > time.Minute {
					transport.ADB(ctx, adb, approved.Serial, "shell", "am", "start", "-n", "com.termux/.app.TermuxActivity")
					h.restart[approved.Serial] = time.Now()
				}
			}
			h.publishNode(n)
			continue
		}
		if caps.ProtocolVersion != protocol.Version || !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,96}$`).MatchString(caps.StableID) || caps.Simulated != approved.Mock {
			n.State = "DEGRADED"
			n.Error = "Worker identity/protocol/mock designation mismatch"
			h.publishNode(n)
			continue
		}
		rttMS := float64(time.Since(started).Microseconds()) / 1000
		prev := n.Profile.Capabilities
		n.ID = caps.StableID
		n.Error = ""
		n.Profile.Capabilities = caps
		n.Profile.LastSeen = time.Now()
		n.Profile.SchemaVersion = 1
		if !approved.Mock {
			if caps.Features == nil {
				caps.Features = map[string]bool{}
			}
			// Each probe spawns an adb process on the host and dumpsys on the
			// device: browser every 5 minutes, power/thermal every 2 minutes
			// (30 seconds while the worker is busy).
			if time.Since(h.probedBrowser[approved.Serial]) > 5*time.Minute {
				caps.Features["browser_qa"] = browser.Socket(ctx, adb, approved.Serial) != ""
				h.probedBrowser[approved.Serial] = time.Now()
			} else {
				caps.Features["browser_qa"] = prev.Features["browser_qa"]
			}
			interval := 2 * time.Minute
			if n.ActiveJobs > 0 {
				interval = 30 * time.Second
			}
			probePower := time.Since(h.probedPower[approved.Serial]) > interval || prev.Resources.Thermal == ""
			if !probePower {
				caps.Resources.BatteryPercent = prev.Resources.BatteryPercent
				caps.Resources.Charging = prev.Resources.Charging
				caps.Resources.Thermal = prev.Resources.Thermal
				caps.Resources.InUse = prev.Resources.InUse
			}
			if caps.AndroidVersion == "" || caps.Model == "" {
				properties, _ := transport.ADB(ctx, adb, approved.Serial, "shell", "getprop")
				props := propMap(properties)
				caps.Model = props["ro.product.model"]
				caps.Manufacturer = props["ro.product.manufacturer"]
				caps.AndroidVersion = props["ro.build.version.release"]
				caps.APILevel, _ = strconv.Atoi(props["ro.build.version.sdk"])
				caps.ABIs = strings.Split(props["ro.product.cpu.abilist"], ",")
			}
			if probePower {
				// One adb round trip for both power and thermal state.
				state, _ := transport.ADB(ctx, adb, approved.Serial, "shell", vitalsCommand)
				h.applyVitals(state, &caps.Resources, &n)
				h.probedPower[approved.Serial] = time.Now()
			}
			n.Profile.Capabilities = caps
		}
		fingerprintBytes, _ := json.Marshal([]any{caps.WorkerVersion, caps.AndroidVersion, caps.Architecture, caps.APILevel, caps.Runtimes})
		fingerprint := sha256.Sum256(fingerprintBytes)
		fp := hex.EncodeToString(fingerprint[:])
		profilePath := filepath.Join(h.Dir, "profiles", n.ID+".json")
		if n.Profile.Calibration.Fingerprint == "" {
			var saved protocol.DeviceProfile
			if b, e := os.ReadFile(profilePath); e == nil {
				json.Unmarshal(b, &saved)
				n.Profile.Calibration = saved.Calibration
			}
		}
		if n.Profile.Calibration.Fingerprint != fp {
			var cal protocol.Calibration
			if e := transport.Request(ctx, n.Endpoint, n.Token, "POST", "/v1/calibrate", map[string]any{}, &cal); e == nil {
				cal.Fingerprint = fp
				cal.RTTMS = rttMS
				tmp, e := os.CreateTemp(h.Dir, "calibration-*.bin")
				if e == nil {
					data := make([]byte, 1<<20)
					for i := range data {
						data[i] = byte(i % 251)
					}
					tmp.Write(data)
					tmp.Close()
					hash := sha256.Sum256(data)
					t := time.Now()
					if transport.Upload(ctx, n.Endpoint, n.Token, hex.EncodeToString(hash[:]), tmp.Name(), int64(len(data))) == nil {
						cal.TransferMBPS = 1 / max(0.001, time.Since(t).Seconds())
					}
					os.Remove(tmp.Name())
				}
				n.Profile.Calibration = cal
			}
		}
		n.State = "READY"
		if n.ActiveJobs > 0 {
			n.State = "BUSY"
		}
		if caps.Resources.Thermal == "severe" || caps.Resources.Thermal == "critical" || caps.Resources.Thermal == "emergency" || caps.Resources.Thermal == "shutdown" {
			n.State = "THERMALLY_LIMITED"
		}
		if caps.Resources.RAMAvailableMB < cfg.ReserveRAMMB {
			n.State = "RESOURCE_LIMITED"
		}
		config.SaveJSON(profilePath, n.Profile)
		h.publishNode(n)
	}
	h.signal()
}
func (h *Host) publishNode(n protocol.WorkerNode) {
	h.workerChanges.Add(1)
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, existing := range h.nodes {
		if existing.Serial == n.Serial {
			n.ActiveJobs = existing.ActiveJobs
			n.Draining = existing.Draining
			if id != n.ID {
				delete(h.nodes, id)
			}
			if existing.State != n.State {
				h.log.Info("device_transition", "device", n.ID, "from", existing.State, "to", n.State)
			}
		}
	}
	h.nodes[n.ID] = &n
}
func (h *Host) Submit(spec protocol.JobSpec) (*protocol.Job, error) {
	if e := classifier.Normalize(&spec); e != nil {
		return nil, e
	}
	if spec.Workspace != "" {
		abs, e := filepath.Abs(spec.Workspace)
		if e != nil {
			return nil, e
		}
		info, e := os.Stat(abs)
		if e != nil || !info.IsDir() {
			return nil, fmt.Errorf("invalid workspace directory")
		}
		spec.Workspace = abs
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pending) >= h.cfg.QueueSize {
		return nil, fmt.Errorf("scheduler queue is full; retry when capacity is available")
	}
	j := &protocol.Job{ID: config.Random(), Spec: spec, State: "QUEUED", Created: time.Now(), Attempts: []protocol.Attempt{}}
	h.jobs[j.ID] = j
	h.pending = append(h.pending, j.ID)
	h.seen[j.ID] = time.Now()
	h.save(j)
	h.log.Info("job_submitted", "job", j.ID, "argv", spec.Argv, "workspace", spec.Workspace)
	h.signal()
	copy := *j
	return &copy, nil
}
func (h *Host) input(spec protocol.JobSpec, manifest *protocol.Manifest) scheduler.Input {
	h.mu.RLock()
	in := scheduler.Input{Spec: spec, Config: h.cfg, Host: h.monitor.Snapshot().Resources, History: append([]protocol.HistorySample(nil), h.history...), MissingBytes: map[string]int64{}, EnvironmentWarm: map[string]bool{}, LocalBusy: h.localActive >= h.cfg.LocalConcurrency}
	for _, n := range h.nodes {
		in.Nodes = append(in.Nodes, *n)
	}
	in.Quarantine = h.quarantineLocked(verifySignature(spec))
	h.mu.RUnlock()
	if spec.Service {
		for _, port := range spec.Ports {
			if !PortFree(port) {
				in.PortsBusy = append(in.PortsBusy, port)
			}
		}
	}
	if manifest == nil {
		return in
	}
	key := treeKey(spec.Workspace)
	for _, n := range in.Nodes {
		in.MissingBytes[n.ID] = manifest.TotalBytes
		if n.State != "READY" && n.State != "BUSY" {
			continue
		}
		// Only content this host has not yet confirmed on the worker is
		// queried; after the first sync this is usually nothing.
		ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
		if bytes, e := h.missingBytes(ctx, n, manifest); e == nil {
			in.MissingBytes[n.ID] = bytes
		}
		cancel()
		if spec.Policy.Provision {
			var environment struct {
				Ready bool `json:"ready"`
			}
			queryCtx, cancelQuery := context.WithTimeout(h.ctx, 3*time.Second)
			if h.call(queryCtx, n, "POST", "/v1/environments/status", map[string]any{"workspace_key": key, "working_directory": spec.WorkingDirectory, "runtime": runtimeOf(spec), "ecosystem": ecosystemOf(spec)}, &environment) == nil {
				in.EnvironmentWarm[n.ID] = environment.Ready
			}
			cancelQuery()
			if !in.EnvironmentWarm[n.ID] && !spec.Policy.ForceRemote {
				h.prepareInBackground(n, spec)
			}
		}
	}
	return in
}

// ecosystemOf names the locked environment a command needs where a project
// directory has both a Node and a Python lock file.
func ecosystemOf(spec protocol.JobSpec) string {
	if len(spec.Argv) > 0 {
		switch strings.TrimSuffix(strings.ToLower(filepath.Base(spec.Argv[0])), ".exe") {
		case "python", "python3", "pytest", "ruff", "mypy", "uv":
			return "python"
		}
	}
	return "node"
}

func runtimeOf(spec protocol.JobSpec) string {
	if spec.Runtime == "" {
		return "termux"
	}
	return spec.Runtime
}

// prepareInBackground installs a project's locked dependencies on a worker
// once, without making the command that noticed the cold environment wait:
// that command runs locally, later ones find the environment warm.
func (h *Host) prepareInBackground(node protocol.WorkerNode, spec protocol.JobSpec) {
	key := node.ID + "|" + treeKey(spec.Workspace) + "|" + spec.WorkingDirectory + "|" + runtimeOf(spec) + "|" + ecosystemOf(spec)
	h.prepareMu.Lock()
	if started, ok := h.preparing[key]; ok && time.Since(started) < 30*time.Minute {
		h.prepareMu.Unlock()
		return
	}
	h.preparing[key] = time.Now()
	h.prepareMu.Unlock()
	prepare := protocol.JobSpec{Argv: []string{"sh", "-c", "true"}, Env: map[string]string{"TIDALBRIDGE_ECOSYSTEM": ecosystemOf(spec)}, Workspace: spec.Workspace, WorkingDirectory: spec.WorkingDirectory, Runtime: spec.Runtime,
		TimeoutSeconds: 3600, EstimatedDurationMS: 1000, Profile: "prepare:" + spec.Profile,
		Requirements: protocol.Requirements{Architecture: "any", Runtimes: map[string]string{"sh": "*"}},
		Policy:       protocol.Policy{ForceRemote: true, DeviceID: node.ID, Provision: true, ProvisionScripts: spec.Policy.ProvisionScripts, SyncEnvFiles: spec.Policy.SyncEnvFiles, Idempotent: true, Retryable: true}}
	go func() {
		if _, err := h.Submit(prepare); err != nil {
			h.log.Error("prepare_submit", "error", err.Error())
			h.prepareMu.Lock()
			delete(h.preparing, key)
			h.prepareMu.Unlock()
		} else {
			h.log.Info("prepare_environment", "device", node.ID, "workspace", spec.Workspace, "runtime", runtimeOf(spec))
		}
	}()
}

func (h *Host) Explain(ctx context.Context, spec protocol.JobSpec) (protocol.Decision, error) {
	if e := classifier.Normalize(&spec); e != nil {
		return protocol.Decision{}, e
	}
	wait := 2500 * time.Millisecond
	if spec.Policy.ForceRemote {
		// A required remote run waits for a first index instead of failing.
		wait = time.Minute
	}
	m, e := h.manifestWithin(ctx, spec, wait)
	if errors.Is(e, errIndexing) {
		// The command runs locally this time; the index keeps building.
		return protocol.Decision{Target: "LOCAL", Explanation: "Indexing this workspace for the first time; running locally meanwhile."}, nil
	}
	if e != nil {
		return protocol.Decision{}, e
	}
	in := h.input(spec, m)
	d := scheduler.Route(in)
	if d.Target != "LOCAL" && d.Target != "REJECT" && d.Target != "WAIT" {
		d.Confirm = h.needsConfirmation(spec)
		if spec.Shard != "" && !d.Confirm && !spec.Policy.ForceRemote {
			h.split(in, &d)
		}
	}
	return d, nil
}
func (h *Host) dispatch(ctx context.Context) {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.wake:
		case <-timer.C:
		}
		h.mu.RLock()
		pending := append([]string(nil), h.pending...)
		h.mu.RUnlock()
		for _, id := range pending {
			h.mu.RLock()
			j := h.jobs[id]
			if j == nil || j.State == "CANCELLED" {
				h.mu.RUnlock()
				h.removePending(id)
				continue
			}
			spec := j.Spec
			full := h.totalActive >= h.cfg.MaxConcurrency && !spec.Service
			h.mu.RUnlock()
			if full {
				break
			}
			manifest, e := h.manifestWithin(ctx, spec, 0)
			if errors.Is(e, errIndexing) {
				h.mu.Lock()
				j.State = "WAIT"
				j.Decision = protocol.Decision{Target: "WAIT", Explanation: "Indexing the workspace for the first time."}
				h.mu.Unlock()
				continue
			}
			if e != nil {
				h.fail(id, e)
				h.removePending(id)
				continue
			}
			d := scheduler.Route(h.input(spec, manifest))
			h.mu.Lock()
			j.Decision = d
			if d.Target == "WAIT" && d.Explanation == scheduler.RecoveringExplanation && time.Since(j.Created) > 15*time.Second {
				// A worker that does not come back must not hold work forever.
				d.Target = "REJECT"
				d.Explanation = "The selected worker did not recover; no compatible, safe remote worker is available."
				j.Decision = d
			}
			if d.Target == "WAIT" {
				j.State = "WAIT"
				h.mu.Unlock()
				continue
			}
			if d.Target == "REJECT" {
				j.State = "FAILED"
				j.Error = d.Explanation
				now := time.Now()
				j.Finished = &now
				h.save(j)
				h.mu.Unlock()
				h.removePending(id)
				continue
			}
			if j.State == "CANCELLED" {
				h.mu.Unlock()
				h.removePending(id)
				continue
			}
			j.State = "RUNNING"
			// Services (dev servers) run for hours; they are bounded by the
			// worker's own service limit, not by batch execution slots.
			if !spec.Service {
				h.totalActive++
			}
			var node *protocol.WorkerNode
			if d.Target == "LOCAL" {
				h.localActive++
			} else {
				n := h.nodes[d.Target]
				if !spec.Service {
					n.ActiveJobs++
					n.State = "BUSY"
				}
				copy := *n
				node = &copy
			}
			attempt := protocol.Attempt{ID: config.Random(), Target: d.Target, Started: time.Now()}
			for _, candidate := range d.Candidates {
				if candidate.Target == d.Target {
					attempt.EnvironmentWarm = candidate.EnvironmentWarm
				}
			}
			j.Attempts = append(j.Attempts, attempt)
			// The worker lets a run that is still printing go past its limit,
			// up to three times it (SILENT_LIMIT and OVERTIME in worker.py).
			jobCtx, cancel := context.WithTimeout(ctx, time.Duration(spec.TimeoutSeconds)*time.Second*overtime+time.Minute)
			h.cancel[id] = cancel
			h.save(j)
			h.mu.Unlock()
			h.removePending(id)
			h.running.Add(1)
			go h.execute(jobCtx, id, node, manifest)
		}
	}
}

// overtime: how far past its limit a worker lets a run that is still printing
// go (OVERTIME in worker.py).
const overtime = 3

func (h *Host) removePending(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, v := range h.pending {
		if v == id {
			h.pending = append(h.pending[:i], h.pending[i+1:]...)
			return
		}
	}
}
func (h *Host) fail(id string, e error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	j := h.jobs[id]
	j.State = "FAILED"
	j.Error = e.Error()
	now := time.Now()
	j.Finished = &now
	h.save(j)
}

type boundedLog struct {
	mu        sync.Mutex
	file      *os.File
	text      strings.Builder
	count     int64
	truncated bool
}

func (b *boundedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.count += int64(n)
	if b.file != nil && b.count <= 64<<20 {
		if _, e := b.file.Write(p); e != nil {
			return 0, e
		}
	}
	space := (128 << 10) - b.text.Len()
	if space > 0 {
		b.text.Write(p[:min(len(p), space)])
	}
	if n > space {
		b.truncated = true
	}
	return n, nil
}
func (h *Host) execute(ctx context.Context, id string, node *protocol.WorkerNode, manifest *protocol.Manifest) {
	defer h.running.Done()
	h.mu.RLock()
	spec := h.jobs[id].Spec
	attempt := h.jobs[id].Attempts[len(h.jobs[id].Attempts)-1]
	h.mu.RUnlock()
	dir := filepath.Join(h.Dir, "results", id, attempt.ID)
	os.MkdirAll(dir, 0700)
	stdoutFile, _ := os.Create(filepath.Join(dir, "stdout.log"))
	stderrFile, _ := os.Create(filepath.Join(dir, "stderr.log"))
	out := &boundedLog{file: stdoutFile}
	errout := &boundedLog{file: stderrFile}
	defer func() {
		if stdoutFile != nil {
			stdoutFile.Close()
		}
		if stderrFile != nil {
			stderrFile.Close()
		}
	}()
	code := 0
	var runErr error
	retry := false
	if spec.Render != nil && node != nil {
		runErr = browser.Capture(ctx, transport.FindADB(h.Config().ADB), *node, *spec.Render, filepath.Join(dir, "artifacts"))
		fmt.Fprintln(out, "Android Chrome viewport capture completed; artifacts:", filepath.Join(dir, "artifacts"))
	} else if node == nil {
		code, runErr = runLocal(ctx, spec, out, errout)
	} else {
		code, attempt, runErr, retry = h.runRemote(ctx, id, spec, *node, manifest, attempt, out, errout)
	}
	attempt.DurationMS = float64(time.Since(attempt.Started).Microseconds()) / 1000
	if runErr != nil {
		attempt.Error = runErr.Error()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	j := h.jobs[id]
	j.Attempts[len(j.Attempts)-1] = attempt
	if !spec.Service {
		h.totalActive--
	}
	if node == nil {
		h.localActive--
	} else {
		if n := h.nodes[node.ID]; n != nil {
			if !spec.Service {
				n.ActiveJobs = max(0, n.ActiveJobs-1)
			}
			if retry && ctx.Err() == nil {
				n.State = "DEGRADED"
				n.Error = attempt.Error
			} else if n.State == "BUSY" && n.ActiveJobs == 0 {
				n.State = "READY"
			}
		}
	}
	endedErr := ctx.Err()
	if cancel := h.cancel[id]; cancel != nil {
		cancel()
		delete(h.cancel, id)
	}
	j.Stdout = out.text.String()
	j.Stderr = errout.text.String()
	j.OutputBytes = out.count + errout.count
	j.Truncated = out.truncated || errout.truncated
	if j.State == "CANCELLED" {
		j.Error = "Cancelled by user."
	} else if endedErr != nil {
		j.State = "FAILED"
		j.Error = "Job deadline/interruption: " + endedErr.Error()
	} else if runErr != nil && retry && spec.Policy.Idempotent && spec.Policy.Retryable && !spec.Policy.Destructive && len(j.Attempts) < 3 {
		j.State = "QUEUED"
		j.Error = "Attempt interrupted: " + runErr.Error()
		if spec.Policy.LocalFallback {
			j.Spec.Policy.ForceRemote = false
			j.Spec.Policy.ForceLocal = true
		}
		h.pending = append(h.pending, id)
		h.save(j)
		h.signal()
		return
	} else if runErr != nil {
		j.State = "FAILED"
		j.Error = runErr.Error()
	} else {
		j.ExitCode = &code
		j.State = "COMPLETED"
		if code != 0 {
			j.State = "FAILED"
		}
		j.Error = ""
	}
	now := time.Now()
	j.Finished = &now
	h.save(j)
	fingerprint := ""
	if node != nil {
		fingerprint = node.Profile.Calibration.Fingerprint
	}
	if spec.Service {
		// A service's lifetime says nothing about job durations.
		h.log.Info("job_finished", "job", id, "target", attempt.Target, "state", j.State, "duration_ms", attempt.DurationMS, "error", j.Error)
		h.signal()
		return
	}
	sample := protocol.HistorySample{Signature: scheduler.Signature(spec), Target: attempt.Target, DurationMS: max(1, attempt.DurationMS-attempt.SyncMS), CacheWarm: node == nil || !spec.Policy.Provision || attempt.EnvironmentWarm, Success: j.State == "COMPLETED", Timestamp: now, DeviceFingerprint: fingerprint}
	if attempt.WorkerCPUSeconds != nil {
		sample.WorkerCPUSeconds = *attempt.WorkerCPUSeconds
	}
	if attempt.WorkerPeakRAMMB != nil {
		sample.WorkerPeakRAMMB = *attempt.WorkerPeakRAMMB
	}
	h.history = append(h.history, sample)
	if len(h.history) > 512 {
		h.history = h.history[len(h.history)-512:]
	}
	config.SaveJSON(filepath.Join(h.Dir, "history.json"), h.history)
	h.log.Info("job_finished", "job", id, "target", attempt.Target, "state", j.State, "duration_ms", attempt.DurationMS, "error", j.Error)
	if len(h.jobs) > 128 {
		var oldest string
		var timeOld time.Time
		for k, v := range h.jobs {
			if v.Finished != nil && (oldest == "" || v.Created.Before(timeOld)) {
				oldest = k
				timeOld = v.Created
			}
		}
		delete(h.jobs, oldest)
		delete(h.seen, oldest)
	}
	h.signal()
}
func runLocal(ctx context.Context, s protocol.JobSpec, out, errout io.Writer) (int, error) {
	argv := append([]string(nil), s.Argv...)
	if len(s.LocalArgv) > 0 {
		argv = append([]string(nil), s.LocalArgv...)
	}
	if runtime.GOOS == "windows" {
		if p, e := exec.LookPath(argv[0]); e == nil && (strings.HasSuffix(strings.ToLower(p), ".cmd") || strings.HasSuffix(strings.ToLower(p), ".bat")) {
			name := strings.TrimSuffix(strings.ToLower(filepath.Base(p)), ".cmd")
			if name == "npm" || name == "npx" {
				script := filepath.Join(filepath.Dir(p), "node_modules", "npm", "bin", name+"-cli.js")
				if _, e := os.Stat(script); e != nil {
					return 0, e
				}
				argv = append([]string{"node", script}, argv[1:]...)
			} else {
				return 0, fmt.Errorf("Windows command scripts require an explicit cmd /c job; shell quoting is not inferred")
			}
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = s.Workspace
	if s.WorkingDirectory != "" {
		cmd.Dir = filepath.Join(s.Workspace, s.WorkingDirectory)
	}
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "TIDALBRIDGE_INTERNAL=1")
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = out
	cmd.Stderr = errout
	prepareProcess(cmd)
	e := cmd.Run()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if exit, ok := e.(*exec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	return 0, e
}
func (h *Host) Job(id string) (protocol.Job, error) {
	h.mu.RLock()
	if j := h.jobs[id]; j != nil {
		copy := *j
		copy.Attempts = append([]protocol.Attempt(nil), j.Attempts...)
		h.mu.RUnlock()
		return copy, nil
	}
	h.mu.RUnlock()
	if !regexp.MustCompile(`^[a-f0-9]{48}$`).MatchString(id) {
		return protocol.Job{}, fmt.Errorf("invalid job ID")
	}
	var j protocol.Job
	b, e := os.ReadFile(filepath.Join(h.Dir, "jobs", id+".json"))
	if e == nil {
		e = json.Unmarshal(b, &j)
	}
	return j, e
}

// evacuate stops the jobs of a critically hot worker; their adapters run them
// on the laptop instead (dev servers through failover).
func (h *Host) evacuate(nodeID string) {
	h.mu.Lock()
	var ids []string
	for id, j := range h.jobs {
		if j.Finished == nil && len(j.Attempts) > 0 && j.Attempts[len(j.Attempts)-1].Target == nodeID {
			j.Evacuated = true
			j.Error = "The phone overheated; the job was stopped there to run on the laptop."
			ids = append(ids, id)
		}
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.Cancel(id)
		h.log.Warn("evacuated", "job", id, "device", nodeID)
	}
}

func (h *Host) Cancel(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	j := h.jobs[id]
	if j == nil {
		return fmt.Errorf("active job not found")
	}
	if j.Finished != nil {
		return nil
	}
	j.State = "CANCELLED"
	now := time.Now()
	j.Finished = &now
	if cancel := h.cancel[id]; cancel != nil {
		cancel()
	}
	h.save(j)
	h.signal()
	return nil
}
func (h *Host) Handler(dashboard http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", dashboard)
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.Token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+h.Config().Bind {
			http.Error(w, "cross-origin requests are denied", 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
		send := func(v any) { json.NewEncoder(w).Encode(v) }
		bad := func(e error) { w.WriteHeader(400); send(map[string]string{"error": e.Error()}) }
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/health":
			send(map[string]any{"product": "Tidal Bridge", "protocol_version": protocol.Version, "version": protocol.WorkerVersion})
		case r.Method == "GET" && r.URL.Path == "/v1/status":
			send(h.Status())
		case r.Method == "GET" && r.URL.Path == "/v1/automation":
			send(h.AutomationStatus())
		case r.Method == "POST" && r.URL.Path == "/v1/automation/settings":
			var input SettingsInput
			if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
				bad(e)
				return
			}
			if e := h.UpdateSettings(input); e != nil {
				bad(e)
				return
			}
			send(h.AutomationStatus())
		case r.Method == "POST" && r.URL.Path == "/v1/automation":
			var input AutomationInput
			if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
				bad(e)
				return
			}
			if e := h.ConfigureAutomation(input); e != nil {
				bad(e)
				return
			}
			send(h.AutomationStatus())
		case r.Method == "POST" && r.URL.Path == "/v1/observations/suite":
			var input protocol.SuiteObservation
			if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
				bad(e)
				return
			}
			if e := h.RecordSuite(input); e != nil {
				bad(e)
				return
			}
			send(map[string]bool{"recorded": true})
		case r.Method == "POST" && r.URL.Path == "/v1/observations/confirmation":
			var input protocol.Confirmation
			if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
				bad(e)
				return
			}
			if e := h.RecordConfirmation(input); e != nil {
				bad(e)
				return
			}
			send(map[string]bool{"recorded": true})
		case r.Method == "POST" && r.URL.Path == "/v1/observations/local":
			var input protocol.LocalObservation
			if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
				bad(e)
				return
			}
			job, e := h.RecordLocal(input)
			if e != nil {
				bad(e)
				return
			}
			send(job)
		case r.Method == "POST" && r.URL.Path == "/v1/observations/adapter":
			var input protocol.AdapterObservation
			if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
				bad(e)
				return
			}
			if e := h.RecordAdapter(input); e != nil {
				bad(e)
				return
			}
			send(map[string]bool{"recorded": true})
		case r.Method == "GET" && r.URL.Path == "/v1/events":
			// A small pulse every second (laptop load, sampled faster while a
			// dashboard is open) and the full status only when it changed.
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			sent, sentWorkers, lastFull := ^uint64(0), uint64(0), time.Time{}
			for {
				h.monitor.Boost(3 * time.Second)
				h.watchedUntil.Store(time.Now().Add(5 * time.Second).UnixNano())
				if v := h.changes.Load(); v != sent || time.Since(lastFull) > 5*time.Minute {
					sentWorkers = h.workerChanges.Load()
					b, _ := json.Marshal(h.Status())
					fmt.Fprintf(w, "event: status\ndata: %s\n\n", b)
					sent, lastFull = v, time.Now()
				} else if v := h.workerChanges.Load(); v != sentWorkers {
					sentWorkers = v
					h.mu.RLock()
					b, _ := json.Marshal(map[string]any{"workers": h.workersLocked()})
					h.mu.RUnlock()
					fmt.Fprintf(w, "event: workers\ndata: %s\n\n", b)
				}
				snapshot := h.monitor.Snapshot()
				pulse := map[string]any{"host": snapshot, "now": time.Now()}
				if c := h.carrying(snapshot.Resources.LogicalCores); c != nil {
					pulse["carrying"] = c
				}
				b, _ := json.Marshal(pulse)
				fmt.Fprintf(w, "event: pulse\ndata: %s\n\n", b)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
				}
			}
		case r.Method == "POST" && r.URL.Path == "/v1/jobs":
			var spec protocol.JobSpec
			if e := json.NewDecoder(r.Body).Decode(&spec); e != nil {
				bad(e)
				return
			}
			j, e := h.Submit(spec)
			if e != nil {
				bad(e)
				return
			}
			w.WriteHeader(202)
			send(j)
		case r.Method == "POST" && r.URL.Path == "/v1/explain":
			var spec protocol.JobSpec
			if e := json.NewDecoder(r.Body).Decode(&spec); e != nil {
				bad(e)
				return
			}
			d, e := h.Explain(r.Context(), spec)
			if e != nil {
				bad(e)
				return
			}
			send(d)
		case r.Method == "POST" && r.URL.Path == "/v1/sync":
			var input struct {
				Workspace string `json:"workspace"`
				DeviceID  string `json:"device_id"`
			}
			if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
				bad(e)
				return
			}
			value, e := h.Sync(r.Context(), input.Workspace, input.DeviceID)
			if e != nil {
				bad(e)
				return
			}
			send(value)
		case strings.HasPrefix(r.URL.Path, "/v1/jobs/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
			if r.Method == "GET" {
				h.touch(strings.TrimSuffix(id, "/output"))
			}
			if strings.HasSuffix(id, "/output") && r.Method == "GET" {
				id = strings.TrimSuffix(id, "/output")
				j, e := h.Job(id)
				if e != nil {
					bad(e)
					return
				}
				if len(j.Attempts) == 0 {
					send(map[string]any{"data_b64": "", "next_offset": 0})
					return
				}
				stream := r.URL.Query().Get("stream")
				if stream != "stdout" && stream != "stderr" {
					bad(fmt.Errorf("invalid output stream"))
					return
				}
				offset, e := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
				if e != nil || offset < 0 {
					bad(fmt.Errorf("invalid output offset"))
					return
				}
				path := filepath.Join(h.Dir, "results", id, j.Attempts[len(j.Attempts)-1].ID, stream+".log")
				f, e := os.Open(path)
				if os.IsNotExist(e) {
					send(map[string]any{"data_b64": "", "next_offset": offset})
					return
				}
				if e != nil {
					bad(e)
					return
				}
				defer f.Close()
				if _, e = f.Seek(offset, 0); e != nil {
					bad(e)
					return
				}
				b := make([]byte, 65536)
				n, e := f.Read(b)
				if e != nil && e != io.EOF {
					bad(e)
					return
				}
				send(map[string]any{"data_b64": base64.StdEncoding.EncodeToString(b[:n]), "next_offset": offset + int64(n)})
				return
			}
			if strings.HasSuffix(id, "/cancel") && r.Method == "POST" {
				id = strings.TrimSuffix(id, "/cancel")
				if e := h.Cancel(id); e != nil {
					bad(e)
					return
				}
				send(map[string]bool{"cancelled": true})
				return
			}
			if r.Method != "GET" {
				w.WriteHeader(405)
				return
			}
			j, e := h.Job(id)
			if e != nil {
				w.WriteHeader(404)
				send(map[string]string{"error": e.Error()})
				return
			}
			h.mu.RLock()
			j.Spared = h.sparedLocked(&j)
			h.mu.RUnlock()
			send(j)
		case r.Method == "POST" && r.URL.Path == "/v1/settings":
			var s struct {
				Mode              *string `json:"mode"`
				Paused            *bool   `json:"paused"`
				WorkerConcurrency *int    `json:"worker_concurrency"`
				FixedCapacity     *bool   `json:"fixed_capacity"`
			}
			if e := json.NewDecoder(r.Body).Decode(&s); e != nil {
				bad(e)
				return
			}
			h.mu.Lock()
			c := h.cfg
			if s.Mode != nil {
				c.Mode = *s.Mode
			}
			if s.Paused != nil {
				c.Paused = *s.Paused
			}
			if s.WorkerConcurrency != nil {
				if *s.WorkerConcurrency < 1 || *s.WorkerConcurrency > 8 {
					h.mu.Unlock()
					bad(fmt.Errorf("jobs at once must be 1 to 8"))
					return
				}
				c.WorkerConcurrency = *s.WorkerConcurrency
				for _, n := range h.nodes {
					n.Capacity = c.WorkerConcurrency
				}
			}
			if s.FixedCapacity != nil {
				c.FixedCapacity = *s.FixedCapacity
			}
			if e := config.Save(h.Dir, c); e != nil {
				h.mu.Unlock()
				bad(e)
				return
			}
			h.cfg = c
			h.mu.Unlock()
			h.signal()
			send(c)
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/devices/") && strings.HasSuffix(r.URL.Path, "/drain"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/devices/"), "/drain")
			var input struct {
				Draining bool `json:"draining"`
			}
			if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
				bad(e)
				return
			}
			h.mu.Lock()
			n := h.nodes[id]
			if n != nil {
				n.Draining = input.Draining
			}
			h.mu.Unlock()
			if n == nil {
				bad(fmt.Errorf("unknown device"))
				return
			}
			send(map[string]bool{"draining": input.Draining})
		case r.Method == "POST" && r.URL.Path == "/v1/refresh":
			// An explicit refresh reads battery, heat and screen afresh.
			h.refreshMu.Lock()
			h.probedPower = map[string]time.Time{}
			h.refreshMu.Unlock()
			h.signal()
			go h.refresh(h.ctx)
			send(map[string]bool{"scheduled": true})
		default:
			w.WriteHeader(404)
			send(map[string]string{"error": "unknown endpoint"})
		}
	})
	return mux
}

const vitalsCommand = "dumpsys battery; dumpsys thermalservice | grep -m1 'Thermal Status'; dumpsys power | grep -E 'mWakefulness=|mStayOn='"

var (
	batteryLevel  = regexp.MustCompile(`level: (\d+)`)
	thermalStatus = regexp.MustCompile(`Thermal Status: (\d+)`)
)

// applyVitals reads battery, charging, screen and heat from vitalsCommand's
// output. A phone that overheated cools down before taking work again, and
// one that is critically hot gives its running jobs back to the laptop.
func (h *Host) applyVitals(state string, r *protocol.Resources, n *protocol.WorkerNode) {
	if m := batteryLevel.FindStringSubmatch(state); len(m) > 0 {
		v, _ := strconv.Atoi(m[1])
		r.BatteryPercent = &v
	}
	if strings.Contains(state, "powered: ") {
		charging := strings.Contains(state, "USB powered: true") || strings.Contains(state, "AC powered: true") || strings.Contains(state, "Wireless powered: true")
		r.Charging = &charging
	}
	if strings.Contains(state, "mWakefulness=") {
		inUse := strings.Contains(state, "mWakefulness=Awake") && !strings.Contains(state, "mStayOn=true")
		r.InUse = &inUse
	}
	if m := thermalStatus.FindStringSubmatch(state); len(m) > 0 {
		v, _ := strconv.Atoi(m[1])
		levels := []string{"nominal", "warm", "moderate", "severe", "critical", "emergency", "shutdown"}
		if v < len(levels) {
			r.Thermal = levels[v]
		}
		if v >= 3 {
			until := time.Now().Add(5 * time.Minute)
			n.CoolingUntil = &until
		}
		if v >= 4 && n.ActiveJobs > 0 {
			go h.evacuate(n.ID)
		}
	}
}

// watchPhones keeps phone readings current between full refreshes. Every 15
// seconds it reads the screen state (one small adb call); while a dashboard
// is open it also reads battery and heat in that call and memory and storage
// from the worker, and the status changes only when a reading does.
func (h *Host) watchPhones(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		watched := time.Now().UnixNano() < h.watchedUntil.Load()
		h.mu.RLock()
		var nodes []protocol.WorkerNode
		for _, n := range h.nodes {
			if n.State == "READY" || n.State == "BUSY" {
				nodes = append(nodes, *n)
			}
		}
		h.mu.RUnlock()
		for _, n := range nodes {
			adb, serial, mock := h.adbTarget(n)
			if mock || adb == "" {
				continue
			}
			command := "dumpsys power | grep -E 'mWakefulness=|mStayOn='"
			if watched {
				command = vitalsCommand
			}
			probe, cancel := context.WithTimeout(ctx, 5*time.Second)
			state, err := transport.ADB(probe, adb, serial, "shell", command)
			var caps protocol.CapabilitySet
			fresh := watched && n.Endpoint != "" && transport.Request(probe, n.Endpoint, n.Token, "GET", "/v1/capabilities", nil, &caps) == nil
			cancel()
			h.mu.Lock()
			if node := h.nodes[n.ID]; node != nil {
				r := &node.Profile.Capabilities.Resources
				before := readings(*r, node.CoolingUntil)
				if err == nil {
					h.applyVitals(state, r, node)
				}
				// Memory and storage move all the time; only a clear change counts.
				if fresh && (caps.Resources.RAMAvailableMB+256 < r.RAMAvailableMB || r.RAMAvailableMB+256 < caps.Resources.RAMAvailableMB) {
					r.RAMAvailableMB = caps.Resources.RAMAvailableMB
				}
				if fresh && (caps.Resources.StorageAvailableMB+1024 < r.StorageAvailableMB || r.StorageAvailableMB+1024 < caps.Resources.StorageAvailableMB) {
					r.StorageAvailableMB = caps.Resources.StorageAvailableMB
				}
				if readings(*r, node.CoolingUntil) != before {
					h.workerChanges.Add(1)
				}
			}
			h.mu.Unlock()
		}
	}
}

// readings summarizes what the dashboard shows of a phone, coarse enough
// that small memory fluctuations do not resend the status.
func readings(r protocol.Resources, cooling *time.Time) string {
	value := func(p *int) int {
		if p == nil {
			return -1
		}
		return *p
	}
	flag := func(p *bool) int {
		if p == nil {
			return -1
		}
		if *p {
			return 1
		}
		return 0
	}
	return fmt.Sprint(value(r.BatteryPercent), flag(r.Charging), flag(r.InUse), r.Thermal, r.RAMAvailableMB, r.StorageAvailableMB, cooling != nil && time.Now().Before(*cooling))
}
