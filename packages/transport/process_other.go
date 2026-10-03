//go:build !windows

package transport

import "os/exec"

func HideConsole(cmd *exec.Cmd) {}
