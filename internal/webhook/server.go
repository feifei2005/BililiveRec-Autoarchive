// Package webhook 提供 Webhook 服务功能
package webhook

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/user/bililive-recorder-autoarchive/internal/transcoder"
	"github.com/user/bililive-recorder-autoarchive/internal/webauth"
)

// EventType 事件类型
type EventType string

const (
	EventFileClosed EventType = "FileClosed" // 文件关闭事件
	EventFileOpened EventType = "FileOpened" // 文件打开事件
)

// Event Webhook 事件（录播姬 Webhook v2 协议）
type Event struct {
	EventType      EventType `json:"EventType"`
	EventTimestamp time.Time `json:"EventTimestamp"`
	EventId        string    `json:"EventId"` // 事件唯一标识，用于去重
	EventData      EventData `json:"EventData"`
}

// EventData 事件数据
type EventData struct {
	RoomID       int    `json:"RoomId"`
	ShortID      int    `json:"ShortId"`
	Name         string `json:"Name"`
	Title        string `json:"Title"`
	RelativePath string `json:"RelativePath"`
	FileSize     int64  `json:"FileSize"`
	SessionID    string `json:"SessionId"`
}

// Handler 事件处理函数类型
type Handler func(event *Event) error

// TranscoderProvider 转码器提供者接口
type TranscoderProvider interface {
	GetTranscoder() *transcoder.Transcoder
}

// Server Webhook 服务器
type Server struct {
	config       Config
	server       *http.Server
	handlers     map[EventType][]Handler
	processedIDs sync.Map // 用于事件去重，存储已处理的 EventId
	mu           sync.RWMutex
	app          TranscoderProvider // 应用程序实例，用于获取转码器
	webUI        http.Handler
	appAPI       http.Handler
	auth         *webauth.Manager
}

// Config 服务器配置
type Config struct {
	BindAddress      string                     // 监听地址；留空表示所有网卡
	APIToken         string                     // 可选 Bearer Token
	Port             int                        // 监听端口
	WebhookPath      string                     // Webhook 路径
	DedupeWindow     time.Duration              // 去重时间窗口，超过此时间的 EventId 会被清理
	InputDir         string                     // 录播姬工作目录，用于拼接完整路径
	MaxFPS           float64                    // 帧率上限，0 表示不限制
	DefaultTranscode transcoder.TranscodeConfig // API 请求未指定字段时使用
}

// New 创建新的 Webhook 服务器实例
func New(cfg Config) *Server {
	if cfg.WebhookPath == "" {
		cfg.WebhookPath = "/webhook"
	}
	if cfg.DedupeWindow == 0 {
		cfg.DedupeWindow = 1 * time.Hour // 默认保留 1 小时的去重记录
	}
	return &Server{
		config:   cfg,
		handlers: make(map[EventType][]Handler),
	}
}

// On 注册事件处理函数
func (s *Server) On(eventType EventType, handler Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[eventType] = append(s.handlers[eventType], handler)
}

// SetApp 设置应用程序实例
func (s *Server) SetApp(app TranscoderProvider) {
	s.app = app
}

// SetWebUI installs the browser UI at the root path.
func (s *Server) SetWebUI(handler http.Handler) { s.webUI = handler }

// SetAppAPI installs the browser-to-Go RPC bridge.
func (s *Server) SetAppAPI(handler http.Handler) { s.appAPI = handler }

// SetAuth enables single-user browser sessions while retaining API-token access.
func (s *Server) SetAuth(auth *webauth.Manager) { s.auth = auth }

