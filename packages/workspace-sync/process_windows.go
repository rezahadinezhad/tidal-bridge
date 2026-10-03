//go:build windows

package workspacesync

import (
	"os/exec"
	"syscall"
)

// prepareCommand keeps helper consoles hidden: the host service has none.
func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
