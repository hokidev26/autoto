# needtodo0709：Autoto 後續改進計劃（審查補強版）

## 0. 報告狀態與審查來源

本報告已根據當前倉庫的只讀審查結果補強，目標是把 Autoto 從“功能型 MVP”推進到更穩定、更可用、更有產品差異化的本地 AI 編碼代理工作臺。

本次補強重點審查了四條主線：

1. Provider / agent loop / tool calling / retry timeout。
2. SQLite schema 初始化與遷移風險。
3. 前端執行閉環、審批、run 回顧、Git 審查與提交入口。
4. 倉庫衛生、統一檢查腳本、專案指令檔案載入。

結論摘要：

- 報告原方向基本正確，但需要把“OpenAI 工具回灌未完成”修正為更嚴重的事實：預設 OpenAI 路徑目前缺 native tool calling，OpenAI-compatible 同時缺 streaming 與 tools。
- DB migration 風險是實打實的 P0：當前只有 `CREATE TABLE IF NOT EXISTS`，沒有 `PRAGMA user_version`。
- Run 通知與回顧很有產品價值，但前置條件是先建立 run 物件、run_id、run 狀態機與可恢復審批。
- 倉庫衛生整體比原報告預期更好：當前 `.gitignore` 已覆蓋主要產物；真正更需要做的是統一檢查腳本、清理/說明本地殘留和空目錄。

### 0.1 2026-07-09 當前執行進度更新

大部分 P0/P1 基礎項已經完成或進入收尾：

- 已建立 SQLite migration 框架，並使用 `PRAGMA user_version` 管理版本。
- 已新增 runs / run_id / RunSummary 後端能力。
- 已補齊 provider 層的 OpenAI official / OpenAI-compatible 工具與流式方向，retry/backoff 與 first token timeout 也已有測試覆蓋。
- 已支援 `AGENTS.md` / `CLAUDE.md` 專案指令載入。
- 已把 RunSummary 接到前端聊天區，形成“任務完成 → 回顧 → 檢視 Git 變更 → 提交”的產品閉環入口。
- 已完成 Bash 輸出流式事件，長時間執行的測試/構建命令會通過 agent WebSocket 推送即時輸出，並在聊天區顯示即時輸出卡片。
- 已完成 Webhook 通知 MVP：服務端持久化通知配置，等待審批、任務完成、錯誤/中斷/被替代時可非同步 POST 到使用者配置端點，設定頁支援儲存與測試傳送。

因此，本檔案後續舊風險段落可視為歷史審查記錄；新的執行重點調整為：

1. checkpoint / rollback。
2. 工具權限規則表與服務端化工作流偏好。
3. Skills 服務端化與全文搜尋。
4. 成本預算與首次執行引導。
5. 通知簽名、重試佇列與通知歷史。

## 1. 產品目標

Autoto 應繼續圍繞“本地優先的 AI 編碼代理伺服器”演進，定位類似自託管版 Claude Code / OpenHands 工作臺，但差異化不應只是更多設定項，而是：

> 使用者派任務，agent 後台執行；需要使用者決策時主動提醒；完成後自動給出變更回顧；使用者審查 diff 後一鍵提交。

優先順序原則：

1. 先補核心 agent 能力，再繼續擴充設定面板和邊緣功能。
2. 先降低 provider、資料庫、工具執行、賬單和 Git 風險，再提升自動化程度。
3. 優先做能形成閉環的功能：派活、執行、審批、回顧、提交。
4. 避免現在進行大規模前端重寫；繼續通過 ES module 小步拆分。
5. 減少“配置項已展示但實際未生效”的 no-op 風險。

## 2. 當前專案定位

Autoto 當前是本地優先的 AI 編碼代理工作臺，核心能力包括：

- 嵌入式 Web UI。
- 多 LLM provider 接入。
- 帶審批的工具執行。
- Git worktree 多工作線。
- SQLite 持久化。
- PTY 終端。
- 本地安全控制。
- 多 agent / workline 工作流。
- Git diff / commit modal。
- WebSocket 驅動的聊天與工具審批事件。

當前最值得強化的是：

1. Provider 能力對等，尤其是預設 OpenAI 路徑的工具能力。
2. 資料庫遷移機制。
3. Run 級狀態、審批恢復、任務回顧與通知。
4. 統一開發檢查入口。
5. AGENTS.md / CLAUDE.md 專案指令自動載入。

## 3. 當前優勢

### 3.1 架構分層清楚

server、agent、providers、tools、db 等模組邊界相對明確。Provider 和 Tool 介面已有清晰抽象，後續擴充新模型或工具有明確路徑。

證據：

- Provider 統一入口在 `internal/providers`。
- agent 主迴圈在 `internal/agent/loop.go`。
- 工具實現集中在 `internal/tools`。
- 資料模型集中在 `internal/db/schema.go` 與 `internal/db/db.go`。

### 3.2 安全意識較強

專案已有本地 token、Origin 校驗、工具審批、Git 路徑邊界、顯式選擇路徑提交、禁止危險 Git 行為等機制。這些是 AI 編碼代理非常重要的基礎。

