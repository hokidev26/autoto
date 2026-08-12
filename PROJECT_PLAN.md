# Autoto Go MVP 專案規劃

## 1. 專案目標

本專案目標是用 Go 實現 Autoto：一個本地 AI 程式設計 Agent 後端。Go module 為 `autoto`，`cmd/autoto` 與 `autoto` 二進位制是當前規範入口；`cmd/autoto` 僅保留為 legacy 相容 shim。

核心目標不是一次性堆滿所有功能，而是先做出一個可執行、可擴充、可逐步替換/增強的 MVP：

- 本地 HTTP 服務
- SQLite 持久化
- Project / Workline / Agent 資料模型
- Agent 會話與訊息記錄
- Provider 抽象
- Tool 抽象與基礎工具執行
- WebSocket 事件推送
- 基礎檔案系統 API
- 基礎開源協議依賴清單
- 簡單內嵌 Web UI
- 公開倉庫入口文件、MIT License、CI 與安全說明

### 1.1 Legacy compatibility lifecycle（唯一事實源）

本節是 legacy 相容面的**唯一生命週期事實源**；README、CHANGELOG、SECURITY 與歷史計劃只能引用，不得另設不同移除日期或視窗。

**規範名稱（canonical names）**：

- 產品、CLI、module、release asset：Autoto / `autoto`
- 本地狀態與配置：`~/.autoto`、`AUTOTO_*`
- HTTP/WebSocket header 與瀏覽器偏好：`X-Autoto-*`、`autoto.*`
- 領域與路由：Agent / Workline、`/api/agents`、`/api/worklines`、`/ws/agent`

**當前兼容面**：

- 舊 CLI shim（保留為兼容入口點）；
- 舊配置目錄的一次性配置遷移讀取；
- 舊環境變量 fallback；
- 舊 HTTP header、cookie 與 localStorage key；
- 舊 JS global（服務端仍注入同值作為 fallback）；
- 舊 API 路由別名；
- migration、測試夾具與 CHANGELOG 歷史中的舊名。
### 3.1 Go 專案骨架

目錄：

```txt
autoto/
  go.mod                         # module autoto
  go.sum
  .gitignore
  cmd/autoto/main.go              # canonical application entrypoint
  cmd/autoto/main.go          # legacy compatibility shim
  internal/config
  internal/db
  internal/server
  internal/agent
  internal/providers
  internal/tools
```

啟動方式：

```bash
go run ./cmd/autoto
```

構建後的規範 CLI 名稱為 `autoto`，例如 `go build -o autoto ./cmd/autoto && ./autoto`。

預設監聽：

```txt
http://localhost:16888
```

預設配置路徑：

```txt
~/.autoto/config.json
```

預設資料庫路徑：

```txt
~/.autoto/autoto.db
```

當規範配置檔案不存在而舊 `~/.autoto/config.json` 存在時，啟動會自動將該 legacy 配置複製到 `~/.autoto/config.json` 後繼續載入；舊目錄僅用於遷移相容。

預設專案目錄：

```txt
~/projects
```

---

### 3.2 配置模組

文件：

```txt
internal/config/defaults.go
```

當前預設配置包含：

- config schema version（當前 `version = 1`，老配置缺欄位時載入回填）
- server host / port
- home dir
- database path
- default project dir
- agent 預設模型
- agent 預設權限模式
- 多 provider 例項配置（OpenAI 官方 / Anthropic 官方 / OpenAI-compatible / CLIProxyAPI 本地預置）

當前預設：

```txt
server.host = localhost
server.port = 16888
agent.defaultPermissionMode = acceptEdits
agent.defaultModel = openai:gpt-4.1-mini
```

Agent 與核心執行時支援的規範環境變數：

```txt
AUTOTO_DEFAULT_MODEL
AUTOTO_SUMMARY_MODEL
AUTOTO_CONTEXT_TOKEN_LIMIT
AUTOTO_EXPOSED
AUTOTO_ACCESS_PASSWORD
AUTOTO_REMOTE_TERMINAL
```

同名 legacy `AUTOTO_*` 環境變數仍作為回退相容；當兩者同時存在時，`AUTOTO_*` 優先。

Provider 支援環境變數：

```txt
OPENAI_API_KEY
OPENAI_MODEL
ANTHROPIC_API_KEY
ANTHROPIC_MODEL
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

首次生成預設 `config.json` 時，執行時仍會讀取環境變數中的 API key，但寫入磁碟的預設配置會清空 provider/backend API key，避免把 shell 環境裡的 secret 持久化。

P2–P3 integration connection 的 bot/access token 不接受明文，只保存 `env:VARIABLE_NAME` 引用，例如：

```txt
Telegram botToken     -> env:AUTOTO_TELEGRAM_BOT_TOKEN
Home Assistant token -> env:AUTOTO_HOME_ASSISTANT_TOKEN
```

公開 API 只返回對應 logical secret 是否已配置，不返回引用目標或解析後的值。Telegram bot token 輪換會改變 credential revision 並撤銷舊配對；懷疑洩漏時還應從本地 UI/API 顯式撤銷配對並重新配對。Home Assistant 不使用 channel pairing，token 輪換後應重啟/重測或停用連線。

---

### 3.3 SQLite 資料庫

文件：

```txt
internal/db/schema.go
internal/db/db.go
```

當前核心表（節選）：

```txt
users
projects
worklines
agents
runs
agent_messages
agent_message_attachments
agent_tool_calls
api_requests
agent_backends
schedules
notification_deliveries
integration_connections
channel_pairings
channel_events
channel_cursors
device_action_requests
```

這些表的命名與欄位風格儘量貼近 AI 程式設計工作臺資料模型，方便後續遷移或擴充。

核心關係：

```txt
projects
  -> worklines
      -> agents
          -> agent_messages
          -> agent_tool_calls
