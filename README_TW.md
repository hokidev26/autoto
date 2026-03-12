[English](README.md) | [繁體中文](README_TW.md)

# AutoTo

AutoTo 是一個本地優先的 AI 助理，內建 79 個工具、瀏覽器介面、一鍵安裝，支援多聊天平台整合。支援 macOS 和 Windows。

## 能做什麼？

- 💬 透過瀏覽器或聊天平台（LINE、Telegram、Discord、Slack 等）與 AI 對話
- 🖥️ 電腦操控 — 點擊、打字、截圖、開啟應用、執行指令
- 🌐 瀏覽器自動化 — 開網頁、點按鈕、填表單、抓資料（Playwright）
- 📧 Email — 收信、搜尋、讀信、發信
- 📱 社群媒體 — 發文到 IG/FB/X/Threads、讀留言、自動私訊留言者
- 📊 社群分析 — 跨平台互動數據一次看
- 📅 排程發文 — 排好時間自動發社群貼文
- 💰 記帳 — 記帳、查帳、月報、匯出 CSV
- 🎬 影音 — YouTube 播放、影片剪輯、音訊擷取、語音轉文字
- 📷 攝影機監控 — RTSP 串流、AI 智慧監控
- 🏠 智慧家電 — 透過 Home Assistant 控制燈光、開關、空調
- 🌤️ 每日簡報 — 天氣 + 待辦 + 信件 + 社群數據一次看
- 🔧 79 個內建工具，支援自訂技能與 AI 技能生成器

## 快速開始

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

安裝完成後：

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

### 卸載

```bash
bash uninstall.sh
```

## 功能一覽（79 個工具）

| 分類 | 工具 |
|------|------|
| 系統與檔案 | 執行指令、讀寫編輯刪除檔案、瀏覽資料夾、系統資訊、程序管理 |
| 桌面操控 | 點擊、打字、快捷鍵、滑鼠、捲動、截圖、開啟/切換應用 |
| 瀏覽器自動化 | 開啟網頁、點擊、輸入、截圖、取得文字、執行 JS、關閉 |
| Email | 收信、搜尋、讀信、發信 |
| 網路 | 搜尋、擷取網頁、結構化爬蟲、下載檔案 |
| 社群媒體 | IG 貼文/留言/私訊/自動私訊/發文、FB 發文、X 發文、Threads 發文 |
| 社群分析 | 跨平台互動數據摘要 |
| 排程發文 | 新增/列表/取消排程貼文 |
| 記帳 | 新增、查詢、匯出 CSV |
| 影音 | 掃描資料夾、探測、剪輯、合併、擷取音訊、語音轉文字、YouTube 播放 |
| 攝影機 | 列表、快照、串流、AI 分析、持續監控 |
| 智慧家電 | 列表裝置、控制、查詢狀態 |
| 排程 | 排程列表/新增/移除 |
| 工具 | 天氣、摘要、通知、剪貼簿、記憶搜尋、每日簡報 |
| 自訂技能 | 自建工具 + AI 技能生成器 |

## 設定

在瀏覽器設定頁面直接設定，或編輯 `~/.autoto/config.json`：

```json
{
  "provider": "groq",
  "apiKey": "你的KEY",
  "model": "llama-3.3-70b-versatile"
}
```

取得免費 API Key：[Groq](https://console.groq.com/keys)

## 多語系介面

AutoTo 自動偵測瀏覽器語言。支援：English、繁體中文、简体中文、日本語、한국어。

## 文檔

- [GET_STARTED.md](GET_STARTED.md) — 入門指南
- [SETUP_GUIDE_TW.md](SETUP_GUIDE_TW.md) — 部署指南
- [LINE_INTEGRATION.md](LINE_INTEGRATION.md) — LINE 整合教學
- [TW_TOOLS.md](TW_TOOLS.md) — 台灣特色工具
- [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md) — 專案結構

## 授權

MIT License。見 [LICENSE](LICENSE)。

第三方致謝見 [NOTICE](NOTICE)。
