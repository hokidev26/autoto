# 外部更新日誌經驗 × Autoto 現況對照報告（修正版）

> 原始日期：2026-07-12
> 原始落地複核：2026-07-13
> 本次重新核對：2026-08-05（對照當前工作樹逐條驗證）
> 第二次核對：2026-08-05 稍後（修正本文自身的過期結論，見「第二次修正說明」）
> 輸入：GPT 對某相似專案更新日誌的八項工程原則總結
> 姊妹文件：`docs/notes/needtodo0712.md`、`docs/notes/external-changelog-lessons-for-autoto.md`、`docs/CODE_REVIEW_2026-07-18.md`
>
> 名稱說明：專案現名為 **Autoto**；舊名稱只保留在兼容介面或歷史記錄中。領域模型維持 **Agent / Workline**。

## 本次修正說明

原報告的工程結論基本成立，但它是 07-13 的快照，而工作樹已前進約三週（新增 `peercontrol`、`themes`、`toolpipeline`、`plugins`、`oauthapp` 等模組）。以下修正分兩類：**事實已漂移**（原文當時正確，現已過期）與**原文本身不準確**。

| # | 位置 | 類型 | 修正 |
| --- | --- | --- | --- |
| M1 | §0、§4 | 已漂移 | migration 已到 **V61**，不是「新增 V19–V22」為最新。 |
| M2 | §1.2、§5.2 | 原文不準確（現已失效） | capability contract 早已不是三個欄位；reasoning 相關能力**已經建成**，原文「未預建 reasoning」為假。 |
| M3 | §2.1 | 原文檔案路徑錯誤 | `internal/db/db.go` **不存在**；`UpdateRunStatus` 在 `internal/db/runs_checkpoints.go:269`。 |
| M4 | §2.1 | 原文描述不準確 | `UpdateRunStatus` 現在**只做** `pending -> running`；終態轉換在 `CompleteRun` 等獨立函式。 |
| M5 | §0 | 原文列舉不全 | 敏感路徑硬阻斷還包含 **`LS`**，不只 Read/Write/Edit/Glob/Grep。 |
| M6 | §5.3 | 原文誇大 | Skills scope/revision 只在 DB 與 API 層完整；**UI 變更操作仍限 global**。 |
| M7 | §0 | 原文用詞不符程式碼 | Home Assistant 風險層級是 `medium/high/blocked`，**沒有 `critical` 這一級**。 |
| M8 | §0、§3 | 新發現的真實矛盾 | `docs/ARCHITECTURE.md:187` 仍稱 catch-up「intentionally deferred」，與已落地的 protocol 2 **直接矛盾**，需修文件。 |
| M9 | §6 | 證據強度不足 | 本次環境無 Go 工具鏈，§6 的 Go 測試結論**無法重驗**；07-18 review 亦受同一限制。 |
| M10 | §7 | 語氣過於收口 | 07-18 review 列出 5 個未修 bug，「高收益建議已完成」不等於「無未決問題」。 |

## 第二次修正說明

第一次修正（M1–M10）本身也有兩處已經站不住腳，且第一次修正建議的動作已可部分執行。以下是對本文自身的修正：

| # | 位置 | 類型 | 修正 |
| --- | --- | --- | --- |
| N1 | M10、§7 | **本文最嚴重的錯誤** | 07-18 review 的 bug #1–#5 **在當前工作樹已全部修好**。M10 與 §7 把它們當成未決問題列出，結論相反。詳見新增的 §8。 |
| N2 | M8、§0、§3、§4 | 已修復 | `docs/ARCHITECTURE.md` 的 catch-up 段落**已於本次改寫**，不再與 protocol 2 矛盾。這條「唯一的文件級不一致」已關閉。 |
| N3 | §6 | 證據可部分升級 | Go 工具鏈仍不存在（`go` 不在 PATH），但 **Node 21.7.3 可用**，前端測試本次實際執行：957 個測試 955 通過、2 失敗。§6 不再是純靜態核對。 |
| N4 | §6 | 新發現 | 2 個失敗集中在 `white-shell.test.mjs`，成因是**未提交 WIP 中的過期測試斷言**而非產品缺陷；細節見 §8.2。 |
| N5 | §7、建議動作 | 過期 | 原「建議後續動作」第 1、3 項已無效（文件已修、bug 已修），第 2、4 項仍成立。 |

