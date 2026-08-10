# Autoto 代碼審查報告

審查對象：`autoto`（module `autoto`），本地優先的 coding-agent 伺服器
審查分支：`autoto/fork-of-main-cf8b973e`，HEAD `ca382eb`
審查日期：2026-08-08
審查方式：靜態閱讀 + 實際執行 build / vet / test；效能結論來自代碼路徑分析，非 profiling 實測

---

## 一、總評

**綜合評分：83 / 100（B+，良好且工程紀律紮實，瓶頸集中在少數熱路徑）**

| 維度 | 評分 | 一句話結論 |
|---|---|---|
| **代碼質量**（重點） | **87 / 100** | 函式粒度控制優異、慣例一致、註解解釋「為什麼」；扣分在 8 個超大檔案與前端重複實作分歧 |
| **效能**（重點） | **72 / 100** | 邊界與上限設計到位，但 SSE 廣播熱路徑在全域鎖內做 JSON 序列化，是最主要的擴展瓶頸 |
| 測試與驗證 | 88 / 100 | 測試/實作行數比 0.60，50 個套件全綠，CI 對 5 個核心套件跑 race detector |
| 架構與模組邊界 | 82 / 100 | 分層清楚、provider 以 adapter 隔離；`internal/server` 累積到 59k 行偏大 |
| 安全工程 | 90 / 100 | fail-closed、輸入普遍有大小上限、redaction 成體系、CI 權限最小化 |

一句話總結：這是一份**工程紀律明顯高於平均水準**的代碼庫，靜態品質指標（函式長度分佈、零 `SELECT *`、regexp 全預編譯、輸入全有界）幾乎都在良好區間，而且 build/vet/test 全綠。真正值得投入的是三個具體的效能熱點，修完即可把效能分推到 85 以上。

---

## 二、驗證基準（本次實際執行）

| 檢查項 | 指令 | 結果 |
|---|---|---|
| 編譯 | `go build ./...` | **通過**，零錯誤零警告 |
| 靜態檢查 | `go vet ./...` | **通過**，零告警 |
| 測試 | `go test ./...` | **50 套件通過，0 失敗**，1 套件無測試檔 |
| Go 版本 | `go version` | go1.26.5 windows/amd64 |

未能在本機執行的項目，已如實列在第七節。

---

## 三、規模指標

| 項目 | 數值 |
|---|---|
| Go 總行數 | 201,515 |
| ├ 實作 | 125,646 |
| └ 測試 | 75,869（**測試/實作 = 0.604**） |
| Go 檔案數 | 636（實作 347 / 測試 289） |
| 前端 MJS | 82,545 行 |
| 前端 CSS | 27,204 行 |
| **專案總計** | **約 311,000 行** |
| 函式總數 | 5,208 |
| 函式 > 120 行 | 35（**0.67%**） |
| 函式 > 200 行 | 7（0.13%） |
| DB 資料表 / 索引 | 56 / 200 |

最大套件：`internal/server` 59,303、`internal/db` 34,225、`internal/agent` 24,871、`internal/providers` 18,294、`internal/tools` 12,271。

---

## 四、效能發現（重點）

| ID | 位置 | 嚴重度 | 問題 |
|---|---|---|---|
| P1 | `internal/agent/hub.go:319,341,342,346` | **高** | SSE 廣播熱路徑在全域鎖內做 `json.Marshal`，且重複多次 |
| P2 | `internal/agent/hub.go:345-349` | **高** | ring buffer 逐出用 O(n) 記憶體移位，置於迴圈中最壞 O(n²) |
| P3 | `internal/db/live_snapshot.go:106-126` | **高** | N+1 查詢：取一頁訊息最壞觸發 101 次查詢，且在交易內 |
| P4 | `internal/db/store.go:70` | 中 | `SetMaxOpenConns(1)` 把讀取一併序列化，未發揮 WAL 並行讀能力 |
| P5 | 6 處 OAuth adapter | 低 | 每次 token refresh 新建 `http.Client`，失去連線池複用 |

### P1（高）Hub 廣播在鎖內做 JSON 序列化

`Publish` 是每個串流 token、每次工具活動都會走的最熱路徑。問題有三層疊加：

