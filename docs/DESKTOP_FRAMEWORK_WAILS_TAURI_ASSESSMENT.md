# Autoto 桌面框架評估：Wails 與 Tauri

核對日期：2026-07-20

## 1. 背景與問題

本次討論源於一篇基於 Wails v3、WebView 和 Go 的桌面端腳手架文章，以及一條評價：

> Wails 這類方案的原生部分比較“毛坯”，Tauri 則可以直接上手。

這裡的 `walis` 應為 `Wails`。

這句話討論的不是 Web 頁面本身是否好看，也不是 Wails 能不能呼叫系統 API，而是兩套框架在以下方面的“開箱即用程度”：

- 原生檔案和目錄對話方塊；
- 托盤、選單、全域性快捷鍵和單例項；
- 自啟動、深度連結、通知和剪貼簿；
- 視窗尺寸與位置持久化；
- 應用更新、簽名和打包；
- 子程序、檔案系統、資料庫和安全儲存；
- 權限宣告、外掛安裝、文件和跨平台一致性。

## 2. “原生比較毛坯”是什麼意思

“毛坯”不是嚴格的技術術語，通常包含四層意思。

### 2.1 核心能力存在，但產品級封裝不夠完整

Wails 可以提供視窗、選單、托盤、系統對話方塊、Go 與 JavaScript 繫結等桌面能力，但開發者經常仍要自己補齊：

- 統一的前端呼叫適配層；
- 不同作業系統的行為差異；
- 失敗和取消狀態；
- 應用生命週期和異常退出處理；
- 自動更新、單例項、自啟動等產品功能；
- 完整的測試和釋出流程。

因此，“毛坯”更接近“基礎結構已經有了，但裝修和家電需要自己配”，而不是“框架沒有原生能力”。

### 2.2 現成外掛和標準組合相對少

Tauri 2 官方外掛覆蓋了很多常見桌面需求，例如：

- Autostart；
- Clipboard；
- Deep Link；
- Dialog；
- File System；
- Global Shortcut；
- Logging；
- Notification；
- Opener；
- Shell；
- Single Instance；
- SQL、Store 和 Stronghold；
- Updater；
- Window State。

對開發者而言，這意味著很多功能可以按照同一種流程安裝外掛、註冊外掛、宣告權限，然後從 JavaScript 或 Rust 呼叫。所謂“Tauri 可以直接上手”，主要是在說這種標準化和外掛化體驗。

### 2.3 文件、工具鏈和生態成熟度不同

截至 2026-07-20，Wails v3 官方仍明確標記為 Alpha。官方說明其 API 已經“相當穩定”，也有生產應用，但文件和工具鏈仍在繼續完善。

這會帶來幾個現實影響：

- 文件可能不完整或調整；
- 示例和最佳實踐數量相對有限；
- 某些桌面能力需要閱讀原始碼或自行封裝；
- 升級時需要承擔更高的適配成本；
- 團隊需要更強的 Go 和平台 API 處理能力。

Tauri 2 已形成更完整的官方外掛目錄、權限模型和安裝流程，因此在“我要馬上加一個桌面功能”這一點上通常更順手。

### 2.4 “原生”不等於原生控制元件 UI

Wails 和 Tauri 都主要使用系統 WebView 渲染前端介面：

- Windows 通常使用 WebView2；
- macOS 使用 WKWebView；
- Linux 使用系統 WebKit 相關實現。

兩者都不是傳統意義上使用 WinUI、AppKit 或 GTK 原生控制元件繪製全部介面的框架。它們的“原生能力”主要指：

- 原生窗口；
- 系統選單和托盤；
- 檔案對話方塊；
- 通知、快捷鍵和剪貼簿；
- 檔案系統和子程序；
- 作業系統生命週期與打包。

因此，“Wails 原生比較毛坯”不應被理解為“Wails 的 WebView 頁面一定更醜”或“Tauri 自動生成原生控制元件介面”。介面質量仍主要取決於前端實現。

## 3. 這條評價是否準確

結論是：**有一定道理，但只說了一半。**

### 3.1 對新專案而言，這條評價大體成立

如果從零開始做一個需要以下功能的桌面產品：

