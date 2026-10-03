package workspacesync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"tidalbridge/packages/protocol"
)

// Change is one file-level difference; Entry is nil for a removal.
type Change struct {
	Path  string
	Entry *protocol.FileEntry
}

// Index keeps a workspace manifest current from file-system notifications,
// so routing a command does not rescan the project, and live services can
// receive edits as they happen. Without notifications it rescans on demand.
type Index struct {
	root string
	opts Options

	mu        sync.Mutex
	files     map[string]protocol.FileEntry
	manifest  *protocol.Manifest
	pending   map[string]bool
	overflow  bool
	timer     *time.Timer
	listeners map[int]func([]Change)
	nextID    int
	nested    map[string][]rule // defaults and .gitignore rules per directory
	effective map[string][]rule // nested plus the project's overrides
	overrides []rule            // .tidalbridgeignore; nil until read
	git       bool              // inside a git working tree
	stop      func()
	watching  bool
	lastUse   time.Time
	rescanned time.Time
	closed    bool
	scanning  bool // the initial scan is running; notifications wait for it
}

// OpenIndex starts watching root, then scans it once (using the hash cache).
// Watching first means an edit made during the scan is never lost: its
// notification is applied after the scan.
func OpenIndex(ctx context.Context, root string, opts Options) (*Index, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	x := &Index{root: abs, opts: opts, files: map[string]protocol.FileEntry{}, pending: map[string]bool{}, listeners: map[int]func([]Change){}, nested: map[string][]rule{}, effective: map[string][]rule{}, lastUse: time.Now(), scanning: true, git: insideGit(abs)}
	stop, watchErr := watch(abs, x.onEvents)
	m, err := ScanWith(ctx, abs, opts)
	if err != nil {
		if watchErr == nil {
			stop()
		}
		return nil, err
	}
	x.mu.Lock()
	for _, f := range m.Files {
		x.files[f.Path] = f
	}
	x.manifest = &m
	x.rescanned = time.Now()
	x.scanning = false
	if watchErr == nil {
		x.stop = stop
		x.watching = true
	}
	early := len(x.pending) > 0 || x.overflow
	x.mu.Unlock()
	if early {
		x.flush()
	}
	return x, nil
}

func (x *Index) Root() string { return x.root }

// Watching reports whether live notifications are active.
func (x *Index) Watching() bool { x.mu.Lock(); defer x.mu.Unlock(); return x.watching }

// LastUse is the last time a caller read the manifest.
func (x *Index) LastUse() time.Time { x.mu.Lock(); defer x.mu.Unlock(); return x.lastUse }

// Listeners reports how many live subscribers exist.
func (x *Index) Listeners() int { x.mu.Lock(); defer x.mu.Unlock(); return len(x.listeners) }

