# 工具輸出 Pipeline 設計提案

狀態：討論稿
日期：2026-07-17
適用專案：Autoto

## 1. 摘要

本文提議為 Autoto 增加一套工具輸出 Pipeline 機制。它允許 Agent 臨時捕獲多個工具呼叫的完整結果，只把短預覽和別名放入模型上下文，最後通過受限的過濾規則合併、篩選並返回真正需要的資訊。

典型流程如下：

```text
StartPipeline
  ↓
Read / Grep / Bash / WebFetch 等工具正常執行
  ↓
完整結果保留用於審計，模型只收到 p1、p2……及短預覽
  ↓
EndPipeline 使用受限規則篩選捕獲結果
  ↓
只有最終篩選結果進入模型上下文
```

結論：Autoto 很適合加入該能力。它不會替代現有上下文壓縮，而是主動減少當前輪工具輸出對上下文視窗的佔用，尤其適合大型程式碼庫搜尋、測試輸出分析、日誌排查和多檔案探索。

## 2. 背景與問題

Autoto 當前擁有 `Read`、`Grep`、`Glob`、`Bash`、`WebFetch`、後台任務和動態工具等能力。工具結果執行後會作為結構化 `tool_result` 訊息加入後續模型請求。

當前可能產生較大結果的工具包括：

- `Read` 單次最多返回 100 KB，見 `internal/tools/read.go`。
- `Bash` 最終結果最多返回 20 KB，流式輸出最多傳送 100 KB，見 `internal/tools/bash.go`。
- `Grep` 預設最多返回 100 條匹配，但單行長度和多次搜尋仍可能形成較大上下文。
- Web、MCP 和動態工具也可能返回大量結構化或文本內容。

工具結果目前在 `internal/agent/loop.go` 的模型迴圈中被完整寫入工具結果訊息。專案已經實現上下文預算、舊訊息摘要和工具結果省略，但這些機制主要在上下文接近限制或訊息變舊後生效。

這會產生幾個問題：

1. Agent 在探索階段可能讀取大量最終並不需要的資訊。
2. 多個獨立工具結果會分別佔用上下文，模型最後只使用其中很小一部分。
3. 現有壓縮機制會在後期省略舊結果，但無法減少當前輪剛產生的大輸出。
4. 大量原始日誌、測試輸出和搜尋結果可能降低模型對關鍵資訊的注意力。
5. 使用 Bash 自行拼接 `grep | head | sort` 雖然可以減少輸出，但會繞過專用工具，並增加命令權限與安全分析成本。

## 3. 適配性評估

### 3.1 有利條件

Autoto 的架構具備實現 Pipeline 的基礎：

- 核心工具通過 `internal/tools/registry.go` 集中註冊，新增控制工具較直接。
- 所有工具共享 `tools.Result`，結果包含 `Output`、`IsError` 和 `Meta`。
- `tools.Env` 已包含 `AgentID`、`RunID`、`CWD` 和 Store，可用於隔離 Pipeline 會話。
- 當前模型迴圈按順序處理工具呼叫，便於為每個 Run 分配穩定的 `p1`、`p2` 等別名。
- 工具完整結果已經持久化到 Tool Call 記錄，可以繼續用於審計和 UI 檢視。
- 後台任務已有輸出分頁、位元組限制、截斷和生命週期管理，可複用相關設計經驗。
- Agent 已有 Run 完成、取消、替代和中斷生命週期，可在這些邊界自動釋放 Pipeline 狀態。

### 3.2 預期收益

Pipeline 對以下場景收益較高：

- 在大型程式碼庫中執行多輪 `Glob`、`Grep` 和 `Read`。
- 同時讀取多個配置、日誌或測試檔案後統一篩選。
- 執行測試、構建或靜態檢查後只提取失敗資訊。
- 分析 Git 狀態、提交歷史或大段 Diff 摘要。
- 呼叫返回內容較大的 Web、MCP 或動態工具。
- 子 Agent 或長任務需要控制上下文成本。

如果使用者主要執行短小、單檔案編輯，收益相對有限。因此建議將 Pipeline 作為 Agent 自動選擇的上下文效率能力，而不是要求每個任務強制使用。

## 4. 目標與非目標

### 4.1 目標

1. 減少多工具探索產生的模型輸入 token。
2. 保留完整工具結果的審計能力。
3. 提供確定性、受限且容易測試的過濾語法。
4. 不改變底層工具原有的權限和風險判斷。
5. 按 Agent 和 Run 嚴格隔離捕獲內容。
6. 對模型提供清晰、穩定的別名和短預覽。
7. 在 Pipeline 未啟用時不改變現有行為。

### 4.2 非目標

