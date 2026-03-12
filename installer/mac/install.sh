#!/bin/bash
# ============================================
# AutoTo 一鍵安裝腳本 (macOS)
# ============================================

set -e

echo ""
echo " AutoTo 一鍵安裝"
echo " ================================"
echo ""

INSTALL_DIR="$HOME/.autoto"
REPO_DIR="$INSTALL_DIR/app"
BACKEND_DIR="$REPO_DIR/backend"
WEB_UI_DIR="$REPO_DIR/renderer"

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

# 3. 建立安裝目錄
echo ""
echo " [3/9] 建立安裝目錄..."
mkdir -p "$INSTALL_DIR" "$REPO_DIR"

# 4. 複製應用程式檔案
echo " [4/9] 安裝應用程式檔案..."
SCRIPT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
if [ ! -d "$SCRIPT_DIR/backend" ] || [ ! -d "$SCRIPT_DIR/electron-app/renderer" ]; then
    echo "  [X] 找不到 backend 或 renderer 檔案"
    echo "  請在完整的 AutoTo 專案目錄中執行安裝腳本"
    exit 1
fi
rm -rf "$BACKEND_DIR" "$WEB_UI_DIR"
cp -R "$SCRIPT_DIR/backend" "$BACKEND_DIR"
cp -R "$SCRIPT_DIR/electron-app/renderer" "$WEB_UI_DIR"
cp "$SCRIPT_DIR/start.sh" "$REPO_DIR/" 2>/dev/null || true

# 5. 建立虛擬環境
echo ""
echo " [5/9] 建立 Python 虛擬環境..."
$PYTHON -m venv "$INSTALL_DIR/venv"
source "$INSTALL_DIR/venv/bin/activate"

# 6. 安裝依賴
echo " [6/9] 安裝依賴..."
pip install --quiet --upgrade pip
pip install --quiet -r "$BACKEND_DIR/requirements.txt"

# 7. 安裝平台依賴
echo ""
echo " [7/9] 安裝平台依賴..."
echo "  選擇要安裝的聊天平台："
echo "  1) Discord"
echo "  2) LINE"
echo "  3) Telegram"
echo "  4) 微信"
echo "  5) Slack"
echo "  6) 全部安裝"
echo "  0) 跳過"
echo ""
PLATFORM_CHOICE="${AUTOTO_PLATFORM_CHOICE:-}"
if [[ -z "$PLATFORM_CHOICE" ]]; then
    if [[ -t 0 ]]; then
        read -p "  請輸入選項（可多選，如 1,3）: " PLATFORM_CHOICE
    else
        PLATFORM_CHOICE="0"
        echo "  非互動模式，跳過平台依賴"
    fi
fi

install_platform() {
    case $1 in
        1) pip install --quiet discord.py && echo "  [OK] Discord" ;;
        2) pip install --quiet line-bot-sdk && echo "  [OK] LINE" ;;
        3) pip install --quiet python-telegram-bot && echo "  [OK] Telegram" ;;
        4) pip install --quiet xmltodict && echo "  [OK] 微信" ;;
        5) pip install --quiet slack-bolt && echo "  [OK] Slack" ;;
    esac
}

if [[ "$PLATFORM_CHOICE" == "6" ]]; then
    for i in 1 2 3 4 5; do install_platform $i; done
elif [[ "$PLATFORM_CHOICE" != "0" ]]; then
    IFS=',' read -ra CHOICES <<< "$PLATFORM_CHOICE"
    for c in "${CHOICES[@]}"; do install_platform "$(echo $c | tr -d ' ')"; done
fi

# 8. 建立啟動腳本
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
    echo 'export PATH="$HOME/.local/bin:$PATH"' >> "$HOME/.zshrc"
    export PATH="$HOME/.local/bin:$PATH"
    echo "  已將 ~/.local/bin 加入 PATH"
fi

# 9. 初始化配置
echo ""
echo " [9/9] 初始化配置..."
mkdir -p "$INSTALL_DIR"
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
  "agent": {"maxTokenBudget": 4000, "compressionEnabled": true, "systemPrompt": "You are AutoTo, an open-source cross-platform AI assistant. Reply in the same language the user uses. AutoTo supports macOS and Windows. GitHub: https://github.com/hokidev26/autoto. You are a web-based AI assistant, not tied to any specific OS. Do not fabricate information you do not know."},
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
