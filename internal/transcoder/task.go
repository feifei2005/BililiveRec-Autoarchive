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
	ID           string          `json:"id"`           // 任务ID
	SeqNum       int64           `json:"seqNum"`       // 任务序号，用于排序（按添加顺序）
	InputPath    string          `json:"inputFile"`    // 输入文件路径
	OutputPath   string          `json:"outputPath"`   // 输出文件路径
	Config       TranscodeConfig `json:"config"`       // 转码配置
	Status       TaskStatus      `json:"status"`       // 任务状态
	Progress     float64         `json:"progress"`     // 进度 (0-100)，基于已处理帧数/总帧数
	Duration     float64         `json:"duration"`     // 视频总时长（秒）
	CurrentTime  float64         `json:"currentTime"`  // 当前处理时间（秒）
	Speed        string          `json:"speed"`        // 处理速度字符串（如 "1.5x"）
	Error        string          `json:"error"`        // 错误信息（包含 FFmpeg 详细输出）
	ErrorLogPath string          `json:"errorLogPath"` // 错误日志文件路径（失败时生成）
	CreatedAt    time.Time       `json:"createdAt"`    // 创建时间
	StartedAt    time.Time       `json:"startedAt"`    // 开始时间
	CompletedAt  time.Time       `json:"completedAt"`  // 完成时间

	// 已用时间
	ElapsedSeconds float64 `json:"elapsedSeconds"` // 已用时间（秒）
	ElapsedString  string  `json:"elapsedString"`  // 已用时间格式化（如"5分32秒"）

	// 基于像素处理速度的进度估算字段
	Width          int     `json:"width"`          // 视频宽度
	Height         int     `json:"height"`         // 视频高度
	FrameRate      float64 `json:"frameRate"`      // 视频帧率
	TotalFrames    int64   `json:"totalFrames"`    // 总帧数
	ProcessedFrame int64   `json:"processedFrame"` // 已处理帧数
	CurrentFPS     float64 `json:"currentFPS"`     // 当前处理速度（帧/秒）
	ETASeconds     float64 `json:"etaSeconds"`     // 预计剩余时间（秒）
	ETAString      string  `json:"etaString"`      // 预计剩余时间（格式化字符串）

	// 智能预测
	PredictedFPS        float64 `json:"predictedFPS"`        // 预测的处理速度（帧/秒），基于分辨率和帧率
	PredictedTotalTime  float64 `json:"predictedTotalTime"`  // 预测的总处理时间（秒）
	PredictedTimeString string  `json:"predictedTimeString"` // 预测的总处理时间格式化字符串
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

	// MaxFPS 帧率上限，0 表示不限制
	// 当源视频帧率高于此值时，会应用 fps 过滤器降低帧率
	// 无论此值为多少，均会启用可变帧率（VFR）模式
	MaxFPS float64 `json:"max_fps" yaml:"max_fps"`
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
	FrameRate   float64 `json:"frameRate"`   // 视频帧率
	TotalFrames int64   `json:"totalFrames"` // 总帧数
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
