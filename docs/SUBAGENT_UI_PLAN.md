# Subagent 前端收納與按需詳情計劃

狀態：計劃稿  
日期：2026-07-19  
適用專案：Autoto

## 1. 摘要

Autoto 已經具備完整的 Subagent 核心鏈路，包括 `Agent` 工具、持久化後台任務、子 Agent / 子 Run、角色與權限約束、按角色選擇模型、取消與等待、`ContextAsk` 查詢，以及前端後台任務面板。

本次計劃不重新實現 Subagent，而是最佳化父 Agent 會話中的展示方式：

1. 父會話只展示緊湊、可審計的子任務摘要。
2. 不在父會話中自動載入或巢狀展示子 Agent 的實際工具呼叫。
3. 子任務詳情、子 Agent 和子 Run 通過顯式點選按需開啟。
4. 已完成任務預設收起；執行中、等待審批和失敗狀態保持足夠醒目。
5. 明確區分“`Agent` 工具已成功派發”和“子 Agent 任務已執行完成”這兩個不同狀態。

結論：採用“摘要優先、漸進展開、顯式檢視子 Run”的方案，保留現有後端安全邊界和審計能力，同時減少父對話噪聲、瀏覽器渲染負擔和不必要的資料請求。

## 2. 背景與現狀

### 2.1 已有後端能力

當前程式碼已經包含以下能力：

- `internal/tools/agent_task.go`
  - 註冊名為 `Agent` 的工具。
  - 建立持久化後台 Agent 任務並立即返回任務控制程式碼。
  - 儲存 `ParentRunID` 和 `ParentToolUseID`，可將父工具呼叫與後台任務關聯。
- `internal/background/agent.go`
  - 建立獨立子 Agent 和子 Run。
  - 繼承或收窄父 Agent 權限，不允許擴大權限。
  - 將子任務狀態儲存為持久化後台任務結果。
- `internal/agentrole/role.go`
  - 支持 `general`、`executor`、`explorer`、`reviewer`、`tester`、`plan` 和 `search` 等角色。
  - 對只讀角色強制使用只讀權限。
- `internal/agent/loop.go`
  - 支援 Subagent 專用模型和模型池。
  - 未單獨配置時可回退到父 Agent 模型或預設模型。
- `internal/tools/context_ask.go`
  - 父 Agent 可以基於持久化子 Run 查詢結果。
  - 不需要把子 Agent 的原始工具呼叫全部塞回父 Agent 上下文。
- `internal/db/background_tasks.go`
  - 後台任務包含 `parentToolUseId`、`childAgentId`、`childRunId`、狀態、時間、公開摘要和錯誤資訊。

現有安全約束應保持不變：

- 只有根 Agent 可以建立第一層 Subagent。
- Subagent 不能繼續建立下一層 Subagent。
- 子 Agent 權限不得超過父 Agent。
- 未知角色必須拒絕，不能回退到更寬鬆的通用角色。
- 子 Agent 的成功狀態不能替代主 Agent 對關鍵結果的獨立驗證。

### 2.2 已有前端能力

當前前端已經包含：

- `internal/server/static/modules/chat-rendering.mjs`
  - 通用工具活動卡片和工具活動分組。
  - 每張工具卡的輸入、輸出和 Diff 已經使用 `<details>` 收納。
  - 工具活動分組當前預設帶有 `open`，因此整組預設展開。
- `internal/server/static/modules/background-tasks.mjs`
  - 顯示後台任務列表、狀態、時間、錯誤和分頁輸出。
  - 支援取消、等待、開啟子 Agent 和開啟子 Run。
  - 資料按當前父 Agent 隔離。
- `internal/server/static/modules/app-main.mjs`
  - 已經具備從後台任務跳轉到子 Agent 或子 Run 的導航入口。

### 2.3 當前問題

