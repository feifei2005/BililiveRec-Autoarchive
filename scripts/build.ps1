# BililiveRecorder 自动整理工具构建脚本
# PowerShell 构建脚本

param(
    [Parameter(Position=0)]
    [ValidateSet("build", "dev", "clean", "test")]
    [string]$Command = "build",
    
    [Parameter()]
    [ValidateSet("windows", "linux", "darwin")]
    [string]$OS = "windows",
    
    [Parameter()]
    [ValidateSet("amd64", "arm64")]
    [string]$Arch = "amd64"
)

# 项目配置
$ProjectName = "bililive-recorder-autoarchive"
$OutputDir = "build"
$Version = git describe --tags --always 2>$null
if (-not $Version) { $Version = "dev.0.2.2" }
$BuildTime = Get-Date -Format "yyyy-MM-dd HH:mm:ss"

# 构建参数
# Wails v2 需要 -tags desktop,production 构建标签
# -w -s 用于减小二进制体积，-H windowsgui 隐藏 Windows 控制台窗口
$BuildTags = "desktop,production"
$LDFlags = "-w -s -H windowsgui -X 'main.Version=$Version' -X 'main.BuildTime=$BuildTime'"

function Build-App {
    Write-Host "正在构建 $ProjectName ..." -ForegroundColor Green
    Write-Host "  版本: $Version"
    Write-Host "  平台: $OS/$Arch"
    Write-Host "  构建标签: $BuildTags"
    Write-Host "  构建时间: $BuildTime"
    
    # 创建输出目录
    if (-not (Test-Path $OutputDir)) {
        New-Item -ItemType Directory -Path $OutputDir | Out-Null
    }
    
    # 设置环境变量
    $env:GOOS = $OS
    $env:GOARCH = $Arch
    $env:CGO_ENABLED = "1"  # Wails 需要 CGO
    
    # 确定输出文件名
    $OutputFile = "$OutputDir/$ProjectName"
    if ($OS -eq "windows") {
        $OutputFile += ".exe"
    }
    
    # 执行构建（包含 Wails 必需的构建标签）
    go build -tags $BuildTags -ldflags $LDFlags -o $OutputFile .
    
    if ($LASTEXITCODE -eq 0) {
        Write-Host "构建成功: $OutputFile" -ForegroundColor Green
    } else {
        Write-Host "构建失败!" -ForegroundColor Red
        exit 1
    }
}

function Dev-Mode {
    Write-Host "启动开发模式..." -ForegroundColor Green
    go run .
}

function Clean-Build {
    Write-Host "清理构建产物..." -ForegroundColor Yellow
    if (Test-Path $OutputDir) {
        Remove-Item -Recurse -Force $OutputDir
        Write-Host "已删除 $OutputDir 目录" -ForegroundColor Green
    }
    
    # 清理 Go 缓存
    go clean -cache
    Write-Host "已清理 Go 缓存" -ForegroundColor Green
}

function Run-Tests {
    Write-Host "运行测试..." -ForegroundColor Green
    go test -v ./...
    
    if ($LASTEXITCODE -eq 0) {
        Write-Host "所有测试通过!" -ForegroundColor Green
    } else {
        Write-Host "测试失败!" -ForegroundColor Red
        exit 1
    }
}

# 执行命令
switch ($Command) {
    "build" { Build-App }
    "dev"   { Dev-Mode }
    "clean" { Clean-Build }
    "test"  { Run-Tests }
}
