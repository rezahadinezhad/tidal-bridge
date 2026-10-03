package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const scripts = `{"scripts": {
	"dev": "next dev", "build": "npm run typecheck && next build", "typecheck": "tsc --noEmit",
	"lint": "eslint .", "format": "prettier --write .", "test": "vitest run --reporter=dot tests",
	"test:watch": "vitest", "api:generate": "openapi-typescript api.yaml -o schema.ts"}}`

func taskNames(p Project) map[string]bool {
	names := map[string]bool{}
	for _, task := range p.Tasks {
		names[task.Name] = true
	}
	return names
}

func TestDetectRecognizesChecksEverywhereAndTestsWithoutPrivateEnv(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shop", "frontend")
	writeFiles(t, root, map[string]string{"package.json": scripts, "package-lock.json": "{}", ".env.local": "API=http://localhost:8001", "src/a.ts": ""})
	on := Settings{Version: 1, Universal: true}
	if _, _, ok := Detect(root, []string{"npm", "run", "typecheck"}, Settings{Version: 1}); ok {
		t.Fatal("universal mode off must detect nothing")
	}
	p, found, ok := Detect(filepath.Join(root, "src"), []string{"tsc", "--noEmit"}, on)
	if !ok || found != root || !p.Detected || p.SyncEnvFiles || p.Runtime != "debian" {
		t.Fatal("project root not detected from a subdirectory", found, p)
	}
	p, _, _ = Detect(root, []string{"npm", "run", "lint"}, on)
	names := taskNames(p)
	for _, want := range []string{"typecheck", "lint", "tsc", "eslint", "prettier"} {
		if !names[want] {
			t.Fatal("missing check", want, names)
		}
	}
	for _, never := range []string{"dev", "build", "test:watch", "api:generate"} {
		if names[never] {
			t.Fatal("detected a dev server needing the private .env, a build, an edit or a watcher", never)
		}
	}
	if !names["test"] || !names["vitest"] {
		t.Fatal("JavaScript tests move without the private .env", names)
	}
	if spec, ok := Spec(p, root, root, []string{"npm", "run", "format"}); !ok || !spec.Policy.WriteBack {
		t.Fatal("a formatter script moves with write-back", spec)
	}
	if spec, ok := Spec(p, root, root, []string{"eslint", ".", "--fix"}); !ok || !spec.Policy.WriteBack {
		t.Fatal("eslint --fix moves with write-back", spec)
	}
	if spec, ok := Spec(p, root, root, []string{"tsc", "--noEmit"}); !ok || spec.Policy.WriteBack {
		t.Fatal("checks never write back", spec)
	}
	if _, ok := Spec(p, root, root, []string{"tsc", "--noEmit", "-w"}); ok {
		t.Fatal("watch mode stays local")
	}
	explicit := Project{Version: 1, Enabled: true, Tasks: []Task{{Name: "eslint", Command: []string{"eslint"}}}}
	if _, ok := Spec(explicit, root, root, []string{"eslint", "--fix"}); ok {
		t.Fatal("a task file without write_back keeps refusing --fix")
	}
	spec, eligible := Spec(p, root, root, []string{"npm", "run", "typecheck"})
	if !eligible || spec.Profile != "detected:typecheck" || !spec.Policy.Idempotent || spec.Policy.SyncEnvFiles || spec.Engine != "native" {
		t.Fatal("detected check spec", spec)
	}
	if _, eligible := Spec(p, root, root, []string{"npm", "run", "build"}); eligible {
		t.Fatal("chained build script must stay local")
	}
	shared := Settings{Version: 1, Universal: true, ShareEnv: []string{filepath.Dir(root)}}
	p, _, _ = Detect(root, []string{"npm", "test"}, shared)
	if names = taskNames(p); !names["dev"] || !names["npm-test"] || !p.SyncEnvFiles {
		t.Fatal("shared .env files let the dev server move", names)
	}
	if spec, eligible = Spec(p, root, root, []string{"npm", "test"}); !eligible || spec.Policy.Idempotent {
		t.Fatal("tests move but are never replayed automatically", spec)
	}
	spec, eligible = Spec(p, root, root, []string{"npm", "run", "dev"})
	if !eligible || !spec.Service || len(spec.Ports) != 1 || spec.Ports[0] != 3000 || len(spec.ReversePorts) < 1 || spec.ReversePorts[0] != 8001 ||
		spec.Argv[len(spec.Argv)-1] != "127.0.0.1" {
		t.Fatal("dev server: laptop port 3000, API on 8001 reachable, phone loopback only", spec)
	}
	excluded := Settings{Version: 1, Universal: true, Excluded: []string{filepath.Dir(root)}}
	if _, _, ok := Detect(root, []string{"npm", "run", "lint"}, excluded); ok {
		t.Fatal("excluded project detected")
	}
}

