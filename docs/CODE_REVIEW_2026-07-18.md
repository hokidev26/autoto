# Autoto Code Review（2026-07-18）

範圍：`cmd/` + `internal/` + 前端 `internal/server/static/`（排除 `autoto-legacy/`、`autoto-references/`）。約 284 個 Go 檔、10.7 萬行。

驗證方式：靜態審查 + `node --check` / `node --test`（355 個前端測試全數通過）。審查環境無 Go 1.26 工具鏈，未能執行 `go build/vet/test`，Go 端結論以人工閱讀為準。

> 本文是 2026-07-18 的快照。修復狀態請看下方「修復狀態」表，不要直接把本文的 bug 清單當成當前未決問題。

## 總評

整體品質相當高：SSRF 防護（DNS pin 到已驗證 IP、metadata 封鎖、redirect 重驗證）、origin/Sec-Fetch-Site 檢查、approval generation 失效機制、git `--` 分隔、輸出全面設限、secret 不入錯誤訊息、CI + golangci-lint + goreleaser 都到位。以下是可以加強的地方，依嚴重度排序。

## 修復狀態（2026-08-05 對照工作樹複核）

本節後加，避免讀者把已修項目當成未決風險。逐條證據見 `docs/notes/feedback-changelog-lessons-0712-revised.md` §8.1。

| # | 項目 | 狀態 |
| --- | --- | --- |
| 1 | bcrypt 密碼長度上限矛盾 | ✅ 已修（SHA-256 pre-hash + `sha256-bcrypt-v1$` 版本前綴） |
| 2 | 死程式碼 `runSingleSegmentLegacy` | ✅ 已修（`internal/agent/loop.go` 整檔已拆分移除） |
| 3 | `newLocalToken()` 熵失敗 fail-open | ✅ 已修（改為 panic） |
| 4 | `PRAGMA foreign_keys` 不保證跟隨連線 | ✅ 已修（DSN `_pragma=foreign_keys(1)` + `busy_timeout(5000)`） |
| 5 | `hasRecursiveArgument` 誤判 | ✅ 已修（只檢查以 `-` 開頭的短選項，並在 `--` 後停止） |
| 6 | `/login` 無暴力破解防護 | ✅ 已實作（handle 維度 10 次 / 15 分鐘 lockout） |
| 7 | WebSocket token 走 query string | ⚠️ 淘汰中（`?token=` 會發出 legacy 警告並指向 cookie/header） |
| 8 | Bash timeout 無上限 | ✅ 已修（`bashMaxTimeout` 30 分鐘，schema 同步宣告上限） |
| 9 | 動態命令 `dynamic`/`ParseKnown=false` 的審批可見性 | ✅ 已修（`chat-rendering.mjs:739` 以 `role="alert"` 顯示 `activity.unclassifiedDynamicWarning`，走 i18n key 而非硬編碼字串，並有測試覆蓋） |
| 10 | 核心層硬編碼簡體中文字串 | 🔄 部分（`bash.go`、`command_facts.go` 現已無 CJK 字元；`internal/agent/runner_context.go` 仍有 prompt 導向的 zh-Hans 字串如「图片附件…未发送」。後者只進入送往 Provider 的 prompt，不是使用者介面文案，嚴重度低於原始描述） |
| 11 | `internal/server` god package | 🔄 部分（`internal/db/db.go` 已按主題拆分並不再存在） |
| 13 | `internal/compat` 無測試 | ✅ 已修（`report_test.go`） |

## 應修正（bug）

### 1. bcrypt 密碼長度上限矛盾 — 註冊 73–1024 bytes 密碼會回 500
`internal/server/auth.go:34` 允許密碼 8–1024 bytes，但 `bcrypt.GenerateFromPassword`（x/crypto v0.41）對 >72 bytes 回傳 `ErrPasswordTooLong`，於 `auth.go:47` 變成 500 而非 400。
建議：上限改 72，或先 SHA-256 + base64 再交給 bcrypt（並於 login 同步處理）。

### 2. 死程式碼：`runSingleSegmentLegacy`
`internal/agent/loop.go:1036` 起約 160 行，全 repo 零引用（`run()` 已固定走 `runContinuous`）。golangci 的 `unused` 不會抓未使用的 method，所以 CI 沒報。建議刪除。

