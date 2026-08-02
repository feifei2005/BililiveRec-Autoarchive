// Package main 是程序入口点
package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/user/bililive-recorder-autoarchive/internal/app"
	"github.com/user/bililive-recorder-autoarchive/internal/autostart"
	"github.com/user/bililive-recorder-autoarchive/internal/config"
	"github.com/user/bililive-recorder-autoarchive/internal/ffmpeg"
	"github.com/user/bililive-recorder-autoarchive/internal/processor"
	"github.com/user/bililive-recorder-autoarchive/internal/scanner"
	"github.com/user/bililive-recorder-autoarchive/internal/storage"
	"github.com/user/bililive-recorder-autoarchive/internal/transcoder"
	"github.com/user/bililive-recorder-autoarchive/internal/webhook"
)

//go:embed all:frontend
var assets embed.FS

// 版本信息，通过编译时 ldflags 注入
var (
	Version   = "dev"
	BuildTime = "unknown"
)

// Application 应用程序结构体，整合所有模块
type Application struct {
	config     *config.Config
	storage    storage.Storage
	scanner    *scanner.DefaultScanner
	ffmpeg     ffmpeg.FFmpeg
	processor  *processor.DefaultProcessor
	webhook    *webhook.Server
	autostart  *autostart.AutoStart
	transcoder *transcoder.Transcoder
	app        *app.App

	ctx      context.Context
	cancel   context.CancelFunc
	wailsCtx context.Context
}

func main() {
	// 设置文件日志记录（使用 lumberjack 实现日志轮转）
	setupFileLogging()

	log.Printf("BililiveRecorder 自动整理工具 %s (构建时间: %s)", Version, BuildTime)

	// 创建应用实例
	application := &Application{}

	// 初始化应用
	if err := application.Initialize(); err != nil {
		log.Fatalf("初始化失败: %v", err)
	}

	// 启动后台服务
	if err := application.StartServices(); err != nil {
		log.Fatalf("启动服务失败: %v", err)
	}

	// 运行 Wails 应用（阻塞）
	if err := application.RunWails(); err != nil {
		log.Fatalf("运行 Wails 失败: %v", err)
	}

	// Wails 窗口关闭后，优雅关闭所有服务
	application.Shutdown()

	log.Println("程序已退出")
}

// setupFileLogging 设置文件日志记录
// 使用 lumberjack 实现日志轮转，防止日志文件无限增大
func setupFileLogging() {
	// 获取可执行文件所在目录
	exePath, err := os.Executable()
	if err != nil {
		log.Printf("警告: 无法获取可执行文件路径: %v", err)
		return
	}
	exeDir := filepath.Dir(exePath)

	// 创建 logs 目录
	logsDir := filepath.Join(exeDir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		log.Printf("警告: 无法创建日志目录 %s: %v", logsDir, err)
		return
	}

	// 创建日志文件路径
	logPath := filepath.Join(logsDir, "app.log")

	// 配置日志轮转
	// lumberjack.Logger 实现了 io.WriteCloser 接口
	// 它会自动处理日志文件的轮转、压缩和清理
	logger := &lumberjack.Logger{
		Filename:   logPath, // 日志文件路径
		MaxSize:    10,      // 单个文件最大大小（MB），超过后自动轮转
		MaxBackups: 5,       // 保留的旧日志文件数量
		MaxAge:     30,      // 保留的天数，超过后自动删除
		Compress:   true,    // 是否压缩旧日志文件（.gz）
		LocalTime:  true,    // 使用本地时间命名备份文件
	}

	// 设置日志输出
	// 在 Windows GUI 模式 (-H windowsgui) 下，os.Stdout 无效
	// 使用 Stat() 检测 stdout 是否有效
	var output io.Writer
	if isStdoutValid() {
		output = io.MultiWriter(os.Stdout, logger)
	} else {
		// GUI 模式下仅输出到文件
		output = logger
	}
	log.SetOutput(output)
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	log.Printf("日志文件: %s (轮转: 最大%dMB, 保留%d个备份, %d天)", logPath, 10, 5, 30)
}

// isStdoutValid 检测标准输出是否有效
// 在 Windows GUI 模式 (-H windowsgui) 下，stdout 无效
func isStdoutValid() bool {
	_, err := os.Stdout.Stat()
	return err == nil
}

