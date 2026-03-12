# ============================================
# AutoTo 一鍵安裝腳本 (Windows PowerShell)
# 使用方式: irm https://raw.githubusercontent.com/hokidev26/autoto/main/install_win.ps1 | iex
# ============================================

[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$ErrorActionPreference = "Stop"

Write-Host ""
Write-Host " AutoTo 一鍵安裝 (Windows)" -ForegroundColor Cyan
Write-Host " ================================" -ForegroundColor Cyan
Write-Host ""

$INSTALL_DIR = "$env:USERPROFILE\.autoto"
$REPO_DIR = "$INSTALL_DIR\app"
$BACKEND_DIR = "$REPO_DIR\backend"
$WEB_UI_DIR = "$REPO_DIR\renderer"
$TEMP_ZIP = "$env:TEMP\autoto.zip"
$TEMP_EXTRACT = "$env:TEMP\autoto-main"

# 1. 檢查 Python
Write-Host " [1/8] 檢查 Python..." -ForegroundColor Yellow
$PYTHON_CMD = $null

# 嘗試 py launcher
foreach ($ver in @("3.13", "3.12", "3.11")) {
    try {
        $null = & py "-$ver" --version 2>&1
        if ($LASTEXITCODE -eq 0) { $PYTHON_CMD = "py -$ver"; break }
    } catch {}
}

# 嘗試 python
if (-not $PYTHON_CMD) {
    try {
        $pyVer = & python -c "import sys; print(sys.version_info >= (3, 11))" 2>&1
        if ($pyVer -eq "True") { $PYTHON_CMD = "python" }
    } catch {}
}

if (-not $PYTHON_CMD) {
    Write-Host "  [X] 未找到 Python 3.11+" -ForegroundColor Red
    Write-Host ""
    Write-Host "  請先安裝 Python 3.11+：" -ForegroundColor White
    Write-Host "  https://www.python.org/downloads/" -ForegroundColor White
    Write-Host "  安裝時請勾選 'Add Python to PATH'" -ForegroundColor White
    Write-Host ""
    Read-Host "按 Enter 結束"
    exit 1
}

$pyVersion = & $PYTHON_CMD.Split()[0] $PYTHON_CMD.Split()[1..9] --version 2>&1
Write-Host "  [OK] $pyVersion" -ForegroundColor Green

# 2. 檢查 ffmpeg
Write-Host ""
Write-Host " [2/8] 檢查 ffmpeg..." -ForegroundColor Yellow
if (Get-Command ffmpeg -ErrorAction SilentlyContinue) {
    Write-Host "  [OK] ffmpeg 已安裝" -ForegroundColor Green
} else {
    Write-Host "  [!] 未找到 ffmpeg，攝影機功能將無法使用" -ForegroundColor DarkYellow
    Write-Host "  如需使用: https://ffmpeg.org/download.html"
}

# 3. 下載專案
Write-Host ""
Write-Host " [3/8] 下載 AutoTo..." -ForegroundColor Yellow
$REPO_URL = "https://github.com/hokidev26/autoto/archive/refs/heads/main.zip"
try {
    if (Test-Path $TEMP_ZIP) { Remove-Item $TEMP_ZIP -Force }
    if (Test-Path $TEMP_EXTRACT) { Remove-Item $TEMP_EXTRACT -Recurse -Force }
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $REPO_URL -OutFile $TEMP_ZIP -UseBasicParsing
    Write-Host "  [OK] 下載完成" -ForegroundColor Green
} catch {
    Write-Host "  [X] 下載失敗，請檢查網路連線" -ForegroundColor Red
    Write-Host "  $($_.Exception.Message)"
    Read-Host "按 Enter 結束"
    exit 1
}

# 4. 解壓並安裝檔案
Write-Host ""
Write-Host " [4/8] 安裝應用程式檔案..." -ForegroundColor Yellow
try {
    Expand-Archive -Path $TEMP_ZIP -DestinationPath $env:TEMP -Force
    if (-not (Test-Path "$TEMP_EXTRACT\backend")) {
        # GitHub zip 可能帶 repo 名前綴
        $extracted = Get-ChildItem "$env:TEMP\autoto-*" -Directory | Select-Object -First 1
        if ($extracted) { $TEMP_EXTRACT = $extracted.FullName }
    }

    # 建立安裝目錄
    New-Item -ItemType Directory -Path $INSTALL_DIR -Force | Out-Null
    New-Item -ItemType Directory -Path $REPO_DIR -Force | Out-Null

    # 複製檔案
    if (Test-Path $BACKEND_DIR) { Remove-Item $BACKEND_DIR -Recurse -Force }
    if (Test-Path $WEB_UI_DIR) { Remove-Item $WEB_UI_DIR -Recurse -Force }
    Copy-Item "$TEMP_EXTRACT\backend" $BACKEND_DIR -Recurse
    Copy-Item "$TEMP_EXTRACT\electron-app\renderer" $WEB_UI_DIR -Recurse

    # 清理暫存
    Remove-Item $TEMP_ZIP -Force -ErrorAction SilentlyContinue
    Remove-Item $TEMP_EXTRACT -Recurse -Force -ErrorAction SilentlyContinue

    Write-Host "  [OK] 檔案安裝完成" -ForegroundColor Green
} catch {
    Write-Host "  [X] 解壓或複製失敗" -ForegroundColor Red
    Write-Host "  $($_.Exception.Message)"
    Read-Host "按 Enter 結束"
    exit 1
}

# 5. 建立虛擬環境
Write-Host ""
Write-Host " [5/8] 建立 Python 虛擬環境..." -ForegroundColor Yellow
$venvArgs = @("-m", "venv", "$INSTALL_DIR\venv")
if ($PYTHON_CMD -like "py *") {
    $pyExe = $PYTHON_CMD.Split()[0]
    $pyArgs = @($PYTHON_CMD.Split()[1]) + $venvArgs
    & $pyExe @pyArgs
} else {
    & $PYTHON_CMD @venvArgs
}

$VENV_PYTHON = "$INSTALL_DIR\venv\Scripts\python.exe"
if (-not (Test-Path $VENV_PYTHON)) {
    Write-Host "  [X] 建立虛擬環境失敗" -ForegroundColor Red
    Read-Host "按 Enter 結束"
    exit 1
}
Write-Host "  [OK] 虛擬環境建立完成" -ForegroundColor Green

# 6. 安裝依賴
Write-Host ""
Write-Host " [6/8] 安裝依賴（可能需要幾分鐘）..." -ForegroundColor Yellow
& $VENV_PYTHON -m pip install --quiet --upgrade pip 2>&1 | Out-Null
& $VENV_PYTHON -m pip install --quiet -r "$BACKEND_DIR\requirements.txt"
if ($LASTEXITCODE -ne 0) {
    Write-Host "  [X] 安裝依賴失敗" -ForegroundColor Red
    Read-Host "按 Enter 結束"
    exit 1
}
Write-Host "  [OK] 依賴安裝完成" -ForegroundColor Green

# 7. 安裝平台依賴
Write-Host ""
Write-Host " [7/8] 安裝平台依賴..." -ForegroundColor Yellow
& $VENV_PYTHON -m pip install --quiet discord.py line-bot-sdk python-telegram-bot xmltodict slack-bolt 2>&1 | Out-Null
if ($LASTEXITCODE -ne 0) {
    Write-Host "  [!] 部分平台依賴安裝失敗，不影響主程式" -ForegroundColor DarkYellow
} else {
    Write-Host "  [OK] 平台依賴安裝完成" -ForegroundColor Green
}

# 8. 建立啟動腳本與桌面捷徑
Write-Host ""
Write-Host " [8/8] 建立啟動腳本與桌面捷徑..." -ForegroundColor Yellow

# 下載圖示
$ICO_URL = "https://raw.githubusercontent.com/hokidev26/autoto/main/autoto.ico"
$ICO_PATH = "$INSTALL_DIR\autoto.ico"
try {
    Invoke-WebRequest -Uri $ICO_URL -OutFile $ICO_PATH -UseBasicParsing 2>$null
} catch {}

# 啟動腳本（延遲 3 秒再開瀏覽器，等 server 啟動）
$startBat = @"
@echo off
chcp 65001 >nul
set "PORT=%1"
if "%PORT%"=="" set "PORT=5678"
echo.
echo  AutoTo 啟動中...
echo  瀏覽器介面: http://127.0.0.1:%PORT%
echo  按 Ctrl+C 停止
echo.
cd /d "$BACKEND_DIR"
start /b cmd /c "ping -n 4 127.0.0.1 >nul && start http://127.0.0.1:%PORT%"
"$INSTALL_DIR\venv\Scripts\python.exe" server.py --port %PORT%
"@
Set-Content -Path "$INSTALL_DIR\start.bat" -Value $startBat -Encoding UTF8

# 桌面捷徑
try {
    $desktop = [Environment]::GetFolderPath("Desktop")
    $ws = New-Object -ComObject WScript.Shell
    $shortcut = $ws.CreateShortcut("$desktop\AutoTo.lnk")
    $shortcut.TargetPath = "$INSTALL_DIR\start.bat"
    $shortcut.WorkingDirectory = $BACKEND_DIR
    $shortcut.Description = "AutoTo AI 助理"
    if (Test-Path $ICO_PATH) { $shortcut.IconLocation = $ICO_PATH }
    $shortcut.Save()
    Write-Host "  [OK] 桌面捷徑已建立" -ForegroundColor Green
} catch {
    Write-Host "  [!] 桌面捷徑建立失敗，但主程式已安裝" -ForegroundColor DarkYellow
}

# 初始化配置
if (-not (Test-Path "$INSTALL_DIR\config.json")) {
    $config = @'
{
  "provider": "groq",
  "apiKey": "",
  "model": "llama-3.3-70b-versatile",
  "customUrl": "",
  "channels": {
    "discord": {"enabled": false, "token": ""},
    "line": {"enabled": false, "channelAccessToken": "", "channelSecret": ""},
    "telegram": {"enabled": false, "botToken": ""},
    "wechat": {"enabled": false, "appId": "", "appSecret": ""},
    "whatsapp": {"enabled": false, "phoneNumberId": "", "accessToken": "", "verifyToken": ""},
    "slack": {"enabled": false, "botToken": "", "signingSecret": ""},
    "messenger": {"enabled": false, "pageAccessToken": "", "verifyToken": ""},
    "qq": {"enabled": false, "httpUrl": "http://127.0.0.1:5700", "webhookPort": 5683},
    "instagram": {"enabled": false, "accessToken": ""}
  },
  "memory": {"enabled": true, "autoArchive": 50},
  "agent": {"maxTokenBudget": 4000, "compressionEnabled": true, "systemPrompt": "你是 AutoTo，一個智能 AI 助理。請用繁體中文回答，語氣友善親切。"},
  "session": {"persist": true},
  "cameras": [],
  "smarthome": {"platforms": []}
}
'@
    Set-Content -Path "$INSTALL_DIR\config.json" -Value $config -Encoding UTF8
}

Write-Host ""
Write-Host " ================================" -ForegroundColor Cyan
Write-Host " AutoTo 安裝完成！" -ForegroundColor Cyan
Write-Host " ================================" -ForegroundColor Cyan
Write-Host ""
Write-Host " 啟動方式：" -ForegroundColor White
Write-Host "   1. 雙擊桌面的 AutoTo 捷徑" -ForegroundColor White
Write-Host "   2. 或執行: $INSTALL_DIR\start.bat" -ForegroundColor White
Write-Host ""
Write-Host " 首次使用：" -ForegroundColor White
Write-Host "   1. 啟動後瀏覽器會自動開啟" -ForegroundColor White
Write-Host "   2. 在設定頁面配置 API Key（推薦 Groq 免費）" -ForegroundColor White
Write-Host "   3. 開始對話！" -ForegroundColor White
Write-Host ""
Write-Host " 安裝位置: $INSTALL_DIR" -ForegroundColor White
Write-Host " ================================" -ForegroundColor Cyan
Write-Host ""
Read-Host "按 Enter 結束"
