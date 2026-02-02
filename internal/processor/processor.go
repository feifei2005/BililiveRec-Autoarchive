// Package processor 提供文件处理引擎功能
package processor

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/user/bililive-recorder-autoarchive/internal/ffmpeg"
	"github.com/user/bililive-recorder-autoarchive/internal/scanner"
	"github.com/user/bililive-recorder-autoarchive/internal/storage"
)

// RemuxProgress 转封装进度信息
type RemuxProgress struct {
	SourceSize         int64         `json:"sourceSize"`         // 源文件总大小（字节）
	WrittenSize        int64         `json:"writtenSize"`        // 已写入大小（字节）
	SpeedBytesPerSec   float64       `json:"speedBytesPerSec"`   // 当前速度（字节/秒）
	Progress           float64       `json:"progress"`           // 进度百分比 (0-100)
	EstimatedRemaining float64       `json:"estimatedRemaining"` // 预计剩余时间（秒）
	ElapsedSeconds     float64       `json:"elapsedSeconds"`     // 已用时间（秒）
	IsActive           bool          `json:"isActive"`           // 是否正在转封装
	IsPaused           bool          `json:"isPaused"`           // 是否暂停
	CurrentFile        string        `json:"currentFile"`        // 当前处理的文件名
	StartTime          time.Time     `json:"-"`                  // 开始时间
	TotalPausedTime    time.Duration `json:"-"`                  // 累计暂停时长
}

// 速度滑动窗口大小
const speedWindowSize = 5

// TaskStatus 任务状态
type TaskStatus string

const (
	StatusPending    TaskStatus = "pending"    // 等待处理
	StatusProcessing TaskStatus = "processing" // 处理中
	StatusSuccess    TaskStatus = "success"    // 已成功
	StatusFailed     TaskStatus = "failed"     // 处理失败
	StatusDiscarded  TaskStatus = "discarded"  // 已丢弃
)

// Task 处理任务
type Task struct {
	ID           string     // 任务ID
	InputPath    string     // 输入文件路径
	OutputPath   string     // 输出文件路径
	StreamerName string     // 主播名称
	RecordDate   string     // 录制日期
	CoverPath    string     // 封面路径
	Status       TaskStatus // 任务状态
	Error        error      // 错误信息
	Progress     float64    // 处理进度 (0-100)
	CreatedAt    time.Time  // 创建时间
	UpdatedAt    time.Time  // 更新时间
}

// Processor 文件处理引擎接口
type Processor interface {
	// Start 启动处理引擎
	Start(ctx context.Context) error

	// Stop 停止处理引擎
	Stop() error

	// Submit 提交处理任务
	Submit(task *Task) error

	// AddTask 添加文件路径到处理队列（供 Webhook 和 Scanner 调用）
	AddTask(path string) error

	// ProcessGroup 处理文件组
	ProcessGroup(group scanner.FileGroup) error

	// Status 获取任务状态
	Status(taskID string) (*Task, error)

	// Tasks 获取所有任务
	Tasks() []*Task
}

// Config 处理引擎配置
type Config struct {
	MaxConcurrent      int                     // 最大并发数
	InputDir           string                  // 输入目录（录播姬工作目录）
	OutputRoot         string                  // 输出根目录
	DiscardDir         string                  // 丢弃目录
	PathTemplate       string                  // 路径模板
	CheckVideoStream   bool                    // 是否检查视频流
	MinFileSizeKB      int64                   // 最小文件大小（KB）
	DiscardFailedFiles bool                    // 处理失败时是否进入丢弃流程
	ConflictMode       ConflictMode            // 文件冲突处理模式
	FFmpeg             ffmpeg.FFmpeg           // FFmpeg 实例
	Storage            storage.Storage         // 持久化存储实例
	DefaultCoverPath   string                  // 全局默认封面路径
	DeleteOriginal     bool                    // 处理成功后是否删除原文件
	ScanInterval       time.Duration           // 扫描间隔
	Scanner            *scanner.DefaultScanner // Scanner 实例
}

// ConflictMode 文件冲突处理模式
type ConflictMode string

const (
	ConflictOverwrite ConflictMode = "overwrite" // 覆盖
	ConflictRename    ConflictMode = "rename"    // 重命名
	ConflictSkip      ConflictMode = "skip"      // 跳过
)

// DefaultProcessor 默认处理引擎实现
type DefaultProcessor struct {
	config       Config
	tasks        map[string]*Task
	pathQueue    chan string // 文件路径队列
	mu           sync.RWMutex
	done         chan struct{}
	wg           sync.WaitGroup // 用于等待所有 worker 退出
	dateRegex    *regexp.Regexp
	storage      storage.Storage
	pendingPaths sync.Map           // 用于任务去重，存储正在处理的文件路径
	ctx          context.Context    // 上下文，用于取消
	cancel       context.CancelFunc // 取消函数

	// 用于跟踪活动任务和等待完成
	activeTasksMu sync.RWMutex
	activeTasks   int           // 当前正在处理的任务数
	taskDone      chan struct{} // 当任务完成时发送信号
	stopped       bool          // 是否已停止

	// 暂停相关状态
	pauseMu                sync.RWMutex
	remuxPaused            bool          // 立即暂停状态
	remuxPauseAfterCurrent bool          // 当前任务后暂停状态
	remuxPausedTime        time.Time     // 暂停开始时间
	remuxTotalPausedTime   time.Duration // 累计暂停时长
	currentRemuxCmd        *exec.Cmd     // 当前转封装进程引用

	// 转封装进度跟踪
	remuxProgressMu   sync.RWMutex
	remuxProgress     RemuxProgress // 当前转封装进度
	remuxSpeedSamples []float64     // 速度滑动窗口样本
	remuxLastSize     int64         // 上次检查时的文件大小
	remuxLastCheck    time.Time     // 上次检查时间
	remuxStopMonitor  chan struct{} // 停止监控信号
}

// New 创建新的处理引擎实例
func New(cfg Config) (*DefaultProcessor, error) {
	// 设置默认值
	if cfg.ConflictMode == "" {
		cfg.ConflictMode = ConflictSkip
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2 // 默认 2 个并发
	}
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = 5 * time.Minute // 默认 5 分钟扫描一次
	}

	// 编译日期正则表达式（匹配录播姬文件名格式: 录制-房间号-YYYYMMDD-HHMMSS）
	dateRegex, err := regexp.Compile(`-\d+-(\d{8})-`)
	if err != nil {
		return nil, fmt.Errorf("invalid date regex: %w", err)
	}

	return &DefaultProcessor{
		config:    cfg,
		tasks:     make(map[string]*Task),
		pathQueue: make(chan string, 100), // 100 个缓冲位
		done:      make(chan struct{}),
		dateRegex: dateRegex,
		storage:   cfg.Storage,
		taskDone:  make(chan struct{}, 100), // 缓冲通道用于任务完成通知
	}, nil
}