1. 不實現通用 Shell 管道直譯器。
2. 不允許 Pipeline 規則執行程式、訪問網路或修改檔案。
3. 不替代 `Bash`、`Grep`、`Task` 或上下文摘要機制。
4. MVP 不支持嵌套 Pipeline。
5. MVP 不要求在伺服器重啟後恢復未結束的 Pipeline。
6. 不允許 Pipeline 降低底層工具的權限等級或繞過人工審批。

## 5. 建議的使用者與模型介面

建議對模型暴露兩個控制工具，底層共用一個 Pipeline Service。

### 5.1 `StartPipeline`

職責：為當前 Agent Run 開始一個工具結果捕獲會話。

建議輸入：

```json
{
  "label": "分析測試失敗",
  "max_preview_chars": 100
}
```

建議輸出：

```text
Pipeline 已啟動。後續可捕獲的工具結果將儲存為 p1、p2……，模型只接收短預覽。
```

行為約束：

- 必須存在有效 `RunID`。
- 同一個 Run 同時只允許一個活動 Pipeline。
- 不支援巢狀啟動。
- `max_preview_chars` 應有較小且固定的上下限。

### 5.2 捕獲後的普通工具結果

例如 `Read` 返回 30 KB 內容後，模型收到：

```text
已捕獲為 p1
工具：Read
位元組數：30720
錯誤：false
預覽：package agent ...
```

完整結果仍應：

- 儲存在 Tool Call 輸出記錄中。
- 可在工具活動詳情中檢視。
- 保持原有 `IsError` 語義。
- 遵循既有敏感資料處理和訪問邊界。

### 5.3 `EndPipeline`

職責：讀取已捕獲的別名，應用受限規則，並返回最終結果。

建議輸入：

```json
{
  "rule": "from p1 p2 p3 | grep -i \"error|failed\" | sort | uniq | head -n 30",
  "format": "sections",
  "max_chars": 12000
}
```

建議輸出包含：

- 使用的別名。
- 捕獲項數量。
- 實際執行的規範化規則。
- 最終篩選結果。
- 是否發生截斷。

規則解析或驗證失敗時，不應立即銷燬 Pipeline，以便模型修正規則後重試。成功結束後應釋放該 Run 的捕獲狀態。

可選增加 `discard` 引數，讓模型在不需要結果時直接關閉並丟棄當前捕獲會話。

## 6. 受限規則語法

MVP 建議只支援以下操作：

```text
from p1 p2
cat
從指定別名讀取內容

grep PATTERN
grep -i PATTERN
grep -v PATTERN
按正規表示式保留或排除行

head -n N
tail -n N
限制行數

sort
sort -r
排序

uniq
去除相鄰重複行

cut -d DELIMITER -f FIELDS
提取字段
```

說明：

- 如果規則未顯式包含 `from`，可以使用輸入引數中的預設別名列表。
- 每個操作都必須設定明確的輸入、輸出和複雜度限制。
- 正規表示式需要限制長度，並在可能的情況下避免災難性回溯。
- `cut` 的欄位數量和範圍必須受限。
- `head`、`tail` 和最終字元數必須設定硬上限。

明確禁止：

- 命令替換。
- 文件重定向。
- 子 Shell。
- 環境變數展開。
- `eval`、`xargs` 或任意程式執行。
- 檔案系統、網路、程序和資料庫訪問。
- 把規則字串交給 `/bin/sh`、`bash` 或 `cmd.exe` 執行。

規則應由專案內獨立解析器處理，而不是複用真實 Shell。

## 7. 建議架構

### 7.1 Pipeline Manager

建議增加一個 Run 級管理器，維護：

```text
(agentID, runID)
  ├─ active
  ├─ label
  ├─ nextAlias
  ├─ createdAt
  ├─ totalBytes
  └─ captures
       ├─ p1 → toolUseID、toolName、output、isError
       ├─ p2 → toolUseID、toolName、output、isError
       └─ ...
```

Manager 必須支援併發訪問，即使當前模型迴圈順序執行，也應為未來並行工具呼叫預留安全性。

建議職責：

- `Start`
- `IsActive`
- `Capture`
- `End`
- `Discard`
- `CloseRun`

### 7.2 控制工具

建議在 `internal/tools` 中增加：

```text
pipeline.go
pipeline_test.go
```

其中包含：

- `StartPipelineTool`
- `EndPipelineTool`
- 輸入 Schema
- Pipeline Service 接口
- 受限規則的呼叫入口

控制工具風險建議標記為 `RiskRead`，因為它們本身不修改工作區，也不執行外部程式。

### 7.3 Agent Loop 集成

只註冊兩個普通工具還不夠，因為普通工具無法自動攔截其他工具的結果。

建議在 `internal/agent/loop.go` 中，普通工具執行完成、完整結果已經記錄，但尚未生成模型可見 `tool_result` 訊息時進行轉換。

當前相關流程位於模型迴圈執行工具並建立工具結果訊息的位置。建議邏輯為：

