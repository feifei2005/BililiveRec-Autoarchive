//go:build !windows

package webhook

import "os/exec"

func hidePowerShellWindow(cmd *exec.Cmd) {
	// 非 Windows 平台无需隐藏窗口
}
