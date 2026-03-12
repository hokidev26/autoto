#!/bin/bash
# ============================================
# AutoTo 一鍵安裝腳本 (macOS 線上安裝)
# 使用方式: curl -fsSL https://raw.githubusercontent.com/hokidev26/autoto/main/install_mac.sh | bash
# ============================================

set -e

echo ""
echo " AutoTo 一鍵安裝 (macOS)"
echo " ================================"
echo ""

INSTALL_DIR="$HOME/.autoto"
REPO_DIR="$INSTALL_DIR/app"
BACKEND_DIR="$REPO_DIR/backend"
WEB_UI_DIR="$REPO_DIR/renderer"
TEMP_ZIP="/tmp/autoto.zip"
TEMP_EXTRACT="/tmp/autoto-main"
REPO_URL="https://github.com/hokidev26/autoto/archive/refs/heads/main.zip"

# 1. 檢查 Python
echo " [1/9] 檢查 Python..."
PYTHON=""
if command -v python3.11 &> /dev/null; then
    PYTHON=python3.11
elif command -v python3.12 &> /dev/null; then
    PYTHON=python3.12
elif command -v python3.13 &> /dev/null; then
    PYTHON=python3.13
elif command -v python3 &> /dev/null && python3 -c 'import sys; raise SystemExit(0 if sys.version_info >= (3, 11) else 1)' 2>/dev/null; then
    PYTHON=python3
else
    echo "  [!] 未找到 Python 3.11+，正在透過 Homebrew 安裝..."
    if ! command -v brew &> /dev/null; then
        echo "  安裝 Homebrew..."
        /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
        if [[ $(uname -m) == 'arm64' ]]; then
            eval "$('/opt/homebrew/bin/brew' shellenv)"
        fi
    fi
    brew install python@3.11
    PYTHON="$(brew --prefix python@3.11)/bin/python3.11"
fi
PY_VERSION=$($PYTHON --version 2>&1 | awk '{print $2}')
echo "  [OK] Python $PY_VERSION"

# 2. 檢查 ffmpeg
echo ""
echo " [2/9] 檢查 ffmpeg..."
if command -v ffmpeg &> /dev/null; then
    echo "  [OK] ffmpeg 已安裝"
else
    echo "  [!] 未找到 ffmpeg，攝影機監控功能將無法使用"
    echo "  如需使用: brew install ffmpeg"
fi

# 3. 下載專案
echo ""
echo " [3/9] 下載 AutoTo..."
rm -f "$TEMP_ZIP"
rm -rf "$TEMP_EXTRACT"
curl -fsSL "$REPO_URL" -o "$TEMP_ZIP"
echo "  [OK] 下載完成"

# 4. 解壓並安裝檔案
echo ""
echo " [4/9] 安裝應用程式檔案..."
unzip -qo "$TEMP_ZIP" -d /tmp
# GitHub zip 解壓後資料夾名稱為 autoto-main
if [ ! -d "$TEMP_EXTRACT" ]; then
    TEMP_EXTRACT=$(find /tmp -maxdepth 1 -name "autoto-*" -type d | head -1)
fi

if [ ! -d "$TEMP_EXTRACT/backend" ] || [ ! -d "$TEMP_EXTRACT/electron-app/renderer" ]; then
    echo "  [X] 下載的檔案不完整"
    exit 1
fi

mkdir -p "$INSTALL_DIR" "$REPO_DIR"
rm -rf "$BACKEND_DIR" "$WEB_UI_DIR"
cp -R "$TEMP_EXTRACT/backend" "$BACKEND_DIR"
cp -R "$TEMP_EXTRACT/electron-app/renderer" "$WEB_UI_DIR"
cp "$TEMP_EXTRACT/start.sh" "$REPO_DIR/" 2>/dev/null || true

# 清理暫存
rm -f "$TEMP_ZIP"
rm -rf "$TEMP_EXTRACT"
echo "  [OK] 檔案安裝完成"

# 5. 建立虛擬環境
echo ""
echo " [5/9] 建立 Python 虛擬環境..."
$PYTHON -m venv "$INSTALL_DIR/venv"
source "$INSTALL_DIR/venv/bin/activate"
echo "  [OK] 虛擬環境建立完成"

# 6. 安裝依賴
echo ""
echo " [6/9] 安裝依賴（可能需要幾分鐘）..."
pip install --quiet --upgrade pip
pip install --quiet -r "$BACKEND_DIR/requirements.txt"
echo "  [OK] 依賴安裝完成"