```

---

### 3.4 HTTP API

當前已實現：

```txt
GET  /api/health
GET  /api/auth/status
GET  /api/settings
GET  /api/models
GET  /api/licenses
GET  /api/runtime/summary
GET  /api/storage/summary
GET  /api/usage/summary
GET  /api/monitoring/snapshot

GET  /api/notifications/settings
PUT  /api/notifications/settings
POST /api/notifications/test
GET  /api/notifications/deliveries
POST /api/notifications/deliveries/{id}/retry

GET    /api/schedules
POST   /api/schedules
PATCH  /api/schedules/{id}
DELETE /api/schedules/{id}
POST   /api/schedules/{id}/run

GET    /api/integrations/connections
POST   /api/integrations/connections
PATCH  /api/integrations/connections/{id}
DELETE /api/integrations/connections/{id}
POST   /api/integrations/connections/{id}/test

POST /api/channels/pairing-codes
GET  /api/channels/pairings
POST /api/channels/pairings/{id}/revoke
GET  /api/audit/events

GET  /api/devices?connectionId=...
POST /api/device-actions
POST /api/device-actions/{id}/approve
POST /api/device-actions/{id}/deny

PUT  /api/providers/{name}/config

GET  /api/providers/cliproxyapi/auth-files
POST /api/providers/cliproxyapi/auth-files/import

GET    /api/backends
POST   /api/backends
GET    /api/backends/{id}
PATCH  /api/backends/{id}
DELETE /api/backends/{id}
POST   /api/backends/{id}/activate
GET    /api/backends/{id}/health

GET  /api/projects
POST /api/projects
GET  /api/projects/{id}
GET  /api/projects/{id}/worklines

GET  /api/worklines/{id}
POST /api/worklines/{id}/fork
GET  /api/worklines/{id}/merge-check?targetWorklineId=...
POST /api/worklines/{id}/merge
GET  /api/worklines/{id}/agents

GET   /api/agents/{id}
PATCH /api/agents/{id}/cwd
PATCH /api/agents/{id}/model
PATCH /api/agents/{id}/permission-mode
POST  /api/agents/{id}/interrupt
GET   /api/agents/{id}/messages
POST  /api/agents/{id}/messages
GET   /api/agents/{id}/tools
POST  /api/agents/{id}/tool-calls
GET   /api/agents/{id}/tool-calls/{toolUseId}
GET   /api/agents/{id}/git/status
GET   /api/agents/{id}/git/diff
GET   /api/agents/{id}/git/log
POST  /api/agents/{id}/git/commit

GET  /api/fs/browse?path=...
GET  /api/fs/directories?path=...
GET  /api/fs/preview?path=...
POST /api/fs/mkdir

GET  /ws/agent?id={agentId}
GET  /ws/terminal?agentId={agentId}
```

規範領域實體與路由為 Agent / Workline、`/api/agents`、`/api/worklines` 和 `/ws/agent`。Legacy 客戶端仍可使用 `/api/projects/{id}/worklines`、`/api/worklines/...`、`/api/agents/...` 與 `/ws/agent`；這些相容別名複用同一組 Agent/Workline handler。

---

### 3.5 Project 建立行為

`POST /api/projects` 請求示例：

```json
{
  "name": "Demo Project",
  "description": "optional",
  "gitPath": "optional",
  "model": "optional provider:model override"
}
```

如果未傳 `gitPath`，系統會自動建立：

```txt
~/projects/<project-name-slug>
```

例如：

```txt
~/projects/demo-project
```

並自動建立：

- project
- root workline
- primary agent

---

### 3.6 Agent loop

文件：

```txt
internal/agent/loop.go
internal/agent/hub.go
```

當前能力：

- 接收使用者訊息
- 寫入 `agent_messages`
- 啟動 goroutine 執行 agent loop
- 呼叫預設 provider
- 寫入 assistant message
- 更新 agent status
- 經 WebSocket 推送事件

當前 WebSocket 事件包括：

```txt
connected
agent.started
agent.text
agent.done
agent.error
message.created
tool.started
tool.finished
```

Agent stream 已使用 protocol 2 envelope（`protocol`、`streamSession`、`sequence`）：同一程序內由有界 ring buffer 提供有限 replay，並在 cursor 過期、replay 超限、訂閱者溢位、stream 淘汰、session 不匹配或前端檢測到序列缺口時要求讀取 authoritative live snapshot 後 resync。該機制不持久化事件，服務重啟或跨程序後不能 replay，不能稱為 durable event log。

---

### 3.7 Provider 抽象

文件：

```txt
internal/providers/provider.go
internal/providers/openai_compatible.go
internal/providers/openai_official.go
internal/providers/anthropic_provider.go
```

當前實現：

```txt
openai              -> OpenAI 官方 Go SDK，Responses API
anthropic           -> Anthropic 官方 Go SDK，Messages API
openai-compatible   -> 手寫 OpenAI-compatible Chat Completions 相容層
cliproxyapi         -> 基於 OpenAI-compatible 的本地 CLIProxyAPI 預置
```

模型字串使用 `provider:model` 字首路由，例如：

```txt
openai:gpt-4.1-mini
anthropic:claude-sonnet-4-5
openai-compatible:gpt-4.1-mini
cliproxyapi:gpt-5.5
```

如果沒有設定對應 API key，provider 會返回配置提示，不會真正請求外部模型；CLIProxyAPI 本地預置例外，它預設允許無客戶端 API key 連線 `http://127.0.0.1:8317/v1`，如 CLIProxyAPI 啟用了 `api-keys` 再通過 `CLIPROXYAPI_API_KEY` 注入。內建 OpenAI official、Anthropic official 與 OpenAI-compatible Provider 均已接入流式輸出、tool calling 與 tool result 回灌，並通過統一最小能力契約宣告 `Tools`、`Streaming`、`ImageInput`。未知或未實現 capability 介面的 Provider 按不支援可選能力處理，Agent loop 按能力降級，不在業務層按 Provider 名稱特判。