### 3. `newLocalToken()` 熵失敗時 fail-open
`internal/server/security.go:42-48`：`rand.Read` 失敗時退回 base64(時間戳)——可預測的管理 token。Go 1.24+ 的 `crypto/rand.Read` 實際上不會回錯（內部 panic），此分支不可達，但與專案「fail-closed」原則矛盾，留著就是隱患。建議改成 panic。

### 4. `PRAGMA foreign_keys = ON` 不保證跟著連線
`internal/db/migrations.go:65` 用一次性 `ExecContext` 設定；database/sql 若因壞連線重建 connection，新連線會回到 SQLite 預設 OFF。目前 `SetMaxOpenConns(1)`（`db.go:634`）通常沒事，但非保證。
建議：改用 DSN 參數（modernc 支援 `?_pragma=foreign_keys(1)`），順便考慮 `busy_timeout`。

### 5. `hasRecursiveArgument` 誤判導致誤封鎖
`internal/tools/command_facts.go:665`：`strings.Contains(value, "R")` 會把任何含大寫 R 的引數當成遞迴旗標。例如 `chmod 777 README` 會被判成「遞迴 chmod 777」→ RiskDanger → 不可覆核的硬封鎖。建議只比對以 `-` 開頭的旗標。

## 建議加強（設計/防護）

### 6. `/login` 沒有暴力破解防護
遠端存取密碼有 lockout（10 次/15 分鐘，`security.go:31-33`），但帳號 login 只靠 bcrypt 天然慢。建議把同一套 failure-lockout 套到 handle 維度。

### 7. WebSocket token 走 query string
`security.go:154`（`?token=`）容易經 log/proxy 外洩。已支援 header，建議逐步淘汰 query 參數或改一次性 ticket。

### 8. Bash timeout 無上限
`internal/tools/bash.go:135`：模型可傳任意大的 timeout，僅靠 run 取消兜底。建議加硬上限（如 30 分鐘）。

### 9. 動態命令繞過 RiskDanger 的可見性
`x=rm; $x -rf /`、`python -c ...` 等會落到 `Program: "dynamic"` / `ParseKnown: false`，只走一般 RiskExec 審批（設計上的兜底沒錯）。建議在審批 UI 對 `dynamic`/`ParseKnown=false` 顯著標示「無法分類的命令」，讓人審時有感。

### 10. 核心層硬編碼簡體中文字串
`bash.go` / `command_facts.go` 的危險命令警告、`loop.go:1716-1748` 的 context 摘要（「已执行」）、`loop.go:732` 的「用户参数：」。文件與 UI 是英文/多語（前端已有 `i18n.mjs` + locale registry），但這些伺服器端字串固定 zh-Hans，繁中使用者也會看到簡體。建議集中成 key 由前端翻譯，或改中性英文。

## 結構與維護性

### 11. `internal/server` 逐漸變成 god package
35K 行、60+ 檔案，git、remote access、provider admin、review、automation、oauth 全在同一 package，邊界只靠檔名。`internal/db/db.go` 也有 4.9K 行。建議按 AGENTS.md 的邊界精神逐步拆子package。

> 2026-08-05 補註：`internal/db/db.go` 已按主題拆分並不再存在（Run 轉換現於 `internal/db/runs_checkpoints.go`，Skills 於 `skills_store.go` / `skills_scopes_revisions.go`）。`internal/server` 的拆分尚未完成。

### 12. 規劃文件已歸檔
規劃文件已移至 `docs/notes/needtodo0709.md`、`docs/notes/needtodo0712.md`、`docs/notes/feedback-changelog-lessons-0712.md`。

### 13. 測試
116 個 Go 測試檔 + 355 個 JS 測試，覆蓋面好。唯 `internal/compat`（76 行）無測試——小，順手補即可。

## 做得好的地方（保持）

`internal/network` 的 transport/policy 是教科書等級的 SSRF 防護；`security.go` 的 forwarded-header/DNS-rebinding 處理極少見地完整；approval 的 generation 失效與 session grant 綁定嚴謹；`resolveInCWD` 對不存在路徑的 symlink 解析正確；`decodeJSON` 有 1MB 限制 + DisallowUnknownFields；gateway key 以 hash 查表天然免時序攻擊。
