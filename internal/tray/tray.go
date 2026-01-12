// Package tray 提供系统托盘功能
package tray

import (
	"log"
	"sync"
	"time"

	"github.com/getlantern/systray"
)

// Action 托盘菜单动作类型
type Action string

const (
	ActionShowWindow    Action = "show_window"    // 显示主窗口
	ActionHideWindow    Action = "hide_window"    // 隐藏主窗口
	ActionScanNow       Action = "scan_now"       // 立即扫描
	ActionSettings      Action = "settings"       // 设置
	ActionToggleAutoRun Action = "toggle_autorun" // 切换开机自启动
	ActionQuit          Action = "quit"           // 退出
)

// Handler 动作处理函数
type Handler func(action Action)

// Tray 系统托盘接口
type Tray interface {
	// Start 启动托盘（阻塞调用，需在主线程运行）
	Start() error

	// Run 启动托盘（非阻塞，内部启动 goroutine）
	Run()

	// Stop 停止托盘
	Stop() error

	// SetTooltip 设置托盘提示文本
	SetTooltip(text string)

	// ShowNotification 显示通知
	ShowNotification(title, message string)

	// OnAction 注册动作处理函数
	OnAction(handler Handler)

	// SetAutoRunChecked 设置开机自启动菜单项的选中状态
	SetAutoRunChecked(checked bool)

	// SetWindowVisible 设置窗口显示/隐藏菜单项状态
	SetWindowVisible(visible bool)
}

// Config 托盘配置
type Config struct {
	IconData       []byte // 图标数据（嵌入的 ICO 文件）
	Tooltip        string // 默认提示文本
	AutoRunEnabled bool   // 开机自启动是否启用
}

// DefaultTray 默认托盘实现
type DefaultTray struct {
	config        Config
	handler       Handler
	mu            sync.RWMutex
	running       bool
	windowVisible bool

	// 菜单项引用
	mShowHide *systray.MenuItem
	mAutoRun  *systray.MenuItem
	mScan     *systray.MenuItem
	mQuit     *systray.MenuItem

	// 用于优雅关闭的 channel
	stopCh chan struct{}

	// 心跳计数器，用于监控托盘运行状态
	heartbeatCount uint64
}

// New 创建新的托盘实例
func New(cfg Config) *DefaultTray {
	if cfg.Tooltip == "" {
		cfg.Tooltip = "BililiveRecorder 自动整理工具"
	}
	return &DefaultTray{
		config:        cfg,
		windowVisible: true, // 默认窗口可见
		stopCh:        make(chan struct{}),
	}
}

// Start 启动托盘（阻塞调用）
func (t *DefaultTray) Start() error {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return nil
	}
	t.running = true
	// 重新创建 stopCh（以防之前被关闭）
	t.stopCh = make(chan struct{})
	t.mu.Unlock()

	// systray.Run 是阻塞的，会在退出时返回
	systray.Run(t.onReady, t.onExit)
	return nil
}

// Run 启动托盘（非阻塞）
func (t *DefaultTray) Run() {
	go t.Start()
}

// Stop 停止托盘
func (t *DefaultTray) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.running {
		return nil
	}

	// 关闭 stopCh 通知所有 goroutine 退出
	select {
	case <-t.stopCh:
		// 已经关闭
	default:
		close(t.stopCh)
	}

	systray.Quit()
	t.running = false
	log.Println("[TRAY] 系统托盘已停止")
	return nil
}

// SetTooltip 设置托盘提示文本
func (t *DefaultTray) SetTooltip(text string) {
	systray.SetTooltip(text)
}

// ShowNotification 显示通知
func (t *DefaultTray) ShowNotification(title, message string) {
	// systray 本身不支持通知，记录日志
	log.Printf("通知: %s - %s", title, message)
}

// OnAction 注册动作处理函数
func (t *DefaultTray) OnAction(handler Handler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.handler = handler
}

// SetAutoRunChecked 设置开机自启动菜单项的选中状态
func (t *DefaultTray) SetAutoRunChecked(checked bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.mAutoRun == nil {
		return
	}

	if checked {
		t.mAutoRun.Check()
	} else {
		t.mAutoRun.Uncheck()
	}
}

// SetWindowVisible 设置窗口显示/隐藏菜单项状态
func (t *DefaultTray) SetWindowVisible(visible bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.windowVisible = visible

	if t.mShowHide == nil {
		return
	}

	if visible {
		t.mShowHide.SetTitle("隐藏主界面")
	} else {
		t.mShowHide.SetTitle("显示主界面")
	}
}