// Start 启动处理引擎
// 1. 启动指定数量的 worker 协程
// 2. 运行一次全量扫描
// 3. 启动定时扫描任务
func (p *DefaultProcessor) Start(ctx context.Context) error {
	p.ctx, p.cancel = context.WithCancel(ctx)

	log.Printf("启动处理引擎，并发数: %d", p.config.MaxConcurrent)

	// 启动 worker 协程池
	for i := 0; i < p.config.MaxConcurrent; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}

	// 运行一次全量扫描（处理启动前产生的文件）
	go func() {
		log.Println("执行启动时全量扫描...")
		if err := p.runFullScan(); err != nil {
			log.Printf("启动时全量扫描失败: %v", err)
		}
	}()

	// 启动定时扫描任务
	go p.scheduledScan()

	return nil
}

// Stop 停止处理引擎
func (p *DefaultProcessor) Stop() error {
	log.Println("正在停止处理引擎...")

	// 取消上下文（通知所有 workers 和定时任务退出）
	if p.cancel != nil {
		p.cancel()
	}

	// 关闭 done 通道（信号通知）
	close(p.done)

	// 关闭 pathQueue 通道，解除 workers 的阻塞
	close(p.pathQueue)

	// 等待所有 workers 退出，带超时
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("所有 workers 已退出")
	case <-time.After(5 * time.Second):
		log.Println("警告: 等待 workers 退出超时")
	}

	return nil
}

// Submit 提交处理任务
func (p *DefaultProcessor) Submit(task *Task) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	task.Status = StatusPending
	task.CreatedAt = time.Now()
	task.UpdatedAt = time.Now()
	p.tasks[task.ID] = task

	// 将任务路径加入队列
	select {
	case p.pathQueue <- task.InputPath:
		return nil
	default:
		return ErrQueueFull
	}
}

