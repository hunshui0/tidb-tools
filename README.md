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

## 进度与比较结果

发布包中的 `./run.sh` 直接启动已经编译好的 Linux 程序，不会编译源码。
启动日志中的 `UTC Build Time` 是程序构建时写入的时间，不是本次启动在进行编译。
连接数据库、列举目标表、读取 DB2 元数据和检查结构均发生在数据比较之前。
这些阶段会记录开始、完成或失败及耗时；执行超过 5 秒时，每隔 5 秒记录当前阶段、
已处理对象数和正在等待的表或操作。`completed` 表示已处理对象数，
不表示这些表已判定一致；同一对象反复出现表示该操作尚未返回。

读取目标端 MySQL/TiDB 表结构元数据或 TiDB 源表结构时，遇到连接失效、EOF 或网络错误，最多尝试 3 次，
两次重试分别等待 0.5 秒、1 秒，并记录重试及恢复结果。
重试使用原连接池及其会话、快照配置，不跳过失败的表；持续失败会明确退出。
权限、SQL 和表不存在等错误不重试。该策略仅用于只读表结构元数据，
不改变数据分块比较或修复 SQL 的执行行为。

DB2 到 TiDB 的 `DATE` 列比较兼容纯日期和驱动返回的 RFC3339 日期时间表示，
例如 `2020-06-19` 与 `2020-06-19T00:00:00+08:00` 按同一个日历日期比较。
`DATE` 不转换为 UTC，避免日期偏移一天；时间戳仍使用原有的时间归一化规则。

终端中的百分比按**已处理完成的表数 / 本次任务总表数**计算，完成数包含已跳过的表，
断点续跑时包含断点之前已处理的表。它不是行数比例或预计耗时比例；一张大表尚未完成时，
百分比可能暂时不变。`chunks 已处理数/已生成数` 表示本轮的数据分块处理情况；
分块仍在生成时，后者会增加，不作为总进度的分母。
终端内按窗口宽度显示并原地更新单行进度，无变化时不重复输出；
重定向到文件或管道时输出普通文本，不使用光标控制符。

每次完成比较后，查看配置中 `output-dir` 下的 **`summary.txt`**。
文件按表名排序，列出每张表的 `Structure`（结构）和 `Data`（数据）两个独立结论，
以及总体结果、两端累计行数和错误或缺表说明。例如：

| Table | Result | Structure | Data |
| --- | --- | --- | --- |
| `app.same` | pass | equal | equal |
| `app.data_diff` | fail | equal | different |
| `app.struct_diff` | fail | different | equal |
| `app.cannot_compare` | fail | different | skipped |
| `app.missing` | skipped | missing | skipped |
| `app.query_error` | error | equal | error |

`equal` 表示一致，`different` 表示不一致，`skipped` 表示跳过，`missing` 表示缺表，
`error` 表示出错，`not_checked` 表示尚未完成检查。只检查结构或只检查数据时，
另一项显示 `skipped`；`pass` 仅表示本次要求的检查通过。
结构一致、数据一致的表数分别统计，结构不同但数据相同的表也会明确显示。
行数只累计已比较的分块；跳过、未完成或出错时，不代表完整表行数。
无唯一键的 `checksum-only` 模式发现不一致时，差异行数仍显示 `unknown/unknown`，
不提供行定位或修复 SQL。

升级后请使用新的 `output-dir` 做首次比较。旧版 checkpoint 缺少明确的已检查标记和
分块行数信息，恢复时可能显示 `not_checked`，不能据此判定一致。

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

### Linux amd64 / 麒麟 V10 x86_64

推荐在 Linux x86_64 构建机上通过 Docker 生成便携包，构建机只需要 Docker 和 Git：

```sh
sh scripts/package-linux-portable.sh
```

