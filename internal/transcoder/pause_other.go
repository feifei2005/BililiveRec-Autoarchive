//go:build !windows

// Package transcoder 提供视频转码功能
package transcoder

import (
	"os/exec"
	"syscall"
)

// suspendProcess 暂停进程（Unix 实现，使用 SIGSTOP）
func suspendProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(cmd.Process.Pid, syscall.SIGSTOP)
}

// resumeProcess 恢复进程（Unix 实现，使用 SIGCONT）
func resumeProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(cmd.Process.Pid, syscall.SIGCONT)
}