---

## 0. 最新結論

原始報告列出的近期高收益項目已收口，但收口範圍需要精確描述：

- **4.1 至 4.7 全部完成「當時定義」的範圍。**
- Agent WebSocket 已使用 protocol 2（`internal/agent/hub.go:17`）：每個進程內提供單調序列、stream session、有界記憶體 replay，以及五種明確 resync 原因下的 authoritative snapshot resync。
- 這不是 durable event log：事件仍未持久化（DB 中無 agent event 表），服務重啟或跨進程後不能 replay；若 IM Gateway 將跨進程補發變成正確性要求，仍需另立持久事件設計。
- Provider capability contract **已遠超**原報告描述的三個欄位，現含 `Tools`、`Streaming`、`ImageInput`、`ImageGeneration`、`Reasoning`、`ReasoningEffort`、`ReasoningEfforts`、`NativeReasoningBlocks`，並另有 model 層 `ModelCapabilities`。未宣告能力的 Provider 仍按不支援處理。
- Skills 已完成 global/project/workspace scope、revision 歷史與 restore，以及 snapshot-stable cursor 分頁；但**建立、SKILL.md 匯入、啟用/停用、編輯、刪除的 UI 操作仍只支援 global**，`ListSkillSummaries` 亦寫死 `scope = 'global'`。
- Schedules/notifications/channels/device-actions 對應 V19–V22，**但 migration 現已到 V61**；把 V19–V22 講成「最新新增」已過期。
- Telegram 現況是 long polling + 私聊 `/pair`、`/status`、`/approve`（固定一次性 `allow_once`）與 `/deny`；未配對/錯誤配對靜默。沒有 `/task`、自由聊天、Telegram webhook、Slack 或 Discord（全 repo grep 無 Slack/Discord 實作）。
- Home Assistant 僅允許 loopback、`.local`/`.localhost`、link-local 或私網 endpoint（`internal/server/integrations_api.go:247`）；狀態摘要只讀且屬性過濾，動作固定 8 條 allowlist，要求本地雙確認 + direct-loopback 批准（`internal/server/monitoring.go:109`）。風險層級為 `medium/high/blocked`；allowlist 外或不合法動作一律 `blocked` 且永不送出，IM 不得發起或批准設備動作。
- 通知已具持久歷史、去重、退避、`dead` 與 retry；monitoring snapshot 只做本地聚合，不是雲監控。
- `Read` / `Write` / `Edit` / `Glob` / `Grep` / **`LS`** 已對敏感路徑硬阻斷或過濾（`sensitiveToolPath`，`internal/tools/path.go:93`）；Bash/stdio MCP 仍不屬於此 filename boundary。
- 五條工程規範（非四條）與快取七問已正式寫入 `CONTRIBUTING.md:56-64` 與 `:66-78`。
- ~~`docs/ARCHITECTURE.md:187` 尚未同步~~ **已修**：該段原寫「monotonic persisted event sequence and catch-up protocol are intentionally deferred」，與 protocol 2 已實作的 sequence + catch-up 矛盾；現已改寫為「protocol 2 已實作 stream session、單調 sequence、有界 replay 與五種具名 resync；durable 持久化與跨進程 replay 仍延後」。第一次修正指出的唯一文件級不一致已關閉。

## 1. 八項原則最新狀態