證據：

- 工具審批持久化在 `agent_tool_calls`。
- Git commit API 只提交顯式選擇路徑，並做敏感路徑檢查。
- `.gitignore` 已覆蓋本地 DB、執行殘留、構建產物和 secrets 類檔案。

### 3.3 工程紀律不錯

已有測試、CI、lint、GoReleaser、架構文件、專案計劃文件與安全說明。專案不是純 demo，有繼續演進為長期專案的基礎。

證據：

- `.github/workflows/ci.yml` 已包含 Go lint/test/vet/build 和前端 `node --check` / `node --test`。
- `docs/ARCHITECTURE.md` 已記錄前後端結構。
- `SECURITY.md` 已列出後續安全 hardening 方向。

### 3.4 已有閉環雛形

雖然還沒有 run 級閉環，但已有可複用基礎：

- 消息持久化。
- 工具審批卡片。
- `tool.approval_required` / `tool.finished` WebSocket 事件。
- Git status / diff / log / commit modal。
- Toast 提醒。

這意味著後續 Run 回顧檢視不必從零開始，而是把現有能力接到 run_id 與 run summary 上。

## 4. 當前主要風險

### 4.1 P0：預設 OpenAI 路徑缺工具能力

這是最高優先順序問題。

當前 agent loop 本身是 provider-agnostic 的工具閉環，但 provider adapter 能力不對等：

- Anthropic 路徑最完整，支援 tools 下發、tool_use 解析、tool_result 結構化回灌和流式文本。
- OpenAI official 路徑支援 Responses API 文本流和 usage，但沒有 native tool calling：沒有傳 `req.Tools`，沒有解析 function/tool call 事件，歷史被壓成字串。
- OpenAI-compatible 路徑使用非流式 Chat Completions，固定 `stream: false`，沒有 `tools` 引數，也沒有解析 `choices[].message.tool_calls`。

關鍵證據：

- Anthropic：`internal/providers/anthropic_provider.go`。
- OpenAI official：`internal/providers/openai_official.go`。
- OpenAI-compatible：`internal/providers/openai_compatible.go`。
- 預設模型是 OpenAI：`internal/config/defaults.go` 中預設 `openai:gpt-4.1-mini`。

影響：新使用者預設體驗可能無法完整體現“編碼代理”的工具呼叫能力。

### 4.2 P0：retry / backoff / 首 token timeout 配置存在但未真正閉環

配置中已有類似欄位：

- `FirstTokenTimeoutMs`。
- `MaxTransientRetries`。

但當前主迴圈沒有統一 retry/backoff，也沒有首事件/首 token 計時器。`api_requests.ttft_ms` 欄位存在，但記錄鏈路未完整寫入。

風險：使用者看到配置項，以為已經生效；實際 provider 抖動、429、5xx、網路 EOF 或首 token 卡住時，體驗仍可能無提示卡死或直接失敗。

### 4.3 P0：缺少資料庫遷移機制

當前資料庫初始化主要依賴 `schemaSQL` 中的 `CREATE TABLE IF NOT EXISTS`，並由 `Store.migrate(ctx)` 執行。

問題：

- 沒有 `PRAGMA user_version`。
- 沒有順序 migration 列表。
- 已存在表不會自動補欄位。
- 新庫可用、舊庫可能執行時才出現 `no such column`。
- 無法區分全新空庫、未標版本完整庫、舊 schema 庫和未來版本庫。

關鍵證據：

- `internal/db/db.go` 中 `migrate(ctx)` 只執行 `schemaSQL`。
- `internal/db/schema.go` 只設置了 `PRAGMA foreign_keys = ON`，沒有 `PRAGMA user_version`。
- 測試主要覆蓋全新臨時資料庫路徑，缺少舊庫升級測試。

### 4.4 P1：Run 級閉環缺失

當前沒有真正的 run 物件：

- 沒有 `runs` 表。
- 沒有 `run_id` 欄位。
- 沒有 run summary API。
- agent 只有 `running / idle / error / interrupted` 等粗粒度狀態。
- tool approval 有持久化，但前端 pending approval 卡片主要儲存在瀏覽器記憶體中，重新整理後可能丟失展示。

現有 Git modal 是全域性工作區視角，不知道某一輪 run 的開始和結束範圍。

### 4.5 P1：功能廣度略領先於核心深度

Settings 中已有不少面板和偏好，但部分能力仍停留在 localStorage 或 UI 層。例如 IM Gateway / 通知偏好目前更像瀏覽器本地偏好，不是服務端通知能力。

建議：先加強 agent 執行可靠性、審批恢復、通知、回顧、回滾等主流程體驗，再繼續擴充設定項。

### 4.6 P1：巨型檔案維護風險

當前不是單檔案前端，已經有 ES module 拆分；但主控檔案仍偏大。

風險證據：

- `internal/agent/loop.go` 約 1400 行。
- `internal/server/static/modules/app-main.mjs` 約 1700 行。
- `internal/server/static/styles.css` 超過 4000 行。
- `model-provider-settings.mjs`、`system-settings.mjs`、`local-preferences-settings.mjs` 等設定模組也較大。