// Start 启动服务器
func (s *Server) Start() error {
	mux := http.NewServeMux()
	if s.webUI != nil {
		mux.Handle("/", s.webUI)
	} else {
		mux.HandleFunc("/", s.handleInfo)
	}
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc(s.config.WebhookPath, s.handleWebhook)
	if s.appAPI != nil {
		mux.Handle("/api/app/", s.appAPI)
	}
	if s.auth != nil {
		s.auth.RegisterRoutes(mux)
	}

	// 转码相关 API 端点
	mux.HandleFunc("/api/transcode/select-folder", s.handleSelectFolder)
	mux.HandleFunc("/api/transcode/scan", s.handleScanFolder)
	mux.HandleFunc("/api/transcode/start", s.handleStartTranscode)
	mux.HandleFunc("/api/transcode/tasks", s.handleGetTasks)
	mux.HandleFunc("/api/transcode/cancel", s.handleCancelTask)

	listenAddress := fmt.Sprintf(":%d", s.config.Port)
	if s.config.BindAddress != "" {
		listenAddress = fmt.Sprintf("%s:%d", s.config.BindAddress, s.config.Port)
	}
	var handler http.Handler = mux
	if s.auth != nil {
		handler = s.auth.Middleware(handler, s.config.APIToken, s.config.WebhookPath)
	} else if s.config.APIToken != "" {
		handler = s.requireAPIToken(handler)
	} else if s.config.BindAddress == "" || s.config.BindAddress == "0.0.0.0" || s.config.BindAddress == "::" {
		log.Printf("警告: HTTP 服务监听外部网卡但未配置 api_token")
	}
	s.server = &http.Server{
		Addr:         listenAddress,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	// 启动定时清理过期的去重记录
	go s.cleanupExpiredEvents()

	log.Printf("HTTP 服务器启动在 %s，Webhook 路径 %s", listenAddress, s.config.WebhookPath)
	return s.server.ListenAndServe()
}

func (s *Server) requireAPIToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health checks and BililiveRecorder webhook delivery do not carry the
		// management API token. The webhook remains constrained to InputDir.
		if r.URL.Path == "/healthz" || r.URL.Path == s.config.WebhookPath {
			next.ServeHTTP(w, r)
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if provided == "" {
			provided = r.Header.Get("X-API-Key")
		}
		if provided == "" {
			if cookie, err := r.Cookie("server_token"); err == nil {
				provided = cookie.Value
			}
		}
		if provided == "" {
			provided = r.URL.Query().Get("token")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(s.config.APIToken)) == 1 {
				http.SetCookie(w, &http.Cookie{Name: "server_token", Value: provided, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
				cleanURL := *r.URL
				query := cleanURL.Query()
				query.Del("token")
				cleanURL.RawQuery = query.Encode()
				http.Redirect(w, r, cleanURL.String(), http.StatusSeeOther)
				return
			}
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.config.APIToken)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"name":   "server",
		"status": "ok",
		"endpoints": []string{
			"GET /healthz",
			"POST /api/transcode/scan",
			"POST /api/transcode/start",
			"GET /api/transcode/tasks",
			"POST /api/transcode/cancel",
		},
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// Stop 停止服务器
func (s *Server) Stop(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// cleanupExpiredEvents 定期清理过期的事件ID记录
func (s *Server) cleanupExpiredEvents() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		s.processedIDs.Range(func(key, value interface{}) bool {
			if timestamp, ok := value.(time.Time); ok {
				if now.Sub(timestamp) > s.config.DedupeWindow {
					s.processedIDs.Delete(key)
				}
			}
			return true
		})
	}
}

// isDuplicate 检查事件是否重复
func (s *Server) isDuplicate(eventId string) bool {
	if eventId == "" {
		return false // 没有 EventId 的事件不去重
	}

	_, loaded := s.processedIDs.LoadOrStore(eventId, time.Now())
	return loaded
}

// handleWebhook 处理 Webhook 请求
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRemoteAddr(r.RemoteAddr) {
		http.Error(w, "webhook is only available from localhost", http.StatusForbidden)
		return
	}

	// 尽快返回响应，避免录播姬等待超时
	defer func() {
		w.WriteHeader(http.StatusNoContent)
	}()

	if r.Method != http.MethodPost {
		log.Printf("Webhook 收到非 POST 请求: %s", r.Method)
		return
	}

	var event Event
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		log.Printf("解析 Webhook 事件失败: %v", err)
		return
	}

	log.Printf("收到 Webhook 事件: %s, EventId: %s, 文件: %s",
		event.EventType, event.EventId, event.EventData.RelativePath)

	// 检查事件是否重复
	if s.isDuplicate(event.EventId) {
		log.Printf("跳过重复事件: %s", event.EventId)
		return
	}

	// 异步处理事件，不阻塞 HTTP 响应
	go s.processEvent(&event)
}

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// processEvent 异步处理事件
func (s *Server) processEvent(event *Event) {
	s.mu.RLock()
	handlers, ok := s.handlers[event.EventType]
	s.mu.RUnlock()

	if !ok || len(handlers) == 0 {
		return
	}

	for _, handler := range handlers {
		if err := handler(event); err != nil {
			log.Printf("处理事件 %s (ID: %s) 失败: %v", event.EventType, event.EventId, err)
		}
	}
}

// GetFullPath 获取文件的完整路径
// 将录播姬返回的相对路径与配置的工作目录拼接
func (s *Server) GetFullPath(relativePath string) (string, error) {
	if strings.TrimSpace(s.config.InputDir) == "" {
		return "", fmt.Errorf("webhook input directory is not configured")
	}
	if strings.TrimSpace(relativePath) == "" {
		return "", fmt.Errorf("webhook relative path is empty")
	}

	basePath, err := filepath.Abs(s.config.InputDir)
	if err != nil {
		return "", fmt.Errorf("resolve webhook input directory: %w", err)
	}
	basePath, err = filepath.EvalSymlinks(basePath)
	if err != nil {
		return "", fmt.Errorf("resolve webhook input directory symlinks: %w", err)
	}

	localPath := filepath.FromSlash(relativePath)
	if filepath.IsAbs(localPath) {
		return "", fmt.Errorf("webhook path must be relative")
	}
	fullPath, err := filepath.EvalSymlinks(filepath.Join(basePath, filepath.Clean(localPath)))
	if err != nil {
		return "", fmt.Errorf("resolve webhook file path: %w", err)
	}
	rel, err := filepath.Rel(basePath, fullPath)
	if err != nil {
		return "", fmt.Errorf("validate webhook file path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("webhook path escapes input directory")
	}
	return fullPath, nil
}

// ================== 转码 API 处理函数 ==================

// handleSelectFolder 处理文件夹选择请求
func (s *Server) handleSelectFolder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	path, err := selectFolderDialog()
	if err != nil {
		log.Printf("选择文件夹失败: %v", err)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"path":    path,
	})
}