- 系統托盤；
- 自動更新；
- 單例項；
- 自啟動；
- 全域性快捷鍵；
- 原生通知；
- 檔案系統權限；
- 安全儲存；
- 視窗狀態恢復；

Tauri 2 的官方外掛體系通常能降低前期整合成本。開發者可以更快找到標準外掛、權限標識、平台支援表和示例。

### 3.2 但“Tauri 直接上手”不等於沒有成本

Tauri 的成本主要轉移到了以下方面：

- Rust 工具鏈和依賴管理；
- `src-tauri` 工程；
- Rust 與前端之間的命令介面；
- Capability 和 Permission 配置；
- 不同視窗、WebView 和平台的權限宣告；
- 外掛版本相容和釋出配置；
- 如果主業務不是 Rust，還需要管理 sidecar 或程序間通訊。

Tauri 2 的 Capability 模型能提供更細的最小權限邊界，但也要求專案明確維護“哪個視窗可以呼叫哪個能力”。對於安全要求高的專案，這是優點；對於只想快速彈出一個視窗的小專案，它也是額外配置成本。

### 3.3 對已經使用 Go 的專案，Wails 的優勢會明顯放大

Wails 的核心優勢不是外掛數量，而是：

- 後端直接使用 Go；
- 可以複用現有 Go package；
- Go 與 JavaScript 繫結自動生成；
- 不需要把核心業務改寫為 Rust；
- 可以避免同時維護 Go 和 Rust 兩套後端工程。

所以“哪個更容易上手”不能脫離現有技術棧判斷。

## 4. Wails 與 Tauri 的實際對比

| 維度 | Wails v3 | Tauri 2 |
|---|---|---|
| 核心後端語言 | Go | Rust |
| 當前成熟度 | v3 仍為 Alpha，API 相對穩定 | v2 正式生態和官方外掛較完整 |
| 系統 WebView | 是 | 是 |
| 前端技術 | 可使用主流 Web 前端 | 可使用主流 Web 前端 |
| Go 專案複用 | 很強，可直接複用 package | 較弱，通常需要 sidecar 或重新實現 Rust 命令 |
| 官方外掛覆蓋 | 核心原生能力存在，但標準外掛組合相對少 | 官方外掛覆蓋常見桌面和移動能力 |
| 權限模型 | 更依賴應用自身服務邊界和 Go 端校驗 | Capability/Permission 模型較完整但配置更多 |
| 原生對話方塊 | 支援 | 官方 Dialog 外掛，安裝和呼叫流程標準化 |
| 自動更新、單例項、自啟動 | 需要核對版本能力並進行更多工程整合 | 有對應官方外掛或標準方案 |
| IPC | Go 與 JavaScript 繫結、記憶體 IPC | Rust Command/Plugin IPC |
| 團隊學習成本 | Go 團隊較低 | 需要 Rust 和 Tauri 配置經驗 |
| 開箱即用程度 | 中等，偏向開發者自行搭建 | 較高，偏向外掛組裝 |
| 長期技術統一性 | 對 Go 產品較好 | 對 Rust 產品較好 |

## 5. 對 Autoto 的具體影響

### 5.1 Autoto 不是一個普通的新建桌面專案

Autoto 已經是一個較完整的 Go 本地服務：

```text
cmd/autoto
  -> internal/app.Run
  -> HTTP API + WebSocket
  -> Agent / Provider / Tools
  -> SQLite
  -> Background / Preview / Terminal
  -> Embedded HTML/CSS/ES Modules
```

現有核心能力包括：

- Go HTTP 服務；
- 嵌入式前端資源；
- SQLite 資料和遷移；
- Agent 與 Tool 審批；
- WebSocket 事件；
- PTY 終端；
- 遠端訪問；
- Telegram 和 Home Assistant；
- Provider、MCP 和插件；
- 瀏覽器客戶端。

所以 Autoto 的桌面框架選擇不能只看“哪一個外掛多”，還必須看：

- 是否複用現有 Go Runtime；
- 是否保留瀏覽器和遠端客戶端；
- 是否引入第二套業務介面；
- 是否需要管理第二個後端執行時；
- 是否破壞現有 HTTP/WebSocket 安全邊界。

