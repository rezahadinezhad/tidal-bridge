package workspacesync

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"tidalbridge/packages/protocol"
)

// Segments that never leave the host: credentials, VCS metadata and host
// dependency trees (Windows node_modules/venvs are not portable to ARM64).
var excluded = map[string]bool{".git": true, ".codex": true, ".claude": true, "node_modules": true, ".venv": true, "venv": true, ".tools": true, ".tidalbridge": true, ".ssh": true, ".aws": true, ".azure": true, ".next": true, "coverage": true, "__pycache__": true, "dist": true, "build": true, "bin": true}

// Build output, caches and logs: regenerated wherever a command runs, and
// often large or constantly changing. Project ignore files may negate these.
var defaultIgnore = []string{".hg/", ".svn/", ".nuxt/", ".output/", ".svelte-kit/", ".turbo/", ".cache/", ".parcel-cache/", ".mypy_cache/", ".pytest_cache/", ".ruff_cache/", ".tox/", "htmlcov/", ".gradle/", ".idea/", ".next-*/", "*.log", "*.pyc", "*.tsbuildinfo", ".DS_Store", "Thumbs.db"}

// MaxFileBytes keeps very large assets out of worker copies.
const MaxFileBytes = 256 << 20

type Options struct {
	// CacheDir holds a per-workspace hash cache; unchanged files (same size
	// and modification time) are not read again. Empty disables caching.
	CacheDir string
	// IncludeEnv includes .env files (explicit per-project opt-in).
	IncludeEnv bool
}

func SafePath(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && !strings.Contains(p, "\\") && !strings.Contains(p, ":") && !strings.ContainsRune(p, 0) && p != ".." && !strings.HasPrefix(p, "../") && !strings.Contains(p, "/../") && !strings.HasSuffix(p, "/..")
}

// Credential stores by file name. Source files that merely mention
// credentials (coach-credentials.ts, lib/credentials/) are ordinary code.
var credentialFiles = map[string]bool{"credentials": true, ".credentials": true, ".git-credentials": true, "credentials.json": true, "credentials.yml": true, "credentials.yaml": true, "credentials.toml": true, "credentials.db": true, "client_secret.json": true, ".netrc": true, "_netrc": true}

func secretSegment(segment string, file, includeEnv bool) bool {
	if excluded[segment] || segment == ".npmrc" || segment == ".pypirc" || segment == "id_rsa" || segment == "id_ed25519" || strings.HasSuffix(segment, ".pem") || strings.HasSuffix(segment, ".key") {
		return true
	}
	if file && credentialFiles[segment] {
		return true
	}
	return !includeEnv && (segment == ".env" || strings.HasPrefix(segment, ".env."))
}

func SecretPath(p string) bool { return secretPath(p, false) }

func secretPath(p string, includeEnv bool) bool {
	segments := strings.Split(strings.ToLower(p), "/")
	for i, segment := range segments {
		if secretSegment(segment, i == len(segments)-1, includeEnv) {
			return true
		}
	}
	return false
}

// secretDir reports a directory that must not be entered.
func secretDir(p string, includeEnv bool) bool {
	for _, segment := range strings.Split(strings.ToLower(p), "/") {
		if secretSegment(segment, false, includeEnv) {
			return true
		}
	}
	return false
}

type rule struct {
	base    string
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
	literal string // the anchored path itself, when the pattern has no wildcards
}

