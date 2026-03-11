# AutoTo 專案結構規劃

## 目錄結構

```
autoto/
├── backend/
│   ├── channels/              # LINE、Telegram、Slack 等平台整合
│   ├── core/                  # agent / config / memory / scheduler / tools
│   ├── requirements.txt
│   └── server.py
│
├── electron-app/              # Electron 殼層與 renderer
│
├── installer/                 # macOS / Windows 安裝器
│
├── LICENSE
│
├── NOTICE
│
├── README_TW.md
│
├── GET_STARTED.md
│
└── start.sh

```

## 模組說明

### backend.channels.line_bot
LINE 官方帳號整合，支援：
- Webhook 接收訊息
- 文字、圖片、語音訊息
- Flex Message（圖文選單）
- Rich Menu（底部選單）
- LINE Pay 整合

### backend.core.tools（weather）
中央氣象署 API 整合：
- 即時天氣查詢
- 未來一週預報
- 颱風警報
- 地震速報
- 空氣品質

### backend.core.tools（invoice）
統一發票功能：
- 對獎查詢
- 中獎通知
- 發票掃描（OCR）
- 消費記錄

### backend.core.tools（stock）
台股資訊：
- 即時股價
- 技術分析
- 新聞摘要
- 個股追蹤

## 開發原則

### 1. 模組化維護
```bash
# 定期更新
git pull
bash install.sh
```

### 2. 模組化設計
- 平台與工具邏輯集中於 `backend/` 目錄
- 後端與 Electron / installer 分層
- 使用 plugin 機制擴充

### 3. 繁體中文優先
- 所有文檔提供繁體中文版
- Prompt 使用台灣用語
- UI 訊息本地化

### 4. 商業化考量
- 支援多租戶
- API rate limiting
- 使用統計和分析
- 付費功能模組化

## 部署方式

### 開發環境
```bash
python3 -m pip install -r backend/requirements.txt
./start.sh
```

### 生產環境（Docker）
```bash
docker build -t autoto .
docker run -p 5678:5678 autoto
```

### SaaS 部署
- Kubernetes + Helm
- 自動擴展
- 監控和日誌
- 備份和災難恢復

## 授權

- 以專案實際授權條款為準
- 台灣擴充功能採用 MIT License
- 商業版本另行授權
