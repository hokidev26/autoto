# ============================================
# AutoTo 一鍵安裝腳本 (Windows PowerShell)
# 使用方式:
#   PowerShell: irm https://raw.githubusercontent.com/hokidev26/autoto/main/install_win.ps1 | iex
#   CMD: powershell -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/hokidev26/autoto/main/install_win.ps1 | iex"
# ============================================

try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch {}

Write-Host ""
Write-Host " AutoTo 一鍵安裝 (Windows)" -ForegroundColor Cyan
Write-Host " ================================" -ForegroundColor Cyan
Write-Host ""

$INSTALL_DIR = Join-Path $env:USERPROFILE ".autoto"
$REPO_DIR = Join-Path $INSTALL_DIR "app"
$BACKEND_DIR = Join-Path $REPO_DIR "backend"
$WEB_UI_DIR = Join-Path $REPO_DIR "renderer"
$TEMP_ZIP = Join-Path $env:TEMP "autoto.zip"
$TEMP_EXTRACT = Join-Path $env:TEMP "autoto-main"

Write-Host " 安裝目錄: $INSTALL_DIR" -ForegroundColor Gray
Write-Host ""

# ---- 1. 檢查 Python ----
Write-Host " [1/8] 檢查 Python..." -ForegroundColor Yellow
$PYTHON_CMD = $null

# 嘗試 py launcher
foreach ($ver in @("3.13", "3.12", "3.11")) {
    try {
        $out = & py "-$ver" --version 2>&1
        if ($LASTEXITCODE -eq 0) {
            $PYTHON_CMD = "py"
            $PYTHON_ARGS = @("-$ver")
            Write-Host "  [OK] $out" -ForegroundColor Green
            break
        }
    } catch {}
}

# 嘗試 python
if (-not $PYTHON_CMD) {
    try {
        $check = & python -c "import sys; print(sys.version_info >= (3, 11))" 2>&1
        if ("$check".Trim() -eq "True") {
            $PYTHON_CMD = "python"
            $PYTHON_ARGS = @()
            $pv = & python --version 2>&1
            Write-Host "  [OK] $pv" -ForegroundColor Green
        }
    } catch {}
}

if (-not $PYTHON_CMD) {
    Write-Host "  [X] 未找到 Python 3.11+" -ForegroundColor Red
    Write-Host ""
    Write-Host "  請先安裝 Python 3.11+：" -ForegroundColor White
    Write-Host "  https://www.python.org/downloads/" -ForegroundColor White
    Write-Host "  安裝時請務必勾選 'Add Python to PATH'" -ForegroundColor White
    Write-Host ""
    Read-Host "按 Enter 結束"
    return
}

# ---- 2. 檢查 ffmpeg ----
Write-Host ""
Write-Host " [2/8] 檢查 ffmpeg..." -ForegroundColor Yellow
if (Get-Command ffmpeg -ErrorAction SilentlyContinue) {
    Write-Host "  [OK] ffmpeg 已安裝" -ForegroundColor Green
} else {
    Write-Host "  [!] 未找到 ffmpeg，攝影機功能將無法使用" -ForegroundColor DarkYellow
}

# ---- 3. 下載專案 ----
Write-Host ""
Write-Host " [3/8] 下載 AutoTo..." -ForegroundColor Yellow
$REPO_URL = "https://github.com/hokidev26/autoto/archive/refs/heads/main.zip"
try {
    if (Test-Path $TEMP_ZIP) { Remove-Item $TEMP_ZIP -Force }
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $REPO_URL -OutFile $TEMP_ZIP -UseBasicParsing
    if (-not (Test-Path $TEMP_ZIP)) { throw "ZIP 檔案不存在" }
    Write-Host "  [OK] 下載完成" -ForegroundColor Green
} catch {
    Write-Host "  [X] 下載失敗: $($_.Exception.Message)" -ForegroundColor Red
    Read-Host "按 Enter 結束"
    return
}