建議：不要全量 React/Vite 重寫；繼續圍繞功能邊界拆 `run-status.mjs`、`approval-state.mjs`、`run-summary.mjs`、`notification-center.mjs`。

### 4.7 P2：倉庫衛生需要收尾，但不是大風險

當前 `.gitignore` 已覆蓋主要產物：

- `/autoto`。
- `.autoto-run-*.json`。
- `*.db`、`*.db-wal`、`*.db-shm`。
- `.env`、`*.log`、`tmp/`、`coverage.out` 等。

觀察到的本地殘留：

- `autoto`：Mach-O 執行檔，已被忽略。
- `.autoto-run-57314.json`：執行殘留，已被忽略。
- `needtodo0709`：當前報告檔案，未跟蹤且未忽略。
- 空目錄：`internal/auth`、`internal/agent`、`internal/project`。

因此原計劃裡“補 `.gitignore`”應改為“確認規則、清理殘留、說明空目錄用途、決定報告檔案是否納入版本控制”。

## 5. 修正版優先順序總覽

### P0：必須優先補齊

1. OpenAI / OpenAI-compatible 工具能力、流式與 provider parity。
2. 統一 retry/backoff、首 token timeout、可讀錯誤提示。
3. SQLite migration 骨架與舊庫升級測試。

### P1：高價值產品閉環

1. Run 物件、run_id、run 狀態機。
2. 可恢復工具審批。
3. Run summary API 與回顧卡片。
4. Bash 輸出流式顯示。
5. AGENTS.md / CLAUDE.md 專案指令自動載入。
6. 統一檢查腳本與文件命令收斂。

### P2：中期增強

1. Webhook / 多渠道通知。
2. 檢查點與回滾。
3. Worklines 工作線視覺化。
4. Skills 服務端化。
5. 會話全文搜尋。
6. 成本預算與告警。
7. 分支 push 與 PR 草稿。
8. 首次執行引導。

## 6. P0-1：Provider tool parity 與可靠性補齊

### 6.1 目標

讓 Anthropic、OpenAI official、OpenAI-compatible 在核心 agent loop 上儘量能力對等：

使用者訊息 → 模型請求工具 → 審批 → 工具執行 → 工具結果回灌 → 模型繼續回答。

### 6.2 當前差異

#### Anthropic

當前最完整：

- 支持流式文本。
- 支援 tools schema 下發。
- 支持 tool_use 解析。
- 支援 tool_result 結構化回灌。
- 支持 usage。
- 支援圖片 base64。

仍缺：

- Autoto 層統一 retry/backoff。
- 首 token timeout。
- 更統一的 provider 錯誤分類。

#### OpenAI official

已有：

- Responses API 文本流。
- usage。
- 部分錯誤事件處理。

缺口：

- 沒有 native tool calling。
- 沒有把 `GenerateRequest.Tools` 傳給 OpenAI。
- 沒有解析 function/tool call 事件。
- 歷史訊息被壓成文本 transcript，tool_use / tool_result 結構丟失。

#### OpenAI-compatible

已有：

- `/models`。
- optional API key。
- 非流式 Chat Completions。
- usage。
- 圖片以 `image_url` 傳送。

缺口：

- 沒有流式輸出。
- 沒有 `tools` 引數。
- 沒有解析 `choices[].message.tool_calls`。
- tool result 只是文本化，不是標準 Chat Completions tool message。
- 如果 endpoint 返回空 `content` + `tool_calls`，當前可能把 tool call 靜默吞掉。

### 6.3 建議任務

#### A. 定義跨 provider 工具 contract

明確 `providers.GenerateRequest.Tools` 的 provider contract：

- 工具 schema 如何表達。
- tool call ID 如何映射。
- tool arguments 如何累積和驗證。
- tool result 歷史如何傳回下一輪模型請求。
- denied / error 工具結果如何表達。
- provider 不支援 tools 時如何降級提示。

#### B. OpenAI official 支持 native tools

任務：

- 不再只用純文本 `renderTranscript`。
- 將工具 schema 傳入 Responses API。
- 解析 function/tool call 事件並 emit `providers.Event{Type: "tool_call"}`。
- 將歷史中的 tool_use / tool_result 轉為 OpenAI 可理解的結構化 input。
- 覆蓋 denied tool、tool error、JSON arguments 分片等邊界。

#### C. OpenAI-compatible 支援 streaming 與 tools

任務：

- Chat Completions 請求增加 `tools`。
- 支持 `stream: true` SSE。
- 累積 `delta.tool_calls[*].function.arguments`。
- 完成後 emit `tool_call` 事件。
- 下一輪請求把 tool result 轉成 `role: "tool"` 或相容格式。
- 對不支援 tools 的 compatible endpoint 提供 capability/degrade 提示。

#### D. Provider capability 標記

建議新增能力標記：

- `SupportsTools`。
- `SupportsStreaming`。
- `SupportsVision`。
- `SupportsUsage`。
- `SupportsPromptCache`。

用途：