| # | 原則 | 最新狀態 | 落地結果 |
| --- | --- | --- | --- |
| 1.1 | 派生資料由可信後端重算 | ✅ 完成 | Skill hash、scanner verdict、風險確認與正規化均由服務端產生；SQLite CHECK 約束提供最深層 fail-closed 防線。 |
| 1.2 | 兼容性能力契約 | ✅ 完成，且已超出原範圍 | Provider 宣告 `Tools`/`Streaming`/`ImageInput`/`ImageGeneration`/`Reasoning`/`ReasoningEffort`/`ReasoningEfforts`，另有 `NativeReasoningBlocks` 內部路由能力與 model 層 `ModelCapabilities`（FastMode、ImageGeneration、ContextTokenLimit、per-model ReasoningEfforts）。未實作 `CapabilityProvider` 者視為全部不支援，不按 Provider 名稱特判。 |
| 1.3 | 異步操作的代次、取消、超時、回退 | ✅ 當前範圍完成 | Skills 前端使用 request sequence 丟棄陳舊結果；Provider first-token timeout、retry/backoff 已有測試；protocol 2 以有界 replay 與 snapshot resync 處理斷線、缺口與慢訂閱者。 |
| 1.4 | 事務原子性與 CAS | ✅ 完成 | Run 啟動與終態轉換使用前置狀態條件及 `RowsAffected`（`requireRunTransition`）；Skill 更新使用 `updated_at` 樂觀鎖並回傳 409。 |
| 1.5 | 快取邊界、版本與失效 | ✅ 完成目前所需 | Skills 已加入 `scanner_version`，啟動只重掃版本或安全中繼資料不一致的候選；損壞列 fail-closed。快取七問已文件化。 |
| 1.6 | 摘要列表與詳情懶加載 | ✅ 完成 | `ListSkillSummaries` 不 SELECT `prompt`，只回傳 `FindingCount` 而非 findings 全文；`GET /api/skills/{id}` 回傳完整詳情。 |
| 1.7 | 狀態機優先於布林組合 | ✅ 完成 | Skills 載入使用 `idle/loading/ready/stale/error`，刷新失敗且保留舊資料時明確進入 `stale`。 |
| 1.8 | 註冊能力與啟用策略分離 | ✅ 完成 | Skill 導入預設停用；blocked 永不可啟用；review 需綁定當前 content hash 的顯式確認；MCP 註冊與 enabled 亦分離。 |

## 2. 已完成的具體落地項

### 2.1 Run 狀態轉換 CAS

**修正檔案路徑**：原報告寫的 `internal/db/db.go` 已不存在（該檔案在 07-18 review 中為 4.9K 行，之後已按主題拆分）。Run 轉換現在位於 `internal/db/runs_checkpoints.go`。

**修正職責劃分**：`UpdateRunStatus`（:269）不再處理所有轉換，它只接受 `running`，其他狀態直接回錯：

- `UpdateRunStatus`：僅 `pending -> running`，SQL 帶 `WHERE id = ? AND status = 'pending'`。
- `CompleteRun`（:477）：處理 `completed|error|interrupted|superseded` 等終態。
- `FailRunGitRollback`（:446）：git rollback 失敗路徑。
- 共用 `requireRunTransitionWithQuerier`（:295）檢查 `RowsAffected`；非 1 時先確認 run 是否存在，再回 `ErrConflict` 或 `sql.ErrNoRows`。終態轉換以 `requireRunTransitionTx` 保證 CAS 檢查與 mutation 在同一交易同一連線。

**修正狀態集合**：run 狀態已擴充為 `pending`、`running`、`continuation_pending`、`completed`、`interrupted`、`error`、`superseded`、`skipped`（:262）。原報告的狀態列表缺 `continuation_pending` 與 `skipped`。

「手動中斷與自然完成互相覆蓋」的原始風險已消除，這一結論不變。

### 2.2 Skill scanner version 與增量重掃

原文描述準確，維持：

- `skills.scanner_version` schema 與 migration；
- `internal/skills.ScannerVersion` 常數；
- 啟動時先掃描摘要中繼資料，只載入真正需要重掃的完整 prompt；
- scanner 版本、hash、verdict、findings 或欄位損壞時重算；
- 異常列停用並記錄 audit，而不是阻止服務啟動；
- CAS 保證啟動重掃不覆蓋較新的使用者更新。

