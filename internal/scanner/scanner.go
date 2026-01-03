// Package scanner 提供文件扫描功能
package scanner

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileInfo 扫描到的文件信息
type FileInfo struct {
	Path      string    // 文件路径
	Name      string    // 文件名
	Size      int64     // 文件大小（字节）
	ModTime   time.Time // 修改时间
	Extension string    // 文件扩展名
}

// FileGroup 表示一组相关联的文件
// 以 .flv 文件为核心，包含对应的 .xml 弹幕文件和封面图片
type FileGroup struct {
	FLVPath      string // FLV 视频文件路径（必须存在）
	XMLPath      string // XML 弹幕文件路径（可选，为空表示不存在）
	CoverPath    string // 封面图片路径（可选，为空表示不存在）
	StreamerDir  string // 主播文件夹名称（如 "1776261556-筱田柴"）
	StreamerName string // 主播名称（从文件夹名解析）
}

// Scanner 文件扫描器接口
type Scanner interface {
	// Start 启动扫描器
	Start(ctx context.Context) error

	// Stop 停止扫描器
	Stop() error

	// ScanNow 立即执行一次扫描
	ScanNow() ([]FileInfo, error)

	// Scan 扫描并返回文件组列表（仅包含未被占用的文件）
	Scan() ([]FileGroup, error)

	// Files 返回扫描到的文件通道
	Files() <-chan FileInfo
}

// Config 扫描器配置
type Config struct {
	InputDir      string        // 扫描目录
	Extensions    []string      // 要匹配的文件扩展名
	ScanInterval  time.Duration // 扫描间隔
	MinFileSize   int64         // 最小文件大小（字节）
	IgnorePattern string        // 忽略的文件名模式
}

// DefaultScanner 默认文件扫描器实现
type DefaultScanner struct {
	config Config
	files  chan FileInfo
	done   chan struct{}
}

// New 创建新的扫描器实例
func New(cfg Config) *DefaultScanner {
	return &DefaultScanner{
		config: cfg,
		files:  make(chan FileInfo, 100),
		done:   make(chan struct{}),
	}
}

// Start 启动扫描器
func (s *DefaultScanner) Start(ctx context.Context) error {
	// TODO: 实现定时扫描逻辑
	// - 定时遍历目录
	// - 过滤符合条件的文件
	// - 发送到 files 通道
	return nil
}

// Stop 停止扫描器
func (s *DefaultScanner) Stop() error {
	close(s.done)
	return nil
}

// ScanNow 立即执行一次扫描
func (s *DefaultScanner) ScanNow() ([]FileInfo, error) {
	// TODO: 实现立即扫描逻辑
	return nil, nil
}

// Files 返回扫描到的文件通道
func (s *DefaultScanner) Files() <-chan FileInfo {
	return s.files
}

// Scan 扫描输入目录，返回未被占用的文件组列表
// 扫描逻辑：
// 1. 遍历 InputDir 下的一级子目录（主播文件夹）
// 2. 在每个主播文件夹中查找 .flv 文件
// 3. 检查 .flv 文件是否被占用，若被占用则跳过整个文件组
// 4. 为每个 .flv 文件匹配对应的 .xml 和 .cover.jpg/png 文件
func (s *DefaultScanner) Scan() ([]FileGroup, error) {
	var groups []FileGroup

	// 检查输入目录是否存在
	inputDir := s.config.InputDir
	if inputDir == "" {
		log.Println("[SCANNER] 扫描跳过：InputDir 为空")
		return groups, nil
	}

	log.Printf("[SCANNER] 开始扫描目录: %s", inputDir)

	// 读取输入目录下的一级子目录（主播文件夹）
	entries, err := os.ReadDir(inputDir)
	if err != nil {
		// 如果目录不存在或无法读取，返回空列表而不是错误
		// 这样可以避免程序因配置问题而崩溃
		log.Printf("[SCANNER] 无法读取输入目录: %v", err)
		return groups, nil
	}

	log.Printf("[SCANNER] 找到 %d 个条目", len(entries))

	// 遍历每个主播文件夹
	for _, entry := range entries {
		// 只处理目录
		if !entry.IsDir() {
			log.Printf("[SCANNER] 跳过非目录条目: %s", entry.Name())
			continue
		}

		streamerDir := entry.Name()
		streamerPath := filepath.Join(inputDir, streamerDir)

		log.Printf("[SCANNER] 正在扫描主播文件夹: %s", streamerPath)

		// 解析主播名称（从文件夹名中提取 - 后的部分）
		streamerName := parseStreamerName(streamerDir)

		// 扫描主播文件夹中的 .flv 文件
		flvGroups, err := s.scanStreamerFolder(streamerPath, streamerDir, streamerName)
		if err != nil {
			// 单个文件夹扫描失败不影响其他文件夹
			log.Printf("[SCANNER] 扫描文件夹失败: %s, 错误: %v", streamerPath, err)
			continue
		}

		log.Printf("[SCANNER] 文件夹 %s 发现 %d 个文件组", streamerDir, len(flvGroups))
		groups = append(groups, flvGroups...)
	}

	log.Printf("[SCANNER] 扫描完成，共发现 %d 个文件组", len(groups))
	return groups, nil
}

