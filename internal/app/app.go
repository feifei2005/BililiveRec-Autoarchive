// Package app 提供 Wails 应用绑定接口
package app

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/user/bililive-recorder-autoarchive/internal/autostart"
	"github.com/user/bililive-recorder-autoarchive/internal/config"
	"github.com/user/bililive-recorder-autoarchive/internal/processor"
	"github.com/user/bililive-recorder-autoarchive/internal/storage"
)

// App 应用程序结构体，用于 Wails 绑定
// 所有公开方法都可以从前端 JavaScript 调用
type App struct {
	ctx       context.Context
	config    *config.Config
	processor *processor.DefaultProcessor
	storage   storage.Storage
	autostart *autostart.AutoStart

	// 回调函数
	onQuit       func()
	onScanNow    func()
	onShowWindow func()
	onHideWindow func()
}

// NewApp 创建新的应用实例
func NewApp() *App {
	return &App{}
}

// SetConfig 设置配置
func (a *App) SetConfig(cfg *config.Config) {
	a.config = cfg
}

// SetProcessor 设置处理器
func (a *App) SetProcessor(p *processor.DefaultProcessor) {
	a.processor = p
}

// SetStorage 设置存储
func (a *App) SetStorage(s storage.Storage) {
	a.storage = s
}

// SetAutostart 设置开机自启动管理器
func (a *App) SetAutostart(as *autostart.AutoStart) {
	a.autostart = as
}

// SetCallbacks 设置回调函数
func (a *App) SetCallbacks(onQuit, onScanNow, onShowWindow, onHideWindow func()) {
	a.onQuit = onQuit
	a.onScanNow = onScanNow
	a.onShowWindow = onShowWindow
	a.onHideWindow = onHideWindow
}

// Startup 应用启动时由 Wails 调用
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	log.Println("Wails 应用已启动")
}

// Shutdown 应用关闭时由 Wails 调用
func (a *App) Shutdown(ctx context.Context) {
	log.Println("Wails 应用正在关闭")
}

// ================== 配置相关 API ==================

// ConfigData 配置数据传输对象
type ConfigData struct {
	InputDir         string   `json:"inputDir"`
	OutputRoot       string   `json:"outputRoot"`
	DiscardDir       string   `json:"discardDir"`
	MaxConcurrent    int      `json:"maxConcurrent"`
	MinFileSizeKB    int64    `json:"minFileSizeKB"`
	CheckVideoStream bool     `json:"checkVideoStream"`
	ServerPort       int      `json:"serverPort"`
	WebhookPath      string   `json:"webhookPath"`
	FFmpegPath       string   `json:"ffmpegPath"`
	FFprobePath      string   `json:"ffprobePath"`
	CustomArgs       []string `json:"customArgs"`
	DefaultCover     string   `json:"defaultCover"`
	PathTemplate     string   `json:"pathTemplate"`
}

// GetConfig 获取当前配置
func (a *App) GetConfig() ConfigData {
	if a.config == nil {
		return ConfigData{}
	}

	return ConfigData{
		InputDir:         a.config.Processing.InputDir,
		OutputRoot:       a.config.Processing.OutputRoot,
		DiscardDir:       a.config.Processing.DiscardDir,
		MaxConcurrent:    a.config.Processing.MaxConcurrent,
		MinFileSizeKB:    a.config.Processing.MinFileSizeKB,
		CheckVideoStream: a.config.Processing.CheckVideoStream,
		ServerPort:       a.config.Server.Port,
		WebhookPath:      a.config.Server.WebhookPath,
		FFmpegPath:       a.config.FFmpeg.Path,
		FFprobePath:      a.config.FFmpeg.FFprobePath,
		CustomArgs:       a.config.FFmpeg.CustomArgs,
		DefaultCover:     a.config.Covers.DefaultCover,
		PathTemplate:     a.config.Rules.PathTemplate,
	}
}

// SaveConfig 保存配置
func (a *App) SaveConfig(data ConfigData) error {
	if a.config == nil {
		return fmt.Errorf("配置未初始化")
	}

	// 更新配置
	a.config.Processing.InputDir = data.InputDir
	a.config.Processing.OutputRoot = data.OutputRoot
	a.config.Processing.DiscardDir = data.DiscardDir
	a.config.Processing.MaxConcurrent = data.MaxConcurrent
	a.config.Processing.MinFileSizeKB = data.MinFileSizeKB
	a.config.Processing.CheckVideoStream = data.CheckVideoStream
	a.config.Server.Port = data.ServerPort
	a.config.Server.WebhookPath = data.WebhookPath
	a.config.FFmpeg.Path = data.FFmpegPath
	a.config.FFmpeg.FFprobePath = data.FFprobePath
	a.config.FFmpeg.CustomArgs = data.CustomArgs
	a.config.Covers.DefaultCover = data.DefaultCover
	a.config.Rules.PathTemplate = data.PathTemplate

	// 保存到文件
	configPath := filepath.Join(".", "config.yaml")
	if err := a.config.Save(configPath); err != nil {
		return fmt.Errorf("保存配置失败: %w", err)
	}

	log.Println("配置已保存")
	return nil
}

// ================== 任务相关 API ==================

// TaskInfo 任务信息传输对象
type TaskInfo struct {
	ID           string  `json:"id"`
	InputPath    string  `json:"inputPath"`
	OutputPath   string  `json:"outputPath"`
	StreamerName string  `json:"streamerName"`
	Status       string  `json:"status"`
	Progress     float64 `json:"progress"`
	Error        string  `json:"error"`
	CreatedAt    string  `json:"createdAt"`
	UpdatedAt    string  `json:"updatedAt"`
}