1. `Agent` 工具仍使用通用工具卡片，沒有專屬的子任務摘要佈局。
2. 通用工具卡更強調輸入 JSON 和工具輸出，不適合表達“任務委派”語義。
3. `Agent` 工具呼叫完成只表示後台任務已成功建立，不代表子 Agent 已完成；當前通用狀態容易讓使用者誤解。
4. 工具活動分組預設展開，長會話中容易佔用大量垂直空間。
5. 如果未來直接把子 Agent 的工具呼叫嵌入父會話，會造成重複資料、額外請求和視覺噪聲。
6. 子任務 Prompt 可能很長，不應直接作為緊湊卡片標題，也不應在預設展開狀態中暴露全部內容。

## 3. 目標與非目標

### 3.1 目標

1. 為 `Agent` 工具提供專屬的緊湊任務卡片。
2. 使用 `parentToolUseId` 將父工具呼叫與後台任務狀態可靠關聯。
3. 在父會話展示角色、描述、模型、狀態、耗時、驗收條件數量和導航入口。
4. 已完成的子任務預設收起，活動或異常狀態保持醒目。
5. 父會話預設不請求子 Run 的工具呼叫列表。
6. 使用者顯式開啟子 Agent 或子 Run 後，再進入現有獨立詳情頁面檢視完整記錄。
7. 保留現有通用工具卡作為關聯失敗時的安全降級方案。
8. 支援簡體中文、繁體中文和英文文案。
9. 保持桌面端、窄視窗和移動端可用，並滿足鍵盤操作和基本可訪問性要求。

### 3.2 非目標

1. 不重寫 `Agent` 工具或後台任務執行器。
2. 不改變 Subagent 角色、權限、模型選擇和禁止巢狀規則。
3. 不在父會話中複製子 Agent 的完整訊息、推理內容或工具呼叫記錄。
4. 不自動生成新的 AI 摘要請求，以免增加模型成本和延遲。
5. 不把子任務工具呼叫數量作為第一階段必需欄位；現有資料沒有可靠提供時不猜測。
6. 不新增多層 Agent 樹或無限巢狀導航。
7. 不用前端隱藏替代後端權限、訪問控制和資料隔離。
8. 第一階段不修改全部普通工具卡的預設展開策略，只處理 Subagent 相關展示和必要的分組行為。

## 4. 設計原則

### 4.1 摘要優先

父會話只顯示完成判斷所需的最小資訊：

- 子任務描述
- Subagent 角色
- 模型
- 後台任務狀態
- 開始時間或耗時
- 驗收條件數量
- 子 Agent / 子 Run 入口
- 錯誤摘要（僅異常時）

不得在緊湊狀態直接展示完整 Prompt、完整工具輸入、完整工具輸出或子 Run 工具呼叫列表。

### 4.2 漸進披露

資訊分為三層：

1. **父會話緊湊卡片**：預設可見，資訊最少。
2. **後台任務詳情面板**：使用者點選後載入現有任務輸出和任務操作。
3. **子 Agent / 子 Run 頁面**：使用者明確進入後檢視完整訊息與工具活動。

### 4.3 狀態必須語義準確

需要同時表達兩個狀態：

- 父 `Agent` 工具狀態：是否成功建立後台任務。
- 子後台任務狀態：排隊、執行、等待審批、成功、失敗、取消或中斷。

父工具呼叫的 `completed` 不應直接翻譯成“子任務已完成”。建議在專屬卡片中將其表達為“已派發”，並以後台任務狀態作為主要狀態。

### 4.4 預設不載入子工具呼叫

父會話渲染過程中不得自動呼叫類似以下子 Run 工具詳情介面：

```text
/api/agents/{childAgentId}/runs/{childRunId}/tool-calls
```

只有使用者顯式開啟子 Run 後，子 Agent 頁面才按現有邏輯載入自己的工具呼叫。

### 4.5 保持可審計與可降級

- 父工具呼叫記錄、後台任務、子 Agent 和子 Run 仍然持久化。
- 關聯失敗時顯示通用 `Agent` 工具卡，不隱藏真實錯誤。
- 資料不完整時使用“狀態未知”或“等待任務資訊”，不能偽造完成狀態。