- UI 可以給出明確提示。
- agent 可以在不支援 tools 時避免假裝可用。
- 設定頁可以更準確展示 provider 能力差異。

#### E. 統一 retry/backoff 與首 token timeout

任務：

- 在 provider 外層或 `Runner.runModelTurn` 實現統一 retry。
- 使用現有 `FirstTokenTimeoutMs` 與 `MaxTransientRetries` 配置。
- transient 錯誤至少包含 HTTP 408 / 409 / 429 / 5xx、網路 reset、EOF、temporary timeout。
- 401/403、invalid request/schema、context canceled 不重試。
- 流式規則：首事件前失敗可重試；已經向 UI 輸出文本或 tool_call 後不靜默重試，避免重複輸出或重複工具呼叫。
- 記錄 TTFT 到 `api_requests.ttft_ms`。

### 6.4 驗收標準

- OpenAI official fake server 返回 function/tool call 後，Runner 會執行工具併發起第二次模型請求。
- 第二次 OpenAI 請求包含結構化工具結果，不是純文本拼接。
- OpenAI-compatible fake SSE 返回 `tool_calls` delta 後，Runner 能執行工具並繼續回答。
- denied tool result 能作為錯誤工具結果回灌。
- compatible 非流式 tool_calls 不會被吞掉。
- provider 臨時 500/429 前幾次失敗、後續成功時能按配置重試。
- 401/403 不重試，並顯示可讀錯誤。
- 首事件超過 `FirstTokenTimeoutMs` 後退出，並在 UI/DB 中顯示明確 timeout。
- partial stream 後錯誤不自動重試。
- `api_requests.ttft_ms` 有非零記錄，並可在 usage/runtime 檢視中展示。
- 測試覆蓋 `internal/providers/*_test.go` 與 `internal/agent/loop_test.go` 的端到端 adapter case。

## 7. P0-2：引入資料庫遷移機制

### 7.1 目標

為後續 runs、skills、預算、搜尋等 schema 演進打基礎，避免舊資料庫升級事故。

### 7.2 當前狀態

當前 DB 初始化路徑：

- `cmd/autoto/main.go` 開啟資料庫。
- `internal/config/defaults.go` 設定預設 DB 路徑。
- `internal/db/db.go` 的 `Open(...)` 呼叫 `store.migrate(ctx)`。
- `store.migrate(ctx)` 只執行 `schemaSQL`。
- `schemaSQL` 在 `internal/db/schema.go` 中，用一大段 `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS`。

當前表包括：

- `users`。
- `projects`。
- `worklines`。
- `agents`。
- `agent_messages`。
- `agent_message_attachments`。
- `agent_tool_calls`。
- `api_requests`。
- `agent_backends`。
- `mcp_servers`。

注意：部分文件裡的表清單可能已舊，需同步補上 `agent_message_attachments` 與 `mcp_servers`。

### 7.3 風險

- 舊庫已有表但缺欄位時，`CREATE TABLE IF NOT EXISTS` 不會補欄位。
- 程式碼可能在執行時才遇到 `no such column`。
- 沒有版本號，無法判斷當前 DB 是新庫、完整舊庫、部分舊庫還是未來版本。
- 沒有 migration 事務，失敗後可能半升級。
- 未來新增欄位若只改 baseline schema，新使用者正常，老使用者失敗。

### 7.4 建議任務

新增 `internal/db/migrations.go`：

- `const CurrentDBVersion = 1`。
- migration struct：`version`、`name`、`up func(context.Context, *sql.Tx) error`。
- `getUserVersion`。
- `setUserVersion`。
- `runMigrations`。
- `tableExists`。
- `columnExists`。
- `ensureColumn`。
- `ensureIndex`。

遷移流程：

1. 開啟 SQLite。
2. 明確啟用 `PRAGMA foreign_keys = ON`。
3. 讀取 `PRAGMA user_version`。
4. 如果版本大於 `CurrentDBVersion`，拒絕啟動並返回可讀錯誤。
5. 如果是空庫，執行 v1 baseline schema 並設定版本。
6. 如果是未標版本但已有表的舊庫，檢查並補齊當前程式碼依賴的缺失表、欄位和索引。
7. 每個 migration 在事務中執行。
8. 每個 migration 成功後更新 `PRAGMA user_version`。
9. 後續新增表/欄位必須追加 migration 和測試，不得只修改 baseline schema。

### 7.5 Quick win 範圍

1-2 天內可以先做骨架：

- 不引入複雜業務 schema。
- 將當前 schema 作為 v1 baseline。
- 支援空庫初始化。
- 支援未標版本完整舊庫設為當前版本。
- 至少構造一個缺欄位舊庫測試，證明能補列且不丟資料。

### 7.6 驗收標準