// Initialize 初始化所有模块
func (a *Application) Initialize() error {
	var err error

	// 创建上下文
	a.ctx, a.cancel = context.WithCancel(context.Background())

	// 获取可执行文件所在目录，用于构建配置文件和数据库的绝对路径
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取可执行文件路径失败: %w", err)
	}
	exeDir := filepath.Dir(exePath)

	// 1. 加载配置
	log.Println("加载配置...")
	configPath := filepath.Join(exeDir, "config.yaml")
	log.Printf("配置文件路径: %s", configPath)
	a.config, err = config.Load(configPath)
	if err != nil {
		log.Printf("警告: 无法加载配置文件，使用默认配置: %v", err)
		a.config = config.Default()
	}

	// 补全缺失的配置字段（处理配置文件缺少新字段的情况）
	a.config.FillDefaults()

	// 如果加载了配置但补全了字段，保存更新后的配置
	// 这样用户可以在配置文件中看到所有可用的选项
	if err == nil {
		if saveErr := a.config.Save(configPath); saveErr != nil {
			log.Printf("警告: 保存更新后的配置文件失败: %v", saveErr)
		}
	}

	// 打印配置信息
	log.Printf("服务器端口: %d", a.config.Server.Port)
	log.Printf("输入目录: %s", a.config.Processing.InputDir)
	log.Printf("输出目录: %s", a.config.Processing.OutputRoot)

	// 2. 初始化数据库
	log.Println("初始化数据库...")
	dbPath := filepath.Join(exeDir, "data.db")
	log.Printf("数据库路径: %s", dbPath)
	a.storage, err = storage.New(storage.Config{
		DBPath: dbPath,
	})
	if err != nil {
		return fmt.Errorf("初始化数据库失败: %w", err)
	}

	// 3. 初始化 FFmpeg
	log.Println("初始化 FFmpeg...")
	a.ffmpeg = ffmpeg.New(ffmpeg.Config{
		FFmpegPath:  a.config.FFmpeg.Path,
		FFprobePath: a.config.FFmpeg.FFprobePath,
		CustomArgs:  a.config.FFmpeg.CustomArgs,
	})

	// 4. 初始化扫描器
	log.Println("初始化扫描器...")
	a.scanner = scanner.New(scanner.Config{
		InputDirFunc: func() string { return a.config.Processing.InputDir },
		Extensions:   []string{".flv"},
		MinFileSize:  a.config.Processing.MinFileSizeKB * 1024,
	})

	// 5. 初始化处理器
	log.Println("初始化处理器...")
	scanInterval := time.Duration(a.config.Processing.ScanIntervalMin) * time.Minute
	if scanInterval <= 0 {
		scanInterval = 5 * time.Minute
	}
	a.processor, err = processor.New(processor.Config{
		MaxConcurrent:      a.config.Processing.MaxConcurrent,
		InputDir:           a.config.Processing.InputDir,
		OutputRoot:         a.config.Processing.OutputRoot,
		DiscardDir:         a.config.Processing.DiscardDir,
		PathTemplate:       a.config.Rules.PathTemplate,
		CheckVideoStream:   a.config.Processing.CheckVideoStream,
		MinFileSizeKB:      a.config.Processing.MinFileSizeKB,
		DiscardFailedFiles: a.config.Processing.DiscardFailedFiles,
		ConflictMode:       processor.ConflictMode(a.config.Processing.ConflictMode),
		DeleteOriginal:     a.config.Processing.DeleteOriginal,
		FFmpeg:             a.ffmpeg,
		Storage:            a.storage,
		DefaultCoverPath:   a.config.Covers.DefaultCover,
		Scanner:            a.scanner,
		ScanInterval:       scanInterval,
	})
	if err != nil {
		return fmt.Errorf("初始化处理器失败: %w", err)
	}

	// 6. 初始化 Webhook 服务器
	log.Println("初始化 Webhook 服务器...")
	a.webhook = webhook.New(webhook.Config{
		BindAddress: a.config.Server.BindAddress,
		APIToken:    a.config.Server.APIToken,
		Port:        a.config.Server.Port,
		WebhookPath: a.config.Server.WebhookPath,
		InputDir:    a.config.Processing.InputDir,
		MaxFPS:      a.config.Transcode.MaxFPS,
	})

	// 注册 Webhook 事件处理
	a.webhook.On(webhook.EventFileClosed, func(event *webhook.Event) error {
		// 检查 Webhook 是否启用（支持热重载）
		if !a.config.Server.WebhookEnabled {
			log.Printf("Webhook: 已禁用，跳过文件: %s", event.EventData.RelativePath)
			return nil
		}

		// 获取完整文件路径
		fullPath, pathErr := a.webhook.GetFullPath(event.EventData.RelativePath)
		if pathErr != nil {
			return pathErr
		}
		log.Printf("Webhook: 收到文件关闭事件，文件: %s", fullPath)

		// 添加到处理队列
		return a.processor.AddTask(fullPath)
	})

	// 7. 初始化开机自启动
	log.Println("初始化开机自启动管理器...")
	a.autostart, err = autostart.New("")
	if err != nil {
		log.Printf("警告: 初始化开机自启动失败: %v", err)
	}

	// 8. 初始化转码器
	log.Println("初始化转码器...")
	maxWorkers := a.config.Transcode.MaxConcurrent
	if maxWorkers <= 0 {
		maxWorkers = 1
	}
	a.transcoder = transcoder.New(transcoder.Config{
		FFmpegPath:  a.config.FFmpeg.Path,
		FFprobePath: a.config.FFmpeg.FFprobePath,
		MaxWorkers:  maxWorkers,
	})
	// 应用 QSV 滤镜链重初始化失败回退策略
	a.transcoder.SetQSVReinitStrategy(transcoder.QSVReinitStrategy(a.config.Transcode.QSVReinitStrategy))
	log.Printf("转码器已创建，并发路数: %d, QSV 回退策略: %s", maxWorkers, a.config.Transcode.QSVReinitStrategy)

	// 9. 初始化 Wails 应用绑定
	log.Println("初始化应用绑定...")
	a.app = app.NewApp()
	a.app.SetConfig(a.config)
	a.app.SetProcessor(a.processor)
	a.app.SetStorage(a.storage)
	if a.autostart != nil {
		a.app.SetAutostart(a.autostart)
	}

	// 设置转码器
	a.app.SetTranscoder(a.transcoder)
	log.Println("转码器已设置到 App")

	// 设置转码器处理日志回调（将转码结果持久化到 process_logs 表，
	// 使错误日志页面能显示转码失败记录）
	a.transcoder.SetProcessLogCallback(func(entry transcoder.ProcessLogEntry) {
		if a.storage == nil {
			return
		}
		err := a.storage.LogProcessResult(storage.ProcessLog{
			TaskID:     entry.TaskID,
			InputPath:  entry.InputPath,
			OutputPath: entry.OutputPath,
			Status:     entry.Status,
			Error:      entry.Error,
			StartTime:  entry.StartTime,
			EndTime:    entry.EndTime,
		})
		if err != nil {
			log.Printf("[main] 写入转码处理日志失败: %v", err)
		}
	})
	log.Println("转码器处理日志回调已设置")

	// 设置 Webhook 服务器的 App 引用（用于转码 API）
	a.webhook.SetApp(a.app)
	log.Println("Webhook 服务器已关联 App")

	// 设置回调函数
	a.app.SetCallbacks(
		func() { a.handleQuit() },       // onQuit
		func() { a.triggerScan() },      // onScanNow
		func() { a.handleShowWindow() }, // onShowWindow
		func() { a.handleHideWindow() }, // onHideWindow
	)

	// 设置关闭相关的回调函数
	a.app.SetShutdownCallbacks(
		func() { a.handleShutdownNow() },                        // onShutdownNow
		func(cb func()) { a.handleShutdownAfterCompletion(cb) }, // onShutdownAfterCompletion
	)

	return nil
}