環境變數：

```txt
OPENAI_API_KEY
OPENAI_MODEL
ANTHROPIC_API_KEY
ANTHROPIC_MODEL
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

後續計劃支援：

- Codex 憑證匯入體驗繼續完善（賬號狀態、額度、錯誤恢復）
- Kiro-like provider
- 本地模型 provider
- 多 provider fallback / load balancing

---

### 3.8 Tool 抽象與核心工具

文件：

```txt
internal/tools
```

當前工具：

```txt
Read
Write
Edit
Bash
Glob
Grep
WebFetch
WebSearch
MCPListTools
MCPCallTool
```

工具接口：

```go
type Tool interface {
    Name() string
    Description() string
    Schema() any
    Risk(input json.RawMessage) Risk
    Execute(ctx context.Context, call Call, env Env) (Result, error)
}
```

當前風險類型：

```txt
read
write
exec
danger
```

當前權限模式：

```txt
readOnly
acceptEdits
default
dontAsk
bypassPermissions
```

初版策略：

- `readOnly`：只允許 read 風險工具
- `acceptEdits/default/dontAsk`：允許 read/write，預設不允許 Bash exec
- `bypassPermissions`：允許大多數工具，但仍阻止 danger
- danger：當前總是拒絕

危險命令初步識別：

```txt
rm
rmdir
shred
```

P2–P3 進一步把敏感路徑阻斷下沉到檔案路徑工具：`Read`、`Write`、`Edit` 直接拒絕，`Glob`、`Grep` 遍歷時過濾 `.env*`、credentials/secret、常見私鑰檔案及 `.git`；同時繼續拒絕 symlink 逃逸。此邊界不覆蓋 Bash/stdio MCP，二者仍是強本地執行能力，不能把敏感路徑過濾描述成完整 sandbox。

---

### 3.9 檔案系統 API

文件：

```txt
internal/server/fs.go
```

當前 API：

```txt
GET  /api/fs/browse?path=...
GET  /api/fs/directories?path=...
GET  /api/fs/preview?path=...
POST /api/fs/mkdir
```

安全邊界：

- 預設限制在 `paths.defaultProjectDir`
- 相對路徑基於 default project dir
- 阻止 `..` 逃逸

後續計劃：

- 支援 agent cwd 邊界
- 支援專案維度 path scope
- 支援二進位制檔案識別
- 支援圖片/Notebook/PDF 預覽

---

### 3.10 Agent Server 後端登錄檔

文件：

```txt
internal/server/backends.go
internal/db/db.go
internal/db/schema.go
```

當前能力：

- 持久化多個相容 OpenHands Agent Server 的後端
- 保證同一時間只有一個 active 後端
- 支援本地後端 `X-Session-API-Key` 與雲端後端 `Authorization: Bearer ...`
- 健康檢查 `/alive`、`/health`、`/ready`、`/server_info`
- UI 中可以新增、檢測、切換、刪除後端
- 可通過環境變數 seed 初始後端：
  - `AUTOTO_AGENT_BACKEND_URL`
  - `AUTOTO_AGENT_BACKEND_NAME`
  - `AUTOTO_AGENT_BACKEND_KIND`
  - `AUTOTO_AGENT_BACKEND_API_KEY`
  - `OPENHANDS_AGENT_SERVER_URL`
  - `AGENT_SERVER_URL`
  - `OPENHANDS_SESSION_API_KEY`
  - `AGENT_SERVER_API_KEY`
- `AUTOTO_AGENT_BACKEND_*` 優先於同名 legacy `AUTOTO_AGENT_BACKEND_*`；後者僅保留為回退相容。

注意：API 返回時只暴露 `apiKeyConfigured`，不會回顯後端 API key。

---

### 3.11 內嵌 Web UI

文件：

```txt
internal/server/ui.go
internal/server/static/index.html
internal/server/static/styles.css
internal/server/static/app.js                  # 輕量 bootstrap
internal/server/static/modules/app-main.mjs    # 當前主 UI 模組
internal/server/static/modules/backend-registry.mjs # Agent Server backend registry/modal/Admin controller
internal/server/static/modules/chat-composer.mjs # chat send/draft/history/attachments/slash command controller
internal/server/static/modules/chat-rendering.mjs # chat message rendering/approval/markdown controller
internal/server/static/modules/directory-browser.mjs # directory chooser/browser/recent paths controller
internal/server/static/modules/formatters.mjs  # shared number/size/money/time formatters
internal/server/static/modules/git-workflow.mjs # Git status/diff/log/commit modal controller
internal/server/static/modules/terminal.mjs    # terminal preferences/settings/WebSocket controller
internal/server/static/modules/runtime.mjs     # API/token/WebSocket helper
internal/server/static/modules/mcp-registry.mjs # MCP registry form parsing helpers
internal/server/static/modules/mcp-registry-ui.mjs # backend MCP registry UI/actions controller
internal/server/static/modules/model-provider-settings.mjs # Settings Models/Providers UI and model helpers
internal/server/static/modules/local-preferences-settings.mjs # Settings local preference panels UI/actions controller
internal/server/static/modules/system-settings.mjs # Settings system/storage/usage/users/about panels controller
internal/server/static/modules/skills-workbench.mjs # Settings Skills workbench UI/actions controller
internal/server/static/modules/ui-shell.mjs     # global shortcuts/sidebar/mobile shell/project search
internal/server/static/modules/settings-preferences.mjs # browser-local settings preferences/backup/import
internal/server/static/modules/dom.mjs          # DOM/query/escape/button helpers
internal/server/static/modules/settings-data.mjs # settings/skills static navigation data
internal/server/static/modules/preferences-data.mjs # localStorage keys/default preference data
```

當前 UI 是 **shadcn-inspired**，參考 shadcn/ui 的簡潔 card、button、input、badge、border、radius 風格，但沒有直接引入 React、Tailwind、Radix 或 shadcn 元件原始碼。前端已開始無構建 ES module 拆分：`app.js` 只負責 bootstrap，業務主模組在 `modules/app-main.mjs`，Agent Server backend registry/彈窗/Agent Admin controller 在 `modules/backend-registry.mjs`，Chat 傳送/草稿/歷史/附件/slash command controller 在 `modules/chat-composer.mjs`，Chat 訊息渲染/審批/Markdown controller 在 `modules/chat-rendering.mjs`，目錄選擇/瀏覽/最近目錄/路徑格式化 controller 在 `modules/directory-browser.mjs`，通用格式化函式在 `modules/formatters.mjs`，Git status/diff/log/commit modal controller 在 `modules/git-workflow.mjs`，終端偏好/設定頁/WebSocket controller 在 `modules/terminal.mjs`，API/token/WebSocket helper 在 `modules/runtime.mjs`，後端 MCP registry UI/action controller 在 `modules/mcp-registry-ui.mjs`，Settings Models/Providers UI 與模型選擇 helper 在 `modules/model-provider-settings.mjs`，Settings 本地偏好面板（Profile/Network Search/IM Gateway/Notifications/Appearance）UI/action controller 在 `modules/local-preferences-settings.mjs`，Settings 系統/儲存/使用/使用者/About 面板 controller 在 `modules/system-settings.mjs`，Settings Skills 工作臺 UI/action controller 在 `modules/skills-workbench.mjs`，全域性快捷鍵/側欄/移動端 shell/專案搜尋 controller 在 `modules/ui-shell.mjs`，瀏覽器本地 Settings 偏好/備份/匯入 controller 在 `modules/settings-preferences.mjs`。

當前路由：

```txt
GET /
GET /ui/styles.css
GET /ui/app.js
```

當前頁面能力：

- 檢視健康狀態
- 檢視專案列表
- 建立專案
- 自動選擇 root workline / primary agent
- 查看 agent messages
- 複製任意使用者/助手訊息原文，或一鍵複製當前對話 Markdown，便於整理 issue、PR 描述或外部筆記
- 傳送訊息
- 按當前 agent 瀏覽器本地自動儲存/恢復聊天輸入草稿，切換專案或重新整理頁面不丟失未傳送內容
- 在聊天輸入框中通過瀏覽器本地提示詞歷史儲存最近提示，並在空輸入時用 ↑/↓ 快速召回
- 在聊天輸入框輸入 `/` 調出已啟用的本地技能命令模板，並通過鍵盤或點選插入提示詞
- 連線 `/ws/agent`
- 查看 WebSocket event log
- 連線 `/ws/terminal` 互動式 PTY
- 通過設定 → 終端管理檢視 PTY 狀態、重連/清空/複製/聚焦終端，並管理輸出保留和連線後聚焦偏好
- 更新 agent cwd / model / permission mode
- 瀏覽 `/api/fs/browse`
- 預覽 `/api/fs/preview`
- 在設定彈窗內搜尋/過濾個人設定、例項管理和各產品化設定面板，並支援快捷鍵聚焦搜尋
- 檢視 settings 簡要統計，並在設定 → 關於中通過 `/api/licenses` 檢視第三方依賴許可證清單
- 在設定 → 關於中複製、下載、匯入瀏覽器本地偏好備份，遷移個人資料、技能草案、聊天草稿、提示詞歷史、搜尋/IM/通知/外觀/終端/模型和中轉協議設定
- 檢視 `/api/runtime/summary` 驅動的伺服器與系統、執行資源、Go runtime、記憶體和 Agent 限制概覽
- 檢視 `/api/storage/summary` 驅動的儲存空間、資料庫、配置檔案和預設專案目錄容量統計
- 檢視 `/api/usage/summary` 驅動的使用歷史、訊息/工具/模型請求和成本統計；未實現真實後台任務前不建立/展示 background_tasks 殭屍模型
- 檢視 `/api/auth/status` 驅動的使用者初始化和註冊開放狀態
- 從 `/api/models` 動態重新整理 CLIProxyAPI 憑證賬號可用模型
- 在 Git 變更面板中檢視 status/diff/log，並顯式選擇檔案建立本地 commit（不自動 push）

- 設定 → 個人資料頁內完成瀏覽器本地顯示名、頭像縮寫、身份標籤、工作臺標籤和 Git 身份輔助
- 設定 → 網路搜尋頁內完成瀏覽器本地搜尋提供商、結果數、安全/確認開關、GitHub 優先和域名規則策略；Agent 工具層已提供 `WebSearch` 公網搜尋結果工具和 `WebFetch` 公網 HTTP(S) 文件抓取工具
- 設定 → P2–P3 管理控制台已接入服務端 schedules、durable deliveries、integration connections、Telegram pairing/revoke、Home Assistant 只讀實體/本地動作審批、monitoring snapshot 與 audit events。舊 `localStorage` IM 草稿只作為“已停用”的遷移提示，不會啟動服務或計入執行狀態
- Telegram 當前只通過 long polling 接收私聊 `/pair`、`/status`、`/approve`（固定一次性 `allow_once`）與 `/deny`；無 `/task`、無自由聊天、無 Telegram webhook、無 Slack/Discord。未配對與錯誤配對保持靜默
- Home Assistant 只允許本機/私網 endpoint；狀態列表只讀，動作僅限固定 allowlist，建立和批准均要求本地 UI 雙確認，最終執行批准還要求 direct loopback。critical/未知動作硬阻斷，IM 不得控制裝置
- 設定 → 技能頁已接入服務端 Skills：後端支援 global/project/workspace CRUD、effective Skills、revision 歷史/restore 與 snapshot-stable cursor 分頁；scoped 面板支援按作用域瀏覽、詳情、分頁、修訂歷史與恢復，但建立、SKILL.md 匯入、啟停、編輯、刪除 UI 仍只操作 global scope。MCP registry 仍可建立/啟停/刪除 server、執行 tools/list，並通過 exec-risk 審批呼叫 stdio MCP tools
- 設定 → 生命週期鉤子頁已接入 global/project/agent 配置、CAS 更新、執行歷史、測試/取消/重試與獨立中英繁中目錄。Shell/HTTP 動作複用現有工具審批和審計閘道器；`env:` 引用只在審批通過後的執行階段解析，Shell cwd 保持在工作區內，HTTP 複用防 SSRF 網路策略，LLM gate 保持隔離且不開放工具
- 設定 → 工作線與容器頁內完成當前專案工作線、當前工作線 Agent、worktree/branch/容器隔離邊界概覽和快速切換
- 設定 → AI 代理頁內完成預設 Agent 策略概覽、當前 agent 狀態、模型/權限/workdir 快速調整和 ID 複製
- 設定 → 使用者管理頁內完成本地 auth status 只讀檢視、註冊狀態、安全邊界和後續多使用者路線提示
- 設定 → 通知頁內完成瀏覽器本地 toast 類型、顯示時長和 UI 終端提示偏好；服務端 Webhook/Telegram 通知改為持久 delivery history，具去重、lease、指數退避、最大嘗試次數、delivered/dead 狀態和顯式 retry
- 設定 → 外觀與介面頁內完成瀏覽器本地主題、佈局密度、終端預設展開和 Agent 事件日誌顯示偏好
- 設定 → 關於頁內完成瀏覽器本地偏好備份、下載、複製和匯入恢復，便於跨瀏覽器或跨機器遷移工作臺設定
- 設定 → 模型/提供商頁內完成模型重新整理、Codex Token/JSON 憑證匯入、賬號列表重新整理、中轉站 API Key/Base URL/協議/預設模型儲存、模型選擇和首選模型儲存
- 設定 → 代理管理頁內完成 Agent Server 後端列表、健康檢測、啟用切換、雙擊確認刪除和新增後端

後續如果需要正式使用 shadcn/ui，可升級為：

```txt
web/
  package.json
  vite.config.ts
  src/
  components/ui/*
```

並使用 React + Tailwind + shadcn registry。正式引入前需要重新整理 Node 依賴協議。

---

### 3.12 License API

文件：

```txt
internal/server/licenses.go
```

當前 API：

```txt
GET /api/licenses
```

當前用途：

- 讀取 Go build info 中的依賴
- 對已確認模組標註 license
- 未確認模組標為 `unknown`

當前已確認直接依賴：

```txt
github.com/go-chi/chi/v5               MIT
github.com/google/uuid                 BSD-3-Clause
modernc.org/sqlite                     BSD-3-Clause
github.com/coder/websocket             ISC
github.com/openai/openai-go/v3         Apache-2.0
github.com/anthropics/anthropic-sdk-go MIT
github.com/creack/pty                  MIT
```

注意：

此介面只是開發期合規輔助，不是法律意見。釋出前仍需生成完整 third-party notice。

---

### 3.13 公開倉庫基礎建設

當前已補齊：

```txt
README.md
LICENSE
SECURITY.md
CONTRIBUTING.md
THIRD_PARTY_NOTICES.md
CHANGELOG.md
docs/ARCHITECTURE.md
.github/workflows/ci.yml
.github/workflows/release.yml
.goreleaser.yaml
```

說明：

- 倉庫入口以 `README.md` 為準。
- `PROJECT_PLAN.md` 用於開發規劃和實現狀態跟蹤。
- `CHANGELOG.md` 記錄 tag 級使用者可見變更、安全邊界和已知缺口。
- `docs/ARCHITECTURE.md` 面向貢獻者說明請求如何流過 server、agent、provider、tools、WebSocket 和 SQLite。
- `THIRD_PARTY_NOTICES.md` 是直接依賴初版說明，不是法律意見；正式釋出前仍應生成完整 transitive notice。
- CI 會檢查 Go 格式、測試、vet、構建、內嵌 JavaScript 語法，並通過 `golangci-lint` 增加 static analysis。
- `v*` tag 會觸發 GoReleaser release workflow，構建 macOS/Linux/Windows archives；README 保留輕量 `docs/demo.svg` 工作流預覽，後續如有真實錄屏可再替換。

---

## 4. 當前測試

已有測試：

```txt
internal/agent/loop_test.go
internal/config/defaults_test.go
internal/db/db_test.go
internal/providers/anthropic_provider_test.go
internal/providers/openai_compatible_test.go
internal/providers/openai_official_test.go
internal/server/backends_test.go
internal/server/workline_workflow_test.go
internal/server/e2e_test.go
internal/server/git_test.go
internal/server/interrupt_test.go
internal/server/mcp_servers_test.go
internal/server/security_test.go
internal/tools/tools_test.go
internal/runtime/supervisor_test.go
internal/app/run_test.go
internal/automation/manager_test.go
internal/channels/telegram_test.go
internal/devices/action_test.go
internal/devices/client_test.go
internal/schedules/expression_test.go
internal/db/automation_p2p3_test.go
internal/server/automation_api_test.go
internal/server/static/modules/automation-control.test.mjs
```

覆蓋：

- 預設配置與後端環境變數 seed
- 建立 project/workline/agent
- agent backend registry 單 active 約束
- OpenHands Agent Server 健康檢查
- 工具路徑越界檢查
- Write 後 Read
- WebFetch HTML 簡化與 local/private host 拒絕
- WebSearch query 校驗、DuckDuckGo HTML 結果解析、格式化輸出和 core 註冊
- MCP stdio client 初始化、tools/list、tools/call、文本結果格式化、registered serverId 查詢和 core 註冊
- MCP server registry：SQLite CRUD、HTTP CRUD、Settings UI 建立/啟停/刪除/發現工具、env value 響應脫敏、`GET /api/mcp/servers/{id}/tools` discovery
- 本地 token、Origin、Sec-Fetch-Site 與 WebSocket 握手防護
- 官方 Anthropic/OpenAI SDK provider 流式事件、usage 與 fallback 行為
- usage cost 估算：OpenAI、Anthropic Sonnet/Opus 與未知模型分支
- Git commit API 的顯式 paths 提交、安全路徑拒絕、空倉庫 diff 降級
- 全鏈路 E2E：真實 httptest server、WebSocket agent stream、HTTP message submit、假 provider tool call、審批 route、Bash 工具執行、tool result 回灌模型、訊息/tool_call/api_requests 落庫
- Workline workflow：fork API 建立 Git worktree/child workline/agent，fork agent Git API 邊界可用，merge-check 能報告衝突檔案，merge API 能成功合併 clean 分支並在衝突時 abort
- V19–V22 migration 與 schedules/deliveries/integration/channel/device action 持久狀態、CAS/lease、統計和敏感 payload 拒絕
- Schedule cron/`@every`/timezone、busy skip、不替換人工 run，以及 run permission cap 不放寬 Agent 權限
- Webhook/Telegram delivery retry/backoff、`dead`、歷史與 Agent-scoped Telegram 路由/脫敏
- Telegram 私聊配對、失敗鎖定與靜默、event/cursor 冪等、`/status`、一次性 `/approve`、`/deny`、danger 拒絕、審計 fail-closed 與限流
- Home Assistant 私網 endpoint、只讀屬性過濾、固定動作 catalog、canonical seal、本地 direct-loopback 二次批准，以及 unlock/camera/script 等 critical/未知動作硬阻斷
- monitoring snapshot 聚合、runtime Supervisor 啟停/回滾順序、Settings P2–P3 控制台的有界 DOM 與 secret 不回顯
- 檔案路徑工具對 `.env*`、credentials/secrets、私鑰與 `.git` 的硬阻斷/過濾

當前驗證命令已收斂為統一入口：

```bash
make check
```

如果本地沒有 `make`，可直接執行 `./scripts/check.sh`。該腳本會檢查 Go 格式但不自動改寫，隨後執行 Go tests/vet/build、前端 `node --check` 與前端 `node --test`。如需格式化 Go 程式碼，執行 `make fmt`。

短啟動驗證包括：

- `/api/health`
- `/api/licenses`
- `/api/backends`
- `/api/backends/{id}/health`
- `/api/mcp/servers`
- `/api/mcp/servers/{id}/tools`
- `POST /api/projects`
- `POST /api/agents/{id}/tool-calls`
- `GET /api/agents/{id}/git/status`
- `GET /api/agents/{id}/git/diff`
- `POST /api/agents/{id}/git/commit`

歷史 dogfood 證據（Autoto 更名前，以下服務名稱、補丁文本和提交資訊保留為 legacy 原始記錄）：2026-07-07 UTC / 2026-07-08 +08:00 使用臨時 Autoto 服務與臨時 Git 倉庫，通過 API 建立專案，執行 `Write` / `Read` / `Grep`，讓已跟蹤檔案 `demo/notes.md` 變為 `worktree=M`，通過 Git diff API 看到 `added=2 deleted=0` 和補丁行 `+- Updated through Autoto Write tool for tracked diff review.`，再用顯式 `paths: ["demo/notes.md"]` 呼叫 Git commit API 建立提交 `96cd79e Dogfood tracked diff workflow`，提交後倉庫 `clean=true`。較早的未跟蹤檔案 smoke 也建立並提交了 `2484ab7 Dogfood Autoto API workflow`。

---

## 5. 工程工作流狀態（歷史 Phase 1–6）

本節的 Phase 1–6 是早期**工程工作流編號**，只用於追蹤實現主題；它們不是 `docs/notes/needtodo0712.md` 的產品 Phase A/B/C。產品 **Phase B 專指 IM 控制面**；當前只完成受限 Telegram 配對/狀態/一次性審批/拒絕，不包含 `/task`、自由聊天或其他渠道。不得把本節的 Provider、Tools、Skills 或前端工作稱為產品 Phase B。

### Engineering Phase 1：當前 MVP 完善

目標：讓後端更適合手工/CLI 除錯。

待做：

- [x] `GET /api/projects/{id}/worklines`
- [x] `GET /api/worklines/{id}/agents`
- [x] `PATCH /api/agents/{id}/cwd`
- [x] `PATCH /api/agents/{id}/model`
- [x] `PATCH /api/agents/{id}/permission-mode`
- [x] `POST /api/agents/{id}/interrupt`
- [x] 工具呼叫 WebSocket 事件
- [x] provider request/response 記錄到 `api_requests`
- [x] 最簡 context 管理（粗略 token 估算、舊訊息摘要、舊工具輸出降級）
- [x] agent status 更細化：`idle/running/error/interrupted`

---

### Engineering Phase 2：工具系統增強

目標：讓工具更接近可用編碼 Agent。

待做：

- [x] Edit 工具
- [x] Bash 支援顯式審批狀態
- [x] Bash 輸出流式事件
- [ ] 工具執行超時配置
- [ ] 工具輸出截斷策略配置
- [ ] 工具輸入 JSON schema 輸出
- [ ] 工具權限規則表
- [ ] whitelist/blacklist dirs
- [ ] whitelist/blacklist commands（已內建 exec 白名單 matcher 與 danger 阻斷，規則配置 UI/表待補）

---

### Engineering Phase 3：Provider 增強

目標：支援真實模型流式與 tool calling。

待做：

- [x] OpenAI-compatible streaming
- [x] OpenAI 官方 Responses API streaming
- [x] Anthropic 官方 Messages API streaming
- [x] tool call parsing（Anthropic / OpenAI official / OpenAI-compatible）
- [x] tool result 回灌模型（Anthropic / OpenAI official / OpenAI-compatible）
- [x] Anthropic 官方 SDK provider（非流式 MVP）
- [x] OpenAI 官方 Responses API provider（非流式 MVP）
- [x] provider 字首路由與基礎 model list
- [x] usage/cost 統計（usage 寫入 `api_requests`，cost 使用內建 per-model USD/MTok 價格表估算；價格來源在 `internal/agent/loop.go` 註釋和 README 中記錄，未知模型估算為 0）
- [x] Anthropic prompt caching（足夠大的 system/tool/message 請求自動新增 5m cache_control breakpoint，小請求跳過以避免額外 cache write 成本）
- [x] retry/backoff
- [x] first token timeout

---

### Engineering Phase 4：Git / Workline 工作流

目標：實現多分支、多工作線能力。

待做：

- [x] Git status/diff/log API（只讀）
- [x] UI diff 檢視器（只讀 Git 變更面板）
- [x] Git commit API
- [x] project git path 檢查（repo root 必須位於專案路徑或 default project dir 內）
- [x] workline fork（後端 API 建立 child workline + primary agent）
- [x] git worktree 建立（`POST /api/worklines/{id}/fork` 使用 sibling `.autoto-worktrees`，避免巢狀進主 repo）
- [x] workline merge-check（`GET /api/worklines/{id}/merge-check` 使用臨時 worktree 做非破壞性衝突預檢）
- [x] merge（`POST /api/worklines/{id}/merge` 要求 source/target clean，衝突時 abort 並返回 409，成功後記錄 merge metadata）
- [ ] AI resolve conflict
- [ ] review workline

---

### Engineering Phase 5：MCP / Terminal / Runtime

目標：補齊高階能力。

待做：

- [x] WebFetch 公網 HTTP(S) 文件抓取工具（local/private host 預設拒絕）
- [x] WebSearch 公網搜尋結果工具（預設 DuckDuckGo HTML，query/limit 校驗，local/private search endpoint 防護）
- [x] MCP server registry（後端持久登錄檔/API + Settings UI 建立/啟停/刪除/發現工具：CRUD、env value 脫敏響應、registered server tools/list discovery）
- [x] MCP tool discovery（`MCPListTools` 通過 stdio initialize + tools/list，並支援 `serverId` 引用已註冊 server）
- [x] MCP tool execution（`MCPCallTool` 通過 stdio initialize + tools/call，支援 `serverId`，exec-risk 審批）
- [x] PTY terminal
- [x] `/ws/terminal`
- [x] V19 schedules + run source/permission cap（僅 `readOnly` / `acceptEdits`，busy skip，不取消人工 run）
- [x] V20 durable Webhook/Telegram deliveries（歷史、去重、lease、指數退避、`dead`、retry）
- [x] V21 Telegram pairing/events/cursor（long polling，`/pair` `/status` `/approve`-once `/deny`，未配對靜默）
- [x] V22 Home Assistant device action requests（本機/私網、只讀狀態、固定 allowlist、本地雙確認、critical hard block、IM 禁止）
- [x] V51 profile configuration + lifecycle hooks（global/project/agent 配置、快照繫結、CAS、歷史、測試；Shell/HTTP 複用審批審計，金鑰延遲解析，HTTP 防 SSRF）
- [x] monitoring snapshot 聚合與 runtime Supervisor 管理 channels / automation / HTTP
- [ ] Slack/Discord channel adapter
- [ ] IM `/task` 與自由聊天（當前明確不提供）
- [ ] 通用 IoT、攝像頭動作、門鎖解鎖、雲監控
- [ ] 顯式通用 background task queue（schedule 已實現，但不等於通用任務佇列）
- [ ] process list
- [ ] runtime cleanup

---

### Engineering Phase 6：前端

目標：提供本地 Web UI。

初版 UI 頁面：

- [x] Project list
- [ ] Workline detail
- [x] Agent chat
- [x] Run summary 回顧卡片（接入 `/api/agents/{id}/runs/{runId}`，支援複製摘要與開啟 Git 變更）
- [ ] Tool calls panel
- [x] File browser
- [x] Settings
- [x] License report

可選技術：

- React + Vite
- SvelteKit
- HTMX + Go templates

建議先用簡單 React/Vite，後端靜態託管 `web/dist`。

---

## 6. 開源協議整理計劃

### 當前 Go MVP

可以從：

```txt
go.mod
go.sum
Go module cache LICENSE files
runtime/debug BuildInfo
```

生成依賴協議表。

後續可以增加命令：

```txt
autoto licenses export
```

生成：

```txt
THIRD_PARTY_NOTICES.md
licenses.json
```

### 上游參考二進位制

僅靠二進位制字串不能可靠確定完整依賴協議。

若要整理上游參考實現的協議，需要輸入：

```txt
package.json
bun.lockb / bun.lock
pnpm-lock.yaml / package-lock.json / yarn.lock
LICENSE
NOTICE
THIRD_PARTY_NOTICES
licenses 目錄
其它子專案的 go.mod / Cargo.lock 等
```

拿到這些檔案後，可以整理：

```txt
依賴名
版本
license
是否 copyleft
是否需要 NOTICE
是否需要原始碼公開
是否可商用
風險等級
備註
```

---

## 7. 當前已知限制

當前 MVP 仍有這些限制：

- Telegram 是唯一入站渠道且只使用 long polling；命令僅 `/pair`、`/status`、`/approve <toolCallId>`（一次性）和 `/deny`。沒有 `/task`、自由聊天、Telegram webhook、Slack 或 Discord。
- Telegram durable event/cursor 與 notification delivery history 不等於 Agent durable event log。Agent stream protocol 2 的 replay 仍只位於當前程序的有界記憶體；沒有持久 retention、服務重啟後或跨程序 replay。
- Home Assistant 是唯一裝置介面卡，且只允許本機/私網 endpoint。沒有通用 IoT、攝像頭動作、門鎖解鎖或雲監控；本地 monitoring snapshot 只是聚合狀態。
- Home Assistant 狀態讀取只返回過濾後的實體/屬性；動作僅限固定 allowlist，並要求本地雙確認和 direct-loopback 最終批准。IM 永遠不能控制裝置。
- Schedule 已實現，但不是通用任務佇列：只允許 `readOnly` / `acceptEdits`，Agent busy 時跳過並記錄，不排隊，也不取消人工 run。
- 檔案路徑工具已硬阻斷敏感路徑，但 Bash 與 stdio MCP 仍能執行強本地操作，不能視為 sandbox。
- 前端 UI 已按 ES module 拆分，但仍有較多業務邏輯留在 `app-main.mjs`，不是完整 React/shadcn 實現。
- `/api/fs` 當前以 default project dir 為邊界，尚未按 agent cwd 動態限制。
- Browser-originated API / WebSocket 已有本地 token 與 Origin/Sec-Fetch-Site 防護，但仍應只繫結可信本地地址。
- Git API 與 workline merge API 已限制 repo root 位於專案路徑、default project dir 或 Autoto 建立的 `.autoto-worktrees` workline worktree 內；尚未實現 AI conflict resolve 與完整 review workline。
- license API 只確認了部分依賴協議。
- 已有 stdio MCP discovery/execution 與 registry；尚未實現 MCP 長連線會話池。
- 顯式通用任務佇列、程序列表與 runtime cleanup 尚未實現。

---

## 8. 下一步建議

產品 Phase A 的 Provider capability、Agent stream 與 Skills 基礎已經收口；P2–P3 已把 schedules、durable deliveries、Telegram pairing/status/一次性 approval/deny、Home Assistant 受限適配、監控聚合和 Supervisor 生命週期接通。下一輪應先穩定現有邊界，而不是擴張渠道或裝置矩陣：

1. 為真實 Telegram bot + Home Assistant 環境補一份可重複的本地 dogfood/重啟恢復記錄，尤其驗證 token 輪換撤銷配對、delivery 重試和 busy schedule skip；
2. 保持 Telegram 命令面只含 `/pair`、`/status`、一次性 `/approve` 與 `/deny`，除非完成獨立威脅模型與預設關閉設計，否則不加入 `/task`；
3. 保持 IM 與裝置控制隔離，不允許 Telegram 建立或批准 Home Assistant action；
4. 補齊通知歷史、channel events、device actions 的 retention/清理策略與更細監控，但不要稱為雲監控；
5. Slack/Discord、通用 IoT、攝像頭動作和門鎖解鎖繼續保持未完成，只有真實需求與安全審查後再立項；
6. 繼續推進 review workline / AI conflict resolve、通用佇列、process list 與 runtime cleanup。

所有文件與 UI 必須持續明確：當前有受限 Telegram 入站控制，但沒有 `/task` 或通用 IM 聊天；當前有受限 Home Assistant 動作，但沒有通用 IoT、攝像頭動作、門鎖解鎖或雲監控。
