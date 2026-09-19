#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
IBM_DB_HOME="$ROOT/clidriver"
export IBM_DB_HOME
export LD_LIBRARY_PATH="$IBM_DB_HOME/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

if [ ! -x "$ROOT/sync_diff_inspector" ]; then
  echo "[ERROR] 缺少可执行文件：$ROOT/sync_diff_inspector" >&2
  exit 1
fi

if [ ! -e "$IBM_DB_HOME/lib/libdb2.so" ]; then
  echo "[ERROR] IBM CLI 驱动不完整：$IBM_DB_HOME" >&2
  exit 1
fi

CONFIG=${1:-"$ROOT/config.toml"}
if [ ! -f "$CONFIG" ]; then
  echo "[ERROR] 缺少配置文件：$CONFIG" >&2
  echo "请把 config.example.toml 复制为 config.toml 并填写连接配置。" >&2
  exit 1
fi

exec "$ROOT/sync_diff_inspector" -C "$CONFIG"
