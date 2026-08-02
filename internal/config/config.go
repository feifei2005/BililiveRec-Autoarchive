// Package config 提供配置管理功能
package config

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 应用程序配置结构
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Processing ProcessingConfig `yaml:"processing"`
	Rules      RulesConfig      `yaml:"rules"`
	Covers     CoversConfig     `yaml:"covers"`
	FFmpeg     FFmpegConfig     `yaml:"ffmpeg"`
	Transcode  TranscodeConfig  `yaml:"transcode"`
}

// ServerConfig Webhook 服务器配置
type ServerConfig struct {
	BindAddress    string `yaml:"bind_address"` // 留空表示监听所有网卡
	APIToken       string `yaml:"api_token"`    // 外部 HTTP API 的 Bearer Token；留空表示不鉴权
	Port           int    `yaml:"port"`
	WebhookPath    string `yaml:"webhook_path"`
	WebhookEnabled bool   `yaml:"webhook_enabled"` // 是否启用 Webhook 自动添加任务
}

// ProcessingConfig 录制处理配置
type ProcessingConfig struct {
	InputDir           string `yaml:"input_dir"`
	OutputRoot         string `yaml:"output_root"`
	DiscardDir         string `yaml:"discard_dir"`
	MaxConcurrent      int    `yaml:"max_concurrent"`
	MinFileSizeKB      int64  `yaml:"min_file_size_kb"`
	CheckVideoStream   bool   `yaml:"check_video_stream"`
	DiscardFailedFiles bool   `yaml:"discard_failed_files"` // 处理失败时是否进入丢弃流程
	DeleteOriginal     bool   `yaml:"delete_original"`
	ConflictMode       string `yaml:"conflict_mode"`
	ScanIntervalMin    int    `yaml:"scan_interval_min"`
}

// RulesConfig 命名与目录规则配置
type RulesConfig struct {
	StreamerNameRegex string `yaml:"streamer_name_regex"`
	PathTemplate      string `yaml:"path_template"`
}

// CoversConfig 封面设置
type CoversConfig struct {
	DefaultCover string `yaml:"default_cover"`
	SaveHistory  bool   `yaml:"save_history"`
}

// FFmpegConfig FFmpeg 设置
type FFmpegConfig struct {
	Path        string   `yaml:"path"`
	FFprobePath string   `yaml:"ffprobe_path"`
	CustomArgs  []string `yaml:"custom_args"`
}

// TranscodeConfig 转码配置
type TranscodeConfig struct {
	// 默认输出格式 (mp4, mkv)
	DefaultFormat string `yaml:"default_format"`
	// 默认转码参数模板
	DefaultParams string `yaml:"default_params"`
	// FFmpeg 输入选项，放在 -i 之前
	// 例如: "-hwaccel qsv -hwaccel_output_format qsv" 用于启用硬件解码加速
	InputArgs string `yaml:"input_args"`
	// 最大并发转码任务数
	MaxConcurrent int `yaml:"max_concurrent"`
	// 输出目录（为空则输出到源文件同目录）
	OutputDir string `yaml:"output_dir"`
	// 转码完成后是否删除源文件
	DeleteSource bool `yaml:"delete_source"`
	// 帧率上限（0 或空表示不限制）
	// 支持的值: 24, 25, 29.97, 30, 50, 59.94, 60 或自定义值
	MaxFPS float64 `yaml:"max_fps"`
	// 是否保留封面
	PreserveCover bool `yaml:"preserve_cover"`
	// 转码成功后删除源文件
	DeleteSourceOnSuccess bool `yaml:"delete_source_on_success"`
	// QSV 滤镜链重初始化失败的回退策略
	// 当输入视频流分辨率中途变化时，QSV 硬件表面格式无法重新初始化滤镜链，
	// FFmpeg 报错 "Error reinitializing filters" + "Function not implemented"
	// 可选值:
	//   - "nv12" : 移交独立单并发 NV12 池重试（默认）
	//   - "error": 直接失败
	QSVReinitStrategy string `yaml:"qsv_reinit_strategy"`
}

