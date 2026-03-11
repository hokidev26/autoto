#!/usr/bin/env python3
"""WhatsApp Channel - 透過 Meta Cloud API (Webhook)"""

import threading
from flask import Flask, request, jsonify

try:
    import requests as req
    HAS_REQUESTS = True
except ImportError:
    HAS_REQUESTS = False


class WhatsappChannel:
    def __init__(self, cfg, agent):
        self.agent = agent
        self.phone_number_id = cfg.get('phoneNumberId', '')
        self.access_token = cfg.get('accessToken', '')
        self.verify_token = cfg.get('verifyToken', 'autoto_verify')
        self._running = False
        self._app = Flask(__name__)
        self._setup_routes()

    def _setup_routes(self):
        @self._app.route('/webhook/whatsapp', methods=['GET'])
        def verify():
            mode = request.args.get('hub.mode')
            token = request.args.get('hub.verify_token')
            challenge = request.args.get('hub.challenge')
            if mode == 'subscribe' and token == self.verify_token:
                return challenge, 200
            return 'Forbidden', 403

        @self._app.route('/webhook/whatsapp', methods=['POST'])
        def webhook():
            data = request.json
            try:
                for entry in data.get('entry', []):
                    for change in entry.get('changes', []):
                        value = change.get('value', {})
                        for msg in value.get('messages', []):
                            if msg.get('type') == 'text':
                                from_number = msg['from']
                                text = msg['text']['body']
                                session_id = f'whatsapp-{from_number}'
                                response = self.agent.process_message(session_id, text, 'whatsapp')
                                self._send_message(from_number, response)
            except Exception as e:
                print(f'  ❌ WhatsApp webhook error: {e}')
            return jsonify({'status': 'ok'})

    def _send_message(self, to, text):
        url = f'https://graph.facebook.com/v18.0/{self.phone_number_id}/messages'
        headers = {'Authorization': f'Bearer {self.access_token}', 'Content-Type': 'application/json'}
        # 分段發送（WhatsApp 限制 4096 字元）
        for i in range(0, len(text), 4000):
            req.post(url, headers=headers, json={
                'messaging_product': 'whatsapp',
                'to': to,
                'type': 'text',
                'text': {'body': text[i:i+4000]}
            })

    def run(self):
        self._running = True
        print('  📱 WhatsApp Webhook 已啟動 (port 5680)')
        self._app.run(host='0.0.0.0', port=5680, debug=False)

    def stop(self):
        self._running = False