```go
// internal/agent/hub.go:317-322
func (h *Hub) Publish(event Event) {
	now := h.now()
	event = boundedHubEvent(event, h.config.MaxEventBytes)  // 第 1 次（鎖外，OK）
	h.mu.Lock()
	h.collectGarbageLocked(now)
```

```go
// internal/agent/hub.go:341-349（全部在 h.mu 持有期間）
	event = boundedHubEvent(event, h.config.MaxEventBytes)  // 第 2 次，鎖內
	eventBytes := hubEventSize(event)                       // 又一次 Marshal
	current.ring = append(current.ring, event)
	current.ringBytes += eventBytes
	for len(current.ring) > h.config.RingSize || current.ringBytes > h.config.RingBytes {
		current.ringBytes -= hubEventSize(current.ring[0])   // 每逐出一個再 Marshal 一次
```

而 `hubEventSize` 就是完整的 JSON 編碼：

```go
// internal/agent/hub.go:553-559
func hubEventSize(event Event) int {
	encoded, err := json.Marshal(event)
	if err != nil {
		return math.MaxInt
	}
	return len(encoded)
}
```

`boundedHubEvent` 內部本身還是階梯式裁切，最多呼叫 `hubEventSize` **四次**（`hub.go:376, 381, 386, 391`）。

實際代價：單次 `Publish` 在全域 `h.mu` 內可能發生 1 到 5 次以上的 `json.Marshal`，而這把鎖由**所有 agent 的所有訂閱者共用**。並行 agent 數與串流速率一上升，這裡就是序列化瓶頸。

建議修法（不改變語意）：
1. 讓 `boundedHubEvent` 回傳 `(Event, int)`，把已算出的編碼長度一併帶出，消除 `hub.go:342` 的重算。
2. 在 `stream.ring` 存 `struct{ event Event; size int }`，逐出時直接讀 `size`，消除 `hub.go:346` 的重算。
3. 第 341 行的第二次 bounding，起因是加入 `Protocol/StreamSession/Sequence/CreatedAt` 後長度會變。這幾個欄位長度可預估，改為「僅在 `已知長度 + 欄位增量 > maximum` 時才重新 bound」，把常態路徑降到零次額外 Marshal。

### P2（高）ring buffer 的 O(n²) 移位

```go
// internal/agent/hub.go:345-349
	for len(current.ring) > h.config.RingSize || current.ringBytes > h.config.RingBytes {
		current.ringBytes -= hubEventSize(current.ring[0])
		copy(current.ring, current.ring[1:])              // O(n) 移位
		current.ring = current.ring[:len(current.ring)-1]
	}
```

`copy(current.ring, current.ring[1:])` 每次搬移整個切片。當一個大事件迫使連續逐出多個舊事件時，迴圈把 O(n) 疊成 O(n²)，同樣在鎖內。

建議：改為真正的環形緩衝（head/tail 索引），或先算出需逐出的數量 `k` 再一次 `current.ring = current.ring[k:]`，把逐出攤平成 O(1) 均攤。

### P3（高）取訊息頁的 N+1 查詢

```go
// internal/db/live_snapshot.go:106-107
	for i := range snapshot.Messages {
		attachmentRows, err := tx.QueryContext(ctx, `SELECT id, message_id, agent_id, filename, COALESCE(mime_type,''), kind, size_bytes, created_at FROM agent_message_attachments WHERE message_id = ? ORDER BY created_at ASC`, snapshot.Messages[i].ID)
```

頁大小為 100（`internal/db/types.go:8`，`DefaultMessagePageLimit = 100`），所以載入一頁訊息最壞是 **1 + 100 次查詢**。三個因素放大它：查詢在交易內、整個 DB 只有一條連線（見 P4）、每次都要重新解析與規劃語句。

建議：改為單次 `WHERE message_id IN (?,?,...)` 批次查詢，在 Go 端依 `message_id` 分組回填。同檔第 128 行的 tool call 查詢已經是這種按 `agent_id` 一次撈取的寫法，照它的模式改即可，一致性也更好。

### P4（中）單一連線把讀取一併序列化

```go
// internal/db/store.go:70
	database.SetMaxOpenConns(1)
```

