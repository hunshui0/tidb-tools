[CmdletBinding()]
param(
    [ValidateSet('Prepare', 'Build', 'Run')]
    [string]$Action = 'Run',

    [string]$ConfigPath = 'sync_diff_inspector/config/config_db2.local.toml',

    [string]$ToolRoot = '.local-tools'
)

$ErrorActionPreference = 'Stop'

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if ([IO.Path]::IsPathRooted($ToolRoot)) {
    $ToolRoot = [IO.Path]::GetFullPath($ToolRoot)
}
else {
    $ToolRoot = [IO.Path]::GetFullPath((Join-Path $repoRoot $ToolRoot))
}
$driverRoot = Join-Path $ToolRoot 'clidriver'
$binaryPath = Join-Path $repoRoot 'bin\sync_diff_inspector.exe'

function Set-PortableEnvironment {
    # On Windows go_ibm_db loads db2cli64.dll through syscall.NewLazyDLL.
    # Its C-backed implementation is used only on Unix platforms, so forcing
    # CGO here adds an unnecessary GCC dependency and breaks otherwise valid
    # Windows builds.
    $env:CGO_ENABLED = '0'
    $env:IBM_DB_HOME = $driverRoot
    $env:Path = "$driverRoot\bin;$env:Path"
}

function Get-Db2Driver {
    $header = Join-Path $driverRoot 'include\sqlcli.h'
    $cli = Join-Path $driverRoot 'bin\db2cli.exe'
    if ((Test-Path -LiteralPath $header) -and (Test-Path -LiteralPath $cli)) {
        return
    }

    Write-Host 'Downloading the portable Db2 CLI driver ...'
    & $goExe install 'github.com/ibmdb/go_ibm_db/installer@v0.5.4'
    if ($LASTEXITCODE -ne 0) {
        throw 'Unable to download the go_ibm_db installer.'
    }

    $moduleCache = (& $goExe env GOMODCACHE).Trim()
    $installerDir = Join-Path $moduleCache 'github.com\ibmdb\go_ibm_db@v0.5.4\installer'
    $setupScript = Join-Path $installerDir 'setup.go'
    if (-not (Test-Path -LiteralPath $setupScript)) {
        throw "go_ibm_db installer source was not found: $setupScript"
    }

    Push-Location $installerDir
    try {
        & $goExe run .\setup.go $ToolRoot
        if ($LASTEXITCODE -ne 0) {
            throw 'Unable to download or unpack the Db2 CLI driver.'
        }
    }
    finally {
        Pop-Location
    }

    if (-not ((Test-Path -LiteralPath $header) -and (Test-Path -LiteralPath $cli))) {
        throw 'The downloaded Db2 CLI driver is incomplete; include/sqlcli.h and bin/db2cli.exe are required.'
    }
}

$goCommand = Get-Command go -ErrorAction SilentlyContinue
if (-not $goCommand) {
    throw 'Go was not found in PATH. Open a new PowerShell window after installing Go, then retry.'
}
$goExe = $goCommand.Source
Set-PortableEnvironment
Get-Db2Driver

if ($Action -eq 'Prepare') {
    Write-Host "Portable environment is ready at $ToolRoot"
    exit 0
}

Push-Location $repoRoot
try {
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $binaryPath) | Out-Null
    Write-Host 'Building sync_diff_inspector with Db2 support ...'
    & $goExe build -tags db2cli -o $binaryPath .\sync_diff_inspector
    if ($LASTEXITCODE -ne 0) {
        throw 'Db2 build failed.'
    }

    if ($Action -eq 'Run') {
        $resolvedConfig = Join-Path $repoRoot $ConfigPath
        if (-not (Test-Path -LiteralPath $resolvedConfig)) {
            throw "Configuration file was not found: $resolvedConfig"
        }
        Write-Host 'Starting sync_diff_inspector ...'
        & $binaryPath -C $resolvedConfig
        exit $LASTEXITCODE
    }
}
finally {
    Pop-Location
}
