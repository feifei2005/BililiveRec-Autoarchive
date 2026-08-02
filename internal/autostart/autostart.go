//go:build windows

// Package autostart 提供 Windows 开机自启动功能
package autostart

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const (
	// 注册表路径
	registryPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	// 应用名称（在注册表中的键名）
	appName = "BililiveRecorderAutoArchive"
)

// AutoStart 开机自启动管理器
type AutoStart struct {
	appPath string // 应用程序可执行文件路径
}

// New 创建新的开机自启动管理器
// appPath 为空时自动获取当前可执行文件路径
func New(appPath string) (*AutoStart, error) {
	if appPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("获取可执行文件路径失败: %w", err)
		}
		appPath, err = filepath.Abs(exe)
		if err != nil {
			return nil, fmt.Errorf("获取绝对路径失败: %w", err)
		}
	}

	return &AutoStart{
		appPath: appPath,
	}, nil
}

// IsEnabled 检查开机自启动是否已启用
func (a *AutoStart) IsEnabled() (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, registryPath, registry.QUERY_VALUE)
	if err != nil {
		return false, fmt.Errorf("打开注册表失败: %w", err)
	}
	defer key.Close()

	val, _, err := key.GetStringValue(appName)
	if err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, fmt.Errorf("读取注册表值失败: %w", err)
	}

	// 检查路径是否匹配（可能路径已更改）
	return val == a.appPath, nil
}

// Enable 启用开机自启动
// 在注册表 HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Run 中添加启动项
func (a *AutoStart) Enable() error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, registryPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("创建注册表键失败: %w", err)
	}
	defer key.Close()

	err = key.SetStringValue(appName, a.appPath)
	if err != nil {
		return fmt.Errorf("设置注册表值失败: %w", err)
	}

	return nil
}

// Disable 禁用开机自启动
// 从注册表中删除启动项
func (a *AutoStart) Disable() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, registryPath, registry.SET_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return nil // 键不存在，无需删除
		}
		return fmt.Errorf("打开注册表失败: %w", err)
	}
	defer key.Close()

	err = key.DeleteValue(appName)
	if err != nil {
		if err == registry.ErrNotExist {
			return nil // 值不存在，无需删除
		}
		return fmt.Errorf("删除注册表值失败: %w", err)
	}

	return nil
}

// Toggle 切换开机自启动状态
// 返回切换后的状态
func (a *AutoStart) Toggle() (bool, error) {
	enabled, err := a.IsEnabled()
	if err != nil {
		return false, err
	}

	if enabled {
		err = a.Disable()
		return false, err
	}

	err = a.Enable()
	return true, err
}

// GetAppPath 获取应用程序路径
func (a *AutoStart) GetAppPath() string {
	return a.appPath
}

// EnableAutoStart 便捷函数：启用开机自启动
func EnableAutoStart() error {
	as, err := New("")
	if err != nil {
		return err
	}
	return as.Enable()
}

// DisableAutoStart 便捷函数：禁用开机自启动
func DisableAutoStart() error {
	as, err := New("")
	if err != nil {
		return err
	}
	return as.Disable()
}

// IsAutoStartEnabled 便捷函数：检查开机自启动是否启用
func IsAutoStartEnabled() (bool, error) {
	as, err := New("")
	if err != nil {
		return false, err
	}
	return as.IsEnabled()
}