## 5. 建議互動

### 5.1 已完成狀態

```text
▸ Explorer 子任務 · 檢查登入流程
  已完成 · 32 秒 · codex:gpt-5.4-mini

  [檢視任務] [開啟子 Agent] [開啟 Run]
```

行為：

- 預設收起。
- 不顯示實際 `Read`、`Grep`、`Bash` 等子工具呼叫。
- 點選“檢視任務”開啟現有後台任務面板。
- 點選“開啟子 Agent”進入子 Agent 會話。
- 點選“開啟 Run”進入子 Run 回顧。

### 5.2 執行中狀態

```text
▾ Reviewer 子任務 · 審查權限邊界
  執行中 · codex:gpt-5.5

  子 Agent 正在獨立執行任務。
  [檢視任務] [取消]
```

行為：

- 活動任務可預設展開簡短狀態。
- 只顯示任務級進度，不流式嵌入子 Agent 的實際工具呼叫。
- 取消操作繼續使用現有後台任務取消介面。

### 5.3 等待審批狀態

```text
▾ Executor 子任務 · 執行修復驗證
  等待審批

  子 Agent 中存在等待處理的受限工具呼叫。
  [開啟子 Agent] [檢視任務]
```

行為：

- 自動展開並使用警告色。
- 不在父卡片複製審批命令內容。
- 使用者進入對應子 Agent 後，通過現有審批介面處理。

### 5.4 失敗或中斷狀態

```text
▾ Tester 子任務 · 執行迴歸測試
  失敗 · 18 秒

  測試任務未完成：子 Run 返回失敗狀態。
  [檢視錯誤] [開啟 Run]
```

行為：

- 自動展開錯誤摘要。
- 錯誤文本必須經過現有長度限制和轉義。
- 不將失敗自動視為主任務失敗，由主 Agent決定後續處理。

## 6. 資料關聯方案

### 6.1 主關聯鍵

使用現有欄位進行關聯：

```text
ToolCall.toolUseId
        =
BackgroundTask.parentToolUseId
```

後台任務進一步提供：

```text
BackgroundTask.id
BackgroundTask.status
BackgroundTask.publicSummary
BackgroundTask.childAgentId
BackgroundTask.childRunId
BackgroundTask.startedAt
BackgroundTask.completedAt
BackgroundTask.errorCode
BackgroundTask.errorMessage
```

### 6.2 前端派生狀態

建議在前端維護：

```text
backgroundTaskByParentToolUseId: Map<toolUseId, task>
```

該索引由以下資料來源更新：

1. 當前 Agent 的 live snapshot。
2. `GET /api/agents/{id}/background-tasks`。
3. `task.created`、`task.status` 和 `task.completed` WebSocket 事件。
4. 單個後台任務詳情重新整理結果。

Agent 切換時必須清空索引，並繼續使用現有 generation / stale request 防護，避免舊 Agent 請求回填當前頁面。

### 6.3 公開摘要欄位

第一階段僅使用現有公開摘要：

```json
{
  "description": "檢查登入流程",
  "subagentType": "explorer",
  "model": "codex:gpt-5.4-mini",
  "acceptanceCount": 2
}
```

緊湊卡片不得從私有 Payload 中提取或顯示完整 Prompt。工具活動詳情中現有輸入 JSON 仍可作為審計降級入口，但必須預設收起。

### 6.4 服務端變更門檻

優先複用現有 API 和事件。實施前先驗證 `parentToolUseId` 是否在以下路徑完整透傳：

- live snapshot
- 後台任務列表 API
- `task.*` WebSocket 事件
- 單任務詳情 API

只有發現某條路徑缺少該欄位時，才補充經過清洗的關聯欄位。預計不需要資料庫遷移，因為欄位已經存在並持久化。

## 7. 前端元件方案

### 7.1 Agent 工具識別

在 `chat-rendering.mjs` 中增加明確判斷：

```text
isAgentToolActivity(tool)
```

僅對規範化工具名精確識別 `Agent`，避免把普通名稱中含有 `agent` 的動態工具誤判為 Subagent。

