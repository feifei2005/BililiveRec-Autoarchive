// Package config 提供配置管理功能
package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Config 应用程序配置结构
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Processing ProcessingConfig `yaml:"processing"`
	Rules      RulesConfig      `yaml:"rules"`
	Covers     CoversConfig     `yaml:"covers"`
	FFmpeg     FFmpegConfig     `yaml:"ffmpeg"`
}

// ServerConfig Webhook 服务器配置
type ServerConfig struct {
	Port        int    `yaml:"port"`
	WebhookPath string `yaml:"webhook_path"`
}

// ProcessingConfig 录制处理配置
type ProcessingConfig struct {
	InputDir         string `yaml:"input_dir"`
	OutputRoot       string `yaml:"output_root"`
	DiscardDir       string `yaml:"discard_dir"`
	MaxConcurrent    int    `yaml:"max_concurrent"`
	MinFileSizeKB    int64  `yaml:"min_file_size_kb"`
	CheckVideoStream bool   `yaml:"check_video_stream"`
	DeleteOriginal   bool   `yaml:"delete_original"`
	ConflictMode     string `yaml:"conflict_mode"`
	ScanIntervalMin  int    `yaml:"scan_interval_min"`
}

// RulesConfig 命名与目录规则配置
type RulesConfig struct {
	StreamerNameRegex string `yaml:"streamer_name_regex"`
	DateRegex         string `yaml:"date_regex"`
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
			Port:        8080,
			WebhookPath: "/webhook",
		},
		Processing: ProcessingConfig{
			InputDir:         "",
			OutputRoot:       "",
			DiscardDir:       "",
			MaxConcurrent:    2,
			MinFileSizeKB:    1024,
			CheckVideoStream: true,
			DeleteOriginal:   false,
			ConflictMode:     "skip",
			ScanIntervalMin:  5,
		},
		Rules: RulesConfig{
			StreamerNameRegex: "-([^\\-]+)",
			DateRegex:         "(\\d{8})",
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