### 2.3 Skills 前端 loadSeq 與狀態枚舉

原文描述準確，維持：`serverSkillsLoadSeq` 阻止陳舊請求覆蓋新結果；`serverSkillsStatus` 使用 `idle/loading/ready/stale/error`；初次失敗為 `error`，已有資料的刷新失敗為 `stale`；Node 測試覆蓋順序競爭與 stale/error 分支。

### 2.4 Skill 輕量 audit log

原文描述準確。`skill_audit_events` 建於 `internal/db/migrations.go:476`，並有 `idx_skill_audit_events_skill_created` 索引：

- 記錄 `create/update/enable/disable/delete`；
- 保存 actor、skill ID、content hash、verdict、finding codes、風險確認時間；
- 不保存 prompt 或 scanner 訊息全文；
- audit 與 Skill mutation 位於同一交易；audit 寫入失敗時 mutation 回滾；
- scanner revalidation 也會留下安全相關 audit。

### 2.5 Skill 樂觀鎖與 409

原文描述準確。`DeleteSkillCAS`（`skills_scopes_revisions.go:507`）與 `RestoreSkillAs`（:553）都要求 `expectedUpdatedAt`，SQL 帶 `AND updated_at = ?`；陳舊更新回傳 409；前端只在真正的 optimistic-lock 409 時重新載入列表並提示使用者。

### 2.6 Skills 摘要列表與詳情 API

原文描述準確，補充實作細節：`ListSkillSummaries`（`skills_store.go:256`）的 SELECT 清單不含 `prompt`，且把 `scan_findings_json` 只解析成 `FindingCount` 整數，`SkillSummary` 結構本身沒有 prompt/findings 欄位——這是型別層面的保證，不只靠 handler 過濾。`GetSkill`（:281）才回傳完整 prompt。

**補充邊界**：`ListSkillSummaries` 的查詢寫死 `AND scope = 'global'`，因此這個舊 endpoint 本質上是 global-only 列表；scoped 列表走 `skills_scopes_revisions.go` 的分頁 API。

## 3. 已固化的工程規範

以下規範已寫入 `CONTRIBUTING.md:56-64`。**修正數量：是五條，不是四條**，原報告漏掉了交易自封閉那條的編號對齊：

1. **不變量儘量下沉**：安全派生欄位由可信後端重算，能使用 DB CHECK 就不只依賴 handler。
2. **狀態轉換一律 CAS**：使用預期狀態或版本條件，並檢查 `RowsAffected`；不得寫成 read-check-unconditional-write。
3. **交易生命週期保持封閉**：交易內只使用 `tx`；提交前不啟動未 join 的 goroutine，也不對外廣播成功。
4. **異步 UI 請求一律使用 seq**：陳舊結果丟棄；超過兩個相關 loading/error 布林就改用顯式枚舉。
5. **業務層禁止 Provider 名稱特判**：能力契約只在真實分歧出現時最小化加入。

快取新增前必須回答七問（`CONTRIBUTING.md:66-78`）：來源與結果、容量上限、過期與最大 stale 時間、哪個 schema/scanner/model/algorithm 版本使其失效、哪些權限/身份/content hash/policy 使其失效、失敗時 fail-open 或 fail-closed 或回到權威重算、是否可能含 credentials/prompt/私有路徑/secrets。

~~**待修**：`docs/ARCHITECTURE.md:176-184` 已同步這五條規範與快取要求，但同檔 :187 的 Agent WebSocket 段落仍是 protocol 2 之前的舊描述。~~ **已修**：該段已改寫為三段敘述——protocol 2 的 session/sequence/有界 replay、五種具名 resync 與 snapshot 恢復、以及「per-process in-memory、非 durable event log」的明確邊界。`docs/ARCHITECTURE.md:176-184` 的五條規範與快取要求原本就已同步。

