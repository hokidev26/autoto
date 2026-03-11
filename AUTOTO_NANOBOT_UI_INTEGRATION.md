# AutoTo UI 整合設計
  
  ## 📋 專案概述
  
  此文件記錄 AutoTo 視覺介面整合設計與早期 UI 參考方向。

## 🎨 核心功能模組

### 1. 對話頁面 (Chat)
- ✅ 即時對話界面
- ✅ 支持文件拖拽上傳
- ✅ 支持圖片、文件預覽
- ✅ Markdown 渲染
- ✅ 代碼高亮顯示
- ✅ 快速操作按鈕

### 2. 設定頁面 (Settings)
#### API 配置
- OpenRouter
- DeepSeek
- Groq
- 自定義 API

#### 聊天平台配置
- LINE（台灣主流）
- Telegram
- Discord
- QQ（可選）

### 3. 記憶管理 (Memory)
- 查看對話歷史
- 手動添加重要記憶
- 記憶搜索功能
- 自動歸檔（每 50 條）

### 4. 技能管理 (Skills)
- 台灣天氣查詢
- 統一發票對獎
- 台股資訊查詢
- 自定義技能

### 5. MCP 管理 (MCP Tools)
- MCP 服務器列表
- 工具啟用/停用
- 工具配置
- 工具測試

### 6. 定時任務 (Scheduler)
- 定時提醒
- 自動查詢（天氣、股價）
- 發票對獎提醒
- 自定義任務

## 🎯 台灣本地化特色

### 快速操作
```
🌤️ 查詢天氣 → "台北市今天天氣如何？"
🎫 發票對獎 → "幫我對統一發票"
📈 查詢股價 → "台積電今天股價"
🚇 捷運資訊 → "台北捷運路線圖"
🍜 美食推薦 → "附近有什麼好吃的？"
```

### 台灣工具整合
1. **中央氣象署 API**
2. **財政部統一發票 API**
3. **證交所股市 API**
4. **台北捷運 API**
5. **Google Maps API（台灣）**

## 📐 界面設計

### 布局結構
```
┌─────────────────────────────────────────┐
│  Sidebar  │      Main Content           │
│           │                             │
│  🐈 Logo  │  ┌─────────────────────┐   │
│           │  │   Chat Header       │   │
│  + 新對話  │  ├─────────────────────┤   │
│           │  │                     │   │
│  歷史記錄  │  │   Messages Area     │   │
│  - 對話1  │  │                     │   │
│  - 對話2  │  │                     │   │
│           │  ├─────────────────────┤   │
│  ⚙️ 設定  │  │   Input Box         │   │
│  🧠 記憶  │  └─────────────────────┘   │
│  🛠️ 技能  │                             │
│  📅 任務  │                             │
└─────────────────────────────────────────┘
```

### 配色方案
```css
--primary: #667eea;      /* 主色 - 紫藍 */
--secondary: #764ba2;    /* 次要色 - 紫色 */
--accent: #f093fb;       /* 強調色 - 粉紫 */
--bg-main: #0f0f23;      /* 主背景 - 深藍黑 */
--bg-sidebar: #1a1a2e;   /* 側邊欄 - 深灰藍 */
--bg-input: #16213e;     /* 輸入框 - 深藍 */
--text-primary: #e4e4e7; /* 主文字 - 淺灰 */
--text-secondary: #a1a1aa; /* 次要文字 - 灰 */
```

## 🔧 技術架構

### 前端
- HTML5 + CSS3
- Vanilla JavaScript（輕量化）
- 或 Vue.js（如需複雜交互）

### 後端
- Flask（Python）
- AutoTo backend
- RESTful API

### 通訊
- WebSocket（即時對話）
- HTTP API（配置、管理）

## 📱 頁面結構

### 1. index.html - 主頁面
```html
<div class="app">
  <aside class="sidebar">
    <!-- 側邊欄導航 -->
  </aside>
  <main class="content">
    <!-- 動態內容區 -->
  </main>
</div>
```

### 2. 模組化組件

#### chat.js - 對話模組
```javascript
class ChatModule {
  constructor() {
    this.messages = [];
    this.ws = null;
  }
  
  sendMessage(text) { }
  receiveMessage(data) { }
  renderMessage(msg) { }
}
```

#### settings.js - 設定模組
```javascript
class SettingsModule {
  loadConfig() { }
  saveConfig() { }
  testConnection() { }
}
```

#### memory.js - 記憶模組
```javascript
class MemoryModule {
  loadMemories() { }
  addMemory(text) { }
  searchMemory(query) { }
}
```

