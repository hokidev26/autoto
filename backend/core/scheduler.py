#!/usr/bin/env python3
"""
排程器 — 讓 AutoTo 24/7 主動執行任務
支援：cron 表達式、間隔、每日、每週、每月
每個任務可指定模型，並保留執行紀錄
"""

import collections
import json
import os
import re
import subprocess
import threading
import time
import uuid
from datetime import datetime, timedelta
from pathlib import Path

AUTOTO_DIR = Path.home() / '.autoto'
SCHEDULER_FILE = AUTOTO_DIR / 'schedules.json'
EXEC_LOG_FILE = AUTOTO_DIR / 'scheduler_logs.json'
MAX_LOGS = 500


def _parse_cron_field(field, min_val, max_val):
    """解析單一 cron 欄位，回傳符合的數字集合"""
    values = set()
    for part in field.split(','):
        part = part.strip()
        if part == '*':
            values.update(range(min_val, max_val + 1))
        elif '/' in part:
            base, step = part.split('/', 1)
            step = int(step)
            start = min_val if base == '*' else int(base)
            values.update(range(start, max_val + 1, step))
        elif '-' in part:
            lo, hi = part.split('-', 1)
            values.update(range(int(lo), int(hi) + 1))
        else:
            values.add(int(part))
    return values


def cron_matches(expr, dt):
    """檢查 datetime 是否符合 cron 表達式 (min hour dom month dow)"""
    fields = expr.strip().split()
    if len(fields) != 5:
        return False
    minute = _parse_cron_field(fields[0], 0, 59)
    hour = _parse_cron_field(fields[1], 0, 23)
    dom = _parse_cron_field(fields[2], 1, 31)
    month = _parse_cron_field(fields[3], 1, 12)
    dow = _parse_cron_field(fields[4], 0, 6)  # 0=Sunday
    py_dow = (dt.weekday() + 1) % 7  # Python: 0=Mon → convert to 0=Sun
    return (dt.minute in minute and dt.hour in hour and
            dt.day in dom and dt.month in month and py_dow in dow)


def simple_to_cron(schedule):
    """
    把 Simple 模式的排程設定轉成 cron 表達式
    schedule: {
      mode: 'interval'|'daily'|'weekly'|'monthly',
      time: '09:00',
      interval_minutes: 30,
      weekdays: [0,1,2,...],  # 0=Mon
      month_day: 1
    }
    """
    mode = schedule.get('mode', 'daily')
    t = schedule.get('time', '09:00')
    parts = t.split(':')
    hour = int(parts[0]) if len(parts) > 0 else 9
    minute = int(parts[1]) if len(parts) > 1 else 0

    if mode == 'interval':
        mins = int(schedule.get('interval_minutes', 30))
        if mins < 60:
            return f'*/{mins} * * * *'
        else:
            hours = mins // 60
            return f'0 */{hours} * * *'
    elif mode == 'daily':
        return f'{minute} {hour} * * *'
    elif mode == 'weekly':
        weekdays = schedule.get('weekdays', [0])
        # 轉成 cron 的 dow (0=Sun)
        cron_days = sorted(set((d + 1) % 7 for d in weekdays))
        return f'{minute} {hour} * * {",".join(str(d) for d in cron_days)}'
    elif mode == 'monthly':
        day = int(schedule.get('month_day', 1))
        return f'{minute} {hour} {day} * *'
    return f'{minute} {hour} * * *'


