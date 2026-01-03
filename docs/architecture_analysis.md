# 架构分析与故障诊断报告

## 1. 应用程序启动流程分析 (`main.go`)

应用程序遵循标准的 Go 程序结构，通过 `main` 函数作为入口点，按以下顺序执行：

1.  **实例化 `Application` 结构体**：作为依赖注入容器，持有所有核心组件。
2.  **初始化 (`Initialize`)**：
    *   **配置 (`config`)**：加载 `config.yaml`。
    *   **存储 (`storage`)**：初始化 SQLite 数据库 `data.db`。
    *   **FFmpeg (`ffmpeg`)**：配置 FFmpeg/FFprobe 路径。
    *   **扫描器 (`scanner`)**：配置扫描目录和规则。
    *   **处理器 (`processor`)**：核心业务逻辑，管理任务队列。
    *   **Webhook (`webhook`)**：初始化 HTTP 服务器以接收录播姬事件。
    *   **自动启动 (`autostart`)**：管理注册表启动项。
    *   **Wails 绑定 (`app`)**：初始化用于前端交互的桥接对象，并注入依赖。
3.  **启动服务 (`StartServices`)**：
    *   启动处理器 (`processor`) 消息循环。
    *   异步启动 Webhook 服务器。
4.  **运行 UI (`RunWails`)**：
    *   配置并启动 Wails 应用程序窗口。
    *   这是主线程的阻塞调用。
5.  **关闭 (`Shutdown`)**：
    *   Wails 窗口关闭后，执行资源清理（停止服务、关闭数据库）。

## 2. 前端服务与通信机制

*   **服务方式**：前端资源（HTML/JS/CSS）位于 `frontend/` 目录，通过 Go 的 `embed` 特性打包进二进制文件，并在运行时由 Wails 的 `AssetServer` 提供服务。
*   **通信机制**：
    *   使用 Wails 的 IPC 机制。
    *   后端在 `internal/app/app.go` 中定义了 `App` 结构体，其公开方法（如 `GetStats`, `GetTasks`）被绑定到前端。
    *   前端 JavaScript 通过 `window.go.main.App.[MethodName]` 进行异步调用（Promise）。

## 3. 系统托盘 (`internal/tray`)

*   **实现**：使用了 `github.com/getlantern/systray` 库。
*   **功能**：定义了显示/隐藏窗口、立即扫描、开机自启动等菜单项。
*   **现状**：虽然 `internal/tray` 包已实现，但在 `main.go` 中**完全未被引用或初始化**。这是导致任务栏托盘图标缺失的直接原因。

## 4. 构建过程 (`scripts/build.ps1`)

*   **命令**：`go build -tags desktop,production -ldflags "-w -s -H windowsgui ..."`
*   **影响**：
    *   `-H windowsgui` 参数隐藏了 Windows 控制台窗口。
    *   这意味着如果后端发生 panic 或打印 `log.Printf` 错误信息，用户在界面上无法看到，只能看到前端通用的“加载失败”提示。

## 5. 故障原因分析

### 故障 1：没有任务栏托盘图标
*   **原因**：代码集成遗漏。`main.go` 没有导入 `internal/tray` 包，也没有调用 `tray.New()` 或 `tray.Run()`。
*   **修复**：需要在 `main.go` 中集成托盘模块，连接点击事件回调，并在适当的生命周期（如 `StartServices` 或 `OnStartup`）中启动它。

### 故障 2：UI 显示“加载状态失败”
*   **现象**：前端调用 `GetStats()` 或 `GetTasks()` 失败。
*   **潜在原因**：
    1.  **后端错误**：后端逻辑在处理请求时可能遇到了错误（如数据库查询失败），导致返回 error 或 panic。由于控制台被隐藏，这些错误不可见。
    2.  **通信超时**：如果后端处理过慢（例如数据库被锁），Wails 调用可能超时。
    3.  **依赖未就绪**：虽然初始化顺序看起来正确，但如果数据库连接在运行时断开，会导致此错误。
*   **建议**：
    *   在修复托盘的同时，建议将日志输出到文件，以便排查运行时错误。
    *   检查 `internal/app/app.go` 中的错误处理逻辑。

## 修复计划

1.  **修改 `main.go`**：
    *   引入 `internal/tray`。
    *   在 `Application` 结构体中添加 `tray` 字段。
    *   初始化托盘，并设置菜单回调（关联到 `app` 的方法）。
    *   启动托盘服务。
2.  **增强日志**：
    *   在 `main.go` 初始化阶段配置文件日志（`log.SetOutput`），确保在无控制台模式下也能捕获错误信息。
3.  **验证**：
    *   重新构建并运行，检查托盘是否出现，以及日志文件中是否有关于“加载状态失败”的详细报错。
