# 外部更新日誌 × Autoto 可借鑒點

> 狀態：對照分析（尚未逐項落地）  
> 撰寫目的：給審閱者快速理解「別人修了什麼、我們能學什麼、優先做哪幾件」  
> 輸入：某相似產品近期 changelog（含 v0.5.21 前後的新功能 / 修復 / 重構 / 安全 / 性能條目）  
> 方法：對照 Autoto 當前代碼與模組邊界（`codeharbor/` 工作樹），標註 already-good / gap / 可學原則  
> 相關文件：`docs/notes/feedback-changelog-lessons-0712.md`、`docs/ARCHITECTURE.md`、`CONTRIBUTING.md`

---

## 0. 給審閱者的三句話

1. **這份 changelog 的精華不是功能清單**，而是一組可複用的工程原則：可快取的必須穩定、可診斷的必須真實且無密鑰、可失敗的必須區分並 fallback、可擴展的必須 enforce 聲明、可並發的必須共享與退避、可更新的不能堵服務、可測試的不能髒全局。
2. **Autoto 並非從零開始**：WebSearch 已區分「解析失敗 ≠ 無結果」、danger reflection 只能降級、network diagnostics 刻意不回傳憑證、Anthropic 已有 5m prompt cache——方向與對方一致。
3. **最值得補的不是主題插件或 22 區 CSS**，而是：cache 前綴工程化、診斷/指紋一致性、測試全局防洩漏硬門檻，以及搜索 fallback / 工具按可用性暴露。

---

## 1. 背景與範圍

### 1.1 外部 changelog 在談什麼（摘要）

對方近期大致覆蓋：

| 類別 | 代表條目 |
| --- | --- |
| 新功能 | 插件貢獻網絡搜索通道；主題擴展（多區域 / 九宮格邊框 / 明暗變體）；插件提供商接收宿主代理提示；官方 Claude 默認原生搜索；Codex 403 一鍵關圖片並重試；OAuth/External API 分層讀消息 |
| 修復 | 中轉站 prompt cache 只寫不讀；原生搜索改獨立副請求以免丟 cache breakpoint；Codex 多傳輸路徑指紋不一致；WebSocket 診斷寫入 Authorization；插件搜索通道重啟被丟棄並重開；插件輸出上限未生效；Codex 思考 summary 字段缺失；搜索失敗被報成「無結果」；無搜索通道仍暴露 WebSearch；更新校驗堵請求路徑；更新/版本接口無認證；測試全局狀態洩漏 |
| 重構 | Codex 請求頭改隨 API 模式變化；移除無效的插件信任等級 |
| 性能 | 消息列表折疊用合成層動畫；敘述者先用緩存繪製；模型刷新 singleflight + 無響應退避 |
| 安全 | 請求轉儲記真實頭並對憑證脫敏；User-Agent 長度上限 |

### 1.2 本文件怎麼用

- **給產品 / 負責人**：看 §0、§2 優先級表、§6 建議 backlog。  
- **給工程**：看 §3–§5 的原則、現狀對照、落地建議。  
- **不在範圍**：逐條抄對方功能；未經驗證的「對方一定比我們好」；具體 PR 實作細節（需另開任務）。

### 1.3 Autoto 對照時看過的主要位置

| 區域 | 路徑（示意） | 與本議題的關係 |
| --- | --- | --- |
| Anthropic / cache | `internal/providers/anthropic_provider.go` | 已有 `cache_control` 5m、讀取 `CacheReadInputTokens` |
| Codex | `internal/providers/codex.go` | Authorization / User-Agent、密鑰 redact |
| 網絡 | `internal/network/` | policy、transport、diagnostics（粗粒度 status class） |
| WebSearch | `internal/tools/websearch.go` | 已區分 parser failure / 無法識別頁 / 真無結果 |
| Danger reflection | `internal/agent/danger_reflection.go` | 只能降級、超時、緩存、結構化 verdict |
| 插件 | `internal/plugins/` | manifest / 工具目錄早期形態 |
| 更新 | `internal/update/` | stage 時哈希、size cap |
| 測試習慣 | 多處 `*_test.go` | 已大量使用 `t.Cleanup` / `t.Setenv` |

---

## 2. 優先級總表（建議怎麼排）