```go
rawResult := executeToolForLoop(...)
modelResult := pipeline.ProcessResult(agentID, runID, call, rawResult)

// Tool Call 審計記錄繼續保留 rawResult。
// 發給模型的 tool_result 使用 modelResult。
```

處理規則：

- `StartPipeline` 和 `EndPipeline` 自身結果不捕獲。
- 未開啟 Pipeline 時直接返回原始結果。
- 開啟後，將普通工具結果儲存為別名並返回短預覽。
- 權限拒絕和工具錯誤仍保留原有錯誤狀態。
- 工具活動 UI 仍可以展示已有的受限結果預覽。

這種整合方式隻影響模型可見結果，不需要改變所有現有工具實現，也不會影響外部 API 直接執行工具的呼叫者。

### 7.4 生命週期清理

以下情況必須呼叫 `CloseRun`：

- Run 正常完成。
- Run 被使用者取消。
- Run 被新請求替代。
- Run 執行失敗。
- Agent 被刪除或 Runner 關閉。
- Pipeline 超過最大存活時間。

清理操作必須冪等。

## 8. 資料持久化策略

### 8.1 MVP 建議

MVP 使用記憶體中的 Run 級狀態：

優點：

- 實現簡單。
- 不重複持久化可能敏感的大段輸出。
- Pipeline 本身只是短期上下文最佳化能力。

缺點：

- 伺服器重啟後活動 Pipeline 無法繼續。
- 無法支援跨程序 Runner。

伺服器重啟後，未結束 Pipeline 應明確失敗並提示重新開始，而不是靜默返回不完整資料。

### 8.2 後續持久化方案

如果未來需要恢復，可以只持久化：

- Pipeline 會話狀態。
- 別名到 `toolUseID` 的對應。
- 順序和大小後設資料。

完整輸出可以從現有 Tool Call 輸出記錄讀取，避免儲存第二份副本。

## 9. 限制與資源保護

建議初始限制：

| 專案 | 建議預設值 |
|---|---:|
| 每個 Run 的活動 Pipeline | 1 |
| 最大捕獲項 | 64 |
| 單項模型預覽 | 100 字元 |
| Pipeline 總捕獲量 | 2–4 MB |
| 規則長度 | 4 KB |
| 規則運算元 | 16 |
| 最終返回內容 | 12 KB |
| 最大輸出行數 | 1,000 |
| 空閒超時 | 10 分鐘 |

具體數值可以根據真實 Dogfood 資料調整，但必須存在硬上限。

當達到限制時：

- 不應導致整個 Agent Run 崩潰。
- 應返回結構化、可理解的錯誤。
- 已有別名應繼續可用。
- 應明確指出哪些內容未捕獲或被截斷。

## 10. 安全與權限

### 10.1 不改變底層工具權限

Pipeline 只處理已經完成權限判斷的工具結果：

- `Read` 仍是讀取風險。
- `Bash` 仍需要執行權限或人工批准。
- 寫入和危險工具仍遵守現有策略。
- Pipeline 不能把多個操作包裝成一次批准以規避逐工具審計。

### 10.2 嚴格隔離

所有讀取和結束操作必須同時校驗：

- `AgentID`
- `RunID`
- Pipeline 會話狀態

禁止一個 Agent 或 Run 讀取另一個 Run 的別名。

### 10.3 輸出與敏感資料

Pipeline 不應創造新的資料訪問能力，但會增加結果快取，因此需要：

- 遵循現有工具輸出訪問控制。
- 對 UI 和事件中的預覽繼續執行長度限制。
- 不把捕獲內容寫入日誌。
- 錯誤訊息不得包含完整敏感結果。
- 清理時釋放所有記憶體引用。

### 10.4 命名衝突

專案當前已使用 `Pipeline` 表示 Shell 命令管道，例如命令事實分析中的 `Pipeline` 欄位。

為避免內部概念混淆，建議內部服務命名為：

- `ToolOutputPipeline`
- `ResultPipeline`
- `ContextPipeline`

對模型暴露的名稱仍可使用 `StartPipeline` 和 `EndPipeline`。

## 11. UI 與可觀測性

MVP 可以不新增複雜 UI，因為現有工具活動已經展示工具呼叫、狀態、結果預覽和截斷狀態。

建議至少補充以下可觀測資訊：

- Pipeline 啟動事件。
- 捕獲別名，例如 `p3`。
- 捕獲工具名稱和原始位元組數。
- Pipeline 結束事件。
- 最終規則和輸出是否截斷。

UI 可以顯示：

```text
Read 已完成 · 結果捕獲為 p2 · 48.1 KB
```

完整輸出仍通過原有工具詳情檢視，模型訊息中只顯示別名和短預覽。

## 12. 錯誤處理

建議定義穩定錯誤類型：

