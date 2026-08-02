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
	configPath string

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

// SetConfigPath sets the file updated by browser or desktop configuration changes.
func (a *App) SetConfigPath(path string) { a.configPath = path }

func (a *App) configFilePath() string {
	if a.configPath != "" {
		return a.configPath
	}
	return filepath.Join(".", "config.yaml")
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
	DeleteOriginal     bool     `json:"deleteOriginal"`     // 处理成功后删除原文件
	ConflictMode       string   `json:"conflictMode"`       // 文件冲突处理模式
	ScanIntervalMin    int      `json:"scanIntervalMin"`    // 定时扫描间隔（分钟）
	ServerPort         int      `json:"serverPort"`
	WebhookPath        string   `json:"webhookPath"`
	WebhookEnabled     bool     `json:"webhookEnabled"` // 是否启用 Webhook 自动添加任务
	FFmpegPath         string   `json:"ffmpegPath"`
	FFprobePath        string   `json:"ffprobePath"`
	CustomArgs         []string `json:"customArgs"`
	DefaultCover       string   `json:"defaultCover"`
	SaveHistory        bool     `json:"saveHistory"` // 是否保存封面历史
	PathTemplate       string   `json:"pathTemplate"`
	StreamerNameRegex  string   `json:"streamerNameRegex"` // 主播名解析正则
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
		DeleteOriginal:     a.config.Processing.DeleteOriginal,
		ConflictMode:       a.config.Processing.ConflictMode,
		ScanIntervalMin:    a.config.Processing.ScanIntervalMin,
		ServerPort:         a.config.Server.Port,
		WebhookPath:        a.config.Server.WebhookPath,
		WebhookEnabled:     a.config.Server.WebhookEnabled,
		FFmpegPath:         a.config.FFmpeg.Path,
		FFprobePath:        a.config.FFmpeg.FFprobePath,
		CustomArgs:         a.config.FFmpeg.CustomArgs,
		DefaultCover:       a.config.Covers.DefaultCover,
		SaveHistory:        a.config.Covers.SaveHistory,
		PathTemplate:       a.config.Rules.PathTemplate,
		StreamerNameRegex:  a.config.Rules.StreamerNameRegex,
	}
}

// SaveConfig 保存配置并更新运行时配置
func (a *App) SaveConfig(data ConfigData) error {
	if a.config == nil {
		return fmt.Errorf("配置未初始化")
	}

	// 更新内存中的配置对象
	a.config.Processing.InputDir = data.InputDir
	a.config.Processing.OutputRoot = data.OutputRoot
	a.config.Processing.DiscardDir = data.DiscardDir
	a.config.Processing.MaxConcurrent = data.MaxConcurrent
	a.config.Processing.MinFileSizeKB = data.MinFileSizeKB
	a.config.Processing.CheckVideoStream = data.CheckVideoStream
	a.config.Processing.DiscardFailedFiles = data.DiscardFailedFiles
	a.config.Processing.DeleteOriginal = data.DeleteOriginal
	a.config.Processing.ConflictMode = data.ConflictMode
	a.config.Processing.ScanIntervalMin = data.ScanIntervalMin
	a.config.Server.Port = data.ServerPort
	a.config.Server.WebhookPath = data.WebhookPath
	a.config.Server.WebhookEnabled = data.WebhookEnabled
	a.config.FFmpeg.Path = data.FFmpegPath
	a.config.FFmpeg.FFprobePath = data.FFprobePath
	a.config.FFmpeg.CustomArgs = data.CustomArgs
	a.config.Covers.DefaultCover = data.DefaultCover
	a.config.Covers.SaveHistory = data.SaveHistory
	a.config.Rules.PathTemplate = data.PathTemplate
	a.config.Rules.StreamerNameRegex = data.StreamerNameRegex

	// 保存到配置文件
	if err := a.config.Save(a.configFilePath()); err != nil {
		return fmt.Errorf("保存配置失败: %w", err)
	}

	// 通知 processor 更新运行时配置（热更新）
	if a.processor != nil {
		processorConfig := processor.Config{
			MaxConcurrent:      data.MaxConcurrent,
			InputDir:           data.InputDir,
			OutputRoot:         data.OutputRoot,
			DiscardDir:         data.DiscardDir,
			PathTemplate:       data.PathTemplate,
			CheckVideoStream:   data.CheckVideoStream,
			MinFileSizeKB:      data.MinFileSizeKB,
			DiscardFailedFiles: data.DiscardFailedFiles,
			ConflictMode:       processor.ConflictMode(data.ConflictMode),
			DefaultCoverPath:   data.DefaultCover,
			DeleteOriginal:     data.DeleteOriginal,
			ScanInterval:       time.Duration(data.ScanIntervalMin) * time.Minute,
		}
		a.processor.UpdateConfig(processorConfig)
		log.Println("处理器配置已热更新")
	}

	log.Println("配置已保存并应用")
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
	InputArgs             string   `json:"inputArgs"` // FFmpeg 输入选项，放在 -i 之前（如 -hwaccel qsv）
	PreserveCover         bool     `json:"preserveCover"`
	DeleteSourceOnSuccess bool     `json:"deleteSourceOnSuccess"`
	MaxFPS                float64  `json:"maxFps"`            // 帧率上限，0 表示不限制（任务级默认）
	QSVReinitStrategy     string   `json:"qsvReinitStrategy"` // QSV 滤镜链重初始化失败回退策略
}