// StartServices 启动后台服务
func (a *Application) StartServices() error {
	log.Println("启动后台服务...")

	// 启动处理器
	if err := a.processor.Start(a.ctx); err != nil {
		return fmt.Errorf("启动处理器失败: %w", err)
	}

	// 启动转码器
	if err := a.transcoder.Start(a.ctx); err != nil {
		return fmt.Errorf("启动转码器失败: %w", err)
	}
	log.Println("转码器已启动")

	// 启动 Webhook 服务器（非阻塞）
	go func() {
		if err := a.webhook.Start(); err != nil {
			log.Printf("Webhook 服务器错误: %v", err)
		}
	}()

	// 启动数据库定期清理任务
	a.startDatabaseCleanup()

	log.Println("后台服务已启动")
	return nil
}

// startDatabaseCleanup 启动数据库定期清理任务
// 每天执行一次清理，并在启动时执行初始清理
func (a *Application) startDatabaseCleanup() {
	// 类型断言获取 SQLiteStorage
	sqliteStorage, ok := a.storage.(*storage.SQLiteStorage)
	if !ok {
		log.Println("警告: 存储不是 SQLiteStorage 类型，跳过数据库清理")
		return
	}

	// 获取默认清理配置
	cleanupCfg := storage.DefaultCleanupConfig()

	// 执行初始清理（启动后延迟 1 分钟执行，避免影响启动性能）
	go func() {
		select {
		case <-time.After(1 * time.Minute):
			a.performDatabaseCleanup(sqliteStorage, cleanupCfg)
		case <-a.ctx.Done():
			return
		}
	}()

	// 启动定期清理（每 24 小时执行一次）
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				a.performDatabaseCleanup(sqliteStorage, cleanupCfg)
			case <-a.ctx.Done():
				log.Println("数据库清理任务已停止")
				return
			}
		}
	}()

	log.Printf("数据库清理任务已启动（每24小时执行，已完成任务保留%d天，失败任务保留%d天）",
		cleanupCfg.CompletedMaxDays, cleanupCfg.FailedMaxDays)
}

