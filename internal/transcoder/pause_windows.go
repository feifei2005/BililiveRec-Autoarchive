//go:build windows

// Package transcoder 提供视频转码功能
package transcoder

import (
	"os/exec"
	"syscall"
	"unsafe"
)

// Windows API 常量
const (
	processAccessSuspendResume = 0x0800
)

var (
	ntdll            = syscall.NewLazyDLL("ntdll.dll")
	ntSuspendProcess = ntdll.NewProc("NtSuspendProcess")
	ntResumeProcess  = ntdll.NewProc("NtResumeProcess")
)

// suspendProcess 暂停进程（Windows 实现）
func suspendProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	handle, err := syscall.OpenProcess(processAccessSuspendResume, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(handle)

	r, _, err := ntSuspendProcess.Call(uintptr(handle))
	if r != 0 {
		return err
	}
	return nil
}

// resumeProcess 恢复进程（Windows 实现）
func resumeProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	handle, err := syscall.OpenProcess(processAccessSuspendResume, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(handle)

	r, _, err := ntResumeProcess.Call(uintptr(handle))
	if r != 0 {
		return err
	}
	return nil
}

// 确保 unsafe 包被使用（可能在其他地方需要）
var _ = unsafe.Sizeof(0)
