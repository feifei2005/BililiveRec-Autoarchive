// Package webhook 提供 Webhook 服务功能
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
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

// Server Webhook 服务器
type Server struct {
	config       Config
	server       *http.Server
	handlers     map[EventType][]Handler
	processedIDs sync.Map // 用于事件去重，存储已处理的 EventId
	mu           sync.RWMutex
}

// Config 服务器配置
type Config struct {
	Port         int           // 监听端口
	WebhookPath  string        // Webhook 路径
	DedupeWindow time.Duration // 去重时间窗口，超过此时间的 EventId 会被清理
	InputDir     string        // 录播姬工作目录，用于拼接完整路径
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

// Start 启动服务器
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc(s.config.WebhookPath, s.handleWebhook)

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.config.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
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
