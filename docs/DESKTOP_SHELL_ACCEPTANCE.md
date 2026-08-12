# Autoto 桌面殼驗收清單

分支：`feature/wails-desktop-shell-foundation`  
預設構建 **不含** Wails；桌面殼需 `-tags desktop`。

## 1. 工程化（無 GUI 機器 / CI）

```bash
# 預設：CLI + 業務 + 前端，不連結 Wails
./scripts/check.sh
# 或
make check

# 確認桌面包被預設排除
! go list ./cmd/autoto-desktop ./internal/desktop
go list -tags desktop ./cmd/autoto-desktop ./internal/desktop

# 有原生 WebView 工具鏈的機器上：
make check-desktop
# 或
AUTOTO_CHECK_DESKTOP=1 ./scripts/check.sh
make build-desktop
```

期望：

- [ ] `make check` 在 Linux CI / 無 WebKit 環境通過
- [ ] 無 `-tags desktop` 時 `go list` 列不出 desktop 包
- [ ] `go build ./cmd/autoto` 成功且體積明顯小於 desktop 二進位制

## 2. Headless 冒煙（任意平台）

```bash
go build -tags desktop -o autoto-desktop ./cmd/autoto-desktop
./autoto-desktop -headless -ready-timeout 15s
# 另開終端：curl -sS "$URL/api/health"  → 200 {"ok":true,...}
# Ctrl+C → 程序 exit 0，埠釋放
```

期望：

- [ ] 日誌含 `desktop runtime ready url=http://127.0.0.1:<port>`
- [ ] `/api/health` 返回 200
- [ ] headless **不**註冊原生 dialog host：`POST /api/desktop/dialog/confirm` → 404
- [ ] SIGINT/SIGTERM 後端口不可再連

## 3. GUI 冒煙（macOS / Windows，本機）

```bash
make build-desktop
./autoto-desktop
```

期望：

- [ ] 原生視窗開啟，載入本地 UI（非空白）
- [ ] 聊天/設定等 API 正常（同源 + local token）
- [ ] 危險操作彈出 **系統** confirm（非瀏覽器樣式），取消不執行
- [ ] 關窗：視窗隱藏，程序仍在，托盤可見
- [ ] 托盤 **Show**：視窗恢復；**Quit**：程序退出且 HTTP 埠釋放
- [ ] 再啟動第二個例項：應聚焦已有視窗，不長期雙開（single instance）
- [ ] 遠端/隧道場景：桌面 dialog API 對非 loopback 保持拒絕（403）

## 4. 迴歸：瀏覽器與遠端不被破壞

```bash
make build-cli
./autoto
# 瀏覽器開啟配置的 host:port
```

期望：

- [ ] CLI 行為與引入桌面前一致
- [ ] 瀏覽器 confirm 仍可用（`platform.mjs` 預設路徑）
- [ ] 遠端訪問密碼/會話邏輯不變；桌面 dialog 端點對遠端 403/不可用

## 5. 原生選目錄 / 圖示 / 視窗狀態（中優先順序）

```bash
make build-desktop && ./autoto-desktop
```

期望：

- [ ] 「選擇資料夾」在桌面殼內彈出 **系統** 目錄對話方塊（非僅內建 modal）
- [ ] Windows/Linux 桌面殼同樣可用（不依賴 macOS AppleScript）
- [ ] 托盤圖示為 Autoto 資源（非 Wails 預設佔位）
- [ ] 調整視窗大小/位置後退出再開：幾何大致恢復；最大化狀態可恢復
- [ ] 狀態檔案：`$AUTOTO_HOME/desktop-window.json`（或配置 `paths.homeDir`）

API（僅 loopback + 桌面 host）：

- `POST /api/desktop/dialog/open-directory` → `{ path, canceled }`
- `POST /api/desktop/dialog/open-file` → `{ path, canceled }`
- 既有 `POST /api/fs/native-directory` 在註冊 shell host 時走 Wails 選擇器

## 6. 桌面 7–9（更新骨架 / 打包邊界 / 自啟動+深鏈）

```bash
make build-desktop && ./autoto-desktop
# 另開終端保持手機能力：
make build-cli && ./autoto   # + 設定裡一鍵隧道
```

期望：

- [ ] 托盤可 Enable/Disable Login Item（或 loopback `POST/DELETE /api/desktop/autostart`）
- [ ] `./autoto-desktop 'autoto://settings?panel=remote-access'` 能聚焦並改 hash（深鏈）
- [ ] `POST /api/desktop/update/stage` 僅 localhost 可暫存本地二進位制；遠端 403
- [ ] `GET /api/update/status` 遠端仍只讀，不能安裝
- [ ] 手機經隧道登入後仍可對話 / 審批（與桌面殼並行時推薦 CLI 常駐）

打包：`make release-desktop` → `dist/`；簽名見 `docs/DESKTOP_PACKAGING.md`。

## 7. 刻意不在本清單驗收

- 完整靜默自動更新 UI、後台替換正在執行的二進位制
- macOS 公證 / Windows Authenticode 生產流水線
- OS 級 `autoto://` 安裝器註冊（需正式 .app/.msi）
- 通用檔案系統 Binding / Agent 繫結
