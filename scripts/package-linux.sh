#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
PACKAGE_NAME=sync-diff-db2-linux-amd64
OUTPUT_ROOT=${OUTPUT_ROOT:-"$REPO_ROOT/release"}
TOOL_ROOT=${DB2_TOOL_ROOT:-"$REPO_ROOT/.local-tools-linux"}
IBM_DB_HOME="$TOOL_ROOT/clidriver"
VALIDATE_ONLY=0

if [ "${1:-}" = "--validate-only" ]; then
  VALIDATE_ONLY=1
elif [ "$#" -gt 0 ]; then
  echo "用法：$0 [--validate-only]" >&2
  exit 2
fi

if [ "$(uname -s)" != "Linux" ]; then
  echo '该脚本只能在 Linux 或 WSL 中运行。' >&2
  exit 1
fi
case "$(uname -m)" in
  x86_64|amd64) ;;
  *) echo '当前打包脚本只支持 Linux amd64。' >&2; exit 1 ;;
esac

for command_name in go git gcc tar sha256sum ldd grep; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "缺少构建命令：$command_name" >&2
    exit 1
  fi
done

PACKAGE_DIR="$OUTPUT_ROOT/$PACKAGE_NAME"
ARCHIVE_PATH="$OUTPUT_ROOT/$PACKAGE_NAME.tar.gz"
ARCHIVE_HASH_PATH="$ARCHIVE_PATH.sha256"

check_inputs() {
  for path in \
    "$IBM_DB_HOME/include/sqlcli.h" \
    "$IBM_DB_HOME/lib/libdb2.so" \
    "$REPO_ROOT/packaging/run.sh" \
    "$REPO_ROOT/sync_diff_inspector/config/config_db2.toml" \
    "$REPO_ROOT/README.md" \
    "$REPO_ROOT/LICENSE"
  do
    if [ ! -e "$path" ]; then
      echo "打包输入不存在：$path" >&2
      return 1
    fi
  done
}

if [ "$VALIDATE_ONLY" -eq 1 ]; then
  check_inputs
  echo 'Linux 打包输入与本机构建工具检查通过。'
  exit 0
fi

if [ -e "$PACKAGE_DIR" ] || [ -e "$ARCHIVE_PATH" ] || [ -e "$ARCHIVE_HASH_PATH" ]; then
  echo "发布输出已经存在，请先移动或删除后重试：$PACKAGE_DIR 或 $ARCHIVE_PATH" >&2
  exit 1
fi

if [ ! -e "$IBM_DB_HOME/include/sqlcli.h" ] || [ ! -e "$IBM_DB_HOME/lib/libdb2.so" ]; then
  echo '正在下载并准备 Linux IBM CLI 驱动...'
  go install github.com/ibmdb/go_ibm_db/installer@v0.5.4
  INSTALLER_DIR="$(go env GOMODCACHE)/github.com/ibmdb/go_ibm_db@v0.5.4/installer"
  if [ ! -f "$INSTALLER_DIR/setup.go" ]; then
    echo "未找到 go_ibm_db 安装器：$INSTALLER_DIR/setup.go" >&2
    exit 1
  fi
  mkdir -p "$TOOL_ROOT"
  (cd "$INSTALLER_DIR" && go run ./setup.go "$TOOL_ROOT")
fi
check_inputs

export IBM_DB_HOME
export CGO_ENABLED=1
export CGO_CFLAGS="-I$IBM_DB_HOME/include"
export CGO_LDFLAGS="-L$IBM_DB_HOME/lib"
export LD_LIBRARY_PATH="$IBM_DB_HOME/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

VERSION=$(git -C "$REPO_ROOT" describe --tags --dirty --always)
BUILD_TIME=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
GIT_HASH=$(git -C "$REPO_ROOT" rev-parse HEAD)
GIT_BRANCH=$(git -C "$REPO_ROOT" rev-parse --abbrev-ref HEAD)
LDFLAGS="-s -w -X github.com/pingcap/tidb-tools/pkg/utils.Version=$VERSION -X github.com/pingcap/tidb-tools/pkg/utils.BuildTS=$BUILD_TIME -X github.com/pingcap/tidb-tools/pkg/utils.GitHash=$GIT_HASH -X github.com/pingcap/tidb-tools/pkg/utils.GitBranch=$GIT_BRANCH"

mkdir -p "$PACKAGE_DIR"
echo '正在构建 Linux amd64 DB2 版本...'
(cd "$REPO_ROOT" && go build -tags db2cli -trimpath -ldflags "$LDFLAGS" \
  -o "$PACKAGE_DIR/sync_diff_inspector" ./sync_diff_inspector)

cp -R "$IBM_DB_HOME" "$PACKAGE_DIR/clidriver"
cp "$REPO_ROOT/packaging/run.sh" "$PACKAGE_DIR/run.sh"
cp "$REPO_ROOT/sync_diff_inspector/config/config_db2.toml" "$PACKAGE_DIR/config.example.toml"
cp "$REPO_ROOT/README.md" "$REPO_ROOT/LICENSE" "$PACKAGE_DIR/"
chmod +x "$PACKAGE_DIR/sync_diff_inspector" "$PACKAGE_DIR/run.sh"

LDD_OUTPUT=$(LD_LIBRARY_PATH="$PACKAGE_DIR/clidriver/lib" ldd "$PACKAGE_DIR/sync_diff_inspector")
printf '%s\n' "$LDD_OUTPUT"
if printf '%s\n' "$LDD_OUTPUT" | grep -q 'not found'; then
  echo 'Linux 发布包存在未解析的动态库依赖。' >&2
  exit 1
fi

echo '正在生成 tar.gz 发布包...'
tar -C "$OUTPUT_ROOT" -czf "$ARCHIVE_PATH" "$PACKAGE_NAME"
(cd "$OUTPUT_ROOT" && sha256sum "$(basename "$ARCHIVE_PATH")" > "$(basename "$ARCHIVE_HASH_PATH")")

echo "Linux 发布包：$ARCHIVE_PATH"
echo "SHA-256 文件：$ARCHIVE_HASH_PATH"
echo '目标机器解压、复制 config.example.toml 为 config.toml 后，只需执行 ./run.sh。'