## 4. 落地清單最新狀態

| # | 項目 | 原優先級 | 最新狀態 |
| --- | --- | --- | --- |
| 4.1 | Run 轉換前置狀態守衛與合法轉換 | P1 | ✅ 完成（實作已拆分至 `runs_checkpoints.go`，非原文的 `db.go`） |
| 4.2 | `scanner_version` 與增量重掃 | P1 | ✅ 完成 |
| 4.3 | Skills loadSeq + status enum | P2 | ✅ 完成 |
| 4.4 | Skills 輕量 audit log | P2 | ✅ 完成 |
| 4.5 | Skill `updated_at` 樂觀鎖與 409 | P2 | ✅ 完成 |
| 4.6 | Skills 列表瘦身與詳情 endpoint | P3 | ✅ 完成（舊列表 endpoint 為 global-only） |
| 4.7 | WebSocket 單調序列與 catch-up | P3 | ✅ protocol 2 + 有界記憶體 replay + snapshot resync；durable/跨進程 replay 未完成（刻意）；ARCHITECTURE.md 已同步 |

## 5. 已完成能力與仍保留的邊界

### 5.1 Agent stream protocol 2

`internal/agent/hub.go` 為事件加上 `streamSession` 與單調 `sequence`（`Event.Protocol`/`StreamSession`/`Sequence`），並以具名上限控制記憶體：`DefaultRingSize` 512、`DefaultRingBytes` 512KB、`DefaultMaxEventBytes` 32KB、`DefaultReplayLimit` 256、`DefaultSubscriberBuffer` 64、`DefaultMaxStreams` 256、`DefaultStreamIdleTimeout` 15 分鐘。重連可在同一進程與同一 stream session 內從 `after` cursor replay。

resync 原因是列舉而非隱式判斷，共五種：`session_mismatch`、`cursor_expired`、`replay_limit`、`subscriber_overrun`、`stream_evicted`。觸發時不做部分補發，而是要求讀取 authoritative live snapshot（`internal/server/stream_recovery.go`、`internal/db/live_snapshot.go`），再以 snapshot watermark 恢復連線。`Subscription` 的 replay 與 live subscriber 在同一把 Hub 鎖下安裝，避免安裝窗口丟事件。

仍未完成且不得混稱為已完成的是：

- durable event log（DB 中確認無 agent event / stream event 表）；
- Agent 事件持久化與 retention policy；
- 服務重啟後或跨進程 replay；
- 多實例間一致的 stream session / sequence。

若產品 Phase B 的 IM Gateway 需要跨進程補發，再基於保留期、權限失效、entity generation 與重放上限另立持久事件設計；不直接把目前記憶體 ring 描述成 durable queue。

### 5.2 Provider capability contract

**本節為原報告失效最嚴重處。** 原文稱「未預建 reasoning、audio、batch 等完整矩陣」，但 reasoning 能力現已建成，且分為 provider 與 model 兩層（`internal/providers/provider.go:206-238`）：

- `Capabilities`：`Tools`、`Streaming`、`ImageInput`、`ImageGeneration`、`Reasoning`、`ReasoningEffort`、`ReasoningEfforts []string`，另有不序列化的 `NativeReasoningBlocks`（標示 adapter 能重放不透明的簽章/加密 reasoning 區塊）。
- `ModelCapabilities`：`FastMode`、`ImageGeneration`、`ContextTokenLimit`，以及可覆寫 provider 清單的 per-model `ReasoningEfforts`。`*Known` 布林用來區分「已知為 false」與「無知識」，避免把未知當否定。
- `EffectiveReasoningEfforts` 負責解析單一 model 實際接受的等級；migration V29/V60/V61 顯示 effort 等級已擴充到 `xhigh`、`max`、`ultra`，且部分 model 不服務高等級、請求即拒。

仍然成立的原則是：未實作 `CapabilityProvider` 的 Provider 預設全部不支援；Agent loop、模型 API 與設定 metadata 共用同一契約；不按 Provider 名稱特判。audio、batch 等仍未預建。

