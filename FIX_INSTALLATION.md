# 🔧 修復安裝問題

## 問題診斷

你遇到的問題：
1. ❌ 沒有安裝 Homebrew
2. ❌ Python 版本是 3.9.6（AutoTo 需要 3.11+）
3. ❌ 虛擬環境使用舊版 Python

## 解決方案

### 步驟 1: 安裝 Homebrew

```bash
# 複製這整行指令到終端機執行
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

安裝過程中：
- 會要求輸入密碼（你的 Mac 登入密碼）
- 可能需要 5-10 分鐘
- 安裝完成後，按照提示執行額外的指令（如果有的話）

### 步驟 2: 安裝 Python 3.11

```bash
# 安裝 Python 3.11
brew install python@3.11

# 驗證安裝
python3.11 --version
# 應該顯示: Python 3.11.x
```

### 步驟 3: 刪除舊的虛擬環境

```bash
# 進入你的 AutoTo 專案目錄
cd /path/to/autoto

# 刪除舊的虛擬環境
rm -rf venv
```

### 步驟 4: 用 Python 3.11 建立新的虛擬環境

```bash
# 建立新的虛擬環境
python3.11 -m venv venv

# 啟動虛擬環境
source venv/bin/activate

# 驗證 Python 版本
python --version
# 應該顯示: Python 3.11.x
```

### 步驟 5: 安裝 AutoTo

```bash
# 回到專案目錄
cd /path/to/autoto

# 安裝後端依賴
python3.11 -m pip install -r backend/requirements.txt
```

### 步驟 6: 設定 API Key

```bash
# 編輯設定檔
open ~/.autoto/config.json
```

貼上以下內容（記得替換成你的 API Key）：

```json
{
  "provider": "groq",
  "apiKey": "你的KEY",
  "model": "llama-3.3-70b-versatile",
  "customUrl": ""
}
```

**取得 API Key：**
1. 前往 https://openrouter.ai/
2. 註冊帳號
3. 前往 https://openrouter.ai/keys
4. 建立新的 API Key
5. 最低儲值 $5 USD

### 步驟 7: 測試

```bash
# 啟動 AutoTo
./start.sh

# 然後打開 http://127.0.0.1:5678
```

## 完整的一次性指令（複製貼上）

如果你想一次執行所有步驟，可以複製以下指令：

```bash
# 1. 安裝 Homebrew（會要求輸入密碼）
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"

# 2. 安裝 Python 3.11
brew install python@3.11

# 3. 進入目錄並重建虛擬環境
cd /path/to/autoto
rm -rf venv
python3.11 -m venv venv
source venv/bin/activate

# 4. 安裝依賴
pip install -r backend/requirements.txt

# 5. 啟動（需要先設定 API Key）
./start.sh
```

## 常見問題

### Q: Homebrew 安裝失敗

如果看到 "xcode-select: command not found"：

```bash
# 先安裝 Xcode Command Line Tools
xcode-select --install

# 然後重新安裝 Homebrew
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

### Q: brew 指令找不到

安裝 Homebrew 後，可能需要執行額外的指令。查看安裝完成時的提示，通常是：

```bash
# Intel Mac
echo 'eval "$(/usr/local/bin/brew shellenv)"' >> ~/.zshrc
eval "$(/usr/local/bin/brew shellenv)"

# Apple Silicon Mac (M1/M2/M3)
echo 'eval "$(/opt/homebrew/bin/brew shellenv)"' >> ~/.zshrc
eval "$(/opt/homebrew/bin/brew shellenv)"
```

### Q: python3.11 指令找不到

```bash
# 確認 Homebrew 安裝成功
brew --version

# 重新安裝 Python
brew install python@3.11

# 查看安裝位置
which python3.11
```

### Q: `autoto` 指令找不到

```bash
# 重新載入 shell 設定
source ~/.zshrc

# 或直接使用安裝後腳本
~/.autoto/start.sh
```

### Q: API 錯誤

```bash
# 檢查設定檔
cat ~/.autoto/config.json

# 確認 API Key 正確
# 確認有儲值（OpenRouter 需要先儲值）
```

## 需要幫助？

如果還是有問題：
1. 把錯誤訊息完整複製
2. 告訴我你執行到哪一步
3. 我會幫你解決！

## 建議做法

如果只是要安裝與使用 AutoTo，請優先執行：

```bash
bash install.sh
```

安裝完成後再執行：

```bash
autoto
```

---

選擇一個方法開始吧！建議先試試 uv 方案，比較簡單。🚀
