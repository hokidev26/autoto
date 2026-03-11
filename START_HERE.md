# 🎯 從這裡開始！

歡迎來到 AutoTo 專案！這是目前建議的安裝與啟動入口。

## ⚡ 3 分鐘快速開始

### 1. 執行安裝腳本

macOS：

```bash
bash install.sh
```

Windows：

```bat
installer\windows\install.bat
```

### 2. 啟動 AutoTo

安裝完成後：

```bash
autoto
```

若你是在 repo 目錄直接測試：

```bash
./start.sh
```

### 3. 打開瀏覽器並設定 API Key

在瀏覽器打開：

```
http://127.0.0.1:5678
```

你可以直接在設定頁面填入 API Key，或編輯：

```bash
open ~/.autoto/config.json
```

設定格式範例：

```json
{
  "provider": "groq",
  "apiKey": "你的KEY",
  "model": "llama-3.3-70b-versatile",
  "customUrl": ""
}
```

### 4. 確認服務正常

```bash
curl http://127.0.0.1:5678/api/status
```

成功！🎉

## 📚 完整文檔

| 文件 | 說明 | 時間 |
|------|------|------|
| [GET_STARTED.md](GET_STARTED.md) | 詳細入門指南 | 15 分鐘 |
| [LINE_INTEGRATION.md](LINE_INTEGRATION.md) | LINE 整合教學 | 10 分鐘 |
| [TW_TOOLS.md](TW_TOOLS.md) | 台灣工具說明 | 5 分鐘 |
| [DEVELOPMENT.md](DEVELOPMENT.md) | 開發指南 | 30 分鐘 |

完整文件清單：[FILES_OVERVIEW.md](FILES_OVERVIEW.md)

## 🎯 你想做什麼？

### 我想快速試用
→ 執行上面的「3 分鐘快速開始」

### 我想整合 LINE
→ 閱讀 [LINE_INTEGRATION.md](LINE_INTEGRATION.md)

### 我想開發功能
→ 閱讀 [DEVELOPMENT.md](DEVELOPMENT.md)

### 我想了解架構
→ 閱讀 [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md)

### 我遇到問題
→ 查看 [GET_STARTED.md](GET_STARTED.md) 的「常見問題」

## 💡 專案特色

✅ **LINE 整合** - 完整的 LINE 官方帳號整合程式碼  
✅ **台灣工具** - 天氣、發票、股票等本地化工具  
✅ **繁體中文** - 完整的繁體中文文檔和範例  
✅ **開箱即用** - 一鍵安裝腳本，快速部署  
✅ **可擴展** - 模組化設計，易於開發新功能  

## 🚀 下一步

1. ✅ 完成快速開始
2. 📱 整合 LINE（可選）
3. 🛠️ 加入台灣工具（可選）
4. 💻 開發自己的功能（可選）

## 📞 需要幫助？

- 📖 查看文檔
- 🐛 開 Issue
- 💬 加入討論

---

開始你的 AI 助理開發之旅吧！🚀
