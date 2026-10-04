package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"tidalbridge/packages/config"
	"tidalbridge/packages/protocol"
	workspacesync "tidalbridge/packages/workspace-sync"
)

// Files a project reads from outside its folder, such as a sibling data
// folder in a monorepo. A phone's copy holds the project folder only, so a
// test reading ../planning/... fails there with "no such file". Tidal Bridge
// learns that folder from the failure and from then on copies it beside the
// project, one level up as on this laptop.
const outsideFile = "outside.json"

var (
	unsafeName = regexp.MustCompile(`[^A-Za-z0-9_-]`)
	// A test runner's time limit: the phone is slower than this laptop, and
	// slower still on a fresh copy.
	slowFailure = regexp.MustCompile(`Test timed out in \d+ ?ms|Exceeded timeout of \d+ ?ms|Timeout - Async callback was not invoked|Hook timed out in \d+ ?ms`)
)

func (h *Host) loadOutside() {
	h.outside = map[string][]string{}
	b, _ := os.ReadFile(filepath.Join(h.Dir, outsideFile))
	json.Unmarshal(b, &h.outside)
	if h.outside == nil {
		h.outside = map[string][]string{}
	}
}

func outsideKey(workspace string) string { return strings.ToLower(filepath.Clean(workspace)) }

// missingOutside finds a "no such file" error for a path beside the phone's
// copy of a project and returns it relative to the project ("../...").
func missingOutside(output, copyPath string) (string, bool) {
	if !strings.HasPrefix(copyPath, "/") {
		return "", false
	}
	parent := path.Dir(copyPath) + "/"
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "ENOENT") && !strings.Contains(strings.ToLower(line), "no such file") {
			continue
		}
		i := strings.Index(line, parent)
		if i < 0 {
			continue
		}
		rest := line[i+len(parent):]
		if end := strings.IndexAny(rest, "'\"` \t\r,)"); end >= 0 {
			rest = rest[:end]
		}
		if rest != "" && workspacesync.SafePath(rest) && !strings.HasPrefix(rest+"/", path.Base(copyPath)+"/") {
			return "../" + rest, true
		}
	}
	return "", false
}

// learnOutside reports whether a failed phone run could not find a file that
// exists outside the project here; that file's folder (the file alone when
// the folder is large) is then copied with the project from now on.
func (h *Host) learnOutside(workspace, copyPath, output string) bool {
	rel, ok := missingOutside(output, copyPath)
	if !ok || workspacesync.SecretPath(rel[3:]) {
		return false
	}
	if info, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(rel))); err != nil || !info.Mode().IsRegular() {
		return false
	}
	add := rel
	if dir := path.Dir(rel); dir != ".." && small(filepath.Join(workspace, filepath.FromSlash(dir))) {
		add = dir
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	key := outsideKey(workspace)
	for _, have := range h.outside[key] {
		if have == add || strings.HasPrefix(add, have+"/") {
			return true
		}
	}
	h.outside[key] = append(h.outside[key], add)
	h.log.Info("outside_path_learned", "workspace", workspace, "path", add)
	config.SaveJSON(filepath.Join(h.Dir, outsideFile), h.outside)
	return true
}

// small: a folder of at most 2,000 files and 64 MB.
func small(dir string) bool {
	files, bytes := 0, int64(0)
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files, bytes = files+1, bytes+info.Size()
		if files > 2000 || bytes > 64<<20 {
			return fs.SkipAll
		}
		return nil
	})
	return err == nil && files <= 2000 && bytes <= 64<<20
}

// treeFor is the manifest, tree key and folder name a workspace is copied to
// a phone with: the project one level down, beside the files it reads from
// outside its folder, when it has any.
func (h *Host) treeFor(workspace string, m *protocol.Manifest) (*protocol.Manifest, string, string) {
	key := treeKey(workspace)
	h.mu.RLock()
	extras := slices.Clone(h.outside[outsideKey(workspace)])
	h.mu.RUnlock()
	if len(extras) == 0 {
		return m, key, ""
	}
	out := *m
	out.Files = slices.Clone(m.Files)
	digest := sha256.New()
	digest.Write([]byte(m.ID))
	for _, rel := range extras {
		filepath.WalkDir(filepath.Join(workspace, filepath.FromSlash(rel)), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			r, err := filepath.Rel(workspace, p)
			if err != nil {
				return nil
			}
			r = filepath.ToSlash(r)
			info, err := d.Info()
			if err != nil || !info.Mode().IsRegular() || !strings.HasPrefix(r, "../") || !workspacesync.SafePath(r[3:]) || workspacesync.SecretPath(r[3:]) {
				return nil
			}
			hash, err := hashFile(p)
			if err != nil {
				return nil
			}
			out.Files = append(out.Files, protocol.FileEntry{Path: r, Hash: hash, Size: info.Size()})
			out.TotalBytes += info.Size()
			fmt.Fprintf(digest, "%s %s\n", r, hash)
			return nil
		})
	}
	out.ID = "o" + hex.EncodeToString(digest.Sum(nil))[:40]
	return &out, nestedTreeKey(key), unsafeName.ReplaceAllString(filepath.Base(workspace), "_")
}

func nestedTreeKey(key string) string { return "wo-" + strings.TrimPrefix(key, "ws-") }

// treeKeyFor is the key of the tree treeFor copies a workspace to. Questions
// about a phone's copy (are its dependencies installed?) go to this tree; the
// project's flat tree from before it read outside files is left behind.
func (h *Host) treeKeyFor(workspace string) string {
	h.mu.RLock()
	nested := len(h.outside[outsideKey(workspace)]) > 0
	h.mu.RUnlock()
	if nested {
		return nestedTreeKey(treeKey(workspace))
	}
	return treeKey(workspace)
}
