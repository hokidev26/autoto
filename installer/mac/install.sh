#!/bin/bash
# ============================================
# AutoTo 一鍵安裝腳本 (macOS)
# ============================================

set -e

echo ""
echo "🤖 AutoTo 一鍵安裝"
echo "================================"
echo ""

INSTALL_DIR="$HOME/.autoto"
REPO_DIR="$INSTALL_DIR/app"
BACKEND_DIR="$REPO_DIR/backend"
WEB_UI_DIR="$REPO_DIR/renderer"

  # 1. 檢查 Python
  echo "📦 檢查 Python..."
  PYTHON=""
  if command -v python3.11 &> /dev/null; then
      PYTHON=python3.11
  elif command -v python3 &> /dev/null && python3 -c 'import sys; raise SystemExit(0 if sys.version_info >= (3, 11) else 1)' 2>/dev/null; then
      PYTHON=python3
  else
      echo "  ⚠️  目前找不到可用的 Python 3.11+，正在透過 Homebrew 安裝..."
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
  echo "  ✅ Python $PY_VERSION"

  # 2. 建立安裝目錄
  echo ""
echo "📁 建立安裝目錄..."
mkdir -p "$INSTALL_DIR" "$REPO_DIR"

# 3. 複製應用程式檔案
echo "📋 安裝應用程式檔案..."
SCRIPT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
if [ ! -d "$SCRIPT_DIR/backend" ] || [ ! -d "$SCRIPT_DIR/electron-app/renderer" ]; then
    echo "  ❌ 找不到 backend 或 renderer 檔案"
    echo "  請在完整的 AutoTo 專案目錄中執行安裝腳本"
    exit 1
fi
rm -rf "$BACKEND_DIR" "$WEB_UI_DIR"
cp -R "$SCRIPT_DIR/backend" "$BACKEND_DIR"
cp -R "$SCRIPT_DIR/electron-app/renderer" "$WEB_UI_DIR"

# 4. 建立虛擬環境
echo "🐍 建立 Python 虛擬環境..."
$PYTHON -m venv "$INSTALL_DIR/venv"
source "$INSTALL_DIR/venv/bin/activate"

# 5. 安裝依賴
echo "📦 安裝依賴..."
pip install --quiet --upgrade pip
pip install --quiet -r "$BACKEND_DIR/requirements.txt"

# 6. 安裝平台依賴（詢問用戶）
echo ""
echo "📱 選擇要安裝的聊天平台依賴："
echo "  1) Discord"
echo "  2) LINE"
echo "  3) Telegram"
echo "  4) 微信"
echo "  5) Slack"
echo "  6) 全部安裝"
echo "  0) 跳過（之後可手動安裝）"
echo ""
PLATFORM_CHOICE="${AUTOTO_PLATFORM_CHOICE:-}"
if [[ -z "$PLATFORM_CHOICE" ]]; then
    if [[ -t 0 ]]; then
        read -p "請輸入選項（可多選，如 1,3）: " PLATFORM_CHOICE
    else
        PLATFORM_CHOICE="0"
        echo "非互動模式，預設跳過平台依賴安裝"
    fi
else
    echo "使用 AUTOTO_PLATFORM_CHOICE=$PLATFORM_CHOICE"
fi

install_platform() {
    case $1 in
        1) pip install --quiet discord.py && echo "  ✅ Discord 依賴已安裝" ;;
        2) pip install --quiet line-bot-sdk && echo "  ✅ LINE 依賴已安裝" ;;
        3) pip install --quiet python-telegram-bot && echo "  ✅ Telegram 依賴已安裝" ;;
        4) pip install --quiet xmltodict && echo "  ✅ 微信依賴已安裝" ;;
        5) pip install --quiet slack-bolt && echo "  ✅ Slack 依賴已安裝" ;;
    esac
}

if [[ "$PLATFORM_CHOICE" == "6" ]]; then
    for i in 1 2 3 4 5; do install_platform $i; done
elif [[ "$PLATFORM_CHOICE" != "0" ]]; then
    IFS=',' read -ra CHOICES <<< "$PLATFORM_CHOICE"
    for c in "${CHOICES[@]}"; do install_platform "$(echo $c | tr -d ' ')"; done
fi

# 7. 建立啟動腳本
echo ""
echo "🔧 建立啟動腳本..."
cat > "$INSTALL_DIR/start.sh" << 'STARTEOF'
#!/bin/bash
source "$HOME/.autoto/venv/bin/activate"
cd "$HOME/.autoto/app/backend"
python server.py "$@"
STARTEOF
chmod +x "$INSTALL_DIR/start.sh"

# 8. 建立命令行捷徑
echo "🔗 建立命令行捷徑..."
mkdir -p "$HOME/.local/bin"
cat > "$HOME/.local/bin/autoto" << 'CMDEOF'
#!/bin/bash
"$HOME/.autoto/start.sh" "$@"
CMDEOF
chmod +x "$HOME/.local/bin/autoto"

  # 確保 PATH 包含 ~/.local/bin
  if [[ ":$PATH:" != *":$HOME/.local/bin:"* ]]; then
      echo 'export PATH="$HOME/.local/bin:$PATH"' >> "$HOME/.zshrc"
      export PATH="$HOME/.local/bin:$PATH"
      echo '  已將 ~/.local/bin 加入 PATH'
  fi
  
  # 9. 初始化配置
  echo ""
echo "⚙️ 初始化配置..."
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
  "agent": {"maxTokenBudget": 4000, "compressionEnabled": true, "systemPrompt": "你是 AutoTo，一個智能 AI 助理。請用繁體中文回答，語氣友善親切。"},
  "session": {"persist": true}
}
CFGEOF
    echo "  ✅ 預設配置已建立"
fi

echo ""
echo "================================"
echo "🎉 AutoTo 安裝完成！"
echo ""
echo "啟動方式："
echo "  autoto                    # 啟動 AutoTo"
echo "  autoto --port 8080        # 指定端口"
echo ""
echo "接下來："
echo "  1. 執行 autoto"
echo "  2. 在瀏覽器開啟 http://127.0.0.1:5678"
echo "  3. 在設定頁面配置 API Key"
echo "  4. 在平台頁面啟用想要的聊天平台"
echo "================================"
