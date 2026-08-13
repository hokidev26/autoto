# Session Surface Projection 評估提案

狀態：討論稿（尚未實作、尚未承諾實作）
日期：2026-08-13
適用專案：Autoto
對照實作：DeepSeek Harness（TypeScript agent harness，以下簡稱 DSH）

> **本文不是 Autoto 現有行為的說明文件。**
>
> `docs/` 下的其餘文件（尤其 `ARCHITECTURE.md`、`PLUGINS.md`）描述的是**已經實作**的狀態。本文評估的是「是否應該引入一組目前不存在的機制」。
>
> 只有第 2 節是現況基準線，且每一條都附 `file:line`。**第 5 節之後出現的所有資料表、欄位與函式都尚不存在**，不得當成實作說明閱讀，也不得被其他文件引用為現況。
>
> 行號基準：撰寫時的工作樹。`internal/agent/runner_context.go`、`context_management.go`、`continuation.go`、`internal/config/defaults.go` 當時皆有未提交的修改，`internal/agent/context_summary.go` 與 `context_estimate.go` 當時尚未納入版控，因此行號可能已漂移；函式與欄位名稱才是穩定的錨點。

## 1. 摘要（結論先行）

DSH 不存 messages 表，而是存一份 append-only 的 typed session event log；送給模型的訊息列表是把這份 log 折疊（fold）後**推導**出來的。每個可產生模型可見訊息的事件都帶一個 `surfaceOp`（`append` 或 `{op:'replace', start, end}`）與 `sourceEventSeqs`。壓縮因此變成「append 一個遮蔽某區間的 replace 事件」，而不是改寫或刪除歷史。

評估結論分三句：

1. **不建議**把 Autoto 改寫成 event store。成本是整個持久化層加上每一條讀取路徑，而換到的 fork/replay 能力 Autoto 目前並不以產品功能提供。
2. Autoto 真正缺的**不是**事件日誌。Autoto 的原始素材（`agent_messages`、`agent_tool_calls.output_json`）本來就不會被壓縮改寫，DSH 靠「log 是唯一真相」換到的「壓縮前的視圖仍可推導」，Autoto 已經有了。缺的是**「當時到底送出了什麼」的憑證**——這正是 `Model-visible ⟺ logged` 這條 invariant 要關的缺口。
3. 因此建議做增量 **A（持久化每次請求的組成清單）** 與 **C（給 `superseded_at` 加上原因與指向）**；增量 **B（把摘要壓縮改成 append-only 記錄）** 條件性採用；明確**拒絕**通用 `surface_op` 區間替換、獨立 format version 常數、以及完整 event-sourced 改寫。

## 2. Autoto 現況（基準線）

### 2.1 持久化的形狀

對話存在 `agent_messages`（`internal/db/schema.go:314-347`）。與本文相關的欄位：`role`、`content_json`、`provider_state_json`、`content_text`、`correction_of_message_id`（`schema.go:333`）、`superseded_at`（`schema.go:334`）、`created_at`。Go 端結構是 `db.Message`（`internal/db/types.go:160-182`）。

三件事值得先講清楚，因為它們決定了後面的成本評估：

- **沒有 sequence 欄位。** 整個對話以 `(created_at, id)` 元組排序讀取，`internal/db/messages_attachments.go:298-300` 的註解明確寫了這一點。
- **壓縮不會改寫訊息列。** 沒有任何路徑會因為上下文壓縮而 UPDATE `content_json`。
- **工具原始輸出另有一份。** `agent_tool_calls.output_json`（`schema.go:389`）獨立保存完整工具輸出。

已持久化的壓縮狀態放在 `agents` 表的三個欄位：`context_summary`（`schema.go:171`）、`prune_boundary_message_id` 與 `pruned_percent`（`schema.go:192-193`），寫入點是 `UpdateAgentContextSummary`（`internal/db/projects_worklines_agents.go:817-829`）。

### 2.2 請求組裝：`managedContextForTurn` 的四個階段

`internal/agent/runner_context.go:308-407`，唯一的生產呼叫點在 `internal/agent/continuation.go:501-508`。