class Scheduler:
    """
    排程管理器
    每個排程項目：
    {
      id, name, description,
      type: 'cron'|'simple',
      expression: cron expr (type=cron),
      schedule: { mode, time, weekdays, ... } (type=simple),
      action: 'agent_message' | 'command',
      payload: { message / command, session_id },
      model: 'default' | 'claude' | 'gemini' | ...,
      enabled, created, last_run, run_count, next_run
    }
    """

    def __init__(self, config_mgr, agent_loop):
        self.config = config_mgr
        self.agent = agent_loop
        self._lock = threading.Lock()
        self._schedules = self._load()
        self._exec_logs = self._load_logs()
        self._running = False
        self._thread = None

    # ==================== 持久化 ====================

    def _load(self):
        if SCHEDULER_FILE.exists():
            try:
                with open(SCHEDULER_FILE, 'r', encoding='utf-8') as f:
                    return json.load(f)
            except Exception:
                return []
        return []

    def _save(self):
        SCHEDULER_FILE.parent.mkdir(parents=True, exist_ok=True)
        with open(SCHEDULER_FILE, 'w', encoding='utf-8') as f:
            json.dump(self._schedules, f, ensure_ascii=False, indent=2)

    def _load_logs(self):
        if EXEC_LOG_FILE.exists():
            try:
                with open(EXEC_LOG_FILE, 'r', encoding='utf-8') as f:
                    logs = json.load(f)
                return logs[-MAX_LOGS:]
            except Exception:
                return []
        return []

    def _save_logs(self):
        EXEC_LOG_FILE.parent.mkdir(parents=True, exist_ok=True)
        with open(EXEC_LOG_FILE, 'w', encoding='utf-8') as f:
            json.dump(self._exec_logs[-MAX_LOGS:], f, ensure_ascii=False, indent=2)

    def _add_log(self, schedule_id, name, status, detail=''):
        log = {
            'id': str(uuid.uuid4())[:8],
            'schedule_id': schedule_id,
            'name': name,
            'status': status,  # 'success' | 'error' | 'running'
            'detail': detail[:500],
            'timestamp': datetime.now().isoformat(),
        }
        with self._lock:
            self._exec_logs.append(log)
            if len(self._exec_logs) > MAX_LOGS:
                self._exec_logs = self._exec_logs[-MAX_LOGS:]
            self._save_logs()
        return log

    # ==================== CRUD ====================

    def get_all(self):
        with self._lock:
            # 計算 next_run
            now = datetime.now()
            for s in self._schedules:
                s['next_run'] = self._calc_next_run(s, now)
            return list(self._schedules)

    def get_logs(self, schedule_id=None, limit=50):
        with self._lock:
            logs = list(self._exec_logs)
        if schedule_id:
            logs = [l for l in logs if l.get('schedule_id') == schedule_id]
        return logs[-limit:]

    def clear_logs(self, schedule_id=None):
        with self._lock:
            if schedule_id:
                self._exec_logs = [l for l in self._exec_logs if l.get('schedule_id') != schedule_id]
            else:
                self._exec_logs = []
            self._save_logs()

    def add(self, name, stype, expression='', action='agent_message',
            payload=None, enabled=True, schedule=None, model='default',
            description=''):
        """新增排程"""
        # Simple 模式自動轉 cron
        effective_expr = expression
        if stype == 'simple' and schedule:
            effective_expr = simple_to_cron(schedule)

        item = {
            'id': str(uuid.uuid4())[:8],
            'name': name,
            'description': description,
            'type': stype,              # cron | simple
            'expression': effective_expr,
            'schedule': schedule or {},  # Simple 模式的原始設定
            'action': action,           # agent_message | command
            'payload': payload or {},
            'model': model,             # default | claude | gemini | ...
            'enabled': enabled,
            'created': datetime.now().isoformat(),
            'last_run': None,
            'run_count': 0,
        }
        with self._lock:
            self._schedules.append(item)
            self._save()
        return item

    def update(self, schedule_id, data):
        with self._lock:
            for s in self._schedules:
                if s['id'] == schedule_id:
                    for k, v in data.items():
                        if k != 'id':
                            s[k] = v
                    # Simple 模式更新時重新計算 cron
                    if s.get('type') == 'simple' and 'schedule' in data:
                        s['expression'] = simple_to_cron(data['schedule'])
                    self._save()
                    return s
        return None

    def delete(self, schedule_id):
        with self._lock:
            self._schedules = [s for s in self._schedules if s['id'] != schedule_id]
            self._save()
        return True

    def toggle(self, schedule_id):
        with self._lock:
            for s in self._schedules:
                if s['id'] == schedule_id:
                    s['enabled'] = not s['enabled']
                    self._save()
                    return s
        return None

    def run_now(self, schedule_id):
        """手動觸發執行"""
        with self._lock:
            target = None
            for s in self._schedules:
                if s['id'] == schedule_id:
                    target = s
                    break
        if not target:
            return False
        threading.Thread(target=self._execute, args=(target,), daemon=True).start()
        return True

    # ==================== 排程引擎 ====================

    def start(self):
        if self._running:
            return
        self._running = True
        self._thread = threading.Thread(target=self._loop, daemon=True)
        self._thread.start()
        count = len([s for s in self._schedules if s.get('enabled')])
        print(f'  ⏰ 排程器已啟動（{count} 個啟用中）')

    def stop(self):
        self._running = False

    def _loop(self):
        while self._running:
            now = datetime.now()
            with self._lock:
                for s in self._schedules:
                    if not s.get('enabled'):
                        continue
                    if self._should_run(s, now):
                        threading.Thread(
                            target=self._execute, args=(s,), daemon=True
                        ).start()
            time.sleep(30)

    def _should_run(self, s, now):
        expr = s.get('expression', '')
        last = s.get('last_run')

        # Simple 和 cron 都用 cron 表達式判斷
        stype = s.get('type', 'cron')
        if stype in ('cron', 'simple'):
            if not cron_matches(expr, now):
                return False
            if last:
                last_dt = datetime.fromisoformat(last)
                if last_dt.strftime('%Y%m%d%H%M') == now.strftime('%Y%m%d%H%M'):
                    return False
            return True

        elif stype == 'interval':
            try:
                interval_sec = int(expr)
            except (ValueError, TypeError):
                return False
            if not last:
                return True
            last_dt = datetime.fromisoformat(last)
            return (now - last_dt).total_seconds() >= interval_sec

        elif stype == 'once':
            if last:
                return False
            try:
                target = datetime.fromisoformat(expr)
                return now >= target
            except ValueError:
                return False

        return False

    def _execute(self, s):
        """執行排程任務"""
        log_id = self._add_log(s['id'], s['name'], 'running', '執行中...')
        try:
            action = s.get('action', 'agent_message')
            payload = s.get('payload', {})
            result_detail = ''

            if action == 'agent_message':
                msg = payload.get('message', '')
                sid = payload.get('session_id', 'scheduler-' + s['id'])
                if msg:
                    print(f'  ⏰ 排程執行: [{s["name"]}] → {msg[:50]}')
                    response = self.agent.process_message(sid, msg, source='scheduler')
                    result_detail = response[:300] if response else '(no response)'

            elif action == 'command':
                cmd = payload.get('command', '')
                if cmd:
                    print(f'  ⏰ 排程執行: [{s["name"]}] → $ {cmd[:50]}')
                    res = subprocess.run(cmd, shell=True, capture_output=True,
                                         text=True, timeout=120,
                                         cwd=str(Path.home()))
                    if res.returncode != 0:
                        raise Exception(f'Exit code {res.returncode}: {res.stderr[:200]}')
                    result_detail = res.stdout[:300] if res.stdout else '(done)'

            # 更新執行紀錄
            with self._lock:
                s['last_run'] = datetime.now().isoformat()
                s['run_count'] = s.get('run_count', 0) + 1
                if s.get('type') == 'once':
                    s['enabled'] = False
                self._save()

            # 更新 log 為成功
            self._add_log(s['id'], s['name'], 'success', result_detail)

        except Exception as e:
            err = str(e)[:300]
            print(f'  ❌ 排程執行錯誤 [{s.get("name", "?")}]: {err}')
            self._add_log(s['id'], s['name'], 'error', err)

    def _calc_next_run(self, s, now):
        """估算下次執行時間（供 UI 顯示）"""
        if not s.get('enabled'):
            return None
        stype = s.get('type', 'cron')
        expr = s.get('expression', '')

        if stype in ('cron', 'simple') and expr:
            # 往後掃最多 7 天找下一個匹配
            check = now.replace(second=0, microsecond=0) + timedelta(minutes=1)
            for _ in range(7 * 24 * 60):
                if cron_matches(expr, check):
                    return check.isoformat()
                check += timedelta(minutes=1)

        elif stype == 'interval':
            try:
                interval_sec = int(expr)
            except (ValueError, TypeError):
                return None
            last = s.get('last_run')
            if last:
                return (datetime.fromisoformat(last) + timedelta(seconds=interval_sec)).isoformat()
            return now.isoformat()

        elif stype == 'once':
            if s.get('last_run'):
                return None
            return expr

        return None