DSN 設定本身是深思熟慮的，WAL 與 `synchronous(NORMAL)` 甚至附了六行註解說明取捨（`store.go:40-48`）。但 `SetMaxOpenConns(1)` 這行**沒有任何註解**，且它抵銷了 WAL 最大的好處：WAL 允許「多讀者 + 單寫者」並行，限制成一條連線後，讀取也必須排隊在寫入之後。

這對一個要同時服務 SSE 串流、UI 輪詢與多個並行 agent 的伺服器是實質限制。考量 `modernc.org/sqlite` 是純 Go 實作，選單連線來規避 `SQLITE_BUSY` 複雜度是保守但可理解的決定——問題在於這個權衡沒有被記錄下來。

建議：改為「單寫者連線 + 多讀者連線池」雙 handle，讀路徑走唯讀池。若決定維持現狀，至少補上與相鄰 WAL 註解同等水準的理由說明。

### P5（低）OAuth refresh 每次新建 HTTP client

`internal/anthropicauth/oauth.go:253`、`internal/codexauth/oauth.go:266`、`internal/geminiauth/auth.go:597`、`internal/grokauth/grok.go:476`、`internal/kimiauth/kimi.go:442`、`internal/kiroauth/kiro.go:229` 六處都是 `client := &http.Client{Timeout: 30 * time.Second}`，每次呼叫新建，連線池與 TLS session 無法複用。

token refresh 頻率低，實際影響小，列此僅為一致性：`internal/network/transport.go:145` 與 `internal/network/provider.go:112` 已經有共用的 client 建構路徑，這六處可以收斂過去。

---

## 五、代碼質量發現（重點）

| ID | 位置 | 嚴重度 | 問題 |
|---|---|---|---|
| Q1 | `internal/server/static/modules/dom.mjs:3` 等 4 處 | 中 | `escapeHtml` 有 4 份重複實作且轉義字元集不一致 |
| Q2 | `scripts/check.sh` size_allowlist | 中 | 8 個檔案豁免 1500 行預算，最大 2151 行 |
| Q3 | `internal/server`（59,303 行） | 中 | 單一套件承載過多職責 |
| Q4 | `internal/server/provider_auth.go:1129` | 低 | `io.ReadAll` 無上限（但目前不可達） |
| Q5 | `internal/db/store.go:70` | 低 | 關鍵效能決策缺註解（與相鄰註解密度落差明顯） |

### Q1（中）escapeHtml 四份實作，行為分歧

共用版本不轉義單引號：

```js
// internal/server/static/modules/dom.mjs:3-9
export function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"]/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[ch]));
}

export function escapeAttr(value) {
  return escapeHtml(value).replace(/'/g, "&#39;");
}
```

局部版本卻轉義了五個字元，還多處理反引號：

```js
// internal/server/static/modules/lifecycle-hooks.mjs:23-24
function escapeHtml(value) { return String(value ?? "").replace(/[&<>"']/g, (char) => ({ ... }[char])); }
function escapeAttr(value) { return escapeHtml(value).replace(/`/g, "&#96;"); }
```

另外 `optional-tools-settings.mjs:5` 與 `overview-dashboard.mjs:125` 各有第三、第四份。

**目前沒有實際 XSS 曝險**：我掃過全部非測試 `.mjs`，用單引號界定屬性再塞 `escapeHtml` 的組合是 **0 處**，屬性一律用雙引號，而雙引號已被轉義。所以這是等待被踩的陷阱，不是現行漏洞。

值得肯定的是，65 處 `innerHTML =` 賦值我抽驗的每一處都套了 `escapeHtml`/`escapeAttr`，另有 136 處直接用 `textContent`——這個習慣本身是對的，問題只在實作沒有單一來源。

建議：全部收斂到 `dom.mjs` 單一實作，並取兩者的聯集（含 `'` 與 `` ` ``），再補一個測試斷言轉義字元集。

### Q2（中）超大檔案豁免清單

`scripts/check.sh` 強制實作檔 1500 行上限，但有 8 個檔案被豁免：

| 檔案 | 行數 |
|---|---|
| `internal/db/automation_p2p3.go` | 2,151 |
| `internal/db/migrations.go` | 2,076 |
| `internal/agent/context_ask.go` | 1,674 |
| `internal/server/agent.go` | 1,752 |
| `internal/server/provider_config.go` | 1,687 |
| `internal/server/static/modules/app-main.mjs` | 4,174 |
| `internal/server/static/modules/chat-rendering.mjs` | 4,252 |
| `internal/server/static/modules/provider-console.mjs` | 2,452 |

