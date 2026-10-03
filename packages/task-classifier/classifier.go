package classifier

import (
	"fmt"
	"path/filepath"
	"strings"
	"tidalbridge/packages/browser"
	"tidalbridge/packages/protocol"
)

func Normalize(s *protocol.JobSpec) error {
	if s.Render != nil {
		if e := browser.Validate(*s.Render); e != nil {
			return e
		}
		s.Requirements.Browser = true
		s.Policy.ForceRemote = true
		s.Policy.LocalFallback = false
		s.Profile = "android-browser"
		s.Argv = []string{"android-browser", s.Render.URL, s.Render.Viewport.Name}
	}
	if s.ProtocolVersion == 0 {
		s.ProtocolVersion = protocol.Version
	}
	if s.ProtocolVersion != protocol.Version {
		return fmt.Errorf("unsupported protocol version")
	}
	if len(s.Argv) == 0 || len(s.Argv) > 128 {
		return fmt.Errorf("argv must contain 1–128 arguments")
	}
	if len(s.LocalArgv) > 128 {
		return fmt.Errorf("local argv exceeds 128 arguments")
	}
	for _, a := range append(append([]string{}, s.Argv...), s.LocalArgv...) {
		if strings.ContainsRune(a, 0) || len(a) > 65536 {
			return fmt.Errorf("invalid command argument")
		}
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = 600
	}
	if s.TimeoutSeconds < 1 || s.TimeoutSeconds > 86400 {
		return fmt.Errorf("timeout must be 1–86400 seconds")
	}
	if s.EstimatedDurationMS <= 0 {
		s.EstimatedDurationMS = 1000
	}
	if s.Policy.ForceLocal && s.Policy.ForceRemote {
		return fmt.Errorf("force-local and force-remote conflict")
	}
	if s.Policy.Destructive && (s.Policy.Idempotent || s.Policy.Retryable) {
		return fmt.Errorf("destructive jobs cannot be marked retryable/idempotent")
	}
	if s.Policy.Network == "" {
		s.Policy.Network = "inherit"
	}
	if s.Policy.Network != "inherit" && s.Policy.Network != "deny" {
		return fmt.Errorf("network must be inherit or deny")
	}
	if s.Policy.Network == "deny" {
		return fmt.Errorf("strict network isolation is unavailable in the unprivileged MVP; use a sandboxed executor")
	}
	if len(s.Env) > 32 {
		return fmt.Errorf("too many environment variables")
	}
	for k := range s.Env {
		upper := strings.ToUpper(k)
		for _, word := range []string{"TOKEN", "SECRET", "PASSWORD", "PRIVATE_KEY", "CREDENTIAL", "API_KEY"} {
			if strings.Contains(upper, word) {
				return fmt.Errorf("credential environment variable %s requires a separate explicit secret-sharing integration", k)
			}
		}
	}
	if s.Requirements.Runtimes == nil {
		s.Requirements.Runtimes = map[string]string{}
	}
	exe := strings.ToLower(filepath.Base(s.Argv[0]))
	switch exe {
	case "python", "python3", "pytest", "ruff", "mypy":
		s.Requirements.Runtimes["python"] = defaultVersion(s.Requirements.Runtimes["python"], ">=3.9")
	case "node", "npm", "pnpm", "yarn", "npx", "tsc", "eslint", "prettier", "vitest", "jest":
		s.Requirements.Runtimes["node"] = defaultVersion(s.Requirements.Runtimes["node"], ">=18")
		// V8 sizes its default heap from the machine: about 4 GB on a 16 GB
		// laptop but 2 GB on a phone, so large type-checks that pass locally
		// would run out of heap remotely. Mirror the laptop default.
		if _, set := s.Env["NODE_OPTIONS"]; !set {
			if s.Env == nil {
				s.Env = map[string]string{}
			}
			s.Env["NODE_OPTIONS"] = "--max-old-space-size=4096"
		}
	case "git":
		s.Requirements.Runtimes["git"] = defaultVersion(s.Requirements.Runtimes["git"], ">=2")
	case "sh", "bash":
		s.Requirements.Runtimes[exe] = defaultVersion(s.Requirements.Runtimes[exe], "*")
	default:
		if len(s.Requirements.Runtimes) == 0 && s.Render == nil {
			s.Requirements.HostOnly = true
		}
	}
	if strings.HasSuffix(exe, ".exe") || strings.HasSuffix(exe, ".cmd") || strings.HasSuffix(exe, ".bat") || exe == "powershell" || exe == "pwsh" || exe == "cmd" || exe == "msbuild" || exe == "dotnet" {
		s.Requirements.HostOnly = true
	}
	for _, a := range s.Argv {
		lower := strings.ToLower(a)
		if strings.Contains(lower, "win32") || strings.Contains(lower, "win-x64") || strings.Contains(lower, "windows-latest") || strings.Contains(lower, "x86_64-pc-windows") {
			s.Requirements.HostOnly = true
		}
	}
	if s.Requirements.Architecture == "" {
		s.Requirements.Architecture = "any"
	}
	if s.WorkingDirectory != "" && (filepath.IsAbs(s.WorkingDirectory) || strings.Contains(s.WorkingDirectory, "..") || strings.Contains(s.WorkingDirectory, ":")) {
		return fmt.Errorf("working_directory must be a safe workspace-relative path")
	}
	for _, p := range s.ExpectedOutputs {
		if filepath.IsAbs(p) || strings.Contains(p, "..") || strings.Contains(p, ":") || strings.Contains(p, "\\") {
			return fmt.Errorf("output paths must be safe relative POSIX paths")
		}
	}
	return nil
}
func defaultVersion(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