### 5.2 使用 Wails 的實際形態

Wails 對 Autoto 的合理用法應是“可選桌面殼”，而不是重寫整個業務協議。

建議形態：

```text
Autoto Go Runtime
├── HTTP API
├── Agent WebSocket
├── Terminal WebSocket
├── SQLite
├── Provider / Tools
└── Runtime Supervisor

客戶端
├── 普通瀏覽器
└── Wails 桌面窗口
```

優勢：

- 可以直接複用 Go package；
- 有機會做成單程序或更緊密的生命週期；
- 不需要維護 Rust 後端；
- 桌面殼可以直接管理 Go Runtime。

缺點：

- Wails v3 仍處於 Alpha；
- 需要自己補齊部分產品級原生能力；
- 如果前端改用 Wails Binding，會與現有 HTTP API 形成兩套呼叫路徑；
- 仍需為瀏覽器模式保留原有介面。

因此，即使使用 Wails，也不建議把 Agent、Provider、Tools、審批、Git 等核心功能改成只允許 Wails Binding 呼叫。

### 5.3 使用 Tauri 的實際形態

Tauri 對 Autoto 最現實的方案是：

```text
Tauri Desktop Shell
  -> 啟動 autoto Go sidecar
  -> 等待本機健康檢查
  -> WebView 載入 Autoto UI
  -> 關閉視窗時停止 sidecar
```

優勢：

- 原生 Dialog、Updater、Single Instance、Autostart、Window State 等功能更容易標準化接入；
- 桌面殼的產品化能力較完整；
- Capability/Permission 模型適合限制不同視窗的系統權限；
- 不需要重寫現有前端頁面。

缺點：

- 同時維護 Go 與 Rust；
- 需要處理 sidecar 啟動、健康檢查、異常退出和升級；
- 需要處理埠選擇、本機認證和程序樹回收；
- Tauri 更新器必須考慮桌面殼和 Go sidecar 的版本一致性；
- 如果前端直接呼叫 Rust Command，容易再形成一套業務介面；
- 構建、簽名和排錯涉及兩個生態。

因此，Tauri 的“直接上手”主要體現在桌面外掛，而不是 Autoto 核心業務可以直接遷入。

## 6. 本次評估的修正結論

此前只從“Autoto 已經是 Go 專案”出發，會自然認為 Wails 是最順手的桌面殼。加入生態成熟度和產品級原生能力後，結論應調整為：

1. **那條評價有事實基礎。** Tauri 2 在常見桌面功能的官方外掛、權限宣告和標準接入流程上，確實比 Wails v3 更開箱即用。
2. **Wails 不是沒有原生能力。** “毛坯”指的是需要開發者自行補齊更多產品化封裝，而不是能力缺失或介面質量差。
3. **Tauri 對 Autoto 也不是無成本的直接替換。** 現有 Go Runtime 仍需作為 sidecar 保留，或者把大量業務改寫為 Rust；前者引入雙執行時，後者代價更大。
4. **Autoto 不應該為了桌面框架而重寫核心協議。** HTTP/WebSocket、Agent、Provider、Tools 和安全邊界應繼續保持唯一事實源。
5. **是否選擇 Tauri，取決於桌面產品化優先順序。** 如果近期目標是快速獲得更新器、單例項、自啟動、原生對話方塊和視窗狀態等能力，Tauri 2 更有優勢。
6. **是否選擇 Wails，取決於單一 Go 技術棧的優先順序。** 如果更重視單程序、Go package 複用和長期技術統一，並能接受自行補齊桌面功能以及 Wails v3 Alpha 風險，Wails 更自然。

## 7. 對 Autoto 的推薦決策

### 7.1 當前不急於釋出桌面安裝包

建議暫時不引入任何桌面框架，先完成框架無關的基礎工作：

1. 從 `internal/app/run.go` 拆出可啟動和可關閉的 Runtime；
2. 統一 Background、Preview、Terminal、MCP 的子程序生命週期；
3. Windows 使用 Job Object 或等價機制回收完整程序樹；
4. 抽象前端 Dialog 介面，移除業務模組對 `window.confirm` 的直接依賴；
5. 保持 HTTP/WebSocket 為唯一核心業務協議；
6. 明確瀏覽器本地偏好與伺服器權威資料的邊界。