该脚本使用固定摘要的 manylinux2014 构建镜像（glibc 2.17）和 Go 1.23.12，
以 `-tags db2cli`、CGO 开启方式编译真实 DB2 连接器。
IBM CLI 固定为 **11.5.9**，下载使用版本目录并验证 SHA-256，避免安装器自动
下载要求新版 glibc 的驱动。不能仅将此版本设为 `CGO_ENABLED=0`，那会失去
Linux 原生 DB2 连接能力。

发布包包含完整 `clidriver/` 和 `runtime/`。后者携带 libstdc++、libgcc、
libxml2 等实际依赖，`run.sh` 为当前进程设置库路径，无需目标机安装 Go、GCC
或 IBM 客户端，也不修改系统环境或替换系统 libc。目标系统仍需要 x86_64、
兼容的 Linux 内核、glibc 2.17 或更新版本及其匹配的动态加载器。
此基线覆盖常见麒麟 V10 x86_64 环境，仍应在实际目标机验证。

打包检查遍历**全部 ELF 文件**，包括程序、IBM 驱动、TLS/认证插件及附带运行库。
任何文件要求高于 glibc 2.17 的符号时都会中止，避免只在新系统上通过 `ldd`
就误判兼容。检查还会启动程序、加载 DB2 驱动并分配 ODBC 环境句柄；这些检查
不需要数据库账号，不代表已验证真实数据库连接或数据比对。

生成文件：

```text
release/sync-diff-db2-linux-amd64.tar.gz
release/sync-diff-db2-linux-amd64.tar.gz.sha256
```

在目标机器解压后执行：

```sh
sha256sum -c sync-diff-db2-linux-amd64.tar.gz.sha256
tar -xzf sync-diff-db2-linux-amd64.tar.gz
cd sync-diff-db2-linux-amd64
./run.sh --version
cp config.example.toml config.toml
# 编辑 config.toml，填写 DB2 和 TiDB 连接信息
./run.sh
```

如需指定其他配置文件：`./run.sh /path/to/config.toml`。
`./run.sh --help` 和 `./run.sh --template norm` 无需配置文件即可执行。
默认输出使用相对路径 `./output`。

可用 `OUTPUT_ROOT` 和 `DB2_TOOL_ROOT` 指定宿主机输出及缓存目录；输出已有同名
包时会拒绝覆盖。旧缓存中的新版 CLI 驱动也会接受 ELF 检查，不能绕过兼容门槛。
如需更换已有驱动，请先移动旧的 `clidriver/`，由脚本下载固定版本。

`scripts/package-linux.sh` 是容器内入口，也可在已配置 Go、GCC、Python 3.9+、
readelf 和 IBM CLI 的旧版 Linux 上直接执行；在新 glibc 主机上直接构建通常
无法通过兼容检查。容器入口已配置所需工具和缓存。

### 在 GitHub 上构建及发布 DB2 Linux 包

[DB2 portable Linux release](.github/workflows/db2-release.yml) 在推送和 PR 时
构建 Linux x86_64 包，并在**未安装任何额外依赖的 CentOS 7 容器**中解压启动
程序及 IBM CLI 校验工具。普通运行的包可从 Actions artifacts 下载。

提交并推送工作流及修改后，在需要发布的提交上创建新标签：

```sh
git tag db2-v0.1.0
git push origin db2-v0.1.0
```

只有构建和旧系统检查成功才会自动创建 GitHub Release，并上传包和 SHA-256。
使用 GitHub 内置令牌，不需要另配密钥。手动运行按钮要求工作流已位于默认分支。
此工作流仅发布 DB2 Linux x86_64 包；Windows 使用上面的现有打包脚本。
DB2 ARM64 需要匹配架构的 IBM 原生 CLI 驱动，不使用 x86_64 驱动冒充支持。

## MySQL/TiDB 跨平台包

除上面的 DB2 Linux 包外，以下流程发布不含 DB2 原生驱动的 MySQL/TiDB 包。
普通包使用 `v*` 标签发布，DB2 包使用 `db2-v*` 标签发布。