// performDatabaseCleanup 执行数据库清理
func (a *Application) performDatabaseCleanup(sqliteStorage *storage.SQLiteStorage, cfg storage.CleanupConfig) {
	log.Println("开始执行数据库清理...")

	// 清理前获取统计信息
	beforeStats, _ := sqliteStorage.GetDatabaseStats()
	log.Printf("清理前: 处理日志 %d 条（成功: %d, 失败: %d, 其他: %d），封面历史 %d 条",
		beforeStats["total_logs"], beforeStats["success_logs"],
		beforeStats["failed_logs"], beforeStats["other_logs"],
		beforeStats["total_covers"])

	// 执行清理
	result := sqliteStorage.CleanupOldRecords(cfg)

	// 记录清理结果
	totalDeleted := result.DeletedCompleted + result.DeletedFailed + result.DeletedOther + result.DeletedOrphaned + result.DeletedCovers
	if totalDeleted > 0 {
		log.Printf("数据库清理完成: 删除已完成 %d 条, 失败 %d 条, 其他 %d 条, 孤立 %d 条, 封面历史 %d 条",
			result.DeletedCompleted, result.DeletedFailed, result.DeletedOther,
			result.DeletedOrphaned, result.DeletedCovers)

		// 如果删除了较多记录，执行 VACUUM 释放空间
		if totalDeleted > 100 {
			log.Println("执行 VACUUM 操作释放磁盘空间...")
			if err := sqliteStorage.VacuumDatabase(); err != nil {
				log.Printf("VACUUM 失败: %v", err)
			} else {
				log.Println("VACUUM 完成")
			}
		}
	} else {
		log.Println("数据库清理完成: 无需删除任何记录")
	}

	// 记录错误
	for _, err := range result.Errors {
		log.Printf("清理错误: %v", err)
	}
}

// RunWails 运行 Wails 应用程序
func (a *Application) RunWails() error {
	log.Println("启动 Wails 窗口...")

	// 设置应用版本号
	app.Version = Version
	app.BuildTime = BuildTime

	err := wails.Run(&options.App{
		Title:  "录播自动归档工具",
		Width:  1200,
		Height: 800,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 248, G: 250, B: 252, A: 1},
		OnStartup:        a.onWailsStartup,
		OnShutdown:       a.app.Shutdown,
		Bind: []interface{}{
			a.app,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
		},
	})

	if err != nil {
		return fmt.Errorf("Wails 运行失败: %w", err)
	}

	return nil
}

// onWailsStartup Wails 启动回调
func (a *Application) onWailsStartup(ctx context.Context) {
	a.wailsCtx = ctx
	a.app.Startup(ctx)
}

// handleShowWindow 显示窗口
func (a *Application) handleShowWindow() {
	log.Println("[WINDOW] handleShowWindow 开始")
	if a.wailsCtx != nil {
		log.Println("[WINDOW] 调用 WindowShow")
		wailsRuntime.WindowShow(a.wailsCtx)
		log.Println("[WINDOW] WindowShow 返回")
	} else {
		log.Println("[WINDOW] 警告: wailsCtx 为 nil，无法显示窗口")
	}
	log.Println("[WINDOW] handleShowWindow 完成")
}

