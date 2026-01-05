//go:build windows

package webhook

import (
	"os/exec"
	"syscall"
)

func hidePowerShellWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