// TranscodeSettings 转码设置（用于持久化）
type TranscodeSettings struct {
	Params                string  `json:"params"`
	Format                string  `json:"format"`
	InputArgs             string  `json:"inputArgs"` // FFmpeg 输入选项，放在 -i 之前
	PreserveCover         bool    `json:"preserveCover"`
	DeleteSourceOnSuccess bool    `json:"deleteSourceOnSuccess"`
	MaxFPS                float64 `json:"maxFps"`
	QSVReinitStrategy     string  `json:"qsvReinitStrategy"` // QSV 滤镜链重初始化失败回退策略：error/segment/nv12
}

// GetTranscodeSettings 获取保存的转码设置
func (a *App) GetTranscodeSettings() TranscodeSettings {
	if a.config == nil {
		return TranscodeSettings{
			Params:            "-c:v av1_qsv -global_quality 23 -look_ahead 1 -c:a aac -b:a 192k",
			Format:            "mp4",
			InputArgs:         "-hwaccel qsv -hwaccel_output_format qsv",
			PreserveCover:     true,
			QSVReinitStrategy: "nv12",
		}
	}

	return TranscodeSettings{
		Params:                a.config.Transcode.DefaultParams,
		Format:                a.config.Transcode.DefaultFormat,
		InputArgs:             a.config.Transcode.InputArgs,
		PreserveCover:         a.config.Transcode.PreserveCover,
		DeleteSourceOnSuccess: a.config.Transcode.DeleteSourceOnSuccess,
		MaxFPS:                a.config.Transcode.MaxFPS,
		QSVReinitStrategy:     a.config.Transcode.QSVReinitStrategy,
	}
}

// SaveTranscodeSettings 保存转码设置到配置文件
func (a *App) SaveTranscodeSettings(settings TranscodeSettings) error {
	if a.config == nil {
		return fmt.Errorf("配置未初始化")
	}

	// 更新配置
	a.config.Transcode.DefaultParams = settings.Params
	a.config.Transcode.DefaultFormat = settings.Format
	a.config.Transcode.InputArgs = settings.InputArgs
	a.config.Transcode.PreserveCover = settings.PreserveCover
	a.config.Transcode.DeleteSourceOnSuccess = settings.DeleteSourceOnSuccess
	a.config.Transcode.MaxFPS = settings.MaxFPS
	a.config.Transcode.QSVReinitStrategy = settings.QSVReinitStrategy

	// 验证和补全配置
	a.config.FillDefaults()

	// 同步到转码器运行时（热更新 QSV 回退策略）
	if a.transcoder != nil {
		a.transcoder.SetQSVReinitStrategy(transcoder.QSVReinitStrategy(a.config.Transcode.QSVReinitStrategy))
	}

	// 保存到文件
	if err := a.config.Save(a.configFilePath()); err != nil {
		return fmt.Errorf("保存配置失败: %w", err)
	}

	log.Printf("[app] 转码设置已保存: format=%s, maxFPS=%.2f, deleteSource=%v, preserveCover=%v, qsvStrategy=%s",
		settings.Format, settings.MaxFPS, settings.DeleteSourceOnSuccess, settings.PreserveCover, a.config.Transcode.QSVReinitStrategy)
	return nil
}

// TranscodeResult 转码结果
type TranscodeResult struct {
	Success   bool   `json:"success"`
	TaskCount int    `json:"taskCount"`
	Error     string `json:"error"`
}