### 7.2 專屬規範化結果

增加一個只用於展示的派生物件，建議包含：

```text
role
description
model
reasoningEffort
acceptanceCount
taskId
taskStatus
childAgentId
childRunId
startedAt
completedAt
durationMs
errorMessage
```

所有文本繼續經過現有 `escapeHtml`、`escapeAttr`、長度限制和狀態白名單處理。

### 7.3 專屬渲染器

建議增加：

```text
renderAgentTaskActivityCardHTML(tool, task, options)
```

通用入口按類型分流：

```text
Agent 工具 + 已有關聯任務
  -> 專屬子任務卡片

Agent 工具 + 暫無關聯任務
  -> “已派發 / 等待任務資訊”卡片

其他工具或關聯解析失敗
  -> 現有 renderToolActivityCardHTML
```

### 7.4 展開策略

建議按後台任務狀態決定預設展開：

| 狀態 | 預設行為 |
| --- | --- |
| `queued` / `running` | 展開簡短狀態 |
| `waiting_approval` | 展開並突出提醒 |
| `succeeded` / `completed` | 收起 |
| `failed` / `error` | 展開錯誤摘要 |
| `canceled` / `cancelled` / `interrupted` | 展開終止說明 |
| 未知或關聯未完成 | 收起並顯示等待資訊 |

不建議第一階段改變所有普通工具活動組的預設展開行為。可以僅讓包含 Subagent 專屬卡片的分組使用狀態驅動策略，降低迴歸範圍。

### 7.5 導航與任務面板

複用現有能力：

- “檢視任務”：選中對應後台任務並開啟後台任務面板。
- “開啟子 Agent”：呼叫現有 `onNavigateAgent(childAgentId)`。
- “開啟 Run”：呼叫現有 `onNavigateRun(childAgentId, childRunId)`。
- “取消”：呼叫現有後台任務取消介面。

如果現有控制器缺少“按任務 ID 開啟面板”的公開方法，應增加最小介面，而不是複製後台任務載入邏輯。

## 8. 實施階段

### 階段 0：資料契約驗證

1. 核對後台任務列表、snapshot 和 `task.*` 事件是否均包含 `parentToolUseId`。
2. 核對父工具呼叫、後台任務和子 Run 的狀態更新時間順序。
3. 確認 Agent 切換和 Run 切換時舊請求不會汙染新頁面。
4. 記錄缺失欄位；只有確有缺失才調整服務端投影。

交付物：明確的資料欄位表和不需要資料庫遷移的證據。

### 階段 1：專屬緊湊卡片

1. 增加 `Agent` 工具精確識別。
2. 從工具輸入和公開任務摘要派生角色、描述和模型。
3. 增加專屬圖示、標題、狀態徽章和耗時。
4. 將完整輸入 / 輸出繼續放在預設收起的審計詳情中。
5. 保留通用工具卡降級路徑。

交付物：靜態和歷史 Run 中的 `Agent` 工具不再顯示為普通工具卡。

### 階段 2：父工具與後台任務即時關聯

1. 在後台任務控制器中建立 `parentToolUseId` 索引。
2. 將 snapshot、列表 API 和即時事件統一寫入索引。
3. 子任務狀態變化時只重新整理對應卡片或工具活動區域。
4. 明確區分“已派發”和“子任務完成”。
5. 對亂序、重複和過期事件保持冪等。

交付物：排隊、執行、成功、失敗、取消和等待審批狀態能夠即時更新。

### 階段 3：按需詳情與導航

1. 接通“檢視任務”。
2. 複用“開啟子 Agent”和“開啟 Run”。
3. 驗證父會話初次渲染不請求子 Run 工具呼叫介面。
4. 使用者點選進入子 Run 後，繼續使用現有工具活動載入邏輯。
5. 子 Agent 或子 Run 尚未建立時停用對應按鈕並顯示明確狀態。

交付物：父會話保持緊湊，完整審計詳情仍然可達。

