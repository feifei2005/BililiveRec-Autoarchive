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
	"sort"
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

	// 任务序号计数器，用于保持添加顺序
	taskSeqCounter int64

	// 当前正在运行的 FFmpeg 进程，用于取消
	currentCmd   *exec.Cmd
	currentCmdMu sync.Mutex

	// 动态速度追踪（用于更准确的全局剩余时间估算）
	avgNormalizedFPS float64   // 归一化到1080p的平均处理速度
	avgFPSLastUpdate time.Time // 上次更新时间

	// 上一个完成任务的归一化处理速度（用于预测待处理任务）
	lastCompletedNormalizedFPS float64
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
// 只扫描 MKV 文件
func (t *Transcoder) ScanFolder(folderPath string) ([]VideoFile, error) {
	videoExtensions := map[string]bool{
		".mkv": true,
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

// ScanPath 扫描单个文件或文件夹中的视频文件（用于拖拽导入）
func (t *Transcoder) ScanPath(path string) ([]VideoFile, error) {
	videoExtensions := map[string]bool{
		".mkv": true,
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("无法访问路径: %w", err)
	}

	// 如果是文件夹，使用 ScanFolder
	if info.IsDir() {
		return t.ScanFolder(path)
	}

	// 如果是单个文件，检查扩展名
	ext := strings.ToLower(filepath.Ext(path))
	if !videoExtensions[ext] {
		return []VideoFile{}, nil // 不是视频文件，返回空列表
	}

	// 探测单个视频文件
	videoFile, err := t.probeVideo(path)
	if err != nil {
		log.Printf("[transcoder] 无法探测视频 %s: %v", path, err)
		// 即使探测失败，也添加基本信息
		return []VideoFile{{
			Path: path,
			Name: info.Name(),
			Size: info.Size(),
		}}, nil
	}

	videoFile.Path = path
	videoFile.Name = info.Name()
	videoFile.Size = info.Size()
	return []VideoFile{*videoFile}, nil
}

// ffprobeOutput ffprobe JSON 输出结构
type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

type ffprobeStream struct {
	Index        int    `json:"index"`
	CodecType    string `json:"codec_type"`
	CodecName    string `json:"codec_name"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	SampleRate   string `json:"sample_rate,omitempty"`
	BitRate      string `json:"bit_rate,omitempty"`
	RFrameRate   string `json:"r_frame_rate,omitempty"`   // 实际帧率（如 "30000/1001"）
	AvgFrameRate string `json:"avg_frame_rate,omitempty"` // 平均帧率
	NbFrames     string `json:"nb_frames,omitempty"`      // 总帧数
	Disposition  struct {
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
				// 解析帧率 (格式如 "30000/1001" 或 "30/1")
				video.FrameRate = parseFrameRate(stream.RFrameRate)
				if video.FrameRate == 0 {
					video.FrameRate = parseFrameRate(stream.AvgFrameRate)
				}
				// 解析总帧数
				if stream.NbFrames != "" {
					video.TotalFrames, _ = strconv.ParseInt(stream.NbFrames, 10, 64)
				}
				// 如果没有总帧数但有时长和帧率，计算总帧数
				if video.TotalFrames == 0 && video.Duration > 0 && video.FrameRate > 0 {
					video.TotalFrames = int64(video.Duration * video.FrameRate)
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

// parseFrameRate 解析帧率字符串 (如 "30000/1001" -> 29.97)
func parseFrameRate(frameRateStr string) float64 {
	if frameRateStr == "" || frameRateStr == "0/0" {
		return 0
	}
	parts := strings.Split(frameRateStr, "/")
	if len(parts) == 2 {
		num, err1 := strconv.ParseFloat(parts[0], 64)
		den, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 == nil && err2 == nil && den > 0 {
			return num / den
		}
	}
	// 尝试直接解析为浮点数
	rate, err := strconv.ParseFloat(frameRateStr, 64)
	if err == nil {
		return rate
	}
	return 0
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

	// 分配序号（原子递增）
	t.mu.Lock()
	t.taskSeqCounter++
	seqNum := t.taskSeqCounter
	t.mu.Unlock()

	task := &TranscodeTask{
		ID:         generateTaskID(inputPath),
		SeqNum:     seqNum,
		InputPath:  inputPath,
		OutputPath: outputPath,
		Config:     config,
		Status:     StatusPending,
		Progress:   0,
		CreatedAt:  time.Now(),
	}

	// 设置完整的视频元数据，用于全局剩余时间估算
	if videoInfo != nil {
		task.Duration = videoInfo.Duration
		task.Width = videoInfo.Width
		task.Height = videoInfo.Height
		task.FrameRate = videoInfo.FrameRate
		task.TotalFrames = videoInfo.TotalFrames

		// 计算预测处理速度和总时间（用于待处理任务的剩余时间估算）
		// 考虑帧率上限：如果设置了 MaxFPS 且源帧率超过上限，使用有效帧数估算
		task.PredictedFPS = predictProcessingFPS(videoInfo.Width, videoInfo.Height)
		effectiveFrames := getEffectiveFrames(task)
		if effectiveFrames > 0 && task.PredictedFPS > 0 {
			task.PredictedTotalTime = float64(effectiveFrames) / task.PredictedFPS
			task.PredictedTimeString = formatETADuration(task.PredictedTotalTime)
		}

		// 日志中显示源帧数和有效帧数（如果不同）
		if effectiveFrames != task.TotalFrames && effectiveFrames > 0 {
			log.Printf("[transcoder] 任务添加: %s, %dx%d, %.2f fps, %d 帧 (限帧后 %d 帧), 预测处理时间: %s",
				filepath.Base(inputPath), videoInfo.Width, videoInfo.Height,
				videoInfo.FrameRate, videoInfo.TotalFrames, effectiveFrames, task.PredictedTimeString)
		} else {
			log.Printf("[transcoder] 任务添加: %s, %dx%d, %.2f fps, %d 帧, 预测处理时间: %s",
				filepath.Base(inputPath), videoInfo.Width, videoInfo.Height,
				videoInfo.FrameRate, videoInfo.TotalFrames, task.PredictedTimeString)
		}
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

	// 输出文件名：保持原文件名，只替换扩展名
	return filepath.Join(outputDir, nameWithoutExt+ext)
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
	// 检查任务是否已被取消（用户可能在任务等待队列期间取消了它）
	t.mu.RLock()
	if task.Status == StatusCancelled {
		t.mu.RUnlock()
		log.Printf("[transcoder] 任务已取消，跳过处理: %s", task.InputPath)
		return
	}
	t.mu.RUnlock()

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

	// 设置任务的视频信息（用于进度估算）
	if videoInfo != nil {
		t.mu.Lock()
		task.Width = videoInfo.Width
		task.Height = videoInfo.Height
		task.FrameRate = videoInfo.FrameRate
		task.TotalFrames = videoInfo.TotalFrames
		task.Duration = videoInfo.Duration

		// 计算预测处理速度和总时间（考虑帧率上限）
		task.PredictedFPS = predictProcessingFPS(videoInfo.Width, videoInfo.Height)
		effectiveFrames := getEffectiveFrames(task)
		if effectiveFrames > 0 && task.PredictedFPS > 0 {
			task.PredictedTotalTime = float64(effectiveFrames) / task.PredictedFPS
			task.PredictedTimeString = formatETADuration(task.PredictedTotalTime)
		}
		t.mu.Unlock()
		if effectiveFrames != videoInfo.TotalFrames && effectiveFrames > 0 {
			log.Printf("[transcoder] 视频信息: %dx%d, %.2f fps, %d 帧 (限帧后 %d 帧), %.2f 秒, 预测速度: %.1f fps, 预测时间: %s",
				videoInfo.Width, videoInfo.Height, videoInfo.FrameRate, videoInfo.TotalFrames, effectiveFrames, videoInfo.Duration,
				task.PredictedFPS, task.PredictedTimeString)
		} else {
			log.Printf("[transcoder] 视频信息: %dx%d, %.2f fps, %d 帧, %.2f 秒, 预测速度: %.1f fps, 预测时间: %s",
				videoInfo.Width, videoInfo.Height, videoInfo.FrameRate, videoInfo.TotalFrames, videoInfo.Duration,
				task.PredictedFPS, task.PredictedTimeString)
		}
	}

	// 构建 FFmpeg 命令
	args := t.buildFFmpegArgs(task, videoInfo)

	// 执行转码
	if err := t.executeFFmpeg(task, args); err != nil {
		t.failTask(task, err)
		return
	}

	t.mu.Lock()
	// 任务完成后，记录实际处理速度用于后续预测
	if task.ProcessedFrame > 0 {
		elapsed := time.Since(task.StartedAt).Seconds()
		if elapsed > 0 {
			actualFPS := float64(task.ProcessedFrame) / elapsed
			t.lastCompletedNormalizedFPS = normalizeToBaseFPS(actualFPS, task.Width, task.Height)
			// 更新所有待处理任务的预计时间
			t.updatePendingTasksPrediction()
		}
	}
	task.Status = StatusSuccess
	task.Progress = 100
	task.ETASeconds = 0
	task.ETAString = "已完成"
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

	// 构建帧率限制过滤器（如果需要）
	// 使用 task.FrameRate（在 AddTask 时获取的源帧率）进行比较
	// 仅当 MaxFPS > 0 且源视频帧率大于 MaxFPS 时才添加帧率限制
	var fpsFilter string
	sourceFPS := task.FrameRate
	// 如果任务中没有帧率信息，尝试从 videoInfo 获取
	if sourceFPS <= 0 && videoInfo != nil {
		sourceFPS = videoInfo.FrameRate
	}

	if task.Config.MaxFPS > 0 {
		if sourceFPS > 0 {
			if sourceFPS > task.Config.MaxFPS {
				// 源帧率高于设定上限，应用帧率限制
				fpsFilter = fmt.Sprintf("fps=fps=%v", task.Config.MaxFPS)
				log.Printf("[transcoder] 应用帧率限制: 源 %.2f fps > 目标 %.2f fps，将降低帧率", sourceFPS, task.Config.MaxFPS)
			} else {
				// 源帧率低于或等于设定上限，不需要限制
				log.Printf("[transcoder] 不需要帧率限制: 源 %.2f fps <= 设定上限 %.2f fps", sourceFPS, task.Config.MaxFPS)
			}
		} else {
			// 无法获取源帧率，为安全起见不应用帧率限制
			log.Printf("[transcoder] 警告: 无法获取源帧率，跳过帧率限制 (设定上限: %.2f fps)", task.Config.MaxFPS)
		}
	}

	// 如果有帧率过滤器，需要与用户的 -vf 参数合并
	if fpsFilter != "" {
		customArgs = mergeVideoFilter(customArgs, fpsFilter)
	}

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

	// 不使用 -progress pipe:1，改为直接解析 stderr 输出
	// 这样可以避免多管道处理的复杂性和潜在阻塞问题

	// 添加输出文件
	args = append(args, task.OutputPath)

	return args
}

// mergeVideoFilter 将新的视频过滤器与现有的 -vf 参数合并
// 如果 customArgs 中已存在 -vf，则在其值后追加新过滤器（用逗号分隔）
// 如果不存在，则添加新的 -filter:v:0 参数
// 注意：使用 -filter:v:0 而不是 -vf，确保滤镜只应用于第一个视频流，
// 避免与封面流（使用 copy 编码器）冲突
func mergeVideoFilter(customArgs []string, newFilter string) []string {
	result := make([]string, 0, len(customArgs)+2)
	vfFound := false

	for i := 0; i < len(customArgs); i++ {
		if customArgs[i] == "-vf" || customArgs[i] == "-filter:v" || customArgs[i] == "-filter:v:0" {
			vfFound = true
			// 强制使用 -filter:v:0 确保只对第一个视频流生效
			result = append(result, "-filter:v:0")
			if i+1 < len(customArgs) {
				// 合并现有过滤器和新过滤器
				result = append(result, customArgs[i+1]+","+newFilter)
				i++ // 跳过下一个参数（过滤器值）
			} else {
				// -vf 后面没有值，直接使用新过滤器
				result = append(result, newFilter)
			}
		} else {
			result = append(result, customArgs[i])
		}
	}

	// 如果没有找到 -vf 参数，添加新的 -filter:v:0
	if !vfFound {
		result = append(result, "-filter:v:0", newFilter)
	}

	return result
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

	// 获取 stderr 用于捕获进度和错误信息
	// 注意：不使用 -progress pipe:1，FFmpeg 的进度信息会输出到 stderr
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("获取 stderr 失败: %w", err)
	}

	// 启动命令
	log.Printf("[transcoder] 执行命令: %s %s", t.ffmpegPath, strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 FFmpeg 失败: %w", err)
	}

	// 使用 WaitGroup 确保管道读取完成后再调用 cmd.Wait()
	var pipeWg sync.WaitGroup
	pipeWg.Add(1)

	// 用于看门狗的帧数跟踪（基于实际进度而非仅有输出）
	var lastFrameCount int64
	var lastFrameTime time.Time
	var frameMu sync.Mutex
	lastFrameTime = time.Now()

	// stderr 输出收集
	var stderrOutput strings.Builder

	// 解析 stderr 中的控制台风格进度输出
	// FFmpeg 输出格式: frame= 720 fps=107 q=0.0 size=   12800kB time=00:00:24.00 bitrate=4369.1kbits/s speed=3.56x
	go func() {
		defer pipeWg.Done()

		// 用于解析控制台风格输出的正则表达式
		frameRegex := regexp.MustCompile(`frame=\s*(\d+)`)
		fpsRegex := regexp.MustCompile(`fps=\s*([\d.]+)`)
		speedRegex := regexp.MustCompile(`speed=\s*([\d.]+)x`)
		timeRegex := regexp.MustCompile(`time=(\d{2}):(\d{2}):(\d{2})\.(\d{2})`)

		buf := make([]byte, 4096)
		var lineBuf strings.Builder

		for {
			n, readErr := stderr.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				stderrOutput.WriteString(chunk)
				lineBuf.WriteString(chunk)

				// 处理完整的行
				content := lineBuf.String()
				lines := strings.Split(content, "\r")

				for i, line := range lines {
					// 最后一个元素可能是不完整的行，保留它
					if i == len(lines)-1 && !strings.HasSuffix(content, "\r") && !strings.HasSuffix(content, "\n") {
						lineBuf.Reset()
						lineBuf.WriteString(line)
						continue
					}

					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}

					// 解析帧数
					if matches := frameRegex.FindStringSubmatch(line); len(matches) > 1 {
						frame, _ := strconv.ParseInt(matches[1], 10, 64)
						t.mu.Lock()
						task.ProcessedFrame = frame
						t.updateETA(task)
						t.mu.Unlock()

						// 更新帧数跟踪（用于看门狗）
						frameMu.Lock()
						if frame > lastFrameCount {
							lastFrameCount = frame
							lastFrameTime = time.Now()
						}
						frameMu.Unlock()
					}

					// 解析 FPS
					if matches := fpsRegex.FindStringSubmatch(line); len(matches) > 1 {
						fps, _ := strconv.ParseFloat(matches[1], 64)
						t.mu.Lock()
						task.CurrentFPS = fps
						t.updateETA(task)
						t.mu.Unlock()

						// 更新全局平均处理速度（用于更准确的剩余时间估算）
						t.updateAvgFPS(fps, task.Width, task.Height)
					}

					// 解析时间 (格式: HH:MM:SS.ms)
					if matches := timeRegex.FindStringSubmatch(line); len(matches) > 4 {
						hours, _ := strconv.Atoi(matches[1])
						minutes, _ := strconv.Atoi(matches[2])
						seconds, _ := strconv.Atoi(matches[3])
						ms, _ := strconv.Atoi(matches[4])
						currentTime := float64(hours*3600+minutes*60+seconds) + float64(ms)/100.0

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
						task.Speed = matches[1] + "x"
						t.mu.Unlock()
					}

					// 检查错误关键词
					lineLower := strings.ToLower(line)
					if strings.Contains(lineLower, "error") ||
						strings.Contains(lineLower, "invalid") ||
						strings.Contains(lineLower, "unrecognized") {
						log.Printf("[transcoder] FFmpeg: %s", line)
					}
				}

				// 如果以换行结尾，清空缓冲区
				if strings.HasSuffix(content, "\r") || strings.HasSuffix(content, "\n") {
					lineBuf.Reset()
				}
			}
			if readErr != nil {
				break // EOF 或其他错误
			}
		}
	}()

	// 看门狗协程 - 基于帧数变化检测 FFmpeg 卡住
	// 如果 30 秒内帧数没有增加，认为进程卡住
	watchdogTimeout := 30 * time.Second
	watchdogDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-watchdogDone:
				return
			case <-t.ctx.Done():
				return
			case <-ticker.C:
				frameMu.Lock()
				elapsed := time.Since(lastFrameTime)
				currentFrame := lastFrameCount
				frameMu.Unlock()

				// 只有在已经开始处理帧后才检查卡住
				if currentFrame > 0 && elapsed > watchdogTimeout {
					log.Printf("[transcoder] 警告: FFmpeg 超过 %v 帧数无变化 (当前帧: %d)，正在终止进程",
						watchdogTimeout, currentFrame)
					if cmd.Process != nil {
						cmd.Process.Kill()
					}
					return
				}

				// 如果还没开始处理帧，给更长的初始化时间（2分钟）
				if currentFrame == 0 && elapsed > 2*time.Minute {
					log.Printf("[transcoder] 警告: FFmpeg 超过 2 分钟未开始处理，正在终止进程")
					if cmd.Process != nil {
						cmd.Process.Kill()
					}
					return
				}
			}
		}
	}()

	// 等待管道读取完成
	pipeWg.Wait()

	// 停止看门狗
	close(watchdogDone)

	// 等待进程完成
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

// parseProgressWithActivity 解析 FFmpeg 进度输出并更新活动时间（用于看门狗）
// 使用帧数和 FPS 计算 ETA，而不是百分比进度
func (t *Transcoder) parseProgressWithActivity(task *TranscodeTask, stdout io.ReadCloser, updateActivity func()) {
	scanner := bufio.NewScanner(stdout)
	frameRegex := regexp.MustCompile(`^frame=(\d+)`)
	fpsRegex := regexp.MustCompile(`^fps=([\d.]+)`)
	speedRegex := regexp.MustCompile(`^speed=([\d.]+)`)
	timeRegex := regexp.MustCompile(`^out_time_ms=(\d+)`)

	for scanner.Scan() {
		line := scanner.Text()

		// 每次读取到数据都更新活动时间
		updateActivity()

		// 解析已处理帧数
		if matches := frameRegex.FindStringSubmatch(line); len(matches) > 1 {
			frame, _ := strconv.ParseInt(matches[1], 10, 64)
			t.mu.Lock()
			task.ProcessedFrame = frame
			t.updateETA(task)
			t.mu.Unlock()
		}

		// 解析当前处理 FPS
		if matches := fpsRegex.FindStringSubmatch(line); len(matches) > 1 {
			fps, _ := strconv.ParseFloat(matches[1], 64)
			t.mu.Lock()
			task.CurrentFPS = fps
			t.updateETA(task)
			t.mu.Unlock()
		}

		// 解析当前时间（用于计算进度百分比作为备用）
		if matches := timeRegex.FindStringSubmatch(line); len(matches) > 1 {
			timeMs, _ := strconv.ParseInt(matches[1], 10, 64)
			currentTime := float64(timeMs) / 1000000.0 // 转换为秒

			t.mu.Lock()
			task.CurrentTime = currentTime
			// 使用时间计算进度百分比（作为备用显示）
			if task.Duration > 0 {
				task.Progress = (currentTime / task.Duration) * 100
				if task.Progress > 100 {
					task.Progress = 100
				}
			}
			t.mu.Unlock()
		}

		// 解析速度倍率
		if matches := speedRegex.FindStringSubmatch(line); len(matches) > 1 {
			t.mu.Lock()
			task.Speed = strings.TrimSpace(matches[1]) + "x"
			t.mu.Unlock()
		}
	}

	// 检查 scanner 错误
	if err := scanner.Err(); err != nil {
		log.Printf("[transcoder] 读取 stdout 时发生错误: %v", err)
	}
}

// updateETA 根据已处理帧数和当前 FPS 计算剩余时间、已用时间和进度
// 必须在持有 t.mu 锁的情况下调用
func (t *Transcoder) updateETA(task *TranscodeTask) {
	// 更新已用时间
	if !task.StartedAt.IsZero() {
		task.ElapsedSeconds = time.Since(task.StartedAt).Seconds()
		task.ElapsedString = formatETADuration(task.ElapsedSeconds)
	}

	// 获取考虑帧率上限后的有效帧数
	effectiveFrames := getEffectiveFrames(task)

	// 基于帧数更新进度（使用有效帧数）
	if effectiveFrames > 0 && task.ProcessedFrame > 0 {
		task.Progress = float64(task.ProcessedFrame) / float64(effectiveFrames) * 100
		if task.Progress > 100 {
			task.Progress = 100
		}
	}

	// 需要有有效帧数和当前 FPS 才能计算 ETA
	if effectiveFrames <= 0 || task.CurrentFPS <= 0 {
		task.ETAString = "计算中..."
		return
	}

	remainingFrames := effectiveFrames - task.ProcessedFrame
	if remainingFrames <= 0 {
		task.ETASeconds = 0
		task.ETAString = "即将完成"
		return
	}

	// ETA = 剩余帧数 / 当前 FPS
	etaSeconds := float64(remainingFrames) / task.CurrentFPS
	task.ETASeconds = etaSeconds

	// 格式化 ETA 字符串
	task.ETAString = formatETADuration(etaSeconds)
}

// formatETADuration 格式化剩余时间为人类可读字符串
func formatETADuration(seconds float64) string {
	if seconds <= 0 {
		return "即将完成"
	}
	if seconds < 60 {
		return fmt.Sprintf("%.0f 秒", seconds)
	}
	if seconds < 3600 {
		minutes := int(seconds) / 60
		secs := int(seconds) % 60
		return fmt.Sprintf("%d 分 %d 秒", minutes, secs)
	}
	hours := int(seconds) / 3600
	minutes := (int(seconds) % 3600) / 60
	return fmt.Sprintf("%d 小时 %d 分", hours, minutes)
}

// predictProcessingFPS 基于分辨率预测处理速度
// 基准：1080p → 112帧/s，分辨率越低速度越快
func predictProcessingFPS(width, height int) float64 {
	baseFPS := 112.0              // 1080p的基准处理速度
	basePixels := 1920.0 * 1080.0 // 1080p像素数

	if width <= 0 || height <= 0 {
		return baseFPS // 无法获取分辨率时使用基准值
	}

	currentPixels := float64(width * height)

	// 像素比例影响（像素越少越快）
	pixelRatio := basePixels / currentPixels

	return baseFPS * pixelRatio
}

// normalizeToBaseFPS 将实际处理速度归一化到1080p基准
// 这样可以将不同分辨率的处理速度统一到同一尺度进行平均
func normalizeToBaseFPS(actualFPS float64, width, height int) float64 {
	const basePixels = 1920.0 * 1080.0
	if width <= 0 || height <= 0 {
		return actualFPS
	}
	actualPixels := float64(width * height)
	return actualFPS * (actualPixels / basePixels)
}

// denormalizeFromBaseFPS 将1080p基准速度转换到目标分辨率
func denormalizeFromBaseFPS(normalizedFPS float64, width, height int) float64 {
	const basePixels = 1920.0 * 1080.0
	if width <= 0 || height <= 0 {
		return normalizedFPS
	}
	targetPixels := float64(width * height)
	return normalizedFPS * (basePixels / targetPixels)
}

// updateAvgFPS 更新全局平均处理速度（使用指数滑动平均）
// 在每次解析 FFmpeg 进度输出时调用
func (t *Transcoder) updateAvgFPS(taskFPS float64, width, height int) {
	if taskFPS <= 0 {
		return
	}

	normalizedFPS := normalizeToBaseFPS(taskFPS, width, height)

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.avgNormalizedFPS <= 0 {
		t.avgNormalizedFPS = normalizedFPS
	} else {
		// 指数滑动平均，α=0.2 表示新值权重20%
		t.avgNormalizedFPS = t.avgNormalizedFPS*0.8 + normalizedFPS*0.2
	}
	t.avgFPSLastUpdate = time.Now()
}

// getDynamicPredictedFPS 获取动态预测FPS
// 优先使用上一个完成任务的速度，其次使用滑动平均，最后回退到固定基准值
func (t *Transcoder) getDynamicPredictedFPS(width, height int) float64 {
	// 注意：调用此方法前应已持有 t.mu 读锁
	// 优先使用上一个完成任务的速度
	if t.lastCompletedNormalizedFPS > 0 {
		return denormalizeFromBaseFPS(t.lastCompletedNormalizedFPS, width, height)
	}
	// 其次使用滑动平均（处理中任务的速度）
	if t.avgNormalizedFPS > 0 && time.Since(t.avgFPSLastUpdate) < 5*time.Minute {
		return denormalizeFromBaseFPS(t.avgNormalizedFPS, width, height)
	}
	// 回退到固定基准值
	return predictProcessingFPS(width, height)
}

// getEffectiveFrames 获取考虑帧率上限后的有效帧数
// 如果设置了帧率上限且源帧率超过上限，则按上限帧率计算实际输出帧数
func getEffectiveFrames(task *TranscodeTask) int64 {
	if task.Config.MaxFPS > 0 && task.FrameRate > task.Config.MaxFPS && task.Duration > 0 {
		return int64(task.Duration * task.Config.MaxFPS)
	}
	return task.TotalFrames
}

// updatePendingTasksPrediction 基于最近完成任务的速度更新所有待处理任务的预计时间
// 必须在持有 t.mu 锁的情况下调用
func (t *Transcoder) updatePendingTasksPrediction() {
	if t.lastCompletedNormalizedFPS <= 0 {
		return
	}

	for _, task := range t.tasks {
		if task.Status == StatusPending {
			// 将归一化速度转换到任务的分辨率
			predictedFPS := denormalizeFromBaseFPS(t.lastCompletedNormalizedFPS, task.Width, task.Height)
			effectiveFrames := getEffectiveFrames(task)
			if effectiveFrames > 0 && predictedFPS > 0 {
				task.PredictedFPS = predictedFPS
				task.PredictedTotalTime = float64(effectiveFrames) / predictedFPS
				task.PredictedTimeString = formatETADuration(task.PredictedTotalTime)
			}
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

// GetAllTasks 获取所有任务（按添加顺序排序，新任务在后）
func (t *Transcoder) GetAllTasks() []*TranscodeTask {
	t.mu.RLock()
	defer t.mu.RUnlock()

	tasks := make([]*TranscodeTask, 0, len(t.tasks))
	for _, task := range t.tasks {
		tasks = append(tasks, task)
	}

	// 按 SeqNum 排序，确保按添加顺序返回
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].SeqNum < tasks[j].SeqNum
	})

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

// GlobalStatus 全局状态信息
type GlobalStatus struct {
	TotalRemainingSeconds float64 `json:"totalRemainingSeconds"` // 总剩余时间（秒）
	TotalRemainingString  string  `json:"totalRemainingString"`  // 总剩余时间格式化
	PendingCount          int     `json:"pendingCount"`          // 待处理任务数
	ProcessingCount       int     `json:"processingCount"`       // 处理中任务数
}

// GetGlobalStatus 获取全局状态，包括所有待处理任务的预估总剩余时间
// 总剩余 = (Σ(所有待处理任务的绿色预计时间) + Σ(当前处理中任务的蓝色剩余时间)) ÷ 线程数
func (t *Transcoder) GetGlobalStatus() GlobalStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var pendingCount, processingCount int
	var pendingTotalTime float64   // 所有待处理任务的绿色预计时间之和
	var processingTotalETA float64 // 所有处理中任务的蓝色剩余时间之和

	for _, task := range t.tasks {
		switch task.Status {
		case StatusPending:
			pendingCount++
			// 使用任务自身的预计时间（基于上一个完成任务的速度）
			if task.PredictedTotalTime > 0 {
				pendingTotalTime += task.PredictedTotalTime
			} else {
				// 回退：如果没有预计时间，使用默认预测
				predictedFPS := t.getDynamicPredictedFPS(task.Width, task.Height)
				effectiveFrames := getEffectiveFrames(task)
				if effectiveFrames > 0 && predictedFPS > 0 {
					pendingTotalTime += float64(effectiveFrames) / predictedFPS
				}
			}

		case StatusProcessing:
			processingCount++
			// 使用当前任务的实际剩余时间（蓝色剩余时间）
			if task.ETASeconds > 0 {
				processingTotalETA += task.ETASeconds
			} else {
				// 回退计算
				effectiveFrames := getEffectiveFrames(task)
				remainingFrames := effectiveFrames - task.ProcessedFrame
				if remainingFrames > 0 && task.CurrentFPS > 0 {
					processingTotalETA += float64(remainingFrames) / task.CurrentFPS
				} else if remainingFrames > 0 {
					predictedFPS := t.getDynamicPredictedFPS(task.Width, task.Height)
					if predictedFPS > 0 {
						processingTotalETA += float64(remainingFrames) / predictedFPS
					}
				}
			}
		}
	}

	// 计算有效线程数
	effectiveWorkers := t.maxWorkers
	if effectiveWorkers < 1 {
		effectiveWorkers = 1
	}

	// 总剩余时间 = (待处理任务预计时间之和 + 处理中任务剩余时间之和) ÷ 线程数
	totalRemaining := (pendingTotalTime + processingTotalETA) / float64(effectiveWorkers)

	return GlobalStatus{
		TotalRemainingSeconds: totalRemaining,
		TotalRemainingString:  formatETADuration(totalRemaining),
		PendingCount:          pendingCount,
		ProcessingCount:       processingCount,
	}
}