這些工作無論最後選 Wails 還是 Tauri 都能複用。

### 7.2 近期必須快速交付桌面版

建議優先驗證：

```text
Tauri 2 殼 + 現有 autoto Go sidecar
```

原因是桌面產品通常很快會需要：

- 單例項；
- 原生檔案和目錄對話方塊；
- 視窗狀態恢復；
- 系統托盤；
- 自啟動；
- 應用更新；
- 通知和開啟外部連結。

Tauri 2 對這些能力已有較標準的外掛路徑。

但驗證必須堅持以下邊界：

- Tauri 不實現 Agent 業務邏輯；
- Tauri 不復制 Provider、Tools 或審批規則；
- Go sidecar 保持服務端權威；
- Tauri 只負責桌面視窗、生命週期和系統整合；
- 桌面版與瀏覽器版使用同一套前端和 API；
- 桌面殼退出時必須可靠停止完整 Go 程序樹；
- 更新必須保證殼與 sidecar 版本一致。

### 7.3 更重視單程序和 Go 技術統一

建議繼續觀察 Wails v3，或者做一個嚴格受限的概念驗證：

- 只建立視窗；
- 只複用現有 Go Runtime；
- 不把核心 API 改成 Wails Binding；
- 只驗證關閉、重啟、崩潰恢復、對話方塊和打包；
- 不在概念驗證階段加入資料庫遷移或大規模前端改造。

如果 Wails v3 的 Alpha 狀態、工具鏈和缺失的桌面能力導致維護成本過高，可以保留實驗分支而不進入主釋出流程。

## 8. 推薦的選擇矩陣

| 產品目標 | 推薦方向 |
|---|---|
| 繼續以本地 Web 服務為主 | 暫不引入桌面框架 |
| 最快獲得成熟桌面產品功能 | Tauri 2 + Go sidecar |
| 最大化複用 Go、減少語言數量 | Wails |
| 強調細粒度桌面權限配置 | Tauri 2 |
| 強調單程序和 Go Runtime 生命週期統一 | Wails |
| 同時保留瀏覽器和遠端訪問 | 兩者都只能作為可選客戶端，不能替代 HTTP/WebSocket 核心 |
| 團隊沒有 Rust 經驗 | Wails 或暫緩桌面化 |
| 團隊願意維護 Rust 殼並重視官方外掛 | Tauri 2 |

## 9. 概念驗證的驗收標準

無論選擇哪一個框架，概念驗證至少應覆蓋：

1. 桌面視窗能可靠啟動現有 Autoto；
2. 不出現固定埠衝突或複用錯誤例項；
3. 關閉視窗後沒有殘留 Go、Shell、Preview 或 MCP 程序；
4. 瀏覽器版仍能獨立執行；
5. Agent WebSocket 和 Terminal WebSocket 正常；
6. 本地 token、Origin、Cookie 和遠端訪問邊界不被削弱；
7. 原生目錄選擇在 Windows、macOS、Linux 行為一致；
8. 應用單例項策略明確；
9. 桌面殼與 Go Runtime 版本不匹配時拒絕啟動或明確報錯；
10. 更新失敗時可恢復到上一完整版本；
11. 打包產物不包含開發憑據或本地資料庫；
12. 至少完成 Windows 和 macOS 的真實安裝、啟動、退出和升級測試。

## 10. 最終總結

“Wails 原生比較毛坯，Tauri 可以直接上手”可以翻譯為：

> Wails 更像給 Go 開發者提供桌面視窗和系統能力的基礎框架，很多產品級功能需要自己封裝；Tauri 2 則通過較完整的官方外掛和權限體系，把常見桌面需求整理成了更標準的安裝與呼叫流程。

這句話對一般新專案有一定準確性，但對 Autoto 不能直接推匯出“Tauri 一定更省事”。Autoto 已經擁有龐大的 Go Runtime，使用 Tauri 意味著維護 Rust 桌面殼與 Go sidecar；使用 Wails 則意味著承擔 v3 Alpha 和更多原生產品化封裝。

