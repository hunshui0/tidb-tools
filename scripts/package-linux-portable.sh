#!/usr/bin/env sh
# Host entry point: Docker is the only required build tool.
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
OUTPUT_ROOT=${OUTPUT_ROOT:-"$REPO_ROOT/release"}
TOOL_ROOT=${DB2_TOOL_ROOT:-"$REPO_ROOT/.local-tools-linux"}

if [ "$#" -ne 0 ]; then
  echo "用法：$0（可设置 OUTPUT_ROOT、DB2_TOOL_ROOT）" >&2
  exit 2
fi
for command_name in docker git; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "缺少命令：$command_name" >&2; exit 1; }
done
case "$(uname -m)" in
  x86_64|amd64) ;;
  *) echo 'DB2 Linux 便携构建目前仅支持 x86_64 主机及驱动。' >&2; exit 1 ;;
esac
mkdir -p "$OUTPUT_ROOT" "$TOOL_ROOT/cache"
OUTPUT_ROOT=$(CDPATH= cd -- "$OUTPUT_ROOT" && pwd)
TOOL_ROOT=$(CDPATH= cd -- "$TOOL_ROOT" && pwd)

# Pass metadata explicitly so Git worktrees also work inside the container.
BUILD_VERSION=${BUILD_VERSION:-$(git -C "$REPO_ROOT" describe --tags --dirty --always)}
BUILD_GIT_HASH=$(git -C "$REPO_ROOT" rev-parse HEAD)
BUILD_GIT_BRANCH=$(git -C "$REPO_ROOT" rev-parse --abbrev-ref HEAD)

docker build -f "$REPO_ROOT/packaging/Dockerfile.linux" \
  -t sync-diff-db2-linux-builder "$REPO_ROOT/packaging"
docker run --rm --user "$(id -u):$(id -g)" \
  -v "$REPO_ROOT:/src:ro" \
  -v "$OUTPUT_ROOT:/out" \
  -v "$TOOL_ROOT:/tools" \
  -v "$TOOL_ROOT/cache:/cache" \
  -e OUTPUT_ROOT=/out -e DB2_TOOL_ROOT=/tools \
  -e BUILD_VERSION="$BUILD_VERSION" \
  -e BUILD_GIT_HASH="$BUILD_GIT_HASH" \
  -e BUILD_GIT_BRANCH="$BUILD_GIT_BRANCH" \
  sync-diff-db2-linux-builder sh scripts/package-linux.sh