### 5.3 Skills 收口

DB 與 API 層已完成：

- global / project / workspace scope 與有效技能覆蓋順序（`workspace` 3 > `project` 2 > `global` 1，見 `skills_scopes_revisions.go:414-418`；workspace scope 以 `workline_id` 為鍵）；
- revision 快照、歷史列表（`ListSkillRevisionsPage`）、詳情與安全重掃記錄；
- 帶 optimistic-lock 的舊版本 restore（`RestoreSkillAs`，含 `SkillRestoreReviewRequiredError`：restore 到需要覆核的版本時要求重新確認風險）；
- scope 列表、revision 列表與 effective Skills 的 snapshot-stable cursor 分頁。

**修正誇大處**：原文說「Skills 已完成」而未區分層級。Settings scoped 面板目前只支援 scope 瀏覽、詳情、分頁、revision 歷史與 restore；**建立、SKILL.md 匯入、啟用/停用、編輯、刪除仍是 global-only**（CHANGELOG 明確記載）。舊報告把 scope、revision、restore、cursor 列為「目前不做」確實已失效，但反向也不能推成「scoped Skills 全功能完成」。

## 6. 驗收證據

**Go 端證據需要降級說明；前端證據本次已實際執行。**

Go 端：當前環境沒有 Go 工具鏈（`go` 不在 PATH，亦未找到安裝），`make check` / `go test -race` 均無法執行。`docs/CODE_REVIEW_2026-07-18.md` 受同一限制（該次審查明確記載「審查環境無 Go 1.26 工具鏈」）。因此所有 Go 端結論的性質是**靜態程式碼核對**——逐條比對 schema、SQL、型別、常數與 handler 實作——不是測試通過證明。

前端：Node **21.7.3 可用**，`node --test internal/server/static/modules/*.test.mjs` 本次實際執行兩輪——18:46 為 957 個測試 955 通過 2 失敗，18:53 重跑為 **957 個測試全數通過**。中間的差異來自工作樹擁有者同步修正了兩條過期斷言，非本文改動所致（見 §8.2）。前端證據因此是執行結果，不只是靜態核對。

原報告聲稱通過的項目（`make check`、`go test -race ./internal/agent ./internal/server`、Skills migration/revalidation/audit rollback/CAS conflict、HTTP summary/detail、Node load sequence 等）在測試檔層面確實存在對應覆蓋——`internal/db/` 下有 skills、runs、automation 等對應 `_test.go`，`internal/agent/hub_test.go` 覆蓋 replay 與 session，`internal/server/ws_test.go`、`stream_recovery_test.go` 覆蓋 protocol 與 resync payload——但「存在測試檔」不等於「本次執行通過」。

`scripts/check.sh` 的實際內容為：gofmt 檢查、`go mod tidy -diff`、`go test ./...`、`go vet ./...`、`go build ./...`、CLI entrypoint 建置，再加**原報告未提及的原始檔大小預算**（Go/mjs 1500 行、CSS 6000 行，含 `size_allowlist` grandfather 清單）。復現結論前需在有 Go 1.26 的環境重跑。

## 7. 最終判斷

這份報告的高收益程式碼建議已完成：protocol 2、有界記憶體 replay、五種具名 resync、Provider 能力契約（且已超出原範圍擴充到 reasoning 兩層），以及 scoped/revisioned Skills 與 snapshot cursor 都已落地。其後的 P2–P3 也已完成受限 schedules、durable deliveries、Telegram pairing/status/一次性 approval/deny、Home Assistant 受限動作與本地監控聚合。

必須繼續區分不同的「durable」：notification deliveries 與 Telegram channel events/cursors 已持久化，但 Agent WebSocket event stream 仍是進程內 ring，服務重啟或跨進程不能 replay。產品邊界同樣不可外推：`/task`、自由聊天、Slack/Discord、通用 IoT、攝像頭動作、門鎖解鎖與雲監控仍未實現（HA allowlist 有 `lock.lock` 但**刻意沒有 unlock**）。

