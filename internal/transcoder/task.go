// Package transcoder 提供视频转码功能
package transcoder

import (
	"time"
)

// TaskStatus 转码任务状态
type TaskStatus string

const (
	StatusPending    TaskStatus = "pending"    // 等待处理
	StatusProcessing TaskStatus = "processing" // 处理中
	StatusSuccess    TaskStatus = "success"    // 已成功
	StatusFailed     TaskStatus = "failed"     // 处理失败
	StatusCancelled  TaskStatus = "cancelled"  // 已取消
)

// TranscodeTask 转码任务
type TranscodeTask struct {
	ID          string          // 任务ID
	InputPath   string          // 输入文件路径
	OutputPath  string          // 输出文件路径
	Config      TranscodeConfig // 转码配置
	Status      TaskStatus      // 任务状态
	Progress    float64         // 进度 (0-100)
	Duration    float64         // 视频总时长（秒）
	CurrentTime float64         // 当前处理时间（秒）
	Speed       string          // 处理速度
	Error       string          // 错误信息（包含 FFmpeg 详细输出）
	CreatedAt   time.Time       // 创建时间
	StartedAt   time.Time       // 开始时间
	CompletedAt time.Time       // 完成时间
}

// TranscodeConfig 用户自定义的转码参数
type TranscodeConfig struct {
	// CustomArgs 用户直接输入的 FFmpeg 参数字符串
	// 例如: "-c:v av1_amf -profile:v main -level auto -rc:v cqp -qp_i 130 -qp_p 130 -quality high_quality -c:a libopus -b:a 96k -f mp4"
	// 注意：不需要包含 -i 输入文件和输出文件路径，这些会自动添加
	// 注意：不需要包含封面相关参数，会自动处理
	CustomArgs string `json:"custom_args" yaml:"custom_args"`

	// OutputDir 输出目录，为空则在源文件目录下创建 transcoded 子目录
	OutputDir string `json:"output_dir" yaml:"output_dir"`

	// OutputExt 输出文件扩展名 (如 ".mp4", ".mkv")
	// 如果为空，会尝试从 CustomArgs 中的 -f 参数推断，否则默认 ".mp4"
	OutputExt string `json:"output_ext" yaml:"output_ext"`

	// DeleteSourceOnSuccess 转码成功后是否删除源文件
	DeleteSourceOnSuccess bool `json:"delete_source_on_success" yaml:"delete_source_on_success"`
}

// VideoFile 扫描到的视频文件信息
type VideoFile struct {
	Path        string  `json:"path"`        // 文件路径
	Name        string  `json:"name"`        // 文件名
	Size        int64   `json:"size"`        // 文件大小（字节）
	Duration    float64 `json:"durationSec"` // 时长（秒）
	DurationStr string  `json:"duration"`    // 时长（格式化字符串）
	Width       int     `json:"width"`       // 视频宽度
	Height      int     `json:"height"`      // 视频高度
	Resolution  string  `json:"resolution"`  // 分辨率字符串 (如 "1920x1080")
	VideoCodec  string  `json:"videoCodec"`  // 视频编码
	AudioCodec  string  `json:"audioCodec"`  // 音频编码
	Bitrate     int64   `json:"bitrate"`     // 总比特率
	HasCover    bool    `json:"hasCover"`    // 是否包含封面流
	CoverIndex  int     `json:"coverIndex"`  // 封面流索引（如果有）
	VideoIndex  int     `json:"videoIndex"`  // 主视频流索引
	AudioIndex  int     `json:"audioIndex"`  // 主音频流索引
}

// CoverStreamInfo 封面流信息
type CoverStreamInfo struct {
	Index     int    // 流索引
	CodecName string // 编解码器名称 (mjpeg, png)
	Width     int    // 宽度
	Height    int    // 高度
	Attached  bool   // 是否标记为 attached_pic
}

// DefaultTranscodeConfig 返回默认转码配置
func DefaultTranscodeConfig() TranscodeConfig {
	return TranscodeConfig{
		CustomArgs: "-c:v libx264 -preset medium -crf 23 -c:a aac -b:a 192k -f mp4",
		OutputExt:  ".mp4",
	}
}

// Presets 预设配置（用户可以直接复制这些参数）
var Presets = map[string]string{
	"high_quality": "-c:v libx264 -preset slow -crf 18 -c:a aac -b:a 320k -f mp4",
	"balanced":     "-c:v libx264 -preset medium -crf 23 -c:a aac -b:a 192k -f mp4",
	"small_size":   "-c:v libx264 -preset fast -crf 28 -c:a aac -b:a 128k -f mp4",
	"copy":         "-c:v copy -c:a copy -f mp4",
	"av1_amd":      "-c:v av1_amf -profile:v main -level auto -rc:v cqp -qp_i 130 -qp_p 130 -quality high_quality -c:a libopus -b:a 96k -f mp4",
	"hevc_nvenc":   "-c:v hevc_nvenc -preset p4 -cq 28 -c:a aac -b:a 192k -f mp4",
}
