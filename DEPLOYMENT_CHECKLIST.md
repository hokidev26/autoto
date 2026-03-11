# ✅ AutoTo 部署檢查清單
 
 使用這個清單確保你的 AutoTo 安裝與啟動順利完成。

## 📋 前置準備

### 系統需求
- [ ] macOS 系統
- [ ] 網路連線正常
- [ ] 有管理員權限

### 開發工具
- [ ] 已安裝 Xcode Command Line Tools
  ```bash
  xcode-select --install
  ```
- [ ] 已安裝 Homebrew
  ```bash
  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
  ```
- [ ] 已安裝 Git
  ```bash
  brew install git
  git --version
  ```

## 🚀 基本部署
 
 ### 安裝 AutoTo
 - [ ] 已在完整專案目錄中
 - [ ] 已執行安裝腳本
   ```bash
   bash install.sh
   ```
 - [ ] 已確認安裝目錄存在
   ```bash
   ls ~/.autoto
   ```
 - [ ] 已確認後端與 Web UI 已複製
   ```bash
   ls ~/.autoto/app/backend
   ls ~/.autoto/app/renderer
   ```
 
 ### API Key 設定
 - [ ] 已註冊 Groq / OpenRouter / DeepSeek
 - [ ] 已取得 API Key
 - [ ] 已編輯 `~/.autoto/config.json` 或在設定頁面儲存
 - [ ] 已測試後端狀態
   ```bash
   curl http://127.0.0.1:5678/api/status
   ```
 
 ### 基本測試
 - [ ] 已成功啟動 AutoTo
   ```bash
   autoto
   ```
 - [ ] 已可在瀏覽器開啟
   ```
   http://127.0.0.1:5678
   ```
 - [ ] 已能正常儲存設定並發送訊息

## 📱 LINE 整合（可選）

### LINE 官方帳號申請
- [ ] 已註冊 LINE Developers 帳號
- [ ] 已建立 Provider
- [ ] 已建立 Messaging API Channel
- [ ] 已取得 Channel Secret
- [ ] 已發行 Channel Access Token
- [ ] 已關閉 Auto-reply messages
- [ ] 已開啟 Use webhook

### Webhook 設定（本地測試）
- [ ] 已安裝 ngrok
  ```bash
  brew install ngrok
  ```
- [ ] 已啟動 ngrok
  ```bash
  ngrok http 8000
  ```
- [ ] 已複製 ngrok URL
- [ ] 已在 LINE Developers 設定 Webhook URL
  ```
  https://你的ngrok網址.ngrok.io/webhook/line
  ```

 ### AutoTo 設定
 - [ ] 已在 config.json 加入 LINE 設定
  ```json
  {
    "channels": {
      "line": {
        "enabled": true,
        "channelAccessToken": "YOUR_TOKEN",
        "channelSecret": "YOUR_SECRET"
      }
    }
  }
  ```
 - [ ] 已啟動 AutoTo 後端
  ```bash
  autoto
  ```

### LINE 測試
- [ ] 已用手機掃描 Bot QR Code
- [ ] 已加入 Bot 為好友
- [ ] 發送訊息「你好」
- [ ] Bot 有正常回覆
- [ ] 可以正常對話

## 🛠️ 台灣工具（可選）

### API Key 申請
- [ ] 已註冊中央氣象署開放資料平台
  - 網址：https://opendata.cwa.gov.tw/
- [ ] 已取得中央氣象署 API Key
- [ ] 已註冊環保署開放資料平台（空氣品質）
  - 網址：https://data.moenv.gov.tw/
- [ ] 已取得環保署 API Key

### 工具設定
- [ ] 已在 config.json 加入工具設定
  ```json
  {
    "tools": {
      "taiwan": {
        "weather": {
          "enabled": true,
          "cwa_api_key": "YOUR_CWA_KEY",
          "moenv_api_key": "YOUR_MOENV_KEY"
        },
        "invoice": {
          "enabled": true
        },
        "stock": {
          "enabled": true
        }
      }
    }
  }
  ```

### 工具測試
- [ ] 天氣查詢正常
  ```
  "台北市今天天氣如何？"
  ```
- [ ] 發票對獎正常
  ```
  "幫我對發票 12345678"
  ```
- [ ] 股票查詢正常
  ```
  "台積電股價多少？"
  ```

## 🔧 進階功能（可選）

