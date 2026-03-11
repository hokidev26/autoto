#!/bin/bash
set -e

echo "🤖 AutoTo 安裝"
echo ""

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PLATFORM="$(uname -s)"

if [ "$PLATFORM" = "Darwin" ]; then
    exec bash "$SCRIPT_DIR/installer/mac/install.sh"
fi

if [ "$PLATFORM" = "Linux" ]; then
    echo "⚠️  目前 repo 內只內建 macOS 與 Windows 安裝器"
    echo "請參考 installer/mac/install.sh 自行調整 Linux 安裝流程"
    exit 1
fi

echo "⚠️  請在 Windows 上執行 installer\\windows\\install.bat"
exit 1