~~**但「高收益建議已完成」不等於「無未決問題」。** 五天後的 `docs/CODE_REVIEW_2026-07-18.md` 列出 5 個應修 bug，其中至少兩個是真實可觸發的……~~

**這段已失效，且是本文最嚴重的錯誤。** 07-18 review 的 bug #1–#5 在當前工作樹**全部已修**，不是未決問題。逐條驗證見 §8.1。第一次修正之所以誤判，是因為只讀了 07-18 review 的內容而沒有回工作樹確認修復狀態——這正是它批評原報告犯的同一類錯。

真正仍然成立的收口邊界是**能力邊界**而非未修 bug：`/task`、自由聊天、Slack/Discord、通用 IoT、攝像頭動作、門鎖解鎖、雲監控、durable Agent event log 與跨進程 replay 仍未實現，且多數是刻意不做。

`docs/ARCHITECTURE.md` 的 catch-up 矛盾已於本次修正，不再是未決項。

## 8. 07-18 review 的實際修復狀態

### 8.1 五個 bug 全部已修

| # | 07-18 原始問題 | 當前狀態 | 證據 |
| --- | --- | --- | --- |
| 1 | bcrypt 上限矛盾：8–1024 bytes 密碼在 >72 bytes 時回 500 | ✅ 已修 | 採用 review 建議的第二方案：`hashUserPassword` 先 `sha256.Sum256` 再交 bcrypt，並以 `userPasswordHashV1 = "sha256-bcrypt-v1$"` 前綴版本化；`verifyUserPassword` 對舊格式保留無前綴的相容路徑，所以 1024 bytes 密碼不再觸發 `ErrPasswordTooLong`。`validUserPasswordLength` 維持 8–1024 是正確的，因為長度已與 bcrypt 的 72 bytes 限制解耦。 |
| 2 | 死程式碼 `runSingleSegmentLegacy`（`internal/agent/loop.go:1036`） | ✅ 已修 | 全 repo 零引用，且 `internal/agent/loop.go` **整個檔案已不存在**（該 package 已拆成 `runner.go`、`continuation.go`、`context_management.go` 等）。 |
| 3 | `newLocalToken()` 熵失敗 fail-open（退回時間戳） | ✅ 已修 | `internal/server/security.go:46-52` 現在 `rand.Read` 失敗即 `panic`，符合 review 建議與專案 fail-closed 原則，不再產生可預測 token。 |
| 4 | `PRAGMA foreign_keys = ON` 不保證跟隨重建連線 | ✅ 已修 | 採用 review 建議的 DSN 方案：`internal/db/store.go:38` 起以 `_pragma=foreign_keys(1)` 掛在連線字串上，每條連線都套用；review 順帶建議的 `busy_timeout(5000)` 也一併加了。`migrations.go:142` 的一次性 `ExecContext` 保留為 bootstrap 冗餘，不再是唯一保障。 |
| 5 | `hasRecursiveArgument` 誤判：`chmod 777 README` 被硬封鎖 | ✅ 已修 | `internal/tools/command_facts.go:1049` 現在只在 `strings.HasPrefix(value, "-")` 且非長選項時才檢查 `ContainsAny(value[1:], "Rr")`，另外精確比對 `--recursive`/`-R`/`-r`，並在 `--` 後停止掃描。`README` 不以 `-` 開頭，不再誤判。 |

review 的建議加強項也有進展：**#6（`/login` 無暴力破解防護）已實作**——`authLoginFailure` + `recordAuthLoginFailure` 提供 10 次 / 15 分鐘視窗 / 15 分鐘鎖定，key 為 `remoteAccessClientKey + canonical handle`，並有 2048 條上限防記憶體膨脹，正是 review 建議的「把同一套 lockout 套到 handle 維度」。**#8（Bash timeout 無上限）已修**：`bashMaxTimeout = 30 * time.Minute`，且 JSON schema 以 `maximum=1800000` 同步宣告。**#13（`internal/compat` 無測試）已修**：已有 `report_test.go`。**#7（WebSocket query token）已進入淘汰流程**：`security.go:250` 對 `?token=` 發出 legacy 警告並指向 cookie/header。

