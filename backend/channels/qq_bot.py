#!/usr/bin/env python3
"""QQ Channel - 透過 OneBot v11 協議（兼容 go-cqhttp / NapCat / Lagrange）"""

import json
import threading
from flask import Flask, request, jsonify

try:
    import requests as req
    HAS_REQUESTS = True
except ImportError:
    HAS_REQUESTS = False


class QQChannel:
    def __init__(self, cfg, agent):
        self.agent = agent
        self.http_url = cfg.get('httpUrl', 'http://127.0.0.1:5700')
        self.webhook_port = cfg.get('webhookPort', 5683)
        self._running = False
        self._app = Flask(__name__)
        self._setup_routes()

    def _setup_routes(self):
        @self._app.route('/', methods=['POST'])
        def onebot_event():
            data = request.json
            if not data:
                return '', 204
            post_type = data.get('post_type')
            if post_type == 'message':
                self._handle_message(data)
            return '', 204

    def _handle_message(self, data):
        msg_type = data.get('message_type')
        raw_msg = data.get('raw_message', '') or data.get('message', '')
        if isinstance(raw_msg, list):
            # CQ 消息段格式，提取純文字
            raw_msg = ''.join(seg.get('data', {}).get('text', '') for seg in raw_msg if seg.get('type') == 'text')
        raw_msg = str(raw_msg).strip()
        if not raw_msg:
            return

        user_id = data.get('user_id', '')
        group_id = data.get('group_id', '')

        if msg_type == 'group':
            session_id = f'qq-group-{group_id}-{user_id}'
        else:
            session_id = f'qq-{user_id}'

        try:
            response = self.agent.process_message(session_id, raw_msg, 'qq')
            if msg_type == 'group':
                self._send_group_msg(group_id, response)
            else:
                self._send_private_msg(user_id, response)
        except Exception as e:
            print(f'  ❌ QQ handle_message error: {e}')

    def _send_private_msg(self, user_id, text):
        try:
            req.post(f'{self.http_url}/send_private_msg', json={
                'user_id': int(user_id),
                'message': text[:4500]
            }, timeout=10)
        except Exception as e:
            print(f'  ⚠️ QQ send_private_msg error: {e}')

    def _send_group_msg(self, group_id, text):
        try:
            req.post(f'{self.http_url}/send_group_msg', json={
                'group_id': int(group_id),
                'message': text[:4500]
            }, timeout=10)
        except Exception as e:
            print(f'  ⚠️ QQ send_group_msg error: {e}')

    def run(self):
        self._running = True
        print(f'  🐧 QQ Bot (OneBot) Webhook 已啟動 (port {self.webhook_port})')
        self._app.run(host='0.0.0.0', port=self.webhook_port, debug=False)

    def stop(self):
        self._running = False
