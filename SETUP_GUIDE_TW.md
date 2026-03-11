# AutoTo 部署指南

## 前置準備

### 1. 安裝 Xcode Command Line Tools
```bash
xcode-select --install
```
點擊彈出視窗的「安裝」按鈕，等待安裝完成（約 5-10 分鐘）

### 2. 安裝 Homebrew（Mac 套件管理工具）
```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

### 3. 安裝 Python 3
```bash
brew install python@3.11
```

驗證安裝：
```bash
python3 --version  # 應該顯示 Python 3.11.x
```

### 4. 安裝 Git
```bash
brew install git
```

## 快速部署 AutoTo

### 方法一：從 AutoTo 原始碼開始（推薦）

```bash
# 1. Clone AutoTo 專案
git clone https://github.com/your-username/autoto.git autoto
cd autoto

# 2. 建立虛擬環境
python3 -m venv venv
source venv/bin/activate
  
# 3. 安裝依賴
pip install -r backend/requirements.txt
  
# 4. 啟動 AutoTo
./start.sh
```

### 方法二：使用安裝腳本（推薦）

```bash
bash install.sh
autoto
```

## 設定 API Key

編輯 `~/.autoto/config.json`，加入以下內容：

```json
{
  "provider": "groq",
  "apiKey": "你的KEY",
  "model": "llama-3.3-70b-versatile",
  "customUrl": ""
}
```

### 取得 API Key

1. **OpenRouter**（推薦，支援多種模型）
   - 註冊：https://openrouter.ai/
   - 取得 API Key：https://openrouter.ai/keys
   - 儲值：最低 $5 USD

2. **DeepSeek**（便宜，中文好）
   - 註冊：https://platform.deepseek.com/
   - 取得 API Key
   - 設定：
   ```json
   {
     "providers": {
       "deepseek": {
         "apiKey": "sk-你的KEY"
       }
     },
     "agents": {
       "defaults": {
         "model": "deepseek-chat",
         "provider": "deepseek"
       }
     }
   }
   ```

## 測試運行

```bash
# 啟動 AutoTo
autoto
  
# 查看狀態
curl http://127.0.0.1:5678/api/status
```

## 啟動聊天平台（支援 LINE 等）

```bash
# 啟動 AutoTo 後，在設定頁面啟用對應平台
autoto
```

## 常見問題

### Q: 如何停止 AutoTo？
```bash
# 找到 process
ps aux | grep server.py
  
# 停止
kill <PID>
```

### Q: 如何查看日誌？
```bash
# 查看後端狀態
curl http://127.0.0.1:5678/api/status
```

### Q: 如何更新 AutoTo？
```bash
cd autoto
git pull
bash install.sh
```

## 下一步

完成基本部署後，我們會加入：
1. LINE 整合
2. 台灣天氣工具
3. 統一發票查詢
4. 繁體中文優化

繼續閱讀其他文件：
- `LINE_INTEGRATION.md` - LINE 整合指南
- `TW_TOOLS.md` - 台灣特色工具
- `DEVELOPMENT.md` - 開發指南
