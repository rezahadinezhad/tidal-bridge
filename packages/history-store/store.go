package historystore

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
)

// RollingWriter bounds the audit log without retaining records in memory.
type RollingWriter struct {
	mu    sync.Mutex
	Path  string
	Limit int64
	file  *os.File
	size  int64
}

func (w *RollingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *RollingWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		if e := w.open(); e != nil {
			return 0, e
		}
	}
	if w.size+int64(len(b)) > w.Limit {
		w.file.Close()
		os.Remove(w.Path + ".1")
		if e := os.Rename(w.Path, w.Path+".1"); e != nil {
			return 0, e
		}
		w.file = nil
		w.size = 0
		if e := w.open(); e != nil {
			return 0, e
		}
	}
	n, e := w.file.Write(b)
	w.size += int64(n)
	return n, e
}
func (w *RollingWriter) open() error {
	file, e := os.OpenFile(w.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	w.file = file
	if info, e := file.Stat(); e == nil {
		w.size = info.Size()
	}
	return nil
}
func Prune(dir string, keep int, active map[string]bool) error {
	root, e := filepath.Abs(dir)
	if e != nil {
		return e
	}
	files, e := filepath.Glob(filepath.Join(root, "jobs", "*.json"))
	if e != nil {
		return e
	}
	if len(files) <= keep {
		return nil
	}
	type record struct {
		path     string
		modified int64
	}
	records := []record{}
	for _, path := range files {
		if info, e := os.Stat(path); e == nil {
			records = append(records, record{path, info.ModTime().UnixNano()})
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].modified < records[j].modified })
	removed := 0
	idPattern := regexp.MustCompile(`^[a-f0-9]{48}$`)
	for _, r := range records {
		if removed >= len(files)-keep {
			break
		}
		id := filepath.Base(r.path)
		id = id[:len(id)-5]
		if active[id] || !idPattern.MatchString(id) {
			continue
		}
		target := filepath.Join(root, "results", id)
		relative, e := filepath.Rel(root, target)
		if e != nil || filepath.IsAbs(relative) || relative == ".." {
			return fmt.Errorf("invalid retention target")
		}
		if e = os.Remove(r.path); e != nil {
			return e
		}
		if e = os.RemoveAll(target); e != nil {
			return e
		}
		removed++
	}
	return nil
}