// AddTask 添加文件路径到处理队列
// 供 Webhook 和 Scanner 调用
// 实现任务去重：同一路径不会被重复加入队列
func (p *DefaultProcessor) AddTask(path string) error {
	// 规范化路径
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	// 生成任务ID
	taskID := generateTaskID(absPath)

	// 检查是否已在处理中（去重）
	if _, loaded := p.pendingPaths.LoadOrStore(absPath, time.Now()); loaded {
		log.Printf("跳过重复任务: %s", absPath)
		return nil
	}

	// 创建 pending 状态的任务对象，使其能在前端「待处理」列表中显示
	now := time.Now()
	task := &Task{
		ID:        taskID,
		InputPath: absPath,
		Status:    StatusPending,
		Progress:  0,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// 尝试解析主播名称（从父目录）
	dir := filepath.Dir(absPath)
	streamerDir := filepath.Base(dir)
	task.StreamerName = parseStreamerNameFromDir(streamerDir)

	p.mu.Lock()
	p.tasks[taskID] = task
	p.mu.Unlock()

	// 加入队列
	select {
	case p.pathQueue <- absPath:
		log.Printf("任务已加入队列: %s (ID: %s)", absPath, taskID)
		return nil
	default:
		// 队列满了，从去重 map 和 tasks 中移除
		p.pendingPaths.Delete(absPath)
		p.mu.Lock()
		delete(p.tasks, taskID)
		p.mu.Unlock()
		return ErrQueueFull
	}
}

// worker 工作协程
// 从队列中获取任务并处理
func (p *DefaultProcessor) worker(id int) {
	defer p.wg.Done()
	log.Printf("Worker %d 启动", id)

	for {
		// 检查是否处于"当前任务后暂停"状态
		p.pauseMu.RLock()
		shouldWait := p.remuxPauseAfterCurrent && !p.remuxPaused
		p.pauseMu.RUnlock()

		if shouldWait {
			// 进入等待状态，不取新任务
			log.Printf("Worker %d: 等待暂停恢复...", id)
			for {
				time.Sleep(500 * time.Millisecond)

				// 检查是否��消或退出
				select {
				case <-p.done:
					log.Printf("Worker %d 停止", id)
					return
				case <-p.ctx.Done():
					log.Printf("Worker %d 收到取消信号", id)
					return
				default:
				}

				// 检查暂停状态是否解除
				p.pauseMu.RLock()
				stillWaiting := p.remuxPauseAfterCurrent
				p.pauseMu.RUnlock()

				if !stillWaiting {
					break
				}
			}
		}

		select {
		case <-p.done:
			log.Printf("Worker %d 停止", id)
			return
		case <-p.ctx.Done():
			log.Printf("Worker %d 收到取消信号", id)
			return
		case path, ok := <-p.pathQueue:
			if !ok {
				// 通道已关闭
				log.Printf("Worker %d: 队列已关闭，退出", id)
				return
			}
			p.processPath(path)
		}
	}
}

// generateTaskID 根据文件路径生成任务ID
func generateTaskID(path string) string {
	hash := md5.Sum([]byte(path))
	return hex.EncodeToString(hash[:8]) // 使用前8字节作为ID
}

// processPath 处理单个文件路径
func (p *DefaultProcessor) processPath(path string) {
	// 增加活动任务计数
	p.activeTasksMu.Lock()
	p.activeTasks++
	p.activeTasksMu.Unlock()

	// 处理完成后减少活动任务计数并发送完成信号
	defer func() {
		p.activeTasksMu.Lock()
		p.activeTasks--
		p.activeTasksMu.Unlock()

		// 非阻塞发送完成信号
		select {
		case p.taskDone <- struct{}{}:
		default:
		}

		// 从去重 map 中移除
		p.pendingPaths.Delete(path)
	}()

	startTime := time.Now()
	log.Printf("开始处理文件: %s", path)

	// 查找或创建任务对象
	taskID := generateTaskID(path)
	var task *Task

	p.mu.Lock()
	if existingTask, ok := p.tasks[taskID]; ok {
		// 任务已存在（由 AddTask 创建的 pending 任务），更新状态
		task = existingTask
		task.Status = StatusProcessing
		task.UpdatedAt = startTime
	} else {
		// 任务不存在，创建新任务（兼容旧的 Submit 方式）
		task = &Task{
			ID:        taskID,
			InputPath: path,
			Status:    StatusProcessing,
			Progress:  0,
			CreatedAt: startTime,
			UpdatedAt: startTime,
		}
		p.tasks[taskID] = task
	}
	p.mu.Unlock()

	// 辅助函数：更新任务状态
	updateTask := func(status TaskStatus, outputPath string, err error, progress float64) {
		p.mu.Lock()
		task.Status = status
		task.OutputPath = outputPath
		task.Error = err
		task.Progress = progress
		task.UpdatedAt = time.Now()
		p.mu.Unlock()
	}

	// 检查文件是否已经成功处理过
	if p.storage != nil && p.storage.IsFileProcessed(path) {
		log.Printf("跳过已处理文件: %s", path)
		updateTask(StatusSuccess, "", nil, 100)
		return
	}

	// 检查文件是否存在
	if _, err := os.Stat(path); os.IsNotExist(err) {
		log.Printf("文件不存在，跳过: %s", path)
		updateTask(StatusFailed, "", fmt.Errorf("文件不存在"), 0)
		return
	}

	// 检查文件是否被占用
	if scanner.IsFileLocked(path) {
		log.Printf("文件被占用，稍后重试: %s", path)
		updateTask(StatusPending, "", nil, 0)
		// 重新加入队列，延迟处理
		go func() {
			time.Sleep(30 * time.Second)
			p.AddTask(path)
		}()
		return
	}

	// 构建文件组
	group := p.buildFileGroup(path)
	if group == nil {
		log.Printf("无法构建文件组: %s", path)
		updateTask(StatusFailed, "", fmt.Errorf("无法构建文件组"), 0)
		return
	}

	// 更新任务的主播信息
	p.mu.Lock()
	task.StreamerName = group.StreamerName
	p.mu.Unlock()

	// 处理文件组
	outputPath, _ := p.buildOutputPath(*group)
	if err := p.ProcessGroup(*group); err != nil {
		// 检查是否是丢弃操作
		if err == ErrDiscarded {
			log.Printf("文件已丢弃: %s", path)
			updateTask(StatusDiscarded, "", nil, 100)

			// 记录丢弃
			if p.storage != nil {
				p.storage.LogProcessResult(storage.ProcessLog{
					InputPath: path,
					Status:    "discarded",
					Error:     "no valid video stream",
					StartTime: startTime,
					EndTime:   time.Now(),
				})
			}
			return
		}

		log.Printf("处理文件失败: %s, 错误: %v", path, err)
		updateTask(StatusFailed, "", err, 0)

		// 清理可能存在的不完整输出文件
		p.cleanupIncompleteOutput(outputPath)

		// 根据配置处理失败的文件
		p.handleFailedFile(*group)

		// 记录处理失败
		if p.storage != nil {
			p.storage.LogProcessResult(storage.ProcessLog{
				InputPath: path,
				Status:    "failed",
				Error:     err.Error(),
				StartTime: startTime,
				EndTime:   time.Now(),
			})
		}
		return
	}

	// 记录处理成功
	updateTask(StatusSuccess, outputPath, nil, 100)

	if p.storage != nil {
		p.storage.LogProcessResult(storage.ProcessLog{
			InputPath:  path,
			OutputPath: outputPath,
			Status:     "success",
			StartTime:  startTime,
			EndTime:    time.Now(),
		})
	}

	log.Printf("文件处理完成: %s", path)
}

// buildFileGroup 根据 FLV 路径构建文件组
func (p *DefaultProcessor) buildFileGroup(flvPath string) *scanner.FileGroup {
	// 获取文件所在目录和基本名称
	dir := filepath.Dir(flvPath)
	baseName := filepath.Base(flvPath)
	ext := filepath.Ext(baseName)

	// 只处理 .flv 文件
	if strings.ToLower(ext) != ".flv" {
		return nil
	}

	nameWithoutExt := strings.TrimSuffix(baseName, ext)

	// 解析主播名称和UID（从父目录名称中提取）
	streamerDir := filepath.Base(dir)
	streamerName := parseStreamerNameFromDir(streamerDir)
	streamerUID := parseStreamerUIDFromDir(streamerDir)

	group := &scanner.FileGroup{
		FLVPath:      flvPath,
		StreamerDir:  streamerDir,
		StreamerName: streamerName,
		StreamerUID:  streamerUID,
	}

	// 查找对应的 XML 文件
	xmlPath := filepath.Join(dir, nameWithoutExt+".xml")
	if _, err := os.Stat(xmlPath); err == nil {
		group.XMLPath = xmlPath
	}

	// 查找封面文件
	coverJpg := filepath.Join(dir, nameWithoutExt+".cover.jpg")
	coverPng := filepath.Join(dir, nameWithoutExt+".cover.png")
	if _, err := os.Stat(coverJpg); err == nil {
		group.CoverPath = coverJpg
	} else if _, err := os.Stat(coverPng); err == nil {
		group.CoverPath = coverPng
	} else if p.config.DefaultCoverPath != "" {
		// 使用默认封面
		if _, err := os.Stat(p.config.DefaultCoverPath); err == nil {
			group.CoverPath = p.config.DefaultCoverPath
		}
	}

	return group
}

// parseStreamerNameFromDir 从目录名解析主播名
// 例如：从 "1776261556-筱田柴" 解析出 "筱田柴"
func parseStreamerNameFromDir(dirName string) string {
	idx := strings.Index(dirName, "-")
	if idx == -1 || idx == len(dirName)-1 {
		return dirName
	}
	return dirName[idx+1:]
}

// parseStreamerUIDFromDir 从目录名解析主播UID
// 例如：从 "1776261556-筱田柴" 解析出 "1776261556"
// 如果目录名不包含 "-"，返回空字符串
func parseStreamerUIDFromDir(dirName string) string {
	idx := strings.Index(dirName, "-")
	if idx == -1 || idx == 0 {
		return ""
	}
	return dirName[:idx]
}

// runFullScan 运行全量扫描
func (p *DefaultProcessor) runFullScan() error {
	if p.config.Scanner == nil {
		log.Println("Scanner 未配置，跳过全量扫描")
		return nil
	}

	groups, err := p.config.Scanner.Scan()
	if err != nil {
		return fmt.Errorf("扫描失败: %w", err)
	}

	log.Printf("全量扫描发现 %d 个文件组", len(groups))

	for _, group := range groups {
		if err := p.AddTask(group.FLVPath); err != nil {
			log.Printf("添加任务失败: %s, 错误: %v", group.FLVPath, err)
		}
	}

	return nil
}

// scheduledScan 定时扫描任务
// 支持 ScanInterval 热重载：每次触发时检查间隔是否变化，如变化则重建 ticker
func (p *DefaultProcessor) scheduledScan() {
	currentInterval := p.config.ScanInterval
	ticker := time.NewTicker(currentInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.done:
			return
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			// 检查扫描间隔是否变化（热重载支持）
			p.mu.RLock()
			newInterval := p.config.ScanInterval
			p.mu.RUnlock()

			if newInterval != currentInterval && newInterval > 0 {
				log.Printf("扫描间隔��变更: %v -> %v", currentInterval, newInterval)
				ticker.Stop()
				currentInterval = newInterval
				ticker = time.NewTicker(currentInterval)
			}

			log.Println("执行定时全量扫描...")
			if err := p.runFullScan(); err != nil {
				log.Printf("定时扫描失败: %v", err)
			}
		}
	}
}