// Manifest returns the current snapshot. Pending notifications are applied
// first so a command sees the edit that was just saved.
func (x *Index) Manifest(ctx context.Context) (protocol.Manifest, error) {
	x.mu.Lock()
	stale := !x.watching && time.Since(x.rescanned) > 2*time.Second
	busy := len(x.pending) > 0 || x.overflow
	x.lastUse = time.Now()
	x.mu.Unlock()
	if busy {
		x.flush()
	}
	if stale {
		if _, err := x.rescan(ctx); err != nil {
			return protocol.Manifest{}, err
		}
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.manifest == nil {
		m := protocol.Manifest{Files: make([]protocol.FileEntry, 0, len(x.files))}
		for _, f := range x.files {
			m.Files = append(m.Files, f)
			m.TotalBytes += f.Size
		}
		sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
		m.ID = snapshotID(x.root, m.Files)
		x.manifest = &m
	}
	out := *x.manifest
	out.Files = append(make([]protocol.FileEntry, 0, len(x.manifest.Files)), x.manifest.Files...)
	return out, nil
}

// Subscribe registers fn for batches of changes; call the result to stop.
func (x *Index) Subscribe(fn func([]Change)) func() {
	x.mu.Lock()
	id := x.nextID
	x.nextID++
	x.listeners[id] = fn
	x.mu.Unlock()
	return func() {
		x.mu.Lock()
		delete(x.listeners, id)
		x.mu.Unlock()
	}
}

func (x *Index) Close() {
	x.mu.Lock()
	if x.closed {
		x.mu.Unlock()
		return
	}
	x.closed = true
	stop := x.stop
	if x.timer != nil {
		x.timer.Stop()
	}
	x.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (x *Index) onEvents(paths []string, overflow bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed {
		return
	}
	if overflow {
		x.overflow = true
	}
	for _, p := range paths {
		x.pending[p] = true
	}
	// Debounce bursts (editors write via temporary files; builds touch many).
	if x.timer == nil {
		x.timer = time.AfterFunc(150*time.Millisecond, x.flush)
	} else {
		x.timer.Reset(150 * time.Millisecond)
	}
}

func (x *Index) flush() {
	x.mu.Lock()
	if x.scanning {
		x.mu.Unlock() // OpenIndex applies these once its scan is done
		return
	}
	pending := x.pending
	overflow := x.overflow
	x.pending = map[string]bool{}
	x.overflow = false
	x.mu.Unlock()
	if len(pending) == 0 && !overflow {
		return
	}
	var changes []Change
	full := overflow || len(pending) > 256
	for p := range pending {
		base := strings.ToLower(filepath.Base(p))
		if base == ".gitignore" || base == ".tidalbridgeignore" {
			full = true
			break
		}
	}
	if !full {
		var rescan bool
		changes, rescan = x.update(pending)
		full = rescan
	}
	if full {
		// update may already have applied some entries; the rescan diff
		// covers everything else, so both sets are reported.
		more, _ := x.rescan(context.Background())
		changes = append(changes, more...)
	}
	x.notify(changes)
}

func (x *Index) notify(changes []Change) {
	if len(changes) == 0 {
		return
	}
	x.mu.Lock()
	listeners := make([]func([]Change), 0, len(x.listeners))
	for _, fn := range x.listeners {
		listeners = append(listeners, fn)
	}
	x.mu.Unlock()
	for _, fn := range listeners {
		fn(changes)
	}
}

// rescan replaces the snapshot with a fresh scan and returns the diff.
func (x *Index) rescan(ctx context.Context) ([]Change, error) {
	m, err := ScanWith(ctx, x.root, x.opts)
	if err != nil {
		return nil, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	next := make(map[string]protocol.FileEntry, len(m.Files))
	var changes []Change
	for _, f := range m.Files {
		next[f.Path] = f
		if old, ok := x.files[f.Path]; !ok || old.Hash != f.Hash || old.Executable != f.Executable {
			entry := f
			changes = append(changes, Change{Path: f.Path, Entry: &entry})
		}
	}
	for path := range x.files {
		if _, ok := next[path]; !ok {
			changes = append(changes, Change{Path: path})
		}
	}
	x.files = next
	x.manifest = &m
	x.nested, x.effective, x.overrides = map[string][]rule{}, map[string][]rule{}, nil
	x.rescanned = time.Now()
	return changes, nil
}

// rulesFor returns the ignore rules that apply inside dir: the defaults, every
// .gitignore down to dir (read lazily and cached), then the project's
// .tidalbridgeignore, which decides last as in a full scan.
func (x *Index) rulesFor(dir string) []rule {
	if r, ok := x.effective[dir]; ok {
		return r
	}
	if x.overrides == nil {
		x.overrides = append([]rule{}, projectRules(x.root)...)
	}
	r := append(append([]rule{}, x.gitRules(dir)...), x.overrides...)
	x.effective[dir] = r
	return r
}

// packageDir: see pythonPackage; only built-in and project rules can hide it.
func (x *Index) packageDir(dir string) bool {
	x.rulesFor("")
	return pythonPackage(x.root, dir, append(compile("", defaultIgnore), x.overrides...))
}

func (x *Index) gitRules(dir string) []rule {
	if r, ok := x.nested[dir]; ok {
		return r
	}
	var r []rule
	if dir == "" {
		r = compile("", defaultIgnore)
	} else {
		parent := filepath.ToSlash(filepath.Dir(dir))
		if parent == "." {
			parent = ""
		}
		r = x.gitRules(parent)
	}
	if b, e := os.ReadFile(filepath.Join(x.root, filepath.FromSlash(dir), ".gitignore")); e == nil {
		r = append(append([]rule{}, r...), compile(dir, strings.Split(string(b), "\n"))...)
	}
	x.nested[dir] = r
	return r
}

// Included reports whether a workspace-relative path would be synced.
func (x *Index) Included(path string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.included(path)
}

func (x *Index) included(path string) bool {
	if !SafePath(path) || secretPath(path, x.opts.IncludeEnv) {
		return false
	}
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		dir := strings.Join(parts[:i], "/")
		parent := strings.Join(parts[:i-1], "/")
		if ignoredBy(x.rulesFor(parent), dir, true) && (x.git || !x.packageDir(dir)) || isRustTarget(x.root, dir) {
			return false
		}
	}
	parent := strings.Join(parts[:len(parts)-1], "/")
	return !ignoredBy(x.rulesFor(parent), path, false)
}

// update applies individual notifications. It asks for a rescan when a
// directory appeared (a rename or a copied tree).
func (x *Index) update(paths map[string]bool) ([]Change, bool) {
	cache := cacheFor(x.opts.CacheDir, x.root)
	if cache != nil {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		cache.load()
		defer cache.save()
	}
	var changes []Change
	x.mu.Lock()
	defer x.mu.Unlock()
	for path := range paths {
		full := filepath.Join(x.root, filepath.FromSlash(path))
		info, err := os.Lstat(full)
		if err == nil && info.IsDir() {
			if x.included(path + "/placeholder") {
				if len(changes) > 0 {
					x.manifest = nil
				}
				return changes, true
			}
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() > MaxFileBytes || !x.included(path) {
			// Removed (a file, or a whole directory) or no longer eligible.
			for existing := range x.files {
				if existing == path || strings.HasPrefix(existing, path+"/") {
					delete(x.files, existing)
					changes = append(changes, Change{Path: existing})
				}
			}
			continue
		}
		hash := ""
		if cache != nil {
			if c, ok := cache.entries[path]; ok && c.Size == info.Size() && c.ModNs == info.ModTime().UnixNano() {
				hash = c.Hash
			}
		}
		if hash == "" {
			f, err := os.Open(full)
			if err != nil {
				continue // being written; a later notification follows
			}
			digest := sha256.New()
			_, err = io.CopyBuffer(digest, f, make([]byte, 64*1024))
			f.Close()
			if err != nil {
				continue
			}
			hash = hex.EncodeToString(digest.Sum(nil))
			if cache != nil {
				cache.entries[path] = cacheEntry{Size: info.Size(), ModNs: info.ModTime().UnixNano(), Hash: hash}
				cache.dirty = true
			}
		}
		entry := protocol.FileEntry{Path: path, Hash: hash, Size: info.Size(), Executable: info.Mode()&0111 != 0}
		if old, ok := x.files[path]; ok && old.Hash == entry.Hash && old.Executable == entry.Executable {
			continue
		}
		x.files[path] = entry
		changes = append(changes, Change{Path: path, Entry: &entry})
	}
	if len(changes) > 0 {
		x.manifest = nil
	}
	return changes, false
}