本次建議是：

- 不重寫 Autoto 核心；
- HTTP/WebSocket 繼續作為唯一業務協議；
- 先完成 Runtime 生命週期、跨平台程式樹回收和 Dialog 抽象；
- 如果近期必須交付桌面版，優先做 Tauri 2 + Go sidecar 的受限概念驗證；
- 如果更重視單一 Go 技術棧和單程序維護，再評估 Wails；
- 桌面框架最終應是 Autoto 的客戶端外殼，而不是新的業務後端。

## 11. 核對依據

本次結論依據以下官方資料核對：

- Wails v3 官方首頁、Quick Start 與 API/功能說明；
- Tauri 2 官方外掛目錄；
- Tauri 2 Capability/Permission 文件；
- Tauri 2 Dialog 外掛文件；
- Autoto 當前 `cmd/autoto`、`internal/app`、`internal/server`、`internal/background`、`internal/preview`、`internal/db` 和嵌入式前端實現。

## 12. 落地進度（分支 `feature/wails-desktop-shell-foundation`）

截至 2026-07-21，地基與 **Wails 薄殼開窗** 已落地：

| 項 | 狀態 | 位置 |
|---|---|---|
| `app.Runtime` Start / WaitReady / Close | 已完成 | `internal/app/runtime.go`，`Run` 改為託管該 Runtime |
| 可選 `EphemeralHTTP`（`host:0`） | 已完成 | `app.Options.EphemeralHTTP` + `bindConfiguredHTTPListeners` |
| Wails 薄殼開窗 | 已完成 | `internal/desktop` + `cmd/autoto-desktop`：`Runtime.URL()` → `WebviewWindow.URL`；關窗 / 訊號 / `ctx` 取消時 `Close` Runtime |
| 桌面 CLI 標誌 | 已完成 | `-ephemeral-http`（預設 true）、`-headless`（CI/無 GUI）、`-ready-timeout`、`-config` |
| 依賴 | 已引入 | `github.com/wailsapp/wails/v3@v3.0.0-alpha2.117`（僅桌面殼路徑使用；業務仍走 HTTP/WebSocket） |
| 跨平台程式組 | 已完成 | `internal/process`；Unix Setpgid；Windows Job Object + KILL_ON_JOB_CLOSE |
| background / preview 接入 | 已完成 | `internal/background/shell.go`、`internal/preview/manager.go` |
| Bash / MCP stdio 接入 | 已完成 | `internal/tools/bash.go`、`internal/mcp/stdio.go` |
| 前端 platform dialog 適配 | 初版 | `internal/server/static/modules/platform.mjs`；`app-main` 兩處 `confirmAction` 已改用 |

設計約束（保持有效）：

- **不取消瀏覽器與遠端**：`cmd/autoto` 行為不變；桌面只是可選客戶端。
- **關窗不削弱鑑權**：殼只關閉本程序 Runtime；local token / 遠端密碼體系仍在 HTTP 服務端。
- **業務不走 Binding**：視窗直接載入 `http://127.0.0.1:<ephemeral>/`；Agent/API 仍是既有路由。

驗收命令（本機 2026-07-21）：

```text
go test ./internal/app/ ./internal/desktop/ ./internal/process/ ./internal/background/ ./internal/preview/ ./internal/tools/ ./internal/mcp/ -count=1
node --test internal/server/static/modules/platform.test.mjs
go build ./cmd/autoto ./cmd/autoto-desktop
# headless smoke（CI / 無視窗）:
#   ./autoto-desktop -headless
# GUI smoke（Mac）:
#   ./autoto-desktop
# 關窗或 Ctrl+C 後進程應退出，HTTP 埠釋放。
```

### 12.1 殼級產品化增量（同分支續作）