// Status 获取任务状态
func (p *DefaultProcessor) Status(taskID string) (*Task, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	task, ok := p.tasks[taskID]
	if !ok {
		return nil, ErrTaskNotFound
	}
	return task, nil
}

// Tasks 获取所有任务
func (p *DefaultProcessor) Tasks() []*Task {
	p.mu.RLock()
	defer p.mu.RUnlock()

	tasks := make([]*Task, 0, len(p.tasks))
	for _, task := range p.tasks {
		tasks = append(tasks, task)
	}

	// 按更新时间降序排序（最近处理的在前）
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
	})

	return tasks
}

// ProcessGroup 处理文件组
// 这是核心处理逻辑，负责：
// 1. 验证文件有效性
// 2. 判断是否需要丢弃
// 3. 解析输出路径
// 4. 执行转封装
func (p *DefaultProcessor) ProcessGroup(group scanner.FileGroup) error {
	ctx := context.Background()

	// 步骤 1: 检查 FLV 文件是否存在
	if group.FLVPath == "" {
		return fmt.Errorf("missing FLV file in group")
	}

	// 步骤 2: 获取文件信息
	fileInfo, err := os.Stat(group.FLVPath)
	if err != nil {
		return fmt.Errorf("failed to stat FLV file: %w", err)
	}

	// 步骤 3: 判断是否为无效文件
	// 两个独立的过滤条件：
	// - MinFileSizeKB: 快速丢弃网络不稳定产生的短片段
	// - CheckVideoStream: 检测视频流是否有效
	isInvalid := false
	var invalidReason string
	fileSizeKB := fileInfo.Size() / 1024

	// 3.1 检查文件大小（快速过滤短片段）
	if p.config.MinFileSizeKB > 0 && fileSizeKB < p.config.MinFileSizeKB {
		isInvalid = true
		invalidReason = fmt.Sprintf("file size %d KB < minimum %d KB (short segment)", fileSizeKB, p.config.MinFileSizeKB)
		log.Printf("文件过小（短片段）: %s (大小: %d KB < 最小 %d KB)", group.FLVPath, fileSizeKB, p.config.MinFileSizeKB)
	}

	// 3.2 检查视频流有效性（即使文件大小合格也需要检查）
	if !isInvalid && p.config.CheckVideoStream && p.config.FFmpeg != nil {
		log.Printf("[processor] 开始检查视频流有效性: %s (大小: %d KB)", group.FLVPath, fileSizeKB)
		hasVideo, err := p.config.FFmpeg.HasVideoStream(ctx, group.FLVPath)
		log.Printf("[processor] 视频流检查结果: %s, hasVideo=%v, err=%v", group.FLVPath, hasVideo, err)
		if err != nil {
			// 无法检测视频流，可能文件损坏
			isInvalid = true
			invalidReason = fmt.Sprintf("failed to check video stream: %v", err)
			log.Printf("[processor] 视频流检测失败: %s, 错误: %v", group.FLVPath, err)
		} else if !hasVideo {
			// 确认无视频流（width 或 height 为 0）
			isInvalid = true
			invalidReason = fmt.Sprintf("no valid video stream detected (file size: %d KB)", fileSizeKB)
			log.Printf("[processor] 文件无有效视频流: %s (大小: %d KB)", group.FLVPath, fileSizeKB)
		} else {
			log.Printf("[processor] 文件视频流有效: %s", group.FLVPath)
		}
	}

	// 步骤 4: 如果是无效文件，执行丢弃逻辑
	if isInvalid {
		if err := p.discardGroup(group, invalidReason); err != nil {
			return err
		}
		// 返回特殊错误标记文件已被丢弃
		return ErrDiscarded
	}

	// 步骤 5: 解析输出路径
	outputPath, err := p.buildOutputPath(group)
	if err != nil {
		return fmt.Errorf("failed to build output path: %w", err)
	}

	// 步骤 6: 处理文件名冲突
	finalOutputPath, err := p.handleConflict(outputPath)
	if err != nil {
		return fmt.Errorf("failed to handle conflict: %w", err)
	}
	if finalOutputPath == "" {
		// 跳过此文件
		return nil
	}

	// 步骤 7: 确保输出目录存在
	outputDir := filepath.Dir(finalOutputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// 步骤 8: 执行转封装（带进度监控）
	if p.config.FFmpeg != nil {
		// 启动进度监控
		p.startRemuxProgressMonitor(fileInfo.Size(), finalOutputPath, group.FLVPath)
		defer p.stopRemuxProgressMonitor()

		opts := &ffmpeg.RemuxOptions{
			CoverPath: group.CoverPath,
		}
		if err := p.config.FFmpeg.Remux(ctx, group.FLVPath, finalOutputPath, opts); err != nil {
			return fmt.Errorf("failed to remux: %w", err)
		}
	}

	// 步骤 9: 处理 XML 文件（如果存在）
	if group.XMLPath != "" {
		xmlOutputPath := strings.TrimSuffix(finalOutputPath, filepath.Ext(finalOutputPath)) + ".xml"
		if err := p.copyFile(group.XMLPath, xmlOutputPath); err != nil {
			// XML 复制失败不影响主流程，记录日志即可
			fmt.Printf("Warning: failed to copy XML file: %v\n", err)
		}
	}

	// 步骤 10: 删除原始文件（根据配置决定）
	if p.config.DeleteOriginal {
		log.Printf("删除原始文件组: %s", group.FLVPath)
		if err := p.deleteGroup(group); err != nil {
			log.Printf("Warning: 删除原始文件失败: %v", err)
			// 不返回错误，因为主要处理已完成
		}
	}

	return nil
}

// discardGroup 丢弃文件组
// 将文件移动到丢弃目录，或直接删除
func (p *DefaultProcessor) discardGroup(group scanner.FileGroup, reason string) error {
	fmt.Printf("Discarding file group (reason: %s): %s\n", reason, group.FLVPath)

	if p.config.DiscardDir == "" {
		// 没有配置丢弃目录，直接删除
		if err := p.deleteGroup(group); err != nil {
			return fmt.Errorf("failed to delete group: %w", err)
		}
		return nil
	}

	// 移动到丢弃目录
	// 保持相对路径结构：DiscardDir/主播名/文件
	discardPath := filepath.Join(p.config.DiscardDir, group.StreamerName)
	if err := os.MkdirAll(discardPath, 0755); err != nil {
		return fmt.Errorf("failed to create discard directory: %w", err)
	}

	// 移动 FLV 文件
	flvName := filepath.Base(group.FLVPath)
	if err := p.moveFile(group.FLVPath, filepath.Join(discardPath, flvName)); err != nil {
		return fmt.Errorf("failed to move FLV file: %w", err)
	}

	// 移动 XML 文件（如果存在）
	if group.XMLPath != "" {
		xmlName := filepath.Base(group.XMLPath)
		if err := p.moveFile(group.XMLPath, filepath.Join(discardPath, xmlName)); err != nil {
			// XML 移动失败不影响主流程
			fmt.Printf("Warning: failed to move XML file: %v\n", err)
		}
	}

	// 移动封面文件（如果存在）
	if group.CoverPath != "" {
		coverName := filepath.Base(group.CoverPath)
		if err := p.moveFile(group.CoverPath, filepath.Join(discardPath, coverName)); err != nil {
			// 封面移动失败不影响主流程
			fmt.Printf("Warning: failed to move cover file: %v\n", err)
		}
	}

	return nil
}

// deleteGroup 删除文件组
func (p *DefaultProcessor) deleteGroup(group scanner.FileGroup) error {
	// 删除 FLV 文件
	if err := os.Remove(group.FLVPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete FLV file: %w", err)
	}

	// 删除 XML 文件
	if group.XMLPath != "" {
		if err := os.Remove(group.XMLPath); err != nil && !os.IsNotExist(err) {
			fmt.Printf("Warning: failed to delete XML file: %v\n", err)
		}
	}

	// 删除封面文件
	if group.CoverPath != "" {
		if err := os.Remove(group.CoverPath); err != nil && !os.IsNotExist(err) {
			fmt.Printf("Warning: failed to delete cover file: %v\n", err)
		}
	}

	return nil
}

// PathTemplateData 路径模板数据结构
type PathTemplateData struct {
	OutputDir   string // 输出根目录
	Streamer    string // 主播名称（如 "筱田柴"）
	StreamerUID string // 主播UID（如 "1776261556"）
	StreamerDir string // 完整主播目录名（如 "1776261556-筱田柴"）
	Year        string // 年份 YYYY
	Month       string // 月份 MM
	Day         string // 日期 DD
}

// buildOutputPath 构建输出路径
// 使用配置的 PathTemplate 模板解析输出目录
// 支持的模板变量: {{.OutputDir}}, {{.Streamer}}, {{.StreamerUID}}, {{.StreamerDir}}, {{.Year}}, {{.Month}}, {{.Day}}
func (p *DefaultProcessor) buildOutputPath(group scanner.FileGroup) (string, error) {
	// 从文件名解析日期
	baseName := filepath.Base(group.FLVPath)
	dateStr := p.parseDateFromFilename(baseName)
	if dateStr == "" {
		return "", fmt.Errorf("failed to parse date from filename: %s", baseName)
	}

	// 解析年月日
	if len(dateStr) != 8 {
		return "", fmt.Errorf("invalid date format: %s (expected YYYYMMDD)", dateStr)
	}
	year := dateStr[0:4]
	month := dateStr[4:6]
	day := dateStr[6:8]

	// 准备模板数据
	data := PathTemplateData{
		OutputDir:   p.config.OutputRoot,
		Streamer:    group.StreamerName,
		StreamerUID: group.StreamerUID,
		StreamerDir: group.StreamerDir,
		Year:        year,
		Month:       month,
		Day:         day,
	}

	// 获取路径模板，如果未配置则使用默认模板
	pathTemplate := p.config.PathTemplate
	if pathTemplate == "" {
		pathTemplate = `{{.OutputDir}}\{{.Streamer}}\{{.Year}}\{{.Month}}\{{.Day}}`
	}

	// 解析模板
	tmpl, err := template.New("path").Parse(pathTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse path template: %w", err)
	}

	// 执行模板
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute path template: %w", err)
	}

	outputDir := buf.String()

	// 构建输出文件名（替换扩展名为 .mkv）
	outputFileName := strings.TrimSuffix(baseName, filepath.Ext(baseName)) + ".mkv"
	outputPath := filepath.Join(outputDir, outputFileName)

	return outputPath, nil
}

