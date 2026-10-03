package protocol

import "time"

const Version = 1
const WorkerVersion = "0.2.1"

type Resources struct {
	CPUPercent         float64 `json:"cpu_percent"`
	RAMTotalMB         uint64  `json:"ram_total_mb"`
	RAMAvailableMB     uint64  `json:"ram_available_mb"`
	StorageAvailableMB uint64  `json:"storage_available_mb"`
	BatteryPercent     *int    `json:"battery_percent"`
	Charging           *bool   `json:"charging"`
	Thermal            string  `json:"thermal"`
	LogicalCores       int     `json:"logical_cores"`
	// InUse: the phone's screen is on without "stay awake", so someone is
	// probably using it.
	InUse *bool `json:"in_use,omitempty"`
	// RAMPhysicalMB: the memory the device was sold with; RAMTotalMB is
	// what its operating system can use (firmware and chips keep the rest).
	RAMPhysicalMB uint64 `json:"ram_physical_mb,omitempty"`
	// ProjectCopies: project copies a phone keeps; unused ones are removed.
	ProjectCopies int `json:"project_copies,omitempty"`
}
type CapabilitySet struct {
	ProtocolVersion int               `json:"protocol_version"`
	WorkerVersion   string            `json:"worker_version"`
	StableID        string            `json:"stable_id"`
	Model           string            `json:"model"`
	Manufacturer    string            `json:"manufacturer"`
	AndroidVersion  string            `json:"android_version"`
	APILevel        int               `json:"api_level"`
	Architecture    string            `json:"architecture"`
	ABIs            []string          `json:"abis"`
	Runtimes        map[string]string `json:"runtimes"`
	Features        map[string]bool   `json:"features"`
	Resources       Resources         `json:"resources"`
	Simulated       bool              `json:"simulated"`
}
type Calibration struct {
	Fingerprint  string    `json:"fingerprint"`
	Timestamp    time.Time `json:"timestamp"`
	PythonMS     float64   `json:"python_ms"`
	NodeMS       float64   `json:"node_ms"`
	HashMBPS     float64   `json:"hash_mbps"`
	RTTMS        float64   `json:"rtt_ms"`
	TransferMBPS float64   `json:"transfer_mbps"`
}
type DeviceProfile struct {
	SchemaVersion int           `json:"schema_version"`
	Capabilities  CapabilitySet `json:"capabilities"`
	Calibration   Calibration   `json:"calibration"`
	LastSeen      time.Time     `json:"last_seen"`
}
type WorkerNode struct {
	ID         string `json:"id"`
	Serial     string `json:"serial,omitempty"`
	State      string `json:"state"`
	Transport  string `json:"transport"`
	Endpoint   string `json:"-"`
	Token      string `json:"-"`
	Draining   bool   `json:"draining"`
	ActiveJobs int    `json:"active_jobs"`
	// CoolingUntil: after overheating the phone takes no new work until then.
	CoolingUntil *time.Time    `json:"cooling_until,omitempty"`
	Capacity     int           `json:"capacity"`
	Error        string        `json:"error,omitempty"`
	Profile      DeviceProfile `json:"profile"`
	// EffectiveCapacity and CapacityReason: how many jobs the worker takes
	// right now and why fewer than configured (computed for status only).
	EffectiveCapacity int    `json:"effective_capacity,omitempty"`
	CapacityReason    string `json:"capacity_reason,omitempty"`
}
type Requirements struct {
	Architecture string            `json:"architecture"`
	Runtimes     map[string]string `json:"runtimes"`
	RAMMB        uint64            `json:"ram_mb"`
	StorageMB    uint64            `json:"storage_mb"`
	HostOnly     bool              `json:"host_only"`
	Browser      bool              `json:"browser"`
}
type Policy struct {
	LocalFallback bool   `json:"local_fallback"`
	Idempotent    bool   `json:"idempotent"`
	Retryable     bool   `json:"retryable"`
	Destructive   bool   `json:"destructive"`
	ForceLocal    bool   `json:"force_local"`
	ForceRemote   bool   `json:"force_remote"`
	DeviceID      string `json:"device_id,omitempty"`
	Provision     bool   `json:"provision"`
	Network       string `json:"network"`
	// ProvisionScripts allows dependency install scripts (npm postinstall).
	ProvisionScripts bool `json:"provision_scripts,omitempty"`
	// SyncEnvFiles includes the project's .env files in the worker copy. Off
	// by default; key/credential files are excluded regardless.
	SyncEnvFiles bool `json:"sync_env_files,omitempty"`
	// WriteBack copies files the job changed in the worker's copy back into
	// the laptop workspace, unless one of them changed on the laptop meanwhile.
	WriteBack bool `json:"write_back,omitempty"`
}
type JobSpec struct {
	Render                 *RenderSpec       `json:"render,omitempty"`
	ProtocolVersion        int               `json:"protocol_version"`
	Argv                   []string          `json:"argv"`
	LocalArgv              []string          `json:"local_argv,omitempty"`
	Workspace              string            `json:"workspace,omitempty"`
	WorkingDirectory       string            `json:"working_directory,omitempty"`
	Env                    map[string]string `json:"env,omitempty"`
	TimeoutSeconds         int               `json:"timeout_seconds"`
	Requirements           Requirements      `json:"requirements"`
	Policy                 Policy            `json:"policy"`
	EstimatedDurationMS    float64           `json:"estimated_duration_ms"`
	ExpectedOutputs        []string          `json:"expected_outputs,omitempty"`
	Profile                string            `json:"profile,omitempty"`
	EnvironmentFingerprint string            `json:"environment_fingerprint,omitempty"`
	// Runtime selects the worker userland: "termux" (Android native) or
	// "debian" (glibc container). Empty means termux.
	Runtime string `json:"runtime,omitempty"`
	// Engine "native" lets a Debian worker run a Node tool without proot when
	// the command needs no shell; otherwise the job runs under proot.
	Engine string `json:"engine,omitempty"`
	// Service jobs (dev servers) run until cancelled; Ports are forwarded
	// from the host's loopback to the worker, ReversePorts the other way.
	Service      bool  `json:"service,omitempty"`
	Ports        []int `json:"ports,omitempty"`
	ReversePorts []int `json:"reverse_ports,omitempty"`
	// AttachedClient ties a service's lifetime to a polling client (a command
	// adapter in a terminal). If the client disappears without cancelling,
	// the host stops the service after a short lease.
	AttachedClient bool `json:"attached_client,omitempty"`
	// Shard names the test runner ("vitest" or "jest") when the command runs
	// a whole suite that can be split between the laptop and a phone.
	Shard string `json:"shard,omitempty"`
}

