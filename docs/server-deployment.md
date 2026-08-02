# Linux Server 部署指南

本文说明如何把无桌面依赖的 `server` 部署到带 Intel 显卡的 Linux 主机。Web 管理页已经嵌入可执行文件，不需要单独复制前端资源，也不需要 Docker 或 Go 运行时。

## 1. 准备文件

部署只需要以下两个文件：

- `build/server`：Linux 可执行文件
- `configs/server.example.yaml`：配置模板，部署后命名为 `server.yaml`

在 Windows PowerShell 中构建：

```powershell
.\scripts\build-server.ps1
```

上传到服务器：

```powershell
ssh root@SERVER_IP "mkdir -p /opt/bililive-autoarchive"
scp .\build\server root@SERVER_IP:/opt/bililive-autoarchive/server
scp .\configs\server.example.yaml root@SERVER_IP:/opt/bililive-autoarchive/server.yaml
```

## 2. 安装运行依赖

Debian 13 上可安装：

```bash
apt update
apt install -y ffmpeg intel-media-va-driver libvpl2 libmfx-gen1.2
```

确认 Intel 渲染设备和 QSV 编解码器可用：

```bash
ls -l /dev/dri
ffmpeg -hide_banner -hwaccels
ffmpeg -hide_banner -decoders | grep qsv
ffmpeg -hide_banner -encoders | grep qsv
```

默认 AV1 配置需要 `av1_qsv`。如果显卡不支持 AV1，应在 `server.yaml` 中改用该设备支持的 QSV 编码器。

## 3. 配置服务

编辑 `/opt/bililive-autoarchive/server.yaml`，至少修改以下项目：

```yaml
server:
  bind_address: "0.0.0.0"
  api_token: "replace-with-a-long-random-token"
  port: 8080
  webhook_path: "/webhook"
  webhook_enabled: true

processing:
  input_dir: "/data/recordings"
  output_root: "/data/archive"

transcode:
  output_dir: "/data/transcoded"
```

创建数据目录：

```bash
mkdir -p /data/recordings /data/archive /data/transcoded
```

端口的永久配置位于 `server.port`。修改后需要重启服务。也可以用命令行临时覆盖监听设置：

```bash
./server -config ./server.yaml -listen 0.0.0.0 -port 9000
```

命令行覆盖不会写回 `server.yaml`。使用防火墙时还需要放行 Web 端口，例如：

```bash
ufw allow 8080/tcp
```

## 4. 首次启动

```bash
cd /opt/bililive-autoarchive
chmod +x server
./server -config ./server.yaml
```

健康检查：

```bash
curl http://127.0.0.1:8080/healthz
```

浏览器打开 `http://SERVER_IP:8080/`。首次访问会显示单用户注册引导，注册后使用 Cookie 会话登录。

运行时会在 `server.yaml` 旁生成：

- `data.db`：任务和处理记录
- `server-auth.json`：管理员用户名和明文密码，权限为 `0600`

修改管理页中的用户名或密码会注销全部会话。忘记密码时可以通过 SSH 修改 `server-auth.json`，然后重启服务。不要把该文件提交到 Git 或公开备份。

## 5. 配置 Webhook

Webhook 不需要网页登录，但只接受服务器本机的 `127.0.0.1` 或 `::1` 请求。录播姬必须运行在同一台 Linux 主机，并使用：

```text
http://127.0.0.1:8080/webhook
```

修改 Web 端口后，需要同步修改录播姬中的 Webhook 地址。来自局域网或公网的 Webhook 请求会返回 `403`。

## 6. 配置 systemd

创建 `/etc/systemd/system/bililive-autoarchive.service`：

```ini
[Unit]
Description=Bililive Recorder Auto Archive
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/bililive-autoarchive
ExecStart=/opt/bililive-autoarchive/server -config /opt/bililive-autoarchive/server.yaml
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
```

启用并启动：

```bash
systemctl daemon-reload
systemctl enable --now bililive-autoarchive
systemctl status bililive-autoarchive
```

查看日志：

```bash
journalctl -u bililive-autoarchive -f
```

## 7. 更新版本

先在本机重新构建并上传为临时名称：

```powershell
.\scripts\build-server.ps1
scp .\build\server root@SERVER_IP:/opt/bililive-autoarchive/server.new
```

然后在服务器上替换：

```bash
systemctl stop bililive-autoarchive
chmod +x /opt/bililive-autoarchive/server.new
mv /opt/bililive-autoarchive/server.new /opt/bililive-autoarchive/server
systemctl start bililive-autoarchive
systemctl status bililive-autoarchive
```

更新可执行文件不会覆盖 `server.yaml`、`server-auth.json` 或 `data.db`。建议定期备份这三个文件。

## 8. 常见检查

检查监听端口：

```bash
ss -ltnp | grep 8080
```

检查服务账号能否访问 Intel GPU：

```bash
ls -l /dev/dri/renderD128
```

检查管理页面认证状态：

```bash
curl http://127.0.0.1:8080/api/auth/status
```

更多配置和 HTTP API 说明见 [server.md](server.md)。