// parseDateFromFilename 从文件名解析日期
// 例如：录制-筱田柴-20251219-203045-154512.flv -> 20251219
func (p *DefaultProcessor) parseDateFromFilename(filename string) string {
	matches := p.dateRegex.FindStringSubmatch(filename)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

// handleConflict 处理文件名冲突
// 返回最终的输出路径，如果返回空字符串表示跳过
func (p *DefaultProcessor) handleConflict(outputPath string) (string, error) {
	// 检查文件是否存在
	_, err := os.Stat(outputPath)
	if os.IsNotExist(err) {
		// 文件不存在，无冲突
		return outputPath, nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to check file existence: %w", err)
	}

	// 文件存在，根据配置处理
	switch p.config.ConflictMode {
	case ConflictOverwrite:
		// 覆盖模式，删除旧文件
		if err := os.Remove(outputPath); err != nil {
			return "", fmt.Errorf("failed to remove existing file: %w", err)
		}
		return outputPath, nil

	case ConflictSkip:
		// 跳过模式
		fmt.Printf("File already exists, skipping: %s\n", outputPath)
		return "", nil

	case ConflictRename:
		// 重命名模式，添加序号
		return p.findAvailableName(outputPath), nil

	default:
		return "", fmt.Errorf("unknown conflict mode: %s", p.config.ConflictMode)
	}
}

// findAvailableName 查找可用的文件名（添加序号）
// 例如：video.mkv -> video_1.mkv -> video_2.mkv
func (p *DefaultProcessor) findAvailableName(originalPath string) string {
	dir := filepath.Dir(originalPath)
	base := filepath.Base(originalPath)
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)

	for i := 1; i < 1000; i++ {
		newName := fmt.Sprintf("%s_%d%s", nameWithoutExt, i, ext)
		newPath := filepath.Join(dir, newName)
		if _, err := os.Stat(newPath); os.IsNotExist(err) {
			return newPath
		}
	}

	// 如果找不到可用名称（极端情况），使用时间戳
	timestamp := fmt.Sprintf("%d", os.Getpid())
	newName := fmt.Sprintf("%s_%s%s", nameWithoutExt, timestamp, ext)
	return filepath.Join(dir, newName)
}

// moveFile 移动文件（支持跨驱动器）
func (p *DefaultProcessor) moveFile(src, dst string) error {
	// 先尝试直接重命名（同驱动器）
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}

	// 重命名失败，可能跨驱动器，使用复制+删除
	if err := p.copyFile(src, dst); err != nil {
		return err
	}

	// 删除源文件
	return os.Remove(src)
}

// copyFile 复制文件
func (p *DefaultProcessor) copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	// 使用 io.Copy 进行流式复制，不占用过多内存
	_, err = destFile.ReadFrom(sourceFile)
	return err
}

