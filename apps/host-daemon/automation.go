package host

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
	"tidalbridge/packages/scheduler"
	adapter "tidalbridge/packages/task-adapter"
	classifier "tidalbridge/packages/task-classifier"
	"time"
)

type AutomationInput struct {
	Workspace string   `json:"workspace"`
	Enabled   bool     `json:"enabled"`
	Profiles  []string `json:"profiles"`
	Provision bool     `json:"provision"`
}

var taskProfiles = map[string]adapter.Task{
	"unittest":      {Name: "unittest", Command: []string{"python", "-m", "unittest"}},
	"pytest":        {Name: "pytest", Command: []string{"python", "-m", "pytest"}},
	"ruff":          {Name: "ruff", Command: []string{"python", "-m", "ruff", "check"}},
	"mypy":          {Name: "mypy", Command: []string{"python", "-m", "mypy"}},
	"typecheck":     {Name: "typecheck", Command: []string{"tsc", "--noEmit"}},
	"eslint":        {Name: "eslint", Command: []string{"eslint"}},
	"prettier":      {Name: "prettier", Command: []string{"prettier", "--check"}},
	"vitest":        {Name: "vitest", Command: []string{"vitest", "run"}},
	"jest":          {Name: "jest", Command: []string{"jest", "--runInBand"}},
	"npm-test":      {Name: "npm-test", Command: []string{"npm", "test"}},
	"npm-typecheck": {Name: "npm-typecheck", Command: []string{"npm", "run", "typecheck"}},
}

