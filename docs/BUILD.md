# 從原始碼構建 Autoto

面向在本機改程式碼、需要重新編譯驗證的場景。如果你只是想跑一個現成的
執行檔，看 [WINDOWS_RUN.md](WINDOWS_RUN.md)。

## 只保留一個執行檔

**規則：構建產物永遠只留一個 `codeharbor/autoto.exe`，舊的直接覆蓋。**

這條規則是有代價換來的。此前根目錄和 `codeharbor/` 累積過 11 個 exe
（約 590 MB），檔名分別是 `autoto-next`、`autoto-reasoning`、
`autoto-web.next`、`autoto.preview`、`autoto.minimal-preview`、
`autoto.new` 等等。問題不在磁碟佔用，而在於**沒人能確定哪個是最新的**：

- 曾經出現過修復已經編譯進 `autoto.new.exe`，但使用者雙擊的是舊的
  `autoto.exe`，於是「修好的 bug 看起來沒修好」，白白多花一輪排查。
- `-o autoto.new.exe` 這類臨時命名會繞過覆蓋，讓舊產物無聲留下。

所以：**構建時不要另起檔名。** 需要保留舊版本時用 git，不要用檔名。

`*.exe` 和 `*.exe~` 已在 `.gitignore` 中，不會誤提交。

## Go 工具鏈位置

如果 Go **不在 PATH 裡**（例如手動解壓安裝到自訂目錄），Git Bash 裡
每個會話先加進 PATH（把路徑換成你的安裝位置）：

```bash
export PATH="$PATH:/c/path/to/go/bin"
go version   # 應輸出 go1.26.x windows/amd64
```

直接敲 `go build` 而沒設 PATH 會得到
`bash: go: command not found`。注意用 `&&` 而不是 `;` 串聯命令，否則
go 失敗了後面的 `echo "BUILD OK"` 照樣會列印，得到假的成功資訊。

## 構建

在 `codeharbor/` 目錄下執行。

### 伺服器 / CLI 版（平時用這個）

```bash
go build -o autoto.exe ./cmd/autoto
```

覆蓋同名檔案即可，不要輸出成別的名字。

### 桌面窗口版

需要 `desktop` 構建標籤和本機 WebView 工具鏈：

```bash
go build -tags desktop -o autoto-desktop.exe ./cmd/autoto-desktop
```

釋出版（去掉 Wails devtools）：

```bash
VERSION=dev make build-desktop-release
```

### Makefile 快捷方式

`Makefile` 裡的目標輸出的是無副檔名檔案（為 Unix 準備），Windows 上
直接用上面的 `go build -o autoto.exe` 更省事。

| 目標 | 作用 |
|------|------|
| `make check` | 跑 `scripts/check.sh`（格式 + vet + 測試） |
| `make check-desktop` | 同上，額外包含 Wails 桌面包 |
| `make fmt` | `gofmt -w ./cmd ./internal` |
| `make build-cli` | 構建 CLI 版 |
| `make build-desktop` | 構建桌面版 |
| `make release-desktop` | 產出 `dist/` + SHA256SUMS |

## 替換正在執行的 exe

Windows 會鎖定正在執行的檔案，**必須先停掉程序**，否則改名會失敗：

```powershell
# 1. 關掉 Autoto（關閉視窗，或結束 autoto.exe 程序）
# 2. 確認沒有殘留
Get-Process -Name autoto -ErrorAction SilentlyContinue
# 3. 確認埠已釋放（16888 = 管理 UI，閘道器埠見設定頁）
Get-NetTCPConnection -State Listen -LocalPort 16888 -ErrorAction SilentlyContinue
```

兩個都沒輸出才是乾淨的，然後再構建。

## 驗證構建產物

```bash
./autoto.exe --help
```

應列出：

```text
-config string      path to config.json
-no-browser         do not open the web UI in a browser on startup
```

沒有 `--version` 引數。想確認拿到的是不是剛編的那個，比時間戳最可靠：

```bash
ls -la autoto.exe
```

## 跑測試

```bash
go build ./...                          # 全量編譯
go test ./internal/providers/...        # 單個包
go test ./internal/...                  # 全部（internal/db 較慢，約 3 分鐘）
```

改動閘道器或供應商相關程式碼時，至少覆蓋：

```bash
go test ./internal/providers/... ./internal/gateway/... ./internal/db/...
```

工作區裡可能有他人未提交的改動，導致測試失敗與你無關。判斷方法是隻讀
對比，不要建 worktree：

```bash
git diff --stat HEAD -- <你改過的檔案>       # 確認改動範圍
git show HEAD:<路徑> | grep -c '<關鍵程式碼>'  # HEAD 的行為
grep -c '<關鍵程式碼>' <路徑>                  # 工作區的行為
```

兩者不一致，說明該檔案正被別人改動中，失敗大機率不是你引入的。

## 開啟 debug 日誌

程式**不會自動寫日誌檔案**（`~/.autoto/*.log` 是歷史遺留，不再更新）。
日誌走標準輸出，**雙擊 exe 啟動看不到任何日誌**。

要看日誌必須從終端啟動：

```powershell
$env:AUTOTO_LOG_LEVEL = "debug"
& ".\autoto.exe"
```

PowerShell 裡兩條命令要分行，寫在同一行會報
`參考運運算元後面遺漏屬性名稱`。

可用級別：`debug` / `info`（預設）/ `warn` / `error`。

環境變數只在當前終端會話有效，關掉視窗就失效，下次雙擊啟動不帶 debug。

### 什麼時候需要 debug

閘道器的 `handleModels` 在供應商不可用時是**靜默跳過**的：

```go
if err != nil {
    continue   // 不寫日誌，不報錯
}
```

所以「模型莫名從 `/v1/models` 消失」這類問題，只有 debug 日誌能定位。
開啟後請求一次 `/v1/models`，會看到每個供應商兩行：

```text
level=DEBUG msg="gateway handleModels provider check" provider=gemini-oauth permitted=true
level=DEBUG msg="gateway handleModels ListModels" provider=gemini-oauth models="[...]" err=<nil>
```

`permitted=false` 指向權限或賬號授權問題；`permitted=true` 但 `err`
非空，指向該供應商的 `ListModels` 呼叫失敗。

## 常見問題

**`bash: go: command not found`**
沒設 PATH，見上面「Go 工具鏈位置」。

**`Access is denied` / 改名失敗**
exe 正在執行，先停程序。

**`package command-line-arguments: no Go files in ...`**
Go 會忽略以 `_` 或 `.` 開頭的檔案。臨時腳本不要命名成
`_tmp_check.go`，用 `cmd/<name>/main.go` 或不帶下劃線字首的名字。

**`go.mod file not found`**
在 `codeharbor/` 目錄外執行了 go 命令。

**測試失敗但程式碼沒問題**
見上面「跑測試」裡的只讀對比方法。