# ---- 4. 解壓並安裝檔案 ----
Write-Host ""
Write-Host " [4/8] 安裝應用程式檔案..." -ForegroundColor Yellow
try {
    # 清理舊的解壓目錄
    Get-ChildItem $env:TEMP -Directory -Filter "autoto-*" -ErrorAction SilentlyContinue | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue

    Expand-Archive -Path $TEMP_ZIP -DestinationPath $env:TEMP -Force

    # 找到解壓後的目錄
    $extracted = Get-ChildItem $env:TEMP -Directory -Filter "autoto-*" | Select-Object -First 1
    if (-not $extracted) { throw "解壓後找不到 autoto 目錄" }
    $TEMP_EXTRACT = $extracted.FullName
    Write-Host "  解壓到: $TEMP_EXTRACT" -ForegroundColor Gray

    if (-not (Test-Path (Join-Path $TEMP_EXTRACT "backend"))) { throw "找不到 backend 目錄" }
    if (-not (Test-Path (Join-Path $TEMP_EXTRACT "electron-app\renderer"))) { throw "找不到 renderer 目錄" }

    # 建立安裝目錄
    New-Item -ItemType Directory -Path $REPO_DIR -Force | Out-Null

    # 複製檔案
    if (Test-Path $BACKEND_DIR) { Remove-Item $BACKEND_DIR -Recurse -Force }
    if (Test-Path $WEB_UI_DIR) { Remove-Item $WEB_UI_DIR -Recurse -Force }
    Copy-Item (Join-Path $TEMP_EXTRACT "backend") $BACKEND_DIR -Recurse
    Copy-Item (Join-Path $TEMP_EXTRACT "electron-app\renderer") $WEB_UI_DIR -Recurse

    # 驗證
    if (-not (Test-Path (Join-Path $BACKEND_DIR "server.py"))) { throw "backend/server.py 複製失敗" }
    if (-not (Test-Path (Join-Path $WEB_UI_DIR "index.html"))) { throw "renderer/index.html 複製失敗" }

    # 清理暫存
    Remove-Item $TEMP_ZIP -Force -ErrorAction SilentlyContinue
    Remove-Item $TEMP_EXTRACT -Recurse -Force -ErrorAction SilentlyContinue

    Write-Host "  [OK] 檔案安裝完成" -ForegroundColor Green
} catch {
    Write-Host "  [X] 安裝失敗: $($_.Exception.Message)" -ForegroundColor Red
    Read-Host "按 Enter 結束"
    return
}

# ---- 5. 建立虛擬環境 ----
Write-Host ""
Write-Host " [5/8] 建立 Python 虛擬環境..." -ForegroundColor Yellow
$VENV_DIR = Join-Path $INSTALL_DIR "venv"
$VENV_PYTHON = Join-Path $VENV_DIR "Scripts\python.exe"
try {
    $venvArgs = $PYTHON_ARGS + @("-m", "venv", $VENV_DIR)
    & $PYTHON_CMD @venvArgs 2>&1
    if (-not (Test-Path $VENV_PYTHON)) { throw "python.exe 不存在" }
    Write-Host "  [OK] 虛擬環境建立完成" -ForegroundColor Green
} catch {
    Write-Host "  [X] 建立虛擬環境失敗: $($_.Exception.Message)" -ForegroundColor Red
    Read-Host "按 Enter 結束"
    return
}

# ---- 6. 安裝依賴 ----
Write-Host ""
Write-Host " [6/8] 安裝依賴（可能需要幾分鐘）..." -ForegroundColor Yellow
try {
    & $VENV_PYTHON -m pip install --upgrade pip 2>&1 | Out-Null
    $reqFile = Join-Path $BACKEND_DIR "requirements.txt"
    & $VENV_PYTHON -m pip install -r $reqFile 2>&1
    if ($LASTEXITCODE -ne 0) { throw "pip install 失敗" }
    Write-Host "  [OK] 依賴安裝完成" -ForegroundColor Green
} catch {
    Write-Host "  [X] 安裝依賴失敗: $($_.Exception.Message)" -ForegroundColor Red
    Read-Host "按 Enter 結束"
    return
}

# ---- 7. 安裝平台依賴 ----
Write-Host ""
Write-Host " [7/8] 安裝平台依賴..." -ForegroundColor Yellow
& $VENV_PYTHON -m pip install --quiet discord.py line-bot-sdk python-telegram-bot xmltodict slack-bolt 2>&1 | Out-Null
if ($LASTEXITCODE -ne 0) {
    Write-Host "  [!] 部分平台依賴安裝失敗，不影響主程式" -ForegroundColor DarkYellow
} else {
    Write-Host "  [OK] 平台依賴安裝完成" -ForegroundColor Green
}

# ---- 8. 建立啟動腳本與桌面捷徑 ----
Write-Host ""
Write-Host " [8/8] 建立啟動腳本與桌面捷徑..." -ForegroundColor Yellow

# 下載圖示
$ICO_PATH = Join-Path $INSTALL_DIR "autoto.ico"
try {
    $ICO_URL = "https://raw.githubusercontent.com/hokidev26/autoto/main/autoto.ico"
    Invoke-WebRequest -Uri $ICO_URL -OutFile $ICO_PATH -UseBasicParsing 2>$null
} catch {}