func (h *Host) AutomationStatus() any {
	var roots []string
	adapter.ReadJSON(filepath.Join(h.Dir, "automation-projects.json"), &roots)
	projects := []any{}
	for _, root := range roots {
		var project adapter.Project
		if adapter.ReadJSON(filepath.Join(root, ".tidalbridge", adapter.FileName), &project) == nil {
			projects = append(projects, map[string]any{"workspace": root, "enabled": project.Enabled, "tasks": project.Tasks})
		}
	}
	_, err := os.Stat(filepath.Join(h.Dir, "shims", "shims.json"))
	settings := adapter.LoadSettings(h.Dir)
	return map[string]any{"adapters_installed": err == nil, "projects": projects, "universal": settings.Universal, "excluded": settings.Excluded, "share_env": settings.ShareEnv,
		"scope": "Approved project commands, plus detected checks and tests in every project when universal mode is on; existing Windows processes remain on the host."}
}
func (h *Host) ConfigureAutomation(input AutomationInput) error {
	if !filepath.IsAbs(input.Workspace) {
		return fmt.Errorf("absolute project path required")
	}
	root, err := filepath.EvalSymlinks(input.Workspace)
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("existing project directory required")
	}
	if len(input.Profiles) > 32 {
		return fmt.Errorf("too many task profiles")
	}
	path := filepath.Join(root, ".tidalbridge", adapter.FileName)
	var project adapter.Project
	if err := adapter.ReadJSON(path, &project); err != nil && !os.IsNotExist(err) {
		return err
	}
	project.Version = 1
	project.Enabled = input.Enabled
	if len(input.Profiles) > 0 {
		for _, name := range input.Profiles {
			task, ok := taskProfiles[name]
			if !ok {
				return fmt.Errorf("unknown profile %s", name)
			}
			task.Provision = input.Provision
			found := false
			for i, old := range project.Tasks {
				if old.Name == task.Name {
					project.Tasks[i].Provision = input.Provision
					found = true
					break
				}
			}
			if !found {
				project.Tasks = append(project.Tasks, task)
			}
		}
	}
	if input.Enabled && len(project.Tasks) == 0 {
		return fmt.Errorf("select at least one task profile")
	}
	dir := filepath.Dir(path)
	if actual, e := filepath.EvalSymlinks(dir); e == nil && !strings.EqualFold(actual, dir) {
		return fmt.Errorf("task configuration directory must not redirect outside the project")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var roots []string
	adapter.ReadJSON(filepath.Join(h.Dir, "automation-projects.json"), &roots)
	present := false
	for _, item := range roots {
		if strings.EqualFold(item, root) {
			present = true
		}
	}
	if !present {
		if len(roots) >= 128 {
			return fmt.Errorf("project registry limit reached")
		}
		roots = append(roots, root)
	}
	if err := config.SaveJSON(path, project); err != nil {
		return err
	}
	if err := config.SaveJSON(filepath.Join(h.Dir, "automation-projects.json"), roots); err != nil {
		return err
	}
	h.log.Info("automation_configured", "workspace", root, "enabled", input.Enabled, "profiles", input.Profiles)
	return nil
}
func (h *Host) RecordLocal(input protocol.LocalObservation) (*protocol.Job, error) {
	if err := classifier.Normalize(&input.Spec); err != nil {
		return nil, err
	}
	now := time.Now()
	if !strings.HasPrefix(input.Spec.Profile, "automatic:") && !strings.HasPrefix(input.Spec.Profile, "detected:") || input.Started.IsZero() || input.Started.After(now.Add(time.Minute)) || input.Started.Before(now.Add(-25*time.Hour)) || input.DurationMS < 0 || input.DurationMS > 86400000 || math.IsNaN(input.DurationMS) || math.IsInf(input.DurationMS, 0) || input.CPUSeconds < 0 || math.IsNaN(input.CPUSeconds) || math.IsInf(input.CPUSeconds, 0) {
		return nil, fmt.Errorf("invalid local observation")
	}
	if input.PeakRAMMB != nil && (*input.PeakRAMMB < 0 || math.IsNaN(*input.PeakRAMMB) || math.IsInf(*input.PeakRAMMB, 0)) {
		return nil, fmt.Errorf("invalid local memory observation")
	}
	// A test or check that ran here is pre-run on the phone after the next edit.
	h.noteCommand(input.Spec, false)
	state := "COMPLETED"
	if input.ExitCode != 0 {
		state = "FAILED"
	}
	decision := protocol.Decision{Target: "LOCAL", Explanation: "Approved task executed by its original local runtime; measured adapter observation."}
	if input.Decision != nil && len(input.Decision.Candidates) <= 64 {
		decision = *input.Decision
		if decision.Target != "LOCAL" {
			decision.Explanation = "Original local runtime used after preview " + decision.Target + ": " + decision.Explanation
		}
		decision.Target = "LOCAL"
	}
	j := &protocol.Job{ID: config.Random(), Spec: input.Spec, State: state, Created: input.Started, Finished: &now, ExitCode: &input.ExitCode,
		Decision: decision,
		Attempts: []protocol.Attempt{{ID: config.Random(), Target: "LOCAL", Started: input.Started, DurationMS: input.DurationMS, CacheWarm: true, HostCPUSeconds: &input.CPUSeconds, HostPeakRAMMB: input.PeakRAMMB}}}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.jobs[j.ID] = j
	h.save(j)
	sample := protocol.HistorySample{Signature: scheduler.Signature(input.Spec), Target: "LOCAL", DurationMS: max(1, input.DurationMS), CacheWarm: true, Success: input.ExitCode == 0, Timestamp: now, HostCPUSeconds: input.CPUSeconds}
	if input.PeakRAMMB != nil {
		sample.HostPeakRAMMB = *input.PeakRAMMB
	}
	h.history = append(h.history, sample)
	if sample.Success && sample.HostCPUSeconds > 0 {
		h.recordCostLocked(sample)
		config.SaveJSON(filepath.Join(h.Dir, "laptop-costs.json"), h.costs)
	}
	if len(h.history) > 512 {
		h.history = h.history[len(h.history)-512:]
	}
	config.SaveJSON(filepath.Join(h.Dir, "history.json"), h.history)
	if len(h.jobs) > 128 {
		oldest := ""
		for id, job := range h.jobs {
			if job.Finished != nil && (oldest == "" || job.Created.Before(h.jobs[oldest].Created)) {
				oldest = id
			}
		}
		delete(h.jobs, oldest)
		delete(h.seen, oldest)
	}
	h.log.Info("local_task_observed", "job", j.ID, "profile", input.Spec.Profile, "duration_ms", input.DurationMS, "cpu_seconds", input.CPUSeconds)
	return j, nil
}
func (h *Host) RecordAdapter(input protocol.AdapterObservation) error {
	if input.Metrics.CPUSeconds < 0 || math.IsNaN(input.Metrics.CPUSeconds) || math.IsInf(input.Metrics.CPUSeconds, 0) || input.Metrics.PeakRAMMB < 0 || math.IsNaN(input.Metrics.PeakRAMMB) || math.IsInf(input.Metrics.PeakRAMMB, 0) {
		return fmt.Errorf("invalid adapter metrics")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	j := h.jobs[input.JobID]
	if j == nil {
		return fmt.Errorf("job is not in the active history")
	}
	input.Metrics.Scope = "Command adapter process only; daemon, ADB and dashboard excluded."
	j.AdapterMetrics = &input.Metrics
	h.save(j)
	return nil
}

// SettingsInput changes universal mode from the dashboard: a nil Universal
// leaves it as it is; a path is added to or removed from a list.
type SettingsInput struct {
	Universal *bool  `json:"universal,omitempty"`
	List      string `json:"list,omitempty"` // "excluded" or "share_env"
	Path      string `json:"path,omitempty"`
	Remove    bool   `json:"remove,omitempty"`
}

func (h *Host) UpdateSettings(input SettingsInput) error {
	s := adapter.LoadSettings(h.Dir)
	s.Version = 1
	if input.Universal != nil {
		s.Universal = *input.Universal
	}
	if input.List != "" {
		if !filepath.IsAbs(input.Path) {
			return fmt.Errorf("an absolute project folder is required")
		}
		path := filepath.Clean(input.Path)
		list := &s.Excluded
		if input.List == "share_env" {
			list = &s.ShareEnv
		} else if input.List != "excluded" {
			return fmt.Errorf("unknown list %q", input.List)
		}
		kept := []string{}
		for _, item := range *list {
			if !strings.EqualFold(filepath.Clean(item), path) {
				kept = append(kept, item)
			}
		}
		if !input.Remove {
			if info, err := os.Stat(path); err != nil || !info.IsDir() {
				return fmt.Errorf("%s is not a folder", path)
			}
			kept = append(kept, path)
		}
		*list = kept
	}
	h.changes.Add(1)
	return config.SaveJSON(filepath.Join(h.Dir, adapter.SettingsFile), s)
}
