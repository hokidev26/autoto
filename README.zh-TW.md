# Autoto

[English](README.md) | 繁體中文 | [簡體中文](README.zh-CN.md)

Autoto 是一個跑在你自己機器上的 coding agent。你給它一個任務，它在背景做，遇到你會想被問一聲的事情，它會先問。

**任務 → 背景 run → 核准 → run 摘要 → diff → 指定路徑 commit**

![Autoto local agent workflow demo](docs/demo.svg)

## 你可以用它做什麼

**丟給它然後去做別的事。** 任務在背景跑，可以同時排幾個。每個 run 結束會給你摘要、可檢視的 diff，以及只暫存你勾選路徑的 commit。它不會替你 push、amend、reset，也不會用 `git add -A`。

**用手機看它做到哪。** 介面是為小螢幕設計的，不是把桌面版縮小塞進去：下拉刷新、通知左滑關閉、輸入框不會被擠掉。可以安裝到手機主畫面獨立開啟，沒有瀏覽器外框，用起來像一般 App。

**從外面連回你的機器。** 在設定頁開一條臨時 Cloudflare 通道，會給你網址和 QR code 直接掃。遠端連線要密碼，分兩種模式：受限模式只能跟進度與核准，完整控制要你明確開啟。連線會過期，而且把政策改嚴會連已經開著的連線一起撤銷。

**在手機上核准，或用 Telegram。** run 需要危險操作的權限時，你可以遠端放行或拒絕。Telegram 配對只限私聊，範圍刻意做窄：查狀態、一次性核准、拒絕。它不是聊天助理，也沒有 `/task`。

**把你的模型當 API 分享出去。** Autoto 可以把你設定好的 provider 以 OpenAI 相容的 `/v1` 端點提供給你其他工具和裝置用。每支金鑰有自己的模型白名單和用量歸戶，所以你可以只分享一部分，而不是把全部交出去。

**在隔離的副本裡動工。** 把 workline fork 成獨立的 Git worktree，讓 agent 在那裡做，做完先檢查合併是否乾淨再併回來。

## 它不會做的事

Autoto 是實驗性質的本機開發 MVP，不是經過加固的多人或生產環境服務。

有些操作是直接拒絕，不只是需要核准。遞迴刪除、對裸裝置寫入、放寬權限、把下載內容直接餵給 shell 執行，這些被歸類為不可逆，任何權限模式、允許規則或人工核准都不能執行它們。這個閘門刻意做成不能為了方便而關掉。

通常存放機密的檔案對檔案工具是硬阻擋：`.env*`、憑證與密鑰材料、`.git` 內容。`Read`、`Write`、`Edit` 直接拒絕，`Glob` 與 `Grep` 則完全不把它們列進結果。

## 快速開始

需要 Go 1.26 或更新版本：

```bash
go run ./cmd/autoto
```

然後開啟：

```text
http://localhost:16888
```

預設的本機狀態位置：

```text
設定檔：  ~/.autoto/config.json
資料庫：  ~/.autoto/autoto.db
專案目錄：~/projects
```

## 下載安裝

每個 tag 的 release 會發布兩種執行檔。它們是同一個產品，差別只在你怎麼開啟介面。

**CLI** — `autoto_<版本>_<系統>_<架構>`，支援 macOS、Linux、Windows，amd64 與 arm64 都有。跑起來是一個本機伺服器，你用瀏覽器開介面。這個是交叉編譯的，所有平台由同一個 job 產出。

**桌面版** — `autoto-desktop_<版本>_<系統>_<架構>.tar.gz`，支援 macOS（arm64 與 amd64）和 Linux amd64。同一個伺服器，但用原生視窗取代瀏覽器分頁。每個平台都在自己的 runner 上編譯，因為桌面外殼要連結該系統的原生 WebView，那個沒辦法交叉編譯。

從 GitHub Releases 下載對應的檔案，解壓縮後執行即可。校驗碼一起發布：CLI 壓縮檔看 `checksums.txt`，桌面版每個壓縮檔旁邊有各自的 `.sha256`。

下載桌面版之前有兩件事要知道。它沒有做程式碼簽章與公證，所以 macOS 的 Gatekeeper 第一次開啟會拒絕，你要到「系統設定 → 隱私權與安全性」明確允許；Windows 也可能出現類似警告。另外 Linux 桌面版只有 amd64，arm64 的 Linux 請用 CLI 版。

從原始碼執行：

```bash
go run ./cmd/autoto
```

也可以指定自訂設定檔路徑：

```bash
go run ./cmd/autoto --config /path/to/config.json
```

### 建置執行檔

CLI 版（可以交叉編譯到任何支援的平台）：

```bash
go build -o autoto ./cmd/autoto
```

桌面版必須加 `desktop` 建置標籤——不加會直接編譯失敗（"build constraints exclude all Go files"）——而且需要該平台的原生 WebView 工具鏈，所以沒辦法交叉編譯。Release 沒有發布 Windows 桌面版，但從原始碼建置沒問題：

```bash
go build -tags desktop -o autoto-desktop ./cmd/autoto-desktop
```

Windows 上再加 `-ldflags "-H windowsgui"`，桌面外殼啟動時才不會多開一個主控台視窗。

想要接近 release 的瘦身版，可以去掉除錯資訊與本機路徑（大約小 25%；panic 堆疊仍保留函式名稱但沒有檔案路徑，`pprof` 之類的工具會少掉符號細節）：

```bash
go build -trimpath -ldflags "-s -w" -o autoto ./cmd/autoto
go build -tags "desktop,production" -trimpath -ldflags "-s -w -H windowsgui" -o autoto-desktop ./cmd/autoto-desktop
```