有預算機制本身是加分項，而且腳本註解明確寫了「shrink that list as they are split」。`migrations.go` 屬於天然追加型檔案，可以接受；`automation_p2p3.go` 這個名字本身就透露它是按專案階段而非領域邊界切分的，優先拆它。

### Q3（中）internal/server 套件過大

59,303 行、佔全部 Go 代碼 29%，涵蓋 HTTP 路由、provider 設定、認證、安全、檔案系統、通知、隧道等。`Routes()` 單一函式 339 行（`internal/server/server.go`，全庫最長函式）。

路由表本質是宣告式的，339 行可接受；真正的問題是套件層級的職責聚集。建議按領域切出 `server/providerapi`、`server/authapi`、`server/fsapi` 等子套件，同時也能讓編譯與測試更好並行。

### Q4（低）無上限讀取上游回應

```go
// internal/server/provider_auth.go:1129
	data, err := io.ReadAll(res.Body)
```

同一檔案第 548 行是 `io.ReadAll(io.LimitReader(r.Body, maxProviderAuthImportBytes+1))`，可見專案慣例是有上限的，這裡是漏網。

**嚴重度下調的原因**：其呼叫鏈 `Server.cliProxyAPIManagementRequest`、`cliProxyAPIManagementKey` 等都列在 `scripts/deadcode.sh` 的 allowlist 中，意即目前不可達。修掉成本極低，建議順手補 `io.LimitReader`，避免它日後被接回可達路徑時變成記憶體耗盡向量。

### Q5（低）關鍵決策缺註解

見 P4。這條單獨列出是因為它反映的是註解**分佈不均**而非缺乏註解：`store.go:40-48` 為 WAL 取捨寫了六行紮實說明，緊接著第 70 行影響更大的併發決策卻一字未提。

---

## 六、值得肯定的工程實踐

這些不是泛泛的稱讚，每一條都有實證：

**1. 輸入邊界普遍收斂。** 全庫 `io.ReadAll` 幾乎無一例外套了 `io.LimitReader` 或 `http.MaxBytesReader`，含 `+1` 溢出偵測慣例（如 `internal/tools/read.go:69`、`internal/workspacefs/workspacefs.go:213`）。唯一例外在不可達代碼中。這是很難靠自覺維持的一致性。

**2. 零 `SELECT *`。** 56 張表、200 個索引的資料層，全庫查詢一律列明欄位，並大量使用 `COALESCE` 處理 nullable。這讓 schema 演進不會靜默破壞掃描順序。

**3. Regexp 全部預編譯。** 60 餘個 `regexp.MustCompile` 全在套件層級 `var` 區塊，函式體內零編譯。熱路徑上的 redaction pattern（`internal/agent/context_ask.go:50-79`、`internal/agent/runner_events.go:18-20`）都是如此。

**4. Hub 廣播不會被慢訂閱者拖垮。**

```go
// internal/agent/hub.go:354-359
	for sub := range current.subscribers {
		select {
		case sub.events <- event:
		default:
			h.resyncSubscriberLocked(current, sub, ResyncSubscriberOverrun)
		}
	}
```

非阻塞送出 + 溢位轉 resync，是串流系統的正確設計。搭配 ring buffer 提供的重播能力，斷線續傳語意完整。

**5. deadcode 檢查有嚴格 allowlist，而且踩過的坑都留了註解。** `scripts/deadcode.sh` 維護 60 餘條精確例外（含來源檔名，避免同名符號互相遮蔽），並且註解記錄了兩個真實踩雷：`comm` 在非 C locale 下會誤判排序（明確提到 `zh_TW.UTF-8`），以及 Windows 反斜線路徑導致整份 allowlist 失配。這種「把失敗原因寫進代碼」的習慣品質很高。

**6. 檔案大小預算機制化。** 見 Q2。有預算、有豁免清單、有「應逐步縮短此清單」的明文要求，比單純的 code review 口頭約定可靠。