- 新建空資料庫後，所有當前表和索引存在。
- 新建空資料庫後，`PRAGMA user_version = CurrentDBVersion`。
- 當前未標版本但 schema 完整的舊庫，開啟後不丟資料並設定版本。
- 人工構造舊庫缺代表性欄位，例如 `agents.prune_boundary_message_id`，開啟後自動補齊。
- 連續 `Open` 兩次不報錯，資料行數不變化，migration 冪等。
- 未來版本庫被拒絕，錯誤資訊清晰。
- 遷移後 `PRAGMA foreign_keys = 1`。
- 測試建議放在 `internal/db/db_test.go`：
  - `TestOpenInitializesUserVersionForNewDatabase`。
  - `TestOpenMigratesLegacyZeroVersionDatabase`。
  - `TestOpenMigratesLegacyDatabaseMissingAgentColumns`。
  - `TestMigrateIsIdempotent`。
  - `TestOpenRejectsFutureDatabaseVersion`。
  - `TestForeignKeysEnabledAfterOpen`。

## 8. P1：Run 級執行閉環、審批恢復與回顧檢視

### 8.1 目標

形成 Autoto 的核心產品閉環：

派活 → 後台執行 → 需要審批時提醒 → 使用者批准/拒絕 → 工具繼續執行 → 完成後回顧 → 審查 diff → 提交。

### 8.2 當前狀態

已有基礎：

- agent loop 可後台 goroutine 執行。
- WebSocket 有 `agent.started`、`agent.done`、`agent.error`、`tool.approval_required`、`tool.finished` 等事件。
- 工具審批記錄在 `agent_tool_calls`。
- 前端能顯示審批卡片並呼叫審批 API。
- Git modal 已支持 status / diff / log / commit。
- Toast 能顯示本地提醒。

缺口：

- 沒有 `runs` 表。
- 沒有 `run_id`。
- 沒有 run 狀態機。
- 沒有 run summary API。
- pending approval 前端狀態主要儲存在記憶體，重新整理後展示可能丟失。
- run 結束後不會自動展示 summary card。
- Git modal 不是 run-aware，無法自動關聯本輪觸碰檔案。

### 8.3 建議把 P0-3 修正為 P1-核心閉環

原報告把“後台任務 + 審批通知 + Run 回顧檢視”列為 P0。經審查，它很有產品價值，但應排在 provider parity 和 DB migration 之後。

更準確的標題：

> P1：Run 級執行閉環與審批恢復。

### 8.4 資料模型建議

新增 `runs` 表：

- `id`。
- `agent_id`。
- `trigger_message_id`。
- `status`：`queued / running / waiting_approval / completed / error / interrupted`。
- `started_at`。
- `completed_at`。
- `error_message`。
- `base_head`。
- `end_head`。
- `created_at`。
- `updated_at`。

新增字段：

- `agent_messages.run_id`。
- `agent_tool_calls.run_id`。
- 可選：`api_requests.run_id`。

### 8.5 API 建議

新增：

- `GET /api/agents/{id}/runs?limit=...`。
- `GET /api/agents/{id}/runs/{runId}`。
- `GET /api/agents/{id}/runs/{runId}/summary`。
- `GET /api/agents/{id}/tool-calls?status=pending_approval` 或 run-scoped pending approvals API。

Run summary 內容：

- run 狀態、耗時、觸發訊息、最終訊息。
- 模型請求次數、usage、成本。
- 工具呼叫列表：名稱、狀態、耗時、錯誤。
- pending / denied / error 工具數。
- 觸碰檔案：初版可從工具 input/output 近似提取，後續再做可靠追蹤。
- Git diff 檔案數與 +/- 統計。
- 錯誤資訊。
- 操作入口：檢視 diff、去提交、複製 summary。

### 8.6 前端建議

新增或拆分模組：

- `run-status.mjs`：當前 run 狀態、頂部/聊天區狀態卡。
- `approval-state.mjs`：pending approval 恢復、審批卡片狀態、審批 API。
- `run-summary.mjs`：summary card、summary API、diff/commit 入口。
- `notification-center.mjs`：in-app toast 與後續 webhook 配置入口。

第一版不必立刻做 webhook，先做 in-app durable 狀態：

- running 時顯示 run 狀態。
- waiting approval 時顯示明顯提醒。
- 重新整理頁面後仍能恢復 pending approval。
- done/error/interrupted 時顯示 summary card。

### 8.7 驗收標準

- 傳送一條使用者訊息後建立一個 run。
- `agent.started / tool.approval_required / tool.finished / agent.done / agent.error` 都攜帶 `runId`。
- UI 可區分 `running / waiting_approval / completed / error / interrupted`。
- 工具等待審批時，重新整理頁面後仍能看到審批卡片並完成審批。
- run 完成後顯示 summary card，至少包含工具呼叫數量、changed files 數量、diff +/- 統計、錯誤文本。
- “檢視 diff / 去提交”複用現有 Git modal。
- 若無法可靠識別本輪檔案，第一版至少開啟 Git modal 並顯示提示。
- error/interrupted 也生成 summary，不只成功 run。
- 增加 e2e 或整合測試覆蓋：提交訊息 → 等待審批 → 批准 → tool finished → run done → summary card → Git modal 可開啟。

## 9. P1：後台通知能力

### 9.1 當前狀態

已有：

- 前端 toast。
- 瀏覽器本地通知偏好。
- IM Gateway / 通知相關設定頁雛形。

