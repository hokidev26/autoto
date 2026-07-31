# 外部更新日誌經驗 × Autoto 現況對照報告

> 原始日期：2026-07-12
> 落地複核：2026-07-13
> 輸入：GPT 對某相似專案更新日誌的八項工程原則總結
> 方法：逐項對照目前工作樹中的實際程式碼、遷移與測試
> 姊妹文件：`docs/notes/needtodo0712.md`
>
> 名稱說明：專案現名為 **Autoto**；舊 Autoto 名稱只保留在兼容介面或歷史記錄中。領域模型維持 **Agent / Workline**。

---

## 0. 最新結論

原始報告列出的近期高收益項目已收口：

- **4.1 至 4.7 全部完成目前定義的範圍。**
- Agent WebSocket 已使用 protocol 2：每個進程內提供單調序列、stream session、有界記憶體 replay，以及 cursor 過期、序列缺口、慢訂閱者或 session 不匹配時的 authoritative snapshot resync。
- 這不是 durable event log：事件仍未持久化，服務重啟或跨進程後不能 replay；若 IM Gateway 將跨進程補發變成正確性要求，仍需另立持久事件設計。
- Provider 已有 `Tools`、`Streaming`、`ImageInput` 最小 capability contract；未知或未宣告能力的 Provider 按不支援處理，業務層不需按名稱特判。
- Skills 已完成 global/project/workspace scope、revision 歷史與 restore，以及 snapshot-stable cursor 分頁；原先「目前不做」的描述已失效。
- P2–P3 已新增 V19 schedules/run source、V20 durable notification deliveries、V21 Telegram pairing/events/cursor、V22 device action requests，並由 runtime Supervisor 管理 channels / automation / HTTP 生命周期。
- Telegram 現況是 long polling + 私聊 `/pair`、`/status`、`/approve`（固定一次性 `allow_once`）與 `/deny`；未配對/錯誤配對靜默。沒有 `/task`、自由聊天、Telegram webhook、Slack 或 Discord。
- Home Assistant 僅允許本機/私網 endpoint；狀態摘要只讀，動作固定 allowlist 且要求本地雙確認/direct-loopback 批准；critical/未知動作硬阻斷，IM 不得控制設備。
- 通知已具持久歷史、去重、退避、`dead` 與 retry；monitoring snapshot 只做本地聚合，不是雲監控。
- `Read` / `Write` / `Edit` / `Glob` / `Grep` 已對敏感路徑硬阻斷或過濾；Bash/stdio MCP 仍不屬於此 filename boundary。
- 四條工程規範與快取七問已正式寫入 `CONTRIBUTING.md`，架構摘要已寫入 `docs/ARCHITECTURE.md`。

## 1. 八項原則最新狀態

| # | 原則 | 最新狀態 | 落地結果 |
| --- | --- | --- | --- |
| 1.1 | 派生資料由可信後端重算 | ✅ 完成 | Skill hash、scanner verdict、風險確認與正規化均由服務端產生；SQLite CHECK 約束提供最深層 fail-closed 防線。 |
| 1.2 | 兼容性能力契約 | ✅ 最小契約完成 | Provider 可宣告 `Tools`、`Streaming`、`ImageInput`；模型與設定 API 暴露同一能力資料，Agent loop 依能力降級或拒絕不一致輸出，不按 Provider 名稱特判。完整矩陣仍只在真實差異出現時擴充。 |
| 1.3 | 異步操作的代次、取消、超時、回退 | ✅ 當前範圍完成 | Skills 前端使用 request sequence 丟棄陳舊結果；Provider first-token timeout、retry/backoff 已有測試；Agent stream protocol 2 以有界 replay 與 snapshot resync 處理斷線、缺口與慢訂閱者。 |
| 1.4 | 事務原子性與 CAS | ✅ 完成 | Run 啟動、終態與中斷轉換使用前置狀態條件及 `RowsAffected`；Skill 更新使用 `updated_at` 樂觀鎖並回傳 409。 |
| 1.5 | 快取邊界、版本與失效 | ✅ 完成目前所需 | Skills 已加入 `scanner_version`，啟動只重掃版本或安全中繼資料不一致的候選；損壞列 fail-closed。快取審查清單已文件化。 |
| 1.6 | 摘要列表與詳情懶加載 | ✅ 完成 | `GET /api/skills` 回傳摘要，不含 prompt/findings 全文；`GET /api/skills/{id}` 回傳完整詳情，前端按需補載。 |
| 1.7 | 狀態機優先於布林組合 | ✅ 完成 | Skills 載入使用 `idle/loading/ready/stale/error`，刷新失敗且保留舊資料時明確進入 `stale`。 |
| 1.8 | 註冊能力與啟用策略分離 | ✅ 完成 | Skill 導入預設停用；blocked 永不可啟用；review 需綁定當前 content hash 的顯式確認；MCP 註冊與 enabled 亦分離。 |

