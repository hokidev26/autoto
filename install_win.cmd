@echo off
chcp 65001 >nul
echo.
echo  正在啟動 AutoTo 安裝程式...
echo.
powershell -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/hokidev26/autoto/main/install_win.ps1 | iex"
pause
