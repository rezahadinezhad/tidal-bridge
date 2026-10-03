package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"tidalbridge/packages/protocol"
)

var (
	// Settings a split run would get wrong: coverage and its thresholds,
	// report files, and test files sharing one module state.
	wholeSuite = regexp.MustCompile(`coverage\s*:\s*\{[^}]*\benabled\s*:\s*true|"?collectCoverage"?\s*:\s*true|outputFile|isolate\s*:\s*false|reporters?\s*:[^\]\n]*['"](junit|json|html|blob)['"]`)
	// Options that do the same, or are not a plain run of the suite.
	wholeSuiteFlags = []string{"--shard", "--watch", "--watchall", "--coverage", "--ui", "--outputfile", "--merge-reports", "--isolate", "--no-isolate",
		"--inspect", "--inspect-brk", "--changed", "--onlychanged", "--findrelatedtests", "--listtests", "--list", "--help", "--version", "--typecheck",
		"--browser", "--standalone", "--config", "-c", "--reporter=json", "--reporter=junit", "--reporter=html", "--reporter=blob"}
	// Database, cache and queue clients: tests using them may share state on
	// the laptop, which two parts running at once would race on.
	serviceClients = []string{"pg", "postgres", "mysql", "mysql2", "mongodb", "mongoose", "redis", "ioredis", "@prisma/client", "prisma", "typeorm",
		"sequelize", "knex", "drizzle-orm", "amqplib", "bullmq", "kafkajs", "@supabase/supabase-js", "firebase-admin"}
	ansi        = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	vitestFiles = regexp.MustCompile(`Test Files\s+[^\n]*\((\d+)\)`)
	jestSuites  = regexp.MustCompile(`Test Suites:[^\n]*?(\d+) total`)
)

// Shardable names the test runner when spec runs a whole test suite once with
// a runner whose --shard option splits it the same way on every machine
// (Vitest 1+, Jest 28+), and nothing in the command, the runner's
// configuration or the project's dependencies needs the suite in one run.
func Shardable(spec protocol.JobSpec, root, cwd string) string {
	// Each part runs once; only a part the phone could not finish runs again
	// here, which a test suite without a database or cache client allows.
	if spec.Service || spec.Policy.WriteBack || len(spec.Argv) == 0 || !slices.Contains([]string{"npm", "vitest", "jest"}, spec.Argv[0]) {
		return ""
	}
	var pkg struct {
		Scripts         map[string]string `json:"scripts"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		Jest            json.RawMessage   `json:"jest"`
	}
	if readPackage(filepath.Join(cwd, "package.json"), &pkg) != nil {
		return ""
	}
	words := spec.Argv
	if words[0] == "npm" {
		name, rest := "", []string(nil)
		switch {
		case len(words) > 1 && words[1] == "test":
			name, rest = "test", words[2:]
		case len(words) > 2 && words[1] == "run":
			name, rest = words[2], words[3:]
		default:
			return ""
		}
		if len(rest) > 0 && rest[0] != "--" {
			return ""
		}
		body := strings.Fields(pkg.Scripts[name])
		if len(body) > 0 && body[0] == "npx" {
			body = body[1:]
		}
		words = append(body, rest...)
		words = slices.DeleteFunc(words, func(w string) bool { return w == "--" })
	}
	if len(words) == 0 || (words[0] != "vitest" && words[0] != "jest") {
		return ""
	}
	runner, once, previous := words[0], words[0] == "jest", ""
	for _, word := range words[1:] {
		lower := strings.ToLower(word)
		if runner == "vitest" && (lower == "run" || lower == "--run") {
			once = true
		}
		if previous == "--reporter" && slices.Contains([]string{"json", "junit", "html", "blob"}, lower) {
			return ""
		}
		for _, flag := range wholeSuiteFlags {
			if lower == flag || strings.HasPrefix(lower, flag+"=") || strings.HasPrefix(lower, flag+".") {
				return ""
			}
		}
		previous = lower
	}
	if !once || installedMajor(runner, cwd, root) < map[string]int{"vitest": 1, "jest": 28}[runner] || wholeSuite.MatchString(runnerConfig(cwd)+string(pkg.Jest)) {
		return ""
	}
	for _, client := range serviceClients {
		if pkg.Dependencies[client] != "" || pkg.DevDependencies[client] != "" {
			return ""
		}
	}
	return runner
}

// installedMajor is the major version of the runner installed for cwd
// (looking up to the project root), or 0.
func installedMajor(runner, cwd, root string) int {
	for dir := filepath.Clean(cwd); ; {
		var pkg struct {
			Version string `json:"version"`
		}
		if readPackage(filepath.Join(dir, "node_modules", runner, "package.json"), &pkg) == nil {
			major, _ := strconv.Atoi(strings.SplitN(pkg.Version, ".", 2)[0])
			return major
		}
		parent := filepath.Dir(dir)
		if strings.EqualFold(dir, filepath.Clean(root)) || parent == dir {
			return 0
		}
		dir = parent
	}
}

// readPackage reads the fields it needs from a package.json, which has many
// more (ReadJSON is strict, for Tidal Bridge's own files).
func readPackage(path string, result any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return fmt.Errorf("%s exceeds 1 MB", path)
	}
	return json.Unmarshal(b, result)
}

// runnerConfig is the text of the test runner configuration files in dir.
func runnerConfig(dir string) string {
	var text strings.Builder
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && (strings.HasPrefix(name, "vitest.") || strings.HasPrefix(name, "vite.config.") || strings.HasPrefix(name, "jest.config.")) {
			if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil && len(b) < 1<<20 {
				text.Write(b)
				text.WriteByte('\n')
			}
		}
	}
	return text.String()
}

// ShardArgs selects part index of total of a test suite. An npm script gets
// them after "--", so npm passes them on (dash: the command has none yet).
// A part that happens to get no test files passes.
func ShardArgs(argv []string, dash bool, index, total int) []string {
	out := slices.Clone(argv)
	if dash {
		out = append(out, "--")
	}
	return append(out, fmt.Sprintf("--shard=%d/%d", index, total), "--passWithNoTests")
}

// SuiteFiles reads how many test files a run covered from the summary its
// runner prints last.
func SuiteFiles(output, runner string) (int, bool) {
	pattern := vitestFiles
	if runner == "jest" {
		pattern = jestSuites
	}
	matches := pattern.FindAllStringSubmatch(ansi.ReplaceAllString(output, ""), -1)
	if len(matches) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(matches[len(matches)-1][1])
	return n, err == nil && n > 0
}
