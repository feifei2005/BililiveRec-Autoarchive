// Package app 提供 Wails 应用绑定接口
package app

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/user/bililive-recorder-autoarchive/internal/autostart"
	"github.com/user/bililive-recorder-autoarchive/internal/config"
	"github.com/user/bililive-recorder-autoarchive/internal/processor"
	"github.com/user/bililive-recorder-autoarchive/internal/storage"
	"github.com/user/bililive-recorder-autoarchive/internal/transcoder"
)

// App 应用程序结构体，用于 Wails 绑定
// 所有公开方法都可以从前端 JavaScript 调用
type App struct {
	ctx        context.Context
	config     *config.Config
	processor  *processor.DefaultProcessor
	storage    storage.Storage
	autostart  *autostart.AutoStart
	transcoder *transcoder.Transcoder

	// 回调函数
	onQuit                    func()
	onScanNow                 func()
	onShowWindow              func()
	onHideWindow              func()
	onShutdownNow             func()                // 立即关闭回调
	onShutdownAfterCompletion func(callback func()) // 等待任务完成后关闭回调
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

// SetTranscoder 设置转码器
func (a *App) SetTranscoder(t *transcoder.Transcoder) {
	a.transcoder = t
}

// GetTranscoder 获取转码器实例
func (a *App) GetTranscoder() *transcoder.Transcoder {
	return a.transcoder
}

// SetCallbacks 设置回调函数
func (a *App) SetCallbacks(onQuit, onScanNow, onShowWindow, onHideWindow func()) {
	a.onQuit = onQuit
	a.onScanNow = onScanNow
	a.onShowWindow = onShowWindow
	a.onHideWindow = onHideWindow
}

// SetShutdownCallbacks 设置关闭相关的回调函数
func (a *App) SetShutdownCallbacks(onShutdownNow func(), onShutdownAfterCompletion func(callback func())) {
	a.onShutdownNow = onShutdownNow
	a.onShutdownAfterCompletion = onShutdownAfterCompletion
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
	InputDir           string   `json:"inputDir"`
	OutputRoot         string   `json:"outputRoot"`
	DiscardDir         string   `json:"discardDir"`
	MaxConcurrent      int      `json:"maxConcurrent"`
	MinFileSizeKB      int64    `json:"minFileSizeKB"`
	CheckVideoStream   bool     `json:"checkVideoStream"`
	DiscardFailedFiles bool     `json:"discardFailedFiles"` // 处理失败时是否进入丢弃流程
	ServerPort         int      `json:"serverPort"`
	WebhookPath        string   `json:"webhookPath"`
	FFmpegPath         string   `json:"ffmpegPath"`
	FFprobePath        string   `json:"ffprobePath"`
	CustomArgs         []string `json:"customArgs"`
	DefaultCover       string   `json:"defaultCover"`
	PathTemplate       string   `json:"pathTemplate"`
}

// GetConfig 获取当前配置
func (a *App) GetConfig() ConfigData {
	if a.config == nil {
		return ConfigData{}
	}

	return ConfigData{
		InputDir:           a.config.Processing.InputDir,
		OutputRoot:         a.config.Processing.OutputRoot,
		DiscardDir:         a.config.Processing.DiscardDir,
		MaxConcurrent:      a.config.Processing.MaxConcurrent,
		MinFileSizeKB:      a.config.Processing.MinFileSizeKB,
		CheckVideoStream:   a.config.Processing.CheckVideoStream,
		DiscardFailedFiles: a.config.Processing.DiscardFailedFiles,
		ServerPort:         a.config.Server.Port,
		WebhookPath:        a.config.Server.WebhookPath,
		FFmpegPath:         a.config.FFmpeg.Path,
		FFprobePath:        a.config.FFmpeg.FFprobePath,
		CustomArgs:         a.config.FFmpeg.CustomArgs,
		DefaultCover:       a.config.Covers.DefaultCover,
		PathTemplate:       a.config.Rules.PathTemplate,
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
	a.config.Processing.DiscardFailedFiles = data.DiscardFailedFiles
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
			case processor.StatusSuccess:
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

// ================== 关闭相关 API ==================

// ShutdownInfo 关闭信息
type ShutdownInfo struct {
	HasActiveTasks bool `json:"hasActiveTasks"`
	ActiveCount    int  `json:"activeCount"`
	PendingCount   int  `json:"pendingCount"`
}

// GetActiveTasksCount 获取正在处理中的任务数量
func (a *App) GetActiveTasksCount() int {
	if a.processor == nil {
		return 0
	}
	return a.processor.GetActiveTasksCount()
}

// GetPendingTasksCount 获取待处理的任务数量
func (a *App) GetPendingTasksCount() int {
	if a.processor == nil {
		return 0
	}
	return a.processor.GetPendingTasksCount()
}

// RequestShutdown 请求关闭应用，返回当前任务状态信息
// 前端可以根据返回的信息决定是否显示确认对话框
func (a *App) RequestShutdown() ShutdownInfo {
	if a.processor == nil {
		return ShutdownInfo{
			HasActiveTasks: false,
			ActiveCount:    0,
			PendingCount:   0,
		}
	}

	activeCount := a.processor.GetActiveTasksCount()
	pendingCount := a.processor.GetPendingTasksCount()

	return ShutdownInfo{
		HasActiveTasks: activeCount > 0,
		ActiveCount:    activeCount,
		PendingCount:   pendingCount,
	}
}

// ShutdownNow 立即关闭，停止所有任务
func (a *App) ShutdownNow() {
	log.Println("前端请求立即关闭（停止所有任务）")
	if a.processor != nil {
		a.processor.StopNow()
	}
	if a.onShutdownNow != nil {
		a.onShutdownNow()
	} else if a.onQuit != nil {
		a.onQuit()
	}
}

// ShutdownAfterCompletion 等待任务完成后关闭
func (a *App) ShutdownAfterCompletion() {
	log.Println("前端请求等待任务完成后关闭")
	if a.onShutdownAfterCompletion != nil {
		a.onShutdownAfterCompletion(func() {
			// 任务完成后的回调
			log.Println("所有任务已完成，正在关闭应用...")
			if a.onQuit != nil {
				a.onQuit()
			}
		})
	} else {
		// 如果没有设置回调，使用默认行为：在后台等待
		go func() {
			if a.processor != nil {
				// 等待最多30分钟
				a.processor.WaitForCompletion(30 * time.Minute)
			}
			log.Println("任务完成，正在关闭应用...")
			if a.onQuit != nil {
				a.onQuit()
			}
		}()
	}
}

// ================== 转码相关 API ==================

// TranscodeRequest 转码请求
type TranscodeRequest struct {
	Files                 []string `json:"files"`
	Params                string   `json:"params"`
	Format                string   `json:"format"`
	PreserveCover         bool     `json:"preserveCover"`
	DeleteSourceOnSuccess bool     `json:"deleteSourceOnSuccess"`
}

// TranscodeResult 转码结果
type TranscodeResult struct {
	Success   bool   `json:"success"`
	TaskCount int    `json:"taskCount"`
	Error     string `json:"error"`
}

// TranscodeTaskInfo 转码任务信息
type TranscodeTaskInfo struct {
	ID        string  `json:"id"`
	InputFile string  `json:"inputFile"`
	Status    string  `json:"status"`
	Progress  float64 `json:"progress"`
	Error     string  `json:"error"`
}

// SelectFolder 打开文件夹选择对话框
func (a *App) SelectFolder() string {
	// 使用 PowerShell 调用 Windows 文件夹选择对话框
	cmd := fmt.Sprintf(`powershell -Command "Add-Type -AssemblyName System.Windows.Forms; $dialog = New-Object System.Windows.Forms.FolderBrowserDialog; $dialog.Description = '选择要转码的文件夹'; $result = $dialog.ShowDialog(); if ($result -eq [System.Windows.Forms.DialogResult]::OK) { Write-Output $dialog.SelectedPath }"`)

	out, err := execCommand(cmd)
	if err != nil {
		log.Printf("选择文件夹失败: %v", err)
		return ""
	}
	return out
}

// execCommand 执行命令并返回输出
func execCommand(cmd string) (string, error) {
	c := exec.Command("cmd", "/C", cmd)
	output, err := c.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// ScanVideoFolder 扫描文件夹中的视频文件
func (a *App) ScanVideoFolder(path string) []transcoder.VideoFile {
	if a.transcoder == nil {
		log.Println("转码器未初始化")
		return []transcoder.VideoFile{}
	}

	videos, err := a.transcoder.ScanFolder(path)
	if err != nil {
		log.Printf("扫描文件夹失败: %v", err)
		return []transcoder.VideoFile{}
	}

	return videos
}

// StartTranscode 开始转码
func (a *App) StartTranscode(req TranscodeRequest) TranscodeResult {
	if a.transcoder == nil {
		return TranscodeResult{
			Success: false,
			Error:   "转码器未初始化",
		}
	}

	if len(req.Files) == 0 {
		return TranscodeResult{
			Success: false,
			Error:   "未选择任何文件",
		}
	}

	// 确定输出扩展名
	outputExt := ".mp4"
	if req.Format == "mkv" {
		outputExt = ".mkv"
	}

	config := transcoder.TranscodeConfig{
		CustomArgs:            req.Params,
		OutputExt:             outputExt,
		DeleteSourceOnSuccess: req.DeleteSourceOnSuccess,
	}

	taskCount := 0
	for _, file := range req.Files {
		_, err := a.transcoder.AddTask(file, config)
		if err != nil {
			log.Printf("添加转码任务失败: %s, 错误: %v", file, err)
			continue
		}
		taskCount++
	}

	return TranscodeResult{
		Success:   taskCount > 0,
		TaskCount: taskCount,
	}
}

// GetTranscodeTasks 获取所有转码任务
func (a *App) GetTranscodeTasks() []TranscodeTaskInfo {
	if a.transcoder == nil {
		return []TranscodeTaskInfo{}
	}

	tasks := a.transcoder.GetAllTasks()
	result := make([]TranscodeTaskInfo, 0, len(tasks))

	for _, task := range tasks {
		result = append(result, TranscodeTaskInfo{
			ID:        task.ID,
			InputFile: task.InputPath,
			Status:    string(task.Status),
			Progress:  task.Progress,
			Error:     task.Error,
		})
	}

	return result
}

// CancelTranscodeTask 取消单个转码任务
func (a *App) CancelTranscodeTask(taskID string) error {
	if a.transcoder == nil {
		return fmt.Errorf("转码器未初始化")
	}
	return a.transcoder.CancelTask(taskID)
}

// CancelAllTranscode 取消所有转码任务
func (a *App) CancelAllTranscode() error {
	if a.transcoder == nil {
		return fmt.Errorf("转码器未初始化")
	}
	a.transcoder.CancelAll()
	return nil
}

// ClearCompletedTranscodeTasks 清除已完成的转码任务
func (a *App) ClearCompletedTranscodeTasks() error {
	if a.transcoder == nil {
		return fmt.Errorf("转码器未初始化")
	}
	a.transcoder.ClearCompleted()
	return nil
}
