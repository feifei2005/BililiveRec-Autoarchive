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

	"github.com/user/bililive-recorder-autoarchive/internal/app"
	"github.com/user/bililive-recorder-autoarchive/internal/autostart"
	"github.com/user/bililive-recorder-autoarchive/internal/config"
	"github.com/user/bililive-recorder-autoarchive/internal/ffmpeg"
	"github.com/user/bililive-recorder-autoarchive/internal/processor"
	"github.com/user/bililive-recorder-autoarchive/internal/scanner"
	"github.com/user/bililive-recorder-autoarchive/internal/storage"
	"github.com/user/bililive-recorder-autoarchive/internal/tray"
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
	config    *config.Config
	storage   storage.Storage
	scanner   *scanner.DefaultScanner
	ffmpeg    ffmpeg.FFmpeg
	processor *processor.DefaultProcessor
	webhook   *webhook.Server
	autostart *autostart.AutoStart
	app       *app.App
	tray      tray.Tray

	ctx      context.Context
	cancel   context.CancelFunc
	wailsCtx context.Context
}

func main() {
	// 设置文件日志记录
	logFile := setupFileLogging()
	if logFile != nil {
		defer logFile.Close()
	}

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

	// 启动系统托盘（非阻塞）
	application.StartTray()

	// 运行 Wails 应用（阻塞）
	if err := application.RunWails(); err != nil {
		log.Fatalf("运行 Wails 失败: %v", err)
	}

	// Wails 窗口关闭后，优雅关闭所有服务
	application.Shutdown()

	log.Println("程序已退出")
}