但當前沒有真正的服務端 webhook 通知能力：

- 沒有 webhook URL 服務端配置。
- 沒有通知傳送器。
- 沒有簽名、重試、脫敏。
- 沒有 run 狀態變化觸發器。

### 9.2 建議分階段實現

#### 階段 1：in-app durable notification

依賴 run 狀態機：

- run waiting_approval 時 UI 明顯提醒。
- run completed/error/interrupted 時 UI toast + summary card。
- 頁面重新整理後可恢復當前等待審批的 run。

#### 階段 2：webhook MVP

新增：

- server-side webhook URL 配置。
- run 狀態變化觸發通知。
- 通知包含專案名、agent、run status、工具摘要、跳轉連結。
- 基礎重試。
- 敏感內容脫敏。

#### 階段 3：多渠道與安全增強

- 通知簽名。
- 每專案/每 agent 策略。
- 多渠道。
- 失敗佇列與重放。

### 9.3 驗收標準

- run 進入 waiting_approval / completed / error / interrupted 時至少有 in-app durable 提醒。
- webhook 配置開啟後，服務端能傳送狀態變化通知。
- 通知失敗不會阻塞 agent loop。
- 通知內容不洩露 API key、token、完整敏感命令輸出。
- 重試次數有限，失敗可診斷。

## 10. 1-2 天 Quick Wins（修正版）

### 10.1 統一檢查腳本

優先順序：高。

當前沒有 `Makefile`、`scripts/check.sh`、`package.json`。README / CONTRIBUTING / docs / PROJECT_PLAN 中存在多處手寫檢查命令，容易和 CI 漂移。

任務：

- 新增 `scripts/check.sh` 作為唯一權威本地檢查入口。
- 可選新增 `Makefile`，讓 `make check` 呼叫 `scripts/check.sh`。
- CI 改為呼叫同一個腳本，減少重複維護。
- README / CONTRIBUTING / docs 中只保留一條統一命令。

建議檢查內容：

- `gofmt -l ./cmd ./internal`，檢查格式但不自動改寫。
- `go test ./...`。
- `go vet ./...`。
- `go build ./...`。
- 前端 `node --check`。
- `node --test internal/server/static/modules/*.test.mjs`。

驗收標準：

- 開發者執行一個命令即可完成主要檢查。
- CI 與本地檢查入口一致。
- README 不再維護一長串手寫 `node --check` 清單。
- `check` 不做破壞性或自動格式化改寫；如需格式化，另設 `fmt`。

### 10.2 DB migration 骨架

優先順序：高。

任務：

- 建立 `PRAGMA user_version` 遷移框架。
- 將當前 schema 作為 v1 baseline。
- 新增新庫、未標版本舊庫、缺欄位舊庫、冪等、未來版本保護測試。

驗收標準見第 7 節。

### 10.3 OpenAI tool calling 實現點梳理

優先順序：高。

任務：

- 明確 OpenAI official Responses API tools 對應。
- 明確 OpenAI-compatible Chat Completions tools 對應。
- 明確 agent message history 中 tool_use / tool_result 的 provider-specific 轉換。
- 寫 fake server 測試設計。

產出：

- provider contract 文件或程式碼註釋。
- 至少一個 OpenAI official tool call fake test。
- 至少一個 OpenAI-compatible streaming tool call fake test。

### 10.4 AGENTS.md / CLAUDE.md 專案指令載入

優先順序：高，難度：低到中。

當前未實現。

任務：

- 支援讀取 agent cwd 下的 `AGENTS.md`。
- 可選支援 `CLAUDE.md`。
- 做路徑邊界校驗。
- 設定大小上限和截斷提示。
- 將內容合併到 system prompt。
- UI/API 顯示“已載入專案指令：檔名 / 是否截斷”。

驗收標準：

- 專案根目錄存在 `AGENTS.md` 時，agent 自動參考其中規範。
- 同時存在 `AGENTS.md` 與 `CLAUDE.md` 時，有明確優先順序或合併規則。
- 檔案過大時截斷，不導致上下文失控。
- 讀取失敗不阻塞主流程，但給出可診斷提示。
- 使用者能知道當前是否載入了專案指令。

### 10.5 抽出模型價格表

優先順序：中。

任務：

- 將硬編碼價格表從 `internal/agent/loop.go` 中拆出。
- 可以放到獨立 Go 檔案或嵌入 JSON。
- 給價格表單獨測試。

驗收標準：

- loop 主邏輯更短。
- 價格表可單獨測試和更新。
- 未知模型有明確 fallback。

### 10.6 倉庫衛生收尾

優先順序：中。

任務：

- 確認 `.gitignore` 已覆蓋當前觀察到的 `/autoto` 和 `.autoto-run-*.json`。
- 刪除或保留說明本地殘留 `autoto`、`.autoto-run-57314.json`。
- 處理空目錄 `internal/auth`、`internal/agent`、`internal/project`：刪除或加明確用途佔位。
- 明確 `needtodo0709` 是否納入版本控制。

