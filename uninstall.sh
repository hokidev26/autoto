#!/bin/bash
# ============================================
# AutoTo 一鍵卸載腳本 (macOS)
# ============================================

echo ""
echo " AutoTo 卸載工具"
echo " ================================"
echo ""

INSTALL_DIR="$HOME/.autoto"
CMD_PATH="$HOME/.local/bin/autoto"

# 確認卸載
if [[ -t 0 ]]; then
    read -p " 確定要卸載 AutoTo 嗎？(y/N): " CONFIRM
    if [[ "$CONFIRM" != "y" && "$CONFIRM" != "Y" ]]; then
        echo " 已取消卸載"
        exit 0
    fi
fi

echo ""

# 1. 刪除安裝目錄 (~/.autoto)
if [ -d "$INSTALL_DIR" ]; then
    echo " [1/3] 刪除安裝目錄 $INSTALL_DIR ..."
    rm -rf "$INSTALL_DIR"
    echo "  [OK] 已刪除"
else
    echo " [1/3] 安裝目錄不存在，跳過"
fi

# 2. 刪除命令行捷徑
if [ -f "$CMD_PATH" ]; then
    echo " [2/3] 刪除命令行捷徑 $CMD_PATH ..."
    rm -f "$CMD_PATH"
    echo "  [OK] 已刪除"
else
    echo " [2/3] 命令行捷徑不存在，跳過"
fi

# 3. 清理 .zshrc 中的 PATH 設定
echo " [3/3] 清理 shell 設定..."
if [ -f "$HOME/.zshrc" ]; then
    if grep -q '.local/bin' "$HOME/.zshrc"; then
        sed -i '' '/export PATH="\$HOME\/.local\/bin:\$PATH"/d' "$HOME/.zshrc"
        echo "  [OK] 已從 .zshrc 移除 PATH 設定"
    else
        echo "  無需清理"
    fi
else
    echo "  無需清理"
fi

echo ""
echo " ================================"
echo " AutoTo 已完全卸載！"
echo " ================================"
echo ""