## 2. 已完成的具體落地項

### 2.1 Run 狀態轉換 CAS

`internal/db/db.go` 已將 Run 轉換拆成帶前置狀態的更新：

- `pending -> running`
- `pending/running -> completed|error|interrupted|superseded`
- 每次更新均檢查 `RowsAffected`；不合法或競爭失敗時回傳 conflict。

這消除了「手動中斷與自然完成互相覆蓋」的原始風險。

### 2.2 Skill scanner version 與增量重掃

已完成：

- `skills.scanner_version` schema 與 migration；
- `internal/skills.ScannerVersion` 常數；
- 啟動時先掃描摘要中繼資料，只載入真正需要重掃的完整 prompt；
- scanner 版本、hash、verdict、findings 或欄位損壞時重算；
- 異常列停用並記錄 audit，而不是阻止服務啟動；
- CAS 保證啟動重掃不覆蓋較新的使用者更新。

### 2.3 Skills 前端 loadSeq 與狀態枚舉

已完成：

- `serverSkillsLoadSeq` 阻止陳舊請求覆蓋新結果；
- `serverSkillsStatus` 使用 `idle/loading/ready/stale/error`；
- 初次失敗為 `error`，已有資料的刷新失敗為 `stale`；
- UI 明確顯示「載入中／舊資料／載入失敗／已載入」；
- Node 測試覆蓋順序競爭和 stale/error 分支。

### 2.4 Skill 輕量 audit log

已完成 `skill_audit_events`：

- 記錄 `create/update/enable/disable/delete`；
- 保存 actor、skill ID、content hash、verdict、finding codes、風險確認時間；
- 不保存 prompt 或 scanner 訊息全文；
- audit 與 Skill mutation 位於同一交易；audit 寫入失敗時 mutation 回滾；
- scanner revalidation 也會留下安全相關 audit。

### 2.5 Skill 樂觀鎖與 409

已完成：

- API 要求 `expectedUpdatedAt`；
- Store 使用 `WHERE id = ? AND updated_at = ?`；
- 陳舊更新回傳 409；
- 前端只在真正的 optimistic-lock 409 時重新載入列表並提示使用者；
- 兩個客戶端競爭更新已有 DB、HTTP 與前端測試。

### 2.6 Skills 摘要列表與詳情 API

已完成：

- `ListSkillSummaries` 不讀取或回傳完整 prompt；
- `GET /api/skills/{id}` 提供詳情；
- 前端對 enabled/需要顯示詳情的 Skill 進行有限並發 hydration；
- 詳情載入失敗採 fail-closed，不允許本地模板繞過服務端保留命令；
- HTTP 與 Node 測試確認列表沒有 `prompt` / `scanFindings` 全文。

## 3. 已固化的工程規範

以下規範已寫入 `CONTRIBUTING.md`，並在 `docs/ARCHITECTURE.md` 提供架構摘要：

1. **不變量儘量下沉**：安全派生欄位由可信後端重算，能使用 DB CHECK 就不只依賴 handler。
2. **狀態轉換一律 CAS**：使用預期狀態或版本條件，並檢查 `RowsAffected`。
3. **交易生命週期保持封閉**：交易內只使用 `tx`；提交前不啟動未等待 goroutine，也不對外廣播成功。
4. **異步 UI 請求一律使用 seq**：陳舊結果丟棄；多布林狀態改用顯式枚舉。
5. **業務層禁止 Provider 名稱特判**：能力契約只在真實分歧出現時最小化加入。

快取新增前必須回答：

- 快取的來源與結果是什麼；
- 容量如何限制；
- 何時過期；
- 哪個 schema/scanner/model/algorithm 版本使其失效；
- 權限、身份、content hash 或 policy 如何使其失效；
- 快取失敗是 fail-open、fail-closed 還是回到權威重算；
- 是否包含 credentials、prompt、私有路徑或其他 secrets。