## Download packages

GitHub Releases provide the following portable archives. Extract the archive;
no Go installation is needed on the destination machine.

| System | CPU | Archive suffix |
| --- | --- | --- |
| Linux | x86_64 / amd64 | `linux-amd64.tar.gz` |
| Linux | ARM64 / aarch64 | `linux-arm64.tar.gz` |
| Windows 10+ / Server 2016+ | x64 / amd64 | `windows-amd64.zip` |
| Kylin V10 | x86_64 / amd64 | `kylin-v10-amd64.tar.gz` |
| Kylin V10 | ARM64 / aarch64 | `kylin-v10-arm64.tar.gz` |

Every archive includes `sync_diff_inspector` (or `sync_diff_inspector.exe`),
sample configuration files in `config/`, usage instructions, and the license.
Edit `config/config.toml` with your database connection information, then run:

```sh
# Linux / Kylin V10
./sync_diff_inspector --version
./sync_diff_inspector --config config/config.toml
```

```powershell
# Windows PowerShell
.\sync_diff_inspector.exe --version
.\sync_diff_inspector.exe --config config/config.toml
```

Linux and Kylin archives use the same statically linked executable
(`CGO_ENABLED=0`), without a glibc dependency. `amd64` builds use the baseline
`GOAMD64=v1` instruction set. Match the package to the machine's CPU architecture
(`uname -m` on Linux/Kylin). Kylin V10 packages still need verification on your
actual target machines.
LoongArch, MIPS and other CPU architectures are not included.
GitHub Actions smoke-tests the Linux executables on Ubuntu and the Windows
executable on Windows; it does not run a Kylin V10 virtual machine or database
integration tests.

These packages contain the current branch's MySQL/TiDB functionality. They do
not include a DB2 driver. A DB2 build using IBM's native `clidriver` needs a
separate compatibility check for that driver's glibc/libstdc++ requirements;
disabling CGO is not a solution for a driver that requires CGO.

Release assets also include `checksums.txt`. Verify downloads with
`sha256sum --check checksums.txt` on Linux/Kylin (download all archives for that
command), or compare an individual file using `sha256sum <archive>` or
`Get-FileHash <archive> -Algorithm SHA256` in PowerShell.

## Build and publish on GitHub

The [Build and release workflow](.github/workflows/release.yml) builds Linux
amd64, Linux arm64 and Windows amd64 on GitHub-hosted runners. A push, pull
request, or manual **Actions → Build and release → Run workflow** run builds and
smoke-tests all three targets. Download the packages from the run's artifacts
(retained for 14 days). For the manual run button to appear, GitHub requires the
workflow to exist on the repository's default branch.

To publish a release, first push this workflow and your changes to the repository,
then create a **new** version tag on the commit you want to release:

```sh
git switch feature/sync-diff-standalone
git tag v0.1.0
git push origin feature/sync-diff-standalone
git push origin v0.1.0
```

A `v*` tag triggers all builds. Only when every build and smoke test succeeds
does the workflow create a GitHub Release and upload the five archives and
checksums. The workflow uses GitHub's built-in `GITHUB_TOKEN`; no personal token
or additional secrets are needed. Repository/organization policy must allow
GitHub Actions and the release job's `contents: write` permission. Use a new
tag for each release; publishing an already existing Release fails rather than
replacing its assets.

To build the same archives locally, install Go 1.23.12 and Python 3, then run:

```sh
python3 scripts/build-release.py --os linux --arch amd64 --version v0.1.0
python3 scripts/build-release.py --os linux --arch arm64 --version v0.1.0
python3 scripts/build-release.py --os windows --arch amd64 --version v0.1.0
```

The script cross-compiles with CGO disabled and writes archives to `dist/`.
It embeds the version, Git commit, branch/ref and UTC build time in the binary.
Set `GOFLAGS=-p=2` to reduce compiler parallelism on machines with limited RAM.

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