### 階段 4：樣式、國際化與移動端

1. 增加專屬 Subagent 卡片樣式。
2. 完成簡體中文、繁體中文和英文文案。
3. 驗證長描述、長模型名、窄視窗和移動端換行。
4. 驗證鍵盤聚焦、`<details>`、按鈕標籤和狀態可讀性。
5. 避免依賴顏色作為唯一狀態提示。

交付物：桌面端和移動端均可穩定使用。

### 階段 5：測試與迴歸

1. 增加前端單元測試。
2. 增加必要的服務端投影測試（僅當階段 0 發現欄位缺失）。
3. 執行目標模組測試。
4. 執行完整 `make check`。
5. 手動驗證父 Agent 建立 Subagent 的真實工作流。

交付物：自動化測試和手動驗收記錄。

## 9. 預計修改檔案

主要前端文件：

- `internal/server/static/modules/chat-rendering.mjs`
- `internal/server/static/modules/chat-rendering.test.mjs`
- `internal/server/static/modules/background-tasks.mjs`
- `internal/server/static/modules/background-tasks.test.mjs`
- `internal/server/static/modules/app-main.mjs`
- `internal/server/static/modules/messages-chat-rendering-extra.mjs`
- `internal/server/static/modules/messages-background-tasks.mjs`
- `internal/server/static/styles.css`

僅在資料欄位缺失時可能修改：

- `internal/server/background_tasks.go`
- `internal/db/live_snapshot.go`
- 對應 Go 測試檔案

預計不需要修改：

- Subagent 資料庫表結構
- `Agent` 工具執行協議
- 角色權限合約
- Provider 接口
- 子 Agent 工具呼叫持久化結構

## 10. 測試計劃

### 10.1 前端單元測試

至少覆蓋：

1. 只有規範工具名 `Agent` 會進入專屬渲染器。
2. `parentToolUseId` 能正確關聯後台任務。
3. 完成狀態預設收起。
4. 執行、審批和失敗狀態按規則展開。
5. 父工具 `completed` 不會誤顯示成子任務完成。
6. 描述、模型名和錯誤內容正確轉義。
7. 缺少 `childAgentId` 或 `childRunId` 時按鈕正確停用。
8. 關聯失敗時回退到通用工具卡。
9. Agent 切換後舊請求和舊事件不能回填。
10. 父會話渲染不會請求子 Run 的工具呼叫介面。
11. 點選“檢視任務”只加載對應後台任務詳情。
12. 點選“開啟子 Agent / Run”呼叫正確導航引數。

### 10.2 服務端測試

如果需要補充欄位，至少覆蓋：

1. 後台任務列表返回 `parentToolUseId`。
2. live snapshot 返回相同關聯值。
3. `task.created`、`task.status` 和 `task.completed` 不泄露私有 Payload 或完整 Prompt。
4. 使用者只能讀取有權訪問的父 Agent 後台任務。
5. 子 Agent 和父 Agent 的工具呼叫仍按 Agent ID 隔離。

### 10.3 手動驗收流程

```text
根 Agent 發起 Agent 工具
  -> 父會話出現緊湊子任務卡片
  -> 狀態從已派發 / 排隊更新到執行中
  -> 父會話沒有出現子 Agent 的 Read/Grep/Bash 明細
  -> 點選檢視任務可開啟後台任務面板
  -> 點選開啟子 Agent 可進入子會話
  -> 點選開啟 Run 後才載入子 Run 工具呼叫
  -> 子任務結束後父卡片更新為完成並預設收起
```

還需分別驗證：

- 等待審批
- 失敗
- 取消
- 中斷
- 子 Agent / Run 建立前的短暫狀態
- 頁面重新整理後的狀態恢復
- WebSocket 重連和 snapshot 恢復
- 移動端窄屏

## 11. 驗收標準

以下條件全部滿足才視為完成：