#### skills.js - 技能模組
```javascript
class SkillsModule {
  loadSkills() { }
  toggleSkill(id) { }
  configureSkill(id, config) { }
}
```

## 🚀 實作步驟

### Phase 1: 基礎界面（1-2 天）
- [x] 創建基本 HTML 結構
- [x] 實作 CSS 樣式
- [x] 側邊欄導航
- [x] 對話界面

### Phase 2: 核心功能（3-5 天）
- [ ] WebSocket 連接
- [ ] 訊息發送/接收
- [ ] 設定頁面
- [ ] API 配置

### Phase 3: 進階功能（5-7 天）
- [ ] 記憶管理
- [ ] 技能管理
- [ ] MCP 整合
- [ ] 定時任務

### Phase 4: 台灣特色（3-5 天）
- [ ] 天氣查詢工具
- [ ] 發票對獎工具
- [ ] 股市查詢工具
- [ ] LINE 整合

### Phase 5: 優化測試（2-3 天）
- [ ] 性能優化
- [ ] 錯誤處理
- [ ] 用戶測試
- [ ] Bug 修復

## 📊 功能對照表

| UI 設計參考 | AutoTo 對應 | 優先級 | 狀態 |
|----------------|------------|--------|------|
| 可視化對話 | 聊天界面 | P0 | ✅ 完成 |
| API 配置 | 設定頁面 | P0 | ✅ 完成 |
| 記憶管理 | 記憶模組 | P1 | 🔄 進行中 |
| 技能管理 | 技能模組 | P1 | 📋 計劃中 |
| MCP 管理 | MCP 模組 | P1 | 📋 計劃中 |
| 定時任務 | 任務模組 | P2 | 📋 計劃中 |
| QQ 整合 | LINE 整合 | P2 | 📋 計劃中 |
| 文件拖拽 | 文件上傳 | P1 | 📋 計劃中 |

## 🎨 UI 組件庫

### 按鈕
```html
<button class="btn btn-primary">主要按鈕</button>
<button class="btn btn-secondary">次要按鈕</button>
<button class="btn btn-icon">🔧</button>
```

### 輸入框
```html
<input class="input" type="text" placeholder="輸入...">
<textarea class="textarea" placeholder="多行輸入..."></textarea>
```

### 卡片
```html
<div class="card">
  <div class="card-header">標題</div>
  <div class="card-body">內容</div>
</div>
```

### 開關
```html
<label class="toggle">
  <input type="checkbox">
  <span class="toggle-slider"></span>
</label>
```

## 📝 API 端點設計

### 對話 API
```
POST /api/chat
Body: { "message": "你好" }
Response: { "response": "你好！", "id": "msg_123" }
```

### 配置 API
```
GET /api/config
Response: { "provider": "openrouter", "model": "..." }

POST /api/config
Body: { "provider": "openrouter", "apiKey": "..." }
```

### 記憶 API
```
GET /api/memories
Response: [{ "id": 1, "text": "...", "timestamp": "..." }]

POST /api/memories
Body: { "text": "重要記憶" }
```

### 技能 API
```
GET /api/skills
Response: [{ "id": "weather", "name": "天氣查詢", "enabled": true }]

POST /api/skills/{id}/toggle
```

## 🔐 安全考量

1. **API Key 加密** - 本地儲存加密
2. **HTTPS** - 生產環境必須
3. **CORS 設定** - 限制來源
4. **輸入驗證** - 防止注入攻擊
5. **速率限制** - 防止濫用

## 📦 部署方案

### 開發環境
```bash
# 啟動後端
python backend/server.py --port 5678
```

### 生產環境
```bash
# 使用 Electron 打包
npm run build

# 或使用 Docker
docker build -t autoto .
docker run -p 5678:5678 autoto
```

## 🎯 下一步行動

1. **立即可做**
   - 驗證後端直接提供 Web UI
   - 測試安裝器與瀏覽器入口
   - 調整樣式

2. **短期目標（本週）**
   - 實作 WebSocket 連接
   - 完成設定頁面
   - 測試 API 整合

3. **中期目標（本月）**
   - 完成所有核心功能
   - 實作台灣工具
   - 用戶測試

4. **長期目標（3 個月）**
   - LINE 整合
   - 移動端適配
   - 發布 1.0 版本

---

🐈 **AutoTo - 台灣本地化 AI 智能助理**

以 AutoTo 後端與瀏覽器介面為核心的本地化 AI 助理
