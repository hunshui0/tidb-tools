# sync-diff 独立开发分支

本分支从 [`tidb-tools`](https://github.com/pingcap/tidb-tools) 中保留
`sync_diff_inspector` 及其必要的内部依赖，作为独立开发基础。它保留原有的
MySQL/TiDB 比对行为，并在此基础上逐步增加 DB2 上游数据源支持。

本分支仍然使用 Go 模块路径 `github.com/pingcap/tidb-tools`，以便现有内部
import 和版本信息能够继续在本地正确解析，避免进行范围过大且未经充分验证的
模块路径重写。

## 构建与测试

```text
make build
make test
```

生成的可执行文件位于 `bin/sync_diff_inspector`。使用方法和配置示例请参阅
[`sync_diff_inspector/README.md`](sync_diff_inspector/README.md)。

## DB2 便携发布包

DB2 连接器依赖 IBM 原生 CLI 驱动，因此发布包与操作系统和 CPU 架构相关。
Windows 和 Linux 必须分别构建和发布，不能混用可执行文件或 CLI 驱动。
目标机器不需要安装 Go 或 C 编译器，但发布包必须包含对应平台的完整
`clidriver` 目录。

推荐的发布包名称：

```text
release/
├── sync-diff-db2-windows-amd64.zip
└── sync-diff-db2-linux-amd64.tar.gz
```

推荐的解压目录结构：

```text
sync-diff-db2-<平台>-amd64/
├── sync_diff_inspector.exe    # 仅 Windows
├── sync_diff_inspector        # 仅 Linux
├── run.cmd                    # Windows 一键启动器
├── run.sh                     # Linux 一键启动器
├── clidriver/
├── config.example.toml
├── README.md
└── LICENSE
```

不要把 `config_db2.local.toml`、密码、`tmp/`、checkpoint、日志或生成的修复
SQL 放入发布包。必须保留完整的 CLI 驱动目录，包括 `bin`、`lib`、`conv`、
`msg`、`security` 以及驱动自带的许可证文件。

### Windows amd64

构建机器需要安装 Go。Windows 版本通过动态方式加载 IBM CLI 驱动，因此不需要
GCC。在仓库根目录执行一条命令即可完成驱动准备、版本信息注入、编译、文件复制、
ZIP 压缩和 SHA-256 校验：

```powershell
.\scripts\package-windows.ps1
```

生成结果：

```text
release\sync-diff-db2-windows-amd64\
release\sync-diff-db2-windows-amd64.zip
release\sync-diff-db2-windows-amd64.zip.sha256
```

只检查本机工具和打包输入、不产生发布包：

```powershell
.\scripts\package-windows.ps1 -ValidateOnly
```

如需修改输出位置或便携驱动位置：

```powershell
.\scripts\package-windows.ps1 `
  -OutputRoot 'D:\release' `
  -ToolRoot 'D:\tools\db2'
```

脚本默认拒绝覆盖已有发布目录或压缩包。重新打包前请先移动或删除旧的发布输出。

以下内容是打包脚本内部执行步骤的参考，正常使用时不需要逐条执行：

```powershell
.\scripts\run-db2-local.ps1 -Action Prepare

$env:CGO_ENABLED = '0'
$env:IBM_DB_HOME = (Resolve-Path '.local-tools\clidriver').Path
$env:Path = "$env:IBM_DB_HOME\bin;$env:Path"

$version = (git describe --tags --dirty --always).Trim()
$buildTime = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
$gitHash = (git rev-parse HEAD).Trim()
$gitBranch = (git rev-parse --abbrev-ref HEAD).Trim()
$ldflags = "-s -w -X github.com/pingcap/tidb-tools/pkg/utils.Version=$version -X github.com/pingcap/tidb-tools/pkg/utils.BuildTS=$buildTime -X github.com/pingcap/tidb-tools/pkg/utils.GitHash=$gitHash -X github.com/pingcap/tidb-tools/pkg/utils.GitBranch=$gitBranch"

$packageDir = Join-Path (Resolve-Path '.').Path 'release\sync-diff-db2-windows-amd64'
if (Test-Path -LiteralPath $packageDir) {
  throw "发布目录已经存在，请先移动或删除后再重新构建：$packageDir"
}
New-Item -ItemType Directory -Path $packageDir | Out-Null

go build -tags db2cli -trimpath -ldflags $ldflags `
  -o (Join-Path $packageDir 'sync_diff_inspector.exe') `
  .\sync_diff_inspector
if ($LASTEXITCODE -ne 0) { throw 'Windows DB2 构建失败。' }

Copy-Item -LiteralPath '.local-tools\clidriver' -Destination $packageDir -Recurse -Force
Copy-Item -LiteralPath 'sync_diff_inspector\config\config_db2.toml' `
  -Destination (Join-Path $packageDir 'config.example.toml') -Force
Copy-Item -LiteralPath 'README.md' -Destination $packageDir -Force
Copy-Item -LiteralPath 'LICENSE' -Destination $packageDir -Force

$archive = Join-Path (Resolve-Path '.').Path 'release\sync-diff-db2-windows-amd64.zip'
Compress-Archive -Path (Join-Path $packageDir '*') -DestinationPath $archive -Force
Get-FileHash -LiteralPath $archive -Algorithm SHA256
```

在目标 Windows 机器上解压发布包，把 `config.example.toml` 复制为
`config.toml` 并填写连接配置。之后只需执行：

```powershell
.\run.cmd
```

也可以指定其他配置文件：

```powershell
.\run.cmd .\config-test.toml
```

`run.cmd` 只在当前进程中设置 `IBM_DB_HOME` 和 `PATH`，不会永久修改系统环境
变量。`scripts/run-db2-local.ps1` 仍然只是开发辅助脚本，会检查 Go 并重新构建。

### Linux amd64

请在 Linux 或 WSL 中使用 Linux amd64 版 IBM CLI 驱动构建 Linux 发布包。
不能复用 Windows 下的 `.local-tools/clidriver`。构建机器需要 Go、GCC 和 C
开发工具链；目标运行机器不需要这些构建工具。在仓库根目录执行一条命令：

```sh
sh ./scripts/package-linux.sh
```

脚本会在缺少 Linux IBM CLI 时调用项目固定版本的安装器，并自动完成编译、
依赖检查、文件复制、tar.gz 压缩和 SHA-256 校验。生成结果：

```text
release/sync-diff-db2-linux-amd64/
release/sync-diff-db2-linux-amd64.tar.gz
release/sync-diff-db2-linux-amd64.tar.gz.sha256
```

只检查工具和打包输入、不产生发布包：

```sh
sh ./scripts/package-linux.sh --validate-only
```

可通过环境变量修改输出和驱动目录：

```sh
OUTPUT_ROOT=/opt/release \
DB2_TOOL_ROOT=/opt/db2-tools \
sh ./scripts/package-linux.sh
```

脚本默认拒绝覆盖已有发布目录或压缩包。重新打包前请先移动或删除旧的发布输出。

以下内容是打包脚本内部执行步骤的参考，正常使用时不需要逐条执行。

先把 Linux CLI 驱动放到 `.local-tools-linux/clidriver`，然后执行：

```sh
set -eu

REPO_ROOT=$(pwd)
export IBM_DB_HOME="$REPO_ROOT/.local-tools-linux/clidriver"
export CGO_ENABLED=1
export CGO_CFLAGS="-I$IBM_DB_HOME/include"
export CGO_LDFLAGS="-L$IBM_DB_HOME/lib"
export LD_LIBRARY_PATH="$IBM_DB_HOME/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

VERSION=$(git describe --tags --dirty --always)
BUILD_TIME=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
GIT_HASH=$(git rev-parse HEAD)
GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
LDFLAGS="-s -w -X github.com/pingcap/tidb-tools/pkg/utils.Version=$VERSION -X github.com/pingcap/tidb-tools/pkg/utils.BuildTS=$BUILD_TIME -X github.com/pingcap/tidb-tools/pkg/utils.GitHash=$GIT_HASH -X github.com/pingcap/tidb-tools/pkg/utils.GitBranch=$GIT_BRANCH"

PACKAGE_DIR="$REPO_ROOT/release/sync-diff-db2-linux-amd64"
if [ -e "$PACKAGE_DIR" ]; then
  echo "发布目录已经存在，请先移动或删除后再重新构建：$PACKAGE_DIR" >&2
  exit 1
fi
mkdir -p "$PACKAGE_DIR"

go build -tags db2cli -trimpath -ldflags "$LDFLAGS" \
  -o "$PACKAGE_DIR/sync_diff_inspector" \
  ./sync_diff_inspector

cp -R "$IBM_DB_HOME" "$PACKAGE_DIR/clidriver"
cp sync_diff_inspector/config/config_db2.toml "$PACKAGE_DIR/config.example.toml"
cp README.md LICENSE "$PACKAGE_DIR/"
chmod +x "$PACKAGE_DIR/sync_diff_inspector"

LD_LIBRARY_PATH="$PACKAGE_DIR/clidriver/lib" ldd "$PACKAGE_DIR/sync_diff_inspector"
tar -C "$REPO_ROOT/release" -czf \
  "$REPO_ROOT/release/sync-diff-db2-linux-amd64.tar.gz" \
  sync-diff-db2-linux-amd64
sha256sum "$REPO_ROOT/release/sync-diff-db2-linux-amd64.tar.gz"
```

发布前必须检查 `ldd` 输出，不能存在任何 `not found` 依赖。为了提高 Linux
兼容性，建议在计划支持的、glibc 版本最老的 Linux 发行版上构建。

在目标 Linux 机器上解压发布包，把 `config.example.toml` 复制为
`config.toml` 并填写连接配置，然后执行：

```sh
./run.sh
```

也可以指定其他配置文件：

```sh
./run.sh ./config-test.toml
```

`run.sh` 只为当前进程设置 `IBM_DB_HOME` 和 `LD_LIBRARY_PATH`，不会永久修改
系统环境变量。

Windows、Linux、amd64 和 arm64 都需要各自独立的可执行文件及匹配的 IBM CLI
驱动。只有在 IBM 提供对应架构 CLI 驱动时才能发布该架构。每个发布包都应在
干净机器上解压验证；向组织外部分发 IBM CLI 前，还需要确认其再分发许可条款。

## 保留的内部依赖包

- `pkg/dbutil`
- `pkg/filter`
- `pkg/table-filter`
- `pkg/table-rule-selector`
- `pkg/column-mapping`
- `pkg/utils`
- `pkg/schemacmp`（为 `pkg/dbutil` 提供测试支持）

## 许可证

本项目使用 Apache 2.0 许可证，详见 [LICENSE](LICENSE)。