// TranscodeTaskInfo 转码任务信息
type TranscodeTaskInfo struct {
	ID                  string  `json:"id"`
	InputFile           string  `json:"inputFile"`
	OutputFile          string  `json:"outputFile"` // 输出文件
	Status              string  `json:"status"`
	ExecutionPool       string  `json:"executionPool"`
	Progress            float64 `json:"progress"`
	Error               string  `json:"error"`
	ErrorLogPath        string  `json:"errorLogPath"`        // 错误日志文件路径
	ETAString           string  `json:"etaString"`           // 剩余时间字符串
	ETASeconds          float64 `json:"etaSeconds"`          // 剩余秒数
	Speed               string  `json:"speed"`               // 速度倍率
	CurrentFPS          float64 `json:"currentFPS"`          // 当前处理 FPS
	ElapsedSeconds      float64 `json:"elapsedSeconds"`      // 已用时间（秒）
	ElapsedString       string  `json:"elapsedString"`       // 已用时间格式化
	PredictedTimeString string  `json:"predictedTimeString"` // 预测总时间
	Width               int     `json:"width"`               // 视频宽度
	Height              int     `json:"height"`              // 视频高度
	TotalFrames         int64   `json:"totalFrames"`         // 总帧数
	ProcessedFrame      int64   `json:"processedFrame"`      // 已处理帧数
}

// SelectFolder 打开文件夹选择对话框
func (a *App) SelectFolder() string {
	return selectFolderDialog()
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

// ScanMultiplePaths 扫描多个文件或文件夹中的视频文件（用于拖拽导入）
func (a *App) ScanMultiplePaths(paths []string) []transcoder.VideoFile {
	if a.transcoder == nil {
		log.Println("转码器未初始化")
		return []transcoder.VideoFile{}
	}

	var allVideos []transcoder.VideoFile
	seen := make(map[string]bool) // 用于去重

	for _, path := range paths {
		videos, err := a.transcoder.ScanPath(path)
		if err != nil {
			log.Printf("扫描路径失败: %s, 错误: %v", path, err)
			continue
		}

		for _, v := range videos {
			if !seen[v.Path] {
				seen[v.Path] = true
				allVideos = append(allVideos, v)
			}
		}
	}

	log.Printf("扫描多个路径完成，共找到 %d 个视频文件", len(allVideos))
	return allVideos
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

	// 使用请求中的 MaxFPS（前端传入的值优先）
	maxFPS := req.MaxFPS

	// 保存当前转码设置到配置文件（异步保存，不影响转码开始）
	go func() {
		settings := TranscodeSettings{
			Params:                req.Params,
			Format:                req.Format,
			InputArgs:             req.InputArgs,
			PreserveCover:         req.PreserveCover,
			DeleteSourceOnSuccess: req.DeleteSourceOnSuccess,
			MaxFPS:                maxFPS,
			QSVReinitStrategy:     req.QSVReinitStrategy,
		}
		if err := a.SaveTranscodeSettings(settings); err != nil {
			log.Printf("[app] 保存转码设置失败: %v", err)
		}
	}()

	// 构造转码配置
	tConfig := transcoder.TranscodeConfig{
		InputArgs:             req.InputArgs,
		QSVReinitStrategy:     req.QSVReinitStrategy,
		CustomArgs:            req.Params,
		OutputExt:             outputExt,
		DeleteSourceOnSuccess: req.DeleteSourceOnSuccess,
		MaxFPS:                maxFPS,
	}

	log.Printf("[app] 开始转码: files=%d, format=%s, maxFPS=%.2f, deleteSource=%v",
		len(req.Files), req.Format, maxFPS, req.DeleteSourceOnSuccess)

	taskCount := 0
	for _, file := range req.Files {
		_, err := a.transcoder.AddTask(file, tConfig)
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

// GetTranscodeMaxWorkers 获取当前转码并发路数
func (a *App) GetTranscodeMaxWorkers() int {
	if a.transcoder == nil {
		return 1
	}
	return a.transcoder.MaxWorkers()
}

// SetTranscodeMaxWorkers 设置转码并发路数（热更新）
func (a *App) SetTranscodeMaxWorkers(n int) error {
	if a.transcoder == nil {
		return fmt.Errorf("转码器未初始化")
	}
	if n <= 0 {
		return fmt.Errorf("并发路数必须大于 0")
	}
	a.transcoder.SetMaxWorkers(n)

	// 同步到配置文件
	if a.config != nil {
		a.config.Transcode.MaxConcurrent = n
		if err := a.config.Save(a.configFilePath()); err != nil {
			return fmt.Errorf("保存并发路数配置失败: %w", err)
		}
	}
	return nil
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
			ID:                  task.ID,
			InputFile:           task.InputPath,
			OutputFile:          task.OutputPath,
			Status:              string(task.Status),
			ExecutionPool:       task.ExecutionPool,
			Progress:            task.Progress,
			Error:               task.Error,
			ErrorLogPath:        task.ErrorLogPath,
			ETAString:           task.ETAString,
			ETASeconds:          task.ETASeconds,
			Speed:               task.Speed,
			CurrentFPS:          task.CurrentFPS,
			ElapsedSeconds:      task.ElapsedSeconds,
			ElapsedString:       task.ElapsedString,
			PredictedTimeString: task.PredictedTimeString,
			Width:               task.Width,
			Height:              task.Height,
			TotalFrames:         task.TotalFrames,
			ProcessedFrame:      task.ProcessedFrame,
		})
	}

	return result
}

