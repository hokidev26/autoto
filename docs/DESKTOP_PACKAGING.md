# Autoto 桌面打包與簽名（邊界說明）

> 原則：**CLI 瀏覽器遠端路徑永遠可用**；桌面殼是可選客戶端。

## 1. 開發入口（日常）

```bash
# CLI + 手機隧道（推薦作為常駐服務）
make build-cli && ./autoto

# 本機原生視窗（預設 ephemeral 埠，不搶 CLI）
make build-desktop && ./autoto-desktop
```

`go build -tags desktop` 需要本機 WebView / Wails 依賴。Linux CI 預設 **不** 鏈 Wails（`//go:build desktop`）。

## 2. 正式安裝包（本倉庫邊界）

| 產物 | 狀態 | 說明 |
|---|---|---|
| 裸二進位制 `autoto-desktop` | 支援 | 開發與內測 |
| macOS `.app` / 公證 | 未產品化 | 需 Apple Developer + notarize CI |
| Windows `.msi` / Authenticode | 未產品化 | 需證書與獨立 release job |
| Linux AppImage/deb | 未產品化 | 可選後續 |

Wails v3 官方打包仍為 Alpha。正式簽名流水線應在 **release CI** 完成，**不要**在執行時自簽名。

建議獨立任務（不在殼程序內）：

1. 構建帶版本 ldflags 的 `autoto` + `autoto-desktop`
2. 生成 checksums（SHA-256）
3. 平台簽名 / 公證
4. 釋出 **惰性** update manifest（`internal/update` 僅後設資料，無 URL/腳本欄位）
5. 使用者在 **本機** 下載後，用殼 API `POST /api/desktop/update/stage` 暫存；**遠端不可 stage/apply**

## 3. 更新骨架（已實現邊界）

- `GET /api/update/status`：只讀計劃後設資料（遠端可讀，不可裝）
- `POST /api/desktop/update/stage`：**loopback + 桌面 host**，複製本地檔案到 `$HOME/updates/staged/`
- `GET/DELETE /api/desktop/update/pending`：檢視/取消暫存
- **無**靜默下載、**無**請求路徑內替換正在執行的二進位制、**無**遠端觸發安裝

## 4. 深度連結與自啟動（殼級）

- 自啟動：托盤選單 Enable/Disable Login Item；或 loopback  
  `GET|POST|DELETE /api/desktop/autostart`
- 深度連結：`autoto://agent?id=…`、`autoto://project?id=…`、`autoto://settings?panel=…`  
  註冊 OS URL scheme 需要打包進 `.app` / 安裝器；開發期可用 argv：  
  `./autoto-desktop 'autoto://settings?panel=remote-access'`

## 5. 手機遠端不受影響

- 常駐請用 `./autoto` + 臨時/命名 Cloudflare 隧道  
- 桌面 7–9 API 全部要求 **非遠端 + loopback**；手機會話繼續用既有 `/api/*` 與 Agent WebSocket  
- 關閉桌面窗只關 **該程序** Runtime（且 desktop 預設 ephemeral）；**不要**把手機會話掛在 desktop 臨時程序上  
