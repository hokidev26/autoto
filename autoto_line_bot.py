#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
AutoTo LINE Bot Integration
讓 AutoTo 可以在 LINE 上使用
"""

from flask import Flask, request, abort
from linebot import LineBotApi, WebhookHandler
from linebot.exceptions import InvalidSignatureError
from linebot.models import MessageEvent, TextMessage, TextSendMessage
import requests
import os

class AutoToLineBot:
    """AutoTo LINE Bot 類"""
    
    def __init__(self, channel_access_token, channel_secret, autoto_api_url='http://127.0.0.1:5678'):
        """
        初始化 LINE Bot
        
        參數：
        - channel_access_token: LINE Channel Access Token
        - channel_secret: LINE Channel Secret
        - autoto_api_url: AutoTo 後端 API 地址
        """
        self.line_bot_api = LineBotApi(channel_access_token)
        self.handler = WebhookHandler(channel_secret)
        self.autoto_api_url = autoto_api_url
        
        # 註冊訊息處理器
        @self.handler.add(MessageEvent, message=TextMessage)
        def handle_message(event):
            self.handle_text_message(event)
    
    def handle_text_message(self, event):
        """處理文字訊息"""
        user_message = event.message.text
        user_id = event.source.user_id
        
        # 白名單檢查（可選）
        # ALLOWED_USERS = ['你的LINE User ID']
        # if user_id not in ALLOWED_USERS:
        #     self.line_bot_api.reply_message(
        #         event.reply_token,
        #         TextSendMessage(text='抱歉，你沒有使用權限')
        #     )
        #     return
        
        # 調用 AutoTo API
        response_text = self.call_autoto_api(user_message)
        
        # 回覆訊息
        self.line_bot_api.reply_message(
            event.reply_token,
            TextSendMessage(text=response_text)
        )
    
    def call_autoto_api(self, message):
        """調用 AutoTo API"""
        try:
            response = requests.post(
                f'{self.autoto_api_url}/api/chat',
                json={'message': message},
                timeout=30
            )
            
            if response.status_code == 200:
                data = response.json()
                if data.get('success'):
                    return data.get('response', '抱歉，我無法回應')
                else:
                    return f"錯誤：{data.get('error', '未知錯誤')}"
            else:
                return f"API 錯誤：{response.status_code}"
        except Exception as e:
            return f"連線錯誤：{str(e)}"
    
    def create_flask_app(self):
        """創建 Flask 應用"""
        app = Flask(__name__)
        
        @app.route("/callback", methods=['POST'])
        def callback():
            # 獲取 X-Line-Signature header
            signature = request.headers['X-Line-Signature']
            
            # 獲取請求內容
            body = request.get_data(as_text=True)
            
            # 處理 webhook
            try:
                self.handler.handle(body, signature)
            except InvalidSignatureError:
                abort(400)
            
            return 'OK'
        
        @app.route("/")
        def home():
            return "AutoTo LINE Bot is running!"
        
        return app


# ==================== 使用範例 ====================

def create_line_bot():
    """創建 LINE Bot 實例"""
    
    # 從環境變數讀取設定
    channel_access_token = os.getenv('LINE_CHANNEL_ACCESS_TOKEN', '')
    channel_secret = os.getenv('LINE_CHANNEL_SECRET', '')
    
    if not channel_access_token or not channel_secret:
        print("❌ 請設定 LINE_CHANNEL_ACCESS_TOKEN 和 LINE_CHANNEL_SECRET 環境變數")
        print("\n設定方法：")
        print("export LINE_CHANNEL_ACCESS_TOKEN='你的 Access Token'")
        print("export LINE_CHANNEL_SECRET='你的 Channel Secret'")
        return None
    
    # 創建 Bot
    bot = AutoToLineBot(channel_access_token, channel_secret)
    return bot.create_flask_app()


if __name__ == '__main__':
    print("🤖 AutoTo LINE Bot")
    print("=" * 50)
    
    app = create_line_bot()
    
    if app:
        print("\n✅ LINE Bot 已啟動")
        print("📍 Webhook URL: http://your-domain.com/callback")
        print("💡 請在 LINE Developers 設定 Webhook URL")
        print("\n按 Ctrl+C 停止服務\n")
        
        # 啟動服務
        app.run(host='0.0.0.0', port=8000, debug=True)
    else:
        print("\n❌ 啟動失敗，請檢查環境變數設定")
