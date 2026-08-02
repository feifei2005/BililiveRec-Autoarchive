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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Transcoder 转码器
type Transcoder struct {
	ffmpegPath  string
	ffprobePath string

	tasks     map[string]*TranscodeTask
	taskQueue chan *TranscodeTask
	nv12Queue chan nv12FallbackJob
	mu        sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup

	// 任务序号��数器，用于保持添加顺序
	taskSeqCounter int64

	// 每个任务当前正在运行的 FFmpeg 进程，用于并发任务的取消和暂停
	currentCmds  map[string]*exec.Cmd
	currentCmdMu sync.Mutex

	// 动态并发数控制
	workersMu      sync.Mutex
	targetWorkers  int // 目标并发数，worker 完成当前任务后据此判断是否退出
	activeWorkers  int
	nextWorkerID   int
	workersStarted bool
	workerWake     chan struct{}

	// 动态速度追踪（用于更准确的全局剩余时间估算）
	avgNormalizedFPS float64   // 归一化到1080p的平均处理速度
	avgFPSLastUpdate time.Time // 上次更新时间

	// 上一个完成任务的归一化处理速度（用于预测待处理任务）
	lastCompletedNormalizedFPS float64

	// 暂停相关状态
	pauseMu                    sync.RWMutex
	transcodePaused            bool          // 立即暂停状态
	transcodePauseAfterCurrent bool          // 当前任务后暂停状态
	transcodePausedTime        time.Time     // 暂停开始时间
	transcodeTotalPausedTime   time.Duration // 累计暂停时长（当前任务）

	// 处理日志回调（由 app 层注入，用于持久化到数据库）
	onProcessLog func(ProcessLogEntry)

	// QSV 滤镜链重初始化失败的回退策略
	qsvReinitMu       sync.RWMutex
	qsvReinitStrategy QSVReinitStrategy

	// 测试注入点；生产环境为空时调用 transcodeWithNV12Fallback。
	nv12FallbackRunner func(*TranscodeTask, *VideoFile) error
}

type nv12FallbackJob struct {
	task          *TranscodeTask
	videoInfo     *VideoFile
	originalError error
}

// QSVReinitStrategy QSV 滤镜链重初始化失败（视频流分辨率变化导致）的回退策略
type QSVReinitStrategy string

const (
	QSVStrategyError   QSVReinitStrategy = "error"   // 直接失败
	QSVStrategySegment QSVReinitStrategy = "segment" // 分段转码后输出独立文件
	QSVStrategyNV12    QSVReinitStrategy = "nv12"    // 改用 NV12 软件滤镜链重试
)

// SetQSVReinitStrategy 设置 QSV 回退策略
func (t *Transcoder) SetQSVReinitStrategy(s QSVReinitStrategy) {
	t.qsvReinitMu.Lock()
	defer t.qsvReinitMu.Unlock()
	t.qsvReinitStrategy = s
}

func (t *Transcoder) getQSVReinitStrategy() QSVReinitStrategy {
	t.qsvReinitMu.RLock()
	defer t.qsvReinitMu.RUnlock()
	return t.qsvReinitStrategy
}

// ProcessLogEntry 处理日志条目（用于回调通知 app 层持久化）
type ProcessLogEntry struct {
	TaskID     string    // 任务ID
	InputPath  string    // 输入文件路径
	OutputPath string    // 主输出文件路径
	Status     string    // 处理状态（success/failed）
	Error      string    // 错误信息（失败时）
	StartTime  time.Time // 开始时间
	EndTime    time.Time // 结束时间
}

// SetProcessLogCallback 设置处理日志回调
// 当转码任务完成（成功或失败）时，会调用此回调通知 app 层持久化
func (t *Transcoder) SetProcessLogCallback(fn func(ProcessLogEntry)) {
	t.onProcessLog = fn
}

func (t *Transcoder) setCurrentCommand(taskID string, cmd *exec.Cmd) {
	t.currentCmdMu.Lock()
	t.currentCmds[taskID] = cmd
	t.currentCmdMu.Unlock()
}

func (t *Transcoder) clearCurrentCommand(taskID string, cmd *exec.Cmd) {
	t.currentCmdMu.Lock()
	if t.currentCmds[taskID] == cmd {
		delete(t.currentCmds, taskID)
	}
	t.currentCmdMu.Unlock()
}

func (t *Transcoder) currentCommands() []*exec.Cmd {
	t.currentCmdMu.Lock()
	defer t.currentCmdMu.Unlock()
	commands := make([]*exec.Cmd, 0, len(t.currentCmds))
	for _, cmd := range t.currentCmds {
		commands = append(commands, cmd)
	}
	return commands
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
		ffmpegPath:        cfg.FFmpegPath,
		ffprobePath:       cfg.FFprobePath,
		tasks:             make(map[string]*TranscodeTask),
		taskQueue:         make(chan *TranscodeTask, 65535),
		nv12Queue:         make(chan nv12FallbackJob, 65535),
		currentCmds:       make(map[string]*exec.Cmd),
		targetWorkers:     cfg.MaxWorkers,
		workerWake:        make(chan struct{}, 65535),
		qsvReinitStrategy: QSVStrategyNV12,
	}
}

// startWorkerLocked 启动一个 worker。调用方必须持有 workersMu。
func (t *Transcoder) startWorkerLocked() {
	id := t.nextWorkerID
	t.nextWorkerID++
	t.activeWorkers++
	t.wg.Add(1)
	go t.worker(id)
}

// SetMaxWorkers 动态调整并发转码路数
// 增加 worker 时立即启动；减少 worker 时，多余的 worker 会在当前任务完成后退出
func (t *Transcoder) SetMaxWorkers(n int) {
	if n <= 0 {
		n = 1
	}

	t.workersMu.Lock()
	old := t.targetWorkers
	t.targetWorkers = n
	started := t.workersStarted
	active := t.activeWorkers
	if started && n > active {
		for i := active; i < n; i++ {
			t.startWorkerLocked()
		}
	}
	toWake := 0
	if started && n < active {
		toWake = active - n
	}
	t.workersMu.Unlock()

	// 唤醒空闲 worker，使降低并发数无需等到下一项任务入队。
	for i := 0; i < toWake; i++ {
		select {
		case t.workerWake <- struct{}{}:
		default:
		}
	}

	if n > old {
		log.Printf("[transcoder] 并发数上调至 %d，新增 %d 个 worker", n, n-old)
	} else if n < old {
		log.Printf("[transcoder] 并发数下调至 %d，%d 个 worker 将在当前任务完成后退出", n, old-n)
	} else {
		log.Printf("[transcoder] 并发数保持 %d", n)
	}
}