// handleHideWindow 隐藏窗口
func (a *Application) handleHideWindow() {
	log.Println("[WINDOW] handleHideWindow 开始")
	if a.wailsCtx != nil {
		log.Println("[WINDOW] 调用 WindowHide")
		wailsRuntime.WindowHide(a.wailsCtx)
		log.Println("[WINDOW] WindowHide 返回")
	} else {
		log.Println("[WINDOW] 警告: wailsCtx 为 nil，无法隐藏窗口")
	}
	log.Println("[WINDOW] handleHideWindow 完成")
}

// handleToggleAutoRun 切换开机自启动
func (a *Application) handleToggleAutoRun() {
	log.Println("切换开机自启动")
	if a.autostart == nil {
		log.Println("开机自启动管理器未初始化")
		return
	}

	enabled, err := a.autostart.IsEnabled()
	if err != nil {
		log.Printf("检查开机自启动状态失败: %v", err)
		return
	}

	if enabled {
		if err := a.autostart.Disable(); err != nil {
			log.Printf("禁用开机自启动失败: %v", err)
			return
		}
		log.Println("已禁用开机自启动")
	} else {
		if err := a.autostart.Enable(); err != nil {
			log.Printf("启用开机自启动失败: %v", err)
			return
		}
		log.Println("已启用开机自启动")
	}
}

// handleQuit 退出应用
func (a *Application) handleQuit() {
	log.Println("前端请求退出")
	if a.wailsCtx != nil {
		wailsRuntime.Quit(a.wailsCtx)
	}
}

// handleShutdownNow 立即关闭，停止所有任务
func (a *Application) handleShutdownNow() {
	log.Println("[SHUTDOWN] 立即关闭，停止所有任务")
	// 停止处理器（会停止所有正在进行的FFmpeg进程）
	if a.processor != nil {
		a.processor.StopNow()
	}
	// 触发应用退出
	a.handleQuit()
}

// handleShutdownAfterCompletion 等待任务完成后关闭
func (a *Application) handleShutdownAfterCompletion(onComplete func()) {
	log.Println("[SHUTDOWN] 等待任务完成后关闭")
	go func() {
		if a.processor != nil {
			// 停止接收新任务
			a.processor.StopGracefully()
			// 等待所有正在进行的任务完成（最多等待30分钟）
			a.processor.WaitForCompletion(30 * time.Minute)
		}
		log.Println("[SHUTDOWN] 所有任务已完成")
		// 调用完成回调
		if onComplete != nil {
			onComplete()
		}
	}()
}

// triggerScan 触发扫描
func (a *Application) triggerScan() {
	log.Println("触发立即扫描")
	go func() {
		groups, err := a.scanner.Scan()
		if err != nil {
			log.Printf("扫描失败: %v", err)
			return
		}
		log.Printf("扫描发现 %d 个文件组", len(groups))
		for _, group := range groups {
			if err := a.processor.AddTask(group.FLVPath); err != nil {
				log.Printf("添加任务失败: %v", err)
			}
		}
	}()
}

// Shutdown 优雅关闭所有服务
func (a *Application) Shutdown() {
	log.Println("正在关闭程序...")

	// 创建关闭超时上下文
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	// 取消主上下文
	if a.cancel != nil {
		log.Println("[SHUTDOWN] 取消主上下文...")
		a.cancel()
	}

	// 停止 Webhook 服务器
	if a.webhook != nil {
		log.Println("[SHUTDOWN] 停止 Webhook 服务器...")
		if err := a.webhook.Stop(shutdownCtx); err != nil {
			log.Printf("[SHUTDOWN] Webhook 停止错误: %v", err)
		}
		log.Println("[SHUTDOWN] Webhook 已停止")
	}

	// 停止处理器
	if a.processor != nil {
		log.Println("[SHUTDOWN] 停止处理器...")
		a.processor.Stop()
		log.Println("[SHUTDOWN] 处理器已停止")
	}

	// 停止转码器
	if a.transcoder != nil {
		log.Println("[SHUTDOWN] 停止转码器...")
		a.transcoder.Stop()
		log.Println("[SHUTDOWN] 转码器已停止")
	}

	// 关闭数据库
	if a.storage != nil {
		log.Println("[SHUTDOWN] 关闭数据库...")
		a.storage.Close()
		log.Println("[SHUTDOWN] 数据库已关闭")
	}

	log.Println("所有服务已停止")
}
