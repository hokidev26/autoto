#!/usr/bin/env python3
"""Slack Channel - 使用 Slack Bolt"""

import threading

try:
    from slack_bolt import App
    from slack_bolt.adapter.socket_mode import SocketModeHandler
    HAS_SLACK = True
except ImportError:
    HAS_SLACK = False


class SlackChannel:
    def __init__(self, cfg, agent):
        if not HAS_SLACK:
            raise ImportError('slack-bolt 未安裝，請執行: pip install slack-bolt')
        self.agent = agent
        self.token = cfg.get('botToken', '')
        self.signing_secret = cfg.get('signingSecret', '')
        self._running = False
        self._app = App(token=self.token, signing_secret=self.signing_secret)
        self._setup_handlers()

    def _setup_handlers(self):
        @self._app.message('')
        def handle_message(message, say):
            text = message.get('text', '')
            if not text:
                return
            user_id = message.get('user', 'unknown')
            session_id = f'slack-{user_id}'
            response = self.agent.process_message(session_id, text, 'slack')
            say(response)

    def run(self):
        self._running = True
        print('  💼 Slack Bot 已啟動')
        self._app.start(port=5681)

    def stop(self):
        self._running = False