// Load 从指定路径加载配置文件
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Default 返回默认配置
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			BindAddress:    "",
			Port:           8080,
			WebhookPath:    "/webhook",
			WebhookEnabled: true, // 默认启用 Webhook
		},
		Processing: ProcessingConfig{
			InputDir:           "",
			OutputRoot:         "",
			DiscardDir:         "",
			MaxConcurrent:      2,
			MinFileSizeKB:      1024,
			CheckVideoStream:   true,
			DiscardFailedFiles: false, // 默认保留失败文件
			DeleteOriginal:     false,
			ConflictMode:       "skip",
			ScanIntervalMin:    5,
		},
		Rules: RulesConfig{
			StreamerNameRegex: "-([^\\-]+)",
			PathTemplate:      "{{.OutputDir}}\\{{.Streamer}}\\{{.Year}}\\{{.Month}}\\{{.Day}}",
		},
		Covers: CoversConfig{
			DefaultCover: "",
			SaveHistory:  true,
		},
		FFmpeg: FFmpegConfig{
			Path:        "ffmpeg",
			FFprobePath: "ffprobe",
			CustomArgs:  []string{"-c", "copy"},
		},
		Transcode: TranscodeConfig{
			DefaultFormat:         "mp4",
			DefaultParams:         "-c:v av1_qsv -global_quality 23 -look_ahead 1 -c:a aac -b:a 192k",
			InputArgs:             "-hwaccel qsv -hwaccel_output_format qsv",
			MaxConcurrent:         1,
			OutputDir:             "",
			DeleteSource:          false,
			MaxFPS:                0, // 0 表示不限制帧率
			PreserveCover:         true,
			DeleteSourceOnSuccess: false,
			QSVReinitStrategy:     "nv12",
		},
	}
}

// Save 将配置保存到指定路径
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// Validate 验证配置有效性
func (c *Config) Validate() error {
	// TODO: 实现配置验证逻辑
	// - 检查必要目录是否存在
	// - 检查端口号是否有效
	// - 检查正则表达式是否合法
	return nil
}

// FillDefaults 补全缺失的配置字段为默认值
// 这个函数在加载配置后调用，确保所有必要字段都有合理的值
func (c *Config) FillDefaults() {
	defaults := Default()

	// 补全 Server 配置
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		c.Server.Port = defaults.Server.Port
	}
	if c.Server.WebhookPath == "" {
		c.Server.WebhookPath = defaults.Server.WebhookPath
	}

	// 补全 Processing 配置
	if c.Processing.MaxConcurrent <= 0 {
		c.Processing.MaxConcurrent = defaults.Processing.MaxConcurrent
	}
	if c.Processing.MinFileSizeKB < 0 {
		c.Processing.MinFileSizeKB = defaults.Processing.MinFileSizeKB
	}
	if c.Processing.ConflictMode == "" {
		c.Processing.ConflictMode = defaults.Processing.ConflictMode
	}
	if c.Processing.ScanIntervalMin <= 0 {
		c.Processing.ScanIntervalMin = defaults.Processing.ScanIntervalMin
	}

	// 补全 FFmpeg 配置
	if c.FFmpeg.Path == "" {
		c.FFmpeg.Path = defaults.FFmpeg.Path
	}
	if c.FFmpeg.FFprobePath == "" {
		c.FFmpeg.FFprobePath = defaults.FFmpeg.FFprobePath
	}

	// 补全 Transcode 配置
	if c.Transcode.DefaultFormat == "" {
		c.Transcode.DefaultFormat = defaults.Transcode.DefaultFormat
	}
	// 验证输出格式是否有效
	if c.Transcode.DefaultFormat != "mp4" && c.Transcode.DefaultFormat != "mkv" {
		c.Transcode.DefaultFormat = defaults.Transcode.DefaultFormat
	}
	if c.Transcode.DefaultParams == "" {
		c.Transcode.DefaultParams = defaults.Transcode.DefaultParams
	}
	// 迁移项目旧版的默认软件编码和 AMD AV1 配置；用户自定义的其他参数保持不变。
	if c.Transcode.DefaultParams == "-c:v libx264 -preset medium -crf 23 -c:a aac -b:a 192k" ||
		strings.Contains(c.Transcode.DefaultParams, "-c:v av1_amf") {
		c.Transcode.DefaultParams = defaults.Transcode.DefaultParams
	}
	if c.Transcode.InputArgs == "" {
		c.Transcode.InputArgs = defaults.Transcode.InputArgs
	}
	if c.Transcode.MaxConcurrent <= 0 {
		c.Transcode.MaxConcurrent = defaults.Transcode.MaxConcurrent
	}
	// 验证帧率上限是否有效（负数无效）
	if c.Transcode.MaxFPS < 0 {
		c.Transcode.MaxFPS = 0
	}

	// segment 是旧版分段输出策略，升级后迁移到独立 NV12 池。
	switch c.Transcode.QSVReinitStrategy {
	case "error", "nv12":
		// 合法值
	case "segment":
		c.Transcode.QSVReinitStrategy = "nv12"
	default:
		c.Transcode.QSVReinitStrategy = defaults.Transcode.QSVReinitStrategy
	}
}
