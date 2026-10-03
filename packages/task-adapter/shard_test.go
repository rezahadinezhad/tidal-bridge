package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tidalbridge/packages/protocol"
)

func TestShardable(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"name":"app","private":true,"scripts":{"test":"vitest run --maxWorkers=1","watch":"vitest","cov":"vitest run --coverage"}}`)
	write("node_modules/vitest/package.json", `{"name":"vitest","version":"4.1.7","type":"module"}`)
	spec := func(argv string) protocol.JobSpec {
		return protocol.JobSpec{Argv: strings.Fields(argv), Policy: protocol.Policy{Idempotent: true}}
	}
	for argv, want := range map[string]string{"npm test": "vitest", "npm test -- tests/unit": "vitest", "vitest run": "vitest", "npm run watch": "", "npm run cov": "",
		"vitest": "", "vitest run --shard=1/3": "", "npm test -- --reporter=junit": "", "vitest run --config e2e.config.ts": ""} {
		if got := Shardable(spec(argv), dir, dir); got != want {
			t.Errorf("%s: got %q, want %q", argv, got, want)
		}
	}
	write("vitest.config.ts", `export default { test: { coverage: { provider: "v8", enabled: true } } }`)
	if Shardable(spec("npm test"), dir, dir) != "" {
		t.Error("coverage on in the configuration")
	}
	write("vitest.config.ts", `export default { test: { coverage: { provider: "v8", reporter: ["text", "json-summary"] } } }`)
	if Shardable(spec("npm test"), dir, dir) != "vitest" {
		t.Error("coverage settings that are off by default")
	}
	write("package.json", `{"scripts":{"test":"vitest run"},"devDependencies":{"ioredis":"5"}}`)
	if Shardable(spec("npm test"), dir, dir) != "" {
		t.Error("a cache client: tests may share state on the laptop")
	}
	write("package.json", `{"scripts":{"test":"vitest run"}}`)
	write("node_modules/vitest/package.json", `{"version":"0.34.6"}`)
	if Shardable(spec("npm test"), dir, dir) != "" {
		t.Error("an old runner")
	}
}

func TestShardArgsAndSuiteFiles(t *testing.T) {
	if got := strings.Join(ShardArgs([]string{"npm", "test"}, true, 2, 2), " "); got != "npm test -- --shard=2/2 --passWithNoTests" {
		t.Error(got)
	}
	if got := strings.Join(ShardArgs([]string{"vitest", "run"}, false, 1, 2), " "); got != "vitest run --shard=1/2 --passWithNoTests" {
		t.Error(got)
	}
	vitest := "\x1b[2m Test Files \x1b[22m \x1b[1m\x1b[31m2 failed\x1b[39m | 10 passed\x1b[22m\x1b[90m (12)\x1b[39m\n      Tests  50 passed (50)\n"
	if n, ok := SuiteFiles(vitest, "vitest"); !ok || n != 12 {
		t.Error("vitest", n, ok)
	}
	if n, ok := SuiteFiles("Test Suites: 1 failed, 11 passed, 12 total\nTests: 40 passed, 40 total", "jest"); !ok || n != 12 {
		t.Error("jest", n, ok)
	}
	if _, ok := SuiteFiles("No test files found, exiting with code 1", "vitest"); ok {
		t.Error("no summary")
	}
}
