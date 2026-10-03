// Package adapter implements opt-in command routing for developer projects.
package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"tidalbridge/packages/protocol"
)

const FileName = "tasks.json"

type Task struct {
	Name                string            `json:"name"`
	Command             []string          `json:"command"`
	EstimatedDurationMS float64           `json:"estimated_duration_ms,omitempty"`
	TimeoutSeconds      int               `json:"timeout_seconds,omitempty"`
	Provision           bool              `json:"provision,omitempty"`
	Idempotent          bool              `json:"idempotent,omitempty"`
	ExpectedOutputs     []string          `json:"expected_outputs,omitempty"`
	RuntimeRequirements map[string]string `json:"runtime_requirements,omitempty"`
	// Runtime overrides the project's worker userland ("termux"/"debian").
	Runtime string `json:"runtime,omitempty"`
	// Service marks a long-running dev server: it runs on the worker until
	// stopped, Ports are served on the laptop's loopback, and edits are synced
	// live. RemoteArgs are appended only for worker runs (for example to bind
	// the server to the worker's loopback instead of its Wi-Fi interface).
	Service          bool     `json:"service,omitempty"`
	Ports            []int    `json:"ports,omitempty"`
	ReversePorts     []int    `json:"reverse_ports,omitempty"`
	RemoteArgs       []string `json:"remote_args,omitempty"`
	ProvisionScripts bool     `json:"provision_scripts,omitempty"`
	// Engine: Debian tasks run Node tools natively on the worker (no proot)
	// whenever the command allows it, as detected commands do; the worker
	// keeps proot for anything else. "proot" opts a task out.
	Engine string `json:"engine,omitempty"`
	// WriteBack lets the command change files (formatters, fixers, code
	// generators): changes made on the worker are copied back.
	WriteBack bool `json:"write_back,omitempty"`
}