1. **推導基準列表。** `providerMessagesForContextPlan`（`runner_context.go:511-533`）從 `prune_boundary_message_id` 之後開始（`contextBoundaryStart`，`internal/agent/context_management.go:149-160`），先塞入 `agent.ContextSummary` 渲染成的 system 訊息（`summaryProviderMessage`，`runner_context.go:652-671`），跳過 `SupersededAt != ""` 的列（`runner_context.go:522`），並為每列標記是否「可裁剪」（早於最後 `CompactKeepTurns` 輪者為 true，`contextRecentTurnsStart`，`context_management.go:175-189`）。
2. **漸進式裁剪（opt-in）。** 若 `agent.PruneEnabled` 且估算值達 `PruneStart`%，`progressivelyPruneContextToolPayloads`（`context_management.go:269-347`）把 `tool_use.Input` 換成 `{"_autotoCompacted":true}`（`context_management.go:284`）、把 `tool_result.Output` 換成 `[Tool X executed; output omitted]`（`context_management.go:289`、`runner_context.go:854-860`）。裁剪量以 5% 視窗為單位向上取整，目的是讓請求前綴在連續輪之間位元穩定、不打壞 provider prompt cache（`context_management.go:313-322`）。
3. **摘要壓縮（自動安全動作）。** 若估算值達 `CompactStart`%，`selectContextTurnCandidates`（`context_management.go:191-216`）挑出最舊的完整輪，`summarizeOldestMessages`（`internal/agent/context_summary.go:21-31`）產生摘要，然後**持久化**到 `agents` 的三個欄位並重新推導（`runner_context.go:346-363`）。
4. **硬視窗兜底。** 仍然超限時依序：`compactOversizedContextToolInputs`（`runner_context.go:775-795`）→ 再試一次摘要壓縮（`runner_context.go:377-398`）→ `compactConversationForBudget`（`runner_context.go:763-773`，內含 `compactAllContextToolResults` 與 `truncateContextSummaryForBudget`，`runner_context.go:797-852`）→ `fitTurnSystemControls`（`runner_context.go:400-406`）。

閾值預設為 standard 80/90、large 85/92（`internal/config/defaults.go:100-102`），`CompactKeepTurns` 預設 2（`internal/config/defaults.go:654-659`）。

### 2.3 關鍵區分：哪些壓縮持久化了，哪些沒有

| 階段 | 機制 | durable 記錄 |
| --- | --- | --- |
| 2 漸進式裁剪 | `progressivelyPruneContextToolPayloads` | **無**。只作用於記憶體中的 `[]providers.Message` |
| 3 摘要壓縮 | `UpdateAgentContextSummary` | **有**，但是單一可覆寫欄位（見 3.2） |
| 4 兜底裁剪 | `compactOversizedContextToolInputs` / `compactAllContextToolResults` / `truncateContextSummaryForBudget` | **無** |
| — | 最終送出的訊息列表本身 | **無** |

`recordAPIRequest`（`internal/agent/runner_model.go:665-703`）在呼叫**之後**記錄 usage、延遲與成本，不記錄請求內容。`api_requests` 表確實有一個 `raw_dump_json` 欄位（`schema.go:441`、`types.go:400`），但生產程式碼從未填值：唯一觸及它的是 `AddAPIRequest` 的 INSERT（`internal/db/tool_calls_usage.go:315`），寫入的永遠是零值；只有測試會塞入內容，而 usage API 也明確斷言不得外洩該欄位（`internal/server/usage_history_test.go:102-105`）。

### 2.4 `SupersededAt` 今天的確切語意

欄位註解寫得很準確：「set on the messages a correction retired. They stay in the transcript so the history remains readable, but are withheld from the model.」（`types.go:173-175`）

三個寫入點，語意都是**尾段退場**：

- **修正（correction）**：`internal/db/messages_attachments.go:296-308`。退場「被修正的那一列本身，以及它之後的一切」（`id >= sourceMessageID`）。
- **重跑（rerun）**：`messages_attachments.go:354-362`。退場「嚴格在該列之後的一切」，目標列本身保留（`messages_attachments.go:355-356` 的註解說明了與 correction 的差異）。
- **回捲（rollback）**：`internal/db/message_operations.go:11-53`，UPDATE 在 `message_operations.go:35-38`。同樣是嚴格之後。

