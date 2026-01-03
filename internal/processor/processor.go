// Package processor 提供文件处理引擎功能
package processor

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/user/bililive-recorder-autoarchive/internal/ffmpeg"
	"github.com/user/bililive-recorder-autoarchive/internal/scanner"
	"github.com/user/bililive-recorder-autoarchive/internal/storage"
)

// TaskStatus 任务状态
type TaskStatus string

const (
	StatusPending    TaskStatus = "pending"    // 等待处理
	StatusProcessing TaskStatus = "processing" // 处理中
	StatusCompleted  TaskStatus = "completed"  // 已完成
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
	MaxConcurrent    int                     // 最大并发数
	InputDir         string                  // 输入目录（录播姬工作目录）
	OutputRoot       string                  // 输出根目录
	DiscardDir       string                  // 丢弃目录
	PathTemplate     string                  // 路径模板
	CheckVideoStream bool                    // 是否检查视频流
	MinFileSizeKB    int64                   // 最小文件大小（KB）
	DateRegex        string                  // 日期解析正则
	ConflictMode     ConflictMode            // 文件冲突处理模式
	FFmpeg           ffmpeg.FFmpeg           // FFmpeg 实例
	Storage          storage.Storage         // 持久化存储实例
	DefaultCoverPath string                  // 全局默认封面路径
	DeleteOriginal   bool                    // 处理成功后是否删除原文件
	ScanInterval     time.Duration           // 扫描间隔
	Scanner          *scanner.DefaultScanner // Scanner 实例
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
}

// New 创建新的处理引擎实例
func New(cfg Config) (*DefaultProcessor, error) {
	// 设置默认值
	if cfg.ConflictMode == "" {
		cfg.ConflictMode = ConflictSkip
	}
	if cfg.DateRegex == "" {
		// 匹配格式: 录制-房间号-YYYYMMDD-HHMMSS，日期在第二个连字符后
		cfg.DateRegex = `-\d+-(\d{8})-`
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2 // 默认 2 个并发
	}
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = 5 * time.Minute // 默认 5 分钟扫描一次
	}

	// 编译日期正则表达式
	dateRegex, err := regexp.Compile(cfg.DateRegex)
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

	// 检查是否已在处理中（去重）
	if _, loaded := p.pendingPaths.LoadOrStore(absPath, time.Now()); loaded {
		log.Printf("跳过重复任务: %s", absPath)
		return nil
	}

	// 加入队列
	select {
	case p.pathQueue <- absPath:
		log.Printf("任务已加入队列: %s", absPath)
		return nil
	default:
		// 队列满了，从去重 map 中移除
		p.pendingPaths.Delete(absPath)
		return ErrQueueFull
	}
}

// worker 工作协程
// 从队列中获取任务并处理
func (p *DefaultProcessor) worker(id int) {
	defer p.wg.Done()
	log.Printf("Worker %d 启动", id)

	for {
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
	// 处理完成后从去重 map 中移除
	defer p.pendingPaths.Delete(path)

	startTime := time.Now()
	log.Printf("开始处理文件: %s", path)

	// 创建任务对象并加入任务列表
	taskID := generateTaskID(path)
	task := &Task{
		ID:        taskID,
		InputPath: path,
		Status:    StatusProcessing,
		Progress:  0,
		CreatedAt: startTime,
		UpdatedAt: startTime,
	}

	p.mu.Lock()
	p.tasks[taskID] = task
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
		updateTask(StatusCompleted, "", nil, 100)
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
	if err := p.ProcessGroup(*group); err != nil {
		log.Printf("处理文件失败: %s, 错误: %v", path, err)
		updateTask(StatusFailed, "", err, 0)
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
	outputPath, _ := p.buildOutputPath(*group)
	updateTask(StatusCompleted, outputPath, nil, 100)

	if p.storage != nil {
		p.storage.LogProcessResult(storage.ProcessLog{
			InputPath:  path,
			OutputPath: outputPath,
			Status:     "completed",
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

	// 解析主播名称（从父目录名称中提取）
	streamerDir := filepath.Base(dir)
	streamerName := parseStreamerNameFromDir(streamerDir)

	group := &scanner.FileGroup{
		FLVPath:      flvPath,
		StreamerDir:  streamerDir,
		StreamerName: streamerName,
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
func (p *DefaultProcessor) scheduledScan() {
	ticker := time.NewTicker(p.config.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.done:
			return
		case <-p.ctx.Done():
			return
		case <-ticker.C:
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
	isInvalid := false
	var invalidReason string

	// 3.1 检查文件大小
	fileSizeKB := fileInfo.Size() / 1024
	if p.config.MinFileSizeKB > 0 && fileSizeKB < p.config.MinFileSizeKB {
		// 文件小于最小大小，需要进一步检查是否有视频流
		if p.config.CheckVideoStream && p.config.FFmpeg != nil {
			hasVideo, err := p.config.FFmpeg.HasVideoStream(ctx, group.FLVPath)
			if err != nil {
				// 无法检测视频流，可能文件损坏
				isInvalid = true
				invalidReason = fmt.Sprintf("failed to check video stream: %v", err)
			} else if !hasVideo {
				// 确认无视频流
				isInvalid = true
				invalidReason = fmt.Sprintf("file size %d KB < %d KB and no video stream", fileSizeKB, p.config.MinFileSizeKB)
			}
		}
	}

	// 步骤 4: 如果是无效文件，执行丢弃逻辑
	if isInvalid {
		return p.discardGroup(group, invalidReason)
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

	// 步骤 8: 执行转封装
	if p.config.FFmpeg != nil {
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

// buildOutputPath 构建输出路径
// 格式：[根输出目录] \ [主播名] \ [YYYY] \ [MM] \ [DD] \ 文件名.mkv
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

	// 构建输出目录
	outputDir := filepath.Join(p.config.OutputRoot, group.StreamerName, year, month, day)

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

// 错误定义
var (
	ErrQueueFull    = &ProcessorError{Message: "任务队列已满"}
	ErrTaskNotFound = &ProcessorError{Message: "任务不存在"}
)

// ProcessorError 处理引擎错误
type ProcessorError struct {
	Message string
}

func (e *ProcessorError) Error() string {
	return e.Message
}
