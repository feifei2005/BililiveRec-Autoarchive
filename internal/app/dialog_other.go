//go:build !windows
// +build !windows

package app

// selectFolderDialog 非Windows平台的实现（返回空字符串）
func selectFolderDialog() string {
	return ""
}
