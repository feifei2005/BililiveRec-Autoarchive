// Package ffmpeg 提供 FFmpeg 封装功能
package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
)

// FFmpeg FFmpeg 操作接口
type FFmpeg interface {
	// Remux 转封装视频文件
	Remux(ctx context.Context, input, output string, opts *RemuxOptions) error

	// Probe 获取媒体文件信息
	Probe(ctx context.Context, path string) (*MediaInfo, error)

	// HasVideoStream 检查文件是否包含视频流
	HasVideoStream(ctx context.Context, path string) (bool, error)
}

// RemuxOptions 转封装选项
type RemuxOptions struct {
	CoverPath  string   // 封面图片路径
	CustomArgs []string // 自定义参数
}

// MediaInfo 媒体文件信息
type MediaInfo struct {
	Duration    float64     // 时长（秒）
	Size        int64       // 文件大小（字节）
	Format      string      // 格式名称
	VideoStream *StreamInfo // 视频流信息
	AudioStream *StreamInfo // 音频流信息
}

// StreamInfo 流信息
type StreamInfo struct {
	Codec      string // 编码格式
	Width      int    // 宽度（仅视频）
	Height     int    // 高度（仅视频）
	Bitrate    int64  // 比特率
	SampleRate int    // 采样率（仅音频）
}

// Config FFmpeg 配置
type Config struct {
	FFmpegPath  string   // ffmpeg 可执行文件路径
	FFprobePath string   // ffprobe 可执行文件路径
	CustomArgs  []string // 默认自定义参数
}

// DefaultFFmpeg 默认 FFmpeg 实现
type DefaultFFmpeg struct {
	config Config
}

// New 创建新的 FFmpeg 实例
func New(cfg Config) *DefaultFFmpeg {
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if cfg.FFprobePath == "" {
		cfg.FFprobePath = "ffprobe"
	}
	return &DefaultFFmpeg{config: cfg}
}

// ffprobeOutput ffprobe JSON 输出结构
type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