// cleanupIncompleteOutput 清理不完整的输出文件
// 当处理失败时，删除可能存在的不完整输出文件
func (p *DefaultProcessor) cleanupIncompleteOutput(outputPath string) {
	if outputPath == "" {
		return
	}

	// 检查输出文件是否存在
	if _, err := os.Stat(outputPath); err == nil {
		log.Printf("清理不完整的输出文件: %s", outputPath)
		if err := os.Remove(outputPath); err != nil {
			log.Printf("清理输出文件失败: %v", err)
		} else {
			log.Printf("已删除不完整的输出文件: %s", outputPath)
		}
	}

	// 也清理可能存在的 XML 文件
	xmlPath := strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".xml"
	if _, err := os.Stat(xmlPath); err == nil {
		log.Printf("清理不完整的 XML 文件: %s", xmlPath)
		if err := os.Remove(xmlPath); err != nil {
			log.Printf("清理 XML 文件失败: %v", err)
		}
	}
}

// handleFailedFile 处理失败的文件
// 根据配置决定是保持原位还是进入丢弃流程
func (p *DefaultProcessor) handleFailedFile(group scanner.FileGroup) {
	if !p.config.DiscardFailedFiles {
		log.Printf("处理失败，保持原位: %s", group.FLVPath)
		return
	}

	log.Printf("处理失败，执行丢弃逻辑: %s", group.FLVPath)
	if err := p.discardGroup(group, "processing failed"); err != nil {
		log.Printf("丢弃失败文件时出错: %v", err)
	}
}

// ================== 任务状态查询方法 ==================

// GetActiveTasksCount 返回正在处理中的任务数量
func (p *DefaultProcessor) GetActiveTasksCount() int {
	p.activeTasksMu.RLock()
	defer p.activeTasksMu.RUnlock()
	return p.activeTasks
}

// GetPendingTasksCount 返回待处理的任务数量（队列中等待的任务）
func (p *DefaultProcessor) GetPendingTasksCount() int {
	return len(p.pathQueue)
}

// GetTasksByStatus 返回按状态分类的任务数量
func (p *DefaultProcessor) GetTasksByStatus() map[TaskStatus]int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	counts := make(map[TaskStatus]int)
	for _, task := range p.tasks {
		counts[task.Status]++
	}
	return counts
}

// IsStopped 返回处理器是否已停止
func (p *DefaultProcessor) IsStopped() bool {
	p.activeTasksMu.RLock()
	defer p.activeTasksMu.RUnlock()
	return p.stopped
}

// ================== 优雅关闭方法 ==================

// StopGracefully 优雅停止处理引擎
// 立即停止接收新任务，但允许正在进行的任务完成
func (p *DefaultProcessor) StopGracefully() {
	log.Println("开始优雅停止处理引擎...")

	p.activeTasksMu.Lock()
	p.stopped = true
	p.activeTasksMu.Unlock()

	// 取消上下文（通知定时扫描等后台任务退出）
	if p.cancel != nil {
		p.cancel()
	}

	// 关闭 done 通道
	select {
	case <-p.done:
		// 已关闭
	default:
		close(p.done)
	}
}

// WaitForCompletion 等待所有正在进行的任务完成
// 返回 true 表示所有任务已完成，false 表示超时
func (p *DefaultProcessor) WaitForCompletion(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for {
		p.activeTasksMu.RLock()
		active := p.activeTasks
		p.activeTasksMu.RUnlock()

		if active == 0 {
			log.Println("所有任务已完成")
			return true
		}

		if time.Now().After(deadline) {
			log.Printf("等待任务完成超时，仍有 %d 个任务在进行中", active)
			return false
		}

		// 等待任务完成信号或超时
		select {
		case <-p.taskDone:
			// 有任务完成，继续检查
		case <-time.After(100 * time.Millisecond):
			// 定期检查
		}
	}
}

// StopNow 立即停止所有任务（强制终止）
// 这会取消所有正在进行的 FFmpeg 进程
func (p *DefaultProcessor) StopNow() error {
	log.Println("立即停止处理引擎（强制终止）...")

	p.activeTasksMu.Lock()
	p.stopped = true
	p.activeTasksMu.Unlock()

	// 取消上下文，这会导致所有使用该上下文的操作被取消
	if p.cancel != nil {
		p.cancel()
	}

	// 关闭 done 通道
	select {
	case <-p.done:
		// 已关闭
	default:
		close(p.done)
	}

	// 关闭队列通道
	select {
	case _, ok := <-p.pathQueue:
		if ok {
			// 尝试清空队列
			for range p.pathQueue {
			}
		}
	default:
		close(p.pathQueue)
	}

	// 等待 workers 退出，带超时
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("所有 workers 已强制退出")
	case <-time.After(3 * time.Second):
		log.Println("警告: 等待 workers 强制退出超时")
	}

	return nil
}

// UpdateConfig 动态更新处理器配置
// 注意：某些配置（如 MaxConcurrent）需要重启才能完全生效
// 此方法会更新可以热更新的配置项
func (p *DefaultProcessor) UpdateConfig(cfg Config) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 更新可以热更新的配置项
	p.config.InputDir = cfg.InputDir
	p.config.OutputRoot = cfg.OutputRoot
	p.config.DiscardDir = cfg.DiscardDir
	p.config.PathTemplate = cfg.PathTemplate
	p.config.CheckVideoStream = cfg.CheckVideoStream
	p.config.MinFileSizeKB = cfg.MinFileSizeKB
	p.config.DiscardFailedFiles = cfg.DiscardFailedFiles
	p.config.ConflictMode = cfg.ConflictMode
	p.config.DefaultCoverPath = cfg.DefaultCoverPath
	p.config.DeleteOriginal = cfg.DeleteOriginal

	// 更新扫描间隔（会在下一次定时扫描时生效）
	if cfg.ScanInterval > 0 {
		p.config.ScanInterval = cfg.ScanInterval
	}

	// 注意：MaxConcurrent 的变更需要重启才能生效，因为 worker 数量在启动时固定
	// 这里仅记录日志提醒用户
	if cfg.MaxConcurrent != p.config.MaxConcurrent {
		log.Printf("配置热更新：MaxConcurrent 从 %d 变更为 %d，需要重启程序才能生效",
			p.config.MaxConcurrent, cfg.MaxConcurrent)
	}

	log.Println("处理器配置已热更新")
}

// GetConfig 获取当前处理器配置（只读副本）
func (p *DefaultProcessor) GetConfig() Config {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config
}

// 错误定义
var (
	ErrQueueFull    = &ProcessorError{Message: "任务队列已满"}
	ErrTaskNotFound = &ProcessorError{Message: "任务不存在"}
	ErrDiscarded    = &ProcessorError{Message: "文件已丢弃"}
)

// ProcessorError 处理引擎错误
type ProcessorError struct {
	Message string
}

func (e *ProcessorError) Error() string {
	return e.Message
}

// ================== 转封装暂停功能 ==================

