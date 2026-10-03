package adapter

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SettingsFile holds the user's automation choices in the data directory, so
// automatic mode never writes into projects.
const SettingsFile = "automation.json"

type Settings struct {
	Version int `json:"version"`
	// Universal recognizes safe commands in every project without a task file.
	Universal bool `json:"universal"`
	// Excluded projects, and everything below them, always run locally.
	Excluded []string `json:"excluded,omitempty"`
	// ShareEnv lists projects whose .env files may be copied to workers; only
	// then can their dev servers and Python tests move, since they read them.
	ShareEnv []string `json:"share_env,omitempty"`
}

func LoadSettings(dataDir string) Settings {
	var s Settings
	if ReadJSON(filepath.Join(dataDir, SettingsFile), &s) != nil || s.Version != 1 {
		return Settings{}
	}
	return s
}

// Within reports whether path is dir or lies below it, ignoring case as
// Windows does.
func Within(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	if strings.EqualFold(path, dir) {
		return true
	}
	prefix := strings.TrimSuffix(dir, string(filepath.Separator)) + string(filepath.Separator)
	return len(path) > len(prefix) && strings.EqualFold(path[:len(prefix)], prefix)
}

func Listed(list []string, path string) bool {
	for _, item := range list {
		if Within(path, item) {
			return true
		}
	}
	return false
}

var (
	nodeLockfiles  = []string{"package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock"}
	pythonMarkers  = []string{"pyproject.toml", "requirements.txt", "setup.py", "setup.cfg", "uv.lock", "manage.py", "pytest.ini"}
	detectedTools  = map[string]bool{"npm": true, "tsc": true, "eslint": true, "prettier": true, "vitest": true, "jest": true, "python": true}
	publicEnvFiles = []string{".example", ".sample", ".template", ".dist"}
)

// Detect builds an implicit project for a command run where no task file
// exists. Read-only checks and JavaScript tests are recognized everywhere;
// dev servers and Python tests only where no private .env file would be
// withheld from the worker. Builds and commands that edit files are never
// detected.
func Detect(cwd string, argv []string, s Settings) (Project, string, bool) {
	if !s.Universal || len(argv) == 0 || !detectedTools[argv[0]] {
		return Project{}, "", false
	}
	python := argv[0] == "python"
	root := projectRoot(cwd, python)
	if root == "" || Listed(s.Excluded, root) {
		return Project{}, "", false
	}
	share := Listed(s.ShareEnv, root)
	// Python tests and dev servers usually read .env files; JavaScript test
	// runners do not load them (Vite exposes only VITE_ variables), and a
	// failure they cause is caught by verification.
	withEnv := share || !privateEnv(root) && !privateEnv(cwd)
	p := Project{Version: 1, Enabled: true, Detected: true, Runtime: "debian", SyncEnvFiles: share}
	add := func(name string, pure bool, command ...string) {
		p.Tasks = append(p.Tasks, Task{Name: name, Command: command, Provision: true, Idempotent: pure, Engine: "native"})
	}
	// Formatters and fixers rewrite files only when asked to (--fix,
	// --write); their changes are copied back, so a rerun is harmless.
	fixer := func(name string, command ...string) {
		p.Tasks = append(p.Tasks, Task{Name: name, Command: command, Provision: true, Idempotent: true, Engine: "native", WriteBack: true})
	}
	if python {
		fixer("ruff", "python", "-m", "ruff", "check")
		fixer("ruff-format", "python", "-m", "ruff", "format")
		add("mypy", true, "python", "-m", "mypy")
		if withEnv {
			add("pytest", false, "python", "-m", "pytest")
			add("unittest", false, "python", "-m", "unittest")
			add("django-test", false, "python", "manage.py", "test")
			p.ReversePorts = laptopServices(root, cwd, nil)
		}
		return p, root, true
	}
	add("tsc", true, "tsc")
	fixer("eslint", "eslint")
	fixer("prettier", "prettier")
	add("vitest", false, "vitest", "run")
	add("jest", false, "jest")
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if b, err := os.ReadFile(filepath.Join(cwd, "package.json")); err == nil && json.Unmarshal(b, &pkg) == nil {
		for name, body := range pkg.Scripts {
			kind := ScriptKind(body)
			if kind == "" || kind == "service" && !withEnv {
				continue
			}
			if kind == "service" {
				if service, ok := devServer(name, body); ok {
					p.Tasks = append(p.Tasks, service)
				}
				continue
			}
			if kind == "fix" {
				fixer(name, "npm", "run", name)
				continue
			}
			add(name, kind == "check", "npm", "run", name)
			if name == "test" {
				add("npm-test", kind == "check", "npm", "test")
			}
		}
	}
	if withEnv {
		var own []int
		for _, task := range p.Tasks {
			own = append(own, task.Ports...)
		}
		p.ReversePorts = laptopServices(root, cwd, own)
	}
	return p, root, true
}

