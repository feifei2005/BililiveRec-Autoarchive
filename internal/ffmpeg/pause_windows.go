//go:build windows

// Package ffmpeg 提供 FFmpeg 封装功能
package ffmpeg

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"
)

// Windows 常量定义
const (
	PROCESS_SUSPEND_RESUME = 0x0800
)

var (
	modNtdll             = syscall.NewLazyDLL("ntdll.dll")
	procNtSuspendProcess = modNtdll.NewProc("NtSuspendProcess")
	procNtResumeProcess  = modNtdll.NewProc("NtResumeProcess")
)

// SuspendProcess 暂停进程（Windows 实现）
// 使用 NtSuspendProcess 暂停整个进程
func SuspendProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("process is nil")
	}

	// 获取进程句柄
	handle, err := syscall.OpenProcess(PROCESS_SUSPEND_RESUME, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("failed to open process: %w", err)
	}
	defer syscall.CloseHandle(handle)

	// 调用 NtSuspendProcess
	ret, _, err := procNtSuspendProcess.Call(uintptr(handle))
	if ret != 0 {
		return fmt.Errorf("NtSuspendProcess failed with status 0x%x: %v", ret, err)
	}

	return nil
}

// ResumeProcess 恢复进程（Windows 实现）
// 使用 NtResumeProcess 恢复整个进程
func ResumeProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("process is nil")
	}

	// 获取进程句柄
	handle, err := syscall.OpenProcess(PROCESS_SUSPEND_RESUME, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("failed to open process: %w", err)
	}
	defer syscall.CloseHandle(handle)

	// 调用 NtResumeProcess
	ret, _, err := procNtResumeProcess.Call(uintptr(handle))
	if ret != 0 {
		return fmt.Errorf("NtResumeProcess failed with status 0x%x: %v", ret, err)
	}

	return nil
}

// SuspendProcessByPid 通过 PID 暂停进程（Windows 实现）
func SuspendProcessByPid(pid int) error {
	// 获取进程句柄
	handle, err := syscall.OpenProcess(PROCESS_SUSPEND_RESUME, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("failed to open process: %w", err)
	}
	defer syscall.CloseHandle(handle)

	// 调用 NtSuspendProcess
	ret, _, err := procNtSuspendProcess.Call(uintptr(handle))
	if ret != 0 {
		return fmt.Errorf("NtSuspendProcess failed with status 0x%x: %v", ret, err)
	}

	return nil
}

// ResumeProcessByPid 通过 PID 恢复进程（Windows 实现）
func ResumeProcessByPid(pid int) error {
	// 获取进程句柄
	handle, err := syscall.OpenProcess(PROCESS_SUSPEND_RESUME, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("failed to open process: %w", err)
	}
	defer syscall.CloseHandle(handle)

	// 调用 NtResumeProcess
	ret, _, err := procNtResumeProcess.Call(uintptr(handle))
	if ret != 0 {
		return fmt.Errorf("NtResumeProcess failed with status 0x%x: %v", ret, err)
	}

	return nil
}

// IsProcessSuspendSupported 检查是否支持进程暂停功能
func IsProcessSuspendSupported() bool {
	// Windows 通过 NtSuspendProcess 支持
	return procNtSuspendProcess.Find() == nil && procNtResumeProcess.Find() == nil
}

// 确保 unsafe 包被使用（用于后续可能的扩展）
var _ = unsafe.Sizeof(0)
