package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"tidalbridge/packages/protocol"
	workspacesync "tidalbridge/packages/workspace-sync"
)

// planWriteBack checks a worker's changes against the manifest the job ran
// with. Every changed or deleted file must still hold that content on the
// laptop, and a created file must be new and a synced path; otherwise
// nothing is applied, so an edit made on the laptop meanwhile is never lost.
// Created files the workspace does not sync (caches, ignored output) stay on
// the worker.
func planWriteBack(root string, manifest *protocol.Manifest, changes protocol.WorkerChanges, included func(string) bool) (writes []protocol.FileEntry, deletes []string, conflict string) {
	synced := map[string]string{}
	for _, f := range manifest.Files {
		synced[f.Path] = f.Hash
	}
	unchanged := func(path string) bool {
		want, ok := synced[path]
		if !ok {
			return false
		}
		hash, err := hashFile(filepath.Join(root, filepath.FromSlash(path)))
		return err == nil && hash == want
	}
	for _, f := range changes.Modified {
		if !workspacesync.SafePath(f.Path) {
			return nil, nil, "unsafe path " + f.Path
		}
		if !unchanged(f.Path) {
			return nil, nil, f.Path + " changed on this laptop while the phone ran the command"
		}
		writes = append(writes, f)
	}
	for _, path := range changes.Deleted {
		if !workspacesync.SafePath(path) {
			return nil, nil, "unsafe path " + path
		}
		if !unchanged(path) {
			return nil, nil, path + " changed on this laptop while the phone ran the command"
		}
		deletes = append(deletes, path)
	}
	for _, f := range changes.Created {
		if !workspacesync.SafePath(f.Path) {
			return nil, nil, "unsafe path " + f.Path
		}
		if !included(f.Path) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f.Path))); err == nil {
			return nil, nil, f.Path + " was created on this laptop while the phone ran the command"
		}
		writes = append(writes, f)
	}
	return writes, deletes, ""
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// writeBack applies a finished job's changes to the laptop workspace: all
// content is downloaded and verified first, then moved into place.
func (h *Host) writeBack(ctx context.Context, node protocol.WorkerNode, remoteID string, spec protocol.JobSpec, m *protocol.Manifest, changes protocol.WorkerChanges, errout io.Writer) *protocol.WriteBackResult {
	included := func(string) bool { return false }
	if idx, err := h.workspaceIndex(ctx, spec.Workspace, spec.Policy.SyncEnvFiles, time.Second); err == nil {
		included = idx.Included
	}
	writes, deletes, conflict := planWriteBack(spec.Workspace, m, changes, included)
	staged := map[string]string{}
	defer func() {
		for _, temporary := range staged {
			os.Remove(temporary)
		}
	}()
	for _, f := range writes {
		if conflict != "" {
			break
		}
		destination := filepath.Join(spec.Workspace, filepath.FromSlash(f.Path))
		temporary := destination + ".tidalbridge-tmp"
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			conflict = err.Error()
			break
		}
		if err := h.download(ctx, node, "/v1/jobs/"+remoteID+"/artifact?path="+url.QueryEscape(f.Path), temporary); err != nil {
			conflict = "copying " + f.Path + " back failed: " + err.Error()
			break
		}
		staged[f.Path] = temporary
		if hash, err := hashFile(temporary); err != nil || hash != f.Hash {
			conflict = f.Path + " changed on the phone while it was copied back"
		}
	}
	if conflict != "" {
		fmt.Fprintf(errout, "[Tidal Bridge] Not copying the phone's changes back: %s.\n", conflict)
		return &protocol.WriteBackResult{Conflict: conflict}
	}
	result := &protocol.WriteBackResult{}
	for path, temporary := range staged {
		if err := os.Rename(temporary, filepath.Join(spec.Workspace, filepath.FromSlash(path))); err != nil {
			fmt.Fprintf(errout, "[Tidal Bridge] Could not update %s: %v\n", path, err)
			continue
		}
		delete(staged, path)
		result.Applied = append(result.Applied, path)
	}
	for _, path := range deletes {
		if err := os.Remove(filepath.Join(spec.Workspace, filepath.FromSlash(path))); err == nil || os.IsNotExist(err) {
			result.Deleted = append(result.Deleted, path)
		}
	}
	if n := len(result.Applied) + len(result.Deleted); n > 0 {
		fmt.Fprintf(errout, "[Tidal Bridge] Copied %d changed file(s) back from the phone.\n", n)
	}
	return result
}
