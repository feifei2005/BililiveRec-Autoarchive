//go:build !windows

package ffmpeg

import "os/exec"

func hideWindow(cmd *exec.Cmd) {
	// 非 Windows 平台不需要特殊处理
}
