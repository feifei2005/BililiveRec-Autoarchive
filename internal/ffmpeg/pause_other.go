//go:build !windows

// Package ffmpeg 提供 FFmpeg 封装功能
package ffmpeg

import (
	"fmt"
	"os/exec"
	"syscall"
)

// SuspendProcess 暂停进程（Linux/Mac 实现）
// 使用 SIGSTOP 信号暂停进程
func SuspendProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("process is nil")
	}

	// 发送 SIGSTOP 信号暂停进程
	err := syscall.Kill(cmd.Process.Pid, syscall.SIGSTOP)
	if err != nil {
		return fmt.Errorf("failed to send SIGSTOP: %w", err)
	}

	return nil
}

// ResumeProcess 恢复进程（Linux/Mac 实现）
// 使用 SIGCONT 信号恢复进程
func ResumeProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("process is nil")
	}

	// 发送 SIGCONT 信号恢复进程
	err := syscall.Kill(cmd.Process.Pid, syscall.SIGCONT)
	if err != nil {
		return fmt.Errorf("failed to send SIGCONT: %w", err)
	}

	return nil
}

// SuspendProcessByPid 通过 PID 暂停进程（Linux/Mac 实现）
func SuspendProcessByPid(pid int) error {
	// 发送 SIGSTOP 信号暂停进程
	err := syscall.Kill(pid, syscall.SIGSTOP)
	if err != nil {
		return fmt.Errorf("failed to send SIGSTOP: %w", err)
	}

	return nil
}

// ResumeProcessByPid 通过 PID 恢复进程（Linux/Mac 实现）
func ResumeProcessByPid(pid int) error {
	// 发送 SIGCONT 信号恢复进程
	err := syscall.Kill(pid, syscall.SIGCONT)
	if err != nil {
		return fmt.Errorf("failed to send SIGCONT: %w", err)
	}

	return nil
}

// IsProcessSuspendSupported 检查是否支持进程暂停功能
func IsProcessSuspendSupported() bool {
	// Linux/Mac 通过 SIGSTOP/SIGCONT 支持
	return true
}