因此它的形狀是：**以 `(created_at, id)` 為序的後綴標記，起點隱含、沒有終點、沒有原因、沒有從「遮蔽者」指向「被遮蔽區間」的指標。** `correction_of_message_id` 指的是「新列的來源列」，方向與粒度都不同——它不表達「我遮蔽了這 14 列」。讀取端只是一個 `superseded_at IS NULL` 過濾，並有對應的 partial index（`schema.go:342-345`）。

### 2.5 migration 機制與它的兩個約束

`runMigrations`（`internal/db/migrations.go:163-200`）：讀 `PRAGMA user_version`，比 `CurrentDBVersion`（目前 65，`migrations.go:13`）新則**拒絕開啟**（`migrations.go:172-174`）；空庫走 `migrateV1Baseline` 一次性建表；否則依序重播增量 migration。每個 migration 在單一 transaction 內完成並推進版本（`migrations.go:202-219`）。

兩個約束會直接影響本文任何 schema 提案：

- **雙軌維護。** 新裝與升級走兩條路，`schemaSQL` 與增量 migration 必須同步，否則新裝使用者靜默缺欄位。`internal/db/schema_parity_test.go:14-25` 明確描述了這個陷阱並以測試把關。任何新欄位/新表都必須同時改 `schema.go` 與新增一個 migration。
- **CHECK 約束難以修改。** SQLite 無法直接 ALTER 一條 CHECK。專案的作法是 `extendTableCheckConstraint`（`migrations.go:1017-1048`）：直接以**字面字串比對**改寫 `sqlite_schema` 中的 DDL，再手動 bump `PRAGMA schema_version` 讓 SQLite 重新解析。註解說明了為何刻意用字面比對（繞過 SQLite parser，寬鬆比對會毀壞 DDL），以及為何不重建表（要拆掉再接回每個子表外鍵，對使用者資料風險更大）。這條路已經被走過三次（`migrations.go:96-105`、`107-151`、`1002-1006`），其中 v61 還必須同時修 CHECK 與 trigger 兩種寫法，因為不同時期建的資料庫用不同機制。
  → **推論：本文提出的任何列舉型欄位都不應加 CHECK 約束，改在 Go 層驗證。** 這類欄位的值域必然會增長。

## 3. 現行設計的具體問題

### 3.1 P1：「第 N 輪模型看到了什麼」不可回答，也不可重建

這是最實質的問題，而且比「沒有留存」更嚴重一層。

**不可回答**：2.3 表格中兩個階段的改寫只存在於記憶體，`recordAPIRequest` 不記請求內容。

**不可重建**：即使拿著不變的訊息列重跑一次組裝，也不保證得到同一個請求。裁剪與壓縮的觸發門檻比較的是 `effectiveLimit`，而它由 `effectiveContextTokenLimit(limit, ratio, calibrated)` 算出（`runner_context.go:330-331`、`internal/agent/context_estimate.go:121-130`）；`ratio` 來自 `contextCalibrationRatio`（`context_estimate.go:86-107`），讀的是 `r.contextCalibration` —— 一個**行程內記憶體 map、會被 LRU 淘汰、隨行程結束消失**（`context_estimate.go:42-66`）。

因此：同一個對話，重啟前後的 `effectiveLimit` 不同 → `desiredReduction` 不同 → 裁剪集合不同（5% 量化只是讓它在**同一行程內**穩定，`context_management.go:313-322`）→ 請求不同。「從訊息列重建」不是可行的替代方案。

實務後果：使用者回報「Agent 忘了它剛剛讀過的檔案」時，無法區分（a）該工具結果被裁剪成佔位字串、（b）boundary 前移把它切掉了、（c）模型收到了但忽略了。三者的修法完全不同，而目前的 durable 記錄無法排除任何一個。

### 3.2 P2：摘要壓縮是單一可覆寫欄位，且每次壓縮銷毀上一份摘要

