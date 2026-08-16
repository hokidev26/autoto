# Autoto

[English](README.md) | 繁體中文 | [簡體中文](README.zh-CN.md)

[![Release](https://img.shields.io/github/v/release/hokidev26/autoto)](https://github.com/hokidev26/autoto/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](go.mod)
[![Platforms](https://img.shields.io/badge/Platforms-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)](#安裝)

> **Autoto** 是一個跑在你自己機器上的 coding agent。
> 你給它一個任務，它在背景做，遇到你會想被問一聲的事情，它會先問。
> **任務 → 背景 run → 核准 → run 摘要 → diff → 指定路徑 commit**

---

## 為什麼用 Autoto

| 痛點 | Autoto 的做法 |
|---|---|
| AI 跑太久，坐電腦前等 | 任務在背景跑，送出後可以去做別的事 |
| AI 亂改一堆檔案 | 結束後只給你看 diff，commit 只能送你點名的路徑 |
| 出門後看不到進度 | 手機 UI 為小螢幕設計，可裝到主畫面當 PWA |
| 出門後不能核准危險操作 | 手機 UI 或 Telegram 私聊遠端核准（一次性） |
| 想從外面連回自家電腦 | 從設定頁開臨時 Cloudflare tunnel，密碼保護、會過期 |
| 一次想跑幾個任務 | 任務可以排隊，背景 agent 自動接著做 |
| 想試新功能又怕髒掉主分支 | Fork 到獨立的 Git worktree，預檢通過再合併 |
| AI 一直重複同一個指令 | 連續重複會插進漸進式提醒，但不會否決呼叫 |
| 工具輸出太大塞爆上下文 | 超過門檻的輸出自動落盤，模型用 Read/Grep 分頁 |
| 想把模型分享給其他工具 | 把 provider 包成 OpenAI 兼容的 `/v1` 端點，每個 API key 獨立配額 |

**Autoto 不會**做的事：自動 push、amend、reset、force、clean、`git add -A`。任何模式、allow rule、核准都不能執行「遞迴刪除」「直接寫入 `/dev/sda`」「把 curl 灌進 shell」這類不可逆操作。`.env*`、credential、私鑰、`.git` 內部：Read/Write/Edit 直接拒絕，Glob/Grep 跳過。

---

## 快速開始（命令列版）

從 [GitHub Releases](https://github.com/hokidev26/autoto/releases) 下載 `autoto_<版本>_<OS>_<arch>`：

| OS | 檔案 | 架構 |
|---|---|---|
| **Windows** | `autoto_<版本>_windows_amd64.zip` | x64 |
| **Windows** | `autoto_<版本>_windows_arm64.zip` | ARM |
| **macOS** | `autoto_<版本>_darwin_arm64.tar.gz` | Apple Silicon |
| **macOS** | `autoto_<版本>_darwin_amd64.tar.gz` | Intel |
| **Linux** | `autoto_<版本>_linux_amd64.tar.gz` | x64 |
| **Linux** | `autoto_<版本>_linux_arm64.tar.gz` | ARM |

解壓縮後直接執行：

```bash
# macOS / Linux
./autoto

# Windows
autoto.exe
```

打開 http://localhost:16888

> **預設狀態路徑**
> ```
> Config:   ~/.autoto/config.json
> Database: ~/.autoto/autoto.db
> Projects: ~/projects
> ```

> **第一次跑缺 Provider？** 進到 `Settings → Providers` 設定 OpenAI / Anthropic / Gemini / 兼容中轉站 之一的 API key，或直接用內建的 `cliproxyapi` 預設組。

### 原生桌面版（選用）

想要原生視窗、不用開瀏覽器，下載 `autoto-desktop_<版本>_<OS>_<arch>.tar.gz`：

> 桌面版需要該平台原生 WebView 工具鏈，**無法交叉編譯**。GitHub Release 只發布 **macOS（arm64 / amd64）** 與 **Linux amd64**。
> Windows 桌面版要從原始碼自編，參考下面「從原始碼構建」。

---

## 從原始碼構建

需求：**Go 1.26+**（`go.mod` 宣告）。

### CLI 版本（跨平台）

```bash
git clone https://github.com/hokidev26/autoto
cd autoto
go run ./cmd/autoto
```

### 桌面版（需原生 WebView）

```bash
# macOS / Linux
go build -tags desktop -o autoto-desktop ./cmd/autoto-desktop

# Windows（額外避免 console 視窗）
go build -tags desktop -ldflags "-H windowsgui" -o autoto-desktop.exe ./cmd/autoto-desktop
```

### 接近 release 的精簡版

加上 `-trimpath -ldflags "-s -w"`（小約 25%；panic 堆疊仍保留函式名但沒有檔案路徑）：

```bash
go build -trimpath -ldflags "-s -w" -o autoto ./cmd/autoto
go build -tags "desktop,production" -trimpath -ldflags "-s -w -H windowsgui" -o autoto-desktop ./cmd/autoto-desktop
```

`production` 標籤會關掉 Wails devtools。完整構建說明見 `docs/BUILD.md` 與 `Makefile`。

---

## 設定

首次啟動會自動建立 `~/.autoto/config.json`（schema 內含 `version` 欄位；沒有這欄的舊設定會被當成版本 `1` 載入並在記憶體中正規化）。

### Agent 模型

```text
AUTOTO_DEFAULT_MODEL        # 預設 agent 模型
AUTOTO_SUMMARY_MODEL        # 摘要用的較小模型
AUTOTO_CONTEXT_TOKEN_LIMIT  # 上下文 token 上限
```

### Provider（環境變數）

```text
OPENAI_API_KEY
OPENAI_MODEL
ANTHROPIC_API_KEY
ANTHROPIC_MODEL
GEMINI_API_KEY
GEMINI_MODEL
GEMINI_BASE_URL
OPENAI_BASE_URL
OPENAI_COMPATIBLE_BASE_URL
OPENAI_COMPATIBLE_API_KEY
OPENAI_COMPATIBLE_MODEL
CLIPROXYAPI_BASE_URL
CLIPROXYAPI_API_KEY
CLIPROXYAPI_MODEL
CLIPROXYAPI_MANAGEMENT_KEY
CLIPROXYAPI_BIN
CLIPROXYAPI_CONFIG
```

### 自動化整合（環境變數優於硬編碼金鑰）

Telegram 與 Home Assistant 的連線分兩部分儲存：非機密的中繼資料 + 邏輯上的金鑰引用。目前只接受 `env:VAR_NAME` 格式；UI 與 API 會直接拒絕明文 token，公開回應只顯示「金鑰是否已配置」。

```sh
export AUTOTO_TELEGRAM_BOT_TOKEN='replace-with-current-bot-token'
export AUTOTO_HOME_ASSISTANT_TOKEN='replace-with-current-ha-token'
```

然後在設定中引用：

```json
{
  "kind": "telegram",
  "name": "Personal Telegram",
  "secretRefs": { "botToken": "env:AUTOTO_TELEGRAM_BOT_TOKEN" }
}
```

```json
{
  "kind": "home-assistant",
  "name": "Home Assistant",
  "endpoint": "http://homeassistant.local:8123",
  "secretRefs": { "accessToken": "env:AUTOTO_HOME_ASSISTANT_TOKEN" }
}
```

Telegram 命令集（限定私聊）：`/pair`, `/status`, `/approve <toolCallId>`（一次性）, `/deny <toolCallId> [reason]`。**沒有** `/task` 或自由對話。token 換新會自動撤銷所有已配對的會話。

Home Assistant 端點必須是 loopback、`.local`、link-local 或私網 IP。

### CLIProxyAPI 預設組

內建 `cliproxyapi` provider profile 對接本機 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)：

```text
Provider: cliproxyapi
Type:     openai-compatible
Base URL: http://127.0.0.1:8317/v1
Model:    gpt-5.5
```

啟動 CLIProxyAPI 之後，進到 `Settings → Providers → Codex 凭證 + 中轉站` 設定。Codex 採「匯入凭證」：貼 Codex auth JSON 或 refresh token / token / account 列表後直接匯入到 CLIProxyAPI，Autoto 會在之後刷新 CLIProxyAPI 的 auth 檔與 `/v1/models`。

讓新專案預設就用這個 profile：

```sh
AUTOTO_DEFAULT_MODEL=cliproxyapi:gpt-5.5 ./autoto
```

### Agent Server 後端

```text
AUTOTO_AGENT_BACKEND_URL
AUTOTO_AGENT_BACKEND_NAME
AUTOTO_AGENT_BACKEND_KIND
AUTOTO_AGENT_BACKEND_API_KEY
OPENHANDS_AGENT_SERVER_URL
OPENHANDS_SESSION_API_KEY
AGENT_SERVER_URL
AGENT_SERVER_API_KEY
```

本地後端用 `X-Session-API-Key` 標頭；雲端後端用 `Authorization: Bearer ...`。

---

## 功能

底下逐項功能清單，會比上面幾段更具體。

### Agent 與模型

- 本機 HTTP 伺服器，內嵌 HTML/CSS/JS 介面，前端用免建置的 ES module 架構，並抽出 Settings 本機偏好面板
- SQLite 持久化：專案、workline、agent、訊息、工具呼叫、backend registry、stdio MCP registry
- Provider 抽象層，以最小 `Tools` / `Streaming` / `ImageInput` 能力契約串接：
  - OpenAI 官方 Responses API（SDK 串流 text delta + usage 收集）
  - Anthropic 官方 Messages API（串流 text delta、tool-use delta、usage 收集、大請求自動 5m prompt-cache breakpoint）
  - OpenAI 兼容 Chat Completions
  - Gemini Interactions API（SSE 串流、圖片、原生 function call、reasoning effort、內部 thought-signature replay）
  - Kiro（Amazon Q）原生訂閱 provider（Event Stream、OAuth token refresh、`ksk_*` API key 認證）
  - CLIProxyAPI 內建本機 OpenAI 兼容預設組
- 核心工具：Read、Write、Edit、Bash、Glob、Grep、WebFetch、WebSearch、MCPListTools、MCPCallTool、AgentSnapshot、AgentSendMessage
- 跨對話協作：`AgentSnapshot` 列出同實例上其他主對話並讀近期內容；`AgentSendMessage`（exec 風險、走審批）把訊息送進另一個對話，讓它以自己的權限跑一輪後，把回報自動送回發問的對話（與子代理相同的喚醒機制）。直接的 A↔B 循環會被拒絕，子代理不能發送也不能被指定為目標。從別的對話讀到的內容——包括經 `PeerSnapshot` 從配對實例讀到的——會被標記為不可信的唯讀背景資料，以免別人轉錄裡的偽造指令靠「從工具結果送來」就取得權限
- 工具輸出落盤：結果超過 `agent.toolOutputSpillBytes`（預設 50000 bytes）時，完整內容寫到 Autoto 主目錄，回給模型的換成首尾預覽加上檔案路徑，由模型用 `Read` 分頁或 `Grep` 搜尋。`Read` 與 `Grep` 本身豁免，避免用「再去讀一次」回答一次讀取；寫檔失敗一律保留原本的內聯結果，絕不把成功的工具呼叫變成錯誤；落盤檔案 7 天後清理
- 重複工具呼叫偵測：同一工具帶同樣參數的連續呼叫按 run 計數，達到 `agent.repeatToolCallThresholds`（預設 `3, 5, 8`）之一時，下次請求中插入漸進式提醒。參數先正規化再比較，被拒絕的呼叫同樣計入；該偵測只觀察、永不否决呼叫
- 續跑預算：可按工作區限制續跑次數、總回合、總 token、實際執行時間，預設無上限（負值明確選擇不設限）
- Project sidebar 拖曳排序，伺服器持久化

### 安全邊界

- 敏感路徑硬阻擋：`Read`、`Write`、`Edit` 直接拒絕受保護檔案，`Glob` 與 `Grep` 跳過不列出。範圍含 `.env*`、憑證／密鑰檔、通用私鑰材料、`.git` 內部
- Bash 危險反思：可設強度（off / loose / medium / strict）的 LLM 安全閘，執行前用當前對話模型審查高風險命令，按結構化判定放行或阻擋
- 遞迴刪除、磁碟寫入、權限放寬、下載直接灌進 shell 等不可逆操作屬硬阻擋層級，任何權限模式、allow rule、核准都不能執行
- Git 工作區限定在 status、diff、log、指定路徑 commit，不會自動 push、amend、reset、clean、force、`git add -A`

### 排程與自動化

- 排程 worker：cron / `@every` 表達式 + IANA 時區。排程權限上限 `readOnly` 或 `acceptEdits`，不會中斷或取代正在跑的手動 run；無人值守的 run 不沿用互動時的 session 核准；含停止／重啟 Autoto 本身的命令會在建立與更新時被拒絕
- 持久化 Webhook / Telegram 通知投遞紀錄：去重、租約、指數退避、嘗試次數上限、delivered / dead 狀態、彙總指標、顯式重試
- Telegram Bot API long polling：私聊 `/pair`、`/status`、`/approve <toolCallId>`（一次性 `allow_once`）、`/deny <toolCallId> [reason]`；未授權命令與失敗配對靜默；已處理 update 用持久化 event ID 與 cursor 保護
- Home Assistant 整合：限本地／私網端點、唯讀狀態、固定的 action allowlist、短時效 action request、雙重 UI 確認、loopback 核准。Door unlock / camera snapshot 等未知／高風險動作硬阻擋；IM 無法控制裝置
- 持久化 SQLite migrations V19–V22 與 API：排程、通知投遞、整合連線、channel 配對／事件／cursor、device-action request
- 本地監控彙總：活躍 run、待核准、排程、投遞狀態、channel、device action、自動化 worker 健康

### 後端與工作區

- Agent Server 後端 registry：sidebar 與 Agent Admin 管理 UI，支援 OpenHands Agent Server 兼容端點
- Workline 與容器設定：建立 Git worktree 的 workline fork、合併前預檢、乾淨 worktree 合併 API
- 互動式 PTY 終端 WebSocket（`/ws/terminal`），含終端管理與瀏覽器端保留／聚焦偏好
- 檔案系統瀏覽／預覽／mkdir API
- 持久化 Server 端 Skills：global / project / workspace CRUD、有效 skill 解析、修訂歷史／還原、快照穩定的 cursor 分頁；MCP registry 操作仍需顯式 exec-risk 核准
- Server 端 lifecycle hooks：global / project / agent 三層 run / tool 邊界，快照穩定分派、CAS 更新、執行歷史、隔離測試執行（不建立普通 Agent run）
- 本機 plugin registry：從本地目錄安裝 stdio MCP 插件，工具以 `plugin__<slug>__<tool>` 動態發現供 agent 呼叫。安裝後一律停用，啟用需明確確認執行本機程式碼；插件進程以乾淨環境與 `env:` 金鑰引用運行，manifest 支援每插件逾時，並有更新與健康檢查端點（見 [docs/PLUGINS.md](docs/PLUGINS.md)）

### 介面與體驗

- 為小螢幕設計的 UI（不是縮放桌面版）：下拉刷新、通知左滑關閉、輸入框不被擠掉；可裝到主畫面當 PWA，沒有瀏覽器外框
- 設定 modal 即時搜尋／過濾 + 鍵盤焦點快捷鍵
- 聊天訊息複製動作：匯出單則訊息與整個對話為 Markdown
- 按登入使用者與 Agent 區分的版本化私訊草稿；瀏覽器本機草稿僅作為未登入相容後備
- Unicode／大小寫不敏感的本地帳號 handle、`@handle` 建議、不可變的使用者訊息更正（保留新舊附件）
- 剪貼簿圖片／檔案附件、Unicode 安全的多語系草稿上限、瀏覽器原生 text undo／redo
- 瀏覽器本機 prompt history：空輸入時 ↑/↓ 召回；舊設定可由 preference 備份遷移
- 聊天輸入框 slash command palette：來自已啟用的本地 Skills command template
- 瀏覽器本機 Settings → Profile：顯示名稱、頭像字首、工作區標籤、Git identity 助手
- 瀏覽器本機 Settings → Network Search：provider 預設、結果數上限、是否確認、網域規則；`WebSearch` 與 `WebFetch` 工具提供公開網頁／文件查詢
- 瀏覽器本機 Settings → Notifications：toast 類別、工作事件提示音（完成／等待核准／失敗，含內建音效或本機自訂音檔、音量與同時播放上限）、系統通知與明確的權限請求、顯示時長、UI 終端提示；伺服器端持久化 Webhook / Telegram 投遞歷史與重試
- 瀏覽器本機 Settings → Appearance：主題、密度、預設終端可見性、Agent event 顯示
- 設定 → Servers/System + Runtime 面板：runtime 摘要、Go runtime、路徑、Agent 限制
- 設定 → Users：管理員可建立只能觀看對話的訪客、核發存取金鑰，並授權專案。訪客限制由伺服器執行，只能看已授權對話並編輯個人資料
- 設定 → Storage 面板：config、database、home、projects 體積
- 設定 → Usage 面板：projects、messages、tool calls、model requests、估計 token 成本、backends
- 設定 → About 依賴授權面板（開發期的 `/api/licenses` 端點）
- 設定 → About 瀏覽器本機偏好備份／匯入：profile、skills、chat drafts、prompt history、search、IM、notification、appearance、terminal、recent directory、model、relay-protocol
- 設定 → IM 閘道 自動化控制：排程、通知歷史／重試、Telegram 與 Home Assistant、pairing／revocation、監控、device state、本地 device-action 確認、稽核事件

### Agent WebSocket 協定

- `ws/agent` 上跑協定 v2，每處理程序單調遞增序號、有限記憶體 replay、權威 live-snapshot 重同步；**不是** 持久化或跨處理程序事件 log

---

## 平台支援

| 元件 | Windows | macOS | Linux |
|---|---|---|---|
| CLI | ✅ amd64 / arm64 | ✅ arm64 / amd64 | ✅ amd64 / arm64 |
| 桌面原生視窗 | ⚠️ 從源碼自編（Release 不發） | ✅ arm64 / amd64 | ✅ amd64 |

桌面版需該平台原生 WebView 工具鏈，無法交叉編譯。

---

## 疑難排解

### Windows：「Windows 已保護你的電腦」

因為 Autoto 沒有 Authenticode 簽章。執行步驟：
1. 對 `autoto.exe` 按右鍵 → 內容
2. 勾「解除封鎖」→ 套用
3. 或在 SmartScreen 視窗點「更多資訊」→「仍要執行」

未來可加程式碼簽章，但需要購買與維護憑證。

### macOS：「無法開啟，因為它來自未識別的開發者」

對 `autoto` 在 Finder 第一次執行會被 Gatekeeper 拒絕：
1. 打開 **系統設定 → 隱私與安全性**
2. 捲到最下面，會看到「已阻擋 `autoto` 的使用」
3. 點「仍要打開」

未來可加公證（notarization），但需要 Apple Developer 帳號（年費 USD 99）。

### 連接埠被佔用

預設 16888 被佔用的話，編輯 `~/.autoto/config.json`：

```json
{
  "server": { "host": "localhost", "port": 17888 }
}
```

或透過 Web UI `Settings → Servers/System` 改。

### SQLite 鎖住

如果程序被強制 kill，database 可能留下 stale lock。刪掉 `~/.autoto/autoto.db-shm` 與 `~/.autoto/autoto.db-wal` 後重啟。

### 出門在外連不到

從外面連回自家網路，**不要**直接把 Autoto 對外公開。打開 `Settings → Remote Access` 開一條臨時 Cloudflare tunnel，會拿到網址 + QR code，密碼保護、過期自動失效。

---

## 費用估算

Autoto 把 provider 用量記在 `api_requests` 表，並在 `Settings → Usage` 顯示彙總成本。估算來自 `internal/pricing/pricing.go` 中的 USD／百萬 token 表，最近一次對齊公開定價頁面是 2026-07-07（OpenAI API pricing、GPT-4.1 pricing announcement、Anthropic Claude pricing）。未知名稱刻意估算為 `0`；OpenAI 兼容中轉／本地模型的費率可能與公開名稱對應的官方費率不同。

---

## 系統需求

- Go 1.26+（`go.mod` 宣告）
- SQLite 走純 Go `modernc.org/sqlite` driver，**不需要**本機 sqlite3
- Node.js **非必要**，只在驗證階段對內嵌前端腳本跑 `node --check` 與 `node --test`

---

## 命名相容性

設定路徑與 route 別名保留向後相容閘。Canonical 名稱總是優先。相容性生命週期與移除閘內部定義：任何相容表面都要在 v1.0.0 以後、且至少經過兩個 tagged release 的遷移窗口才能移除。

---

## 相關文件

- `docs/BUILD.md` — 從源碼構建
- `docs/WINDOWS_RUN.md` — 在 Windows 上跑
- `docs/ARCHITECTURE.md` — 架構總覽
- `docs/PLUGINS.md` — 本地 MCP 插件
- `docs/DESKTOP_PACKAGING.md` — 桌面打包邊界
- `CHANGELOG.md` — 變更紀錄
- `SECURITY.md` — 漏洞回報
- `CONTRIBUTING.md` — 貢獻指南
- `AGENTS.md` — Agent 行為準則
- `THIRD_PARTY_NOTICES.md` — 第三方依賴授權

---

## 授權

[MIT](LICENSE) — Copyright (c) 2026 Autoto contributors
