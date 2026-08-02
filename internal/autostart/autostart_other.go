//go:build !windows

// Package autostart provides a clear unsupported implementation outside Windows.
package autostart

import "fmt"

type AutoStart struct {
	appPath string
}

func New(appPath string) (*AutoStart, error) {
	return &AutoStart{appPath: appPath}, nil
}

func (a *AutoStart) IsEnabled() (bool, error) { return false, nil }

func (a *AutoStart) Enable() error {
	return fmt.Errorf("当前系统不支持此开机自启动设置")
}

func (a *AutoStart) Disable() error {
	return fmt.Errorf("当前系统不支持此开机自启动设置")
}

func (a *AutoStart) Toggle() (bool, error) {
	return false, fmt.Errorf("当前系统不支持此开机自启动设置")
}

func (a *AutoStart) GetAppPath() string { return a.appPath }

func EnableAutoStart() error { return fmt.Errorf("当前系统不支持此开机自启动设置") }

func DisableAutoStart() error { return fmt.Errorf("当前系统不支持此开机自启动设置") }

func IsAutoStartEnabled() (bool, error) { return false, nil }