`UpdateAgentContextSummary`（`projects_worklines_agents.go:817-829`）以 last-write-wins 覆寫三個欄位。而 `compactionSummary`（`context_summary.go:33-44`）產生新摘要時是把**舊摘要摺進新摘要**（`summarizeWithModel(ctx, agent.ContextSummary, candidates)`，`context_summary.go:38`）。兩者相加的結果是：寫入後上一份摘要文字不復存在。

具體丟失的資訊：

- 每次壓縮覆蓋了哪個區間（只剩「當時 boundary 之前的全部」，而 boundary 本身是單一可變欄位，壓縮 N+1 之後已經無法還原壓縮 N 的範圍）。
- 摘要是模型產生的還是 deterministic fallback。`compactionSummary` 回傳了這個布林值，但只以 ephemeral event 發佈（`context_summary.go:26-29`），沒有落地。
- 壓縮前後的 token 量、使用的摘要模型。
- 「撤銷上一次壓縮」沒有任何依據。

還有一個相關的失效模式：boundary 是一個**沒有外鍵**的 message id。`contextAgentForMessages`（`context_management.go:162-173`）在載入的訊息切片中找不到 boundary 列時，會靜默丟棄整份摘要（註解說明理由正確：否則會把摘要和完整原始逐字稿一起送出）。`DeleteConversationMessage` 把這件事變成 durable（`message_operations.go:112-117`）。這個設計本身是對的，但因為只有一個 slot，一次刪除就讓整段壓縮歷史無可退回。（`ForkAgentFromMessage` 也只在 boundary 列一起被複製時才帶過摘要，`message_operations.go:222-230`。）

### 3.3 P3：`superseded_at` 是尾段退場，不是區間替換

2.4 已述。它確實是 `surfaceOp: replace` 的一個**部分**版本，但缺的部分正好是壓縮需要的部分：

| | DSH `{op:'replace', start, end}` | Autoto `superseded_at` |
| --- | --- | --- |
| 區間起點 | 顯式 `start` | 隱含（目標列之後） |
| 區間終點 | 顯式 `end` | 無（到尾端） |
| 中段替換 | 支援 | 不支援 |
| 遮蔽者→被遮蔽者 | `sourceEventSeqs` 必須涵蓋全部（`packages/core/session/src/surface.ts:239-242`） | 無 |
| 原因 | 事件型別本身即原因 | 無（correction / rerun / rollback 三者不可分辨） |
| 驗證 | append 時驗證區間存在且涵蓋完整（`surface.ts:246-266`） | 無 |

實際痛點是最後兩列而非中段替換能力：UI 無法標示一列為何被隱藏，稽核無法回答「這 14 列是誰退場的」。

### 3.4 不是問題、不應誇大的部分

為免此文被讀成「Autoto 缺乏上下文管理」——並非如此，且以下這些不需要修：

- 原始素材沒有遺失。訊息列不被壓縮改寫，工具完整輸出另存於 `agent_tool_calls.output_json`（`schema.go:389`）。DSH 用 log-only 事件保住的稽核素材，Autoto 用不可變的列保住了。
- 逐字稿與模型視圖已經分離。DSH 需要 `isAppendSurfaceEvent`（`packages/core/session/src/surface.ts:51-55`）來避免替換節點抹掉使用者已經看過的對話；Autoto 天然分離——退場的列留在逐字稿裡，只是被模型視圖過濾掉。
- 摘要注入已經有 prompt-injection 防護與 trust 標記（`runner_context.go:652-671`）。
- 版本拒絕已經存在。`migrations.go:172-174` 拒絕開啟比本 build 新的資料庫。

## 4. DSH 的做法與它買到的性質

### 4.1 機制

