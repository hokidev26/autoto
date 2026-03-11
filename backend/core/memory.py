#!/usr/bin/env python3
"""
記憶管理器
強化版：核心記憶永不歸檔、智能過濾問句、自動合併相似記憶
"""

import json
import os
import re
import threading
import uuid
from datetime import datetime
from pathlib import Path

MEMORY_DIR = Path.home() / '.autoto'
MEMORY_FILE = MEMORY_DIR / 'memories.json'

# 自動捕獲信號：(信號詞, 是否要求句首匹配)
CAPTURE_SIGNALS = [
    ('記住', False), ('請記住', False), ('幫我記', False),
    ('我叫', True), ('我是', True), ('我住', True),
    ('我喜歡', True), ('我不喜歡', True), ('我習慣', True),
    ('我常用', True), ('我偏好', True),
    ('以後都', False), ('每次都', False), ('預設', False),
    ('設定為', False), ('改成', False),
    ('remember', False), ('my name is', False),
    ('i am', True), ("i'm", True), ('i live', True),
    ('i prefer', True), ('i like', True), ("i don't like", True),
]

# 問句特徵 — 含有這些就不存
QUESTION_PATTERNS = [
    '嗎', '呢', '？', '?',
    '你知道', '你記得', '還記得', '是不是', '有沒有',
    '什麼', '哪裡', '怎麼', '為什麼', '多少',
    'do you know', 'do you remember', 'what', 'where', 'how', 'why',
]

# 核心記憶特徵 — 匹配到的記憶永遠不會被歸檔
CORE_PATTERNS = [
    '我叫', '我是', '我住', '我的名字', '名字是', '名字叫',
    '我在', '我從', '我來自',
    '我喜歡', '我不喜歡', '我偏好', '我習慣',
    '記住', '請記住', '幫我記', '永遠',
    'my name', 'i am', "i'm", 'i live', 'remember',
]


class MemoryManager:
    def __init__(self, config_mgr):
        self.config = config_mgr
        self._lock = threading.Lock()
        MEMORY_DIR.mkdir(parents=True, exist_ok=True)
        self._memories = self._load()

    def _load(self):
        if MEMORY_FILE.exists():
            try:
                with open(MEMORY_FILE, 'r', encoding='utf-8') as f:
                    return json.load(f)
            except Exception:
                return []
        return []

    def _save(self):
        with open(MEMORY_FILE, 'w', encoding='utf-8') as f:
            json.dump(self._memories, f, ensure_ascii=False, indent=2)

    def get_all(self):
        with self._lock:
            return list(self._memories)

    def add(self, content, source='manual'):
        content = content.strip()
        if not content:
            return
        with self._lock:
            for m in self._memories:
                if m['content'] == content:
                    return
                if self._similarity(m['content'], content) > 0.8:
                    m['content'] = content
                    m['timestamp'] = datetime.now().strftime('%Y-%m-%d %H:%M')
                    self._save()
                    return

            entry = {
                'id': str(uuid.uuid4())[:8],
                'content': content,
                'source': source,
                'pinned': self._is_core(content),
                'timestamp': datetime.now().strftime('%Y-%m-%d %H:%M')
            }
            self._memories.append(entry)
            self._save()

            archive_threshold = self.config.get('memory.autoArchive', 50)
            if len(self._memories) > archive_threshold:
                self._archive()

    def delete(self, memory_id):
        with self._lock:
            self._memories = [m for m in self._memories if m.get('id') != memory_id]
            self._save()

    def toggle_pin(self, memory_id):
        """切換釘選狀態"""
        with self._lock:
            for m in self._memories:
                if m.get('id') == memory_id:
                    m['pinned'] = not m.get('pinned', False)
                    self._save()
                    return

    def _is_core(self, text):
        """判斷是否為核心記憶（身份、偏好等永久資訊）"""
        text_lower = text.lower()
        for pat in CORE_PATTERNS:
            if pat in text_lower:
                return True
        return False

    def _similarity(self, a, b):
        if not a or not b:
            return 0.0
        def bigrams(s):
            s = s.lower()
            return set(s[i:i+2] for i in range(len(s)-1)) if len(s) > 1 else {s}
        ba, bb = bigrams(a), bigrams(b)
        if not ba or not bb:
            return 0.0
        return len(ba & bb) / max(len(ba | bb), 1)

    def _is_question(self, text):
        text_lower = text.lower().strip()
        for pat in QUESTION_PATTERNS:
            if pat in text_lower:
                return True
        return False

    def recall(self, query, max_tokens=500):
        """
        智能記憶召回：
        - 核心記憶（pinned）永遠注入
        - 記憶 ≤ 10 條：全部注入
        - 記憶 > 10 條：核心 + 相似度篩選
        """
        with self._lock:
            if not self._memories:
                return []

            if len(self._memories) <= 10:
                return list(self._memories)

            # 核心記憶一定要
            pinned = [m for m in self._memories if m.get('pinned')]
            others = [m for m in self._memories if not m.get('pinned')]

        # 對非核心記憶做相似度篩選
        query_lower = query.lower()
        query_chars = set(query_lower)
        scored = []

        for m in others:
            content_lower = m['content'].lower()
            score = 0.0
            sim = self._similarity(query_lower, content_lower)
            score += sim * 3.0
            content_chars = set(content_lower)
            score += len(query_chars & content_chars) * 0.2
            for word in query_lower.split():
                if len(word) >= 2 and word in content_lower:
                    score += 1.5
            for i in range(len(query_lower)):
                for length in [2, 3, 4]:
                    sub = query_lower[i:i+length]
                    if len(sub) == length and sub in content_lower:
                        score += length * 0.5
            if score > 0.5:
                scored.append((score, m))

        scored.sort(key=lambda x: x[0], reverse=True)

        # 核心記憶 + 最相關的一般記憶，總共不超過 8 條
        result = list(pinned)
        total_chars = sum(len(m['content']) for m in result)
        for _, m in scored:
            if len(result) >= 8:
                break
            if total_chars + len(m['content']) > max_tokens * 2:
                break
            result.append(m)
            total_chars += len(m['content'])
        return result

    def auto_capture(self, user_msg, bot_response):
        if not self.config.get('memory.enabled', True):
            return
        msg = user_msg.strip()
        if len(msg) < 4:
            return
        if self._is_question(msg):
            return
        msg_lower = msg.lower()
        for signal, require_start in CAPTURE_SIGNALS:
            if require_start:
                if msg_lower.startswith(signal):
                    self.add(msg, source='auto')
                    return
            else:
                if signal in msg_lower:
                    self.add(msg, source='auto')
                    return

    def _archive(self):
        """
        智能歸檔：
        - 核心記憶（pinned）永遠保留，不歸檔
        - 只歸檔非核心的舊記憶
        """
        archive_file = MEMORY_DIR / 'memories_archive.json'
        archive = []
        if archive_file.exists():
            try:
                with open(archive_file, 'r', encoding='utf-8') as f:
                    archive = json.load(f)
            except Exception:
                pass

        pinned = [m for m in self._memories if m.get('pinned')]
        normal = [m for m in self._memories if not m.get('pinned')]

        # 只歸檔非核心記憶，保留最近 20 條一般記憶
        if len(normal) > 20:
            to_archive = normal[:-20]
            normal = normal[-20:]
            archive.extend(to_archive)
            with open(archive_file, 'w', encoding='utf-8') as f:
                json.dump(archive, f, ensure_ascii=False, indent=2)

        self._memories = pinned + normal
        self._save()