// projectRoot is the nearest directory holding a Node lock file (with its
// package.json) or a Python project marker, inside the repository. Drive
// roots and the user's home or its parents never count: a stray lock file
// there must not make a whole profile a workspace.
func projectRoot(cwd string, python bool) string {
	markers := nodeLockfiles
	if python {
		markers = pythonMarkers
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Clean(cwd)
	for {
		parent := filepath.Dir(dir)
		if parent == dir || home != "" && Within(home, dir) {
			return ""
		}
		for _, name := range markers {
			if regular(filepath.Join(dir, name)) && (python || regular(filepath.Join(dir, "package.json"))) {
				return dir
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return ""
		}
		dir = parent
	}
}

func regular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// privateEnv reports .env files other than published examples.
func privateEnv(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if name != ".env" && !strings.HasPrefix(name, ".env.") {
			continue
		}
		public := false
		for _, suffix := range publicEnvFiles {
			public = public || strings.HasSuffix(name, suffix)
		}
		if !public {
			return true
		}
	}
	return false
}

// ScriptKind classifies a package script by the tool it starts: "check" for
// read-only analysis, "test" for a test runner, "" for anything else.
// Chained, redirected and file-editing scripts are refused later by Spec.
func ScriptKind(body string) string {
	words := strings.Fields(body)
	for len(words) > 0 && (words[0] == "cross-env" || strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "-")) {
		words = words[1:]
	}
	if len(words) == 0 {
		return ""
	}
	has := func(flags ...string) bool {
		for _, word := range words[1:] {
			for _, flag := range flags {
				if word == flag {
					return true
				}
			}
		}
		return false
	}
	switch words[0] {
	case "tsc", "vue-tsc":
		if has("--noEmit") && !has("-b", "--build", "-w", "--watch") {
			return "check"
		}
	case "eslint":
		if has("--fix") {
			return "fix"
		}
		return "check"
	case "prettier":
		if has("--write", "-w") {
			return "fix"
		}
		if has("--check", "-c") {
			return "check"
		}
	case "biome":
		if len(words) > 1 && (words[1] == "check" || words[1] == "lint" || words[1] == "format" || words[1] == "ci") {
			if has("--write", "--fix") {
				return "fix"
			}
			if words[1] != "format" && !has("--apply", "--unsafe") {
				return "check"
			}
		}
	case "vitest":
		if has("run", "--run") {
			return "test"
		}
	case "jest", "mocha":
		return "test"
	case "node":
		if has("--test") {
			return "test"
		}
	case "next":
		if len(words) > 1 && words[1] == "dev" {
			return "service"
		}
	case "vite":
		if len(words) == 1 || words[1] == "dev" || words[1] == "serve" || strings.HasPrefix(words[1], "-") {
			return "service"
		}
	}
	return ""
}

// devServer describes a Next.js or Vite dev server script: its laptop port
// and the arguments that keep it on the phone's loopback. Scripts that pick
// their own host interface stay local.
func devServer(name, body string) (Task, bool) {
	words := strings.Fields(body)
	for len(words) > 0 && (words[0] == "cross-env" || strings.Contains(words[0], "=")) {
		words = words[1:]
	}
	port := 3000
	var remote []string
	if words[0] == "vite" {
		port = 5173
	} else {
		remote = []string{"--", "-H", "127.0.0.1"}
	}
	for i, word := range words {
		value := ""
		switch {
		case word == "-H" || word == "--hostname" || word == "--host" || strings.HasPrefix(word, "--host="):
			return Task{}, false
		case (word == "-p" || word == "--port") && i+1 < len(words):
			value = words[i+1]
		case strings.HasPrefix(word, "--port="):
			value = strings.TrimPrefix(word, "--port=")
		}
		if value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1024 || n > 65535 {
				return Task{}, false
			}
			port = n
		}
	}
	return Task{Name: name, Command: []string{"npm", "run", name}, Provision: true, Service: true, Ports: []int{port}, RemoteArgs: remote, Engine: "native"}, true
}

var (
	loopbackPort = regexp.MustCompile(`(?:localhost|127\.0\.0\.1):(\d{2,5})`)
	portSetting  = regexp.MustCompile(`(?m)^\s*[A-Z0-9_]*PORT\s*=\s*["']?(\d{4,5})["']?\s*$`)
	// Database, cache and queue ports a project usually reaches on the laptop.
	devServicePorts = []int{5432, 5433, 3306, 6379, 6380, 6381, 27017, 5672, 9000, 11211}
)

// laptopServices lists the laptop ports a project's jobs need through the
// cable: those its .env files name, and well-known database, cache and queue
// ports that are listening now. The job's own service ports are excluded.
func laptopServices(root, cwd string, own []int) []int {
	ports := envPorts(root, cwd, own)
	seen := map[int]bool{}
	for _, port := range append(own, ports...) {
		seen[port] = true
	}
	for _, port := range devServicePorts {
		if seen[port] {
			continue
		}
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 150*time.Millisecond); err == nil {
			conn.Close()
			seen[port] = true
			ports = append(ports, port)
		}
	}
	return ports
}

// envPorts lists laptop ports a shared .env file points at (an API or a
// database), so the phone's copy can reach them through the cable.
func envPorts(root, cwd string, own []int) []int {
	seen := map[int]bool{}
	for _, port := range own {
		seen[port] = true
	}
	var ports []int
	for _, dir := range []string{root, cwd} {
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			name := strings.ToLower(entry.Name())
			if name != ".env" && !strings.HasPrefix(name, ".env.") || entry.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil || len(b) > 1<<20 {
				continue
			}
			for _, m := range append(loopbackPort.FindAllStringSubmatch(string(b), -1), portSetting.FindAllStringSubmatch(string(b), -1)...) {
				if port, err := strconv.Atoi(m[1]); err == nil && port >= 1024 && port <= 65535 && !seen[port] && len(ports) < 12 {
					seen[port] = true
					ports = append(ports, port)
				}
			}
		}
	}
	return ports
}
