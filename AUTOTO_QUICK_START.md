# 🚀 AutoTo 快速開始指南

## 📋 現在的標準入口

AutoTo 已改成由後端直接提供 Web UI：

- 安裝入口：`install.sh` / `installer/windows/install.bat`
- 啟動入口：`backend/server.py`
- 瀏覽器入口：`http://127.0.0.1:5678`
- 設定檔：`~/.autoto/config.json`

## ⚡ 快速啟動（3 步驟）

### 步驟 1：安裝

macOS：

```bash
bash install.sh
```

Windows：

```bat
installer\windows\install.bat
```

### 步驟 2：啟動 AutoTo

安裝完成後：

```bash
autoto
```

若你是在 repo 目錄中直接測試：

```bash
./start.sh
```

### 步驟 3：打開瀏覽器並完成設定

在瀏覽器中打開：

```
http://127.0.0.1:5678
```

接著：

1. 點擊 **⚙️ 設定**
2. 選擇 API Provider
3. 輸入你自己的 API Key
4. 點擊 **儲存設定**
5. 開始聊天

## 🎯 功能測試

### 測試基本對話
```
你好
AutoTo 是什麼？
```

### 測試快速操作
點擊界面上的快速按鈕：
- 🌤️ 查詢天氣
- 🎫 發票對獎
- 📈 查詢股價

### 測試設定功能
1. 點擊 **⚙️ 設定**
2. 切換不同的 API 服務商
3. 測試連線
4. 開關聊天平台

## 📊 API 端點

後端提供以下 API：

### 基礎 API
- `GET /api/status` - 服務狀態
- `GET /api/config` - 獲取配置
- `POST /api/config` - 更新配置
- `POST /api/chat` - 發送訊息
- `POST /api/test` - 測試連線

### 記憶 API
- `GET /api/memories` - 獲取記憶列表
- `POST /api/memories` - 添加記憶

### 台灣工具 API
- `POST /api/tools/weather` - 查詢天氣
- `POST /api/tools/invoice` - 發票對獎
- `POST /api/tools/stock` - 查詢股價

## 🔧 測試 API

使用 curl 測試：

```bash
# 測試狀態
curl http://127.0.0.1:5678/api/status

# 測試聊天
curl -X POST http://127.0.0.1:5678/api/chat \
  -H "Content-Type: application/json" \
  -d '{"message":"你好"}'
```

## 🐛 常見問題

### Q: 後端啟動失敗？
A:
1. 確認依賴已安裝完成
2. 確認使用的是安裝器建立的 venv
3. 在 repo 內可先執行 `python3 -m pip install -r backend/requirements.txt`

### Q: 前端無法連接後端？
A: 確認：
1. 後端服務正在運行（http://127.0.0.1:5678）
2. 瀏覽器沒有擋掉本地請求
3. 防火牆沒有阻擋

### Q: API Key 無效？
A:
1. 檢查 API Key 是否正確
2. 確認服務商選擇正確
3. 檢查是否需要設定 `customUrl`

### Q: 界面顯示異常？
A:
1. 重新整理瀏覽器
2. 清除瀏覽器快取
3. 查看後端日誌

## 📈 下一步開發

### 優先級 P0（立即）
- [x] 基礎界面
- [x] 後端 API
- [ ] 整合核心 agent loop
- [ ] 實際 LLM 調用

### 優先級 P1（本週）
- [ ] WebSocket 即時通訊
- [ ] 記憶管理界面
- [ ] 台灣工具實作

### 優先級 P2（本月）
- [ ] 技能管理
- [ ] MCP 整合
- [ ] 定時任務
- [ ] LINE 整合

## 🎨 自定義界面

### 修改配色
編輯 `electron-app/renderer/index.html` 與對應樣式中的 CSS 變數：

```css
:root {
    --primary: #667eea;      /* 主色 */
    --secondary: #764ba2;    /* 次要色 */
    --bg-main: #0f0f23;      /* 背景色 */
}
```

### 修改歡迎訊息
找到 `.welcome-message` 區塊並修改內容

## 📚 相關文檔

- **專案架構說明**: `PROJECT_STRUCTURE.md`
- **完整使用說明**: `README_TW.md`

## 🆘 需要幫助？

1. 查看瀏覽器控制台（F12）
2. 查看後端日誌
3. 閱讀相關設計文檔
4. 詢問我！

---

🐈 **AutoTo v1.0.0 - 台灣本地化 AI 智能助理**

現在開始使用吧！
