#!/usr/bin/env python3
"""Facebook Messenger Channel - 透過 Webhook"""

from flask import Flask, request, jsonify

try:
    import requests as req
    HAS_REQUESTS = True
except ImportError:
    HAS_REQUESTS = False


class MessengerChannel:
    def __init__(self, cfg, agent):
        self.agent = agent
        self.page_token = cfg.get('pageAccessToken', '')
        self.verify_token = cfg.get('verifyToken', 'autoto_verify')
        self._running = False
        self._app = Flask(__name__)
        self._setup_routes()

    def _setup_routes(self):
        @self._app.route('/webhook/messenger', methods=['GET'])
        def verify():
            mode = request.args.get('hub.mode')
            token = request.args.get('hub.verify_token')
            challenge = request.args.get('hub.challenge')
            if mode == 'subscribe' and token == self.verify_token:
                return challenge, 200
            return 'Forbidden', 403

        @self._app.route('/webhook/messenger', methods=['POST'])
        def webhook():
            data = request.json
            try:
                for entry in data.get('entry', []):
                    for event in entry.get('messaging', []):
                        if 'message' in event and 'text' in event['message']:
                            sender_id = event['sender']['id']
                            text = event['message']['text']
                            session_id = f'messenger-{sender_id}'
                            response = self.agent.process_message(session_id, text, 'messenger')
                            self._send_message(sender_id, response)
            except Exception as e:
                print(f'  ❌ Messenger webhook error: {e}')
            return jsonify({'status': 'ok'})

    def _send_message(self, recipient_id, text):
        url = 'https://graph.facebook.com/v18.0/me/messages'
        headers = {'Content-Type': 'application/json'}
        params = {'access_token': self.page_token}
        for i in range(0, len(text), 2000):
            req.post(url, params=params, headers=headers, json={
                'recipient': {'id': recipient_id},
                'message': {'text': text[i:i+2000]}
            })

    def run(self):
        self._running = True
        print('  💬 Messenger Webhook 已啟動 (port 5682)')
        self._app.run(host='0.0.0.0', port=5682, debug=False)

    def stop(self):
        self._running = False
