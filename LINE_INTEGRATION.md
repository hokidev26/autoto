# LINE 整合設計文件

## 架構設計

### 1. LINE Messaging API 整合

```python
# backend/channels/line_bot.py

from typing import Optional, Dict, Any
import asyncio
from linebot import LineBotApi, WebhookHandler
from linebot.models import (
    MessageEvent, TextMessage, ImageMessage, AudioMessage,
    TextSendMessage, ImageSendMessage, FlexSendMessage
)
from linebot.exceptions import LineBotApiError

class LineChannel:
    """LINE 官方帳號整合"""
    
    def __init__(self, config: Dict[str, Any]):
        self.channel_access_token = config.get("channelAccessToken")
        self.channel_secret = config.get("channelSecret")
        self.allow_from = config.get("allowFrom", [])
        
        self.line_bot_api = LineBotApi(self.channel_access_token)
        self.handler = WebhookHandler(self.channel_secret)
        
        # 註冊事件處理器
        self._register_handlers()
    
    def _register_handlers(self):
        """註冊 LINE 事件處理器"""
        
        @self.handler.add(MessageEvent, message=TextMessage)
        def handle_text_message(event):
            user_id = event.source.user_id
            
            # 檢查白名單
            if self.allow_from and user_id not in self.allow_from:
                return
            
            # 取得用戶訊息
            user_message = event.message.text
            
            # 發送到 agent 處理
            response = self._process_message(user_id, user_message)
            
            # 回覆用戶
            self.line_bot_api.reply_message(
                event.reply_token,
                TextSendMessage(text=response)
            )
        
        @self.handler.add(MessageEvent, message=ImageMessage)
        def handle_image_message(event):
            # 處理圖片訊息
            pass
        
        @self.handler.add(MessageEvent, message=AudioMessage)
        def handle_audio_message(event):
            # 處理語音訊息
            pass
    
    def _process_message(self, user_id: str, message: str) -> str:
        """
        將訊息發送到 AutoTo 後端 API 處理
        """
        import requests
        
        response = requests.post(
            "http://127.0.0.1:5678/api/chat",
            json={"message": message, "sessionId": f"line-{user_id}"},
            timeout=30,
        )
        response.raise_for_status()
        payload = response.json()
        return payload.get("response", "")
    
    def send_message(self, user_id: str, message: str):
        """主動發送訊息給用戶"""
        try:
            self.line_bot_api.push_message(
                user_id,
                TextSendMessage(text=message)
            )
        except LineBotApiError as e:
            print(f"Error sending message: {e}")
    
    def send_flex_message(self, user_id: str, alt_text: str, contents: Dict):
        """發送 Flex Message（圖文訊息）"""
        try:
            self.line_bot_api.push_message(
                user_id,
                FlexSendMessage(alt_text=alt_text, contents=contents)
            )
        except LineBotApiError as e:
            print(f"Error sending flex message: {e}")
    
    def get_user_profile(self, user_id: str) -> Optional[Dict]:
        """取得用戶資料"""
        try:
            profile = self.line_bot_api.get_profile(user_id)
            return {
                "display_name": profile.display_name,
                "user_id": profile.user_id,
                "picture_url": profile.picture_url,
                "status_message": profile.status_message
            }
        except LineBotApiError:
            return None
```

### 2. Webhook 伺服器

```python
# backend/server.py

from flask import Flask, request, abort
from linebot.exceptions import InvalidSignatureError

app = Flask(__name__)

# 全域 LINE channel 實例
line_channel = None

def init_line_webhook(channel_instance):
    """初始化 webhook 伺服器"""
    global line_channel
    line_channel = channel_instance

@app.route("/webhook/line", methods=['POST'])
def line_webhook():
    """LINE Webhook 端點"""
    
    # 取得 X-Line-Signature header
    signature = request.headers['X-Line-Signature']
    
    # 取得 request body
    body = request.get_data(as_text=True)
    
    # 驗證簽名並處理事件
    try:
        line_channel.handler.handle(body, signature)
    except InvalidSignatureError:
        abort(400)
    
    return 'OK'

@app.route("/health", methods=['GET'])
def health_check():
    """健康檢查端點"""
    return {"status": "ok"}

def run_webhook_server(host='0.0.0.0', port=5678):
    """啟動 webhook 伺服器"""
    app.run(host=host, port=port)
```

### 3. 設定檔格式

```json
{
  "channels": {
    "line": {
      "enabled": true,
      "channelAccessToken": "YOUR_CHANNEL_ACCESS_TOKEN",
      "channelSecret": "YOUR_CHANNEL_SECRET",
      "allowFrom": [],
      "webhookUrl": "https://your-autoto-site.example.com/webhook/line",
      "features": {
        "richMenu": true,
        "flexMessage": true,
        "quickReply": true,
        "imagemap": false
      }
    }
  }
}
```

## 申請 LINE 官方帳號

### 步驟 1: 建立 LINE Developers 帳號
1. 前往 https://developers.line.biz/
2. 使用 LINE 帳號登入
3. 建立新的 Provider

### 步驟 2: 建立 Messaging API Channel
1. 在 Provider 下建立新的 Channel
2. 選擇「Messaging API」
3. 填寫基本資訊：
   - Channel name: AutoTo
   - Channel description: 台灣本地化 AI 助理
   - Category: 選擇適合的分類
   - Subcategory: 選擇子分類

### 步驟 3: 設定 Channel
1. 進入 Channel 設定頁面
2. 取得 **Channel Secret**（在 Basic settings）
3. 發行 **Channel Access Token**（在 Messaging API）
4. 設定 Webhook URL:
   - 如果本地測試，使用 ngrok: `https://xxx.ngrok.io/webhook/line`
   - 如果正式環境，使用你的網域