// selectFolderDialog 使用 PowerShell 调用文件夹选择对话框
func selectFolderDialog() (string, error) {
	// 使用 -NoProfile 加速启动，-WindowStyle Hidden 隐藏窗口
	psScript := `
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = '选择要转码的文件夹'
$dialog.ShowNewFolderButton = $true
# 使用隐藏窗口作为父窗口来确保对话框显示在最前面
$form = New-Object System.Windows.Forms.Form
$form.TopMost = $true
$result = $dialog.ShowDialog($form)
if ($result -eq [System.Windows.Forms.DialogResult]::OK) {
    Write-Host $dialog.SelectedPath
}
$form.Dispose()
`
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", psScript)

	// 隐藏 PowerShell 窗口
	hidePowerShellWindow(cmd)

	output, err := cmd.Output()
	if err != nil {
		// 检查是否有 stderr 输出
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("执行 PowerShell 失败: %w, stderr: %s", err, string(exitErr.Stderr))
		}
		return "", fmt.Errorf("执行 PowerShell 失败: %w", err)
	}

	result := strings.TrimSpace(string(output))
	// 移除可能的 BOM 或其他不可见字符
	result = strings.TrimPrefix(result, "\ufeff")
	return result, nil
}

// handleScanFolder 处理文件夹扫描请求
func (s *Server) handleScanFolder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Invalid request body",
		})
		return
	}

	if req.Path == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Path is required",
		})
		return
	}

	if s.app == nil || s.app.GetTranscoder() == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Transcoder not initialized",
		})
		return
	}

	videos, err := s.app.GetTranscoder().ScanFolder(req.Path)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"files":   videos,
	})
}

// handleStartTranscode 处理开始转码请求
func (s *Server) handleStartTranscode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Files                 []string `json:"files"`
		CustomArgs            string   `json:"customArgs"`
		InputArgs             string   `json:"inputArgs"`
		OutputDir             string   `json:"outputDir"`
		OutputExt             string   `json:"outputExt"`
		QSVReinitStrategy     string   `json:"qsvReinitStrategy"`
		LimitResolution       *bool    `json:"limitResolution"`
		DeleteSourceOnSuccess *bool    `json:"deleteSourceOnSuccess"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Invalid request body",
		})
		return
	}

	if len(req.Files) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "No files specified",
		})
		return
	}

	if s.app == nil || s.app.GetTranscoder() == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Transcoder not initialized",
		})
		return
	}

	tc := s.app.GetTranscoder()
	taskIDs := make([]string, 0, len(req.Files))

	config := s.config.DefaultTranscode
	if req.CustomArgs != "" {
		config.CustomArgs = req.CustomArgs
	}
	if req.InputArgs != "" {
		config.InputArgs = req.InputArgs
	}
	if req.OutputDir != "" {
		config.OutputDir = req.OutputDir
	}
	if req.OutputExt != "" {
		config.OutputExt = req.OutputExt
	}
	if req.QSVReinitStrategy != "" {
		config.QSVReinitStrategy = req.QSVReinitStrategy
	}
	if req.LimitResolution != nil {
		config.LimitResolution = *req.LimitResolution
	}
	if req.DeleteSourceOnSuccess != nil {
		config.DeleteSourceOnSuccess = *req.DeleteSourceOnSuccess
	}
	if config.MaxFPS == 0 {
		config.MaxFPS = s.config.MaxFPS
	}

	for _, file := range req.Files {
		task, err := tc.AddTask(file, config)
		if err != nil {
			log.Printf("添加转码任务失败: %s, 错误: %v", file, err)
			continue
		}
		taskIDs = append(taskIDs, task.ID)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"taskIds": taskIDs,
	})
}

// handleGetTasks 处理获取任务列表请求
func (s *Server) handleGetTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.app == nil || s.app.GetTranscoder() == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Transcoder not initialized",
		})
		return
	}

	tasks := s.app.GetTranscoder().GetAllTasks()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"tasks":   tasks,
	})
}

// handleCancelTask 处理取消任务请求
func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		TaskID string `json:"taskId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Invalid request body",
		})
		return
	}

	if req.TaskID == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Task ID is required",
		})
		return
	}

	if s.app == nil || s.app.GetTranscoder() == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Transcoder not initialized",
		})
		return
	}

	if err := s.app.GetTranscoder().CancelTask(req.TaskID); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}