// TranscodeGlobalStatus 转码全局状态
type TranscodeGlobalStatus struct {
	TotalRemainingSeconds float64 `json:"totalRemainingSeconds"` // 总剩余时间（秒）
	TotalRemainingString  string  `json:"totalRemainingString"`  // 总剩余时间格式化
	PendingCount          int     `json:"pendingCount"`          // 待处理任务数
	ProcessingCount       int     `json:"processingCount"`       // 处理中任务数
}

// GetTranscodeGlobalStatus 获取转码全局状态（包括总剩余时间）
func (a *App) GetTranscodeGlobalStatus() TranscodeGlobalStatus {
	if a.transcoder == nil {
		return TranscodeGlobalStatus{}
	}

	status := a.transcoder.GetGlobalStatus()
	return TranscodeGlobalStatus{
		TotalRemainingSeconds: status.TotalRemainingSeconds,
		TotalRemainingString:  status.TotalRemainingString,
		PendingCount:          status.PendingCount,
		ProcessingCount:       status.ProcessingCount,
	}
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

// ================== 暂停相关 API ==================

// PauseStatus 暂停状态
type PauseStatus struct {
	// 转封装暂停状态
	RemuxPaused            bool   `json:"remuxPaused"`            // 转封装是否立即暂停
	RemuxPauseAfterCurrent bool   `json:"remuxPauseAfterCurrent"` // 转封装是否当前任务后暂停
	RemuxPausedTimeStr     string `json:"remuxPausedTimeStr"`     // 转封装暂停时长

	// 转码暂停状态
	TranscodePaused            bool   `json:"transcodePaused"`            // 转码是否立即暂停
	TranscodePauseAfterCurrent bool   `json:"transcodePauseAfterCurrent"` // 转码是否当前任务后暂停
	TranscodePausedTimeStr     string `json:"transcodePausedTimeStr"`     // 转码暂停时长
}

// GetPauseStatus 获取所有暂停状态
func (a *App) GetPauseStatus() PauseStatus {
	status := PauseStatus{}

	// 获取转封装暂停状态
	if a.processor != nil {
		remuxStatus := a.processor.GetRemuxPauseStatus()
		status.RemuxPaused = remuxStatus.Paused
		status.RemuxPauseAfterCurrent = remuxStatus.PauseAfterCurrent
		status.RemuxPausedTimeStr = remuxStatus.PausedTimeStr
	}

	// 获取转码暂停状态
	if a.transcoder != nil {
		transcodeStatus := a.transcoder.GetTranscodePauseStatus()
		status.TranscodePaused = transcodeStatus.Paused
		status.TranscodePauseAfterCurrent = transcodeStatus.PauseAfterCurrent
		status.TranscodePausedTimeStr = transcodeStatus.PausedTimeStr
	}

	return status
}

// ================== 转封装暂停 API ==================

// PauseRemux 立即暂停转封装
func (a *App) PauseRemux() error {
	if a.processor == nil {
		return fmt.Errorf("处理器未初始化")
	}
	return a.processor.PauseRemux()
}

// ResumeRemux 恢复转封装
func (a *App) ResumeRemux() error {
	if a.processor == nil {
		return fmt.Errorf("处理器未初始化")
	}
	return a.processor.ResumeRemux()
}

// PauseRemuxAfterCurrent 当前任务后暂停转封装
func (a *App) PauseRemuxAfterCurrent() {
	if a.processor != nil {
		a.processor.PauseRemuxAfterCurrent()
	}
}

// CancelPauseRemuxAfterCurrent 取消当前任务后暂停转封装
func (a *App) CancelPauseRemuxAfterCurrent() {
	if a.processor != nil {
		a.processor.CancelPauseRemuxAfterCurrent()
	}
}

// GetRemuxPauseStatus 获取转封装暂停状态
func (a *App) GetRemuxPauseStatus() processor.RemuxPauseStatus {
	if a.processor == nil {
		return processor.RemuxPauseStatus{}
	}
	return a.processor.GetRemuxPauseStatus()
}

// ================== 转码暂停 API ==================

// PauseTranscode 立即暂停转码
func (a *App) PauseTranscode() error {
	if a.transcoder == nil {
		return fmt.Errorf("转码器未初始化")
	}
	return a.transcoder.PauseTranscode()
}

// ResumeTranscode 恢复转码
func (a *App) ResumeTranscode() error {
	if a.transcoder == nil {
		return fmt.Errorf("转码器未初始化")
	}
	return a.transcoder.ResumeTranscode()
}

// PauseTranscodeAfterCurrent 当前任务后暂停转码
func (a *App) PauseTranscodeAfterCurrent() {
	if a.transcoder != nil {
		a.transcoder.PauseTranscodeAfterCurrent()
	}
}

// CancelPauseTranscodeAfterCurrent 取消当前任务后暂停转码
func (a *App) CancelPauseTranscodeAfterCurrent() {
	if a.transcoder != nil {
		a.transcoder.CancelPauseTranscodeAfterCurrent()
	}
}

// GetTranscodePauseStatus 获取转码暂停状态
func (a *App) GetTranscodePauseStatus() transcoder.TranscodePauseStatus {
	if a.transcoder == nil {
		return transcoder.TranscodePauseStatus{}
	}
	return a.transcoder.GetTranscodePauseStatus()
}

// ================== 转封装进度 API ==================

// RemuxProgressInfo 转封装进度信息（用于前端）
type RemuxProgressInfo struct {
	SourceSize       int64   `json:"sourceSize"`       // 源文件大小（字节）
	WrittenSize      int64   `json:"writtenSize"`      // 已写入大小（字节）
	SourceSizeStr    string  `json:"sourceSizeStr"`    // 源文件大小字符串
	WrittenSizeStr   string  `json:"writtenSizeStr"`   // 已写入大小字符串
	SpeedBytesPerSec float64 `json:"speedBytesPerSec"` // 速度（字节/秒）
	SpeedStr         string  `json:"speedStr"`         // 速度字符串
	Progress         float64 `json:"progress"`         // 进度百分比
	ElapsedSeconds   float64 `json:"elapsedSeconds"`   // 已用时间秒数
	ElapsedStr       string  `json:"elapsedStr"`       // 已用时间字符串
	IsActive         bool    `json:"isActive"`         // 是否正在进行
	IsPaused         bool    `json:"isPaused"`         // 是否暂停
	CurrentFile      string  `json:"currentFile"`      // 当前文件名
}

// GetRemuxProgress 获取转封装进度
func (a *App) GetRemuxProgress() RemuxProgressInfo {
	if a.processor == nil {
		return RemuxProgressInfo{}
	}

	// 获取原始进度
	progress := a.processor.GetRemuxProgress()
	// 获取格式化的进度信息
	formatted := a.processor.GetRemuxProgressInfo()

	return RemuxProgressInfo{
		SourceSize:       progress.SourceSize,
		WrittenSize:      progress.WrittenSize,
		SourceSizeStr:    formatted.SourceSizeStr,
		WrittenSizeStr:   formatted.WrittenSizeStr,
		SpeedBytesPerSec: progress.SpeedBytesPerSec,
		SpeedStr:         formatted.SpeedStr,
		Progress:         progress.Progress,
		ElapsedSeconds:   progress.ElapsedSeconds,
		ElapsedStr:       formatted.ElapsedStr,
		IsActive:         progress.IsActive,
		IsPaused:         progress.IsPaused,
		CurrentFile:      progress.CurrentFile,
	}
}

// ================== 转码错误日志 API ==================

// OpenTranscodeErrorLog 打开转码任务的错误日志文件（在资源管理器中定位）
func (a *App) OpenTranscodeErrorLog(taskID string) error {
	if a.transcoder == nil {
		return fmt.Errorf("转码器未初始化")
	}

	// 获取任务
	task, err := a.transcoder.GetTask(taskID)
	if err != nil {
		return fmt.Errorf("任务不存在: %s", taskID)
	}

	// 检查错误日志路径
	if task.ErrorLogPath == "" {
		return fmt.Errorf("该任务没有错误日志文件")
	}

	// 调用平台特定的打开文件函数
	return openFileInExplorer(task.ErrorLogPath)
}
