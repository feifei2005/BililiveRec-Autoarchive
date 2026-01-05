// Package transcoder 提供视频转码功能
package transcoder

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Transcoder 转码器
type Transcoder struct {
	ffmpegPath  string
	ffprobePath string

	tasks      map[string]*TranscodeTask
	taskQueue  chan *TranscodeTask
	mu         sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	maxWorkers int

	// 当前正在运行的 FFmpeg 进程，用于取消
	currentCmd   *exec.Cmd
	currentCmdMu sync.Mutex
}

// Config 转码器配置
type Config struct {
	FFmpegPath  string // ffmpeg 可执行文件路径
	FFprobePath string // ffprobe 可执行文件路径
	MaxWorkers  int    // 最大并发数
}

// New 创建新的转码器实例
func New(cfg Config) *Transcoder {
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if cfg.FFprobePath == "" {
		cfg.FFprobePath = "ffprobe"
	}
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 1
	}

	return &Transcoder{
		ffmpegPath:  cfg.FFmpegPath,
		ffprobePath: cfg.FFprobePath,
		tasks:       make(map[string]*TranscodeTask),
		taskQueue:   make(chan *TranscodeTask, 100),
		maxWorkers:  cfg.MaxWorkers,
	}
}

// Start 启动转码器
func (t *Transcoder) Start(ctx context.Context) error {
	t.ctx, t.cancel = context.WithCancel(ctx)

	log.Printf("[transcoder] 启动转码器，并发数: %d", t.maxWorkers)

	for i := 0; i < t.maxWorkers; i++ {
		t.wg.Add(1)
		go t.worker(i)
	}

	return nil
}

// Stop 停止转码器
func (t *Transcoder) Stop() error {
	log.Println("[transcoder] 正在停止转码器...")

	if t.cancel != nil {
		t.cancel()
	}

	// 取消当前正在运行的 FFmpeg 进程
	t.currentCmdMu.Lock()
	if t.currentCmd != nil && t.currentCmd.Process != nil {
		t.currentCmd.Process.Kill()
	}
	t.currentCmdMu.Unlock()

	close(t.taskQueue)

	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("[transcoder] 转码器已停止")
	case <-time.After(5 * time.Second):
		log.Println("[transcoder] 警告: 等待停止超时")
	}

	return nil
}

// worker 工作协程
func (t *Transcoder) worker(id int) {
	defer t.wg.Done()
	log.Printf("[transcoder] Worker %d 启动", id)

	for {
		select {
		case <-t.ctx.Done():
			log.Printf("[transcoder] Worker %d 停止", id)
			return
		case task, ok := <-t.taskQueue:
			if !ok {
				log.Printf("[transcoder] Worker %d: 队列已关闭", id)
				return
			}
			t.processTask(task)
		}
	}
}

// generateTaskID 生成任务ID
func generateTaskID(path string) string {
	hash := md5.Sum([]byte(path + time.Now().String()))
	return hex.EncodeToString(hash[:8])
}

// ScanFolder 扫描文件夹中的视频文件（并发扫描优化）
func (t *Transcoder) ScanFolder(folderPath string) ([]VideoFile, error) {
	videoExtensions := map[string]bool{
		".flv":  true,
		".mp4":  true,
		".mkv":  true,
		".avi":  true,
		".mov":  true,
		".wmv":  true,
		".webm": true,
	}

	// 第一步：收集所有视频文件路径
	type videoPathInfo struct {
		path string
		info os.FileInfo
	}
	var videoPaths []videoPathInfo

	err := filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if videoExtensions[ext] {
			videoPaths = append(videoPaths, videoPathInfo{path: path, info: info})
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("扫描文件夹失败: %w", err)
	}

	log.Printf("[transcoder] 发现 %d 个视频文件，开始并发探测...", len(videoPaths))

	// 第二步：并发探测视频信息
	results := make([]VideoFile, len(videoPaths))
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 4) // 限制并发数为 4

	for i, vp := range videoPaths {
		wg.Add(1)
		go func(idx int, videoPath videoPathInfo) {
			defer wg.Done()
			semaphore <- struct{}{}        // 获取信号量
			defer func() { <-semaphore }() // 释放信号量

			videoFile, err := t.probeVideo(videoPath.path)
			if err != nil {
				log.Printf("[transcoder] 无法探测视频 %s: %v", videoPath.path, err)
				// 即使探测失败，也添加基本信息
				results[idx] = VideoFile{
					Path: videoPath.path,
					Name: videoPath.info.Name(),
					Size: videoPath.info.Size(),
				}
				return
			}

			videoFile.Path = videoPath.path
			videoFile.Name = videoPath.info.Name()
			videoFile.Size = videoPath.info.Size()
			results[idx] = *videoFile
		}(i, vp)
	}

	wg.Wait()

	log.Printf("[transcoder] 扫描完成，发现 %d 个视频文件", len(results))
	return results, nil
}