// RemuxPauseStatus 转封装暂停状态
type RemuxPauseStatus struct {
	Paused            bool   `json:"paused"`            // 是否立即暂停
	PauseAfterCurrent bool   `json:"pauseAfterCurrent"` // 是否当前任务后暂停
	PausedTimeStr     string `json:"pausedTimeStr"`     // 暂停时长字符串
}

// PauseRemux 立即暂停当前转封装进程
func (p *DefaultProcessor) PauseRemux() error {
	p.pauseMu.Lock()
	defer p.pauseMu.Unlock()

	if p.remuxPaused {
		return nil // 已经暂停
	}

	p.remuxPaused = true
	p.remuxPausedTime = time.Now()

	// 暂停当前正在运行的 FFmpeg 进程
	if p.currentRemuxCmd != nil && p.currentRemuxCmd.Process != nil {
		if err := ffmpeg.SuspendProcess(p.currentRemuxCmd); err != nil {
			log.Printf("[processor] 暂停 FFmpeg 进程失败: %v", err)
			return fmt.Errorf("暂停进程失败: %w", err)
		}
		log.Println("[processor] 转封装已暂停")
	}

	return nil
}

// ResumeRemux 恢复转封装进程
func (p *DefaultProcessor) ResumeRemux() error {
	p.pauseMu.Lock()
	defer p.pauseMu.Unlock()

	if !p.remuxPaused {
		return nil // 未暂停
	}

	// 恢复当前正在运行的 FFmpeg 进程
	if p.currentRemuxCmd != nil && p.currentRemuxCmd.Process != nil {
		if err := ffmpeg.ResumeProcess(p.currentRemuxCmd); err != nil {
			log.Printf("[processor] 恢复 FFmpeg 进程失败: %v", err)
			return fmt.Errorf("恢复进程失败: %w", err)
		}
	}

	// 累加暂停时长
	p.remuxTotalPausedTime += time.Since(p.remuxPausedTime)
	p.remuxPaused = false
	p.remuxPausedTime = time.Time{}

	log.Println("[processor] 转封装已恢复")
	return nil
}

// PauseRemuxAfterCurrent 设置当前任务后暂停标志
func (p *DefaultProcessor) PauseRemuxAfterCurrent() {
	p.pauseMu.Lock()
	defer p.pauseMu.Unlock()

	p.remuxPauseAfterCurrent = true
	log.Println("[processor] 已设置：当前任务完成后暂停转封装")
}

// CancelPauseRemuxAfterCurrent 取消当前任务后暂停
func (p *DefaultProcessor) CancelPauseRemuxAfterCurrent() {
	p.pauseMu.Lock()
	defer p.pauseMu.Unlock()

	p.remuxPauseAfterCurrent = false
	log.Println("[processor] 已取消：当前任务后暂停转封装")
}

// GetRemuxPauseStatus 获取转封装暂停状态
func (p *DefaultProcessor) GetRemuxPauseStatus() RemuxPauseStatus {
	p.pauseMu.RLock()
	defer p.pauseMu.RUnlock()

	status := RemuxPauseStatus{
		Paused:            p.remuxPaused,
		PauseAfterCurrent: p.remuxPauseAfterCurrent,
	}

	if p.remuxPaused && !p.remuxPausedTime.IsZero() {
		duration := time.Since(p.remuxPausedTime)
		status.PausedTimeStr = formatPauseDuration(duration)
	}

	return status
}

// IsRemuxPaused 检查转封装是否暂停（包括立即暂停和等待当前任务后暂停）
func (p *DefaultProcessor) IsRemuxPaused() bool {
	p.pauseMu.RLock()
	defer p.pauseMu.RUnlock()
	return p.remuxPaused
}

// ShouldPauseAfterCurrentRemux 检查是否需要在当前任务后暂停
func (p *DefaultProcessor) ShouldPauseAfterCurrentRemux() bool {
	p.pauseMu.RLock()
	defer p.pauseMu.RUnlock()
	return p.remuxPauseAfterCurrent
}

// SetCurrentRemuxCmd 设置当前转封装命令引用（供 FFmpeg 包调用）
func (p *DefaultProcessor) SetCurrentRemuxCmd(cmd *exec.Cmd) {
	p.pauseMu.Lock()
	defer p.pauseMu.Unlock()
	p.currentRemuxCmd = cmd
}

// ClearCurrentRemuxCmd 清除当��转封装命令引用
func (p *DefaultProcessor) ClearCurrentRemuxCmd() {
	p.pauseMu.Lock()
	defer p.pauseMu.Unlock()
	p.currentRemuxCmd = nil
}

// GetRemuxTotalPausedTime 获取累计暂停时长
func (p *DefaultProcessor) GetRemuxTotalPausedTime() time.Duration {
	p.pauseMu.RLock()
	defer p.pauseMu.RUnlock()
	return p.remuxTotalPausedTime
}

// ResetRemuxPausedTime 重置暂停时长（任务开始时调用）
func (p *DefaultProcessor) ResetRemuxPausedTime() {
	p.pauseMu.Lock()
	defer p.pauseMu.Unlock()
	p.remuxTotalPausedTime = 0
}

// formatPauseDuration 格式化暂停时长
func formatPauseDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d分%d秒", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d小时%d分", int(d.Hours()), int(d.Minutes())%60)
}

// ================== 转封装进度监控 ==================

// GetRemuxProgress 获取当前转封装进度
func (p *DefaultProcessor) GetRemuxProgress() RemuxProgress {
	p.remuxProgressMu.RLock()
	defer p.remuxProgressMu.RUnlock()

	progress := p.remuxProgress

	// 检查暂停状态
	p.pauseMu.RLock()
	progress.IsPaused = p.remuxPaused
	p.pauseMu.RUnlock()

	// 如果正在进行，更新已用时间
	if progress.IsActive && !progress.StartTime.IsZero() {
		totalPausedTime := p.remuxProgress.TotalPausedTime
		if progress.IsPaused && !p.remuxPausedTime.IsZero() {
			totalPausedTime += time.Since(p.remuxPausedTime)
		}
		progress.ElapsedSeconds = time.Since(progress.StartTime).Seconds() - totalPausedTime.Seconds()
		if progress.ElapsedSeconds < 0 {
			progress.ElapsedSeconds = 0
		}
	}

	return progress
}

// startRemuxProgressMonitor 启动转封装进度监控
// 在转封装开始时调用，传入源文件大小和输出文件路径
func (p *DefaultProcessor) startRemuxProgressMonitor(sourceSize int64, outputPath string, inputPath string) {
	p.remuxProgressMu.Lock()
	// 初始化进度信息
	p.remuxProgress = RemuxProgress{
		SourceSize:         sourceSize,
		WrittenSize:        0,
		SpeedBytesPerSec:   0,
		Progress:           0,
		EstimatedRemaining: 0,
		ElapsedSeconds:     0,
		IsActive:           true,
		IsPaused:           false,
		CurrentFile:        filepath.Base(inputPath),
		StartTime:          time.Now(),
		TotalPausedTime:    0,
	}
	p.remuxSpeedSamples = make([]float64, 0, speedWindowSize)
	p.remuxLastSize = 0
	p.remuxLastCheck = time.Now()
	p.remuxStopMonitor = make(chan struct{})
	p.remuxProgressMu.Unlock()

	// 启动监控协程
	go p.monitorRemuxProgress(outputPath)
}

