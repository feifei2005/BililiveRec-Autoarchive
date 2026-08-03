# Linux Server 部署指南（服务器面板版）

`server` 是已经包含 Web 管理页的单个 Go 可执行文件，不需要 Docker、Node.js、Go 运行时，也不需要单独上传 `frontend`。运行时只依赖 FFmpeg、ffprobe 和 Intel 显卡驱动。

## 1. 面板里要上传什么

先在本机仓库执行：

```powershell
.\scripts\build-server.ps1
```

构建完成后，只上传这两个文件：

| 本机文件 | 服务器目标位置 |
|---|---|
| `build/server` | `/opt/bililive-autoarchive/server` |
| `configs/server.example.yaml` | `/opt/bililive-autoarchive/server.yaml` |

在服务器面板的文件管理器中创建 `/opt/bililive-autoarchive`，上传后把配置模板重命名为 `server.yaml`。给 `server` 增加可执行权限 `755`；`server.yaml` 用 `644` 即可。

最终目录应当是：

```text
/opt/bililive-autoarchive/
├── server
├── server.yaml
├── rec/          # 录播姬直接录到这里
├── waiting/      # 整理和转码工作区
├── output/       # 只保存已完整转码的成品
└── discard/      # 短坏片段、确认的孤立伴随文件
```

`data.db` 和 `server-auth.json` 会在首次运行后自动出现在 `server.yaml` 旁边，不需要预先创建。

## 2. 面板里怎样填写“启动命令”

如果面板有“添加守护进程 / Go 项目 / 自定义项目”功能，填写：

| 面板字段 | 填写内容 |
|---|---|
| 项目名称 | `bililive-autoarchive` |
| 运行目录 / Working Directory | `/opt/bililive-autoarchive` |
| 启动文件 | `/opt/bililive-autoarchive/server` |
| 启动参数 | `-config /opt/bililive-autoarchive/server.yaml` |
| 完整启动命令（若只有一个输入框） | `/opt/bililive-autoarchive/server -config /opt/bililive-autoarchive/server.yaml` |
| 自动重启 | 开启 |

运行目录必须填写正确，因为示例配置中的 `./rec`、`./waiting`、`./output` 都相对于运行目录。不要把运行目录填成 `/root`。

如果面板要求用 Shell 包一层，启动命令填写：

```bash
cd /opt/bililive-autoarchive && exec ./server -config ./server.yaml
```

## 3. 修改 Web 端口

永久修改端口：在面板文件编辑器打开 `/opt/bililive-autoarchive/server.yaml`，修改：

```yaml
server:
  bind_address: "0.0.0.0"
  port: 8080
```

保存后在面板重启项目，并在面板防火墙/安全组放行同一个 TCP 端口。浏览器访问 `http://服务器IP:8080/`。

也可以临时在启动参数末尾加 `-port 9000`，但它不会写回 YAML，不适合长期使用。Webhook 与 Web 共用端口，改端口后还要把录播姬的地址改为 `http://127.0.0.1:新端口/webhook`。

## 4. 配置三段目录流水线

示例配置已经采用下面的布局：

```yaml
processing:
  input_dir: "./rec"
  output_root: "./waiting"
  discard_dir: "./discard"
  staging_mode: "move"
  min_file_size_kb: 1024
  min_duration_sec: 1
  settle_seconds: 10
  orphan_grace_minutes: 60

transcode:
  output_dir: "./output"
```

- `move`：录完后只把 FLV、XML、封面移动到 waiting；转码时直接嵌入封面。推荐使用，省去一次完整 MKV 和额外硬盘写入。
- `remux`：保留旧架构，先在 waiting 生成嵌封面的 MKV，再交给转码器。
- FFmpeg 的转码临时文件始终写在 waiting，文件名以点开头；FFmpeg 成功退出且文件关闭后，程序才把成品发布到 output。因此 output 不会出现正在增长或被 FFmpeg 占用的文件。
- 整理器和转码器不互相调用：整理器只负责把完整文件组发布到 waiting；独立的转码调度器每 10 秒扫描 waiting。服务意外重启后也靠同一目录扫描恢复，不依赖上游任务状态。
- 小于大小阈值、短于时长阈值或无有效视频流的文件，会连同同 stem XML/封面一起移到 discard。
- 孤立 XML/封面只有在超过 `orphan_grace_minutes` 且磁盘上完全没有同 stem `.flv/.mkv/.mp4` 时才会进入 `discard/orphan`。视频过小、被占用或尚未通过校验都不会导致其封面被误删。

建议将录播姬的录制根目录直接设置为 `/opt/bililive-autoarchive/rec`。如果录制盘挂载在别处，也可以把 `input_dir` 改成绝对路径；waiting 和 output 也可以在不同磁盘，程序会用“隐藏临时文件 + 完成后改名”的方式发布。

## 5. 安装 Intel/FFmpeg 依赖

Debian 13：

```bash
apt update
apt install -y ffmpeg intel-media-va-driver libvpl2 libmfx-gen1.2
```

这里安装的是 FFmpeg、ffprobe、Intel Media Driver、oneVPL 和 Intel MFX 运行库；按约定无需在测试后移除。

检查硬件：

```bash
ls -l /dev/dri
ffmpeg -hide_banner -hwaccels
ffmpeg -hide_banner -decoders | grep qsv
ffmpeg -hide_banner -encoders | grep qsv
```

服务账号必须能访问 `/dev/dri/renderD128`。示例使用 `av1_qsv`；显卡不支持 AV1 时，把 `transcode.default_params` 中的编码器换成设备支持的 `hevc_qsv` 或 `h264_qsv`，解码仍保持 Intel QSV。

## 6. 首次启动和登录

在面板启动项目后，先查看项目日志，正常会出现监听地址。健康检查：

```bash
curl http://127.0.0.1:8080/healthz
```

浏览器首次打开管理页会进入单用户注册引导。用户名和明文密码保存在 `/opt/bililive-autoarchive/server-auth.json`（权限 `0600`）；管理页修改用户名或密码后会注销全部现有会话。忘记密码时可在面板文件管理器或 SSH 中编辑此文件，然后重启服务。

Webhook 不要求网页登录，但只接受本机 `127.0.0.1`/`::1` 请求；其余 Web 页面和 API 需要登录 Cookie 或配置的 API Token。

## 7. 不使用面板守护进程时的 systemd 配置

创建 `/etc/systemd/system/bililive-autoarchive.service`：

```ini
[Unit]
Description=Bililive Recorder Auto Archive
After=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/bililive-autoarchive
ExecStart=/opt/bililive-autoarchive/server -config /opt/bililive-autoarchive/server.yaml
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
```

然后执行：

```bash
systemctl daemon-reload
systemctl enable --now bililive-autoarchive
```

## 8. 更新程序

只替换 `/opt/bililive-autoarchive/server`：先在面板停止项目，上传新文件并设为 `755`，再启动。不要覆盖 `server.yaml`、`server-auth.json` 或 `data.db`。`rec`、`waiting`、`output`、`discard` 也都不需要移动或清空。
