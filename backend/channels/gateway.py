#!/usr/bin/env python3
"""
Channel Gateway - 多平台訊息路由
統一管理 Discord / LINE / Telegram / 微信 的連線
"""

import threading
import time


class ChannelGateway:
    def __init__(self, config_mgr, agent):
        self.config = config_mgr
        self.agent = agent
        self._channels = {}
        self._threads = {}

    def start_all(self):
        """啟動所有已啟用的 channels"""
        channels_cfg = self.config.get('channels', {})

        for name, cfg in channels_cfg.items():
            if cfg.get('enabled'):
                self._start_channel(name, cfg)

    def _start_channel(self, name, cfg):
        """啟動單一 channel"""
        try:
            if name == 'discord' and cfg.get('token'):
                from channels.discord_bot import DiscordChannel
                ch = DiscordChannel(cfg, self.agent)
            elif name == 'line' and cfg.get('channelAccessToken'):
                from channels.line_bot import LineChannel
                ch = LineChannel(cfg, self.agent)
            elif name == 'telegram' and cfg.get('botToken'):
                from channels.telegram_bot import TelegramChannel
                ch = TelegramChannel(cfg, self.agent)
            elif name == 'wechat' and cfg.get('appId'):
                from channels.wechat_bot import WechatChannel
                ch = WechatChannel(cfg, self.agent)
            elif name == 'whatsapp' and cfg.get('accessToken'):
                from channels.whatsapp_bot import WhatsappChannel
                ch = WhatsappChannel(cfg, self.agent)
            elif name == 'slack' and cfg.get('botToken'):
                from channels.slack_bot import SlackChannel
                ch = SlackChannel(cfg, self.agent)
            elif name == 'messenger' and cfg.get('pageAccessToken'):
                from channels.messenger_bot import MessengerChannel
                ch = MessengerChannel(cfg, self.agent)
            elif name == 'qq' and cfg.get('httpUrl'):
                from channels.qq_bot import QQChannel
                ch = QQChannel(cfg, self.agent)
            else:
                return

            self._channels[name] = ch
            t = threading.Thread(target=ch.run, daemon=True, name=f'channel-{name}')
            t.start()
            self._threads[name] = t
            print(f'  ✅ {name} channel 已啟動')
        except ImportError as e:
            print(f'  ⚠️ {name} channel 缺少依賴: {e}')
        except Exception as e:
            print(f'  ❌ {name} channel 啟動失敗: {e}')

    def stop_channel(self, name):
        ch = self._channels.get(name)
        if ch:
            if hasattr(ch, 'stop'):
                ch.stop()
            del self._channels[name]
            self._threads.pop(name, None)

    def restart_channel(self, name):
        self.stop_channel(name)
        cfg = self.config.get(f'channels.{name}', {})
        if cfg.get('enabled'):
            self._start_channel(name, cfg)

    def reload_channels(self):
        """重新載入所有 channels 配置"""
        channels_cfg = self.config.get('channels', {})
        for name, cfg in channels_cfg.items():
            if cfg.get('enabled') and name not in self._channels:
                self._start_channel(name, cfg)
            elif not cfg.get('enabled') and name in self._channels:
                self.stop_channel(name)

    def get_active_channels(self):
        return list(self._channels.keys())

    def get_status(self):
        result = {}
        channels_cfg = self.config.get('channels', {})
        for name in channels_cfg:
            result[name] = {
                'enabled': channels_cfg[name].get('enabled', False),
                'running': name in self._channels
            }
        return result
