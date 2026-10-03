//go:build windows

package transport

import (
	"os/exec"
	"syscall"
)

// HideConsole stops console helpers (adb) from opening windows when the
// caller is the windowless host service.
func HideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