| 優先級 | 主題 | 對 Autoto 的意義 | 粗判 |
| --- | --- | --- | --- |
| ★★★ | Prompt cache 前綴穩定 + 抓包校正 | 長會話 / 中轉計費；已有 cache 基礎 | **高收益 gap** |
| ★★★ | 診斷記真實請求 + 憑證脫敏 | Codex 等多路徑；避免 token 落盤 | **部分已有，需統一** |
| ★★★ | 測試全局狀態洩漏防範 | 大測試套件長期穩定 | **習慣已有，缺硬門檻** |
| ★★ | 搜索失敗 vs 無結果 + fallback 鏈 | 工具正確性與模型行為 | **語義已部分做對** |
| ★★ | 按「通道是否真可用」暴露工具 | 避免廣告用不了的 WebSearch | **可補** |
| ★★ | 熱路徑不做重同步 I/O（更新校驗） | 更新檢查不拖垮 API/WS | **需確認 handler 路徑** |
| ★★ | 反思失敗可觀測、不掛死、防死鎖 | danger reflection 狀態機 | **設計好，需對照自檢** |
| ★ | 宿主代理提示傳給插件/子進程 | 僅代理環境常見 | **可學** |
| ★ | 插件用戶偏好持久化 | 重啟不丟「已關閉的通道」 | **插件早期即可定原則** |
| △ | 主題多區域 / 九宮格 / 示例主題 | 擴展點方法可學，不必照抄規模 | **低優先** |
| △ | OAuth / External API 分層讀消息 | 有外部只讀消費方再做 | **待需求** |

---

## 3. 最該學的修復（建議當成設計原則）

### 3.1 Prompt cache：前綴必須可復用，不能每輪摻變數

**對方問題**

- 官方 Claude 路徑在緩存前綴內塞入**每輪都變的證明值** → 緩存只寫入、從不命中讀取。  
- 長會話每輪按**全量 input**計費。  
- 在主請求上聲明服務端搜索工具，會讓中轉站**丟掉 cache breakpoint**。

**對方做法（可抽象的原則）**

1. 依**真實抓包**校正請求字段，而不是只信 SDK 默認序列化。  
2. 把會破壞前綴的能力（例如原生 web search）拆成**獨立一次性副請求**。  
3. 主對話請求保持穩定前綴，才能讓中轉 / 官方 cache 真正 read。

**Autoto 現狀**

- 已有 Anthropic 5m ephemeral `cache_control`，並處理 `cache_read_input_tokens`。  
- 若流量大量走 CLIProxy / 中轉 Anthropic，**前綴是否被動態字段污染**需要專門審計——這比「再開一次 cache」更重要。

**建議落地**

| 動作 | 說明 |
| --- | --- |
| 緩存前綴白名單審計 | system / tools / 穩定消息中禁止 nonce、時間戳、每輪證明、請求 id、動態 tool 聲明 |
| 主請求與副作用請求分離 | 原生搜索、一次性服務端 tool 等不掛在帶 breakpoint 的主請求上 |
| 中轉兼容回歸 | 用抓包或 golden request 快照鎖字段；SDK 升級要重跑 |
| 可觀測性 | 日誌或 usage UI 區分 cache read vs write，否則「修好了」用戶無感 |

**審查提問（七問精簡版）**

1. 前綴裡有沒有每輪必變字段？  
2. tools 列表是否在「無搜索 / 有搜索」之間抖動？  
3. 中轉是否要求特定字段順序或省略空字段？  
4. 是否只看了 write 標記、沒驗證 read 命中？  
5. 長會話第 N 輪 input tokens 是否仍接近全量？

---

### 3.2 診斷與請求轉儲：真實，但無密鑰

**對方問題**

- WebSocket 診斷記錄過**硬編碼佔位**，與實際發送不符。  
- 轉儲原樣寫入 `Authorization` → **OAuth token 進入持久化診斷**。  
- 多條傳輸路徑（HTTP 聊天 / Responses WebSocket / 一次性工具調用）客戶端指紋不一致。

**對方後來的方向**

- 記錄**實際發送**的請求頭；  
- 對憑證**脫敏**；  
- User-Agent 等增加**長度上限**；  
- 指紋隨 **API 模式**變化，而不是一堆獨立布爾開關自由組合（避免造出真實客戶端從不發送的頭組合）。

**Autoto 現狀**

