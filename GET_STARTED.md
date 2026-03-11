# 🚀 AutoTo 入門指南
 
 歡迎使用 AutoTo！這份文件會帶你快速上手。

## 📋 目錄

1. [快速部署](#快速部署)
2. [基本使用](#基本使用)
3. [LINE 整合](#line-整合)
4. [台灣工具](#台灣工具)
5. [進階設定](#進階設定)
6. [常見問題](#常見問題)

 ## 快速部署
 
 ### 前置需求
 
 - macOS 或 Windows
 - 網路連線
 - 約 10 分鐘時間
 
 ### 一鍵安裝
 
 macOS：
 
 ```bash
 bash install.sh
 ```
 
 Windows：
 
 ```bat
 installer\windows\install.bat
 ```
 
 安裝流程會自動：
 1. 建立 `~/.autoto` 安裝目錄
 2. 複製 `backend/` 與 `renderer/`
 3. 建立 Python 虛擬環境
 4. 安裝 `backend/requirements.txt`
 5. 產生啟動腳本
 
 ### 直接在 repo 中測試
 
 ```bash
 python3 -m pip install -r backend/requirements.txt
 ./start.sh
 ```
 
 ## 基本使用
 
 ### 1. 取得 API Key
 
 你需要一個 LLM API Key 才能使用。推薦選項：
 
 **選項 A: Groq（預設）**
 - 網址：https://console.groq.com/keys
 - 優點：設定簡單、回應快
 
 **選項 B: OpenRouter**
 - 網址：https://openrouter.ai/keys
 - 優點：支援多種模型
 
 **選項 C: DeepSeek**
 - 網址：https://platform.deepseek.com/
 - 優點：中文表現好
 
 ### 2. 設定 API Key
 
 你可以直接在瀏覽器設定頁面輸入，或手動編輯：
 
 ```bash
 open ~/.autoto/config.json
 ```
 
 範例：
 
 ```json
 {
   "provider": "groq",
   "apiKey": "你的KEY",
   "model": "llama-3.3-70b-versatile",
   "customUrl": ""
 }
 ```
 
 ### 3. 啟動並測試
 
 ```bash
 # 安裝後啟動
 autoto
 
 # 或直接在 repo 啟動
 ./start.sh
 
 # 測試狀態
 curl http://127.0.0.1:5678/api/status
 ```
 
 成功！🎉
 
 ### 4. 開始互動
 
 在瀏覽器打開：
 
 ```
 http://127.0.0.1:5678
 ```
 
 然後：
 - 在聊天頁面直接輸入訊息
 - 在設定頁面切換 Provider / Model
 - 在平台頁面啟用聊天平台

## LINE 整合

### 為什麼要整合 LINE？

- 台灣最多人使用的聊天平台
- 隨時隨地用手機與 AI 對話
- 支援圖片、語音、位置等多媒體
- 可以分享給朋友使用

### 快速設定（5 分鐘）

#### 步驟 1: 申請 LINE 官方帳號

1. 前往 https://developers.line.biz/
2. 用你的 LINE 帳號登入
3. 建立新的 Provider（隨便取個名字）
4. 建立 Messaging API Channel
5. 取得兩個重要資訊：
   - **Channel Secret**（在 Basic settings）
   - **Channel Access Token**（在 Messaging API，需要點「Issue」）

#### 步驟 2: 設定 Webhook（本地測試）

```bash
# 安裝 ngrok
brew install ngrok

# 啟動 ngrok（開一個新終端機視窗）
ngrok http 8000

# 會看到類似這樣的輸出：
# Forwarding  https://abc123.ngrok.io -> http://localhost:8000
```

複製 `https://abc123.ngrok.io` 這個網址。

#### 步驟 3: 在 LINE Developers 設定

1. 回到 LINE Developers 的 Messaging API 頁面
2. 找到「Webhook URL」
3. 填入：`https://abc123.ngrok.io/webhook/line`
4. 點「Verify」測試（會失敗，沒關係）
5. 開啟「Use webhook」
6. 關閉「Auto-reply messages」

#### 步驟 4: 設定 AutoTo

編輯 `~/.autoto/config.json`，加入：

```json
{
  "channels": {
    "line": {
      "enabled": true,
      "channelAccessToken": "你的 Channel Access Token",
      "channelSecret": "你的 Channel Secret",
      "allowFrom": []
    }
  }
}
```

#### 步驟 5: 啟動

```bash
# 終端機 1: 啟動 ngrok
ngrok http 5678

# 終端機 2: 啟動 AutoTo
autoto
```

#### 步驟 6: 測試

1. 在 LINE Developers 找到你的 Bot 的 QR Code
2. 用手機 LINE 掃描加入好友
3. 傳訊息給 Bot：「你好」
4. Bot 應該會回覆！🎉

### 詳細教學

完整的 LINE 整合教學（包含 Rich Menu、Flex Message 等進階功能）請參考：
[LINE_INTEGRATION.md](LINE_INTEGRATION.md)

## 台灣工具

AutoTo 內建多個台灣特色工具：

### 天氣查詢

```
"台北市今天天氣如何？"
"未來一週天氣預報"
"有颱風嗎？"
"最近有地震嗎？"
"台北空氣品質如何？"
```

### 統一發票

```
"幫我對發票 12345678"
"本期中獎號碼"
```

### 台股資訊

```
"台積電股價多少？"
"2330 今天漲跌"
```

### 設定 API Key

這些工具需要額外的 API Key：

```json
{
  "tools": {
    "taiwan": {
      "weather": {
        "enabled": true,
        "cwa_api_key": "你的中央氣象署KEY"
      }
    }
  }
}
```

**取得 API Key：**
- 中央氣象署：https://opendata.cwa.gov.tw/
- 環保署：https://data.moenv.gov.tw/

詳細說明請參考：[TW_TOOLS.md](TW_TOOLS.md)

## 進階設定

### 定時任務

```bash
# 每天早上 9 點發送天氣預報
curl -X POST http://127.0.0.1:5678/api/schedules \
  -H "Content-Type: application/json" \
  -d '{
    "name": "morning_weather",
    "type": "cron",
    "expression": "0 9 * * *",
    "action": "agent_message",
    "payload": {"message": "台北市今天天氣如何？"}
  }'

# 查看所有任務
curl http://127.0.0.1:5678/api/schedules
```

### 週期性任務（Heartbeat）

目前建議以 AutoTo 內建排程器與設定頁面管理週期任務，而不是依賴舊的 Heartbeat 檔案流程。

建議先確認：
- 已成功啟動 `autoto`
- 可打開 `http://127.0.0.1:5678`
- scheduler 相關 API 正常

### 多模型切換

```json
{
  "agents": {
    "defaults": {
      "model": "anthropic/claude-3.5-sonnet"
    },
    "cheap": {
      "model": "deepseek-chat"
    },
    "smart": {
      "model": "openai/gpt-4"
    }
  }
}
```

### Docker 部署

```bash
# 建立映像
docker build -t autoto .

# 執行
docker run -d \
  -v ~/.autoto:/root/.autoto \
  -p 5678:5678 \
  autoto
```

## 常見問題

### Q: 安裝時出現 "xcode-select: command not found"

```bash
# 安裝 Xcode Command Line Tools
xcode-select --install
```

### Q: `autoto` 指令找不到

```bash
# 重新載入 shell 設定
source ~/.zshrc

# 或直接使用安裝後腳本
~/.autoto/start.sh
```

### Q: LINE Bot 沒有回應

檢查清單：
1. ngrok 是否還在執行？
2. AutoTo 是否在執行？
3. LINE Developers 的 Webhook URL 是否正確？
4. 檢查終端機的錯誤訊息

```bash
# 查看後端狀態
curl http://127.0.0.1:5678/api/status
```

### Q: API 錯誤或超時

```bash
# 檢查 API Key 是否正確
cat ~/.autoto/config.json

# 測試 API 連線
curl http://127.0.0.1:5678/api/test
```

### Q: 如何更新 AutoTo？

```bash
cd autoto
git pull
bash install.sh
```

### Q: 成本會很高嗎？

使用 DeepSeek 或 OpenRouter 的便宜模型：
- 個人使用：每月約 $5-10 USD
- 每 1000 則訊息約 $1-3 USD
- 可以設定預算上限

### Q: 可以商業使用嗎？

可以，但請注意：
1. 確認 AutoTo 的授權條款
2. 考慮資料隱私和安全
3. 建議使用 Docker 部署
4. 設定適當的 rate limiting

## 下一步

### 學習更多

- [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md) - 了解專案架構
- [DEVELOPMENT.md](DEVELOPMENT.md) - 開發自己的功能
- [LINE_INTEGRATION.md](LINE_INTEGRATION.md) - LINE 進階功能
- [TW_TOOLS.md](TW_TOOLS.md) - 台灣工具開發

### 加入社群

- GitHub Issues: 回報問題或建議
- Pull Requests: 貢獻程式碼
- Discussions: 討論和交流

### 商業合作

  如果你想要：
  - 客製化開發
  - 企業部署支援
  - SaaS 平台合作
  
  請透過此 repo 的 GitHub Issues 或 Discussions 聯絡與討論。

## 🎉 開始使用吧！

現在你已經準備好了：

```bash
# 啟動 AutoTo
autoto

# 或直接在 repo 中啟動
./start.sh
```

祝你使用愉快！有任何問題歡迎開 Issue。

---

⭐ 覺得有幫助？給個星星支持一下！