## 4. 落地清單最新狀態

| # | 項目 | 原優先級 | 最新狀態 |
| --- | --- | --- | --- |
| 4.1 | `UpdateRunStatus` 前置狀態守衛與合法轉換 | P1 | ✅ 完成 |
| 4.2 | `scanner_version` 與增量重掃 | P1 | ✅ 完成 |
| 4.3 | Skills loadSeq + status enum | P2 | ✅ 完成 |
| 4.4 | Skills 輕量 audit log | P2 | ✅ 完成 |
| 4.5 | Skill `updated_at` 樂觀鎖與 409 | P2 | ✅ 完成 |
| 4.6 | Skills 列表瘦身與詳情 endpoint | P3 | ✅ 完成 |
| 4.7 | WebSocket 單調序列與 catch-up | P3 | ✅ protocol 2 + 有界記憶體 replay + snapshot resync；durable/跨進程 replay 未完成 |

## 5. 已完成能力與仍保留的邊界

### 5.1 Agent stream protocol 2

目前 Agent WebSocket 已不是只能「斷線即丟」的即時流：protocol 2 為事件加上 `streamSession` 與單調 `sequence`，Hub 以固定 ring、replay 上限、subscriber buffer、最大 stream 數與 idle timeout 控制記憶體；重連可在同一進程與同一 stream session 內從 `after` cursor replay。

遇到 cursor 過期、replay 超限、慢訂閱者溢位、stream 淘汰、session 不匹配或前端觀察到序列缺口時，不做部分補發，而是要求讀取 authoritative live snapshot，再以 snapshot watermark 恢復連線。

仍未完成且不得混稱為已完成的是：

- durable event log；
- Agent 事件持久化與 retention policy；
- 服務重啟後或跨進程 replay；
- 多實例間一致的 stream session / sequence。

若產品 Phase B 的 IM Gateway 需要跨進程補發，再基於保留期、權限失效、entity generation 與重放上限另立持久事件設計；不直接把目前記憶體 ring 描述成 durable queue。

### 5.2 Provider capability contract

最小契約已完成，欄位為 `Tools`、`Streaming`、`ImageInput`。內建 Provider 明確宣告能力，未知 Provider 預設為全部不支援；Agent loop、模型 API 與設定 metadata 使用同一契約。

未預建 reasoning、audio、batch 等完整矩陣。後續只在真實 Provider 差異出現時擴充欄位，維持「能力驅動，不按 Provider 名稱特判」。

### 5.3 Skills 收口

Skills 已完成：

- global / project / workspace scope 與有效技能覆蓋順序；
- revision 快照、歷史列表、詳情與安全重掃記錄；
- 帶 optimistic-lock 的舊版本 restore；
- scope 列表、revision 列表與 effective Skills 的 snapshot-stable cursor 分頁。

因此，舊報告中把 scope、revision、restore 或 cursor 列為「目前不做」的說法已刪除。

## 6. 驗收證據

本輪收口已通過：

- `make check`；
- `go test -race ./internal/agent ./internal/server`；
- Skills DB migration、scanner revalidation、audit rollback、CAS conflict 測試；
- Skills HTTP summary/detail、風險確認與 optimistic-lock 測試；
- Skills Node load sequence、stale/error、detail hydration 與 fail-closed 測試；
- Agent/Workline 命名、Autoto 品牌與兼容遷移的全量回歸。

## 7. 最終判斷

這份報告的高收益程式碼建議已完成：Agent stream protocol 2、有界記憶體 replay、snapshot resync、Provider 最小能力契約，以及 scoped/revisioned Skills 與 snapshot cursor 都已落地。其後的 P2–P3 也已完成受限 schedules、durable deliveries、Telegram pairing/status/一次性 approval/deny、Home Assistant 受限動作與本地監控聚合。

必須繼續區分不同的「durable」：notification deliveries 與 Telegram channel events/cursors 已持久化，但 Agent WebSocket event stream 仍是進程內 ring，服務重啟或跨進程不能 replay。產品邊界同樣不可外推：`/task`、自由聊天、Slack/Discord、通用 IoT、攝像頭動作、門鎖解鎖與雲監控仍未實現。Provider 契約只按真實差異增量擴充。