- Codex 路徑有 `Authorization` / `User-Agent` 設置，錯誤信息有 `redactCodexSecrets` 一類處理。  
- `internal/network/diagnostics` 刻意只暴露粗粒度 status class，不回傳 URL / 代理地址 / 憑證——**安全默認值正確**。  
- 風險在於：若未來加強「請求轉儲 / 排障包」，容易在「更好調試」時退回明文 header。

**建議原則（可寫進貢獻規範）**

> 任何 `DumpRequest` / trace / 網絡診斷落盤前，必須經過統一的 `RedactHTTPHeaders`（或等價層）。  
> 多傳輸路徑共用同一套 header 構造；禁止「HTTP 一套、WS 一套」。  
> 診斷要麼記真實（已脫敏），要麼不記；**假佔位數據比沒有更糟**。

**建議落地**

1. 抽出 `codex`（及類似仿官方客戶端）的 `mode → headers` 純函數 + 單測快照。  
2. 審計所有寫入磁盤的 request dump。  
3. 對外可控字符串（UA 等）設最大長度。

---

### 3.3 測試：全局狀態洩漏要防得住，最好寫不出來

**對方問題**

- 兩個測試套件洩漏進程級全局狀態，導致約 20 個無關測試失敗。  
- 強化後：註冊全局變量或模塊 mock **卻不登記清理會直接編譯失敗**。

**Autoto 現狀**

- 已廣泛使用 `t.Cleanup`、`t.Setenv`（Go 會自動恢復 env），基礎紀律好於許多項目。  
- 仍可能存在：改 `http.DefaultClient` / `DefaultTransport`、包級 `var`、未還原的 mock 時鐘/隨機源等。

**建議落地（Go 版）**

| 做法 | 目的 |
| --- | --- |
| `testkit.OverrideGlobal(t, &pkg.X, v)` 內部強制 `t.Cleanup` | 統一入口，避免漏還原 |
| 禁止測試直接碰 `DefaultClient` / `DefaultTransport` | 減少串測 |
| CI 全量 package 測試 + `-count=1` / 並行 | 抓隱藏耦合 |
| （可選）lint 或 API 設計讓「無 cleanup 的 override」難以通過 | 對齊對方「編譯期失敗」精神 |

---

## 4. 與現有模組高度對齊的優化

### 4.1 搜索：失敗要報錯並 fallback，絕不能裝成「無結果」

**對方**

- 搜索中途失敗 → 正確報錯 → 順延下一個 fallback。  
- 不再向「所在供應商沒有可用搜索通道」的模型提供 WebSearch。  
- 插件貢獻的搜索通道與內置同等進入路由。  
- 修復：插件通道每次重啟被靜默丟棄，還把用戶關掉的通道重新打開並挪到 fallback 末尾。

**Autoto 已做對的部分**（`WebSearchTool`）

- 區分 parser failure、無法識別結果頁（風控/挑戰頁）、以及真的零命中。  
- 明確提示模型：**這不是「沒搜到」**。

**還可加強**

1. **多通道 fallback 鏈**（例如內置 HTML 搜索 → 插件通道 → 官方 native search）。  
2. **工具目錄按運行時可用性裁剪**：當前 provider / 網絡策略下無可用 search → 不註冊、不廣告 WebSearch。  
3. 插件用戶開關與順序 **持久化**，重啟不得靜默重置。

### 4.2 擴展點聲明的上限必須強制執行

**對方**：插件聲明的輸出大小上限從未生效，實際拿到完整宿主配額。

**原則**

> 擴展點若聲明 `maxOutput` / timeout / risk，**執行路徑必須 enforce**；測試覆蓋「聲明 1KB 絕不能返回 1MB」。

**Autoto 適用面**：插件 tool、MCP、WebSearch/WebFetch 截斷、hooks 輸出——適合做一次「聲明 vs 實際」審計。

### 4.3 代理環境：宿主把 proxy 決議傳下去

**對方**：插件提供商收到宿主代理提示，僅代理環境不再直接失敗。

**Autoto**：`network` 已有 policy / transport / diagnostics 的代理相關思維。

**建議**

- 子進程插件、外部 SDK 繼承同一套 proxy 決議。  
- 錯誤文案區分 `policy_denied` / `proxy_error` / `timeout`（可複用 diagnostics 的 status class 思路）。

### 4.4 Danger / 反思路徑：失敗可見、不永久掛起、防死鎖

**對方 v0.5.21 附近**

