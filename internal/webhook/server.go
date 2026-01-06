// Package webhook 提供 Webhook 服务功能
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/user/bililive-recorder-autoarchive/internal/transcoder"
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
}

// Config 服务器配置
type Config struct {
	Port         int           // 监听端口
	WebhookPath  string        // Webhook 路径
	DedupeWindow time.Duration // 去重时间窗口，超过此时间的 EventId 会被清理
	InputDir     string        // 录播姬工作目录，用于拼接完整路径
	MaxFPS       float64       // 帧率上限，0 表示不限制
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

// Start 启动服务器
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc(s.config.WebhookPath, s.handleWebhook)

	// 转码相关 API 端点
	mux.HandleFunc("/api/transcode/select-folder", s.handleSelectFolder)
	mux.HandleFunc("/api/transcode/scan", s.handleScanFolder)
	mux.HandleFunc("/api/transcode/start", s.handleStartTranscode)
	mux.HandleFunc("/api/transcode/tasks", s.handleGetTasks)
	mux.HandleFunc("/api/transcode/cancel", s.handleCancelTask)

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.config.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	// 启动定时清理过期的去重记录
	go s.cleanupExpiredEvents()

	log.Printf("Webhook 服务器启动在端口 %d，路径 %s", s.config.Port, s.config.WebhookPath)
	return s.server.ListenAndServe()
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
func (s *Server) GetFullPath(relativePath string) string {
	if s.config.InputDir == "" {
		return relativePath
	}
	return s.config.InputDir + "/" + relativePath
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
		Files      []string `json:"files"`
		CustomArgs string   `json:"customArgs"`
		OutputDir  string   `json:"outputDir"`
		OutputExt  string   `json:"outputExt"`
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

	config := transcoder.TranscodeConfig{
		CustomArgs: req.CustomArgs,
		OutputDir:  req.OutputDir,
		OutputExt:  req.OutputExt,
		MaxFPS:     s.config.MaxFPS,
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
