#!/usr/bin/env python3
"""配置管理器"""

import json
import os
import threading
from pathlib import Path
from copy import deepcopy

CONFIG_DIR = Path.home() / '.autoto'
CONFIG_FILE = CONFIG_DIR / 'config.json'

DEFAULT_CONFIG = {
    'provider': 'groq',
    'apiKey': '',
    'model': 'llama-3.3-70b-versatile',
    'customUrl': '',
    'channels': {
        'discord': {'enabled': False, 'token': ''},
        'line': {'enabled': False, 'channelAccessToken': '', 'channelSecret': ''},
        'telegram': {'enabled': False, 'botToken': ''},
        'wechat': {'enabled': False, 'appId': '', 'appSecret': ''},
        'whatsapp': {'enabled': False, 'phoneNumberId': '', 'accessToken': '', 'verifyToken': ''},
        'slack': {'enabled': False, 'botToken': '', 'signingSecret': ''},
        'messenger': {'enabled': False, 'pageAccessToken': '', 'verifyToken': ''},
        'qq': {'enabled': False, 'httpUrl': 'http://127.0.0.1:5700', 'webhookPort': 5683},
        'instagram': {'enabled': False, 'accessToken': ''}
    },
    'memory': {'enabled': True, 'autoArchive': 50},
    'agent': {
        'maxTokenBudget': 4000,
        'compressionEnabled': True,
        'systemPrompt': '你是 AutoTo，一個智能 AI 助理。請用繁體中文回答，語氣友善親切。'
    },
    'session': {
        'persist': True
    },
    'cameras': [],
    'smarthome': {
        'platforms': []
    }
}


class ConfigManager:
    def __init__(self):
        CONFIG_DIR.mkdir(parents=True, exist_ok=True)
        self._lock = threading.Lock()
        self._config = self._load()

    def _load(self):
        if CONFIG_FILE.exists():
            try:
                with open(CONFIG_FILE, 'r', encoding='utf-8') as f:
                    saved = json.load(f)
                # 合併預設值（確保新欄位存在）
                merged = deepcopy(DEFAULT_CONFIG)
                self._deep_merge(merged, saved)
                return merged
            except Exception:
                return deepcopy(DEFAULT_CONFIG)
        return deepcopy(DEFAULT_CONFIG)

    def _deep_merge(self, base, override):
        for k, v in override.items():
            if k in base and isinstance(base[k], dict) and isinstance(v, dict):
                self._deep_merge(base[k], v)
            else:
                base[k] = v

    def _save(self):
        with open(CONFIG_FILE, 'w', encoding='utf-8') as f:
            json.dump(self._config, f, ensure_ascii=False, indent=2)

    def get(self, key=None, default=None):
        with self._lock:
            if key is None:
                return deepcopy(self._config)
            keys = key.split('.')
            val = self._config
            for k in keys:
                if isinstance(val, dict) and k in val:
                    val = val[k]
                else:
                    return default
            return val

    def update(self, data):
        """更新配置（不覆蓋未提供的欄位）"""
        data = deepcopy(data)  # 避免 mutate 呼叫端的 dict
        # 特殊處理 apiKey：如果是遮罩值就跳過
        if 'apiKey' in data and data['apiKey'].startswith('***'):
            del data['apiKey']
        with self._lock:
            self._deep_merge(self._config, data)
            self._save()

    def get_safe_config(self):
        """回傳隱藏敏感資訊的配置"""
        with self._lock:
            cfg = deepcopy(self._config)
        if cfg.get('apiKey'):
            cfg['apiKey'] = '***' + cfg['apiKey'][-4:]
        # 隱藏 channel tokens
        for ch_name, ch_cfg in cfg.get('channels', {}).items():
            for key in ch_cfg:
                if 'token' in key.lower() or 'secret' in key.lower():
                    if ch_cfg[key]:
                        ch_cfg[key] = '***' + ch_cfg[key][-4:]
        return cfg