// EnvironmentFingerprint changes with the host/runtime or dependency inputs,
// while ordinary source edits retain comparable timing history.
func EnvironmentFingerprint(cwd string, local []string) string {
	h := sha256.New()
	io.WriteString(h, os.Getenv("COMPUTERNAME"))
	for i, path := range local {
		if i > 1 {
			break
		}
		if info, err := os.Stat(path); err == nil {
			fmt.Fprintf(h, "%s:%d:%d", path, info.Size(), info.ModTime().UnixNano())
		}
	}
	for _, name := range []string{"requirements.txt", "pyproject.toml", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock"} {
		if f, err := os.Open(filepath.Join(cwd, name)); err == nil {
			io.WriteString(h, name)
			io.CopyBuffer(h, f, make([]byte, 65536))
			f.Close()
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

type Project struct {
	Version int    `json:"version"`
	Enabled bool   `json:"enabled"`
	Tasks   []Task `json:"tasks"`
	// Runtime is the default worker userland for this project's tasks.
	Runtime string `json:"runtime,omitempty"`
	// ReversePorts are laptop services the project's commands reach on
	// 127.0.0.1 (database, API); they are exposed to the worker as well.
	ReversePorts []int `json:"reverse_ports,omitempty"`
	// SyncEnvFiles copies .env files to the worker (key files never).
	SyncEnvFiles bool `json:"sync_env_files,omitempty"`
	// Detected marks an implicit project from universal mode (never stored).
	Detected bool `json:"-"`
}
type Installation struct {
	Version       int                 `json:"version"`
	DataDir       string              `json:"data_dir"`
	LocalCommands map[string][]string `json:"local_commands"`
	// SelfDir is the adapter directory, never resolved as an original tool.
	SelfDir string `json:"-"`
}

func ReadJSON(path string, result any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 65536 {
		return fmt.Errorf("adapter configuration exceeds 64 KB")
	}
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(result); err != nil {
		return err
	}
	var trailing any
	if err = d.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("unexpected trailing adapter configuration")
	}
	return nil
}

// Find chooses the nearest explicit project configuration; a disabled child
// configuration stops inheritance. Projects are never implicitly opted in.
func Find(cwd string) (Project, string, error) {
	root, err := filepath.Abs(cwd)
	if err != nil {
		return Project{}, "", err
	}
	for {
		path := filepath.Join(root, ".tidalbridge", FileName)
		var project Project
		err := ReadJSON(path, &project)
		if err == nil {
			if project.Version != 1 || len(project.Tasks) > 64 {
				return project, root, fmt.Errorf("unsupported task configuration")
			}
			return project, root, nil
		}
		if !os.IsNotExist(err) {
			return project, root, err
		}
		parent := filepath.Dir(root)
		if parent == root {
			return Project{}, "", os.ErrNotExist
		}
		root = parent
	}
}

var nodeEntries = map[string]string{
	"typescript/bin/tsc": "tsc", "eslint/bin/eslint.js": "eslint",
	"prettier/bin/prettier.cjs": "prettier", "prettier/bin/prettier.mjs": "prettier",
	"vitest/vitest.mjs": "vitest", "jest/bin/jest.js": "jest",
	"npm/bin/npm-cli.js": "npm", "npm/bin/npx-cli.js": "npx",
}
var packages = map[string]string{"tsc": "typescript/bin/tsc", "eslint": "eslint/bin/eslint.js", "prettier": "prettier/bin/prettier.cjs", "vitest": "vitest/vitest.mjs", "jest": "jest/bin/jest.js"}

// Canonical maps runtime entry points to the same scheduler signature used by
// their command shims. No command string is interpreted as shell code.
func Canonical(tool string, args []string) []string {
	tool = strings.TrimSuffix(strings.ToLower(filepath.Base(tool)), ".exe")
	if tool == "python3" {
		tool = "python"
	}
	if tool == "npx" && len(args) > 0 {
		rest := args
		for len(rest) > 0 && (rest[0] == "--no-install" || rest[0] == "--yes" || rest[0] == "--") {
			rest = rest[1:]
		}
		if len(rest) > 0 {
			if _, ok := packages[rest[0]]; ok {
				return append([]string{}, rest...)
			}
		}
	}
	if tool == "pytest" || tool == "ruff" || tool == "mypy" {
		return append([]string{"python", "-m", tool}, args...)
	}
	if tool == "node" && len(args) > 0 {
		path := strings.ReplaceAll(args[0], "\\", "/")
		for entry, name := range nodeEntries {
			if strings.HasSuffix(path, "/node_modules/"+entry) || path == "node_modules/"+entry {
				return append([]string{name}, args[1:]...)
			}
		}
	}
	return append([]string{tool}, args...)
}

// Spec only matches a project's explicitly approved command prefix. Unsafe
// arguments, host paths, watch modes and undeclared file edits stay local.
func Spec(project Project, root, cwd string, argv []string) (protocol.JobSpec, bool) {
	if !project.Enabled || len(argv) == 0 {
		return protocol.JobSpec{}, false
	}
	var matched *Task
	for i := range project.Tasks {
		task := &project.Tasks[i]
		if task.Name == "" || len(task.Command) == 0 || len(argv) < len(task.Command) {
			continue
		}
		matches := true
		for j, arg := range task.Command {
			if argv[j] != arg {
				matches = false
				break
			}
		}
		if matches && (matched == nil || len(task.Command) > len(matched.Command)) {
			matched = task
		}
	}
	if matched == nil {
		return protocol.JobSpec{}, false
	}
	for _, arg := range argv {
		lower := strings.ToLower(arg)
		if strings.ContainsRune(arg, 0) || filepath.IsAbs(arg) || strings.Contains(arg, ":\\") || strings.Contains(arg, "../") || strings.Contains(arg, "..\\") {
			return protocol.JobSpec{}, false
		}
		if matched.WriteBack && (lower == "--fix" || lower == "--write" || lower == "-w" && argv[0] == "prettier") {
			// Copied back after the job, unless the laptop's copy changed.
			continue
		}
		for _, flag := range []string{"--watch", "--interactive", "--fix", "--fix-only", "--write", "--update", "--updatesnapshot", "--update-snapshots", "--init", "--install", "--build", "--outdir", "--outfile"} {
			if lower == flag || strings.HasPrefix(lower, flag+"=") {
				return protocol.JobSpec{}, false
			}
		}
		if lower == "-w" || lower == "-i" || lower == "-u" || lower == "-b" {
			return protocol.JobSpec{}, false
		}
	}
	if argv[0] == "tsc" {
		noEmit := false
		for i, a := range argv[1:] {
			if a == "--noEmit" {
				noEmit = true
			}
			if a == "--noEmit=false" || a == "--noEmit" && i+2 < len(argv) && argv[i+2] == "false" {
				return protocol.JobSpec{}, false
			}
		}
		if !noEmit {
			return protocol.JobSpec{}, false
		}
	}
	if argv[0] == "npm" {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		b, err := os.ReadFile(filepath.Join(cwd, "package.json"))
		if err != nil || json.Unmarshal(b, &pkg) != nil {
			return protocol.JobSpec{}, false
		}
		name := ""
		if len(argv) > 1 && argv[1] == "test" {
			name = "test"
		}
		if len(argv) > 2 && argv[1] == "run" {
			name = argv[2]
		}
		if name == "" || pkg.Scripts[name] == "" {
			return protocol.JobSpec{}, false
		}
		text := strings.ToLower(pkg.Scripts[name])
		for _, unsafe := range []string{"powershell", "pwsh", "cmd.exe", "msbuild", "dotnet", "win32", "--fix", "--write", "--watch", "&&", "||", ";", " >", " <"} {
			if strings.Contains(text, unsafe) && !(matched.WriteBack && (unsafe == "--fix" || unsafe == "--write")) {
				return protocol.JobSpec{}, false
			}
		}
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return protocol.JobSpec{}, false
	}
	if rel == "." {
		rel = ""
	}
	portable := append([]string(nil), argv...)
	for i := 1; i < len(portable); i++ {
		if strings.HasPrefix(portable[i], ".\\") {
			portable[i] = strings.ReplaceAll(portable[i], "\\", "/")
		}
	}
	estimate := matched.EstimatedDurationMS
	if estimate <= 0 {
		estimate = 1000
	} // history supersedes this cautious prior
	timeout := matched.TimeoutSeconds
	if timeout == 0 {
		timeout = 600
	}
	runtime := matched.Runtime
	if runtime == "" {
		runtime = project.Runtime
	}
	if runtime == "termux" {
		runtime = ""
	}
	reverse := append(append([]int(nil), project.ReversePorts...), matched.ReversePorts...)
	if matched.Service {
		portable = append(portable, matched.RemoteArgs...)
		if matched.TimeoutSeconds == 0 {
			timeout = 86400
		}
	}
	profile := "automatic:" + matched.Name
	if project.Detected {
		profile = "detected:" + matched.Name
	}
	engine := matched.Engine
	if engine == "" && runtime == "debian" {
		engine = "native"
	} else if engine == "proot" {
		engine = ""
	}
	return protocol.JobSpec{Argv: portable, Workspace: root, WorkingDirectory: filepath.ToSlash(rel), Profile: profile,
		EstimatedDurationMS: estimate, TimeoutSeconds: timeout, ExpectedOutputs: matched.ExpectedOutputs,
		Requirements: protocol.Requirements{Runtimes: matched.RuntimeRequirements},
		Runtime:      runtime, Engine: engine, Service: matched.Service, Ports: matched.Ports, ReversePorts: reverse, AttachedClient: matched.Service,
		Policy: protocol.Policy{LocalFallback: true, Idempotent: matched.Idempotent && !matched.Service, Retryable: matched.Idempotent && !matched.Service, Provision: matched.Provision,
			ProvisionScripts: matched.ProvisionScripts, SyncEnvFiles: project.SyncEnvFiles, WriteBack: matched.WriteBack}}, true
}

// LookPath finds name the way the caller's shell would, skipping adapter
// directories so an adapter never resolves to itself. Only real executables
// are returned; Windows command scripts need the installation's mapping.
func LookPath(name, skip string) string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		if skip != "" && strings.EqualFold(filepath.Clean(dir), filepath.Clean(skip)) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "shims.json")); err == nil {
			continue
		}
		candidate := filepath.Join(dir, name+ext)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && (ext != "" || info.Mode()&0111 != 0) {
			return candidate
		}
	}
	return ""
}

