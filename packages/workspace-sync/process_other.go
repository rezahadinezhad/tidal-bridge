//go:build !windows

package workspacesync

import "os/exec"

func prepareCommand(cmd *exec.Cmd) {}