func globToRegexp(glob string) string {
	var b strings.Builder
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(glob[i:], "/**") && i+3 == len(glob):
			b.WriteString("(?:/.*)?")
			i += 2
		case strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			end := strings.IndexByte(glob[i:], ']')
			if end > 1 {
				class := glob[i+1 : i+end]
				if strings.HasPrefix(class, "!") {
					class = "^" + class[1:]
				}
				b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
				i += end
			} else {
				b.WriteString(`\[`)
			}
		case c == '\\' && i+1 < len(glob):
			i++
			b.WriteString(regexp.QuoteMeta(string(glob[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

// compile turns ignore-file lines into rules relative to base ("" = root).
func compile(base string, lines []string) []rule {
	var rules []rule
	for _, raw := range lines {
		raw = strings.TrimRight(raw, " \r\t")
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		r := rule{base: base}
		if strings.HasPrefix(raw, "!") {
			r.negate = true
			raw = raw[1:]
		}
		if strings.HasSuffix(raw, "/") {
			r.dirOnly = true
			raw = strings.TrimRight(raw, "/")
		}
		anchored := strings.Contains(raw, "/")
		raw = strings.TrimPrefix(raw, "/")
		if raw == "" {
			continue
		}
		prefix := "(?:^|/)"
		if anchored {
			prefix = "^"
		}
		re, err := regexp.Compile(prefix + globToRegexp(raw) + "$")
		if err != nil {
			continue
		}
		r.re = re
		if anchored && !strings.ContainsAny(raw, `*?[\`) {
			r.literal = raw
		}
		rules = append(rules, r)
	}
	return rules
}

func ignoredBy(rules []rule, rel string, isDir bool) bool {
	result := false
	for _, r := range rules {
		if r.dirOnly && !isDir {
			continue
		}
		p := rel
		if r.base != "" {
			if !strings.HasPrefix(rel, r.base+"/") {
				continue
			}
			p = rel[len(r.base)+1:]
		}
		if r.re.MatchString(p) {
			result = !r.negate
		}
	}
	return result
}

// ignored keeps the original single-file semantics (root-relative patterns).
func ignored(path string, patterns []string) bool {
	return ignoredBy(compile("", patterns), strings.TrimSuffix(path, "/"), strings.HasSuffix(path, "/"))
}

type cacheEntry struct {
	Size  int64  `json:"s"`
	ModNs int64  `json:"m"`
	Hash  string `json:"h"`
}

type hashCache struct {
	mu      sync.Mutex
	path    string
	loaded  bool
	dirty   bool
	entries map[string]cacheEntry
}

var (
	cachesMu sync.Mutex
	caches   = map[string]*hashCache{}
)

func cacheFor(dir, root string) *hashCache {
	if dir == "" {
		return nil
	}
	key := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	path := filepath.Join(dir, hex.EncodeToString(key[:12])+".json")
	cachesMu.Lock()
	defer cachesMu.Unlock()
	c := caches[path]
	if c == nil {
		c = &hashCache{path: path, entries: map[string]cacheEntry{}}
		caches[path] = c
	}
	return c
}

func (c *hashCache) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	if b, err := os.ReadFile(c.path); err == nil {
		json.Unmarshal(b, &c.entries)
	}
	if c.entries == nil {
		c.entries = map[string]cacheEntry{}
	}
}

func (c *hashCache) save() {
	if !c.dirty {
		return
	}
	b, err := json.Marshal(c.entries)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(c.path), 0700)
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, b, 0600) == nil && os.Rename(tmp, c.path) == nil {
		c.dirty = false
	}
}

func Scan(ctx context.Context, root string) (protocol.Manifest, error) {
	return ScanWith(ctx, root, Options{})
}

func ScanWith(ctx context.Context, root string, opts Options) (protocol.Manifest, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return protocol.Manifest{}, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return protocol.Manifest{}, fmt.Errorf("workspace must be an existing directory")
	}
	h := sha256.Sum256([]byte(abs))
	m := protocol.Manifest{ID: hex.EncodeToString(h[:12]), Files: []protocol.FileEntry{}}
	defaults, overrides := compile("", defaultIgnore), projectRules(abs)
	rules := append(append([]rule{}, defaults...), overrides...)
	cache := cacheFor(opts.CacheDir, abs)
	if cache != nil {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		cache.load()
	}
	seenCache := map[string]bool{}
	var unhashed []pendingHash
	add := func(path string, info os.FileInfo) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if !SafePath(path) || secretPath(path, opts.IncludeEnv) || ignoredBy(rules, path, false) {
			return nil
		}
		full := filepath.Join(abs, filepath.FromSlash(path))
		if info == nil {
			var e error
			if info, e = os.Lstat(full); e != nil {
				if os.IsNotExist(e) {
					return nil // deleted while listing
				}
				return e
			}
		}
		if !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
			return nil
		}
		if len(m.Files) >= 100000 {
			return fmt.Errorf("workspace exceeds 100000-file manifest limit")
		}
		hash := ""
		if cache != nil {
			if c, ok := cache.entries[path]; ok && c.Size == info.Size() && c.ModNs == info.ModTime().UnixNano() {
				hash = c.Hash
			}
		}
		seenCache[path] = true
		m.Files = append(m.Files, protocol.FileEntry{Path: path, Hash: hash, Size: info.Size(), Executable: info.Mode()&0111 != 0})
		m.TotalBytes += info.Size()
		if hash == "" {
			unhashed = append(unhashed, pendingHash{index: len(m.Files) - 1, full: full, info: info})
		}
		return nil
	}
	finish := func() error {
		if err := hashAll(ctx, m.Files, unhashed, cache); err != nil {
			return err
		}
		kept := m.Files[:0]
		m.TotalBytes = 0
		for _, f := range m.Files {
			if f.Hash != "" { // empty: deleted while hashing
				kept = append(kept, f)
				m.TotalBytes += f.Size
			}
		}
		m.Files = kept
		sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
		m.ID = snapshotID(abs, m.Files)
		if cache != nil {
			for path := range cache.entries {
				if !seenCache[path] {
					delete(cache.entries, path)
					cache.dirty = true
				}
			}
			cache.save()
		}
		return nil
	}
	if listed, ok, e := gitList(ctx, abs); ok {
		// Version control ignores build output that a project may bring back.
		for _, dir := range reincluded(overrides) {
			if more, ok, _ := gitFiles(ctx, abs, "--others", "--ignored", "--exclude-standard", "--", dir); ok {
				listed = append(listed, more...)
			}
		}
		memo := map[string]bool{}
		for _, path := range listed {
			if skipByDirectory(abs, path, rules, memo) {
				continue
			}
			if e = add(path, nil); e != nil {
				return m, e
			}
		}
		return m, finish()
	} else if e != nil && ctx.Err() != nil {
		return m, e
	}
	err = walk(ctx, abs, defaults, overrides, opts, add)
	if e := finish(); err == nil {
		err = e
	}
	return m, err
}

type pendingHash struct {
	index int
	full  string
	info  os.FileInfo
}

// hashReaders: a file's first read waits for the antivirus scan, which runs
// per file, so several readers index a new project several times sooner
// (2,000 new files: 102 s with one reader, 18 s with eight).
const hashReaders = 8

// hashAll fills in the hashes of files the cache did not know, several at a
// time; a file deleted meanwhile keeps an empty hash and is dropped.
func hashAll(ctx context.Context, files []protocol.FileEntry, pending []pendingHash, cache *hashCache) error {
	work := make(chan pendingHash)
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	for range min(hashReaders, len(pending)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buffer := make([]byte, 64*1024)
			for p := range work {
				hash, err := hashWith(p.full, buffer)
				mu.Lock()
				switch {
				case err == nil:
					files[p.index].Hash = hash
					if cache != nil {
						cache.entries[files[p.index].Path] = cacheEntry{Size: p.info.Size(), ModNs: p.info.ModTime().UnixNano(), Hash: hash}
						cache.dirty = true
					}
				case !os.IsNotExist(err) && first == nil:
					first = err
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, p := range pending {
		select {
		case work <- p:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	if first == nil {
		first = ctx.Err()
	}
	return first
}

func hashWith(path string, buffer []byte) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	digest := sha256.New()
	if _, err := io.CopyBuffer(digest, f, buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// projectRules reads the project's .tidalbridgeignore. Its rules apply after
// every .gitignore, so "!dir/" brings back output a .gitignore excludes but
// remote commands need (for example a generated package the dev server
// copies).
func projectRules(abs string) []rule {
	b, err := os.ReadFile(filepath.Join(abs, ".tidalbridgeignore"))
	if err != nil {
		return nil
	}
	return compile("", strings.Split(string(b), "\n"))
}

// reincluded lists the literal paths .tidalbridgeignore negates.
func reincluded(overrides []rule) []string {
	var paths []string
	for _, r := range overrides {
		if r.negate && r.literal != "" && SafePath(r.literal) {
			paths = append(paths, r.literal)
		}
	}
	return paths
}

// skipByDirectory applies directory rules (and Rust target detection) to a
// path produced by git, whose own ignore handling already ran. Decisions are
// memoized per directory for the duration of one scan.
func skipByDirectory(root, path string, rules []rule, memo map[string]bool) bool {
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		dir := strings.Join(parts[:i], "/")
		skip, known := memo[dir]
		if !known {
			skip = ignoredBy(rules, dir, true) || isRustTarget(root, dir)
			memo[dir] = skip
		}
		if skip {
			return true
		}
	}
	return false
}

func isRustTarget(root, rel string) bool {
	if filepath.Base(rel) != "target" {
		return false
	}
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(filepath.ToSlash(filepath.Dir(rel))), "Cargo.toml"))
	return err == nil
}

func gitList(ctx context.Context, abs string) ([]string, bool, error) {
	return gitFiles(ctx, abs, "--cached", "--others", "--exclude-standard")
}

func gitFiles(ctx context.Context, abs string, args ...string) ([]string, bool, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, false, nil
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", abs, "ls-files", "-z"}, args...)...)
	cmd.Stderr = io.Discard
	prepareCommand(cmd)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err = cmd.Start(); err != nil {
		return nil, false, err
	}
	reader := bufio.NewReader(pipe)
	seen := map[string]bool{}
	var paths []string
	for {
		path, readErr := reader.ReadString(0)
		if path = strings.TrimSuffix(path, "\x00"); path != "" && !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
		if readErr != nil {
			break
		}
	}
	if err = cmd.Wait(); err != nil {
		return nil, false, err
	}
	return paths, true, nil
}

// walk lists files outside git, honoring .gitignore files at every level.
// pythonPackage reports a directory with an __init__.py that no built-in
// exclusion or project rule (base) hides. In a project without git, .gitignore
// rules are only advice, and one written for data (media/, uploads/) must
// not hide a package that imports need.
func pythonPackage(abs, dir string, base []rule) bool {
	info, err := os.Stat(filepath.Join(abs, filepath.FromSlash(dir), "__init__.py"))
	return err == nil && info.Mode().IsRegular() && !ignoredBy(base, dir, true)
}

// insideGit reports whether a directory belongs to a git working tree.
func insideGit(abs string) bool {
	for dir := abs; ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func walk(ctx context.Context, abs string, defaults, overrides []rule, opts Options, add func(string, os.FileInfo) error) error {
	rulesByDir := map[string][]rule{"": defaults}
	base := append(append([]rule{}, defaults...), overrides...)
	git := insideGit(abs)
	if b, e := os.ReadFile(filepath.Join(abs, ".gitignore")); e == nil {
		rulesByDir[""] = append(append([]rule{}, defaults...), compile("", strings.Split(string(b), "\n"))...)
	}
	// The project's own rules decide last, over every .gitignore level.
	effective := map[string][]rule{}
	rulesIn := func(dir string) []rule {
		r, ok := effective[dir]
		if !ok {
			r = append(append([]rule{}, rulesByDir[dir]...), overrides...)
			effective[dir] = r
		}
		return r
	}
	return filepath.WalkDir(abs, func(full string, d os.DirEntry, e error) error {
		if e != nil {
			if os.IsNotExist(e) || os.IsPermission(e) {
				return nil
			}
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, e := filepath.Rel(abs, full)
		if e != nil {
			return e
		}
		if rel == "." {
			return nil
		}
		path := filepath.ToSlash(rel)
		parent := filepath.ToSlash(filepath.Dir(rel))
		if parent == "." {
			parent = ""
		}
		rules := rulesByDir[parent]
		if d.IsDir() {
			hidden := ignoredBy(rulesIn(parent), path, true) && (git || !pythonPackage(abs, path, base))
			if secretDir(path, opts.IncludeEnv) || hidden || isRustTarget(abs, path) || d.Type()&os.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			own := rules
			if b, e := os.ReadFile(filepath.Join(full, ".gitignore")); e == nil {
				own = append(append([]rule{}, rules...), compile(path, strings.Split(string(b), "\n"))...)
			}
			rulesByDir[path] = own
			return nil
		}
		if ignoredBy(rulesIn(parent), path, false) {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return nil
		}
		return add(path, info)
	})
}

func snapshotID(root string, files []protocol.FileEntry) string {
	h := sha256.New()
	io.WriteString(h, root)
	for _, f := range files {
		io.WriteString(h, "\x00"+f.Path+"\x00"+f.Hash)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