// ResolveLocal never resolves an installed shim back to itself. Project Node
// tools are preferred to global tools, matching npm's normal project behavior.
// Runtimes are resolved from the current PATH, so an activated virtualenv or a
// switched Node version keeps working exactly as without the adapter.
func ResolveLocal(inst Installation, tool, cwd string, args []string) ([]string, error) {
	tool = strings.TrimSuffix(strings.ToLower(filepath.Base(tool)), ".exe")
	node := inst.LocalCommands["node"]
	if found := LookPath("node", inst.SelfDir); found != "" {
		node = []string{found}
	}
	if entry, ok := packages[tool]; ok {
		root := cwd
		for {
			path := filepath.Join(root, "node_modules", filepath.FromSlash(entry))
			if _, err := os.Stat(path); err == nil && len(node) > 0 {
				return append(append(append([]string{}, node...), path), args...), nil
			}
			parent := filepath.Dir(root)
			if parent == root {
				break
			}
			root = parent
		}
	}
	if found := LookPath(tool, inst.SelfDir); found != "" {
		return append([]string{found}, args...), nil
	}
	if (tool == "npm" || tool == "npx") && len(node) == 1 {
		script := filepath.Join(filepath.Dir(node[0]), "node_modules", "npm", "bin", tool+"-cli.js")
		if _, err := os.Stat(script); err == nil {
			return append([]string{node[0], script}, args...), nil
		}
	}
	prefix := inst.LocalCommands[tool]
	if len(prefix) == 0 {
		return nil, fmt.Errorf("original local command %s is not installed", tool)
	}
	return append(append([]string{}, prefix...), args...), nil
}