// stopRemuxProgressMonitor 停止转封装进度监控
func (p *DefaultProcessor) stopRemuxProgressMonitor() {
	p.remuxProgressMu.Lock()
	defer p.remuxProgressMu.Unlock()

	// 发送停止信号
	if p.remuxStopMonitor != nil {
		close(p.remuxStopMonitor)
		p.remuxStopMonitor = nil
	}

	// 标记为非活动状态
	p.remuxProgress.IsActive = false
	p.remuxProgress.Progress = 100
}

// monitorRemuxProgress 监控转封装进度的协程
func (p *DefaultProcessor) monitorRemuxProgress(outputPath string) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.remuxStopMonitor:
			return
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.updateRemuxProgress(outputPath)
		}
	}
}

// updateRemuxProgress 更新转封装进度
func (p *DefaultProcessor) updateRemuxProgress(outputPath string) {
	// 检查暂停状态
	p.pauseMu.RLock()
	isPaused := p.remuxPaused
	p.pauseMu.RUnlock()

	// 获取输出文件大小
	fileInfo, err := os.Stat(outputPath)
	if err != nil {
		// 文件可能还不存在
		return
	}

	currentSize := fileInfo.Size()
	now := time.Now()

	p.remuxProgressMu.Lock()
	defer p.remuxProgressMu.Unlock()

	// 如果��于暂停状态，更新暂停时间但不更新速度
	if isPaused {
		p.remuxProgress.IsPaused = true
		return
	}

	// 计算时间间隔
	elapsed := now.Sub(p.remuxLastCheck).Seconds()
	if elapsed <= 0 {
		return
	}

	// 计算当前速度
	sizeChange := currentSize - p.remuxLastSize
	if sizeChange < 0 {
		sizeChange = 0
	}
	currentSpeed := float64(sizeChange) / elapsed

	// 更新滑动窗口
	if len(p.remuxSpeedSamples) >= speedWindowSize {
		p.remuxSpeedSamples = p.remuxSpeedSamples[1:]
	}
	p.remuxSpeedSamples = append(p.remuxSpeedSamples, currentSpeed)

	// 计算平均速度
	var avgSpeed float64
	if len(p.remuxSpeedSamples) > 0 {
		var sum float64
		for _, s := range p.remuxSpeedSamples {
			sum += s
		}
		avgSpeed = sum / float64(len(p.remuxSpeedSamples))
	}

	// 更新进度信息
	p.remuxProgress.WrittenSize = currentSize
	p.remuxProgress.SpeedBytesPerSec = avgSpeed
	p.remuxProgress.IsPaused = false

	// 计算进度百分比
	if p.remuxProgress.SourceSize > 0 {
		p.remuxProgress.Progress = float64(currentSize) / float64(p.remuxProgress.SourceSize) * 100
		if p.remuxProgress.Progress > 100 {
			p.remuxProgress.Progress = 100
		}
	}

	// 计算剩余时间
	remaining := p.remuxProgress.SourceSize - currentSize
	if remaining > 0 && avgSpeed > 0 {
		p.remuxProgress.EstimatedRemaining = float64(remaining) / avgSpeed
	} else {
		p.remuxProgress.EstimatedRemaining = 0
	}

	// 更新已用时间
	totalElapsed := time.Since(p.remuxProgress.StartTime)
	p.remuxProgress.ElapsedSeconds = totalElapsed.Seconds() - p.remuxProgress.TotalPausedTime.Seconds()
	if p.remuxProgress.ElapsedSeconds < 0 {
		p.remuxProgress.ElapsedSeconds = 0
	}

	// 更新上次检查信息
	p.remuxLastSize = currentSize
	p.remuxLastCheck = now
}

// FormatRemuxProgressInfo 格式化转封装进度信息（用于前端显示）
type RemuxProgressInfo struct {
	SourceSizeStr  string  `json:"sourceSizeStr"`  // 源文件大小字符串
	WrittenSizeStr string  `json:"writtenSizeStr"` // 已写入大小字符串
	SpeedStr       string  `json:"speedStr"`       // 速度字符串
	ElapsedStr     string  `json:"elapsedStr"`     // 已用时间字符串
	Progress       float64 `json:"progress"`       // 进度百分比
	IsActive       bool    `json:"isActive"`       // 是否活动
	IsPaused       bool    `json:"isPaused"`       // 是否暂停
	CurrentFile    string  `json:"currentFile"`    // 当前文件名
}

// GetRemuxProgressInfo 获取格式化的转封装进度信息
func (p *DefaultProcessor) GetRemuxProgressInfo() RemuxProgressInfo {
	progress := p.GetRemuxProgress()

	info := RemuxProgressInfo{
		SourceSizeStr:  formatBytes(progress.SourceSize),
		WrittenSizeStr: formatBytes(progress.WrittenSize),
		SpeedStr:       formatSpeed(progress.SpeedBytesPerSec),
		Progress:       progress.Progress,
		IsActive:       progress.IsActive,
		IsPaused:       progress.IsPaused,
		CurrentFile:    progress.CurrentFile,
		ElapsedStr:     formatDurationSeconds(progress.ElapsedSeconds),
	}

	return info
}

// formatBytes 格式化字节为人类可读字符串
func formatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// formatSpeed 格式化速度为人类可读字符串
func formatSpeed(bytesPerSec float64) string {
	const (
		KB = 1024.0
		MB = KB * 1024.0
	)

	switch {
	case bytesPerSec >= MB:
		return fmt.Sprintf("%.1f MB/s", bytesPerSec/MB)
	case bytesPerSec >= KB:
		return fmt.Sprintf("%.1f KB/s", bytesPerSec/KB)
	default:
		return fmt.Sprintf("%.0f B/s", bytesPerSec)
	}
}

// formatDurationSeconds 格式化秒数为人类可读时间字符串
func formatDurationSeconds(seconds float64) string {
	if seconds <= 0 {
		return "--"
	}
	if seconds < 60 {
		return fmt.Sprintf("%.0f秒", seconds)
	}
	if seconds < 3600 {
		minutes := int(seconds) / 60
		secs := int(seconds) % 60
		return fmt.Sprintf("%d分%d秒", minutes, secs)
	}
	hours := int(seconds) / 3600
	minutes := (int(seconds) % 3600) / 60
	return fmt.Sprintf("%d小时%d分", hours, minutes)
}