結論：07-18 review 的 bug 清單不應再與本報告的「已完成」清單並讀為未決風險。

### 8.2 前端測試：曾短暫 2 失敗，現已全綠

核對期間工作樹正被同步編輯，因此這一節記錄的是兩個時間點：

**18:46 的執行結果為 957 個測試、955 通過、2 失敗**，兩個失敗都在 `internal/server/static/modules/white-shell.test.mjs`，成因都是**未提交 WIP 中的過期測試斷言**而非產品缺陷：

- `desktop conversation layout follows the compact resizable geometry` 當時斷言 `.app-shell.nav-mode-icons` 下 `.sidebar` 與 `.sidebar-resize-handle` 合併在同一條規則裡 `display: none`。實際 CSS（`styles/extras.css:2851`、`:2857`）是兩條獨立規則，且 resize handle 刻意保持 `display: block` 並停在 icon rail 邊緣——原始碼註解寫明理由是「拖曳是離開這個 layout 的方式，不只是進入的方式」。實作是刻意的，斷言是舊的。
- `sidebar resizer restores, drags, keys, persists, and cleans up` 當時期望拖曳後寬度為 `400px`，實際得到 `332px`。`handlePointerMove` 已改成以 `navigationAreaLeft()`（global rail 左緣）量測，columns 模式再扣掉 `globalRailExpandedWidth`；測試 fake DOM 未提供 globalRail，`(globalRail || sidebar)` 回退到 sidebar 的 `left: 100`，於是 `500 - 100 - 68 = 332`。斷言與 fixture 都還是舊模型。

**18:53 重跑為 957 個測試、957 通過、0 失敗。** 工作樹擁有者已在 18:51 自行修正這兩條斷言：selector 斷言拆成兩條並改為期望 resize handle `display: block`（:1631-1633），寬度斷言改為 `332px`（:1376）。兩者都是把測試對齊到新的 nav-mode 幾何模型，方向與上述診斷一致。

因此結論是：**這 2 個失敗不構成未決問題**，但它們說明一件事——在工作樹持續變動時，測試結果必須連同時間戳一起記錄，否則下一位讀者無法判斷結論是否仍然有效。這也是本文第一次修正誤判 07-18 review 的同一類問題。

附帶觀察（非上述失敗的成因，仍然成立）：`navigationAreaLeft()` 的 `(globalRail || sidebar)` 回退在真實 DOM 缺少 globalRail 時，會讓 columns 模式多扣一次 rail 寬度。正常 DOM 一定有 globalRail，所以目前不可觸發，但這個回退與後續的固定扣減並不自洽，值得順手收斂。

### 建議後續動作

已完成而移除的原第 1、3 項不再列出。仍待處理：

1. 在有 Go 1.26 的環境重跑 `make check`，把 §6 的 Go 端從靜態核對升級為執行證據（前端已是執行證據）。
2. 若要讓 scoped Skills 名實相符，補齊 scoped 的建立/編輯/啟用/刪除 UI；否則在文件中固定標註 global-only 邊界。
3. 考慮把 `navigationAreaLeft()` 的 `(globalRail || sidebar)` 回退與後續的 rail 寬度扣減對齊，避免缺少 globalRail 時的靜默偏差；或在 fake DOM 補上 globalRail 讓測試覆蓋真實路徑。
4. ~~為 `docs/CODE_REVIEW_2026-07-18.md` 加上修復狀態標註~~ **已完成**：該文件開頭已加時效警告，並新增「修復狀態」表覆蓋 #1–#11 與 #13，避免後續讀者再把已修 bug 當成未決問題。

