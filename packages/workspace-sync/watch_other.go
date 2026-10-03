//go:build !windows

package workspacesync

import "errors"

// Without a native watcher the index rescans on demand (with the hash cache).
func watch(root string, report func(paths []string, overflow bool)) (func(), error) {
	return nil, errors.New("file watching is implemented for Windows hosts")
}