// MaxWorkers 返回当前目标并发路数
func (t *Transcoder) MaxWorkers() int {
	t.workersMu.Lock()
	defer t.workersMu.Unlock()
	return t.targetWorkers
}

// Start 启动转码器
func (t *Transcoder) Start(ctx context.Context) error {
	t.workersMu.Lock()
	if t.workersStarted {
		t.workersMu.Unlock()
		return fmt.Errorf("转码器已经启动")
	}
	t.ctx, t.cancel = context.WithCancel(ctx)
	t.workersStarted = true
	workerCount := t.targetWorkers
	for i := 0; i < workerCount; i++ {
		t.startWorkerLocked()
	}
	t.wg.Add(1)
	go t.nv12FallbackWorker()
	t.workersMu.Unlock()

	log.Printf("[transcoder] 启动转码器，主池并发数: %d，NV12 回退池并发数: 1", workerCount)

	return nil
}

// Stop 停止转码器
func (t *Transcoder) Stop() error {
	log.Println("[transcoder] 正在停止转码器...")
	t.workersMu.Lock()
	t.workersStarted = false
	t.workersMu.Unlock()

	if t.cancel != nil {
		t.cancel()
	}

	// 取消所有正在运行的 FFmpeg 进程
	t.currentCmdMu.Lock()
	commands := make([]*exec.Cmd, 0, len(t.currentCmds))
	for _, cmd := range t.currentCmds {
		commands = append(commands, cmd)
	}
	t.currentCmdMu.Unlock()
	for _, cmd := range commands {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}

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
	registered := true
	defer func() {
		t.workersMu.Lock()
		if registered {
			t.activeWorkers--
		}
		t.workersMu.Unlock()
		t.wg.Done()
	}()
	log.Printf("[transcoder] Worker %d 启动", id)

	for {
		// 动态并发数调整：多余的 worker 在完成当前任务后退出。
		t.workersMu.Lock()
		shouldExit := t.activeWorkers > t.targetWorkers
		if shouldExit {
			t.activeWorkers--
			registered = false
		}
		t.workersMu.Unlock()
		if shouldExit {
			log.Printf("[transcoder] Worker %d: 并发数已下调，停止接收新任务", id)
			return
		}

		// 检查是否处于"当前任务后暂停"状态
		t.pauseMu.RLock()
		shouldWait := t.transcodePauseAfterCurrent || t.transcodePaused
		t.pauseMu.RUnlock()

		if shouldWait {
			// 进入等待状态，不取新任务
			log.Printf("[transcoder] Worker %d: 等待暂停恢复...", id)
			for {
				time.Sleep(500 * time.Millisecond)

				// 检查是否取消或退出
				select {
				case <-t.ctx.Done():
					log.Printf("[transcoder] Worker %d 停止", id)
					return
				default:
				}

				// 检查暂停状态是否解除
				t.pauseMu.RLock()
				stillWaiting := t.transcodePauseAfterCurrent || t.transcodePaused
				t.pauseMu.RUnlock()

				if !stillWaiting {
					break
				}
			}
		}

		select {
		case <-t.ctx.Done():
			log.Printf("[transcoder] Worker %d 停止", id)
			return
		case <-t.workerWake:
			continue
		case task, ok := <-t.taskQueue:
			if !ok {
				log.Printf("[transcoder] Worker %d: 队列已关闭", id)
				return
			}
			t.processTask(task)
		}
	}
}

