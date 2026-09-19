[CmdletBinding()]
param(
    [string]$OutputRoot = 'release',
    [string]$ToolRoot = '.local-tools',
    [switch]$ValidateOnly
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$packageName = 'sync-diff-db2-windows-amd64'

function Resolve-FromRepo([string]$Path) {
    if ([IO.Path]::IsPathRooted($Path)) {
        return [IO.Path]::GetFullPath($Path)
    }
    return [IO.Path]::GetFullPath((Join-Path $repoRoot $Path))
}

if (-not [Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Windows)) {
    throw '该脚本只能在 Windows 上运行。'
}
if ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture -ne [Runtime.InteropServices.Architecture]::X64) {
    throw '当前打包脚本只支持 Windows amd64。'
}

$goCommand = Get-Command go -ErrorAction SilentlyContinue
if (-not $goCommand) {
    throw 'PATH 中未找到 Go。打包机器需要安装 Go。'
}

$outputRootPath = Resolve-FromRepo $OutputRoot
$toolRootPath = Resolve-FromRepo $ToolRoot
$driverRoot = Join-Path $toolRootPath 'clidriver'
$packageDir = Join-Path $outputRootPath $packageName
$archivePath = Join-Path $outputRootPath "$packageName.zip"
$archiveHashPath = "$archivePath.sha256"

if ($ValidateOnly) {
    $required = @(
        (Join-Path $driverRoot 'include\sqlcli.h'),
        (Join-Path $driverRoot 'bin\db2cli.exe'),
        (Join-Path $driverRoot 'bin\db2cli64.dll'),
        (Join-Path $repoRoot 'packaging\run.cmd'),
        (Join-Path $repoRoot 'sync_diff_inspector\config\config_db2.toml'),
        (Join-Path $repoRoot 'README.md'),
        (Join-Path $repoRoot 'LICENSE')
    )
    $missing = @($required | Where-Object { -not (Test-Path -LiteralPath $_) })
    if ($missing.Count -gt 0) {
        throw "打包输入不完整：$($missing -join ', ')"
    }
    Write-Host 'Windows 打包输入与本机工具检查通过。'
    exit 0
}

if ((Test-Path -LiteralPath $packageDir) -or
    (Test-Path -LiteralPath $archivePath) -or
    (Test-Path -LiteralPath $archiveHashPath)) {
    throw "发布输出已经存在，请先移动或删除后重试：$packageDir 或 $archivePath"
}

$powershellHost = (Get-Process -Id $PID).Path
& $powershellHost -NoProfile -File (Join-Path $repoRoot 'scripts\run-db2-local.ps1') -Action Prepare -ToolRoot $toolRootPath
if ($LASTEXITCODE -ne 0) {
    throw '准备 Windows IBM CLI 驱动失败。'
}

$env:CGO_ENABLED = '0'
$env:IBM_DB_HOME = $driverRoot
$env:Path = "$driverRoot\bin;$env:Path"

$version = (& git -C $repoRoot describe --tags --dirty --always).Trim()
$buildTime = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
$gitHash = (& git -C $repoRoot rev-parse HEAD).Trim()
$gitBranch = (& git -C $repoRoot rev-parse --abbrev-ref HEAD).Trim()
$ldflags = "-s -w -X github.com/pingcap/tidb-tools/pkg/utils.Version=$version -X github.com/pingcap/tidb-tools/pkg/utils.BuildTS=$buildTime -X github.com/pingcap/tidb-tools/pkg/utils.GitHash=$gitHash -X github.com/pingcap/tidb-tools/pkg/utils.GitBranch=$gitBranch"

New-Item -ItemType Directory -Path $packageDir -Force | Out-Null
$binaryPath = Join-Path $packageDir 'sync_diff_inspector.exe'

Push-Location $repoRoot
try {
    Write-Host '正在构建 Windows amd64 DB2 版本...'
    & $goCommand.Source build -tags db2cli -trimpath -ldflags $ldflags -o $binaryPath .\sync_diff_inspector
    if ($LASTEXITCODE -ne 0) {
        throw 'Windows DB2 构建失败。'
    }
}
finally {
    Pop-Location
}

Copy-Item -LiteralPath $driverRoot -Destination (Join-Path $packageDir 'clidriver') -Recurse
Copy-Item -LiteralPath (Join-Path $repoRoot 'packaging\run.cmd') -Destination $packageDir
Copy-Item -LiteralPath (Join-Path $repoRoot 'sync_diff_inspector\config\config_db2.toml') -Destination (Join-Path $packageDir 'config.example.toml')
Copy-Item -LiteralPath (Join-Path $repoRoot 'README.md') -Destination $packageDir
Copy-Item -LiteralPath (Join-Path $repoRoot 'LICENSE') -Destination $packageDir

Write-Host '正在生成 ZIP 发布包...'
Compress-Archive -Path (Join-Path $packageDir '*') -DestinationPath $archivePath
$archiveHash = Get-FileHash -LiteralPath $archivePath -Algorithm SHA256
"$($archiveHash.Hash.ToLowerInvariant()) *$([IO.Path]::GetFileName($archivePath))" |
    Set-Content -LiteralPath $archiveHashPath -Encoding ascii

Write-Host "Windows 发布包：$archivePath"
Write-Host "SHA-256 文件：$archiveHashPath"
Write-Host '目标机器解压、复制 config.example.toml 为 config.toml 后，只需执行 run.cmd。'