1. 父會話能夠顯示 Subagent 角色、描述、模型和真實後台任務狀態。
2. `Agent` 工具“已派發”和子任務“已完成”不會混淆。
3. 已完成子任務預設收起。
4. 等待審批和失敗狀態不會被隱藏。
5. 父會話預設不會載入子 Run 的實際工具呼叫內容。
6. 使用者可以顯式開啟後台任務、子 Agent 和子 Run。
7. 缺失或亂序資料不會導致錯誤的成功狀態。
8. 子任務 Prompt 不會出現在預設緊湊摘要中。
9. 所有動態文本均正確轉義並受長度限制。
10. 簡體中文、繁體中文和英文文案齊全。
11. 桌面端和移動端樣式可用。
12. 不改變既有權限、審批和 Subagent 安全邊界。
13. 目標測試和完整檢查通過。

## 12. 風險與緩解措施

### 12.1 工具狀態與任務狀態混淆

風險：父 `Agent` 工具很快完成，但子任務仍在執行。

緩解：使用兩個明確語義；卡片主狀態來自後台任務，工具狀態僅表示“派發成功或失敗”。

### 12.2 即時事件亂序

風險：`task.completed`、列表重新整理和 snapshot 到達順序不同，舊狀態覆蓋新狀態。

緩解：沿用任務 `revision`、Agent generation 和現有 stale request 防護；只接受更新版本。

### 12.3 Prompt 或私有資料洩露

風險：直接使用 Agent 工具輸入作為卡片摘要會顯示完整 Prompt。

緩解：緊湊卡片只使用公開摘要中的 `description`、角色、模型和數量欄位；完整 Prompt 不預設展開，也不加入任務事件。

### 12.4 為展示而增加模型呼叫

風險：自動生成子任務摘要會增加成本、延遲和失敗點。

緩解：第一階段不新增摘要模型呼叫；使用現有結構化狀態和使用者提供的描述。

### 12.5 前端狀態重複

風險：聊天渲染控制器和後台任務控制器各自儲存一份不一致的資料。

緩解：後台任務控制器作為任務狀態事實源，對外提供只讀查詢或訂閱介面；聊天渲染只消費規範化任務狀態。

### 12.6 完成任務全部摺疊導致問題不醒目

風險：失敗或審批任務與普通完成任務一起被收起。

緩解：僅成功終態預設收起；失敗、審批、取消和中斷使用不同展開規則與文字標籤。

## 13. 回滾方案

本次改動應優先保持為前端漸進增強：

1. 專屬渲染只在 `toolName === "Agent"` 且資料可識別時啟用。
2. 刪除專屬分支後可以立即回退到現有通用工具卡。
3. 保留現有後台任務面板和子 Agent / Run 導航，不修改持久化資料。
4. 如果服務端僅補充公開關聯欄位，回滾時可停止前端消費；欄位本身保持向後相容。
5. 不做資料庫遷移，因此不涉及資料回滾。

## 14. 建議實施順序

建議按以下順序執行，避免先做樣式後發現數據無法關聯：

1. 驗證 `parentToolUseId` 資料鏈路。
2. 建立後台任務關聯索引。
3. 實現專屬 Agent 任務卡片。
4. 接入即時狀態更新。
5. 接入任務面板與子 Agent / Run 導航。
6. 完成狀態驅動的展開規則。
7. 補齊樣式與三語言文案。
8. 完成單元測試和手動流程驗證。
9. 執行完整檢查並審查最終 Diff。

## 15. 後續可選增強

以下能力不屬於本次範圍，可在真實使用資料證明有需要後再規劃：

- 父卡片顯示子 Run 工具呼叫總數，但不顯示明細。
- 顯示子任務 token 和成本摘要。
- 使用者顯式請求後加載只讀子 Run 摘要，而不是完整工具呼叫。
- 多個並行 Subagent 的分組檢視。
- 按角色、狀態或父 Run 過濾後台任務。
- 子任務完成後的通知偏好。
- 在不洩露 Prompt 的前提下提供更明確的任務結果摘要欄位。

這些增強仍應遵守同一原則：父會話只展示摘要，完整子 Agent 過程必須通過顯式導航按需檢視。