// nv12FallbackWorker 独立于主转码池，固定单并发处理分辨率变化任务。
func (t *Transcoder) nv12FallbackWorker() {
	defer t.wg.Done()
	log.Printf("[transcoder] NV12 回退池启动，并发数: 1")

	for {
		select {
		case <-t.ctx.Done():
			log.Printf("[transcoder] NV12 回退池停止")
			return
		case job := <-t.nv12Queue:
			t.processNV12Fallback(job)
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
		ID:            generateTaskID(inputPath),
		SeqNum:        seqNum,
		InputPath:     inputPath,
		OutputPath:    outputPath,
		Config:        config,
		Status:        StatusPending,
		ExecutionPool: "main",
		Progress:      0,
		CreatedAt:     time.Now(),
	}

	// 设置完整的视频元数据，用于全局剩余时间估算
	if videoInfo != nil {
		task.Duration = videoInfo.Duration
		task.Width = videoInfo.Width
		task.Height = videoInfo.Height
		task.FrameRate = videoInfo.FrameRate
		task.TotalFrames = videoInfo.TotalFrames

		// 计算预测处理速度和总时间（基于实测速度，无实测数据时不预测）
		// 考虑帧率上限：如果设置了 MaxFPS 且源帧率超过上限，使用有效帧数估算
		task.PredictedFPS = t.getDynamicPredictedFPS(videoInfo.Width, videoInfo.Height)
		effectiveFrames := getEffectiveFrames(task)
		if effectiveFrames > 0 && task.PredictedFPS > 0 {
			task.PredictedTotalTime = float64(effectiveFrames) / task.PredictedFPS
			task.PredictedTimeString = formatETADuration(task.PredictedTotalTime)
		} else {
			// 无实测数据，显示占位符
			task.PredictedTimeString = "--:--"
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

	// 加入队列（先入队成功，再加入 map，确保原子性）
	select {
	case t.taskQueue <- task:
		t.mu.Lock()
		t.tasks[task.ID] = task
		t.mu.Unlock()
		log.Printf("[transcoder] 任务已加入队列: %s", inputPath)
	default:
		return nil, fmt.Errorf("任务队列已满")
	}

	return task, nil
}

// buildOutputPath 构建输出文件路径
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
	task.ExecutionPool = "main"
	task.StartedAt = time.Now()
	t.mu.Unlock()

	// 确保输出目录存在
	if task.OutputPath != "" {
		if err := os.MkdirAll(filepath.Dir(task.OutputPath), 0755); err != nil {
			t.failTask(task, fmt.Errorf("创建输出目录失败: %w", err))
			return
		}
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

		// 计算预测处理速度和总时间（基于实测速度，无实测数据时不预测）
		task.PredictedFPS = t.getDynamicPredictedFPS(videoInfo.Width, videoInfo.Height)
		effectiveFrames := getEffectiveFrames(task)
		if effectiveFrames > 0 && task.PredictedFPS > 0 {
			task.PredictedTotalTime = float64(effectiveFrames) / task.PredictedFPS
			task.PredictedTimeString = formatETADuration(task.PredictedTotalTime)
		} else {
			task.PredictedTimeString = "--:--"
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
		if t.isTaskCancelled(task) {
			return
		}
		// 检查是否是 QSV 滤镜链重初始化失败（分辨率变化导致）
		if isQSVReinitError(err) {
			strategy := QSVReinitStrategy(task.Config.QSVReinitStrategy)
			switch strategy {
			case QSVStrategyError, QSVStrategySegment, QSVStrategyNV12:
				// 使用任务创建时指定的策略。
			default:
				strategy = t.getQSVReinitStrategy()
			}
			log.Printf("[transcoder] 检测到 QSV 滤镜链重初始化失败，回退策略: %s", strategy)
			switch strategy {
			case QSVStrategyNV12, QSVStrategySegment:
				// segment 是旧配置值。新行为统一移交独立的单并发 NV12 池，
				// 主 worker 立即返回并继续处理主队列。
				if queueErr := t.enqueueNV12Fallback(task, videoInfo, err); queueErr != nil {
					t.failTask(task, fmt.Errorf("原始错误: %v\n加入 NV12 回退池失败: %v", err, queueErr))
				}
				return
			default:
				t.failTask(task, err)
				return
			}
		} else {
			t.failTask(task, err)
			return
		}
	}

	t.completeTask(task)
}

func (t *Transcoder) enqueueNV12Fallback(task *TranscodeTask, videoInfo *VideoFile, originalErr error) error {
	t.mu.Lock()
	if task.Status == StatusCancelled {
		t.mu.Unlock()
		return fmt.Errorf("任务已取消")
	}
	task.ExecutionPool = "nv12"
	task.Progress = 0
	task.ProcessedFrame = 0
	task.CurrentTime = 0
	task.CurrentFPS = 0
	task.Speed = ""
	task.ETASeconds = 0
	task.ETAString = "等待 NV12 回退"
	t.mu.Unlock()

	job := nv12FallbackJob{task: task, videoInfo: videoInfo, originalError: originalErr}
	select {
	case <-t.ctx.Done():
		return t.ctx.Err()
	case t.nv12Queue <- job:
		log.Printf("[transcoder] 任务已移交 NV12 回退池: %s", task.InputPath)
		return nil
	}
}

func (t *Transcoder) processNV12Fallback(job nv12FallbackJob) {
	if t.isTaskCancelled(job.task) {
		return
	}

	t.mu.Lock()
	job.task.ETAString = "NV12 回退处理中"
	t.mu.Unlock()

	log.Printf("[transcoder] NV12 回退池开始处理: %s", job.task.InputPath)
	var err error
	if t.nv12FallbackRunner != nil {
		err = t.nv12FallbackRunner(job.task, job.videoInfo)
	} else {
		err = t.transcodeWithNV12Fallback(job.task, job.videoInfo)
	}
	if err != nil {
		if t.isTaskCancelled(job.task) {
			return
		}
		t.failTask(job.task, fmt.Errorf("原始 QSV 错误: %v\nNV12 回退失败: %v", job.originalError, err))
		return
	}

	t.completeTask(job.task)
}

func (t *Transcoder) completeTask(task *TranscodeTask) {

	t.mu.Lock()
	if task.Status == StatusCancelled {
		t.mu.Unlock()
		return
	}
	// NV12 池有独立排队和内存传输开销，不能用来预测主池速度。
	if task.ExecutionPool == "main" && task.ProcessedFrame > 0 {
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
	startedAt := task.StartedAt
	outputPath := task.OutputPath
	t.mu.Unlock()

	// 通知 app 层持久化处理日志
	if t.onProcessLog != nil {
		t.onProcessLog(ProcessLogEntry{
			TaskID:     task.ID,
			InputPath:  task.InputPath,
			OutputPath: outputPath,
			Status:     "success",
			StartTime:  startedAt,
			EndTime:    task.CompletedAt,
		})
	}

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

// failTask 标记任务失败并写入错误日志文件
func (t *Transcoder) failTask(task *TranscodeTask, err error) {
	t.mu.Lock()
	if task.Status == StatusCancelled {
		t.mu.Unlock()
		return
	}
	task.Status = StatusFailed
	task.Error = err.Error()
	task.CompletedAt = time.Now()
	startedAt := task.StartedAt
	t.mu.Unlock()

	// 写入错误日志文件
	logPath := t.writeErrorLog(task, err)
	if logPath != "" {
		t.mu.Lock()
		task.ErrorLogPath = logPath
		t.mu.Unlock()
	}

	// 通知 app 层持久化处理日志（用于错误日志页面）
	if t.onProcessLog != nil {
		t.onProcessLog(ProcessLogEntry{
			TaskID:     task.ID,
			InputPath:  task.InputPath,
			OutputPath: task.OutputPath,
			Status:     "failed",
			Error:      err.Error(),
			StartTime:  startedAt,
			EndTime:    task.CompletedAt,
		})
	}

	log.Printf("[transcoder] 任务失败: %s, 错误: %v", task.InputPath, err)
}

func (t *Transcoder) isTaskCancelled(task *TranscodeTask) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return task.Status == StatusCancelled
}

// writeErrorLog 将错误信息写入日志文件
// 返回日志文件路径，如果写入失败返回空字符串
func (t *Transcoder) writeErrorLog(task *TranscodeTask, err error) string {
	// 获取可执行文件所在目录，日志统一存放在可执行文件目录下的 logs 子目录
	exePath, exeErr := os.Executable()
	if exeErr != nil {
		log.Printf("[transcoder] 无法获取可执行文件路径: %v", exeErr)
		return ""
	}
	exeDir := filepath.Dir(exePath)
	logDir := filepath.Join(exeDir, "logs")

	// 创建日志目录
	if mkErr := os.MkdirAll(logDir, 0755); mkErr != nil {
		log.Printf("[transcoder] 创建错误日志目录失败: %v", mkErr)
		return ""
	}

	// 生成日志文件名: {任务ID}_{时间戳}_error.log
	timestamp := time.Now().Format("20060102_150405")
	logFileName := fmt.Sprintf("%s_%s_error.log", task.ID, timestamp)
	logPath := filepath.Join(logDir, logFileName)

	// 构建日志内容
	var content strings.Builder
	content.WriteString("===========================================\n")
	content.WriteString("转码任务错误日志\n")
	content.WriteString("===========================================\n\n")
	content.WriteString(fmt.Sprintf("任务ID: %s\n", task.ID))
	content.WriteString(fmt.Sprintf("输入文件: %s\n", task.InputPath))
	content.WriteString(fmt.Sprintf("输出文件: %s\n", task.OutputPath))
	content.WriteString(fmt.Sprintf("创建时间: %s\n", task.CreatedAt.Format("2006-01-02 15:04:05")))
	content.WriteString(fmt.Sprintf("开始时间: %s\n", task.StartedAt.Format("2006-01-02 15:04:05")))
	content.WriteString(fmt.Sprintf("失败时间: %s\n", task.CompletedAt.Format("2006-01-02 15:04:05")))
	content.WriteString(fmt.Sprintf("视频信息: %dx%d, %.2f fps, %d 帧\n", task.Width, task.Height, task.FrameRate, task.TotalFrames))
	content.WriteString(fmt.Sprintf("处理进度: %.1f%% (%d/%d 帧)\n", task.Progress, task.ProcessedFrame, task.TotalFrames))
	content.WriteString(fmt.Sprintf("FFmpeg 参数: %s\n", task.Config.CustomArgs))
	content.WriteString("\n===========================================\n")
	content.WriteString("错误信息\n")
	content.WriteString("===========================================\n\n")
	content.WriteString(err.Error())
	content.WriteString("\n")

	// 写入文件
	if writeErr := os.WriteFile(logPath, []byte(content.String()), 0644); writeErr != nil {
		log.Printf("[transcoder] 写入错误日志文件失败: %v", writeErr)
		return ""
	}

	log.Printf("[transcoder] 错误日志已保存: %s", logPath)
	return logPath
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

	// 输入选项（如 -hwaccel qsv -hwaccel_output_format qsv），必须放在 -i 之前
	if inputArgs := strings.TrimSpace(task.Config.InputArgs); inputArgs != "" {
		args = append(args, strings.Fields(inputArgs)...)
	}

	args = append(args, "-i", task.InputPath)

	hasCover := videoInfo != nil && videoInfo.HasCover && videoInfo.CoverIndex >= 0

	// 帧率上限过滤
	fpsFilter := buildFPSFilter(task, videoInfo, task.Config.MaxFPS)
	customArgs := strings.Fields(task.Config.CustomArgs)
	if fpsFilter != "" {
		customArgs = mergeVideoFilter(customArgs, fpsFilter)
	}

	if hasCover {
		// 源文件包含封面流：将封面流一并映射到输出
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
		args = append(args, "-map", fmt.Sprintf("0:%d", videoInfo.CoverIndex))

		// 将 -c:v 等无索引选择器规范为 :v:0，避免误伤封面流（封面流使用 copy 编码器）
		processedArgs := normalizeVideoStreamSelectors(customArgs)
		args = append(args, processedArgs...)

		// 封面流保持 copy 并标记为 attached_pic
		args = append(args, "-c:v:1", "copy")
		args = append(args, "-disposition:v:1", "attached_pic")
	} else {
		// 无封面流：仅映射视频和音频
		args = append(args, "-map", "0:v:0")
		args = append(args, "-map", "0:a:0?")
		args = append(args, customArgs...)
	}

	args = append(args, task.OutputPath)

	// 不使用 -progress pipe:1，改为直接解析 stderr 输出
	return args
}

// buildFPSFilter 根据源帧率与上限构建 fps 过滤器字符串
// 不需要限帧时返回空字符串
// sourceFPS 优先取 task.FrameRate，回退到 videoInfo.FrameRate
func buildFPSFilter(task *TranscodeTask, videoInfo *VideoFile, maxFPS float64) string {
	if maxFPS <= 0 {
		return ""
	}
	sourceFPS := task.FrameRate
	if sourceFPS <= 0 && videoInfo != nil {
		sourceFPS = videoInfo.FrameRate
	}
	if sourceFPS <= 0 {
		log.Printf("[transcoder] 警告: 无法获取源帧率，跳过帧率限制 (设定上限: %.2f fps)", maxFPS)
		return ""
	}
	if sourceFPS <= maxFPS {
		log.Printf("[transcoder] 不需要帧率限制: 源 %.2f fps <= 设定上限 %.2f fps", sourceFPS, maxFPS)
		return ""
	}
	log.Printf("[transcoder] 应用帧率限制: 源 %.2f fps > 目标 %.2f fps，将降低帧率", sourceFPS, maxFPS)
	return fmt.Sprintf("fps=fps=%v", maxFPS)
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

	// 按任务保存命令引用，确保并发任务可独立取消。
	t.setCurrentCommand(task.ID, cmd)
	defer t.clearCurrentCommand(task.ID, cmd)

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
						isMainPool := task.ExecutionPool == "main"
						t.mu.Unlock()

						// 仅用主池样本更新主池速度预测。
						if isMainPool {
							t.updateAvgFPS(fps, task.Width, task.Height)
						}
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
				// 检查是否处于暂停状态
				t.pauseMu.RLock()
				isPaused := t.transcodePaused
				t.pauseMu.RUnlock()

				if isPaused {
					// 暂停状态下重置计时器，不计入超时
					frameMu.Lock()
					lastFrameTime = time.Now()
					frameMu.Unlock()
					continue
				}

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
						// 进程被杀死时清除暂停状态
						t.pauseMu.Lock()
						t.transcodePaused = false
						t.pauseMu.Unlock()
					}
					return
				}

				// 如果还没开始处理帧，给更长的初始化时间（2分钟）
				if currentFrame == 0 && elapsed > 2*time.Minute {
					log.Printf("[transcoder] 警告: FFmpeg 超过 2 分钟未开始处理，正在终止进程")
					if cmd.Process != nil {
						cmd.Process.Kill()
						// 进程被杀死时清除暂停状态
						t.pauseMu.Lock()
						t.transcodePaused = false
						t.pauseMu.Unlock()
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
	// 检查暂停状态
	t.pauseMu.RLock()
	isPaused := t.transcodePaused
	totalPausedTime := t.transcodeTotalPausedTime
	// 如果当前处于暂停状态，加上当前暂停持续时间
	if isPaused && !t.transcodePausedTime.IsZero() {
		totalPausedTime += time.Since(t.transcodePausedTime)
	}
	t.pauseMu.RUnlock()

	// 更新已用时间（排除暂停时长）
	if !task.StartedAt.IsZero() {
		totalElapsed := time.Since(task.StartedAt)
		effectiveElapsed := totalElapsed - totalPausedTime
		if effectiveElapsed < 0 {
			effectiveElapsed = 0
		}
		task.ElapsedSeconds = effectiveElapsed.Seconds()
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

	// 如果处于暂停状态，显示"已暂停"
	if isPaused {
		task.ETAString = "已暂停"
		return
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

// getDynamicPredictedFPS 获取动态预测FPS（基于实测速度）
// 优先使用上一个完成任务的速度，其次使用滑动平均；无任何实测数据时返回 0
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
	// 无实测数据，返回 0 表示无法预测
	return 0
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

	wasProcessing := task.Status == StatusProcessing
	task.Status = StatusCancelled
	task.CompletedAt = time.Now()
	t.mu.Unlock()

	if wasProcessing {
		// 只终止该任务自己的 FFmpeg 进程。
		t.currentCmdMu.Lock()
		cmd := t.currentCmds[taskID]
		t.currentCmdMu.Unlock()
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}

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

	// 计算有效线程数（使用目标并发数，而非历史最大值）
	t.workersMu.Lock()
	effectiveWorkers := t.targetWorkers
	t.workersMu.Unlock()
	if effectiveWorkers < 1 {
		effectiveWorkers = 1
	}

	// 实际并行度 = min(总任务数, 并发路数)
	// 任务数少于并发数时，多余 worker 闲置，不应按满并发折算
	totalTasks := pendingCount + processingCount
	actualParallelism := totalTasks
	if actualParallelism > effectiveWorkers {
		actualParallelism = effectiveWorkers
	}
	if actualParallelism < 1 {
		actualParallelism = 1
	}

	// 总剩余时间 = (待处理任务预计时间之和 + 处理中任务剩余时间之和) ÷ 实际并行度
	totalRemaining := (pendingTotalTime + processingTotalETA) / float64(actualParallelism)

	return GlobalStatus{
		TotalRemainingSeconds: totalRemaining,
		TotalRemainingString:  formatETADuration(totalRemaining),
		PendingCount:          pendingCount,
		ProcessingCount:       processingCount,
	}
}

// ================== 转码暂停功能 ==================

// TranscodePauseStatus 转码暂停状态
type TranscodePauseStatus struct {
	Paused            bool   `json:"paused"`            // 是否立即暂停
	PauseAfterCurrent bool   `json:"pauseAfterCurrent"` // 是否当前任务后暂停
	PausedTimeStr     string `json:"pausedTimeStr"`     // 暂停时长字符串
}

// PauseTranscode 立即暂停当前转码进程
func (t *Transcoder) PauseTranscode() error {
	t.pauseMu.Lock()
	defer t.pauseMu.Unlock()

	if t.transcodePaused {
		return nil // 已经暂停
	}

	commands := t.currentCommands()
	if len(commands) == 0 {
		return fmt.Errorf("没有正在运行的转码进程")
	}

	suspended := make([]*exec.Cmd, 0, len(commands))
	for _, cmd := range commands {
		if cmd == nil || cmd.Process == nil {
			continue
		}
		if err := suspendProcess(cmd); err != nil {
			for _, previous := range suspended {
				_ = resumeProcess(previous)
			}
			log.Printf("[transcoder] 暂停 FFmpeg 进程失败: %v", err)
			return fmt.Errorf("暂停进程失败: %w", err)
		}
		suspended = append(suspended, cmd)
	}

	t.transcodePaused = true
	t.transcodePausedTime = time.Now()
	log.Printf("[transcoder] 已暂停 %d 个转码进程", len(suspended))

	return nil
}

// ResumeTranscode 恢复转码进程
func (t *Transcoder) ResumeTranscode() error {
	t.pauseMu.Lock()
	defer t.pauseMu.Unlock()

	if !t.transcodePaused {
		return nil // 未暂停
	}

	commands := t.currentCommands()
	if len(commands) == 0 {
		// 进程已不存在，清除暂停状态
		t.transcodePaused = false
		t.transcodePausedTime = time.Time{}
		t.transcodeTotalPausedTime = 0
		log.Println("[transcoder] 转码进程已不存在，已清除暂停状态")
		return fmt.Errorf("转码进程已不存在")
	}

	resumed := 0
	for _, cmd := range commands {
		if cmd == nil || cmd.Process == nil {
			continue
		}
		if err := resumeProcess(cmd); err != nil {
			log.Printf("[transcoder] 恢复 FFmpeg 进程失败: %v", err)
			return fmt.Errorf("恢复进程失败: %w", err)
		}
		resumed++
	}

	// 累加暂停时长
	t.transcodeTotalPausedTime += time.Since(t.transcodePausedTime)
	t.transcodePaused = false
	t.transcodePausedTime = time.Time{}

	log.Printf("[transcoder] 已恢复 %d 个转码进程", resumed)
	return nil
}

// PauseTranscodeAfterCurrent 设置当前任务后暂停标志
func (t *Transcoder) PauseTranscodeAfterCurrent() {
	t.pauseMu.Lock()
	defer t.pauseMu.Unlock()

	t.transcodePauseAfterCurrent = true
	log.Println("[transcoder] 已设置：当前任务完成后暂停转码")
}

// CancelPauseTranscodeAfterCurrent 取消当前任务后暂停
func (t *Transcoder) CancelPauseTranscodeAfterCurrent() {
	t.pauseMu.Lock()
	defer t.pauseMu.Unlock()

	t.transcodePauseAfterCurrent = false
	log.Println("[transcoder] 已取消：当前任务后暂停转码")
}

// GetTranscodePauseStatus 获取转码暂停状态
func (t *Transcoder) GetTranscodePauseStatus() TranscodePauseStatus {
	t.pauseMu.RLock()
	defer t.pauseMu.RUnlock()

	status := TranscodePauseStatus{
		Paused:            t.transcodePaused,
		PauseAfterCurrent: t.transcodePauseAfterCurrent,
	}

	if t.transcodePaused && !t.transcodePausedTime.IsZero() {
		duration := time.Since(t.transcodePausedTime)
		status.PausedTimeStr = formatTranscodePauseDuration(duration)
	}

	return status
}

// IsTranscodePaused 检查转码是否暂停
func (t *Transcoder) IsTranscodePaused() bool {
	t.pauseMu.RLock()
	defer t.pauseMu.RUnlock()
	return t.transcodePaused
}

// ShouldPauseAfterCurrentTranscode 检查是否需要在当前任务后暂停
func (t *Transcoder) ShouldPauseAfterCurrentTranscode() bool {
	t.pauseMu.RLock()
	defer t.pauseMu.RUnlock()
	return t.transcodePauseAfterCurrent
}

// GetTranscodeTotalPausedTime 获取累计暂停时长
func (t *Transcoder) GetTranscodeTotalPausedTime() time.Duration {
	t.pauseMu.RLock()
	defer t.pauseMu.RUnlock()
	return t.transcodeTotalPausedTime
}

// ResetTranscodePausedTime 重置暂停时长（任务开始时调用）
func (t *Transcoder) ResetTranscodePausedTime() {
	t.pauseMu.Lock()
	defer t.pauseMu.Unlock()
	t.transcodeTotalPausedTime = 0
}

// formatTranscodePauseDuration 格式化暂停时长
func formatTranscodePauseDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d分%d秒", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d小时%d分", int(d.Hours()), int(d.Minutes())%60)
}

// ==========================================================================
// 分段转码（QSV 滤镜链重初始化失败的回退方案）
// ==========================================================================

// isQSVReinitError 检测是否为 QSV 滤镜链重初始化失败
// 当视频流中分辨率发生变化时，QSV 硬件表面格式无法重新初始化滤镜链，
// FFmpeg 报错 "Error reinitializing filters" + "Function not implemented" (-40)
func isQSVReinitError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "reinitializing filters") &&
		(strings.Contains(s, "function not implemented") ||
			strings.Contains(s, "0xffffffd8") ||
			strings.Contains(s, "error code -40"))
}

// resolutionSegment 表示一个分辨率一致的片段
type resolutionSegment struct {
	startTime float64 // 段开始时间（秒）
	endTime   float64 // 段结束时间（秒）
	width     int
	height    int
}

type resolutionProbeFrame struct {
	BestEffortTimestampTime string `json:"best_effort_timestamp_time"`
	PTSTime                 string `json:"pts_time"`
	Width                   int    `json:"width"`
	Height                  int    `json:"height"`
}

type resolutionProbeOutput struct {
	Frames []resolutionProbeFrame `json:"frames"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

type resolutionKeyframe struct {
	timestamp float64
	width     int
	height    int
}

// parseResolutionSegments 将 ffprobe 的绝对时间戳转换为相对文件起点的片段。
// TS/FLV 等容器经常从非零 PTS 开始，而 format.duration 始终是时长；两者不能直接混用。
func parseResolutionSegments(output []byte, totalDuration float64) ([]resolutionSegment, error) {
	var probe resolutionProbeOutput
	if err := json.Unmarshal(output, &probe); err != nil {
		return nil, fmt.Errorf("解析 ffprobe 关键帧输出失败: %w", err)
	}

	keyframes := make([]resolutionKeyframe, 0, len(probe.Frames))
	for _, frame := range probe.Frames {
		if frame.Width <= 0 || frame.Height <= 0 {
			continue
		}

		timeText := frame.BestEffortTimestampTime
		if timeText == "" || timeText == "N/A" {
			timeText = frame.PTSTime
		}
		timestamp, err := strconv.ParseFloat(timeText, 64)
		if err != nil {
			continue
		}

		keyframes = append(keyframes, resolutionKeyframe{
			timestamp: timestamp,
			width:     frame.Width,
			height:    frame.Height,
		})
	}
	if len(keyframes) == 0 {
		return nil, fmt.Errorf("未解析到有效关键帧信息")
	}

	sort.Slice(keyframes, func(i, j int) bool {
		return keyframes[i].timestamp < keyframes[j].timestamp
	})

	if totalDuration <= 0 && probe.Format.Duration != "" {
		totalDuration, _ = strconv.ParseFloat(probe.Format.Duration, 64)
	}
	if totalDuration <= 0 {
		return nil, fmt.Errorf("无法确定视频总时长")
	}

	const timestampEpsilon = 0.001
	origin := keyframes[0].timestamp
	current := resolutionSegment{
		startTime: 0,
		width:     keyframes[0].width,
		height:    keyframes[0].height,
	}
	segments := make([]resolutionSegment, 0, 4)

	for _, frame := range keyframes[1:] {
		if frame.width == current.width && frame.height == current.height {
			continue
		}

		boundary := frame.timestamp - origin
		if boundary <= current.startTime+timestampEpsilon {
			// 同一时间戳不能生成零时长片段，以最后报告的分辨率为准。
			current.width = frame.width
			current.height = frame.height
			continue
		}
		if boundary >= totalDuration-timestampEpsilon {
			// 文件尾部或时长之外的参数变化不构成可转码片段。
			continue
		}

		current.endTime = boundary
		segments = append(segments, current)
		current = resolutionSegment{
			startTime: boundary,
			width:     frame.width,
			height:    frame.height,
		}
	}

	current.endTime = totalDuration
	if current.endTime-current.startTime <= timestampEpsilon {
		return nil, fmt.Errorf("最后一个分辨率片段时长无效: %.3fs - %.3fs", current.startTime, current.endTime)
	}
	segments = append(segments, current)
	return segments, nil
}

// findResolutionSegments 使用 ffprobe 扫描关键帧，检测分辨率变化点
// 返回按时间排序的分辨率段列表
func (t *Transcoder) findResolutionSegments(inputPath string, totalDuration float64) ([]resolutionSegment, error) {
	// 使用 -skip_frame nokey 只扫描关键帧，大幅提升速度
	args := []string{
		"-v", "quiet",
		"-select_streams", "v:0",
		"-skip_frame", "nokey",
		"-show_entries", "frame=best_effort_timestamp_time,pts_time,width,height:format=duration",
		"-of", "json",
		inputPath,
	}

	parentCtx := t.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, t.ffprobePath, args...)
	hideWindow(cmd)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe 扫描关键帧失败: %w", err)
	}

	return parseResolutionSegments(output, totalDuration)
}

// runFFmpegSimple 执行 FFmpeg 命令（无进度跟踪，用于分段转码）
func (t *Transcoder) runFFmpegSimple(taskID string, args []string) error {
	ctx := t.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, t.ffmpegPath, args...)
	hideWindow(cmd)

	t.setCurrentCommand(taskID, cmd)
	defer t.clearCurrentCommand(taskID, cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		lastLines := getLastLines(string(output), 15)
		return fmt.Errorf("%w\n--- FFmpeg 输出 (最后 15 行) ---\n%s", err, lastLines)
	}
	return nil
}

func buildSegmentTranscodeArgs(task *TranscodeTask, videoInfo *VideoFile, seg resolutionSegment, outputPath string) []string {
	args := []string{"-y"}
	if inputArgs := strings.TrimSpace(task.Config.InputArgs); inputArgs != "" {
		args = append(args, strings.Fields(inputArgs)...)
	}
	if seg.startTime > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.6f", seg.startTime))
	}
	args = append(args, "-i", task.InputPath)
	args = append(args, "-t", fmt.Sprintf("%.6f", seg.endTime-seg.startTime))
	args = append(args, "-map", "0:v:0", "-map", "0:a:0?")

	customArgs := strings.Fields(task.Config.CustomArgs)
	if fpsFilter := buildFPSFilter(task, videoInfo, task.Config.MaxFPS); fpsFilter != "" {
		customArgs = mergeVideoFilter(customArgs, fpsFilter)
	}
	args = append(args, customArgs...)
	args = append(args, "-avoid_negative_ts", "make_zero", outputPath)
	return args
}

// transcodeWithSegments 分段转码：在分辨率变化点切分视频，输出独立文件
// 当 QSV 滤镜链因分辨率变化无法重初始化时作为回退方案
func (t *Transcoder) transcodeWithSegments(task *TranscodeTask, videoInfo *VideoFile) error {
	log.Printf("[transcoder] 分段转码: 开始，输入文件=%s", task.InputPath)

	// 1. 检测分辨率变化段
	segments, err := t.findResolutionSegments(task.InputPath, task.Duration)
	if err != nil {
		return fmt.Errorf("检测分辨率变化失败: %w", err)
	}

	if len(segments) <= 1 {
		return fmt.Errorf("未检测到分辨率变化，分段转码不适用")
	}

	log.Printf("[transcoder] 检测到 %d 个分辨率段:", len(segments))
	for i, seg := range segments {
		log.Printf("[transcoder]   段 %d: %dx%d, %.1fs - %.1fs (时长 %.0fs)",
			i+1, seg.width, seg.height, seg.startTime, seg.endTime, seg.endTime-seg.startTime)
	}

	// 2. 在输出目录创建临时目录，确保后续 Rename 不跨磁盘。
	tempDir, err := os.MkdirTemp(filepath.Dir(task.OutputPath), ".transcode-segments-*")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tempDir)

	totalSegments := len(segments)
	const copyExt = ".mkv"
	completedDuration := 0.0
	totalDuration := segments[len(segments)-1].endTime
	outputExt := filepath.Ext(task.OutputPath)
	if outputExt == "" {
		outputExt = ".mkv"
	}
	outputBase := strings.TrimSuffix(task.OutputPath, filepath.Ext(task.OutputPath))
	if outputBase == "" {
		outputBase = task.OutputPath
	}

	videoMap := "0:v:0"
	audioMap := "0:a:0?"
	if videoInfo != nil && videoInfo.VideoIndex >= 0 {
		videoMap = fmt.Sprintf("0:%d", videoInfo.VideoIndex)
	}
	if videoInfo != nil && videoInfo.AudioIndex >= 0 {
		audioMap = fmt.Sprintf("0:%d", videoInfo.AudioIndex)
	}
	stagedOutputs := make([]string, 0, totalSegments)

	// 3. 逐段：流复制提取 → QSV 转码 → 输出独立文件
	for i, seg := range segments {
		if t.ctx != nil && t.ctx.Err() != nil {
			return fmt.Errorf("任务已取消")
		}
		segmentDuration := seg.endTime - seg.startTime
		if segmentDuration <= 0 {
			return fmt.Errorf("片段 %d 时长无效: %.3fs - %.3fs", i+1, seg.startTime, seg.endTime)
		}

		// 3a. 流复制提取。变化点来自关键帧，使用输入级 seek 可避免非零 PTS 偏移。
		segCopy := filepath.Join(tempDir, fmt.Sprintf("copy_%04d%s", i, copyExt))
		copyArgs := []string{"-y"}
		if seg.startTime > 0 {
			copyArgs = append(copyArgs, "-ss", fmt.Sprintf("%.6f", seg.startTime))
		}
		copyArgs = append(copyArgs,
			"-i", task.InputPath,
			"-t", fmt.Sprintf("%.6f", segmentDuration),
			"-map", videoMap, "-map", audioMap,
			"-c", "copy", "-avoid_negative_ts", "make_zero", segCopy)

		log.Printf("[transcoder] 流复制提取段 %d/%d: %dx%d, %.3fs - %.3fs",
			i+1, totalSegments, seg.width, seg.height, seg.startTime, seg.endTime)
		if err := t.runFFmpegSimple(task.ID, copyArgs); err != nil {
			return fmt.Errorf("提取段 %d 失败: %w", i+1, err)
		}

		// 3b. QSV 转码到临时目录，全部成功后再发布为 _1、_2、_3...
		segOutput := filepath.Join(tempDir, fmt.Sprintf("segment_%04d%s", i+1, outputExt))

		transcodeArgs := []string{"-y"}
		if inputArgs := strings.TrimSpace(task.Config.InputArgs); inputArgs != "" {
			transcodeArgs = append(transcodeArgs, strings.Fields(inputArgs)...)
		}
		transcodeArgs = append(transcodeArgs, "-i", segCopy)
		transcodeArgs = append(transcodeArgs, "-map", "0:v:0", "-map", "0:a:0?")

		// 封面处理
		if videoInfo != nil && videoInfo.HasCover && videoInfo.CoverIndex >= 0 {
			transcodeArgs = append(transcodeArgs,
				"-i", task.InputPath,
				"-map", fmt.Sprintf("1:%d", videoInfo.CoverIndex),
				"-c:v:1", "copy", "-disposition:v:1", "attached_pic")
		}

		customArgs := strings.Fields(task.Config.CustomArgs)
		if fpsFilter := buildFPSFilter(task, videoInfo, task.Config.MaxFPS); fpsFilter != "" {
			customArgs = mergeVideoFilter(customArgs, fpsFilter)
		}
		if videoInfo != nil && videoInfo.HasCover && videoInfo.CoverIndex >= 0 {
			customArgs = normalizeVideoStreamSelectors(customArgs)
		}
		transcodeArgs = append(transcodeArgs, customArgs...)
		transcodeArgs = append(transcodeArgs, "-avoid_negative_ts", "make_zero", segOutput)

		log.Printf("[transcoder] 转码段 %d/%d: %dx%d -> %s",
			i+1, totalSegments, seg.width, seg.height, segOutput)
		if err := t.runFFmpegSimple(task.ID, transcodeArgs); err != nil {
			return fmt.Errorf("转码段 %d 失败: %w", i+1, err)
		}
		stagedOutputs = append(stagedOutputs, segOutput)

		// 按已完成时长更新进度
		completedDuration += segmentDuration
		t.mu.Lock()
		task.Progress = completedDuration / totalDuration * 100
		if task.Progress > 99 {
			task.Progress = 99
		}
		task.ProcessedFrame = int64(completedDuration / totalDuration * float64(task.TotalFrames))
		t.mu.Unlock()
	}

	// 更新进度为 100%
	t.mu.Lock()
	task.Progress = 100
	task.ProcessedFrame = task.TotalFrames
	t.mu.Unlock()

	// 所有分段成功后才发布到最终目录，避免失败时留下半成品。
	finalOutputs := make([]string, totalSegments)
	for i, stagedPath := range stagedOutputs {
		finalPath := fmt.Sprintf("%s_%d%s", outputBase, i+1, outputExt)
		if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理旧分段 %d 失败: %w", i+1, err)
		}
		if err := os.Rename(stagedPath, finalPath); err != nil {
			return fmt.Errorf("发布分段 %d 失败: %w", i+1, err)
		}
		finalOutputs[i] = finalPath
	}
	// 初始 QSV 尝试可能在主输出路径留下半成品；独立分段发布后删除它。
	if task.OutputPath != finalOutputs[0] {
		if err := os.Remove(task.OutputPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理原始输出半成品失败: %w", err)
		}
	}
	t.mu.Lock()
	task.OutputPath = finalOutputs[0]
	t.mu.Unlock()

	log.Printf("[transcoder] 分段转码完成: %d 段 -> %s ...", totalSegments, finalOutputs[0])
	return nil
}

// transcodeWithNV12Fallback 使用 NV12 软件滤镜链重试转码
// 当 QSV 硬件表面格式导致滤镜链无法重初始化时，强制使用 QSV 解码并输出
// 系统内存中的 NV12 帧，使软件滤镜可处理分辨率变化。
// 编码器保持用户配置（如 h264_qsv），仅滤镜链改用软件处理。
func (t *Transcoder) transcodeWithNV12Fallback(task *TranscodeTask, videoInfo *VideoFile) error {
	log.Printf("[transcoder] NV12 回退: 开始，原 InputArgs=%q", task.Config.InputArgs)

	newInputArgs := strings.Join(buildQSVNV12InputArgs(task.Config.InputArgs), " ")

	log.Printf("[transcoder] NV12 回退: InputArgs 从 %q 调整为 %q", task.Config.InputArgs, newInputArgs)

	// 复制任务配置，使用调整后的 InputArgs
	fallbackTask := *task
	fallbackTask.Config.InputArgs = newInputArgs

	// 重置原任务进度，避免前端显示旧的失败进度。
	t.mu.Lock()
	task.Progress = 0
	task.ProcessedFrame = 0
	t.mu.Unlock()

	// 构建新的 FFmpeg 参数并执行
	args := t.buildFFmpegArgs(&fallbackTask, videoInfo)
	log.Printf("[transcoder] NV12 回退: 重新执行转码，参数: %v", args)
	// 使用原任务执行，让进度、当前时间和速度更新回传到任务对象。
	err := t.executeFFmpeg(task, args)
	if err != nil {
		log.Printf("[transcoder] NV12 回退: 失败: %v", err)
	} else {
		log.Printf("[transcoder] NV12 回退: 成功")
	}
	return err
}

// buildQSVNV12InputArgs 保留设备选择等输入参数，但强制 Intel QSV 解码并输出 NV12。
// 这样回退路径不会悄悄退成 CPU 软件解码。
func buildQSVNV12InputArgs(inputArgs string) []string {
	original := strings.Fields(strings.TrimSpace(inputArgs))
	result := make([]string, 0, len(original)+4)
	hasHWAccel := false
	hasOutputFormat := false

	for i := 0; i < len(original); i++ {
		switch original[i] {
		case "-hwaccel":
			result = append(result, "-hwaccel", "qsv")
			hasHWAccel = true
			if i+1 < len(original) {
				i++
			}
		case "-hwaccel_output_format":
			result = append(result, "-hwaccel_output_format", "nv12")
			hasOutputFormat = true
			if i+1 < len(original) {
				i++
			}
		default:
			result = append(result, original[i])
		}
	}

	if !hasHWAccel {
		result = append([]string{"-hwaccel", "qsv"}, result...)
	}
	if !hasOutputFormat {
		result = append(result, "-hwaccel_output_format", "nv12")
	}
	return result
}