- Session 是 append-only 的 `SessionEvent` log，模型訊息由 `deriveMessages()` 折疊 surface 推導而來（`packages/core/session/src/index.ts:726-747`；`docs/subsystems/session.md:5`）。
- 只有三個事件型別可上 surface（`user/message`、`assistant/message`、`tool/result`），且**必須**帶 `surfaceOp`；非 surface 事件在編譯期就不允許攜帶（`packages/core/session/src/types.ts:231-243`；`docs/subsystems/session.md:251-290`）。
- 壓縮的提交方式：append 一個 `compaction/summary` 記錄（含 `shadowedRange`、`shadowedSeqs`、`shadowedTokenCount`、provider、model、usage），再 append 一個帶 `surfaceOp: {op:'replace', start, end}` 與完整 `sourceEventSeqs` 的 `user/message`（`packages/compaction/compaction-basic/src/region.ts:447-465`）。沒有任何一列被改寫。
- fold 在 append 時驗證：區間兩端必須存在於當前 surface、`sourceEventSeqs` 必須涵蓋每個被遮蔽節點（`packages/core/session/src/surface.ts:210-266`、`362-379`）。
- invariant 寫進 `AGENTS.md:107`：**「Model-visible ⟺ logged: anything that reaches a model request must be reconstructable from the session log.」**
- 版本策略：`SESSION_FORMAT_VERSION`（`packages/core/session/src/types.ts:33-56`）明文「不提供 migration，不相容即拒絕」，且判準是**writer 發出了什麼**而非 reader 能接受什麼；詞彙增長由 per-event `ignorable: true` 承接（`types.ts:220-230`），不需 bump。
- 值得注意：DSH 自己的 SQLite backend 就是「一列一事件」，欄位為 `(session_id, seq, type, time, data, source_event_seqs, surface_op)`（`docs/subsystems/persistence.md:236`）。

### 4.2 拆開來看，它買到六個可獨立取用的性質

1. 壓縮可稽核（替換了什麼、由什麼替換、provenance 完整）。
2. 壓縮非破壞性（壓縮前視圖仍可推導）。
3. 「模型看到了什麼」可回答。
4. 任意點 fork / replay。
5. 中段區間替換的表達力。
6. 遇到不認識的格式**拒絕**而非猜測。

### 4.3 為什麼不該照搬：Autoto 的改寫大多是「單輪有效」的

這是本評估最重要的一點。DSH 的 replace 事件之所以適合 append，是因為它記錄的是**一次持久的狀態變更**——壓縮發生一次、之後每輪都沿用。

Autoto 階段 2 與階段 4 的改寫不是這種東西。它們依賴當輪的估算值與行程內校準比例，且**設計上每輪都可能不同**（5% 量化的存在恰好證明了這一點：它是為了抑制每輪漂移而加的，`context_management.go:313-322`）。把它們變成 append-only 的 surface op，等於為只在單輪有效的決策在多數輪都寫入資料列，並讓 surface fold 的結果依賴一個每輪重算的量。這是機制與語意的錯配。

反過來看，性質 2 對 Autoto 大部分已經免費（3.4 第一點），性質 4 沒有產品需求，性質 5 沒有任何生產者要求（3.3），性質 6 大致已有（3.4 第四點）。

**剩下真正缺的是性質 3，其次是性質 1 之中僅限於階段 3 的那部分。** 這決定了下面的增量設計：用「記錄結果」買性質 3，只對「本來就是持久狀態變更」的階段 3 用「記錄操作」。

## 5. 增量方案（依建議順序）

### 5.1 增量 A：持久化每次模型請求的組成清單（建議做）

**要解決**：P1。

**改什麼**：新表記錄每次模型呼叫送出的內容。關鍵設計選擇是**存清單（manifest）而非請求 body**。

存完整 body 的問題是體積：請求包含整段對話，每輪都存一份，長對話下總量對輪數呈平方成長。清單則是「有序的 (message_id, 套用的變換) 列表 + 無列可指的合成訊息（摘要文字、control 訊息）逐字內容 + 渲染後 body 的 digest」。因為訊息列不可變（2.1），清單足以**精確重建**送出的請求，而 digest 讓重建結果可被驗證。

擬議 schema（名稱待定；不加 CHECK 約束，見 2.5）：

```
agent_request_snapshots
  id                  TEXT PRIMARY KEY
  agent_id            TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE
  run_id              TEXT REFERENCES runs(id) ON DELETE SET NULL
  turn_index          INTEGER NOT NULL
  continuation_index  INTEGER NOT NULL
  provider            TEXT
  model               TEXT
  estimated_tokens    INTEGER      -- managedContextForTurn 已回傳
  effective_limit     INTEGER      -- 連同 calibration ratio 一起存，P1 的重建前提
  calibration_ratio   REAL
  entries_json        TEXT NOT NULL -- 有序清單
  body_digest         TEXT NOT NULL -- SHA-256 of 渲染後請求
  created_at          TEXT NOT NULL
```

