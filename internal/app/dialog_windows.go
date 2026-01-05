//go:build windows
// +build windows

package app

import (
	"log"
	"syscall"
	"unsafe"
)

var (
	shell32             = syscall.NewLazyDLL("shell32.dll")
	ole32               = syscall.NewLazyDLL("ole32.dll")
	shBrowseForFolder   = shell32.NewProc("SHBrowseForFolderW")
	shGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")
	coInitialize        = ole32.NewProc("CoInitialize")
	coUninitialize      = ole32.NewProc("CoUninitialize")
)

// BROWSEINFO 结构体
type browseInfo struct {
	HwndOwner      uintptr
	PidlRoot       uintptr
	PszDisplayName *uint16
	LpszTitle      *uint16
	UlFlags        uint32
	Lpfn           uintptr
	LParam         uintptr
	IImage         int32
}

const (
	BIF_RETURNONLYFSDIRS  = 0x00000001
	BIF_NEWDIALOGSTYLE    = 0x00000040
	BIF_EDITBOX           = 0x00000010
	BIF_NONEWFOLDERBUTTON = 0x00000200
)

// selectFolderDialog 使用Windows原生API打开文件夹选择对话框
func selectFolderDialog() string {
	// 初始化 COM
	coInitialize.Call(0)
	defer coUninitialize.Call()

	// 准备标题
	title, err := syscall.UTF16PtrFromString("选择要转码的文件夹")
	if err != nil {
		log.Printf("转换标题失败: %v", err)
		return ""
	}

	// 准备显示名称缓冲区
	displayName := make([]uint16, syscall.MAX_PATH)

	// 设置 BROWSEINFO 结构
	bi := browseInfo{
		HwndOwner:      0,
		PidlRoot:       0,
		PszDisplayName: &displayName[0],
		LpszTitle:      title,
		UlFlags:        BIF_RETURNONLYFSDIRS | BIF_NEWDIALOGSTYLE | BIF_EDITBOX,
		Lpfn:           0,
		LParam:         0,
		IImage:         0,
	}

	// 调用 SHBrowseForFolder
	pidl, _, _ := shBrowseForFolder.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		// 用户取消了选择
		return ""
	}

	// 从 PIDL 获取路径
	path := make([]uint16, syscall.MAX_PATH)
	ret, _, _ := shGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&path[0])))
	if ret == 0 {
		log.Println("获取文件夹路径失败")
		return ""
	}

	// 转换为 Go 字符串
	result := syscall.UTF16ToString(path)
	return result
}