// ffprobeOutput ffprobe JSON 输出结构
type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

type ffprobeStream struct {
	Index       int    `json:"index"`
	CodecType   string `json:"codec_type"`
	CodecName   string `json:"codec_name"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	SampleRate  string `json:"sample_rate,omitempty"`
	BitRate     string `json:"bit_rate,omitempty"`
	Disposition struct {
		AttachedPic int `json:"attached_pic"`
	} `json:"disposition"`
}

type ffprobeFormat struct {
	Duration string `json:"duration"`
	Size     string `json:"size"`
	BitRate  string `json:"bit_rate"`
}

// hideWindow 在 Windows 上隐藏命令行窗口
func hideWindow(cmd *exec.Cmd) {
	if runtime.GOOS == "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		}
	}
}

// formatDuration 格式化时长为 HH:MM:SS
func formatDuration(seconds float64) string {
	if seconds <= 0 {
		return "--"
	}
	totalSec := int(seconds)
	hours := totalSec / 3600
	minutes := (totalSec % 3600) / 60
	secs := totalSec % 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%02d:%02d", minutes, secs)
}

// probeVideo 使用 ffprobe 获取视频信息
func (t *Transcoder) probeVideo(path string) (*VideoFile, error) {
	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, t.ffprobePath, args...)
	// 在 Windows 上隐藏窗口
	hideWindow(cmd)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe 执行失败: %w", err)
	}

	var probe ffprobeOutput
	if err := json.Unmarshal(output, &probe); err != nil {
		return nil, fmt.Errorf("解析 ffprobe 输出失败: %w", err)
	}

	video := &VideoFile{
		VideoIndex: -1,
		AudioIndex: -1,
		CoverIndex: -1,
	}

	// 解析时长
	if probe.Format.Duration != "" {
		video.Duration, _ = strconv.ParseFloat(probe.Format.Duration, 64)
		video.DurationStr = formatDuration(video.Duration)
	}

	// 解析比特率
	if probe.Format.BitRate != "" {
		video.Bitrate, _ = strconv.ParseInt(probe.Format.BitRate, 10, 64)
	}

	// 解析流信息
	for _, stream := range probe.Streams {
		switch stream.CodecType {
		case "video":
			// 检查是否是封面流（附加图片）
			if stream.Disposition.AttachedPic == 1 ||
				stream.CodecName == "mjpeg" ||
				stream.CodecName == "png" {
				video.HasCover = true
				video.CoverIndex = stream.Index
			} else if video.VideoIndex == -1 {
				// 主视频流
				video.VideoIndex = stream.Index
				video.VideoCodec = stream.CodecName
				video.Width = stream.Width
				video.Height = stream.Height
				// 设置分辨率字符串
				if stream.Width > 0 && stream.Height > 0 {
					video.Resolution = fmt.Sprintf("%dx%d", stream.Width, stream.Height)
				}
			}
		case "audio":
			if video.AudioIndex == -1 {
				video.AudioIndex = stream.Index
				video.AudioCodec = stream.CodecName
			}
		}
	}

	return video, nil
}

// AddTask 添加转码任务
func (t *Transcoder) AddTask(inputPath string, config TranscodeConfig) (*TranscodeTask, error) {
	// 检查输入文件是否存在
	if _, err := os.Stat(inputPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("输入文件不存在: %s", inputPath)
	}

	// 生成输出路径
	outputPath := t.buildOutputPath(inputPath, config)

	// 获取视频信息
	videoInfo, err := t.probeVideo(inputPath)
	if err != nil {
		log.Printf("[transcoder] 无法获取视频信息: %v", err)
	}

	task := &TranscodeTask{
		ID:         generateTaskID(inputPath),
		InputPath:  inputPath,
		OutputPath: outputPath,
		Config:     config,
		Status:     StatusPending,
		Progress:   0,
		CreatedAt:  time.Now(),
	}

	if videoInfo != nil {
		task.Duration = videoInfo.Duration
	}

	t.mu.Lock()
	t.tasks[task.ID] = task
	t.mu.Unlock()

	// 加入队列
	select {
	case t.taskQueue <- task:
		log.Printf("[transcoder] 任务已加入队列: %s", inputPath)
	default:
		return nil, fmt.Errorf("任务队列已满")
	}

	return task, nil
}

// buildOutputPath 构建输出路径
func (t *Transcoder) buildOutputPath(inputPath string, config TranscodeConfig) string {
	dir := filepath.Dir(inputPath)
	baseName := filepath.Base(inputPath)
	nameWithoutExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))

	// 输出目录：如果用户指定了输出目录，使用用户指定的；否则放在原始文件旁边
	outputDir := config.OutputDir
	if outputDir == "" {
		outputDir = dir // 直接放在原始文件所在目录
	}

	// 确定输出扩展名
	ext := config.OutputExt
	if ext == "" {
		ext = t.inferOutputExt(config.CustomArgs)
	}
	if ext == "" {
		ext = ".mp4"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}

	// 输出文件名：原文件名_transcoded.扩展名
	return filepath.Join(outputDir, nameWithoutExt+"_transcoded"+ext)
}

// inferOutputExt 从 FFmpeg 参数推断输出扩展名
func (t *Transcoder) inferOutputExt(args string) string {
	// 查找 -f 参数
	parts := strings.Fields(args)
	for i, part := range parts {
		if part == "-f" && i+1 < len(parts) {
			format := parts[i+1]
			switch format {
			case "mp4":
				return ".mp4"
			case "matroska":
				return ".mkv"
			case "webm":
				return ".webm"
			case "avi":
				return ".avi"
			case "mov":
				return ".mov"
			default:
				return "." + format
			}
		}
	}
	return ""
}

// processTask 处理单个转码任务
func (t *Transcoder) processTask(task *TranscodeTask) {
	log.Printf("[transcoder] 开始处理任务: %s", task.InputPath)

	t.mu.Lock()
	task.Status = StatusProcessing
	task.StartedAt = time.Now()
	t.mu.Unlock()

	// 确保输出目录存在
	outputDir := filepath.Dir(task.OutputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.failTask(task, fmt.Errorf("创建输出目录失败: %w", err))
		return
	}

	// 获取视频信息（包括封面流）
	videoInfo, err := t.probeVideo(task.InputPath)
	if err != nil {
		log.Printf("[transcoder] 无法获取视频信息，继续转码: %v", err)
	}

	// 构建 FFmpeg 命令
	args := t.buildFFmpegArgs(task, videoInfo)

	// 执行转码
	if err := t.executeFFmpeg(task, args); err != nil {
		t.failTask(task, err)
		return
	}

	t.mu.Lock()
	task.Status = StatusSuccess
	task.Progress = 100
	task.CompletedAt = time.Now()
	t.mu.Unlock()

	log.Printf("[transcoder] 任务完成: %s -> %s", task.InputPath, task.OutputPath)

	// 转码成功后删除源文件（如果配置了该选项）
	if task.Config.DeleteSourceOnSuccess {
		if err := os.Remove(task.InputPath); err != nil {
			log.Printf("[transcoder] 警告: 删除源文件失败: %s, 错误: %v", task.InputPath, err)
		} else {
			log.Printf("[transcoder] 已删除源文件: %s", task.InputPath)
		}
	}
}

// failTask 标记任务失败
func (t *Transcoder) failTask(task *TranscodeTask, err error) {
	t.mu.Lock()
	task.Status = StatusFailed
	task.Error = err.Error()
	task.CompletedAt = time.Now()
	t.mu.Unlock()
	log.Printf("[transcoder] 任务失败: %s, 错误: %v", task.InputPath, err)
}

// getLastLines 获取字符串的最后 N 行
func getLastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// buildFFmpegArgs 构建 FFmpeg 参数
func (t *Transcoder) buildFFmpegArgs(task *TranscodeTask, videoInfo *VideoFile) []string {
	args := []string{"-y"} // 覆盖输出文件

	// 添加输入文件
	args = append(args, "-i", task.InputPath)

	// 解析用户自定义参数
	customArgs := strings.Fields(task.Config.CustomArgs)

	// 检查是否有封面流，并且用户没有禁用封面处理
	hasCover := videoInfo != nil && videoInfo.HasCover && videoInfo.CoverIndex >= 0

	if hasCover {
		// 有封面流，需要特殊处理
		// 映射主视频流和音频流
		if videoInfo.VideoIndex >= 0 {
			args = append(args, "-map", fmt.Sprintf("0:%d", videoInfo.VideoIndex))
		} else {
			args = append(args, "-map", "0:v:0")
		}

		if videoInfo.AudioIndex >= 0 {
			args = append(args, "-map", fmt.Sprintf("0:%d", videoInfo.AudioIndex))
		} else {
			args = append(args, "-map", "0:a:0?")
		}

		// 映射封面流
		args = append(args, "-map", fmt.Sprintf("0:%d", videoInfo.CoverIndex))

		// 将用户自定义参数中的 :v 后缀替换为 :v:0，确保只应用于主视频流
		// 这样可以避免视频编码器参数（如 -level:v）被错误地应用到封面流的 copy 编码器
		processedArgs := normalizeVideoStreamSelectors(customArgs)
		args = append(args, processedArgs...)

		// 封面流保持 copy 并标记为 attached_pic
		args = append(args, "-c:v:1", "copy")
		args = append(args, "-disposition:v:1", "attached_pic")
	} else {
		// 没有封面流，使用默认映射
		args = append(args, "-map", "0:v:0")
		args = append(args, "-map", "0:a:0?")
		args = append(args, customArgs...)
	}

	// 添加进度输出
	args = append(args, "-progress", "pipe:1")

	// 添加输出文件
	args = append(args, task.OutputPath)

	return args
}

// normalizeVideoStreamSelectors 将视频流选择器 :v 替换为 :v:0
// 这确保编码器参数只应用于第一个视频流，而不会影响封面流
func normalizeVideoStreamSelectors(args []string) []string {
	result := make([]string, len(args))
	
	// 需要处理的视频相关选项前缀
	videoOptionPrefixes := []string{
		"-c:v",
		"-codec:v",
		"-profile:v",
		"-level:v",
		"-rc:v",
		"-b:v",
		"-maxrate:v",
		"-minrate:v",
		"-bufsize:v",
		"-g:v",
		"-keyint_min:v",
		"-sc_threshold:v",
		"-pix_fmt:v",
		"-preset:v",
		"-tune:v",
		"-crf:v",
		"-qp:v",
		"-cq:v",
	}
	
	for i, arg := range args {
		processed := arg
		
		// 检查是否是需要处理的视频选项
		for _, prefix := range videoOptionPrefixes {
			// 精确匹配 "-option:v"（不是已经带数字的如 "-option:v:0"）
			if arg == prefix {
				processed = prefix + ":0"
				break
			}
		}
		
		result[i] = processed
	}
	
	return result
}

// executeFFmpeg 执行 FFmpeg 命令
func (t *Transcoder) executeFFmpeg(task *TranscodeTask, args []string) error {
	cmd := exec.CommandContext(t.ctx, t.ffmpegPath, args...)

	// 在 Windows 上隐藏窗口
	hideWindow(cmd)

	// 保存当前命令引用，用于取消
	t.currentCmdMu.Lock()
	t.currentCmd = cmd
	t.currentCmdMu.Unlock()

	defer func() {
		t.currentCmdMu.Lock()
		t.currentCmd = nil
		t.currentCmdMu.Unlock()
	}()

	// 获取 stdout 用于读取进度
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("获取 stdout 失败: %w", err)
	}

	// 获取 stderr 用于捕获错误信息
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("获取 stderr 失败: %w", err)
	}

	// 启动命令
	log.Printf("[transcoder] 执行命令: %s %s", t.ffmpegPath, strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 FFmpeg 失败: %w", err)
	}

	// 解析进度输出
	go t.parseProgress(task, stdout)

	// 捕获 stderr 输出
	var stderrOutput strings.Builder
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			stderrOutput.WriteString(line + "\n")
			// 只记录包含错误关键词的行
			if strings.Contains(strings.ToLower(line), "error") ||
				strings.Contains(strings.ToLower(line), "invalid") ||
				strings.Contains(strings.ToLower(line), "unrecognized") ||
				strings.Contains(strings.ToLower(line), "option") {
				log.Printf("[transcoder] FFmpeg stderr: %s", line)
			}
		}
	}()

	// 等待完成
	if err := cmd.Wait(); err != nil {
		// 检查是否是取消导致的
		if t.ctx.Err() != nil {
			return fmt.Errorf("任务已取消")
		}
		// 包含 stderr 输出以便诊断
		errOutput := stderrOutput.String()
		lastLines := getLastLines(errOutput, 15) // 获取最后 15 行
		return fmt.Errorf("FFmpeg 错误: %s\n\n--- FFmpeg 输出 (最后 15 行) ---\n%s", err.Error(), lastLines)
	}

	return nil
}

// parseProgress 解析 FFmpeg 进度输出
func (t *Transcoder) parseProgress(task *TranscodeTask, stdout io.ReadCloser) {
	scanner := bufio.NewScanner(stdout)
	timeRegex := regexp.MustCompile(`out_time_ms=(\d+)`)
	speedRegex := regexp.MustCompile(`speed=(.+)`)

	for scanner.Scan() {
		line := scanner.Text()

		// 解析当前时间
		if matches := timeRegex.FindStringSubmatch(line); len(matches) > 1 {
			timeMs, _ := strconv.ParseInt(matches[1], 10, 64)
			currentTime := float64(timeMs) / 1000000.0 // 转换为秒

			t.mu.Lock()
			task.CurrentTime = currentTime
			if task.Duration > 0 {
				task.Progress = (currentTime / task.Duration) * 100
				if task.Progress > 100 {
					task.Progress = 100
				}
			}
			t.mu.Unlock()
		}

		// 解析速度
		if matches := speedRegex.FindStringSubmatch(line); len(matches) > 1 {
			t.mu.Lock()
			task.Speed = strings.TrimSpace(matches[1])
			t.mu.Unlock()
		}
	}
}

// GetTask 获取任务状态
func (t *Transcoder) GetTask(taskID string) (*TranscodeTask, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	task, ok := t.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("任务不存在: %s", taskID)
	}
	return task, nil
}

// GetAllTasks 获取所有任务
func (t *Transcoder) GetAllTasks() []*TranscodeTask {
	t.mu.RLock()
	defer t.mu.RUnlock()

	tasks := make([]*TranscodeTask, 0, len(t.tasks))
	for _, task := range t.tasks {
		tasks = append(tasks, task)
	}
	return tasks
}

// CancelTask 取消任务
func (t *Transcoder) CancelTask(taskID string) error {
	t.mu.Lock()
	task, ok := t.tasks[taskID]
	if !ok {
		t.mu.Unlock()
		return fmt.Errorf("任务不存在: %s", taskID)
	}

	if task.Status == StatusProcessing {
		// 如果正在处理，需要终止 FFmpeg 进程
		t.currentCmdMu.Lock()
		if t.currentCmd != nil && t.currentCmd.Process != nil {
			t.currentCmd.Process.Kill()
		}
		t.currentCmdMu.Unlock()
	}

	task.Status = StatusCancelled
	task.CompletedAt = time.Now()
	t.mu.Unlock()

	log.Printf("[transcoder] 任务已取消: %s", taskID)
	return nil
}

// ClearCompletedTasks 清除已完成的任务
func (t *Transcoder) ClearCompletedTasks() {
	t.mu.Lock()
	defer t.mu.Unlock()

	for id, task := range t.tasks {
		if task.Status == StatusSuccess || task.Status == StatusFailed || task.Status == StatusCancelled {
			delete(t.tasks, id)
		}
	}
	log.Println("[transcoder] 已清除完成的任务")
}

// ClearCompleted 清除已完成的任务（ClearCompletedTasks 的别名）
func (t *Transcoder) ClearCompleted() {
	t.ClearCompletedTasks()
}

// CancelAll 取消所有正在进行或等待中的任务
func (t *Transcoder) CancelAll() {
	t.mu.Lock()
	taskIDs := make([]string, 0)
	for id, task := range t.tasks {
		if task.Status == StatusPending || task.Status == StatusProcessing {
			taskIDs = append(taskIDs, id)
		}
	}
	t.mu.Unlock()

	for _, id := range taskIDs {
		t.CancelTask(id)
	}
	log.Println("[transcoder] 已取消所有任务")
}
