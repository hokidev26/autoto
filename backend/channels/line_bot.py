#!/usr/bin/env python3
"""LINE Channel"""

import threading

try:
    from flask import Flask, request, abort
    from linebot import LineBotApi, WebhookHandler
    from linebot.exceptions import InvalidSignatureError
    from linebot.models import MessageEvent, TextMessage, TextSendMessage
    HAS_LINE = True
except ImportError:
    HAS_LINE = False


class LineChannel:
    def __init__(self, cfg, agent):
        if not HAS_LINE:
            raise ImportError('line-bot-sdk 未安裝，請執行: pip install line-bot-sdk')
        self.agent = agent
        self.line_api = LineBotApi(cfg['channelAccessToken'])
        self.handler = WebhookHandler(cfg['channelSecret'])
        self._app = Flask(__name__)
        self._running = False

        @self.handler.add(MessageEvent, message=TextMessage)
        def handle_message(event):
            user_msg = event.message.text
            user_id = event.source.user_id
            session_id = f'line-{user_id}'
            response = self.agent.process_message(session_id, user_msg, source='line')
            self.line_api.reply_message(
                event.reply_token,
                TextSendMessage(text=response)
            )

        @self._app.route('/webhook/line', methods=['POST'])
        def webhook():
            signature = request.headers.get('X-Line-Signature', '')
            body = request.get_data(as_text=True)
            try:
                self.handler.handle(body, signature)
            except InvalidSignatureError:
                abort(400)
            return 'OK'

    def run(self):
        self._running = True
        print('  💚 LINE Bot webhook 啟動於 port 5679')
        self._app.run(host='0.0.0.0', port=5679, debug=False)

    def stop(self):
        self._running = False