// onReady 托盘就绪回调
func (t *DefaultTray) onReady() {
	log.Println("[TRAY] onReady 开始执行")

	// 设置托盘图标
	var iconData []byte
	if len(t.config.IconData) > 0 {
		iconData = t.config.IconData
		log.Printf("[TRAY] 使用配置图标，大小: %d 字节", len(iconData))
	} else {
		iconData = defaultIcon()
		log.Printf("[TRAY] 使用默认图标，大小: %d 字节", len(iconData))
	}
	systray.SetIcon(iconData)
	log.Println("[TRAY] SetIcon 完成")

	systray.SetTitle("BililiveRecorder")
	systray.SetTooltip(t.config.Tooltip)

	// 添加菜单项
	t.mShowHide = systray.AddMenuItem("隐藏主界面", "显示或隐藏主界面")
	systray.AddSeparator()
	t.mScan = systray.AddMenuItem("立即扫描", "立即执行全量扫描")
	systray.AddSeparator()
	t.mAutoRun = systray.AddMenuItemCheckbox("开机自启动", "设置开机自动启动", t.config.AutoRunEnabled)
	systray.AddSeparator()
	t.mQuit = systray.AddMenuItem("退出", "退出程序")

	log.Println("[TRAY] 系统托盘已启动，菜单项已创建")

	// 处理菜单点击事件
	go t.handleClicks()

	// 启动心跳检测和定时刷新，避免长期运行无响应
	go t.heartbeat()
}

// handleClicks 处理菜单点击事件
func (t *DefaultTray) handleClicks() {
	log.Println("[TRAY] handleClicks 协程已启动")
	for {
		select {
		case <-t.stopCh:
			log.Println("[TRAY] handleClicks 收到停止信号，协程退出")
			return

		case <-t.mShowHide.ClickedCh:
			log.Println("[TRAY] 收到 显示/隐藏 点击事件")
			t.mu.RLock()
			handler := t.handler
			visible := t.windowVisible
			t.mu.RUnlock()
			log.Printf("[TRAY] handler=%v, visible=%v", handler != nil, visible)

			if handler != nil {
				if visible {
					log.Println("[TRAY] 触发 ActionHideWindow")
					go handler(ActionHideWindow) // 异步处理，不阻塞消息循环
				} else {
					log.Println("[TRAY] 触发 ActionShowWindow")
					go handler(ActionShowWindow) // 异步处理，不阻塞消息循环
				}
			} else {
				log.Println("[TRAY] 警告: handler 为 nil")
			}

		case <-t.mScan.ClickedCh:
			log.Println("[TRAY] 收到 立即扫描 点击事件")
			t.mu.RLock()
			handler := t.handler
			t.mu.RUnlock()

			if handler != nil {
				log.Println("[TRAY] 触发 ActionScanNow")
				go handler(ActionScanNow) // 异步处理，不阻塞消息��环
			} else {
				log.Println("[TRAY] 警告: handler 为 nil")
			}

		case <-t.mAutoRun.ClickedCh:
			log.Println("[TRAY] 收到 开机自启动 点击事件")
			t.mu.RLock()
			handler := t.handler
			t.mu.RUnlock()

			if handler != nil {
				log.Println("[TRAY] 触发 ActionToggleAutoRun")
				go handler(ActionToggleAutoRun) // 异步处理，不阻塞消息循环
			} else {
				log.Println("[TRAY] 警告: handler 为 nil")
			}

		case <-t.mQuit.ClickedCh:
			log.Println("[TRAY] 收到 退出 点击事件")
			t.mu.RLock()
			handler := t.handler
			t.mu.RUnlock()

			if handler != nil {
				log.Println("[TRAY] 触发 ActionQuit")
				go handler(ActionQuit) // 异步处理，不阻塞消息循环
			}
			log.Println("[TRAY] handleClicks 协程退出")
			return
		}
	}
}

// heartbeat 心跳检测和定时刷新
// 定期刷新托盘状态，避免 Windows 消息队列阻塞导致托盘无响应
func (t *DefaultTray) heartbeat() {
	log.Println("[TRAY] heartbeat 协程已启动")

	// 心跳间隔：5分钟
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-t.stopCh:
			log.Println("[TRAY] heartbeat 收到停止信号，协程退出")
			return

		case <-ticker.C:
			t.mu.Lock()
			t.heartbeatCount++
			count := t.heartbeatCount
			running := t.running
			t.mu.Unlock()

			if !running {
				log.Println("[TRAY] heartbeat: 托盘已停止，协程退出")
				return
			}

			// 记录心跳日志
			log.Printf("[TRAY] heartbeat #%d: 托盘运行正常", count)

			// 刷新托盘提示文本，触发 Windows 消息循环
			// 这有助于保持托盘图标的响应性
			t.refreshTray()
		}
	}
}