func TestDetectNeedsLockfileAndStaysInsideRepository(t *testing.T) {
	base := t.TempDir()
	writeFiles(t, base, map[string]string{"package-lock.json": "{}", "package.json": "{}", "repo/.git/HEAD": "", "repo/app/package.json": scripts, "repo/app/.env.example": "A=1"})
	on := Settings{Version: 1, Universal: true}
	if _, _, ok := Detect(filepath.Join(base, "repo", "app"), []string{"npm", "run", "lint"}, on); ok {
		t.Fatal("a lock file outside the repository must not make a workspace")
	}
	writeFiles(t, base, map[string]string{"repo/app/package-lock.json": "{}"})
	p, _, ok := Detect(filepath.Join(base, "repo", "app"), []string{"npm", "test"}, on)
	if !ok || !taskNames(p)["test"] {
		t.Fatal("published .env.example files do not block tests", p.Tasks)
	}
	writeFiles(t, base, map[string]string{"py/requirements.txt": "pytest==8.3.0", "py/tests/test_a.py": "", "py/manage.py": "",
		"py/.env": "POSTGRES_HOST=localhost\nPOSTGRES_PORT=5433\nEMAIL_PORT=587\nREDIS_URL=redis://localhost:6381/0\n"})
	shared := Settings{Version: 1, Universal: true, ShareEnv: []string{filepath.Join(base, "py")}}
	p, _, _ = Detect(filepath.Join(base, "py"), []string{"python", "manage.py", "test"}, shared)
	if !taskNames(p)["django-test"] || len(p.ReversePorts) < 2 || p.ReversePorts[0] != 5433 && p.ReversePorts[1] != 5433 {
		t.Fatal("Django tests with shared .env reach the laptop's database and cache", p.Tasks, p.ReversePorts)
	}
	if p, _, _ := Detect(filepath.Join(base, "py"), []string{"python", "manage.py", "test"}, on); taskNames(p)["django-test"] {
		t.Fatal("Python tests need their .env shared")
	}
	p, root, ok := Detect(filepath.Join(base, "py", "tests"), []string{"python", "-m", "pytest"}, shared)
	if !ok || root != filepath.Join(base, "py") || !taskNames(p)["pytest"] {
		t.Fatal("python project", root, p.Tasks)
	}
	if _, _, ok := Detect(base, []string{"node", "server.js"}, on); ok {
		t.Fatal("arbitrary node programs are never detected")
	}
}

func TestDevServerPortsAndHosts(t *testing.T) {
	if task, ok := devServer("dev", "next dev -p 3100"); !ok || task.Ports[0] != 3100 {
		t.Fatal(task)
	}
	if task, ok := devServer("dev", "vite --port=4000"); !ok || task.Ports[0] != 4000 || len(task.RemoteArgs) != 0 {
		t.Fatal(task)
	}
	if _, ok := devServer("dev", "next dev -H 0.0.0.0"); ok {
		t.Fatal("a script choosing its own interface must stay local")
	}
}

func TestScriptKind(t *testing.T) {
	cases := map[string]string{
		"tsc --noEmit": "check", "tsc": "", "tsc -b --noEmit": "", "vue-tsc --noEmit": "check",
		"eslint .": "check", "eslint . --fix": "fix", "prettier --check .": "check", "prettier --write .": "fix", "prettier .": "",
		"biome check src": "check", "biome check --write": "fix", "biome format": "", "biome format --write": "fix", "vitest run": "test", "vitest": "",
		"cross-env NODE_ENV=test jest": "test", "node --test": "test", "node server.js": "", "next dev": "service", "next build": "",
		"vite": "service", "vite --port 4000": "service", "vite build": "",
	}
	for body, want := range cases {
		if got := ScriptKind(body); got != want {
			t.Errorf("ScriptKind(%q) = %q, want %q", body, got, want)
		}
	}
}
