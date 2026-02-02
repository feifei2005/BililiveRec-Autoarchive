//go:build !windows
// +build !windows

package app

import (
	"fmt"
	"os/exec"
	"runtime"
)

// selectFolderDialog 非Windows平台的实现（返回空字符串）
func selectFolderDialog() string {
	return ""
}

// openFileInExplorer 在文件管理器中打开并选中指定文件
func openFileInExplorer(filePath string) error {
	switch runtime.GOOS {
	case "darwin":
		// macOS: 使用 open -R 命令在 Finder 中显示并选中文件
		return exec.Command("open", "-R", filePath).Run()
	case "linux":
		// Linux: 尝试使用 xdg-open 打开文件所在目录
		// 注意：xdg-open 无法直接选中文件，只能打开目录
		return exec.Command("xdg-open", filePath).Run()
	default:
		return fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
	}
}