| 項 | 狀態 | 說明 |
|---|---|---|
| 原生 dialog（殼級） | 已完成 | HTTP 橋 `POST /api/desktop/dialog/{confirm,alert}` → `ShellDialogHost` → Wails `Dialog.Question/Info`；**不**走 Wails Service Binding（外部 `Runtime.URL()` 與 asset origin 不同） |
| 前端 platform 自動接線 | 已完成 | `platform.mjs` 在 `AUTOTO_DESKTOP_SHELL` 時 `fetch` 上述端點；瀏覽器預設仍用 `window.confirm` |
| 單例項 | 已完成 | Wails `SingleInstance` UniqueID `com.autoto.desktop`；二次啟動 Focus/Restore 主窗 |
| Windows Job Object CI | 已完成 | `.github/workflows/ci.yml` 增加 `windows-process`：`go test ./internal/process` + 構建 CLI |
| 系統托盤（Show/Hide/Quit） | 已完成 | `internal/desktop/tray.go`：關窗隱藏到托盤，托盤選單退出才關 Runtime |
| 托盤/應用圖示 | 已完成 | `internal/desktop/assets/*` embed；`application.Options.Icon` + tray `SetIcon` |
| 原生選目錄/檔案 | 已完成 | `ShellDialogHost.PickDirectory/PickFile`；`/api/desktop/dialog/open-*`；`fsNativeDirectory` 優先 shell host（跨平台）；瀏覽器仍 macOS AppleScript |
| 視窗狀態持久化 | 已完成 | `desktop-window.json`（homeDir）；尺寸/位置/最大化；move/resize 防抖寫入 |
| 全面替換 `window.confirm` | 已完成 | 業務模組經 `platform.confirm`；瀏覽器預設仍 `window.confirm` |
| 自動更新 / 打包簽名 | 骨架+邊界 | 本地 stage：`POST /api/desktop/update/stage`（loopback only）；`/api/update/status` 仍只讀；`make release-desktop` + `docs/DESKTOP_PACKAGING.md`；無靜默下載/遠端安裝 |
| 自啟動 | 已完成 | 托盤 Login Item + `GET/POST/DELETE /api/desktop/autostart`（Wails Autostart） |
| 深度連結 `autoto://` | 已完成 | 解析 + 窗內 hash 導航；argv / ApplicationLaunchedWithUrl / 二次例項；OS 註冊需正式打包 |
| 無 GUI build tag 拆分 | 已完成 | `//go:build desktop` 於 `cmd/autoto-desktop` + `internal/desktop`；預設 `go test/build ./...` 與 CI 不連結 Wails；`make check-desktop` / `AUTOTO_CHECK_DESKTOP=1` 可選驗收；清單見 `docs/DESKTOP_SHELL_ACCEPTANCE.md` |

安全邊界：

- dialog 路由在 host 未註冊時 **404**（純 CLI/瀏覽器無桌面殼）；
- **禁止遠端**：`remoteAccessGateRequired` 或非 loopback peer → 403；
- 瀏覽器發起請求仍校驗 local token（與現有 API 一致）；
- 僅 confirm/alert，**無** Agent/FS/任意 Binding。

### 12.2 自動更新 / 打包簽名（邊界，未產品化）

**現狀（可保留，不要誤做成“桌面自動升級”）：**

- `internal/server/update.go` + `/api/update/status` 只報告**本地注入**的信任 manifest 後設資料（版本、channel、是否需要備份/重啟）。
- **無**網路拉取 release、**無**二進位制下載、**無**靜默替換、**無**程式碼簽名校驗流水線。
- CLI 與桌面殼共享同一 Runtime；遠端會話同樣只讀 status，不能觸發安裝。

**桌面殼若要做更新，建議硬約束：**

1. 與瀏覽器路徑一致：先 `/api/update/status`，使用者顯式確認後再動作；
2. 下載與校驗在殼程序完成，寫入臨時目錄，**絕不**經遠端 API 觸發安裝；
3. 簽名：macOS notarize / Windows Authenticode 在 CI release job 中完成，不在執行時自簽名；
4. 打包：Wails 官方打包工具鏈仍為 Alpha；本分支繼續用 `go build ./cmd/autoto-desktop` 作為開發入口，正式 `.app` / `.msi` 另開發布任務；
5. 更新失敗必須可回滾到舊二進位制；開發版（`0.1.0-dev`）預設 `development_build`，不提示升級。

**本分支刻意不做：** 完整 updater UI、後台靜默升級、跨平台安裝器簽名流水線。
