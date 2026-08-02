# BililiveRec-Autoarchive

B站录播姬自动归档工具，专为配合 [BililiveRecorder](https://github.com/BililiveRecorder/BililiveRecorder) 使用而设计。能够自动监控录制目录，将 FLV 录制文件无损转封装为 MKV 格式，并按照`主播/年/月/日`的结构进行自动归档。

## ✨ 功能特性

- **录播自动归档**：自动将B站录播文件按主播分类归档，支持 `主播/年/月/日` 目录结构
- **视频转码**：支持将 FLV/MKV 转码为 AV1 等格式，支持 NVIDIA/AMD/Intel 硬件加速
- **智能帧率上限**：可设置输出帧率上限，仅在源帧率超过设定值时自动降帧
- **智能进度预测**：基于视频分辨率和帧率预测处理时间，支持帧率上限时的精确估算
- **拖拽导入**：支持拖拽多个文件或文件夹导入进行批量转码
- **日志自动轮转**：日志文件自动拆分（10MB/文件），保留最近5个备份
- **数据库自动清理**：每24小时自动清理过期任务记录和孤立数据
- **Webhook 支持**：可接收录播姬的 Webhook 通知，实现录制结束后即时处理
- **Web 管理界面**：内置 Web 界面，随时查看任务状态和处理进度
- **弹幕归档**：自动同步移动对应的 XML 弹幕文件

## 🚀 安装说明

### 前置要求

- 系统中已安装 [FFmpeg](https://ffmpeg.org/)，并添加到系统 PATH 环境变量中（或在配置文件中指定路径）
- Windows 10/11：运行桌面版
- Linux amd64/arm64：运行无桌面依赖的 `server`；Intel QSV 转码需要对应显卡驱动、oneVPL/MFX 运行库及 QSV 版 FFmpeg

### 安装步骤

1. 在 [Releases](https://github.com/feifei2005/BililiveRec-Autoarchive/releases) 页面下载最新版本的压缩包
2. 解压到任意目录
3. 运行 `BililiveRecorder-autoarchive.exe`
4. 程序会自动在浏览器中打开 Web 管理界面

## 📖 使用方法

Linux Intel QSV 服务端的构建、配置、systemd 和更新说明见 [Linux Server 部署指南](docs/server-deployment.md)。服务端包含内嵌 Web 管理页、单用户登录、Webhook 自动归档以及独立单并发 NV12 回退池。

### 录播自动归档

1. 在 Web 界面的「录播转MKV」页面配置输入目录（录播姬工作目录）和输出目录
2. 程序会自动监控输入目录，发现新的录制文件后自动处理
3. 处理完成后，MKV 文件和弹幕文件会按 `主播/年/月/日` 结构归档到输出目录

### 视频转码

1. 在 Web 界面的「视频转码」页面设置输出目录和 FFmpeg 参数
2. 通过以下方式添加待转码文件：
   - 点击「添加文件」按钮选择文件
   - 点击「添加文件夹」按钮选择文件夹
   - **直接拖拽文件或文件夹到页面**
3. 点击「开始转码」按钮开始处理
4. 可在界面查看实时进度、已用时间和预计剩余时间

### 配合录播姬使用（Webhook）

1. 打开录播姬设置 -> Webhook
2. 添加 Webhook 地址：`http://localhost:8080/webhook`
3. 勾选「录制结束」事件
4. 录制结束时，本工具会立即收到通知并开始处理

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

### FFmpeg 转码参数示例

用户填写的自定义参数将用于视频转码。以下是一些常见的配置示例：

**H.264 软件编码（CPU）：**
```
-c:v libx264 -preset medium -crf 23 -c:a aac -b:a 128k
```

**HEVC/H.265 软件编码：**
```
-c:v libx265 -preset medium -crf 28 -c:a aac -b:a 128k
```

**NVIDIA 硬件加速 (NVENC)：**
```
-c:v h264_nvenc -preset p4 -cq 23 -c:a aac -b:a 128k
```

**AMD 硬件加速 (AMF) - AV1：**
```
-c:v av1_amf -profile:v main -rc:v cqp -qp_i 100 -qp_p 100 -c:a libopus -b:a 96k
```

**Intel 硬件加速 (QSV)：**
```
-c:v h264_qsv -preset medium -global_quality 23 -c:a aac -b:a 128k
```

### 注意事项

1. **不要在自定义参数中包含以下内容**：
   - `-i` 输入文件参数（自动添加）
   - `-map` 流映射参数（自动处理）
   - `-progress` 进度参数（自动添加）
   - 输出文件路径（自动生成）

2. **封面流处理**：
   - 程序会自动检测源视频是否包含封面流
   - 如果有封面，会自动映射并保留到输出文件

3. **硬件加速**：
   - 使用硬件加速编码器前，请确保系统已安装相应驱动
   - 不同硬件的参数和取值范围可能不同，请参考 FFmpeg 官方文档

## ⚠️ 已知问题

- **录播转MKV待处理列表UI可能无法正常显示**（待验证）

如果你遇到任何问题（包括上述已知问题），欢迎在 [Issues](https://github.com/feifei2005/BililiveRec-Autoarchive/issues) 页面提交反馈。

## 📋 更新日志

查看完整更新日志：[CHANGELOG.md](CHANGELOG.md)

## 📄 许可证

本项目采用 **GNU General Public License v3.0 (GPL v3)** 许可证。
详情请参阅 [LICENSE](LICENSE) 文件。