`ON DELETE CASCADE` 是刻意的：`api_requests` 用 `SET NULL`（`schema.go:422-423`），對 usage 統計正確，但對含對話內容的記錄不行——訊息列被刪後其內容不該繼續存活於快照中。

**檔案**：`internal/db/schema.go`（新表，同時必須加入 baseline，見 2.5 雙軌維護）、`internal/db/migrations.go`（新增 v66）、新的 store 檔案、`internal/agent/runner_context.go`（`managedContextForTurn` 需一併回傳它實際套用的變換）、`internal/agent/continuation.go:501-508`（穿參）、`runModelTurn`（`internal/agent/runner_model.go:50`）派送前後的寫入點、以及讀取用的 server handler。

寫入時機建議在派送**之前**：若請求本身讓 provider 回錯，事後最需要那份快照的正是這種情況，而 `recordAPIRequest` 是回應之後才跑的（`runner_model.go:665-703`），錯誤路徑上不保證留下對應列。

**成本**：中等。schema + migration + parity 約 100 行；`managedContextForTurn` 目前只回傳 `([]providers.Message, db.Agent, int, error)`，要讓它同時回報「哪些 block 被換成佔位字串」需要在四個階段各累積一筆紀錄，這是最主要的實作工作；加測試估 400–700 行。

**買到**：性質 3 完整。以及性質 1 的診斷價值——送出的清單直接顯示某個 tool_result 是否被換成佔位字串，因此 3.1 的 (a)/(b)/(c) 三種情況變得可區分。同時因為存下了 `effective_limit` 與 `calibration_ratio`，跨行程的不可重現性也被記錄下來而非猜測。

**不買到**：不讓壓縮變成非破壞性（P2 照舊）；不提供區間替換表達力（P3 照舊）；不改變壓縮的決策方式；不提供 fork/replay。另外要誠實說明其邊界：清單只在被引用的訊息列仍存在時能重建；列被刪除後重建會失敗——但因為存了 digest，**失敗是可偵測的，不會靜默給出錯誤答案**。這正是 DSH「拒絕而非猜測」（`docs/subsystems/persistence.md:94`）在此處的低成本對應。

**逃生門**：若清單在某些除錯場景仍不足，可加一個 config 開關存完整 body，預設關閉。不建議預設開啟，理由即上述體積問題。

### 5.2 增量 B：把摘要壓縮改成 append-only 記錄（條件性採用）

**要解決**：P2。

**改什麼**：新增一張只增不改的壓縮記錄表；`agents` 的三個欄位保持原樣作為「最新一筆」的快取，因此**讀取路徑完全不動**。

```
agent_context_compactions
  id                       TEXT PRIMARY KEY
  agent_id                 TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE
  run_id                   TEXT REFERENCES runs(id) ON DELETE SET NULL
  previous_compaction_id   TEXT REFERENCES agent_context_compactions(id) ON DELETE SET NULL
  boundary_message_id      TEXT   -- 不設外鍵，與現行 prune_boundary_message_id 一致
  shadowed_from_message_id TEXT
  shadowed_message_count   INTEGER NOT NULL
  summary_text             TEXT NOT NULL
  summary_source           TEXT NOT NULL  -- 'model' | 'deterministic'，Go 層驗證，不加 CHECK
  summary_model            TEXT
  tokens_before            INTEGER
  tokens_after             INTEGER
  created_at               TEXT NOT NULL
```

**檔案**：`schema.go` + `migrations.go`（v67）+ store 方法 + `runner_context.go:352` 與 `:383` 兩個壓縮寫入點 + 手動壓縮路徑；`compactionSummary`（`context_summary.go:33-44`）需要把目前只回傳給 ephemeral event 的 `bool` 一路帶到寫入點。

**成本**：小到中等，約 250–400 行含測試。

**買到**：性質 1 完整（壓縮鏈可稽核：每一份摘要、覆蓋範圍、來源模型或 fallback 都留存）；「撤銷上一次壓縮」變得有依據；3.2 的 boundary 刪除失效模式有了退路（前一筆記錄仍在）。