- danger 反思檢查失敗時回合永久掛起 → 修。  
- 反思請求與更新閘門死鎖 → 修。  
- 失敗時提示**哪一項檢查失敗 + 重試次數**，不再靜默。  
- 敘述者不再在回合結束後仍顯示「運行中」。

**Autoto**：`danger_reflection` 設計完整（只能降級、超時、cache、用 tool 表達 verdict）。

**自檢清單**

| # | 問題 | 期望 |
| --- | --- | --- |
| 1 | 反思超時或 Unavailable 時 run 會不會卡在 waiting？ | 應進入 ask/deny 或明確失敗，不得無限掛起 |
| 2 | 反思 HTTP 與更新閘門 / run 鎖是否可能互相等待？ | 鎖序文檔化；超時必須釋放 |
| 3 | UI 是否只轉圈不說明原因？ | 顯示失敗項 + 重試次數 |
| 4 | 回合結束後是否仍顯示「運行中」？ | 終態與 UI 訂閱必須收斂 |

### 4.5 更新檢查：別在請求路徑上同步哈希大文件

**對方**

- 下載更新後首次檢查在請求路徑同步哈希約 100MB → 服務短暫卡頓。  
- 更新檢查與版本查詢接口曾**完全不需要認證**。

**Autoto**：`update` 在 stage 階段打開文件做 SHA-256，有 size cap 與 mutex，方向合理。

**建議**

1. 哈希/校驗放在 **stage 或後台**，不要綁在每次 version check 的 HTTP handler 熱路徑。  
2. 即便 local-first，更新元數據接口也應綁定 **local token / 會話**，避免「完全裸奔」。  
3. 大文件流式哈希，避免拖垮 Agent WebSocket 與其它 API。

### 4.6 並發與前端體感（次優先但便宜）

| 對方做法 | 可學點 |
| --- | --- |
| 模型刷新 singleflight + 無響應網關退避 | 設置頁/探活不要連點打爆上游 |
| 重新進入敘述者先用緩存文檔繪製 | stale-while-revalidate |
| 消息列表折疊用合成層動畫、不重算佈局 | `transform`/`opacity`，避免整表 reflow |
| 可恢復錯誤卡片「一鍵關能力並重試」 | 403/配額類錯誤 = 解釋 + 寫回設置 + 重試 |

### 4.7 消息分層讀取（有外部 API 時再做）

對方 OAuth / External API：骨架 / 摘要 / 文本 / 完整，每層條數與字節上限。

**何時值得做**

- 受控遠程協作、只讀網關、第三方 OAuth 應用需要「能讀結構但不碰 reasoning/tool 載荷」時。  
- 本地 UI 性能也可借鑑「列表先骨架、詳情再拉」。

---

## 5. 建議暫時別急著學的

| 條目 | 原因 |
| --- | --- |
| 移除插件信任等級 | 對方是因為機制永遠落在最嚴檔、形同虛設；Autoto 若尚無 trust 模型，無債可還 |
| 官方 Claude 默認開原生網絡搜索 | 產品策略，且與 cache 副請求設計綁定；先有 cache 紀律再談默認開 |
| 主題 22 區大擴 + 九宮格邊框 | 維護成本高；可先穩定自身 theme token 表，再談插件貢獻 |
| 完整 External API 分層 | 沒有外部消費方時，過早擴大 API 表面積 |

**若學主題系統，只學方法不學規模**

- 穩定、文檔化的 token 區域表；  
- 每個擴展點附**完整示例**；  
- 明暗 variant 一等公民。

---

## 6. 建議 Backlog（給排期用）

### 6.1 建議優先 5 件

1. **Cache 前綴審計 + 中轉/官方抓包回歸**（含「搜索/動態 tool 不進主前綴」）。  
2. **統一 HTTP/WS 診斷脫敏 + 真實指紋**（Codex 等多路徑 header 單一來源）。  
3. **測試全局覆蓋必須 cleanup**（testkit + CI 抓洩漏）。  
4. **WebSearch 多通道 fallback + 無通道不暴露工具**（站在現有「失敗 ≠ 無結果」之上）。  
5. **Danger reflection / run 狀態機自檢**：失敗可見、超時降級、鎖序防死鎖、結束後不得仍「運行中」。

### 6.2 次優先

6. 更新校驗移出熱路徑 + version/update 接口鑒權複核。  
7. 插件/子進程繼承 proxy 提示。  
8. 擴展點 `maxOutput`（及 timeout）強制執行 + 單測。  
9. 可恢復錯誤卡片：「一鍵改設置並重試」。  
10. 模型/配置刷新 singleflight 與無響應退避。