**7. CI 覆蓋到位。** `lint`（golangci-lint：errcheck/govet/ineffassign/misspell/staticcheck/unused）+ 完整 `scripts/check.sh`（gofmt、mod tidy、test、vet、build、size budget、deadcode、Node 語法檢查、Node 測試）+ 對 `agent`/`server`/`background`/`db`/`tools` 五個核心套件跑 `-race`，並註明主套件不帶 race 的原因。權限宣告最小化（`contents: read`）。

**8. 函式粒度控制優異。** 5,208 個函式僅 0.67% 超過 120 行。在 20 萬行規模的專案裡，這個分佈相當難得。

**9. Migration 有版本化測試。** 測試中大量出現 `PRAGMA user_version = N` 的舊版 DB 種子（涵蓋 v1 到 v52），代表升級路徑是逐版驗證的，不是只測最新 schema。

**10. 註解解釋「為什麼」而非「做什麼」。** 如 `internal/agent/runner_model.go:389-391` 說明重試上限為何從 2s 提到 30s（provider 限流常需數十秒），`internal/db/store.go:29-31` 說明 Windows 檔案 URI 的 `/C:/` 前綴為何必要。

---

## 七、本次未能驗證的項目

如實列出，避免把未驗證當成已驗證：

- **race detector 未執行。** 本機無 cgo 環境（`-race requires cgo`，缺 C 編譯器），無法本地驗證資料競爭。CI 在 Linux 上對 5 個核心套件有覆蓋，但我未親自確認其歷史結果。
- **無 profiling 實測。** 第四節的效能結論來自代碼路徑分析與複雜度推導，未做 benchmark 或 pprof。P1/P2 的實際影響幅度取決於事件速率與並行 agent 數，建議補 `BenchmarkHubPublish` 量化後再排優先序。
- **golangci-lint 未本地執行**（未安裝）。CI 有跑。
- **前端 82,545 行 MJS 未深入審查。** 僅做了 `innerHTML`/`escapeHtml` 的針對性掃描與檔案規模統計。
- **`internal/server` 59k 行為抽樣審查**，非逐行。原計畫的並行子代理審查未能完成，該部分深度低於 `internal/db` 與 `internal/agent`。
- **索引覆蓋率未逐條交叉比對。** 已確認 200 個索引對 56 張表的量級合理，但未把每個 `WHERE`/`ORDER BY` 謂詞逐一對應到索引。

---

## 八、建議的修復順序

依「影響 ÷ 成本」排序：

| 順序 | 項目 | 預估成本 | 預期收益 |
|---|---|---|---|
| 1 | P3 修 N+1（改批次 `IN` 查詢） | 小（單一函式，同檔已有範本） | 訊息頁載入從 101 次查詢降到 2 次 |
| 2 | P1 消除鎖內重複 Marshal（帶出已算長度、ring 存 size） | 中 | 直接降低 SSE 熱路徑的鎖持有時間 |
| 3 | P2 ring 逐出改 O(1) 均攤 | 小 | 消除 O(n²) 最壞情況 |
| 4 | Q1 收斂 escapeHtml 到單一實作 | 小 | 消除等待被踩的 XSS 陷阱 |
| 5 | Q4 補 `io.LimitReader` + Q5 補註解 | 極小 | 一致性 |
| 6 | P4 評估讀寫分離連線池 | 中大（需併發測試） | 解放 WAL 並行讀能力 |
| 7 | Q2/Q3 拆分超大檔案與套件 | 大（持續進行） | 長期可維護性 |

前五項合計約可在一天內完成，且都是低風險的局部修改。

---

## 九、結論

Autoto 的代碼質量處在**良好偏優**的水準：靜態指標幾乎全面健康，安全預設是 fail-closed，工程流程（size budget、deadcode allowlist、versioned migration test、race CI）比多數同規模專案更成體系，而且註解習慣是解釋權衡而非複述代碼。build、vet、50 個測試套件在本次審查中全部通過。

效能是相對弱項，但**弱在集中而非瀰漫**——三個高嚴重度發現全部指向兩個檔案（`internal/agent/hub.go`、`internal/db/live_snapshot.go`），且修法明確、風險低。這比問題散佈全庫的情況好處理得多。

最需要留意的長期趨勢是 `internal/server` 的體積累積與 8 個檔案的預算豁免。機制已經就位並且明文要求收斂，關鍵是別讓豁免清單繼續變長。