// scanStreamerFolder 扫描单个主播文件夹，返回该文件夹中的文件组
func (s *DefaultScanner) scanStreamerFolder(folderPath, streamerDir, streamerName string) ([]FileGroup, error) {
	var groups []FileGroup

	// 读取文件夹内容
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, err
	}

	// 收集所有文件信息，用于后续匹配
	fileMap := make(map[string]os.DirEntry)
	for _, entry := range entries {
		if !entry.IsDir() {
			fileMap[entry.Name()] = entry
		}
	}

	// 遍历所有 .flv 文件
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))

		// 只处理 .flv 文件
		if ext != ".flv" {
			continue
		}

		flvPath := filepath.Join(folderPath, name)

		// 检查文件大小是否满足最小要求
		info, err := entry.Info()
		if err != nil {
			continue
		}

		if s.config.MinFileSize > 0 && info.Size() < s.config.MinFileSize {
			// 文件太小，跳过
			continue
		}

		// 检查文件是否被占用（Windows 特有逻辑）
		if isFileLocked(flvPath) {
			// 文件被占用，跳过整个文件组
			log.Printf("[SCANNER] 文件被占用，跳过: %s", flvPath)
			continue
		}

		log.Printf("[SCANNER] 发现可处理的 FLV 文件: %s (大小: %d bytes)", flvPath, info.Size())

		// 构建文件组
		baseName := strings.TrimSuffix(name, ext)
		group := FileGroup{
			FLVPath:      flvPath,
			StreamerDir:  streamerDir,
			StreamerName: streamerName,
		}

		// 查找匹配的 .xml 文件
		xmlName := baseName + ".xml"
		if _, exists := fileMap[xmlName]; exists {
			xmlPath := filepath.Join(folderPath, xmlName)
			// 也检查 XML 文件是否被占用
			if !isFileLocked(xmlPath) {
				group.XMLPath = xmlPath
			}
		}

		// 查找匹配的封面文件（.cover.jpg 或 .cover.png）
		coverPath := findCoverFile(folderPath, baseName, fileMap)
		if coverPath != "" && !isFileLocked(coverPath) {
			group.CoverPath = coverPath
		}

		groups = append(groups, group)
	}

	return groups, nil
}

// parseStreamerName 从主播文件夹名称中解析主播名
// 例如：从 "1776261556-筱田柴" 解析出 "筱田柴"
func parseStreamerName(folderName string) string {
	// 查找第一个 "-" 的位置
	idx := strings.Index(folderName, "-")
	if idx == -1 || idx == len(folderName)-1 {
		// 没有找到 "-" 或 "-" 在最后，返回整个文件夹名
		return folderName
	}
	return folderName[idx+1:]
}

// findCoverFile 在文件夹中查找封面文件
// 优先查找 .cover.jpg，其次 .cover.png
func findCoverFile(folderPath, baseName string, fileMap map[string]os.DirEntry) string {
	// 尝试 .cover.jpg
	coverJpg := baseName + ".cover.jpg"
	if _, exists := fileMap[coverJpg]; exists {
		return filepath.Join(folderPath, coverJpg)
	}

	// 尝试 .cover.png
	coverPng := baseName + ".cover.png"
	if _, exists := fileMap[coverPng]; exists {
		return filepath.Join(folderPath, coverPng)
	}

	return ""
}

// isFileLocked 检测文件是否被其他进程占用（Windows 实现）
// 通过尝试以独占读写模式打开文件来检测占用状态
// 如果返回 "sharing violation" 错误，表示文件正被其他进程使用
func isFileLocked(filePath string) bool {
	// 尝试以独占模式打开文件
	// os.O_RDWR: 读写模式
	// os.O_EXCL: 配合其他标志使用，这里主要依赖 OpenFile 的行为
	// 在 Windows 上，如果文件被其他进程以独占方式打开（如录制软件），
	// 这个操作会失败并返回 sharing violation 错误
	file, err := os.OpenFile(filePath, os.O_RDWR, 0)
	if err != nil {
		// 无法打开文件，可能是��占用或权限问题
		// 为了安全起见，都视为被占用
		return true
	}

	// 成功打开，关闭文件并返回未被占用
	file.Close()
	return false
}

// IsFileLocked 导出的文件占用检测函数，供其他包使用
func IsFileLocked(filePath string) bool {
	return isFileLocked(filePath)
}