`production` 標籤會另外關掉 Wails 的 devtools。完整建置參考見 `docs/BUILD.md` 和 `Makefile`。

## 系統需求

- Go 1.26 或更新版本，以 `go.mod` 的宣告為準
- SQLite 透過純 Go 的 `modernc.org/sqlite` 驅動提供，不需要另外裝
- Node.js 是選用的，只在驗證階段用來對內嵌前端腳本跑 `node --check` 與 `node --test`

## 主要功能

英文版 README 有逐項的完整清單。這裡按類別整理，方便先掌握輪廓。

**Agent 與模型**

- 本機 HTTP 伺服器，內嵌 HTML/CSS/JS 介面，前端用免建置的 ES module 架構
- SQLite 持久化：專案、workline、agent、訊息、工具呼叫、backend 註冊、stdio MCP 註冊
- Provider 抽象層，以最小的 `Tools` / `Streaming` / `ImageInput` 能力契約接上 OpenAI Responses API、Anthropic Messages API、OpenAI 相容 Chat Completions、Gemini Interactions API、Kiro（Amazon Q）原生訂閱，以及本機 CLIProxyAPI 預設組
- 核心工具：Read、Write、Edit、Bash、Glob、Grep、WebFetch、WebSearch、MCPListTools、MCPCallTool
- 續跑預算設定：可依工作區限制續跑次數、總回合、總 token 與實際執行時間，預設無上限

**安全邊界**

- 敏感路徑硬阻擋：`Read`、`Write`、`Edit` 直接拒絕受保護檔案，`Glob` 與 `Grep` 則略過不列出。範圍包含 `.env*`、憑證與密鑰檔、常見私鑰材料，以及 `.git` 內容
- Bash 危險反思：可設定強度（關閉／寬鬆／中等／嚴格）的 LLM 安全閘，在執行前用當前對話模型審查高風險指令，依結構化判定放行或阻擋
- 遞迴刪除、磁碟寫入、權限放寬等災難性且不可逆的操作屬於硬阻擋層級，任何權限模式都不能執行，也不能用核准繞過
- Git 操作限定在 status、diff、log 與指定路徑 commit，不會自動 push、amend、reset、clean、force，也不會用 `git add -A`

**工作流程與介面**

- Workline 與容器設定，支援建立 Git worktree 的 workline fork、合併前檢查，以及乾淨 worktree 的合併 API
- 互動式 PTY 終端機 WebSocket，含終端機管理與瀏覽器端的保留／聚焦偏好
- 排程 worker，支援 cron 與 `@every` 表示式和 IANA 時區。排程權限上限為 `readOnly` 或 `acceptEdits`，不會中斷或取代正在跑的手動 run，且無人值守的 run 不會沿用互動時給過的 session 核准
- 具持久性的 Webhook／Telegram 通知投遞紀錄，含去重、租約、指數退避、次數上限與明確重試
- 伺服器端 Skills 與生命週期 hooks，含版本歷史、還原、快照穩定的分派，以及沿用既有核準與稽核閘道的 Shell／HTTP 動作
- 設定頁涵蓋 Providers、自動化、通知、外觀、儲存、用量、使用者與授權資訊

## 設定

首次啟動時，Autoto 會在設定檔不存在時建立一份。執行期的機密可以用環境變數提供。`config.json` 有 schema `version` 欄位；沒有這個欄位的舊設定會被當成版本 `1` 載入並在記憶體中正規化。

Agent 模型相關環境變數：

```text
AUTOTO_DEFAULT_MODEL
AUTOTO_SUMMARY_MODEL
AUTOTO_CONTEXT_TOKEN_LIMIT
```

Provider 環境變數的完整清單請看 [英文版 README](README.md#configuration)，包含 OpenAI、Anthropic、Gemini、OpenAI 相容端點與 CLIProxyAPI 各自的變數名稱。

### 自動化整合與機密參照

Telegram 與 Home Assistant 的連線設定分成兩部分儲存：非機密的中介資料，加上機密的邏輯參照。目前只接受 `env:變數名稱` 這種參照格式；連線 API 與介面會直接拒絕明文 token，而公開的回應只會顯示每個必要機密「是否已設定」，不會回傳值。

先在 Autoto 的行程環境設定實際值：

```sh
export AUTOTO_TELEGRAM_BOT_TOKEN='replace-with-current-bot-token'
export AUTOTO_HOME_ASSISTANT_TOKEN='replace-with-current-ha-token'
```

然後在連線設定裡用參照指向它：

```json
{
  "kind": "telegram",
  "name": "Personal Telegram",
  "secretRefs": { "botToken": "env:AUTOTO_TELEGRAM_BOT_TOKEN" }
}
```

Telegram 固定連官方 API 端點，只透過 long polling 收更新。在本機介面產生短效配對碼，再從私聊送 `/pair <碼>`。可用指令只有 `/status`、`/approve <toolCallId>`（一律是一次性核准）與 `/deny <toolCallId> [原因]`。沒有 `/task`，也沒有自由對話。token 若可能外洩就輪替它，token 版號會改變、舊配對會被撤銷，之後需要重新配對。

Home Assistant 的端點必須是 loopback、`.local`、link-local 或私有網段。門鎖解鎖、攝影機截圖這類未知或關鍵動作是硬阻擋，IM 也不能控制裝置。

## 授權與安全性

- 安全性問題回報方式見 [SECURITY.md](SECURITY.md)
- 開發規範見 [CONTRIBUTING.md](CONTRIBUTING.md)
- 架構說明見 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- 第三方授權見 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)
