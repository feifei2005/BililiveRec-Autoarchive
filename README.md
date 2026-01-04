# BililiveRec-Autoarchive

> ⚠️ **项目持续开发中 / Work in Progress**
> 
> 本项目目前处于活跃开发阶段，功能可能会发生变化，部分功能可能不够稳定。建议在使用前备份重要数据。

B站录播姬自动归档工具，专为配合 [BililiveRecorder](https://github.com/BililiveRecorder/BililiveRecorder) 使用而设计。能够自动监控录制目录，将 FLV 录制文件无损转封装为 MKV 格式，并按照`主播/年/月/日`的结构进行自动归档。

## 📋 更新日志

### v0.1.1 (2026-01-04)

- **配置文件修复**：移除默认配置中冗余的 `date_regex` 行，代码中已有完善的日期提取逻辑
- **视频流有效性检测修复**：使用 `ffprobe` 检查视频流的 `width` 和 `height`，确保只处理真正有效的视频文件，无效文件将被丢弃
- **前端 UI 改进**：
  - 将"已处理"状态改为"已成功"
  - 添加"已丢弃"分类显示预检不通过的文件
  - 添加处理失败文件的处理选项 UI（保持原位/移动到回收站/删除）
- **后端改进**：
  - 添加 `failed_file_action` 配置项，支持配置处理失败文件的行为
  - 自动清理处理失败遗留的输出文件

## ✨ 功能特性

- **自动监控**：实时监控录制目录，自动发现新完成的录制文件
- **无损转封装**：将 FLV 视频文件无损转换为 MKV 格式，保留原始画质
- **弹幕归档**：自动同步移动对应的 XML 弹幕文件
- **智能归档**：按 `主播/年/月/日` 目录结构自动分类整理
- **后台运行**：支持最小化到系统托盘运行，不占用任务栏
- **Web 界面**：内置 Web 管理界面，随时查看任务状态和处理进度
- **Webhook 支持**：支持接收录播姬的 Webhook 通知，实现即时处理

## 🚀 安装说明

1. 在 [Releases](https://github.com/user/bililive-recorder-autoarchive/releases) 页面下载最新版本的压缩包。
2. 解压到任意目录。
3. 确保系统中已安装 FFmpeg，并将其添加到系统 PATH 环境变量中（或者在配置文件中指定路径）。

## ⚙️ 配置说明

程序首次运行会自动生成 `config.yaml` 配置文件。你也可以手动复制 `configs/config.example.yaml` 进行配置。

### 基础配置

```yaml
processing:
  input_dir: "D:\\录播姬\\录制目录"    # 录播姬的工作目录
  output_root: "E:\\整理后的录播"       # 整理后的输出目录
  discard_dir: "E:\\无效录制"           # 无效文件存放目录（可选）

server:
  port: 8080                            # Webhook 监听端口
```

### 配合录播姬使用

1. 打开录播姬设置 -> Webhook
2. 添加 Webhook 地址：`http://localhost:8080/webhook`
3. 勾选 "录制结束" 事件

这样当录制结束时，本工具会立即收到通知并开始处理。

## 📄 许可证

本项目采用 **GNU General Public License v3.0 (GPL v3)** 许可证。
详情请参阅 [LICENSE](LICENSE) 文件。
