param(
    [ValidateSet("amd64", "arm64")]
    [string]$Arch = "amd64",

    [switch]$Docker
)

$ErrorActionPreference = "Stop"
$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$Version = git -C $ProjectRoot describe --tags --always 2>$null
if (-not $Version) { $Version = "dev" }
$BuildTime = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
$OutputDir = Join-Path $ProjectRoot "build"
New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null

if ($Docker) {
    $escapedRoot = $ProjectRoot.Replace("\", "/")
    $buildCommand = @"
CGO_ENABLED=0 GOOS=linux GOARCH=$Arch go build -trimpath \
  -ldflags "-w -s -X 'main.Version=$Version' -X 'main.BuildTime=$BuildTime'" \
  -o /src/build/server ./cmd/server
"@

    Write-Host "Building Linux server with Docker Desktop..." -ForegroundColor Green
    docker run --rm `
        -v "${escapedRoot}:/src" `
        -w /src `
        golang:1.24-bookworm `
        sh -c $buildCommand
} else {
    Write-Host "Building pure-Go Linux server locally..." -ForegroundColor Green
    $previousGOOS = $env:GOOS
    $previousGOARCH = $env:GOARCH
    $previousCGO = $env:CGO_ENABLED
    try {
        $env:GOOS = "linux"
        $env:GOARCH = $Arch
        $env:CGO_ENABLED = "0"
        $ldflags = "-w -s -X main.Version=$Version -X 'main.BuildTime=$BuildTime'"
        Push-Location $ProjectRoot
        try {
            go build -trimpath -ldflags $ldflags -o "build/server" ./cmd/server
        } finally {
            Pop-Location
        }
    } finally {
        $env:GOOS = $previousGOOS
        $env:GOARCH = $previousGOARCH
        $env:CGO_ENABLED = $previousCGO
    }
}

if ($LASTEXITCODE -ne 0) {
    throw "Linux server build failed"
}

Write-Host "Build succeeded: build/server" -ForegroundColor Green
