[English](README.md) | [繁體中文](README_TW.md)

# AutoTo
 
 AutoTo 是一個本地優先的 AI 助理專案，提供統一的 Python 後端、瀏覽器介面、一鍵安裝流程，以及多聊天平台整合能力。
 
 你可以用它來：
 
 - 啟動本機 AI 助理與 Web UI
 - 管理 API Key、模型與平台設定
 - 整合 LINE、Telegram、Slack 等聊天平台
 - 執行排程任務與工具自動化
 - 擴充台灣在地化工具，例如天氣、發票與股市資訊
 
 ## ✨ 特色
 
 ### 統一架構
 - **單一後端入口** - `backend/server.py` 統一提供 API 與 Web UI
 - **統一設定檔** - 預設使用 `~/.autoto/config.json`
 - **本機優先** - 預設在 `http://127.0.0.1:5678` 啟動
 
 ### 在地化能力
 - **繁體中文優化** - 台灣用語習慣與預設 system prompt
 - **台灣特色工具** - 天氣、發票、股市、空氣品質等
 - **LINE 整合** - 適合台灣常見聊天平台場景
 
 ### 可擴充性
 - **多模型支援** - Groq、OpenRouter、DeepSeek、OpenAI 等
 - **工具擴充** - 可自行新增自訂工具與平台整合
 - **排程能力** - 支援定時任務與自動化工作流
 
 ## 🚀 快速開始
 
 ### 一鍵安裝（推薦）
 
 macOS：
 
 ```bash
 curl -fsSL https://raw.githubusercontent.com/hokidev26/autoto/main/install_mac.sh | bash
 ```
 
 Windows（PowerShell）：
 
 ```powershell
 irm https://raw.githubusercontent.com/hokidev26/autoto/main/install_win.ps1 | iex
 ```
 
 Windows（CMD）：
 
 ```cmd
 powershell -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/hokidev26/autoto/main/install_win.ps1 | iex"
 ```
 
 安裝完成後啟動：
 
 ```bash
 autoto
 ```
 
 然後打開：`http://127.0.0.1:5678`
 
 ### 直接在 repo 內啟動
 
 ```bash
 git clone https://github.com/hokidev26/autoto.git
 cd autoto
 python3 -m pip install -r backend/requirements.txt
 ./start.sh
 ```
 
 ### 設定 API Key
 
 你可以在瀏覽器設定頁面直接儲存，或編輯 `~/.autoto/config.json`：
 
 ```json
 {
   "provider": "groq",
   "apiKey": "你的KEY",
   "model": "llama-3.3-70b-versatile",
   "customUrl": ""
 }
 ```
 
 **取得 API Key：**
 - [Groq](https://console.groq.com/keys) - 預設 Provider
 - [OpenRouter](https://openrouter.ai/keys) - 多模型選擇
 - [DeepSeek](https://platform.deepseek.com/) - 中文表現佳
 
 ### 開始使用

```bash
# 安裝後啟動
autoto

# 或在 repo 內直接啟動
./start.sh

# 開啟狀態 API
curl http://127.0.0.1:5678/api/status
```

## 📱 LINE 整合

### 1. 申請 LINE 官方帳號
1. 前往 [LINE Developers](https://developers.line.biz/)
2. 建立 Messaging API Channel
3. 取得 Channel Access Token 和 Channel Secret

### 2. 設定 Webhook

```bash
# 本地測試用 ngrok
brew install ngrok
ngrok http 8000

# 在 LINE Developers 設定 Webhook URL
# https://你的ngrok網址.ngrok.io/webhook/line
```

### 3. 設定檔

```json
{
  "channels": {
    "line": {
      "enabled": true,
      "channelAccessToken": "YOUR_TOKEN",
      "channelSecret": "YOUR_SECRET",
      "allowFrom": []
    }
  }
}
```

詳細教學請參考 [LINE_INTEGRATION.md](LINE_INTEGRATION.md)

## 🛠️ 台灣特色工具

### 天氣查詢

```bash
# 在 chat 中使用
"台北市今天天氣如何？"
"未來一週天氣預報"
"有颱風嗎？"
"最近有地震嗎？"
"台北空氣品質如何？"
```

### 統一發票

```bash
"幫我對發票 12345678"
"本期中獎號碼"
```

### 台股資訊

```bash
"台積電股價多少？"
"2330 今天漲跌"
```

詳細說明請參考 [TW_TOOLS.md](TW_TOOLS.md)

## 📚 文檔

- [SETUP_GUIDE_TW.md](SETUP_GUIDE_TW.md) - 完整部署指南
- [LINE_INTEGRATION.md](LINE_INTEGRATION.md) - LINE 整合教學
- [TW_TOOLS.md](TW_TOOLS.md) - 台灣特色工具開發
- [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md) - 專案結構說明

## 🏗️ 專案結構

```
autoto/
├── backend/              # 後端 API、agent、排程與聊天平台
│   ├── channels/
│   ├── core/
│   ├── requirements.txt
│   └── server.py
├── electron-app/         # Electron 殼層與 renderer
├── installer/            # macOS / Windows 安裝器
├── LICENSE
├── NOTICE
├── README_TW.md
├── GET_STARTED.md
└── start.sh
```

## 🔧 開發

### 維護與版本更新

```bash
git pull
bash install.sh
```

### 加入新工具

```python
@agent.tool(
    name="my_tool",
    description="我的工具說明"
)
def my_tool(param: str) -> str:
    """工具實作"""
    return f"處理結果: {param}"
```

### 執行測試

```bash
pytest tests/
```

## 💰 成本估算

使用 OpenRouter + DeepSeek：
- 每 1000 則訊息約 $1-3 USD
- 每月個人使用約 $5-10 USD
- 商業使用視流量而定

## 🤝 貢獻

歡迎提交 PR！

1. Fork 專案
2. 建立功能分支 (`git checkout -b feature/amazing-feature`)
3. 提交變更 (`git commit -m 'Add amazing feature'`)
4. 推送到分支 (`git push origin feature/amazing-feature`)
5. 開啟 Pull Request

## 📄 授權

本專案採用 [MIT License](LICENSE)。
詳細的第三方來源與致謝說明請參考 [NOTICE](NOTICE)。

若你在此專案中保留或改寫了來自其他開源專案的程式碼，公開時請一併保留相容的授權聲明與來源致謝。

## 🙏 致謝

- [nanobot](https://github.com/HKUDS/nanobot) - 架構與 agent loop 設計參考（MIT）
- [nanoBot-ui](https://github.com/qq695500710-ui/nanoBot-ui) - 可視化介面與視窗設計參考
- [OpenClaw](https://github.com/openclaw/openclaw) - 安裝流程與工作流體驗靈感
- 中央氣象署、財政部 - 開放資料 API

## 📞 聯絡

- Issues: 請使用本 repo 的 GitHub Issues

## 🗺️ Roadmap

- [x] LINE 整合
- [x] 台灣天氣工具
- [x] 統一發票查詢
- [ ] Facebook Messenger 整合
- [ ] Instagram DM 整合
- [ ] 台鐵/高鐵時刻表
- [ ] Ubike 即時資訊
- [ ] 電商 API 整合（蝦皮、momo）
- [ ] LINE Pay 整合
- [ ] 商業版 SaaS 平台

---

⭐ 如果這個專案對你有幫助，請給個星星！