# 7. 安裝平台依賴
echo ""
echo " [7/9] 安裝平台依賴..."
pip install --quiet discord.py line-bot-sdk python-telegram-bot xmltodict slack-bolt 2>/dev/null
if [ $? -eq 0 ]; then
    echo "  [OK] 平台依賴安裝完成"
else
    echo "  [!] 部分平台依賴安裝失敗，不影響主程式"
fi

# 8. 建立啟動腳本與命令行捷徑
echo ""
echo " [8/9] 建立啟動腳本..."
cat > "$INSTALL_DIR/start.sh" << 'STARTEOF'
#!/bin/bash
PORT="${1:-5678}"
source "$HOME/.autoto/venv/bin/activate"
cd "$HOME/.autoto/app/backend"
echo ""
echo " AutoTo 啟動中..."
echo " 瀏覽器介面: http://127.0.0.1:$PORT"
echo " 按 Ctrl+C 停止"
echo ""
open "http://127.0.0.1:$PORT" 2>/dev/null &
python server.py --port "$PORT"
STARTEOF
chmod +x "$INSTALL_DIR/start.sh"

# 命令行捷徑
mkdir -p "$HOME/.local/bin"
cat > "$HOME/.local/bin/autoto" << 'CMDEOF'
#!/bin/bash
"$HOME/.autoto/start.sh" "$@"
CMDEOF
chmod +x "$HOME/.local/bin/autoto"

if [[ ":$PATH:" != *":$HOME/.local/bin:"* ]]; then
    SHELL_RC="$HOME/.zshrc"
    [ -n "$BASH_VERSION" ] && SHELL_RC="$HOME/.bashrc"
    echo 'export PATH="$HOME/.local/bin:$PATH"' >> "$SHELL_RC"
    export PATH="$HOME/.local/bin:$PATH"
    echo "  已將 ~/.local/bin 加入 PATH"
fi
echo "  [OK] 啟動腳本建立完成"

# 9. 初始化配置
echo ""
echo " [9/9] 初始化配置..."
if [ ! -f "$INSTALL_DIR/config.json" ]; then
    cat > "$INSTALL_DIR/config.json" << 'CFGEOF'
{
  "provider": "groq",
  "apiKey": "",
  "model": "llama-3.3-70b-versatile",
  "customUrl": "",
  "channels": {
    "discord": {"enabled": false, "token": ""},
    "line": {"enabled": false, "channelAccessToken": "", "channelSecret": ""},
    "telegram": {"enabled": false, "botToken": ""},
    "wechat": {"enabled": false, "appId": "", "appSecret": ""},
    "whatsapp": {"enabled": false, "phoneNumberId": "", "accessToken": "", "verifyToken": ""},
    "slack": {"enabled": false, "botToken": "", "signingSecret": ""},
    "messenger": {"enabled": false, "pageAccessToken": "", "verifyToken": ""},
    "qq": {"enabled": false, "httpUrl": "http://127.0.0.1:5700", "webhookPort": 5683},
    "instagram": {"enabled": false, "accessToken": ""}
  },
  "memory": {"enabled": true, "autoArchive": 50},
  "agent": {"maxTokenBudget": 4000, "compressionEnabled": true, "systemPrompt": "你是 AutoTo，一個開源跨平台 AI 助理。請用繁體中文回答，語氣友善親切。AutoTo 支援 macOS 和 Windows，GitHub: https://github.com/hokidev26/autoto。你不是某個特定作業系統的程式，你是 Web AI 助理。不要編造你不知道的資訊。"},
  "session": {"persist": true},
  "cameras": [],
  "smarthome": {"platforms": []}
}
CFGEOF
    echo "  [OK] 預設配置已建立"
fi

echo ""
echo " ================================"
echo " AutoTo 安裝完成！"
echo " ================================"
echo ""
echo " 啟動方式："
echo "   autoto                 # 啟動（預設 port 5678）"
echo "   autoto 8080            # 指定 port"
echo ""
echo " 首次使用："
echo "   1. 執行 autoto"
echo "   2. 瀏覽器會自動開啟"
echo "   3. 在設定頁面配置 API Key（推薦 Groq 免費）"
echo "   4. 開始對話！"
echo ""
echo " 安裝位置: $INSTALL_DIR"
echo " ================================"
