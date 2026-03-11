#!/usr/bin/env python3
"""微信公眾號 Channel"""

import hashlib
import time

try:
    from flask import Flask, request
    import xmltodict
    HAS_WECHAT = True
except ImportError:
    HAS_WECHAT = False


class WechatChannel:
    def __init__(self, cfg, agent):
        if not HAS_WECHAT:
            raise ImportError('xmltodict 未安裝，請執行: pip install xmltodict')
        self.agent = agent
        self.app_id = cfg['appId']
        self.app_secret = cfg['appSecret']
        self.token = cfg.get('verifyToken', 'autoto')
        self._app = Flask(__name__)
        self._running = False

        @self._app.route('/webhook/wechat', methods=['GET', 'POST'])
        def webhook():
            if request.method == 'GET':
                # 微信驗證
                signature = request.args.get('signature', '')
                timestamp = request.args.get('timestamp', '')
                nonce = request.args.get('nonce', '')
                echostr = request.args.get('echostr', '')
                check = ''.join(sorted([self.token, timestamp, nonce]))
                if hashlib.sha1(check.encode()).hexdigest() == signature:
                    return echostr
                return 'fail'

            # 處理訊息
            xml_data = request.get_data(as_text=True)
            msg = xmltodict.parse(xml_data)['xml']

            if msg.get('MsgType') == 'text':
                user_msg = msg['Content']
                user_id = msg['FromUserName']
                to_user = msg['ToUserName']
                session_id = f'wechat-{user_id}'

                response = self.agent.process_message(session_id, user_msg, source='wechat')

                reply_xml = f'''<xml>
                    <ToUserName><![CDATA[{user_id}]]></ToUserName>
                    <FromUserName><![CDATA[{to_user}]]></FromUserName>
                    <CreateTime>{int(time.time())}</CreateTime>
                    <MsgType><![CDATA[text]]></MsgType>
                    <Content><![CDATA[{response}]]></Content>
                </xml>'''
                return reply_xml

            return 'success'

    def run(self):
        self._running = True
        print('  💬 微信 Bot webhook 啟動於 port 5680')
        self._app.run(host='0.0.0.0', port=5680, debug=False)

    def stop(self):
        self._running = False