### 定時任務
- [ ] 已設定 cron 任務
  ```bash
  curl http://127.0.0.1:5678/api/scheduler/jobs
  ```
- [ ] 已測試任務列表
  ```bash
  curl http://127.0.0.1:5678/api/scheduler/jobs
  ```
 
### Heartbeat
- [ ] 已確認使用 AutoTo 內建排程機制
- [ ] 已測試週期性任務

### Docker 部署
- [ ] 已安裝 Docker
- [ ] 已建立 Dockerfile
- [ ] 已建立 Docker image
- [ ] 已測試 Docker 容器運行

## 🚀 生產環境（可選）

### 伺服器準備
- [ ] 已準備伺服器（VPS、雲端等）
- [ ] 已設定網域名稱
- [ ] 已設定 SSL 憑證（Let's Encrypt）
- [ ] 已設定防火牆規則

### 部署方式選擇
- [ ] Docker 部署
- [ ] systemd 服務
- [ ] PM2 管理
- [ ] Kubernetes（大規模）

### 監控和日誌
- [ ] 已設定日誌記錄
- [ ] 已設定錯誤通知
- [ ] 已設定效能監控
- [ ] 已設定備份機制

### 安全性
- [ ] 已設定 allowFrom 白名單
- [ ] 已設定 rate limiting
- [ ] 已設定環境變數（不要把 API Key 寫在程式碼裡）
- [ ] 已設定 HTTPS
- [ ] 已定期更新依賴套件

## 📊 測試清單

### 功能測試
- [ ] 後端啟動正常
- [ ] 瀏覽器模式正常
- [ ] 聊天平台整合正常
- [ ] LINE 訊息收發正常
- [ ] 工具呼叫正常
- [ ] 錯誤處理正常

### 效能測試
- [ ] 回應時間 < 5 秒
- [ ] 記憶體使用正常
- [ ] CPU 使用正常
- [ ] 可以處理並發請求

### 壓力測試
- [ ] 可以處理大量訊息
- [ ] 長時間運行穩定
- [ ] 錯誤恢復正常

## 🐛 問題排查

### 常見問題檢查
- [ ] Python 版本正確（3.11+）
- [ ] 虛擬環境已啟動
- [ ] API Key 正確無誤
- [ ] 網路連線正常
- [ ] 防火牆沒有阻擋
- [ ] 日誌沒有錯誤訊息

### 除錯工具
- [ ] 知道如何查看日誌
  ```bash
  curl http://127.0.0.1:5678/api/status
  ```
- [ ] 知道如何查看後端日誌
- [ ] 知道如何使用 Python debugger
- [ ] 知道如何查看 API 請求

## 📝 文檔檢查

### 已閱讀文檔
- [ ] GET_STARTED.md
- [ ] SETUP_GUIDE_TW.md
- [ ] LINE_INTEGRATION.md（如果使用 LINE）
- [ ] TW_TOOLS.md（如果使用台灣工具）
- [ ] DEVELOPMENT.md（如果要開發）

### 已準備文檔
- [ ] 已記錄 API Keys
- [ ] 已記錄設定檔位置
- [ ] 已記錄部署步驟
- [ ] 已記錄常見問題解決方法

## ✅ 完成確認

### 基本部署
- [ ] ✅ AutoTo 安裝成功
- [ ] ✅ API Key 設定完成
- [ ] ✅ 基本功能測試通過

### LINE 整合（如果需要）
- [ ] ✅ LINE 官方帳號申請完成
- [ ] ✅ Webhook 設定完成
- [ ] ✅ LINE 訊息收發正常

### 台灣工具（如果需要）
- [ ] ✅ API Keys 申請完成
- [ ] ✅ 工具設定完成
- [ ] ✅ 工具測試通過

### 生產環境（如果需要）
- [ ] ✅ 伺服器部署完成
- [ ] ✅ 監控設定完成
- [ ] ✅ 安全性設定完成

## 🎉 恭喜！

如果所有相關項目都打勾了，你的 AutoTo 已經成功部署！

## 📞 需要幫助？

如果遇到問題：
1. 查看 [GET_STARTED.md](GET_STARTED.md) 的「常見問題」
2. 查看 [SETUP_GUIDE_TW.md](SETUP_GUIDE_TW.md) 的詳細說明
3. 在 GitHub 開 Issue
4. 加入社群討論

---

祝你使用愉快！🚀
