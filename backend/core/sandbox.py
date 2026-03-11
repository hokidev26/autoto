#!/usr/bin/env python3
"""
權限沙盒 — 細粒度工具權限控制
每個工具可設定：
  - allowed: bool（是否允許）
  - confirm: bool（執行前需確認）
  - rate_limit: int（每分鐘最多幾次，0=無限）
  - path_whitelist: list（檔案操作限制路徑）
  - path_blacklist: list（禁止存取的路徑）
  - arg_filters: dict（參數過濾規則）
"""

import json
import os
import re
import time
import threading
from pathlib import Path
from datetime import datetime

PERMISSIONS_FILE = Path.home() / '.autoto' / 'permissions.json'

# 預設權限等級
PRESETS = {
    'full': {
        'description': '完全存取 — 所有工具無限制',
        'default_allow': True,
        'default_confirm': False,
        'default_rate_limit': 0,
        'overrides': {}
    },
    'standard': {
        'description': '標準模式 — 危險操作需確認',
        'default_allow': True,
        'default_confirm': False,
        'default_rate_limit': 0,
        'overrides': {
            'exec': {'confirm': True},
            'delete_file': {'confirm': True},
            'write_file': {'confirm': True},
            'process_kill': {'confirm': True},
            'cron_add': {'confirm': True},
            'cron_remove': {'confirm': True},
            'key_press': {'rate_limit': 30},
            'type_text': {'rate_limit': 20},
            'click': {'rate_limit': 60},
            'move_mouse': {'rate_limit': 120},
            'drag_mouse': {'rate_limit': 30},
            'scroll': {'rate_limit': 120},
        }
    },
    'restricted': {
        'description': '限制模式 — 僅允許讀取和搜尋',
        'default_allow': False,
        'default_confirm': False,
        'default_rate_limit': 10,
        'overrides': {
            'read_file': {'allowed': True},
            'list_dir': {'allowed': True},
            'web_search': {'allowed': True},
            'web_fetch': {'allowed': True},
            'clipboard_read': {'allowed': True},
            'process_list': {'allowed': True},
            'memory_search': {'allowed': True},
            'system_info': {'allowed': True},
            'summarize': {'allowed': True},
            'weather': {'allowed': True},
            'screenshot': {'allowed': True},
            'ig_get_posts': {'allowed': True},
            'ig_get_comments': {'allowed': True},
        }
    }
}


class PermissionManager:
    def __init__(self, config_mgr):
        self.config = config_mgr
        self._lock = threading.Lock()
        self._call_log = {}  # tool_name → [timestamps]
        self._permissions = self._load()
        self._pending_confirms = {}  # request_id → {tool, args, event}

    def _load(self):
        if PERMISSIONS_FILE.exists():
            try:
                with open(PERMISSIONS_FILE, 'r', encoding='utf-8') as f:
                    return json.load(f)
            except Exception:
                pass
        return {
            'preset': 'full',
            'custom': {}
        }

    def _save(self):
        PERMISSIONS_FILE.parent.mkdir(parents=True, exist_ok=True)
        with open(PERMISSIONS_FILE, 'w', encoding='utf-8') as f:
            json.dump(self._permissions, f, ensure_ascii=False, indent=2)

    def get_preset(self):
        return self._permissions.get('preset', 'full')

    def set_preset(self, preset_name):
        if preset_name not in PRESETS:
            return False
        with self._lock:
            self._permissions['preset'] = preset_name
            self._save()
        return True

    def get_tool_permission(self, tool_name):
        """取得某工具的有效權限"""
        preset_name = self._permissions.get('preset', 'full')
        preset = PRESETS.get(preset_name, PRESETS['full'])
        custom = self._permissions.get('custom', {})

        # 自訂覆蓋 > preset 覆蓋 > preset 預設
        base = {
            'allowed': preset['default_allow'],
            'confirm': preset['default_confirm'],
            'rate_limit': preset['default_rate_limit'],
            'path_whitelist': [],
            'path_blacklist': [] if preset_name == 'full' else ['/System', '/usr', '/bin', '/sbin',
                               'C:\\Windows', 'C:\\Program Files'],
        }

        # 套用 preset overrides
        if tool_name in preset.get('overrides', {}):
            base.update(preset['overrides'][tool_name])

        # 套用自訂覆蓋
        if tool_name in custom:
            base.update(custom[tool_name])

        return base

    def check_permission(self, tool_name, args=None):
        """
        檢查工具是否可執行
        回傳: (allowed: bool, reason: str, needs_confirm: bool)
        """
        perm = self.get_tool_permission(tool_name)

        # 1. 是否允許
        if not perm.get('allowed', True):
            return False, f'工具 {tool_name} 已被權限系統禁止', False

        # 2. 速率限制
        rate_limit = perm.get('rate_limit', 0)
        if rate_limit > 0:
            now = time.time()
            with self._lock:
                calls = self._call_log.get(tool_name, [])
                # 清除超過 60 秒的紀錄
                calls = [t for t in calls if now - t < 60]
                self._call_log[tool_name] = calls
                if len(calls) >= rate_limit:
                    return False, f'工具 {tool_name} 已達速率限制（{rate_limit}/分鐘）', False

        # 3. 路徑檢查（針對檔案操作工具）
        if args and tool_name in ('read_file', 'write_file', 'edit_file',
                                   'delete_file', 'list_dir', 'exec'):
            path_arg = args.get('path', args.get('command', ''))
            blacklist = perm.get('path_blacklist', [])
            for blocked in blacklist:
                if blocked and blocked.lower() in str(path_arg).lower():
                    return False, f'路徑 {path_arg} 在黑名單中', False

        # 4. 是否需要確認
        needs_confirm = perm.get('confirm', False)

        return True, 'ok', needs_confirm

    def record_call(self, tool_name):
        """記錄工具呼叫（用於速率限制）"""
        with self._lock:
            if tool_name not in self._call_log:
                self._call_log[tool_name] = []
            self._call_log[tool_name].append(time.time())

    def set_tool_permission(self, tool_name, overrides):
        """設定單一工具的自訂權限"""
        with self._lock:
            if 'custom' not in self._permissions:
                self._permissions['custom'] = {}
            self._permissions['custom'][tool_name] = overrides
            self._save()

    def remove_tool_override(self, tool_name):
        """移除工具的自訂覆蓋"""
        with self._lock:
            custom = self._permissions.get('custom', {})
            if tool_name in custom:
                del custom[tool_name]
                self._save()

    def get_all_permissions(self):
        """取得所有工具的有效權限（給 UI 用）"""
        return {
            'preset': self.get_preset(),
            'presets': {k: v['description'] for k, v in PRESETS.items()},
            'preset_rules': PRESETS,
            'custom': self._permissions.get('custom', {}),
        }

    def get_stats(self):
        """取得呼叫統計"""
        now = time.time()
        stats = {}
        with self._lock:
            for tool, calls in self._call_log.items():
                recent = [t for t in calls if now - t < 60]
                stats[tool] = len(recent)
        return stats