5. 啟用「Use webhook」
6. 關閉「Auto-reply messages」（讓 bot 完全控制回覆）

### 步驟 4: 本地測試用 ngrok
```bash
# 安裝 ngrok
brew install ngrok

# 啟動 ngrok（假設 webhook 在 port 5678）
ngrok http 5678

# 複製 ngrok 提供的 HTTPS URL
# 例如: https://abc123.ngrok.io

# 在 LINE Developers 設定 Webhook URL:
# https://abc123.ngrok.io/webhook/line
```

## LINE 特色功能

### 1. Rich Menu（底部選單）

```python
def create_rich_menu():
    """建立 Rich Menu"""
    rich_menu = {
        "size": {"width": 2500, "height": 1686},
        "selected": True,
        "name": "AutoTo 主選單",
        "chatBarText": "功能選單",
        "areas": [
            {
                "bounds": {"x": 0, "y": 0, "width": 833, "height": 843},
                "action": {"type": "message", "text": "查詢天氣"}
            },
            {
                "bounds": {"x": 833, "y": 0, "width": 834, "height": 843},
                "action": {"type": "message", "text": "對發票"}
            },
            {
                "bounds": {"x": 1667, "y": 0, "width": 833, "height": 843},
                "action": {"type": "message", "text": "查股票"}
            },
            {
                "bounds": {"x": 0, "y": 843, "width": 833, "height": 843},
                "action": {"type": "uri", "uri": "https://your-autoto-site.example.com"}
            },
            {
                "bounds": {"x": 833, "y": 843, "width": 834, "height": 843},
                "action": {"type": "message", "text": "設定"}
            },
            {
                "bounds": {"x": 1667, "y": 843, "width": 833, "height": 843},
                "action": {"type": "message", "text": "幫助"}
            }
        ]
    }
    
    # 建立 rich menu
    rich_menu_id = line_bot_api.create_rich_menu(rich_menu)
    
    # 上傳圖片（需要準備 2500x1686 的圖片）
    with open('rich_menu.png', 'rb') as f:
        line_bot_api.set_rich_menu_image(rich_menu_id, 'image/png', f)
    
    # 設為預設選單
    line_bot_api.set_default_rich_menu(rich_menu_id)
```

### 2. Flex Message（卡片訊息）

```python
def create_weather_flex_message(weather_data):
    """建立天氣 Flex Message"""
    return {
        "type": "bubble",
        "hero": {
            "type": "image",
            "url": weather_data["icon_url"],
            "size": "full",
            "aspectRatio": "20:13",
            "aspectMode": "cover"
        },
        "body": {
            "type": "box",
            "layout": "vertical",
            "contents": [
                {
                    "type": "text",
                    "text": weather_data["city"],
                    "weight": "bold",
                    "size": "xl"
                },
                {
                    "type": "box",
                    "layout": "baseline",
                    "margin": "md",
                    "contents": [
                        {
                            "type": "text",
                            "text": f"{weather_data['temp']}°C",
                            "size": "3xl",
                            "weight": "bold"
                        },
                        {
                            "type": "text",
                            "text": weather_data["description"],
                            "size": "sm",
                            "color": "#999999",
                            "margin": "md",
                            "flex": 0
                        }
                    ]
                }
            ]
        },
        "footer": {
            "type": "box",
            "layout": "vertical",
            "spacing": "sm",
            "contents": [
                {
                    "type": "button",
                    "style": "primary",
                    "action": {
                        "type": "message",
                        "label": "查看一週預報",
                        "text": "一週天氣預報"
                    }
                }
            ]
        }
    }
```

### 3. Quick Reply（快速回覆）

```python
def send_with_quick_reply(user_id, message, quick_reply_items):
    """發送帶有快速回覆按鈕的訊息"""
    from linebot.models import QuickReply, QuickReplyButton, MessageAction
    
    quick_reply = QuickReply(items=[
        QuickReplyButton(action=MessageAction(label=item["label"], text=item["text"]))
        for item in quick_reply_items
    ])
    
    line_bot_api.push_message(
        user_id,
        TextSendMessage(text=message, quick_reply=quick_reply)
    )
```

## 部署流程

### 開發環境
```bash
# 1. 安裝依賴
pip install line-bot-sdk flask

# 2. 設定環境變數
export LINE_CHANNEL_ACCESS_TOKEN="your_token"
export LINE_CHANNEL_SECRET="your_secret"

# 3. 啟動 webhook 伺服器
python backend/server.py --port 5678

# 4. 啟動 ngrok
ngrok http 5678

# 5. 在 LINE Developers 設定 webhook URL
```

### 生產環境
```bash
# 使用 gunicorn + nginx
gunicorn -w 4 -b 0.0.0.0:5678 backend.server:app
```

## 測試

```python
# tests/test_line_channel.py

import pytest
from backend.channels.line_bot import LineChannel

def test_line_channel_init():
    config = {
        "channelAccessToken": "test_token",
        "channelSecret": "test_secret"
    }
    channel = LineChannel(config)
    assert channel.channel_access_token == "test_token"

def test_message_processing():
    # TODO: 實作測試
    pass
```

## 注意事項

1. **Webhook 必須使用 HTTPS**
2. **回覆訊息有時間限制**（30 秒內必須回覆）
3. **主動推送訊息有數量限制**（免費版每月 500 則）
4. **Rich Menu 圖片規格**：2500x1686 或 2500x843 像素
5. **Flex Message 有大小限制**（最大 50KB）

## 下一步

- 實作 LINE Pay 整合
- 加入 LIFF（LINE Front-end Framework）
- 實作群組聊天支援
- 加入 LINE Login 整合