// ffprobeStream ffprobe 流信息
type ffprobeStream struct {
	CodecType  string `json:"codec_type"`
	CodecName  string `json:"codec_name"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	SampleRate string `json:"sample_rate,omitempty"`
	BitRate    string `json:"bit_rate,omitempty"`
}

// ffprobeFormat ffprobe 格式信息
type ffprobeFormat struct {
	Filename   string `json:"filename"`
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
	Size       string `json:"size"`
	BitRate    string `json:"bit_rate"`
}

// Remux 转封装视频文件（FLV -> MKV）
// 使用 -c copy 保持原样编码，不进行转码
// 如果提供了封面图片，会将其作为附加图片写入 MKV
func (f *DefaultFFmpeg) Remux(ctx context.Context, input, output string, opts *RemuxOptions) error {
	args := []string{
		"-y",                        // 覆盖输出文件
		"-fflags", "+genpts+igndts", // 重新生成 PTS 并忽略损坏的 DTS
		"-avoid_negative_ts", "make_zero", // 处理负时间戳
		"-i", input, // 输入文件
	}

	hasCover := opts != nil && opts.CoverPath != ""

	// 添加封面作为附件 (MKV 专用方式)
	if hasCover {
		args = append(args, "-attach", opts.CoverPath)

		// 根据文件扩展名设置 MIME 类型
		mimeType := "image/jpeg"
		if strings.HasSuffix(strings.ToLower(opts.CoverPath), ".png") {
			mimeType = "image/png"
		}
		args = append(args, "-metadata:s:t", "mimetype="+mimeType)
	}

	// 映射所有流（从输入文件）
	args = append(args, "-map", "0")

	// 使用 copy 编码（无转码）
	args = append(args, "-c", "copy")

	// 添加自定义参数
	if opts != nil && len(opts.CustomArgs) > 0 {
		args = append(args, opts.CustomArgs...)
	}

	// 添加输出文件
	args = append(args, output)

	// 创建命令
	cmd := exec.CommandContext(ctx, f.config.FFmpegPath, args...)
	hideWindow(cmd)

	// 捕获 stderr 输出（FFmpeg 的主要输出都在 stderr）
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// 执行命令
	err := cmd.Run()
	if err != nil {
		// 返回包含 FFmpeg 错误输出的详细错误信息
		return fmt.Errorf("ffmpeg remux failed: %w\nFFmpeg output:\n%s", err, stderr.String())
	}

	return nil
}

// Probe 获取媒体文件信息
// 使用 ffprobe 以 JSON 格式输出媒体文件的详细信息
func (f *DefaultFFmpeg) Probe(ctx context.Context, path string) (*MediaInfo, error) {
	args := []string{
		"-v", "quiet", // 静默模式，不输出日志
		"-print_format", "json", // 输出 JSON 格式
		"-show_format",  // 显示格式信息
		"-show_streams", // 显示流信息
		path,
	}

	cmd := exec.CommandContext(ctx, f.config.FFprobePath, args...)
	hideWindow(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w\nstderr: %s", err, stderr.String())
	}

	// 解析 JSON 输出
	var output ffprobeOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		return nil, fmt.Errorf("failed to parse ffprobe output: %w\noutput: %s", err, stdout.String())
	}

	// 构建 MediaInfo
	info := &MediaInfo{
		Format: output.Format.FormatName,
	}

	// 解析时长
	if output.Format.Duration != "" {
		fmt.Sscanf(output.Format.Duration, "%f", &info.Duration)
	}

	// 解析文件大小
	if output.Format.Size != "" {
		fmt.Sscanf(output.Format.Size, "%d", &info.Size)
	}

	// 解析流信息
	for _, stream := range output.Streams {
		switch stream.CodecType {
		case "video":
			// 跳过附加图片流（封面）
			if info.VideoStream == nil {
				info.VideoStream = &StreamInfo{
					Codec:  stream.CodecName,
					Width:  stream.Width,
					Height: stream.Height,
				}
				if stream.BitRate != "" {
					fmt.Sscanf(stream.BitRate, "%d", &info.VideoStream.Bitrate)
				}
			}
		case "audio":
			if info.AudioStream == nil {
				info.AudioStream = &StreamInfo{
					Codec: stream.CodecName,
				}
				if stream.SampleRate != "" {
					fmt.Sscanf(stream.SampleRate, "%d", &info.AudioStream.SampleRate)
				}
				if stream.BitRate != "" {
					fmt.Sscanf(stream.BitRate, "%d", &info.AudioStream.Bitrate)
				}
			}
		}
	}

	return info, nil
}

// HasVideoStream 检查文件是否包含有效的视频流
// 使用 ffprobe 检测文件中是否存在有效的视频流（width 和 height 都大于 0）
// 此方法用于过滤无效的录制文件（如纯音频或损坏的文件）
func (f *DefaultFFmpeg) HasVideoStream(ctx context.Context, path string) (bool, error) {
	// 检查 width 和 height，确保视频流有有效的尺寸信息
	args := []string{
		"-v", "quiet",
		"-select_streams", "v:0", // 只选择第一个视频流
		"-show_entries", "stream=width,height", // 获取宽高信息
		"-of", "csv=p=0", // 简单输出格式，输出为 "width,height"
		path,
	}

	cmd := exec.CommandContext(ctx, f.config.FFprobePath, args...)
	hideWindow(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		log.Printf("[ffmpeg] HasVideoStream ffprobe failed for %s: %v, stderr: %s", path, err, stderr.String())
		return false, fmt.Errorf("ffprobe failed: %w\nstderr: %s", err, stderr.String())
	}

	// 解析输出，格式应为 "width,height"，例如 "1920,1080"
	output := strings.TrimSpace(stdout.String())
	log.Printf("[ffmpeg] HasVideoStream ffprobe output for %s: %q", path, output)

	if output == "" {
		log.Printf("[ffmpeg] HasVideoStream: no video stream found in %s", path)
		return false, nil
	}

	// 解析宽高
	parts := strings.Split(output, ",")
	if len(parts) != 2 {
		log.Printf("[ffmpeg] HasVideoStream: invalid output format for %s: %q", path, output)
		return false, nil
	}

	width, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))

	if err1 != nil || err2 != nil {
		log.Printf("[ffmpeg] HasVideoStream: failed to parse dimensions for %s: width=%q height=%q", path, parts[0], parts[1])
		return false, nil
	}

	hasValidVideo := width > 0 && height > 0
	log.Printf("[ffmpeg] HasVideoStream: %s has valid video stream: %v (width=%d, height=%d)", path, hasValidVideo, width, height)

	return hasValidVideo, nil
}

// Version 获取 FFmpeg 版本信息
func (f *DefaultFFmpeg) Version(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, f.config.FFmpegPath, "-version")
	hideWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get ffmpeg version: %w", err)
	}
	// 只返回第一行
	lines := strings.Split(string(output), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0]), nil
	}
	return string(output), nil
}

// FFprobeVersion 获取 ffprobe 版本信息
func (f *DefaultFFmpeg) FFprobeVersion(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, f.config.FFprobePath, "-version")
	hideWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get ffprobe version: %w", err)
	}
	// 只返回第一行
	lines := strings.Split(string(output), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0]), nil
	}
	return string(output), nil
}

// CheckAvailable 检查 FFmpeg 和 ffprobe 是否可用
func (f *DefaultFFmpeg) CheckAvailable(ctx context.Context) error {
	// 检查 ffmpeg
	if _, err := f.Version(ctx); err != nil {
		return fmt.Errorf("ffmpeg not available: %w", err)
	}

	// 检查 ffprobe
	if _, err := f.FFprobeVersion(ctx); err != nil {
		return fmt.Errorf("ffprobe not available: %w", err)
	}

	return nil
}