驗收標準：

- `git status` 中沒有無意義產物。
- 編譯產物和執行殘留不會被提交。
- 空目錄若保留，應有明確用途或佔位說明。
- 報告檔案的版本控制策略明確。

## 11. 1-2 周短期計劃

### 11.1 Provider 可靠性補齊

包含：

- OpenAI official native tool calling。
- OpenAI-compatible streaming。
- OpenAI-compatible tool_calls。
- provider capability 標記。
- retry/backoff。
- first token timeout。
- TTFT 記錄。
- 錯誤分類與 UI 提示。

優先順序：最高。

### 11.2 DB migration 完整化

包含：

- migration skeleton。
- legacy zero-version database 兼容。
- 缺欄位補齊。
- 未來版本保護。
- migration 文件。
- schema change policy。

### 11.3 Bash 輸出流式顯示

目標：長命令執行時不要等結束才看到結果。

當前問題：`BashTool.Execute` 使用類似 `CombinedOutput()` 的結束後返回模式，前端只能在工具完成後看到結果。

MVP：

- Bash 執行時按塊傳送 stdout/stderr chunk。
- 前端工具卡片即時追加輸出。
- DB 中仍儲存最終截斷結果。
- timeout 後保留已輸出內容並標記 error。

價值：

- 執行測試、構建、安裝依賴時體感明顯提升。
- 使用者更容易判斷 agent 是否卡住。

### 11.4 Plan Mode 閉環

目標：讓 agent 在動手修改前先只讀分析併產出計劃。

當前有一些 plan mode 痕跡，但行為未完整閉環。

MVP：

- 新增或明確 `plan` 權限模式。
- 只允許只讀工具。
- system prompt 要求先產出計劃。
- 使用者批准後再切回可寫模式。
- UI 明確顯示當前處於 plan mode。

價值：

- 降低 agent 誤改風險。
- 適合複雜任務和高成本模型。

### 11.5 結構化日誌與 run ID

目標：提升問題排查能力。

任務：

- 使用結構化日誌記錄 provider 請求、工具呼叫、審批等待、錯誤。
- run ID 貫穿 agent loop、工具執行和 WebSocket 事件。

驗收標準：

- 使用者反饋“卡住”時，可以通過 run ID 快速定位階段。
- provider 失敗、工具失敗、審批等待可區分。

## 12. 1-2 個月中期計劃

### 12.1 Run 回顧檢視

MVP：

- 新增 run summary API。
- 聚合本輪工具呼叫、觸碰檔案、diff 統計、錯誤資訊。
- 前端在 run 結束時顯示摘要卡片。
- 摘要卡片提供“查看 diff / 去提交”入口。

價值：

- 使用者不用在聊天、工具卡片、Git 面板之間來回切。
- 更容易審查 agent 結果。

### 12.2 後台任務 + 審批通知

MVP：

- 支持配置 webhook URL。
- run 進入 waiting_approval / completed / error / interrupted 時傳送通知。
- 通知包含專案名、agent、工具摘要、跳轉連結。

後續增強：

- 重試機制。
- 通知簽名。
- 多通知渠道。
- 每個專案單獨配置通知策略。

### 12.3 檢查點與回滾

MVP：

- 每輪 run 開始前記錄 Git 狀態。
- 如果 worktree 乾淨，記錄 HEAD。
- 如果 worktree 髒，考慮建立臨時快照。
- 提供恢復到 run 開始前的 API 和 UI 按鈕。

注意：

- 初版只支援 Git 倉庫內檔案。
- 必須明確提示會影響當前未提交改動。
- 不應偷偷執行破壞性 Git 命令。

### 12.4 Worklines 工作線視覺化面板

MVP：

- 側邊欄或獨立面板展示 workline 樹/列表。
- 顯示分支名、worktree 狀態、merge-check 狀態。
- 提供 fork、merge-check、merge 的入口。

價值：

- 將 Autoto 獨特的多工作線能力產品化。
- 降低使用者理解 workline / fork / merge 的門檻。

### 12.5 Skills 服務端化

MVP：

- 新增 `skills` 表。
- 提供 CRUD API。
- 前端從服務端讀取和儲存 skills。
- 保留從 localStorage 匯入的遷移路徑。

價值：

- 技能不再只存在瀏覽器。
- 可跨 agent、跨專案或未來跨裝置複用。

### 12.6 會話全文搜尋

MVP：

- 使用 SQLite FTS5 為訊息內容建立索引。
- 新增搜索 API。
- 前端提供全局搜索入口。
- 搜尋結果可跳轉到對應 agent / message。

價值：

- 長期使用後能找回歷史決策、bug 修復過程和 agent 操作記錄。

### 12.7 專案成本預算與告警

MVP：

- 專案級預算。
- agent 或 run 級成本統計。
- 超過閾值時提醒或阻止繼續執行。
- 明確展示估算價格來源和未知模型 fallback。

## 13. 長期方向

### 13.1 真實登入與 session 認證

當前不必立刻做多使用者，但建議提前把本地 token、遠端硬化模式、使用者表等概念抽象成統一 session 介面。