# 啟動腳本
$startBatContent = @"
@echo off
chcp 65001 >nul
set "PORT=%~1"
if "%PORT%"=="" set "PORT=5678"
echo.
echo  AutoTo 啟動中...
echo  瀏覽器介面: http://127.0.0.1:%PORT%
echo  按 Ctrl+C 停止
echo.
cd /d "$BACKEND_DIR"
start /b cmd /c "ping -n 4 127.0.0.1 >nul && start http://127.0.0.1:%PORT%"
"$VENV_PYTHON" server.py --port %PORT%
pause
"@
$startBatPath = Join-Path $INSTALL_DIR "start.bat"
Set-Content -Path $startBatPath -Value $startBatContent -Encoding ASCII

# 桌面捷徑
try {
    $desktop = [Environment]::GetFolderPath("Desktop")
    $ws = New-Object -ComObject WScript.Shell
    $lnkPath = Join-Path $desktop "AutoTo.lnk"
    $shortcut = $ws.CreateShortcut($lnkPath)
    $shortcut.TargetPath = $startBatPath
    $shortcut.WorkingDirectory = $BACKEND_DIR
    $shortcut.Description = "AutoTo AI 助理"
    if (Test-Path $ICO_PATH) { $shortcut.IconLocation = $ICO_PATH + ",0" }
    $shortcut.Save()
    Write-Host "  [OK] 桌面捷徑已建立" -ForegroundColor Green
} catch {
    Write-Host "  [!] 桌面捷徑建立失敗: $($_.Exception.Message)" -ForegroundColor DarkYellow
}

# 初始化配置
$configPath = Join-Path $INSTALL_DIR "config.json"
if (-not (Test-Path $configPath)) {
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
  "agent": {"maxTokenBudget": 4000, "compressionEnabled": true, "systemPrompt": "你是 AutoTo，一個開源跨平台 AI 助理。請用繁體中文回答，語氣友善親切。AutoTo 支援 macOS 和 Windows，GitHub: https://github.com/hokidev26/autoto。你不是某個特定作業系統的程式，你是 Web AI 助理。不要編造你不知道的資訊。"},
  "session": {"persist": true},
  "cameras": [],
  "smarthome": {"platforms": []}
}
'@
    Set-Content -Path $configPath -Value $config -Encoding UTF8
}

# 驗證安裝
Write-Host ""
Write-Host " 驗證安裝..." -ForegroundColor Yellow
$allOk = $true
if (-not (Test-Path $VENV_PYTHON)) { Write-Host "  [X] venv python 不存在" -ForegroundColor Red; $allOk = $false }
if (-not (Test-Path (Join-Path $BACKEND_DIR "server.py"))) { Write-Host "  [X] server.py 不存在" -ForegroundColor Red; $allOk = $false }
if (-not (Test-Path (Join-Path $WEB_UI_DIR "index.html"))) { Write-Host "  [X] index.html 不存在" -ForegroundColor Red; $allOk = $false }
if (-not (Test-Path $startBatPath)) { Write-Host "  [X] start.bat 不存在" -ForegroundColor Red; $allOk = $false }

if ($allOk) {
    Write-Host "  [OK] 所有檔案驗證通過" -ForegroundColor Green
    Write-Host ""
    Write-Host " ================================" -ForegroundColor Cyan
    Write-Host " AutoTo 安裝完成！" -ForegroundColor Cyan
    Write-Host " ================================" -ForegroundColor Cyan
    Write-Host ""
    Write-Host " 啟動方式：" -ForegroundColor White
    Write-Host "   1. 雙擊桌面的 AutoTo 捷徑" -ForegroundColor White
    Write-Host "   2. 或執行: $startBatPath" -ForegroundColor White
    Write-Host ""
    Write-Host " 首次使用：" -ForegroundColor White
    Write-Host "   1. 啟動後瀏覽器會自動開啟" -ForegroundColor White
    Write-Host "   2. 在設定頁面配置 API Key（推薦 Groq 免費）" -ForegroundColor White
    Write-Host "   3. 開始對話！" -ForegroundColor White
    Write-Host ""
    Write-Host " 安裝位置: $INSTALL_DIR" -ForegroundColor White
    Write-Host " ================================" -ForegroundColor Cyan
} else {
    Write-Host ""
    Write-Host " [X] 安裝不完整，請截圖上方訊息回報問題" -ForegroundColor Red
}

Write-Host ""
Read-Host "按 Enter 結束"