### 6.3 建議的落地順序（工程視角）

```text
先修會「靜默燒錢 / 洩密 / 掛死」的
  → cache 前綴、診斷脫敏、reflection 掛起
再修會「教壞模型 / 誤導用戶」的
  → 搜索失敗語義、工具按可用性暴露
再補「長期工程衛生」
  → 測試防洩漏、singleflight、更新熱路徑
最後才考慮產品向擴展
  → 插件搜索市場、主題擴展點、External API 分層
```

---

## 7. 一頁紙結論（可直接貼進會議）

**對方 changelog 教我們的核心句式**

> 可緩存的必須穩定；可診斷的必須真實且無密鑰；可失敗的必須區分並 fallback；可擴展的必須 enforce 聲明；可並發的必須共享與退避；可更新的不能堵服務；可測試的不能髒全局。

**Autoto 相對位置**

| 維度 | 位置 |
| --- | --- |
| WebSearch 失敗語義 | 已部分領先/對齊 |
| Danger reflection 降級設計 | 已對齊良好 |
| Network diagnostics 脱敏默認 | 已對齊良好 |
| Anthropic prompt cache | 有基礎，缺「前綴污染 / 中轉」專項 |
| 多路徑客戶端指紋與 dump 紀律 | 需統一成規範 |
| 測試全局硬門檻 | 習慣有、編譯期/工具鏈級約束可加強 |
| 插件搜索/主題大擴 | 過早，先定持久化與契約 enforce |

**給決策者的建議**

- 若資源只夠做一件：**做 prompt cache 前綴審計與中轉回歸**（直接關係長會話成本與可靠性）。  
- 若資源夠做三件：加上 **診斷脫敏統一** 與 **搜索可用性/fallback**。  
- 不要優先排「22 區主題」或「抄原生默認開搜索」——投入產出比明顯低於上述工程項。

---

## 8. 附錄：外部條目 → 可學原則速查

| 外部條目（意譯） | 可學原則 | Autoto 建議動作 |
| --- | --- | --- |
| 中轉 prompt cache 只寫不讀 | 前綴穩定；抓包校正 | 前綴審計 + golden request |
| 原生搜索獨立副請求 | 副作用不進 cache 主路徑 | 設計 native search 時拆請求 |
| Codex 三路徑指紋不一致 | header 是 mode 的純函數 | 單一構造 + 快照測試 |
| WS dump 寫入 Authorization | 診斷真實且脫敏 | 統一 Redact 層 |
| 插件搜索重啟丟偏好 | 用戶配置持久化 | 插件配置 schema + 遷移 |
| 插件 maxOutput 未生效 | 聲明即契約 | enforce + 測試 |
| 搜索失敗報無結果 | 錯誤分類 | 已有基礎；加 fallback |
| 無通道仍給 WebSearch | 能力按可用性暴露 | 註冊前探活/策略檢查 |
| 更新哈希堵請求 | 重 I/O 離開熱路徑 | 複核 update/version handler |
| 更新接口無認證 | 元數據也要鉴权 | 複核 local token 綁定 |
| 測試全局洩漏 | 清理強制化 | testkit + CI |
| 反思失敗掛起/死鎖 | 超時、鎖序、可觀測 | 狀態機自檢 |
| 模型刷新 singleflight | 合併並發讀 | provider/model 刷新 |
| 合成層折疊動畫 | 避免 reflow | 前端列表性能 checklist |
| 一鍵關圖片並重試 | 可恢復錯誤閉環 | 錯誤卡片動作模式 |
| 插件收代理提示 | 子系統繼承網絡策略 | 插件進程環境注入 |
| 分層讀消息 | 最小暴露 + 配額 | 遠程/OAuth 再做 |
| 去掉無效信任等級 | 無效抽象不如刪除 | 勿過早引入空 trust |
| 請求頭隨 API 模式 | 少布爾開關 | mode→headers 表 |

---

## 9. 修訂記錄

| 日期 | 說明 |
| --- | --- |
| 2026-08-05 | 初稿：依外部 changelog 與 Autoto 工作樹對照整理，供內部傳閱 |

---

*本文件是學習與排期輸入，不是承諾範圍。具體實作應另開任務，並以測試與抓包/用量證據驗收。*