// setupFileLogging 设置文件日志记录
func setupFileLogging() *os.File {
	// 获取可执行文件所在目录
	exePath, err := os.Executable()
	if err != nil {
		log.Printf("警告: 无法获取可执行文件路径: %v", err)
		return nil
	}
	exeDir := filepath.Dir(exePath)

	// 创建日志文件路径
	logPath := filepath.Join(exeDir, "app.log")

	// 打开或创建日志文件（追加模式）
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Printf("警告: 无法创建日志文件 %s: %v", logPath, err)
		return nil
	}

	// 设置日志输出
	// 在 Windows GUI 模式 (-H windowsgui) 下，os.Stdout 无效
	// 使用 Stat() 检测 stdout 是否有效
	var output io.Writer
	if isStdoutValid() {
		output = io.MultiWriter(os.Stdout, logFile)
	} else {
		// GUI 模式下仅输出到文件
		output = logFile
	}
	log.SetOutput(output)
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	log.Printf("日志文件: %s", logPath)
	return logFile
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
		InputDir:    a.config.Processing.InputDir,
		Extensions:  []string{".flv"},
		MinFileSize: a.config.Processing.MinFileSizeKB * 1024,
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
		Port:        a.config.Server.Port,
		WebhookPath: a.config.Server.WebhookPath,
		InputDir:    a.config.Processing.InputDir,
	})

	// 注册 Webhook 事件处理
	a.webhook.On(webhook.EventFileClosed, func(event *webhook.Event) error {
		// 获取完整文件路径
		fullPath := a.webhook.GetFullPath(event.EventData.RelativePath)
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

	// 8. 初始化 Wails 应用绑定
	log.Println("初始化应用绑定...")
	a.app = app.NewApp()
	a.app.SetConfig(a.config)
	a.app.SetProcessor(a.processor)
	a.app.SetStorage(a.storage)
	if a.autostart != nil {
		a.app.SetAutostart(a.autostart)
	}

	// 设置回调函数
	a.app.SetCallbacks(
		func() { a.handleQuit() },       // onQuit
		func() { a.triggerScan() },      // onScanNow
		func() { a.handleShowWindow() }, // onShowWindow
		func() { a.handleHideWindow() }, // onHideWindow
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

	// 启动 Webhook 服务器（非阻塞）
	go func() {
		if err := a.webhook.Start(); err != nil {
			log.Printf("Webhook 服务器错误: %v", err)
		}
	}()

	log.Println("后台服务已启动")
	return nil
}

// StartTray 启动系统托盘
func (a *Application) StartTray() {
	log.Println("初始化系统托盘...")

	// 检查是否启用开机自启动
	autoRunEnabled := false
	if a.autostart != nil {
		enabled, err := a.autostart.IsEnabled()
		if err == nil {
			autoRunEnabled = enabled
		}
	}

	// 创建托盘实例
	a.tray = tray.New(tray.Config{
		Tooltip:        fmt.Sprintf("BililiveRecorder 自动整理工具 %s", Version),
		AutoRunEnabled: autoRunEnabled,
	})

	// 注册托盘事件处理
	a.tray.OnAction(func(action tray.Action) {
		log.Printf("[TRAY-HANDLER] 收到托盘动作: %s", action)

		switch action {
		case tray.ActionShowWindow:
			log.Println("[TRAY-HANDLER] 处理 ShowWindow")
			a.handleShowWindow()
		case tray.ActionHideWindow:
			log.Println("[TRAY-HANDLER] 处理 HideWindow")
			a.handleHideWindow()
		case tray.ActionScanNow:
			log.Println("[TRAY-HANDLER] 处理 ScanNow")
			a.triggerScan()
		case tray.ActionToggleAutoRun:
			log.Println("[TRAY-HANDLER] 处理 ToggleAutoRun")
			a.handleToggleAutoRun()
		case tray.ActionQuit:
			log.Println("[TRAY-HANDLER] 处理 Quit")
			a.handleQuit()
		default:
			log.Printf("[TRAY-HANDLER] 未知动作: %s", action)
		}
		log.Printf("[TRAY-HANDLER] 动作 %s 处理完成", action)
	})

	// 非阻塞启动托盘
	a.tray.Run()
	log.Println("系统托盘已启动")
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
	log.Printf("[WINDOW] wailsCtx=%v, tray=%v", a.wailsCtx != nil, a.tray != nil)
	if a.wailsCtx != nil {
		log.Println("[WINDOW] 调用 WindowShow")
		wailsRuntime.WindowShow(a.wailsCtx)
		log.Println("[WINDOW] WindowShow 返回")
		if a.tray != nil {
			log.Println("[WINDOW] 更新托盘状态为可见")
			a.tray.SetWindowVisible(true)
		}
	} else {
		log.Println("[WINDOW] 警告: wailsCtx 为 nil，无法显示窗口")
	}
	log.Println("[WINDOW] handleShowWindow 完成")
}

// handleHideWindow 隐藏窗口
func (a *Application) handleHideWindow() {
	log.Println("[WINDOW] handleHideWindow 开始")
	log.Printf("[WINDOW] wailsCtx=%v, tray=%v", a.wailsCtx != nil, a.tray != nil)
	if a.wailsCtx != nil {
		log.Println("[WINDOW] 调用 WindowHide")
		wailsRuntime.WindowHide(a.wailsCtx)
		log.Println("[WINDOW] WindowHide 返回")
		if a.tray != nil {
			log.Println("[WINDOW] 更新托盘状态为隐藏")
			a.tray.SetWindowVisible(false)
		}
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
		if a.tray != nil {
			a.tray.SetAutoRunChecked(false)
		}
	} else {
		if err := a.autostart.Enable(); err != nil {
			log.Printf("启用开机自启动失败: %v", err)
			return
		}
		log.Println("已启用开机自启动")
		if a.tray != nil {
			a.tray.SetAutoRunChecked(true)
		}
	}
}

// handleQuit 退出应用
func (a *Application) handleQuit() {
	log.Println("前端请���退出")
	if a.wailsCtx != nil {
		wailsRuntime.Quit(a.wailsCtx)
	}
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

	// 停止系统托盘
	if a.tray != nil {
		log.Println("[SHUTDOWN] 停止系统托盘...")
		if err := a.tray.Stop(); err != nil {
			log.Printf("[SHUTDOWN] 托盘停止错误: %v", err)
		}
		log.Println("[SHUTDOWN] 系统托盘已停止")
	}

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

	// 关闭数据库
	if a.storage != nil {
		log.Println("[SHUTDOWN] 关闭数据库...")
		a.storage.Close()
		log.Println("[SHUTDOWN] 数据库已关闭")
	}

	log.Println("所有服务已停止")
}