- `pipeline_not_active`
- `pipeline_already_active`
- `pipeline_run_required`
- `pipeline_alias_not_found`
- `pipeline_limit_exceeded`
- `pipeline_rule_invalid`
- `pipeline_operation_not_allowed`
- `pipeline_output_truncated`
- `pipeline_state_lost`

行為建議：

- Start 失敗：不建立部分狀態。
- Capture 失敗：返回原始工具結果或明確的受限預覽，不得靜默丟失。
- End 規則錯誤：保留活動 Pipeline，允許重試。
- End 成功：先生成最終結果，再原子關閉會話。
- Run 結束：無論 End 是否呼叫都強制清理。

## 13. 測試計劃

### 13.1 單元測試

1. Start 後依次捕獲 `p1`、`p2`。
2. 未 Start 時工具結果保持不變。
3. 控制工具自身不會被捕獲。
4. 同一 Run 不允許巢狀 Start。
5. 不同 Agent 和 Run 之間嚴格隔離。
6. 捕獲項數、總位元組和預覽長度限制有效。
7. UTF-8 截斷不會產生無效字串。
8. `grep`、`head`、`tail`、`sort`、`uniq` 和 `cut` 行為確定。
9. 禁止命令替換、重定向和任意程式執行。
10. End 解析失敗後仍可重試。
11. End 成功後狀態被釋放。
12. Run 取消、失敗和替代時狀態被清理。
13. 底層工具風險和批准流程不受影響。
14. 工具錯誤結果仍保持 `IsError` 語義。

### 13.2 Agent Loop 整合測試

模擬 Provider 按以下順序呼叫：

```text
StartPipeline
Read
Grep
EndPipeline
```

驗證：

- Tool Call 資料庫記錄包含完整原始結果。
- 模型歷史中的中間結果只包含別名和預覽。
- EndPipeline 返回經過過濾的內容。
- 最終請求 token 估算顯著小於未使用 Pipeline 的請求。
- 現有工具事件和審計記錄沒有迴歸。

### 13.3 安全測試

- 嘗試注入 `; rm ...`。
- 嘗試 `$(...)`、反引號和環境變數展開。
- 嘗試跨 Run 讀取別名。
- 嘗試構造超長正則、欄位列表和輸出。
- 嘗試在 Pipeline 中繞過 Bash 審批。

所有場景都應失敗關閉或返回受限錯誤，不得執行外部命令。

## 14. 分階段實施建議

### 階段一：核心 MVP

- 記憶體 Pipeline Manager。
- `StartPipeline` 與 `EndPipeline`。
- Run 級隔離和生命週期清理。
- `from`、`grep`、`head`、`tail`、`sort`、`uniq`。
- 基礎單元測試和 Agent Loop 整合測試。

### 階段二：產品體驗

- 工具活動中的捕獲別名和大小展示。
- `cut` 與更好的 sections 輸出格式。
- 使用量、節省字元數和截斷指標。
- 根據模型能力調整工具描述。

### 階段三：耐久性與高階能力

- Pipeline 會話持久化。
- 伺服器重啟恢復。
- 別名引用已有 Tool Call 輸出，避免重複儲存。
- 後台任務輸出與 Pipeline 的安全組合。
- 基於真實使用資料自動建議或自動啟用 Pipeline。

## 15. 需要評審者重點確認的問題

1. Pipeline 是否只對模型迴圈生效，還是也應開放給外部工具執行 API？
2. MVP 是否接受伺服器重啟後丟失活動 Pipeline？
3. 完整捕獲內容應儲存在記憶體，還是隻儲存 Tool Call 引用？
4. End 規則錯誤時是否保持 Pipeline 活動？本文建議保持。
5. 底層工具發生錯誤時，是否仍分配別名？本文建議分配並保留錯誤狀態。
6. 是否需要第三個 `CancelPipeline` 工具，還是由 `EndPipeline` 的 `discard` 引數承擔？
7. 預設總捕獲量和最終返回上限應設定為多少？
8. 是否需要在第一階段同步實現前端狀態展示？

## 16. 最終建議

建議加入工具輸出 Pipeline，並優先採用“小型、受限、Run 級、預設記憶體實現”的方案。

它與 Autoto 當前架構匹配，能直接解決多工具探索造成的當前輪上下文膨脹問題。實現時最重要的原則是：

1. 完整結果繼續保留用於審計。
2. 只轉換髮給模型的結果，不侵入每個現有工具。
3. Pipeline 規則絕不交給真實 Shell 執行。
4. 不改變底層工具權限和審批邊界。
5. 所有狀態嚴格繫結 Agent 與 Run，並在生命週期結束時清理。

在這些約束下，該功能具備明確收益，實施複雜度中等，適合作為 Autoto 的上下文效率增強能力進入後續評審與實現階段。
