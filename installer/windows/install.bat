@echo off
chcp 65001 >nul
echo.
echo  AutoTo 一鍵安裝 (Windows)
echo  ================================
echo.

set INSTALL_DIR=%USERPROFILE%\.autoto
set REPO_DIR=%INSTALL_DIR%\app
set BACKEND_DIR=%REPO_DIR%\backend
set WEB_UI_DIR=%REPO_DIR%\renderer

:: 1. 檢查 Python
echo  [1/8] 檢查 Python...
set "PYTHON_CMD="
py -3.11 --version >nul 2>&1 && set "PYTHON_CMD=py -3.11"
if not defined PYTHON_CMD py -3.12 --version >nul 2>&1 && set "PYTHON_CMD=py -3.12"
if not defined PYTHON_CMD py -3.13 --version >nul 2>&1 && set "PYTHON_CMD=py -3.13"
if not defined PYTHON_CMD python -c "import sys; raise SystemExit(0 if sys.version_info >= (3, 11) else 1)" >nul 2>&1 && set "PYTHON_CMD=python"
if not defined PYTHON_CMD (
    echo   [X] 未找到 Python 3.11+
    echo   請先安裝 Python 3.11+ 並勾選 Add Python to PATH
    echo   https://www.python.org/downloads/
    echo.
    pause
    exit /b 1
)
echo   [OK] 使用 %PYTHON_CMD%

:: 2. 檢查 ffmpeg（攝影機功能需要，非必要）
echo.
echo  [2/8] 檢查 ffmpeg...
where ffmpeg >nul 2>&1
if errorlevel 1 (
    echo   [!] 未找到 ffmpeg，攝影機監控功能將無法使用
    echo   如需使用，請安裝: https://ffmpeg.org/download.html
) else (
    echo   [OK] ffmpeg 已安裝
)

:: 3. 建立安裝目錄
echo.
echo  [3/8] 建立安裝目錄...
if not exist "%INSTALL_DIR%" mkdir "%INSTALL_DIR%"
if not exist "%REPO_DIR%" mkdir "%REPO_DIR%"

:: 4. 複製應用程式檔案
echo  [4/8] 安裝應用程式檔案...
if not exist "%~dp0..\..\backend" (
    echo   [X] 找不到 backend 目錄
    pause
    exit /b 1
)
if not exist "%~dp0..\..\electron-app\renderer" (
    echo   [X] 找不到 renderer 目錄
    pause
    exit /b 1
)
if exist "%BACKEND_DIR%" rmdir /S /Q "%BACKEND_DIR%"
if exist "%WEB_UI_DIR%" rmdir /S /Q "%WEB_UI_DIR%"
xcopy /E /I /Y /Q "%~dp0..\..\backend" "%BACKEND_DIR%" >nul
xcopy /E /I /Y /Q "%~dp0..\..\electron-app\renderer" "%WEB_UI_DIR%" >nul
:: 也複製啟動腳本
if exist "%~dp0..\..\start.sh" copy /Y "%~dp0..\..\start.sh" "%REPO_DIR%\" >nul
if not exist "%BACKEND_DIR%\server.py" (
    echo   [X] backend 複製失敗
    pause
    exit /b 1
)
if not exist "%WEB_UI_DIR%\index.html" (
    echo   [X] renderer 複製失敗
    pause
    exit /b 1
)

:: 5. 建立虛擬環境
echo.
echo  [5/8] 建立 Python 虛擬環境...
%PYTHON_CMD% -m venv "%INSTALL_DIR%\venv"
set "VENV_PYTHON=%INSTALL_DIR%\venv\Scripts\python.exe"
if not exist "%VENV_PYTHON%" (
    echo   [X] 建立虛擬環境失敗
    pause
    exit /b 1
)

:: 6. 安裝依賴
echo  [6/8] 安裝依賴...
"%VENV_PYTHON%" -m pip install --quiet --upgrade pip
if errorlevel 1 (
    echo   [X] pip 升級失敗
    pause
    exit /b 1
)
"%VENV_PYTHON%" -m pip install --quiet -r "%BACKEND_DIR%\requirements.txt"
if errorlevel 1 (
    echo   [X] 安裝後端依賴失敗
    pause
    exit /b 1
)

:: 7. 安裝平台依賴（可選）
echo.
echo  [7/8] 安裝平台依賴...
"%VENV_PYTHON%" -m pip install --quiet discord.py line-bot-sdk python-telegram-bot xmltodict slack-bolt 2>nul
if errorlevel 1 (
    echo   [!] 部分平台依賴安裝失敗，不影響主程式使用
) else (
    echo   [OK] 平台依賴安裝完成
)

:: 8. 建立啟動腳本
echo.
echo  [8/8] 建立啟動腳本...

:: 主啟動腳本（含自動開瀏覽器）
(
echo @echo off
echo chcp 65001 ^>nul
echo set "PORT=%%1"
echo if "%%PORT%%"=="" set "PORT=5678"
echo echo.
echo echo  AutoTo 啟動中...
echo echo  瀏覽器介面: http://127.0.0.1:%%PORT%%
echo echo  按 Ctrl+C 停止
echo echo.
echo cd /d "%BACKEND_DIR%"
echo start "" "http://127.0.0.1:%%PORT%%"
echo "%INSTALL_DIR%\venv\Scripts\python.exe" server.py --port %%PORT%%
) > "%INSTALL_DIR%\start.bat"

if not exist "%INSTALL_DIR%\start.bat" (
    echo   [X] 建立啟動腳本失敗
    pause
    exit /b 1
)

:: 建立桌面捷徑
echo  建立桌面捷徑...
powershell -Command "$desktop = [Environment]::GetFolderPath('Desktop'); $ws = New-Object -ComObject WScript.Shell; $s = $ws.CreateShortcut((Join-Path $desktop 'AutoTo.lnk')); $s.TargetPath = '%INSTALL_DIR%\start.bat'; $s.WorkingDirectory = '%BACKEND_DIR%'; $s.Description = 'AutoTo AI 助理'; $s.Save()" 2>nul
if errorlevel 1 echo   [!] 建立桌面捷徑失敗，但主程式已安裝完成

echo.
echo  ================================
echo  AutoTo 安裝完成！
echo  ================================
echo.
echo  啟動方式：
echo    1. 雙擊桌面的 AutoTo 捷徑
echo    2. 或執行: %INSTALL_DIR%\start.bat
echo.
echo  首次使用：
echo    1. 啟動後瀏覽器會自動開啟
echo    2. 在設定頁面配置 API Key（推薦 Groq 免費）
echo    3. 開始對話！
echo.
echo  安裝位置: %INSTALL_DIR%
echo  ================================
echo.
pause
