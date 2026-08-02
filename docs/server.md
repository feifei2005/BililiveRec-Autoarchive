# Linux server

`server` 是无桌面依赖的 Go HTTP 服务，包含浏览器管理页、录播姬 Webhook、自动扫描归档、SQLite 处理记录和 Intel QSV 转码。主转码池按配置并发执行；遇到分辨率变化引发的 QSV 滤镜重初始化失败时，任务会移交到独立的单并发 NV12 池，不占用主池 worker。

完整的文件上传、端口配置、systemd 和升级步骤见 [Linux Server 部署指南](server-deployment.md)。

## 运行依赖

- Linux amd64 或 arm64
- 支持 Intel QSV 的 GPU 和驱动
- 带 `qsv`、`h264_qsv` 以及目标 QSV 编码器（示例使用 `av1_qsv`）的 FFmpeg 和 ffprobe
- Intel oneVPL/MFX 运行库；Debian 13 的 Arc 显卡需要 `libmfx-gen1.2`
- 服务账号需要访问 `/dev/dri/renderD*`

可以用以下命令检查运行环境：

```bash
ffmpeg -hide_banner -hwaccels
ffmpeg -hide_banner -decoders | grep qsv
ffmpeg -hide_banner -encoders | grep qsv
ls -l /dev/dri
```

## 构建

默认使用本机 Go 交叉编译，不需要 Docker：

```powershell
.\scripts\build-server.ps1
```

没有合适的本机 Go 环境时，可以使用 Docker Desktop 提供一次性构建环境：

```powershell
.\scripts\build-server.ps1 -Docker
```

两种方式都会生成 `build/server`。程序使用 `CGO_ENABLED=0` 构建，目标机器不需要 Go 运行时。

## 配置和启动

以 `configs/server.example.yaml` 为模板创建 `server.yaml`，至少检查输入目录、输出目录、监听地址和 API Token。外部访问需要将 `bind_address` 设为 `0.0.0.0` 或指定网卡地址，并在防火墙中放行端口。

```bash
chmod +x server
./server -config server.yaml
```

命令行可覆盖监听设置：

```bash
./server -config server.yaml -listen 0.0.0.0 -port 8080
```

浏览器访问 `http://服务器地址:端口/`。首次访问会进入单用户注册引导，设置管理员用户名和密码；后续使用 Cookie 会话登录。管理页的“账户安全”可以修改用户名或密码，修改成功会注销所有浏览器会话。

凭据以明文 JSON 保存在配置文件同目录的 `server-auth.json`，文件权限为 `0600`：

```json
{
  "username": "admin",
  "password": "change-this-password",
  "created_at": "2026-08-02T00:00:00Z"
}
```

忘记密码时可以通过 SSH 修改这个文件，然后重启服务。不要将该文件提交到版本库或放入公开备份。

`api_token` 用于非浏览器脚本调用管理 API，可通过 `Authorization: Bearer <token>` 或 `X-API-Key: <token>` 发送。浏览器登录不使用 API Token；`/healthz` 保持免登录。录播姬 Webhook 路径也不要求登录，但只接受来自 `127.0.0.1` 或 `::1` 的请求。不要使用示例配置里的 Token。

## HTTP API

健康检查：

```bash
curl http://127.0.0.1:8080/healthz
```

添加任务：

```bash
curl -X POST http://127.0.0.1:8080/api/transcode/start \
  -H 'Authorization: Bearer <token>' \
  -H 'Content-Type: application/json' \
  -d '{"files":["/data/input/example.flv"]}'
```

查询任务：

```bash
curl http://127.0.0.1:8080/api/transcode/tasks \
  -H 'Authorization: Bearer <token>'
```

任务 JSON 的 `executionPool` 为 `main` 或 `nv12`。`nv12` 表示该任务已被移交独立回退池；它仍使用 QSV 解码和配置的 QSV 编码器，只将解码输出改为系统内存中的 NV12 帧。

取消任务：

```bash
curl -X POST http://127.0.0.1:8080/api/transcode/cancel \
  -H 'Authorization: Bearer <token>' \
  -H 'Content-Type: application/json' \
  -d '{"taskId":"<task-id>"}'
```
