@echo off
chcp 65001 >nul
echo.
echo 🤖 AutoTo 一鍵安裝 (Windows)
echo ================================
echo.

set INSTALL_DIR=%USERPROFILE%\.autoto
set REPO_DIR=%INSTALL_DIR%\app
set BACKEND_DIR=%REPO_DIR%\backend
set WEB_UI_DIR=%REPO_DIR%\renderer

:: 1. 檢查 Python
echo 📦 檢查 Python...
set "PYTHON_CMD="
py -3.11 --version >nul 2>&1 && set "PYTHON_CMD=py -3.11"
if not defined PYTHON_CMD python -c "import sys; raise SystemExit(0 if sys.version_info >= (3, 11) else 1)" >nul 2>&1 && set "PYTHON_CMD=python"
if not defined PYTHON_CMD (
    echo   ❌ 未找到可用的 Python 3.11+
    echo   請先安裝 Python 3.11+ 並勾選 Add Python to PATH
    echo   https://www.python.org/downloads/
    echo.
    pause
    exit /b 1
)
echo   ✅ 使用 %PYTHON_CMD%

:: 2. 建立安裝目錄
echo.
echo 📁 建立安裝目錄...
if not exist "%INSTALL_DIR%" mkdir "%INSTALL_DIR%"
if not exist "%REPO_DIR%" mkdir "%REPO_DIR%"

:: 3. 複製應用程式檔案
echo 📋 安裝應用程式檔案...
if not exist "%~dp0..\..\backend" (
    echo   ❌ 找不到 backend 目錄
    pause
    exit /b 1
)
if not exist "%~dp0..\..\electron-app\renderer" (
    echo   ❌ 找不到 renderer 目錄
    pause
    exit /b 1
)
if exist "%BACKEND_DIR%" rmdir /S /Q "%BACKEND_DIR%"
if exist "%WEB_UI_DIR%" rmdir /S /Q "%WEB_UI_DIR%"
xcopy /E /I /Y /Q "%~dp0..\..\backend" "%BACKEND_DIR%" >nul
xcopy /E /I /Y /Q "%~dp0..\..\electron-app\renderer" "%WEB_UI_DIR%" >nul
if not exist "%BACKEND_DIR%\server.py" (
    echo   ❌ backend 複製失敗
    pause
    exit /b 1
)
if not exist "%WEB_UI_DIR%\index.html" (
    echo   ❌ renderer 複製失敗
    pause
    exit /b 1
)

:: 4. 建立虛擬環境
echo 🐍 建立 Python 虛擬環境...
%PYTHON_CMD% -m venv "%INSTALL_DIR%\venv"
set "VENV_PYTHON=%INSTALL_DIR%\venv\Scripts\python.exe"
if not exist "%VENV_PYTHON%" (
    echo   ❌ 建立虛擬環境失敗
    pause
    exit /b 1
)

:: 5. 安裝依賴
echo 📦 安裝依賴...
"%VENV_PYTHON%" -m pip install --quiet --upgrade pip
if errorlevel 1 (
    echo   ❌ pip 升級失敗
    pause
    exit /b 1
)
"%VENV_PYTHON%" -m pip install --quiet -r "%BACKEND_DIR%\requirements.txt"
if errorlevel 1 (
    echo   ❌ 安裝後端依賴失敗
    pause
    exit /b 1
)

:: 6. 安裝平台依賴
echo.
echo 📱 安裝所有平台依賴...
"%VENV_PYTHON%" -m pip install --quiet discord.py line-bot-sdk python-telegram-bot xmltodict slack-bolt
if errorlevel 1 (
    echo   ❌ 安裝平台依賴失敗
    pause
    exit /b 1
)

:: 7. 建立啟動腳本
(
echo @echo off
echo cd /d "%BACKEND_DIR%"
echo "%INSTALL_DIR%\venv\Scripts\python.exe" server.py %%*
) > "%INSTALL_DIR%\start.bat"
if not exist "%INSTALL_DIR%\start.bat" (
    echo   ❌ 建立啟動腳本失敗
    pause
    exit /b 1
)

:: 8. 建立桌面捷徑
echo 🔗 建立桌面捷徑...
powershell -Command "$desktop = [Environment]::GetFolderPath('Desktop'); $ws = New-Object -ComObject WScript.Shell; $s = $ws.CreateShortcut((Join-Path $desktop 'AutoTo.lnk')); $s.TargetPath = '%INSTALL_DIR%\start.bat'; $s.WorkingDirectory = '%BACKEND_DIR%'; $s.Description = 'AutoTo AI 助理'; $s.Save()"
if errorlevel 1 echo   ⚠️ 建立桌面捷徑失敗，但主程式已安裝完成

:: 9. 初始化配置
echo.
echo ⚙️ 初始化配置...
if not exist "%INSTALL_DIR%\config.json" (
    echo {"provider":"groq","apiKey":"","model":"llama-3.3-70b-versatile","customUrl":"","channels":{"discord":{"enabled":false,"token":""},"line":{"enabled":false,"channelAccessToken":"","channelSecret":""},"telegram":{"enabled":false,"botToken":""},"wechat":{"enabled":false,"appId":"","appSecret":""},"whatsapp":{"enabled":false,"phoneNumberId":"","accessToken":"","verifyToken":""},"slack":{"enabled":false,"botToken":"","signingSecret":""},"messenger":{"enabled":false,"pageAccessToken":"","verifyToken":""},"qq":{"enabled":false,"httpUrl":"http://127.0.0.1:5700","webhookPort":5683},"instagram":{"enabled":false,"accessToken":""}},"memory":{"enabled":true,"autoArchive":50},"agent":{"maxTokenBudget":4000,"compressionEnabled":true,"systemPrompt":"你是 AutoTo，一個智能 AI 助理。請用繁體中文回答，語氣友善親切。"},"session":{"persist":true}} > "%INSTALL_DIR%\config.json"
)
if not exist "%INSTALL_DIR%\config.json" (
    echo   ❌ 初始化配置失敗
    pause
    exit /b 1
)

echo.
echo ================================
echo 🎉 AutoTo 安裝完成！
echo.
echo 啟動方式：
echo   雙擊桌面的 AutoTo 捷徑
echo   或執行: %INSTALL_DIR%\start.bat
echo.
echo 接下來：
echo   1. 啟動 AutoTo 後端
echo   2. 開啟瀏覽器訪問 http://127.0.0.1:5678
echo   3. 在設定頁面配置 API Key
echo   4. 在平台頁面啟用想要的聊天平台
echo ================================
echo.
pause