// GetTasks 获取所有任务
func (a *App) GetTasks() []TaskInfo {
	if a.processor == nil {
		return []TaskInfo{}
	}

	tasks := a.processor.Tasks()
	result := make([]TaskInfo, 0, len(tasks))

	for _, task := range tasks {
		errMsg := ""
		if task.Error != nil {
			errMsg = task.Error.Error()
		}

		result = append(result, TaskInfo{
			ID:           task.ID,
			InputPath:    task.InputPath,
			OutputPath:   task.OutputPath,
			StreamerName: task.StreamerName,
			Status:       string(task.Status),
			Progress:     task.Progress,
			Error:        errMsg,
			CreatedAt:    task.CreatedAt.Format(time.RFC3339),
			UpdatedAt:    task.UpdatedAt.Format(time.RFC3339),
		})
	}

	return result
}

// ScanNow 立即执行扫描
func (a *App) ScanNow() {
	log.Println("前端请求立即扫描")
	if a.onScanNow != nil {
		a.onScanNow()
	}
}

// ================== 开机自启动 API ==================

// IsAutoStartEnabled 检查开机自启动是否启用
func (a *App) IsAutoStartEnabled() bool {
	if a.autostart == nil {
		enabled, _ := autostart.IsAutoStartEnabled()
		return enabled
	}
	enabled, _ := a.autostart.IsEnabled()
	return enabled
}

// SetAutoStart 设置开机自启动
func (a *App) SetAutoStart(enabled bool) error {
	var err error
	if a.autostart == nil {
		if enabled {
			err = autostart.EnableAutoStart()
		} else {
			err = autostart.DisableAutoStart()
		}
	} else {
		if enabled {
			err = a.autostart.Enable()
		} else {
			err = a.autostart.Disable()
		}
	}

	if err != nil {
		return fmt.Errorf("设置开机自启动失败: %w", err)
	}

	log.Printf("开机自启动已%s", map[bool]string{true: "启用", false: "禁用"}[enabled])
	return nil
}

// ToggleAutoStart 切换开机自启动状态
func (a *App) ToggleAutoStart() (bool, error) {
	if a.autostart == nil {
		as, err := autostart.New("")
		if err != nil {
			return false, err
		}
		return as.Toggle()
	}
	return a.autostart.Toggle()
}

// ================== 错误日志 API ==================

// ErrorLogEntry 错误日志条目
type ErrorLogEntry struct {
	ID        int64  `json:"id"`
	Timestamp string `json:"timestamp"`
	FilePath  string `json:"filePath"`
	Error     string `json:"error"`
	Status    string `json:"status"`
}

// GetErrorLogs 获取错误日志列表（使用失败的处理日志）
func (a *App) GetErrorLogs(limit int) []ErrorLogEntry {
	if a.storage == nil {
		return []ErrorLogEntry{}
	}

	logs, err := a.storage.ListFailedLogs(a.ctx, limit)
	if err != nil {
		log.Printf("获取错误日志失败: %v", err)
		return []ErrorLogEntry{}
	}

	result := make([]ErrorLogEntry, 0, len(logs))
	for _, entry := range logs {
		result = append(result, ErrorLogEntry{
			ID:        entry.ID,
			Timestamp: entry.CreatedAt.Format(time.RFC3339),
			FilePath:  entry.InputPath,
			Error:     entry.Error,
			Status:    entry.Status,
		})
	}

	return result
}

// ================== 窗口控制 API ==================

// Quit 退出应用
func (a *App) Quit() {
	log.Println("前端请求退出应用")
	if a.onQuit != nil {
		a.onQuit()
	}
}

// ShowWindow 显示窗口
func (a *App) ShowWindow() {
	if a.onShowWindow != nil {
		a.onShowWindow()
	}
}

// HideWindow 隐藏窗口
func (a *App) HideWindow() {
	if a.onHideWindow != nil {
		a.onHideWindow()
	}
}

// ================== 系统信息 API ==================

// SystemInfo 系统信息
type SystemInfo struct {
	Version      string `json:"version"`
	BuildTime    string `json:"buildTime"`
	GoVersion    string `json:"goVersion"`
	ConfigPath   string `json:"configPath"`
	DatabasePath string `json:"databasePath"`
}

// 版本信息变量，通过 ldflags 注入
var (
	Version   = "dev"
	BuildTime = "unknown"
)

// GetSystemInfo 获取系统信息
func (a *App) GetSystemInfo() SystemInfo {
	return SystemInfo{
		Version:      Version,
		BuildTime:    BuildTime,
		GoVersion:    "go1.24",
		ConfigPath:   "config.yaml",
		DatabasePath: "data.db",
	}
}

// ================== 统计信息 API ==================

// Stats 统计信息
type Stats struct {
	TotalProcessed  int     `json:"totalProcessed"`
	TotalFailed     int     `json:"totalFailed"`
	TotalDiscarded  int     `json:"totalDiscarded"`
	PendingTasks    int     `json:"pendingTasks"`
	ProcessingTasks int     `json:"processingTasks"`
	TotalSizeGB     float64 `json:"totalSizeGB"`
}

// GetStats 获取统计信息
func (a *App) GetStats() Stats {
	stats := Stats{}

	if a.processor != nil {
		tasks := a.processor.Tasks()
		for _, task := range tasks {
			switch task.Status {
			case processor.StatusCompleted:
				stats.TotalProcessed++
			case processor.StatusFailed:
				stats.TotalFailed++
			case processor.StatusDiscarded:
				stats.TotalDiscarded++
			case processor.StatusPending:
				stats.PendingTasks++
			case processor.StatusProcessing:
				stats.ProcessingTasks++
			}
		}
	}

	return stats
}
