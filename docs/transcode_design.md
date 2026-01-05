# 转码功能设计文档

## 1. 现状分析与问题诊断

### 1.1 封面识别问题
通过 `ffprobe` 分析现有 MKV 文件发现，封面是以“附件”（Attachment）形式存储在 MKV 容器中的：
- 流信息：`Stream #0:2: Video: mjpeg (Baseline), yuvj420p(pc, bt470bg/unknown/unknown), 1280x720 [SAR 1:1 DAR 16:9], 90 fps, 90 tbr, 1k tbn (attached pic)`
- 现有逻辑：使用 `-attach` 参数将图片文件作为附件放入 MKV。

**问题原因**：Windows 资源管理器对 MKV 附件的识别支持有限。对于 MP4 容器，Windows 通常要求封面作为第二个视频流存在，并标记为 `attached_pic`。

### 1.2 视频结构
- 源文件：BililiveRecorder 录制的 `.flv`。
- 中间文件：当前工具合并后的 `.mkv`（包含视频、音频和封面附件）。
- 目标文件：用户需要的 `.mp4`（包含转码后的视频/音频，以及可识别的封面）。

## 2. 技术方案设计

### 2.1 修复封面识别方案 (针对 MP4)
为了确保 Windows 能识别 MP4 封面，转码命令应采用以下逻辑：
1. 将封面图片作为输入源之一。
2. 使用 `-map` 将视频、音频和封面图片映射到输出。
3. 设置封面的 disposition 为 `attached_pic`。
4. 针对 PNG 封面，确保编码器兼容性。

**核心 FFmpeg 命令示例**：
```bash
ffmpeg -i input.mkv -i cover.jpg -map 0:v -map 0:a -map 1:v -c copy -c:v:1 mjpeg -disposition:v:1 attached_pic output.mp4
```
*注：如果从现有 MKV 提取封面，则使用 `-map 0:v:0 -map 0:a:0 -map 0:v:1`。*

### 2.2 新增模块结构
- `internal/transcoder/transcoder.go`: 处理转码逻辑的核心模块。
  - `Task`: 定义转码任务（输入路径、输出路径、参数、封面路径）。
  - `Transcoder`: 执行器，管理 FFmpeg 进程。
- `internal/transcoder/scanner.go`: 专门用于扫描已处理文件夹中的视频文件。

### 2.3 集成方式
- **App 层** (`internal/app/app.go`): 暴露新的 API 供前端调用（如 `SelectFolder`, `StartTranscode`, `GetTranscodeProgress`）。
- **配置层** (`internal/config/config.go`): 增加 `TranscodeConfig` 结构。
  ```go
  type TranscodeConfig struct {
      DefaultParams string `yaml:"default_params"` // 默认转码参数
      OutputDir     string `yaml:"output_dir"`     // 转码输出目录
  }
  ```

### 2.4 数据流与处理流程
1. **扫描阶段**：用户选择文件夹 -> 后端扫描 `.mkv`/`.mp4`/`.flv` -> 返回视频列表及元数据。
2. **配置阶段**：用户在前端勾选视频，输入自定义 FFmpeg 参数（如 `-c:v libx264 -crf 23`）。
3. **执行阶段**：
   - 后端创建转码队列。
   - 逐个提取/准备封面。
   - 调用 FFmpeg 执行转码。
   - 更新进度到前端。

## 3. 前端界面设计
- **转码页签**：新增一个独立页面。
- **文件夹选择器**：调用系统对话框选择待转码目录。
- **任务列表**：表格显示文件名、时长、状态、进度。
- **参数设置区**：
  - 全局参数输入框。
  - 预设方案下拉框（如“高画质”、“小体积”）。
- **控制按钮**：开始转码、暂停、清空已完成。

## 4. 关键代码实现思路 (Go)

### 4.1 封面提取与嵌入逻辑
```go
// 伪代码：构建转码参数
func buildArgs(input, output, cover string, customParams []string) []string {
    args := []string{"-i", input}
    if cover != "" {
        args = append(args, "-i", cover)
    }
    
    // 映射流
    args = append(args, "-map", "0:v:0", "-map", "0:a:0")
    if cover != "" {
        args = append(args, "-map", "1:v:0", "-c:v:1", "mjpeg", "-disposition:v:1", "attached_pic")
    }
    
    // 加入用户自定义参数
    args = append(args, customParams...)
    args = append(args, output)
    return args
}
```

## 5. 兼容性考虑
- **PNG 封面**：如果输入封面是 PNG，FFmpeg 的 `mjpeg` 编码器可以处理，或者保持 `png` 编码并设置 `attached_pic`。
- **硬件加速**：允许用户在自定义参数中加入 `-hwaccel`。
- **覆盖安装**：转码后的文件建议存放在子目录 `transcoded/` 下，避免覆盖原文件。
