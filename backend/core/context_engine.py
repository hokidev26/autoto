#!/usr/bin/env python3
"""
Context Engine — 智慧上下文管理
分層壓縮、跨 session 共享、token 精確控制
"""

import json
import hashlib
import threading
import time
from datetime import datetime
from pathlib import Path

CONTEXT_DIR = Path.home() / '.autoto'
CONTEXT_FILE = CONTEXT_DIR / 'context_store.json'


class ContextEngine:
    """
    三層上下文管理：
    1. 原文層 — 最近 3 輪保留原文
    2. 摘要層 — 較舊的對話壓縮成摘要
    3. 關鍵資訊層 — 提取跨 session 的關鍵事實
    """

    def __init__(self, config_mgr):
        self.config = config_mgr
        self._lock = threading.Lock()
        self._store = self._load()

    def _load(self):
        if CONTEXT_FILE.exists():
            try:
                with open(CONTEXT_FILE, 'r', encoding='utf-8') as f:
                    return json.load(f)
            except Exception:
                pass
        return {'summaries': {}, 'facts': [], 'cross_session': {}}

    def _save(self):
        CONTEXT_DIR.mkdir(parents=True, exist_ok=True)
        with open(CONTEXT_FILE, 'w', encoding='utf-8') as f:
            json.dump(self._store, f, ensure_ascii=False, indent=2)

    # ==================== 建構上下文 ====================

    def build_context(self, session_id, history, current_message, token_budget):
        """
        根據 token 預算，智慧組裝上下文：
        1. 最近 3 輪原文（一定保留）
        2. 較舊的壓縮摘要
        3. 跨 session 的關鍵事實
        回傳: list of message dicts
        """
        context = []
        used = self._estimate_tokens(current_message)
        reply_reserve = int(token_budget * 0.4)
        available = max(token_budget - used - reply_reserve, 300)

        # 第一層：跨 session 關鍵事實（最高優先）
        facts = self._get_relevant_facts(session_id, current_message)
        if facts:
            facts_text = '## 跨對話關鍵資訊\n' + '\n'.join(f'- {f}' for f in facts)
            facts_tokens = self._estimate_tokens(facts_text)
            if facts_tokens < available * 0.15:  # 最多佔 15%
                context.append({'role': 'system', 'content': facts_text})
                available -= facts_tokens

        # 第二層：壓縮摘要（較舊的對話）
        summary = self._get_summary(session_id)
        if summary and len(history) > 6:
            summary_tokens = self._estimate_tokens(summary)
            if summary_tokens < available * 0.25:  # 最多佔 25%
                context.append({'role': 'system',
                    'content': f'## 先前對話摘要\n{summary}'})
                available -= summary_tokens

        # 第三層：最近的原文（從最新往回取）
        recent = history[-7:-1] if len(history) > 1 else []
        selected = []
        for msg in reversed(recent):
            content = msg.get('content', '')
            # 去掉工具細節，只保留摘要
            if msg.get('_tools'):
                content = self._trim_tool_content(content)
            msg_tokens = self._estimate_tokens(content)
            if msg_tokens > available:
                break
            selected.insert(0, {'role': msg['role'], 'content': content})
            available -= msg_tokens

        context.extend(selected)
        return context

    # ==================== 壓縮策略 ====================

    def compress_history(self, session_id, history, llm_call=None):
        """
        壓縮較舊的對話歷史：
        - 最近 6 輪保留原文
        - 更早的壓縮成摘要
        - 提取關鍵事實存入跨 session 儲存
        """
        if len(history) < 8:
            return  # 太短不需要壓縮

        # 要壓縮的部分（排除最近 6 輪）
        to_compress = history[:-6]
        old_summary = self._get_summary(session_id)

        # 組裝要壓縮的文字
        lines = []
        if old_summary:
            lines.append(f'[先前摘要] {old_summary}')
        for msg in to_compress:
            role = '用戶' if msg['role'] == 'user' else 'AI'
            content = msg.get('content', '')[:200]
            lines.append(f'{role}: {content}')

        text_to_compress = '\n'.join(lines)

        if llm_call:
            # 用 LLM 做智慧壓縮
            new_summary = self._llm_compress(text_to_compress, llm_call)
            # 同時提取關鍵事實
            facts = self._llm_extract_facts(text_to_compress, llm_call)
            if facts:
                self._add_facts(session_id, facts)
        else:
            # 無 LLM 時用簡單規則壓縮
            new_summary = self._rule_compress(to_compress, old_summary)

        with self._lock:
            self._store['summaries'][session_id] = {
                'text': new_summary,
                'updated': datetime.now().isoformat(),
                'msg_count': len(to_compress),
            }
            self._save()

    def _llm_compress(self, text, llm_call):
        """用 LLM 壓縮對話"""
        prompt = (
            '請將以下對話歷史壓縮成一段簡潔的摘要（100-200字），'
            '保留關鍵資訊、用戶偏好、重要決定和待辦事項：\n\n'
            f'{text[:3000]}'
        )
        try:
            result = llm_call(prompt)
            return result[:500] if result else self._rule_compress_text(text)
        except Exception:
            return self._rule_compress_text(text)

    def _llm_extract_facts(self, text, llm_call):
        """用 LLM 提取關鍵事實"""
        prompt = (
            '從以下對話中提取關鍵事實（用戶的身份、偏好、重要決定等），'
            '每個事實一行，最多 5 條。如果沒有值得記錄的事實，回覆「無」：\n\n'
            f'{text[:3000]}'
        )
        try:
            result = llm_call(prompt)
            if not result or '無' in result.strip()[:5]:
                return []
            return [line.strip().lstrip('- ·•') for line in result.strip().split('\n')
                    if line.strip() and len(line.strip()) > 3][:5]
        except Exception:
            return []

    def _rule_compress(self, messages, old_summary=''):
        """無 LLM 時的規則壓縮"""
        parts = []
        if old_summary:
            parts.append(old_summary)
        for msg in messages:
            content = msg.get('content', '')
            role = '用戶' if msg['role'] == 'user' else 'AI'
            # 只保留前 80 字
            short = content[:80].replace('\n', ' ')
            if len(content) > 80:
                short += '...'
            parts.append(f'{role}: {short}')
        return '\n'.join(parts)[-800:]

    def _rule_compress_text(self, text):
        """文字截斷壓縮"""
        return text[:500]

    # ==================== 跨 session 共享 ====================

    def _get_relevant_facts(self, session_id, query):
        """取得與當前問題相關的跨 session 事實"""
        all_facts = self._store.get('facts', [])
        if not all_facts:
            return []

        query_lower = query.lower()
        scored = []
        for fact in all_facts:
            text = fact.get('text', '')
            score = 0
            # 關鍵字匹配
            for word in query_lower.split():
                if len(word) >= 2 and word in text.lower():
                    score += 2
            # 字元重疊
            overlap = len(set(query_lower) & set(text.lower()))
            score += overlap * 0.1
            if score > 0.5:
                scored.append((score, text))

        scored.sort(key=lambda x: x[0], reverse=True)
        return [text for _, text in scored[:5]]

    def _add_facts(self, session_id, facts):
        """新增跨 session 事實"""
        with self._lock:
            existing = {f.get('text', '') for f in self._store.get('facts', [])}
            for fact_text in facts:
                if fact_text not in existing:
                    self._store.setdefault('facts', []).append({
                        'text': fact_text,
                        'source_session': session_id,
                        'created': datetime.now().isoformat(),
                    })
            # 最多保留 50 條
            if len(self._store['facts']) > 50:
                self._store['facts'] = self._store['facts'][-50:]
            self._save()

    def share_context(self, from_session, to_session):
        """把一個 session 的上下文帶到另一個 session"""
        summary = self._get_summary(from_session)
        if summary:
            with self._lock:
                self._store.setdefault('cross_session', {})[to_session] = {
                    'from': from_session,
                    'summary': summary,
                    'shared_at': datetime.now().isoformat(),
                }
                self._save()

    def get_shared_context(self, session_id):
        """取得從其他 session 帶過來的上下文"""
        shared = self._store.get('cross_session', {}).get(session_id)
        if shared:
            return shared.get('summary', '')
        return ''

    # ==================== 工具方法 ====================

    def _get_summary(self, session_id):
        entry = self._store.get('summaries', {}).get(session_id)
        return entry.get('text', '') if entry else ''

    def _trim_tool_content(self, content):
        """去掉工具執行的冗長輸出，只保留結果摘要"""
        if len(content) > 300:
            return content[:150] + '\n...(已截斷)...\n' + content[-100:]
        return content

    @staticmethod
    def _estimate_tokens(text):
        if not text:
            return 0
        cn = sum(1 for c in text if '\u4e00' <= c <= '\u9fff')
        en = len(text) - cn
        return int(cn / 1.5 + en / 4)

    def get_facts(self):
        """取得所有跨 session 事實（供 API 用）"""
        return self._store.get('facts', [])

    def delete_fact(self, fact_text):
        with self._lock:
            self._store['facts'] = [
                f for f in self._store.get('facts', [])
                if f.get('text') != fact_text
            ]
            self._save()

    def get_stats(self):
        """取得 Context Engine 統計"""
        return {
            'summaries': len(self._store.get('summaries', {})),
            'facts': len(self._store.get('facts', [])),
            'cross_sessions': len(self._store.get('cross_session', {})),
        }