長期價值：

- 支援更安全的遠端訪問。
- 為未來多裝置或多使用者能力留介面。

### 13.2 AI 衝突解決與 review workline

目標：進一步發揮 workline / worktree 架構價值，讓 agent 可以輔助處理 merge conflict 或獨立審查另一條工作線。

### 13.3 MCP 長連線會話池

目標：提升 MCP 工具呼叫效率，減少重複初始化開銷。

### 13.4 前端框架遷移評估

目前不建議立刻全量遷移 React / Vite。應先繼續拆分現有模組，等 UI 複雜度確實超過原生模組維護能力後，再做框架遷移評估。

## 14. 可新增功能清單

| 功能 | 優先順序 | 難度 | MVP |
| --- | --- | --- | --- |
| OpenAI native tool calling | P0 | 中 | OpenAI 模型可完成工具呼叫閉環 |
| OpenAI-compatible streaming + tools | P0 | 中 | SSE 流式與 tool_calls 解析 |
| retry/backoff/first token timeout | P0 | 中 | 臨時錯誤有限重試，首 token 超時可見 |
| DB migration | P0 | 中 | `PRAGMA user_version` + baseline migration |
| 統一檢查腳本 | P1 | 低 | `scripts/check.sh` / `make check` |
| AGENTS.md / CLAUDE.md 載入 | P1 | 低到中 | 自動載入專案指令並顯示狀態 |
| Run 狀態機 | P1 | 中 | `runs` 表 + run_id + 狀態展示 |
| 可恢復審批 | P1 | 中 | 重新整理後仍能看到 pending approval |
| Run 回顧檢視 | P1 | 中 | summary API + summary card |
| Bash 輸出流式顯示 | P1 | 中 | 工具卡片即時追加輸出 |
| 後台 webhook 通知 | P2 | 中 | run 狀態變化傳送 webhook |
| 檢查點與回滾 | P2 | 中 | run 開始前記錄 Git 狀態 |
| Worklines 工作線視覺化 | P2 | 中 | 展示 workline、branch、worktree 狀態 |
| 會話全文搜尋 | P2 | 低到中 | SQLite FTS5 搜尋訊息 |
| 成本預算與告警 | P2 | 低 | 專案預算和閾值提醒 |
| Skills 服務端化 | P2 | 中 | skills 表 + CRUD API |
| 分支 push 與 PR 草稿 | P3 | 中 | 顯式確認後生成/push PR 草稿流程 |
| 首次執行引導 | P2 | 低 | 配置 key、驗證模型、建立專案三步向導 |

## 15. 暫時不建議優先做的事情

1. 繼續增加 Settings 面板。
2. 繼續新增 browser-local 偏好。
3. 前端全量重寫到 React / Vite。
4. 多使用者或團隊協作能力。
5. 在沒有真實服務端通知能力前繼續打磨 IM Gateway 策略面板。
6. 過早做複雜雲同步或賬號體系。
7. 在沒有 run_id 前做複雜 run summary UI。
8. 在沒有 provider capability 前假設所有 provider 能力等價。

## 16. 建議的 0709 執行順序

如果 0709 只安排一天，建議按這個順序做：

1. 建立 `scripts/check.sh` / `make check`，把 README 和 CI 收斂到同一入口。
2. 加 DB migration 骨架：`PRAGMA user_version`、baseline migration、基礎舊庫測試。
3. 梳理並實現 OpenAI official tool calling 的最小閉環 fake test。
4. 梳理 OpenAI-compatible streaming + tool_calls 的 fake test 和實現點。
5. 加 `AGENTS.md` 專案指令載入。
6. 把模型價格表從 `loop.go` 中拆出。
7. 清理或說明本地殘留與空目錄。

如果當天時間充足，再繼續：

8. 開始實現 provider retry / first token timeout。
9. 草擬 `runs` 表和 run summary API 資料結構。
10. 為 pending approval 恢復設計 API。

## 17. 最重要的三個下一步

1. **修復預設 OpenAI 路徑的工具能力**：OpenAI official 和 OpenAI-compatible 必須能完成工具呼叫閉環，否則預設體驗不像 AI coding agent。
2. **建立 DB migration 機制**：儘快引入 `PRAGMA user_version`，避免後續 runs、skills、search 等 schema 演進造成舊庫事故。
3. **建立 run_id 與可恢復審批**：這是後台通知、Run 回顧檢視、diff/commit 閉環的共同前置條件。

## 18. 核心產品判斷

Autoto 最有潛力的差異化功能不是繼續堆設定項，而是把“常駐伺服器 + 持久化 + 多工作線 + 審批工具執行”結合起來，形成普通 CLI agent 難以做到的體驗：

> 使用者派任務，agent 後台執行；需要使用者決策時主動提醒；完成後自動給出變更回顧；使用者審查 diff 後一鍵提交。

因此，後續功能應該圍繞這個閉環推進。但實現順序上必須先補底座：provider parity、DB migration、run_id。只有底座穩定，通知、回顧、回滾和自動提交才不會變成脆弱的 UI 包裝。