// LocalObservation reports a completed adapter command, never a simulated run.
type LocalObservation struct {
	Spec       JobSpec   `json:"spec"`
	Decision   *Decision `json:"decision,omitempty"`
	Started    time.Time `json:"started"`
	DurationMS float64   `json:"duration_ms"`
	ExitCode   int       `json:"exit_code"`
	CPUSeconds float64   `json:"cpu_seconds"`
	PeakRAMMB  *float64  `json:"peak_ram_mb,omitempty"`
}

// SuiteObservation reports how many test files a whole run of a splittable
// suite covered; SplitSum, when set, is what the parts of the split run before
// it covered (-1: unknown).
type SuiteObservation struct {
	Spec     JobSpec `json:"spec"`
	Files    int     `json:"files"`
	SplitSum int     `json:"split_sum,omitempty"`
}
type Viewport struct {
	Name   string  `json:"name"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	DPR    float64 `json:"dpr"`
}
type RenderSpec struct {
	URL      string   `json:"url"`
	Viewport Viewport `json:"viewport"`
}
type Candidate struct {
	Target          string   `json:"target"`
	Eligible        bool     `json:"eligible"`
	Reasons         []string `json:"reasons"`
	TotalCostMS     float64  `json:"total_cost_ms"`
	Benefit         float64  `json:"benefit"`
	SyncBytes       int64    `json:"sync_bytes"`
	EnvironmentWarm bool     `json:"environment_warm"`
}
type Decision struct {
	Target      string      `json:"target"`
	Explanation string      `json:"explanation"`
	Candidates  []Candidate `json:"candidates"`
	LocalCostMS float64     `json:"local_cost_ms"`
	// Confirm asks the adapter to check a failed remote run locally: a
	// command's phone results are trusted once both sides agreed twice.
	Confirm bool `json:"confirm,omitempty"`
	// ForcedLocal: the caller required this laptop although a phone could
	// have taken the command.
	ForcedLocal bool `json:"forced_local,omitempty"`
	// Split: run the suite in this many parts at once, the first on this
	// laptop and the rest on Target; together they must cover SplitFiles
	// test files.
	Split      int `json:"split,omitempty"`
	SplitFiles int `json:"split_files,omitempty"`
}

// WorkerChanges lists files a job changed in the worker's copy of a
// workspace, outside dependency and cache directories.
type WorkerChanges struct {
	Modified []FileEntry `json:"modified,omitempty"`
	Created  []FileEntry `json:"created,omitempty"`
	Deleted  []string    `json:"deleted,omitempty"`
}

// WriteBackResult: files applied to the workspace, or the conflict that
// stopped all of them (the adapter then runs the command locally).
type WriteBackResult struct {
	Applied  []string `json:"applied,omitempty"`
	Deleted  []string `json:"deleted,omitempty"`
	Conflict string   `json:"conflict,omitempty"`
}

// Confirmation compares a failed remote run with the local rerun it caused.
type Confirmation struct {
	Spec           JobSpec `json:"spec"`
	RemoteExitCode int     `json:"remote_exit_code"`
	LocalExitCode  int     `json:"local_exit_code"`
}
type Attempt struct {
	ID               string    `json:"id"`
	Target           string    `json:"target"`
	Started          time.Time `json:"started"`
	DurationMS       float64   `json:"duration_ms"`
	Error            string    `json:"error,omitempty"`
	BytesSent        int64     `json:"bytes_sent"`
	SyncMS           float64   `json:"sync_ms"`
	CacheWarm        bool      `json:"cache_warm"`
	EnvironmentWarm  bool      `json:"environment_warm"`
	WorkerPeakRAMMB  *float64  `json:"worker_peak_ram_mb,omitempty"`
	WorkerCPUSeconds *float64  `json:"worker_cpu_seconds,omitempty"`
	HostCPUSeconds   *float64  `json:"host_cpu_seconds,omitempty"`
	HostPeakRAMMB    *float64  `json:"host_peak_ram_mb,omitempty"`
}
type Job struct {
	ID       string     `json:"id"`
	Spec     JobSpec    `json:"spec"`
	State    string     `json:"state"`
	Created  time.Time  `json:"created"`
	Finished *time.Time `json:"finished,omitempty"`
	Decision Decision   `json:"decision"`
	Attempts []Attempt  `json:"attempts"`
	ExitCode *int       `json:"exit_code,omitempty"`
	// Evacuated: the phone overheated, so the job was stopped there and its
	// adapter runs it on the laptop.
	Evacuated bool `json:"evacuated,omitempty"`
	// Recheck: why a failed phone run may not be the code's fault, so the
	// adapter runs it here: "outside" (the phone's copy lacked a file from
	// outside the project folder; it has it from now on) or "timeout" (a test
	// ran out of time on the slower phone).
	Recheck string `json:"recheck,omitempty"`
	// WriteBack reports what a write-back job changed in the workspace.
	WriteBack      *WriteBackResult `json:"write_back,omitempty"`
	Error          string           `json:"error,omitempty"`
	Stdout         string           `json:"stdout,omitempty"`
	Stderr         string           `json:"stderr,omitempty"`
	Truncated      bool             `json:"truncated"`
	OutputBytes    int64            `json:"output_bytes"`
	AdapterMetrics *AdapterMetrics  `json:"adapter_metrics,omitempty"`
	// Forwarded lists host loopback ports currently served by the worker.
	Forwarded []int `json:"forwarded,omitempty"`
	// Spared: what running it on a phone spared the laptop (computed for
	// display, not stored).
	Spared *Relief `json:"spared,omitempty"`
}

// Relief is what a job run on a phone spared the laptop.
type Relief struct {
	CPUSeconds float64 `json:"cpu_seconds"`
	RAMMB      float64 `json:"ram_mb,omitempty"`
	// Measured: from the command's own runs on the laptop; otherwise the
	// phone's measurement of the same work.
	Measured bool `json:"measured,omitempty"`
	// LaptopSeconds: how long the command takes on the laptop, when measured.
	LaptopSeconds float64 `json:"laptop_seconds,omitempty"`
}
type AdapterMetrics struct {
	CPUSeconds float64 `json:"cpu_seconds"`
	PeakRAMMB  float64 `json:"peak_ram_mb"`
	Scope      string  `json:"scope"`
}
type AdapterObservation struct {
	JobID   string         `json:"job_id"`
	Metrics AdapterMetrics `json:"metrics"`
}
type FileEntry struct {
	Path       string `json:"path"`
	Hash       string `json:"hash"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable"`
}
type Manifest struct {
	ID         string      `json:"id"`
	Files      []FileEntry `json:"files"`
	TotalBytes int64       `json:"total_bytes"`
}
type HistorySample struct {
	Signature string `json:"signature"`
	// Measured cost of running the command on the host, when known: what
	// offloading it saves the laptop.
	HostCPUSeconds    float64   `json:"host_cpu_seconds,omitempty"`
	HostPeakRAMMB     float64   `json:"host_peak_ram_mb,omitempty"`
	Target            string    `json:"target"`
	DurationMS        float64   `json:"duration_ms"`
	CacheWarm         bool      `json:"cache_warm"` // Execution/dependency environment; input sync is priced separately.
	Success           bool      `json:"success"`
	Timestamp         time.Time `json:"timestamp"`
	DeviceFingerprint string    `json:"device_fingerprint,omitempty"`

	// A worker's own measurement of a run: a first estimate of what the
	// command costs where it has not run yet.
	WorkerCPUSeconds float64 `json:"worker_cpu_seconds,omitempty"`
	WorkerPeakRAMMB  float64 `json:"worker_peak_ram_mb,omitempty"`
}