**不買到**：對階段 2 與階段 4 毫無幫助；本身**不會**讓壓縮在模型視圖上可逆（要另寫回捲路徑）；不改變模型看到的內容。

**為何列為條件性**：P2 丟失的是稽核與可逆性，不是使用者資料——原始訊息列都還在。如果目前沒有人在問「這份摘要覆蓋了什麼」或「把上次壓縮退回去」，這筆投資可以等到有人問再做。它與增量 A 完全獨立，晚做不會產生返工。

### 5.3 增量 C：給 `superseded_at` 加上原因與指向（建議做）

**要解決**：P3 中真正會痛的兩列（原因、遮蔽指向）。

**改什麼**：`agent_messages` 增加兩個 nullable 欄位：

```
superseded_reason        TEXT   -- 'correction' | 'rerun' | 'rollback'，Go 層驗證，不加 CHECK
superseded_by_message_id TEXT   -- correction 指向新列；rerun/rollback 指向保留的目標列
```

**檔案**：`schema.go`（表定義 + baseline）、`migrations.go`（v68，兩個 `ensureColumn`）、三個寫入點（`messages_attachments.go:303`、`messages_attachments.go:357`、`message_operations.go:35`）、`types.go:160-182`、以及選擇性的 UI 標示。

**成本**：最小，約 150 行含測試。純新增 nullable 欄位，既有列保持 NULL，讀取路徑的 `superseded_at IS NULL` 過濾與 partial index（`schema.go:342-345`）完全不受影響。

**買到**：「這列為何被隱藏」可回答；UI 可分辨修正／重跑／回捲；稽核可從遮蔽者反查。

**不買到**：不提供中段區間替換（語意仍是後綴）；不與壓縮機制統一；不改變模型看到的內容。

## 6. 建議停在哪裡

**做 A 與 C。B 等到有人真的要求壓縮稽核或壓縮回捲時再做。A/B/C 之後停止。**

理由：

- **A 是本評估的重點。** 它是唯一直接關閉 `Model-visible ⟺ logged` 缺口的增量，也是唯一讓 3.1 那類使用者回報變成可診斷的增量。其他所有增量都只是讓「已經 durable 的東西」更好稽核。
- **C 便宜到不需要論證。** 兩個 nullable 欄位、三個寫入點、零讀取路徑變更，換到一個目前完全無法回答的問題。
- **B 之所以是條件性**：它修的是稽核能力，而原始素材並未遺失。沒有需求時，它是替一個沒人問的問題付表與 migration 的成本。
- **停在這裡的原因**：再往後的每一步（通用區間替換、獨立 format version、event log 改寫，見第 7 節）都要求動到讀取路徑或核心持久化模型，而換到的性質在 Autoto 目前沒有需求者。DSH 的 surface projection 對 DSH 是對的，因為它的 log 是唯一真相；Autoto 的訊息列本身就是不可變的真相，因此只需要補上「送出了什麼」這一份憑證，不需要換掉真相的載體。

一條實作紀律建議寫進 `CONTRIBUTING.md`（不在本提案範圍內動它）：**任何新的「模型可見但無 durable 記錄」的請求組裝步驟，都應同時寫入增量 A 的清單。** 這是把 DSH 的 invariant 降級成 Autoto 可負擔的版本——不要求可從 log 完整重建，但要求可被記錄與比對。

## 7. 考慮過但拒絕的方案

### 7.1 在 `agent_messages` 加通用 `surface_op` 區間替換

**拒絕。** Autoto 只有兩種替換生產者：supersede 家族（永遠是後綴，由明確使用者意圖驅動）與摘要壓縮（永遠是到 boundary 的前綴）。兩者都不需要中段區間。而通用 `{start,end}` 必須建立在 `(created_at, id)` 排序之上（沒有 sequence 欄位，`messages_attachments.go:298-300`），並要求每條讀取路徑都做有序折疊——把目前一個由 partial index 服務的 `superseded_at IS NULL` 過濾（`schema.go:342-345`）換成 fold。為沒有需求者的表達力付讀取路徑與複雜度的代價，不划算。增量 C 用兩個 nullable 欄位取走了同一個需求裡真正會痛的部分。