// refreshTray 刷新托盘状态
// 通过重新设置 tooltip 来触发 Windows 消息循环更新
func (t *DefaultTray) refreshTray() {
	t.mu.RLock()
	tooltip := t.config.Tooltip
	t.mu.RUnlock()

	// 重新设置 tooltip 以触发刷新
	systray.SetTooltip(tooltip)
}

// onExit 托盘退出回调
func (t *DefaultTray) onExit() {
	log.Println("[TRAY] 托盘已退出")

	// 确保 stopCh 被关闭，通知所有 goroutine 退出
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.running {
		// 尝试关闭 stopCh（如果还没被关闭）
		select {
		case <-t.stopCh:
			// 已经关闭
		default:
			close(t.stopCh)
		}
		t.running = false
	}
}

// defaultIcon 返回默认图标数据
// 这是一个完整的 16x16 32位 ICO 文件（蓝色方块）
func defaultIcon() []byte {
	// ICO 文件结构:
	// - ICO Header (6 bytes)
	// - Directory Entry (16 bytes)
	// - BITMAPINFOHEADER (40 bytes)
	// - Pixel Data (16x16x4 = 1024 bytes) BGRA format, bottom-to-top
	// - AND Mask (64 bytes)
	// Total: 1150 bytes

	icon := make([]byte, 0, 1150)

	// ICO Header (6 bytes)
	icon = append(icon,
		0x00, 0x00, // Reserved
		0x01, 0x00, // Image type: ICO
		0x01, 0x00, // Image count: 1
	)

	// Directory Entry (16 bytes)
	icon = append(icon,
		0x10,       // Width: 16
		0x10,       // Height: 16
		0x00,       // Color palette: 0 (no palette)
		0x00,       // Reserved
		0x01, 0x00, // Color planes: 1
		0x20, 0x00, // Bits per pixel: 32
		0x68, 0x04, 0x00, 0x00, // Image data size: 1128 bytes (40+1024+64)
		0x16, 0x00, 0x00, 0x00, // Image data offset: 22 bytes (6+16)
	)

	// BITMAPINFOHEADER (40 bytes)
	icon = append(icon,
		0x28, 0x00, 0x00, 0x00, // Header size: 40
		0x10, 0x00, 0x00, 0x00, // Width: 16
		0x20, 0x00, 0x00, 0x00, // Height: 32 (doubled for XOR+AND mask)
		0x01, 0x00, // Planes: 1
		0x20, 0x00, // Bit count: 32
		0x00, 0x00, 0x00, 0x00, // Compression: BI_RGB
		0x00, 0x04, 0x00, 0x00, // Image size: 1024 (can be 0 for BI_RGB)
		0x00, 0x00, 0x00, 0x00, // X pixels per meter
		0x00, 0x00, 0x00, 0x00, // Y pixels per meter
		0x00, 0x00, 0x00, 0x00, // Colors used
		0x00, 0x00, 0x00, 0x00, // Colors important
	)

	// Pixel Data: 16x16 pixels, BGRA format (bottom-to-top, left-to-right)
	// 蓝色主题图标，带圆角效果
	// B=0x4F, G=0x8E, R=0xF5, A=0xFF (Bilibili 粉色/橙色调)
	for row := 0; row < 16; row++ {
		for col := 0; col < 16; col++ {
			// 创建一个简单的圆角矩形效果
			isCorner := (row < 2 || row > 13) && (col < 2 || col > 13)
			isEdgeCorner := (row == 0 || row == 15) && (col == 0 || col == 15)

			if isEdgeCorner {
				// 透明角落
				icon = append(icon, 0x00, 0x00, 0x00, 0x00) // BGRA: transparent
			} else if isCorner {
				// 半透明过渡
				icon = append(icon, 0x4F, 0x8E, 0xF5, 0x80) // BGRA: semi-transparent pink
			} else {
				// 主体颜色 (Bilibili 粉色)
				icon = append(icon, 0x4F, 0x8E, 0xF5, 0xFF) // BGRA: solid pink/salmon
			}
		}
	}

	// AND Mask: 64 bytes (16 rows x 4 bytes, padded to 32-bit boundary)
	// 全0表示所有像素都显示（由 alpha 通道控制透明度）
	for i := 0; i < 64; i++ {
		icon = append(icon, 0x00)
	}

	return icon
}
