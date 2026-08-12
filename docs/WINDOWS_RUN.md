# 在 Windows 上執行 Autoto（本機獨立例項）

這是 **Windows 自己的一套 Autoto**，資料在 Windows 使用者目錄，**不會**自動同步 Mac 上的專案。

## 1. 準備檔案

從本倉庫 `dist/` 複製對應架構的執行檔到 Windows，例如：

| 電腦架構 | 檔案 |
|----------|------|
| 普通 x64（大多數 Intel/AMD 筆記本） | `autoto-windows-amd64.exe` |
| ARM 版 Windows（部分 Surface） | `autoto-windows-arm64.exe` |

建議放到例如：

```text
C:\Users\<你的使用者名稱>\autoto\autoto.exe
```

（把 `autoto-windows-amd64.exe` 重新命名為 `autoto.exe` 即可。）

## 2. 啟動

在 PowerShell 或「命令提示符」中：

```bat
cd C:\Users\<你的使用者名稱>\autoto
.\autoto.exe
```

看到類似 `autoto listening` 後，用瀏覽器開啟：

```text
http://127.0.0.1:16888
```

## 3. 預設資料位置（Windows）

與 Mac 類似，落在使用者主目錄下：

```text
%USERPROFILE%\.autoto\config.json
%USERPROFILE%\.autoto\autoto.db
```

首次執行會自動建立配置與資料庫。

## 4. 配置模型 API

1. 開啟 http://127.0.0.1:16888  
2. 進入 **設定 → 模型 / 提供商**  
3. 填入你自己的 API Key 與 Base URL  

Mac 上的 Key **不會**自動過來，需要在 Windows 上重新配置（或自行匯出配置檔案再複製）。

## 5. 後台常駐（可選）

- 保持一個 PowerShell 視窗執行 `.\autoto.exe`  
- 或用「任務計劃程式」登入時啟動該 exe  
- 關閉視窗即停止服務（除非你做成 Windows 服務，本 MVP 預設不做）

## 6. 注意

- 需要 **Windows 10/11**；本檔案提供的是 **CLI 服務 + 瀏覽器 UI**，不是安裝版 `.msi`。  
- 桌面殼（獨立視窗）尚未提供成熟 Windows 安裝包。  
- 若 SmartScreen 攔截未簽名 exe：更多資訊 → 仍要執行（僅在你信任該檔案來源時）。  
- 防火牆若詢問，允許專用網路即可（本機訪問一般不需要公網放行）。

## 7. 停止

在執行 Autoto 的終端按 `Ctrl+C`。
