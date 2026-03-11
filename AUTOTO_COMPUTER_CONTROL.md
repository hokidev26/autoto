# 🖥️ AutoTo 電腦操作功能

## 📋 功能概述

AutoTo 可以幫你操作電腦，執行各種自動化任務！

## ✅ 支援的操作

### 1. 文件操作
- ✅ 讀取文件
- ✅ 寫入文件
- ✅ 列出目錄
- ✅ 創建/刪除文件

### 2. 應用程式控制
- ✅ 開啟應用程式（Safari、Chrome、VS Code 等）
- ✅ 開啟網址
- ✅ 開啟 Finder
- ✅ 開啟終端機

### 3. Shell 命令
- ✅ 執行終端命令
- ✅ 安全模式（防止危險命令）
- ✅ 命令輸出捕獲

### 4. 系統資訊
- ✅ CPU 使用率
- ✅ 記憶體使用率
- ✅ 硬碟空間
- ✅ 系統資訊

### 5. Mac 專用功能
- ✅ 語音朗讀（say）
- ✅ 螢幕截圖
- ✅ Finder 操作

## 🚀 使用範例

### 對話範例

**你：** 幫我開啟 Safari
**AutoTo：** 好的，正在開啟 Safari... ✅ 已開啟

**你：** 查看系統資訊
**AutoTo：** 
- CPU 使用率：25%
- 記憶體使用率：60%
- 可用記憶體：8.5 GB
- 硬碟使用率：45%

**你：** 開啟 Google 首頁
**AutoTo：** 正在開啟 https://www.google.com... ✅ 已開啟

**你：** 執行命令：ls -la
**AutoTo：** 
```
total 128
drwxr-xr-x  25 user  staff   800 Feb 27 16:00 .
drwx------   7 user  staff   224 Feb 27 15:00 ..
...
```

**你：** 幫我截圖
**AutoTo：** ✅ 截圖已儲存：screenshot.png

## 📡 API 端點

### 執行命令
```bash
POST /api/computer/command
Body: {
  "command": "ls -la",
  "safe_mode": true
}
```

### 開啟應用程式
```bash
POST /api/computer/open-app
Body: {
  "app_name": "Safari"
}
```

### 開啟網址
```bash
POST /api/computer/open-url
Body: {
  "url": "https://www.google.com"
}
```

### 獲取系統資訊
```bash
GET /api/computer/system-info
```

### 讀取文件
```bash
POST /api/computer/read-file
Body: {
  "filepath": "/path/to/file.txt"
}
```

### 寫入文件
```bash
POST /api/computer/write-file
Body: {
  "filepath": "/path/to/file.txt",
  "content": "Hello AutoTo!"
}
```

### 截圖（Mac）
```bash
POST /api/computer/screenshot
Body: {
  "filepath": "screenshot.png"
}
```

## 🔒 安全機制

### 安全模式（預設開啟）
自動阻擋危險命令：
- ❌ `rm -rf` - 刪除所有文件
- ❌ `sudo` - 超級用戶權限
- ❌ `format` - 格式化
- ❌ `del /f` - 強制刪除

### 命令超時
- 所有命令限制 30 秒執行時間
- 防止無限循環或卡死

### 權限控制
- 只能操作用戶有權限的文件
- 無法執行需要 root 權限的操作

## 💡 實用場景

### 1. 自動化工作流程
```
你：幫我開啟 VS Code，然後開啟終端機
AutoTo：
✅ VS Code 已開啟
✅ 終端機已開啟
```

### 2. 系統監控
```
你：每小時告訴我系統資源使用情況
AutoTo：好的，我會定時檢查並通知你
```

### 3. 文件管理
```
你：幫我整理桌面的文件
AutoTo：正在分析桌面文件...
已將 15 個文件分類到對應資料夾
```

### 4. 快速操作
```
你：開啟 Google 搜尋「台北天氣」
AutoTo：✅ 已在瀏覽器中開啟搜尋結果
```

### 5. 批次處理
```
你：幫我把這個資料夾的所有圖片轉成 PNG
AutoTo：正在處理...
✅ 已轉換 25 張圖片
```

## 🎯 常用命令

### Mac 應用程式
```
Safari
Google Chrome
Visual Studio Code
Terminal
Finder
Notes
Calendar
Mail
```

### 常用命令
```bash
# 列出文件
ls -la

# 查看當前目錄
pwd

# 查看系統資訊
uname -a

# 查看網路狀態
ifconfig

# 查看進程
ps aux

# 查看硬碟空間
df -h
```

## 🔧 進階功能

### 1. 鏈式操作
```
你：開啟 Chrome，然後開啟 YouTube，搜尋「AutoTo 教學」
AutoTo：
✅ Chrome 已開啟
✅ YouTube 已開啟
✅ 搜尋結果已顯示
```

### 2. 條件執行
```
你：如果 CPU 使用率超過 80%，通知我
AutoTo：好的，我會持續監控
```

### 3. 定時任務
```
你：每天早上 9 點開啟 Safari 和 Mail
AutoTo：定時任務已設定
```

## ⚠️ 注意事項

### 安全性
1. **不要執行不明來源的命令**
2. **保持安全模式開啟**
3. **定期檢查執行歷史**
4. **不要分享敏感文件路徑**

### 隱私
1. AutoTo 不會主動讀取你的文件
2. 所有操作都需要你的明確指令
3. 不會上傳任何本地數據

### 限制
1. 無法執行需要 root 權限的操作
2. 無法操作系統核心功能
3. 某些應用程式可能需要手動授權

## 🚀 快速測試

### 測試 1：系統資訊
```bash
curl -X GET http://127.0.0.1:5678/api/computer/system-info
```

### 測試 2：開啟應用程式
```bash
curl -X POST http://127.0.0.1:5678/api/computer/open-app \
  -H "Content-Type: application/json" \
  -d '{"app_name":"Safari"}'
```

### 測試 3：執行命令
```bash
curl -X POST http://127.0.0.1:5678/api/computer/command \
  -H "Content-Type: application/json" \
  -d '{"command":"echo Hello AutoTo"}'
```

## 📊 功能對照表
  
| 功能 | 舊版流程 | AutoTo | 狀態 |
|------|----------|--------|------|
| 文件讀寫 | ✅ | ✅ | 完成 |
| Shell 命令 | ✅ | ✅ | 完成 |
| 應用程式控制 | ✅ | ✅ | 完成 |
| 系統資訊 | ✅ | ✅ | 完成 |
| 截圖功能 | ❌ | ✅ | 新增 |
| 語音朗讀 | ❌ | ✅ | 新增 |
| 安全模式 | ❌ | ✅ | 新增 |

## 🎓 學習資源
  
- AutoTo 專案文件：README_TW.md
- Python subprocess：https://docs.python.org/3/library/subprocess.html
- macOS 自動化：https://developer.apple.com/automation/

---

🐈 **AutoTo - 你的智能電腦助理**

現在就試試讓 AutoTo 幫你操作電腦吧！