### 7.2 獨立的 conversation format version 常數與「不靜默 migrate」宣告

**拒絕（只留一條註解）。** 這件事大部分已經存在：`CurrentDBVersion`（`migrations.go:13`）加上 `migrations.go:172-174` 的拒絕，已經是「比我新的資料庫我不開」的立場，訊息也明確。

與 DSH 的差別是 DSH 的版本管的是**日誌語意**、Autoto 的管的是 **SQL schema**。但在 Autoto，對話形狀的語意變更幾乎必然伴隨欄位變更，所以 schema 版本已經把它蓋住了。再加一個常數，只是多一個要 bump 的東西與一種新的不一致方式。

值得借用的是**判準而非常數**：在 `CurrentDBVersion` 旁補一條註解，說明「若某次變更改變了**既有列的解讀方式**而未改動任何欄位，仍須 bump 版本」——這正是現行機制唯一抓不到的情況（DSH 的對應論述在 `packages/core/session/src/types.ts:40-49`）。這是一條註解，不是程式碼。

### 7.3 完整 event-sourced 改寫

**拒絕。** 成本是整個 `internal/db` 訊息層加上每一條讀取路徑（`ListMessagesPage`、live snapshot、navigation conversations、attachments、generated images、tool calls），外加 65 個版本的 migration 歷史。換到的主要是性質 4（任意點 fork/replay），而 Autoto 已有 `ForkConversationFromMessage`（`internal/db/message_operations.go:130`，以複製列實作，摘要沿用邏輯在 `message_operations.go:222-230`）——那是這個能力的廉價版本，且能用。

### 7.4 直接把請求 body 塞進 `api_requests.raw_dump_json`

**拒絕。** 表面上最便宜（欄位已存在、已被 usage API 排除，`usage_history_test.go:102-105`），但三點不合：

1. `api_requests` 的 `agent_id` / `run_id` 是 `ON DELETE SET NULL`（`schema.go:422-423`）。訊息列被刪除後，含完整對話內容的 dump 會繼續存活——這是保留期與隱私問題，不只是整潔問題。
2. 該列由 `recordAPIRequest` 在呼叫**之後**寫入（`runner_model.go:665-703`），語意是回應側的 usage 記帳；把請求側內容混進去會讓一張表有兩個生命週期。
3. 體積無上界，且如 5.1 所述總量對輪數呈平方成長。

### 7.5 把階段 2／階段 4 的裁剪也做成 durable 的 surface op

**拒絕。** 理由見 4.3：這些改寫依設計每輪都可能不同，且依賴行程內的校準狀態。增量 A 用「記錄結果」而非「記錄操作」來覆蓋它們，是正確的機制配對。

## 8. 不確定、需要維護者裁決的點

以下幾點本文無法從程式碼單獨判定，刻意不猜：

1. **增量 A 的保留期。** 每輪一列快照需要一個清理策略（依 run 保留、依時間、或依對話）。`internal/db/archive_deletion.go` 存在但本文未細讀其策略是否可直接沿用。
2. **`entries_json` 的確切結構。** 「有序 (message_id, 變換) 列表」的具體編碼，以及 control／sidecar 訊息（`fitTurnSystemControls`，`runner_context.go:400-406`）與 prompt prefix（`runner_context.go:312-321`）該如何表示，需要一輪設計。本文未提出具體 JSON 形狀。
3. **`managedContextForTurn` 的簽章變更範圍。** 它目前回傳四個值並在多處測試被直接呼叫（例如 `internal/agent/runner_context_test.go:539`、`internal/agent/context_estimate_test.go:151`）。改成回傳一個結構體較乾淨，但會擴大 diff；本文沒有替維護者決定。
4. **是否值得為增量 A 增加一個 UI 檢視面。** 「這一輪送出了什麼」對開發者顯然有用，對終端使用者是否要露出，是產品判斷。
5. **`api_requests.raw_dump_json` 的處置。** 它目前是一個生產程式碼永不填值的欄位（2.3）。若增量 A 落地，這個欄位是否該移除或明確標註為 gateway 專用，本文未主張。
