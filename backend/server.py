#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
AutoTo Backend Server
統一提供 API、排程、聊天平台與 Web UI 的後端入口
"""

import argparse
import asyncio
import json
import os
import re
import sys
import threading
import time
from datetime import datetime
from pathlib import Path

from flask import Flask, request, jsonify, send_file, send_from_directory
from flask_cors import CORS

# 內部模組
from core.agent import AgentLoop
from core.config import ConfigManager
from core.memory import MemoryManager
from core.camera import CameraManager
from core.smarthome import SmartHomeManager
from channels.gateway import ChannelGateway
from core.scheduler import Scheduler
from core.sandbox import PermissionManager

app = Flask(__name__)
CORS(app)

# 全域實例
config_mgr = ConfigManager()
memory_mgr = MemoryManager(config_mgr)
camera_mgr = CameraManager(config_mgr)
smarthome_mgr = SmartHomeManager(config_mgr)
agent = AgentLoop(config_mgr, memory_mgr)
gateway = ChannelGateway(config_mgr, agent)
permission_mgr = PermissionManager(config_mgr)
scheduler = Scheduler(config_mgr, agent)

# 把 permission_mgr 注入 agent，讓 agent loop 能做權限檢查
agent.permissions = permission_mgr
# 注入攝影機和智慧家電管理器，讓 tools 可以使用
agent.camera_mgr = camera_mgr
agent.smarthome_mgr = smarthome_mgr

BACKEND_DIR = Path(__file__).resolve().parent
PROJECT_DIR = BACKEND_DIR.parent


def get_web_ui_dir():
    candidates = [
        PROJECT_DIR / 'renderer',
        PROJECT_DIR / 'electron-app' / 'renderer',
    ]
    for candidate in candidates:
        if (candidate / 'index.html').exists():
            return candidate
    return None


# ==================== API 端點 ====================

@app.route('/api/status', methods=['GET'])
def status():
    channels = gateway.get_active_channels()
    return jsonify({
        'status': 'running',
        'version': '1.0.0',
        'timestamp': datetime.now().isoformat(),
        'channels': channels
    })


@app.route('/api/chat', methods=['POST'])
def chat():
    """主對話端點"""
    try:
        data = request.json
        message = data.get('message', '').strip()
        session_id = data.get('session_id', 'web-default')

        if not message:
            return jsonify({'success': False, 'error': '訊息不能為空'}), 400

        response = agent.process_message(session_id, message, source='web')
        return jsonify({
            'success': True,
            'response': response,
            'timestamp': datetime.now().isoformat()
        })
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/config', methods=['GET'])
def get_config():
    cfg = config_mgr.get_safe_config()
    return jsonify(cfg)


@app.route('/api/config', methods=['POST'])
def update_config():
    try:
        data = request.json
        config_mgr.update(data)
        # 重新載入 gateway 的 channels
        gateway.reload_channels()
        return jsonify({'success': True})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/memory', methods=['GET'])
def get_memories():
    memories = memory_mgr.get_all()
    return jsonify({'memories': memories})


@app.route('/api/memory', methods=['POST'])
def add_memory():
    try:
        data = request.json
        content = data.get('content', '').strip()
        if not content:
            return jsonify({'success': False, 'error': '內容不能為空'}), 400
        memory_mgr.add(content, source='manual')
        return jsonify({'success': True})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/memory/<memory_id>', methods=['DELETE'])
def delete_memory(memory_id):
    try:
        memory_mgr.delete(memory_id)
        return jsonify({'success': True})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/memory/<memory_id>/pin', methods=['POST'])
def toggle_pin_memory(memory_id):
    """切換記憶的釘選狀態"""
    try:
        memory_mgr.toggle_pin(memory_id)
        return jsonify({'success': True})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/session/clear', methods=['POST'])
def clear_session():
    """清除對話 session"""
    try:
        data = request.json or {}
        session_id = data.get('session_id', 'web-default')
        clear_all = data.get('clear_all', False)
        if clear_all:
            agent.clear_all_sessions()
        else:
            if session_id in agent.sessions:
                del agent.sessions[session_id]
            # 也刪除本地檔案
            import os
            safe_name = session_id.replace('/', '_').replace('\\', '_')
            fpath = os.path.join(str(Path.home()), '.autoto', 'sessions', safe_name + '.json')
            if os.path.exists(fpath):
                os.remove(fpath)
        return jsonify({'success': True})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/sessions', methods=['GET'])
def list_sessions():
    """列出所有對話 session（含標題和時間）"""
    sessions_dir = os.path.join(str(Path.home()), '.autoto', 'sessions')
    result = []
    if os.path.isdir(sessions_dir):
        for fname in sorted(os.listdir(sessions_dir), reverse=True):
            if not fname.endswith('.json'):
                continue
            sid = fname[:-5]
            fpath = os.path.join(sessions_dir, fname)
            try:
                mtime = os.path.getmtime(fpath)
                with open(fpath, 'r', encoding='utf-8') as f:
                    data = json.load(f)
                # 取第一個 user 訊息當標題
                title = ''
                for msg in (data if isinstance(data, list) else []):
                    if msg.get('role') == 'user':
                        title = msg.get('content', '')[:40]
                        break
                if not title:
                    title = sid
                result.append({
                    'id': sid,
                    'title': title,
                    'updated': datetime.fromtimestamp(mtime).isoformat(),
                    'messageCount': len(data) if isinstance(data, list) else 0
                })
            except Exception:
                continue
    # 按更新時間排序（最新在前）
    result.sort(key=lambda x: x['updated'], reverse=True)
    return jsonify({'sessions': result})


@app.route('/api/sessions', methods=['POST'])
def create_session():
    """建立空白 session，讓前端可先新增對話再開始聊天"""
    try:
        data = request.json or {}
        session_id = data.get('session_id') or f"web-{int(datetime.now().timestamp() * 1000)}"
        agent.create_session(session_id)
        return jsonify({
            'success': True,
            'session': {
                'id': session_id,
                'title': session_id,
                'updated': datetime.now().isoformat(),
                'messageCount': 0,
            }
        })
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/sessions/<session_id>/messages', methods=['GET'])
def get_session_messages(session_id):
    """取得某個 session 的所有訊息"""
    # 先看記憶體
    msgs = agent.sessions.get(session_id)
    if msgs is None:
        # 從檔案載入
        safe_name = session_id.replace('/', '_').replace('\\', '_')
        fpath = os.path.join(str(Path.home()), '.autoto', 'sessions', safe_name + '.json')
        if os.path.exists(fpath):
            try:
                with open(fpath, 'r', encoding='utf-8') as f:
                    msgs = json.load(f)
            except Exception:
                msgs = []
        else:
            msgs = []
    # 只回傳 user 和 assistant 的訊息（過濾 system/tool）
    filtered = []
    for m in msgs:
        role = m.get('role', '')
        content = m.get('content', '')
        if role in ('user', 'assistant') and content:
            filtered.append({'role': role, 'content': content})
    return jsonify({'messages': filtered})


@app.route('/api/skills', methods=['GET'])
def get_skills():
    """取得所有技能（工具）列表及啟用狀態"""
    skills = config_mgr.get('skills', {})
    tool_schemas = agent.tools.get_schemas()
    result = []
    for s in tool_schemas:
        name = s['function']['name']
        desc = s['function']['description']
        enabled = skills.get(name, True)
        result.append({'name': name, 'description': desc, 'enabled': enabled})
    return jsonify({'skills': result})


@app.route('/api/skills', methods=['POST'])
def update_skill():
    """更新技能啟用狀態"""
    try:
        data = request.json
        name = data.get('name', '')
        enabled = data.get('enabled', True)
        skills = config_mgr.get('skills', {})
        if not isinstance(skills, dict):
            skills = {}
        skills[name] = enabled
        config_mgr.update({'skills': skills})
        return jsonify({'success': True})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/channels/status', methods=['GET'])
def channels_status():
    return jsonify(gateway.get_status())


@app.route('/api/custom-tools', methods=['GET'])
def get_custom_tools():
    """取得自訂技能列表"""
    tools = agent.tools.get_custom_tools()
    return jsonify({'tools': tools})


@app.route('/api/custom-tools', methods=['POST'])
def save_custom_tool():
    """新增或更新自訂技能"""
    try:
        data = request.json
        name = data.get('name', '').strip()
        if not name:
            return jsonify({'success': False, 'error': '名稱不能為空'}), 400
        if not data.get('command', '').strip():
            return jsonify({'success': False, 'error': '指令不能為空'}), 400
        agent.tools.save_custom_tool(data)
        return jsonify({'success': True})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/custom-tools/<name>', methods=['DELETE'])
def delete_custom_tool(name):
    """刪除自訂技能"""
    try:
        agent.tools.delete_custom_tool(name)
        return jsonify({'success': True})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/screenshot/<filename>', methods=['GET'])
def serve_screenshot(filename):
    """提供截圖檔案"""
    ss_dir = Path.home() / '.autoto' / 'screenshots'
    filepath = ss_dir / filename
    if filepath.exists() and filepath.suffix == '.png':
        return send_file(str(filepath), mimetype='image/png')
    return jsonify({'error': 'not found'}), 404


@app.route('/api/channels/restart', methods=['POST'])
def restart_channels():
    try:
        channel = request.json.get('channel')
        gateway.restart_channel(channel)
        return jsonify({'success': True})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


# ==================== 攝影機監控 ====================

@app.route('/api/cameras', methods=['GET'])
def get_cameras():
    return jsonify({'cameras': camera_mgr.get_stream_status()})


@app.route('/api/cameras', methods=['POST'])
def add_camera():
    try:
        cam = camera_mgr.add_camera(request.json)
        return jsonify({'success': True, 'camera': cam})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/cameras/<cam_id>', methods=['PUT'])
def update_camera(cam_id):
    cam = camera_mgr.update_camera(cam_id, request.json)
    if cam:
        return jsonify({'success': True, 'camera': cam})
    return jsonify({'success': False, 'error': 'not found'}), 404


@app.route('/api/cameras/<cam_id>', methods=['DELETE'])
def delete_camera(cam_id):
    camera_mgr.delete_camera(cam_id)
    return jsonify({'success': True})


@app.route('/api/cameras/<cam_id>/stream/start', methods=['POST'])
def start_camera_stream(cam_id):
    ok, msg = camera_mgr.start_stream(cam_id)
    return jsonify({'success': ok, 'message': msg})


@app.route('/api/cameras/<cam_id>/stream/stop', methods=['POST'])
def stop_camera_stream(cam_id):
    camera_mgr.stop_stream(cam_id)
    return jsonify({'success': True})


@app.route('/api/cameras/<cam_id>/snapshot', methods=['GET'])
def camera_snapshot(cam_id):
    """擷取快照並回傳 JPEG"""
    frame = camera_mgr.get_frame(cam_id)
    if frame:
        from io import BytesIO
        return send_file(BytesIO(frame), mimetype='image/jpeg')
    # 嘗試直接擷取
    path = camera_mgr.snapshot(cam_id)
    if path:
        return send_file(path, mimetype='image/jpeg')
    return jsonify({'error': '無法擷取畫面'}), 404


@app.route('/api/cameras/<cam_id>/mjpeg')
def camera_mjpeg(cam_id):
    """MJPEG 即時串流端點 — 瀏覽器用 <img src="..."> 即可顯示"""
    def generate():
        while True:
            frame = camera_mgr.get_frame(cam_id)
            if frame:
                yield (b'--frame\r\n'
                       b'Content-Type: image/jpeg\r\n\r\n' + frame + b'\r\n')
            time.sleep(0.1)  # ~10 fps

    from flask import Response
    return Response(generate(), mimetype='multipart/x-mixed-replace; boundary=frame')


@app.route('/api/cameras/<cam_id>/analyze', methods=['POST'])
def analyze_camera(cam_id):
    """AI 分析攝影機畫面"""
    try:
        data = request.json or {}
        prompt = data.get('prompt', '')
        result = camera_mgr.analyze_frame(cam_id, prompt=prompt, agent=agent)
        return jsonify({'success': True, 'analysis': result})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/cameras/<cam_id>/watch/start', methods=['POST'])
def start_camera_watch(cam_id):
    """啟動 AI 持續監控"""
    try:
        data = request.json or {}
        interval = data.get('interval', 60)
        alert_prompt = data.get('alert_prompt', '')

        def notify(cam_name, alert_text):
            """異常時透過 agent 發通知"""
            msg = f'🚨 攝影機「{cam_name}」偵測到異常：\n{alert_text}'
            agent.process_message('camera-alerts', msg, source='camera-watch')

        ok, msg = camera_mgr.start_watch(
            cam_id, interval=interval,
            alert_prompt=alert_prompt,
            notify_callback=notify
        )
        return jsonify({'success': ok, 'message': msg})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/cameras/<cam_id>/watch/stop', methods=['POST'])
def stop_camera_watch(cam_id):
    camera_mgr.stop_watch(cam_id)
    return jsonify({'success': True})


@app.route('/api/cameras/<cam_id>/watch/status', methods=['GET'])
def camera_watch_status(cam_id):
    return jsonify(camera_mgr.get_watch_status(cam_id))


@app.route('/api/cameras/<cam_id>/watch/logs', methods=['GET'])
def camera_watch_logs(cam_id):
    limit = request.args.get('limit', 20, type=int)
    return jsonify({'logs': camera_mgr.get_watch_logs(cam_id, limit=limit)})


# ==================== 智慧家電 ====================

@app.route('/api/smarthome/platforms', methods=['GET'])
def get_platforms():
    return jsonify({'platforms': smarthome_mgr.get_platforms()})


@app.route('/api/smarthome/platforms', methods=['POST'])
def add_platform():
    try:
        plat = smarthome_mgr.add_platform(request.json)
        return jsonify({'success': True, 'platform': plat})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/smarthome/platforms/<plat_id>', methods=['PUT'])
def update_platform(plat_id):
    plat = smarthome_mgr.update_platform(plat_id, request.json)
    if plat:
        return jsonify({'success': True, 'platform': plat})
    return jsonify({'success': False, 'error': 'not found'}), 404


@app.route('/api/smarthome/platforms/<plat_id>', methods=['DELETE'])
def delete_platform(plat_id):
    smarthome_mgr.delete_platform(plat_id)
    return jsonify({'success': True})


@app.route('/api/smarthome/devices', methods=['GET'])
def get_smart_devices():
    force = request.args.get('refresh', '').lower() == 'true'
    return jsonify({'devices': smarthome_mgr.get_devices(force_refresh=force)})


@app.route('/api/smarthome/devices/<device_id>/control', methods=['POST'])
def control_smart_device(device_id):
    data = request.json or {}
    action = data.get('action', 'toggle')
    params = data.get('params', {})
    result = smarthome_mgr.control_device(device_id, action, params)
    code = 200 if result.get('success') else 500
    return jsonify(result), code


@app.route('/api/smarthome/devices/<device_id>/state', methods=['GET'])
def get_smart_device_state(device_id):
    state = smarthome_mgr.get_device_state(device_id)
    if state:
        return jsonify(state)
    return jsonify({'error': 'not found'}), 404


# ==================== Context Engine ====================

@app.route('/api/context/stats', methods=['GET'])
def context_stats():
    return jsonify(agent.context.get_stats())


@app.route('/api/context/facts', methods=['GET'])
def context_facts():
    return jsonify({'facts': agent.context.get_facts()})


@app.route('/api/context/facts', methods=['DELETE'])
def delete_context_fact():
    text = (request.json or {}).get('text', '')
    agent.context.delete_fact(text)
    return jsonify({'success': True})


@app.route('/api/context/share', methods=['POST'])
def share_context():
    """跨 session 共享上下文"""
    data = request.json or {}
    from_sid = data.get('from_session', '')
    to_sid = data.get('to_session', '')
    if from_sid and to_sid:
        agent.context.share_context(from_sid, to_sid)
        return jsonify({'success': True})
    return jsonify({'success': False, 'error': 'missing session ids'}), 400


# ==================== 排程管理 ====================

@app.route('/api/schedules', methods=['GET'])
def get_schedules():
    return jsonify({'schedules': scheduler.get_all()})


@app.route('/api/schedules', methods=['POST'])
def add_schedule():
    try:
        d = request.json
        item = scheduler.add(
            name=d.get('name', '未命名排程'),
            stype=d.get('type', 'simple'),
            expression=d.get('expression', ''),
            action=d.get('action', 'agent_message'),
            payload=d.get('payload', {}),
            enabled=d.get('enabled', True),
            schedule=d.get('schedule'),
            model=d.get('model', 'default'),
            description=d.get('description', ''),
        )
        return jsonify({'success': True, 'schedule': item})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/schedules/<sid>', methods=['PUT'])
def update_schedule(sid):
    try:
        item = scheduler.update(sid, request.json)
        if item:
            return jsonify({'success': True, 'schedule': item})
        return jsonify({'success': False, 'error': 'not found'}), 404
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/schedules/<sid>', methods=['DELETE'])
def delete_schedule(sid):
    scheduler.delete(sid)
    return jsonify({'success': True})


@app.route('/api/schedules/<sid>/toggle', methods=['POST'])
def toggle_schedule(sid):
    item = scheduler.toggle(sid)
    if item:
        return jsonify({'success': True, 'schedule': item})
    return jsonify({'success': False, 'error': 'not found'}), 404


@app.route('/api/schedules/<sid>/run', methods=['POST'])
def run_schedule_now(sid):
    """手動觸發排程"""
    ok = scheduler.run_now(sid)
    if ok:
        return jsonify({'success': True})
    return jsonify({'success': False, 'error': 'not found'}), 404


@app.route('/api/schedules/logs', methods=['GET'])
def get_schedule_logs():
    """取得排程執行紀錄"""
    sid = request.args.get('schedule_id', '')
    limit = request.args.get('limit', 50, type=int)
    logs = scheduler.get_logs(schedule_id=sid or None, limit=limit)
    return jsonify({'logs': logs})


@app.route('/api/schedules/logs', methods=['DELETE'])
def clear_schedule_logs():
    sid = request.args.get('schedule_id', '')
    scheduler.clear_logs(schedule_id=sid or None)
    return jsonify({'success': True})


# ==================== 權限沙盒 ====================

@app.route('/api/permissions', methods=['GET'])
def get_permissions():
    return jsonify(permission_mgr.get_all_permissions())


@app.route('/api/permissions/preset', methods=['POST'])
def set_permission_preset():
    try:
        preset = request.json.get('preset', 'full')
        if permission_mgr.set_preset(preset):
            return jsonify({'success': True})
        return jsonify({'success': False, 'error': 'unknown preset'}), 400
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/permissions/tool', methods=['POST'])
def set_tool_permission():
    try:
        d = request.json
        tool = d.get('tool', '')
        overrides = d.get('overrides', {})
        permission_mgr.set_tool_permission(tool, overrides)
        return jsonify({'success': True})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


@app.route('/api/permissions/tool/<tool_name>', methods=['DELETE'])
def remove_tool_override(tool_name):
    permission_mgr.remove_tool_override(tool_name)
    return jsonify({'success': True})


@app.route('/api/permissions/stats', methods=['GET'])
def get_permission_stats():
    return jsonify({'stats': permission_mgr.get_stats()})


# ==================== Skill 市集 ====================

@app.route('/api/skill-market', methods=['GET'])
def get_skill_market():
    """從 GitHub 取得社群技能列表"""
    import requests as req
    try:
        # 從 AutoTo 的 community-skills repo 取得 index
        url = 'https://raw.githubusercontent.com/anthropics/autoto-skills/main/index.json'
        r = req.get(url, timeout=10)
        if r.status_code == 200:
            return jsonify({'skills': r.json()})
        # fallback: 內建推薦技能
        return jsonify({'skills': _builtin_market_skills()})
    except Exception:
        return jsonify({'skills': _builtin_market_skills()})


def _builtin_market_skills():
    """內建推薦技能（離線可用）"""
    return [
        {
            'name': 'git_status',
            'description': 'Show git status of a repository',
            'command': 'cd {path} && git status --short',
            'params': [{'name': 'path', 'description': 'Repository path', 'required': True}],
            'author': 'AutoTo',
            'category': 'developer',
            'downloads': 0
        },
        {
            'name': 'docker_ps',
            'description': 'List running Docker containers',
            'command': 'docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"',
            'params': [],
            'author': 'AutoTo',
            'category': 'developer',
            'downloads': 0
        },
        {
            'name': 'disk_usage',
            'description': 'Show disk usage of a directory',
            'command': 'du -sh {path}/*',
            'params': [{'name': 'path', 'description': 'Directory path', 'required': True}],
            'author': 'AutoTo',
            'category': 'system',
            'downloads': 0
        },
        {
            'name': 'network_check',
            'description': 'Check network connectivity and DNS',
            'command': 'ping -c 3 {host}',
            'params': [{'name': 'host', 'description': 'Host to ping (e.g. google.com)', 'required': True}],
            'author': 'AutoTo',
            'category': 'network',
            'downloads': 0
        },
        {
            'name': 'find_large_files',
            'description': 'Find files larger than specified size',
            'command': 'find {path} -type f -size +{size} -exec ls -lh {} \\;',
            'params': [
                {'name': 'path', 'description': 'Search path', 'required': True},
                {'name': 'size', 'description': 'Min size (e.g. 100M)', 'required': True}
            ],
            'author': 'AutoTo',
            'category': 'system',
            'downloads': 0
        },
        {
            'name': 'screenshot_region',
            'description': 'Take a screenshot of a specific screen region (macOS)',
            'command': 'screencapture -R {x},{y},{w},{h} /tmp/region_screenshot.png && echo "Saved to /tmp/region_screenshot.png"',
            'params': [
                {'name': 'x', 'description': 'X coordinate'},
                {'name': 'y', 'description': 'Y coordinate'},
                {'name': 'w', 'description': 'Width'},
                {'name': 'h', 'description': 'Height'}
            ],
            'author': 'AutoTo',
            'category': 'utility',
            'downloads': 0
        },
    ]


@app.route('/api/skill-market/install', methods=['POST'])
def install_market_skill():
    """安裝市集技能"""
    try:
        d = request.json
        tool_data = {
            'name': d.get('name', ''),
            'description': d.get('description', ''),
            'command': d.get('command', ''),
            'params': d.get('params', [])
        }
        agent.tools.save_custom_tool(tool_data)
        return jsonify({'success': True})
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


# ==================== Skill 自動生成 ====================

@app.route('/api/skill-generate', methods=['POST'])
def generate_skill():
    """讓 AI 根據描述自動生成技能"""
    try:
        d = request.json
        description = d.get('description', '').strip()
        if not description:
            return jsonify({'success': False, 'error': '請描述你想要的技能'}), 400

        prompt = f"""你是一個技能生成器。用戶描述了一個他想要的自動化技能，請生成對應的 shell 指令。

用戶描述：{description}

請回傳一個 JSON 物件（不要 markdown 包裹），格式如下：
{{
  "name": "技能名稱（英文，snake_case）",
  "description": "技能描述（繁體中文）",
  "command": "shell 指令模板，參數用 {{param_name}} 表示",
  "params": [
    {{"name": "param_name", "description": "參數描述", "required": true}}
  ]
}}

規則：
- 指令必須是合法的 shell 指令
- 不能包含 rm -rf、format、mkfs 等危險操作
- 參數用 {{name}} 格式
- name 用英文 snake_case
- description 用繁體中文
- 只回傳 JSON，不要其他文字"""

        # 用 agent 的 LLM 來生成
        provider = config_mgr.get('provider', 'groq')
        api_key = config_mgr.get('apiKey', '')
        model = config_mgr.get('model', '')

        result = agent._call_llm_no_tools(provider, api_key, model, [
            {'role': 'system', 'content': '你是一個 JSON 生成器，只回傳有效的 JSON。'},
            {'role': 'user', 'content': prompt}
        ])

        text = result.get('content', '').strip()
        # 嘗試從回覆中提取 JSON
        if '```' in text:
            match = re.search(r'```(?:json)?\s*(\{.*?\})\s*```', text, re.S)
            if match:
                text = match.group(1)

        tool_data = json.loads(text)

        # 驗證必要欄位
        if not tool_data.get('name') or not tool_data.get('command'):
            return jsonify({'success': False, 'error': 'AI 生成的技能缺少必要欄位'}), 400

        return jsonify({'success': True, 'tool': tool_data})
    except json.JSONDecodeError:
        return jsonify({'success': False, 'error': 'AI 回覆格式錯誤，請重試'}), 500
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


# ==================== 版本更新 ====================
APP_VERSION = '1.0.0'
GITHUB_REPO = ''  # TODO: 填入你的 GitHub repo（格式：owner/repo）

@app.route('/api/version', methods=['GET'])
def get_version():
    """取得當前版本和 git 資訊"""
    import subprocess as sp
    info = {'version': APP_VERSION}
    try:
        root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
        res = sp.run(['git', 'rev-parse', '--short', 'HEAD'],
            capture_output=True, text=True, timeout=5, cwd=root)
        if res.returncode == 0:
            info['commit'] = res.stdout.strip()
        res2 = sp.run(['git', 'remote', 'get-url', 'origin'],
            capture_output=True, text=True, timeout=5, cwd=root)
        if res2.returncode == 0:
            info['remote'] = res2.stdout.strip()
    except:
        pass
    return jsonify(info)


@app.route('/api/update/check', methods=['GET'])
def check_update():
    """檢查 GitHub 是否有新版本"""
    import subprocess as sp
    project_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    try:
        # git fetch
        sp.run(['git', 'fetch', 'origin'], capture_output=True, timeout=30, cwd=project_root)
        # 比較 local 和 remote
        local = sp.run(['git', 'rev-parse', 'HEAD'], capture_output=True, text=True, timeout=5, cwd=project_root)
        remote = sp.run(['git', 'rev-parse', 'origin/main'], capture_output=True, text=True, timeout=5, cwd=project_root)
        if local.returncode != 0 or remote.returncode != 0:
            # 嘗試 master branch
            remote = sp.run(['git', 'rev-parse', 'origin/master'], capture_output=True, text=True, timeout=5, cwd=project_root)
        local_hash = local.stdout.strip()
        remote_hash = remote.stdout.strip()
        has_update = local_hash != remote_hash
        result = {
            'hasUpdate': has_update,
            'currentCommit': local_hash[:7],
            'latestCommit': remote_hash[:7],
            'currentVersion': APP_VERSION,
        }
        if has_update:
            # 取得更新日誌
            log = sp.run(['git', 'log', f'{local_hash}..{remote_hash}', '--oneline', '--max-count=10'],
                capture_output=True, text=True, timeout=10, cwd=project_root)
            result['changelog'] = log.stdout.strip() if log.returncode == 0 else ''
            # 計算有幾個 commit 落後
            count = sp.run(['git', 'rev-list', '--count', f'{local_hash}..{remote_hash}'],
                capture_output=True, text=True, timeout=5, cwd=project_root)
            result['behindCount'] = int(count.stdout.strip()) if count.returncode == 0 else 0
        return jsonify(result)
    except Exception as e:
        return jsonify({'error': str(e), 'hasUpdate': False}), 500


@app.route('/api/update/apply', methods=['POST'])
def apply_update():
    """執行 git pull 更新"""
    import subprocess as sp
    project_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    try:
        # git pull
        res = sp.run(['git', 'pull', 'origin'], capture_output=True, text=True, timeout=60, cwd=project_root)
        if res.returncode != 0:
            return jsonify({'success': False, 'error': res.stderr or 'git pull failed'}), 500
        # pip install requirements if changed
        req_file = os.path.join(project_root, 'backend', 'requirements.txt')
        if os.path.exists(req_file):
            sp.run(['pip3', 'install', '-r', req_file, '-q'], capture_output=True, timeout=120, cwd=project_root)
        return jsonify({
            'success': True,
            'output': res.stdout.strip(),
            'message': '更新完成，請重新啟動後端服務以套用變更。'
        })
    except Exception as e:
        return jsonify({'success': False, 'error': str(e)}), 500


# ==================== 日誌系統 ====================
import collections
_log_buffer = collections.deque(maxlen=200)
_original_print = print

def _capture_print(*args, **kwargs):
    """攔截 print 輸出，同時存到日誌 buffer"""
    text = ' '.join(str(a) for a in args)
    _log_buffer.append({
        'time': datetime.now().strftime('%H:%M:%S'),
        'text': text
    })
    _original_print(*args, **kwargs)

# 替換全域 print
import builtins
builtins.print = _capture_print


@app.route('/api/logs', methods=['GET'])
def get_logs():
    """取得系統日誌"""
    since = request.args.get('since', 0, type=int)
    logs = list(_log_buffer)
    if since > 0 and since < len(logs):
        logs = logs[since:]
    return jsonify({'logs': logs, 'total': len(_log_buffer)})


@app.route('/api/logs/clear', methods=['POST'])
def clear_logs_api():
    _log_buffer.clear()
    return jsonify({'success': True})


# ==================== 系統診斷 ====================

@app.route('/api/diagnostics', methods=['GET'])
def run_diagnostics():
    """一鍵系統診斷"""
    import subprocess, platform, shutil, socket
    results = []

    # 1. Python 版本
    py_ver = platform.python_version()
    py_ok = tuple(int(x) for x in py_ver.split('.')[:2]) >= (3, 8)
    results.append({
        'name': 'Python 版本',
        'status': 'ok' if py_ok else 'error',
        'detail': f'Python {py_ver}' + ('' if py_ok else '（需要 3.8+）'),
        'category': 'system'
    })

    # 2. 必要 Python 套件
    required_pkgs = {
        'flask': 'Flask（後端伺服器）',
        'flask_cors': 'Flask-CORS（跨域支援）',
        'requests': 'Requests（API 呼叫）',
    }
    optional_pkgs = {
        'telegram': 'python-telegram-bot（Telegram 機器人）',
        'discord': 'discord.py（Discord 機器人）',
        'linebot': 'line-bot-sdk（LINE 機器人）',
    }
    for mod, label in required_pkgs.items():
        try:
            __import__(mod)
            results.append({'name': label, 'status': 'ok', 'detail': '已安裝', 'category': 'package'})
        except ImportError:
            results.append({'name': label, 'status': 'error', 'detail': '未安裝（必要）', 'fix': f'pip3 install {mod.replace("_", "-")}', 'category': 'package'})

    for mod, label in optional_pkgs.items():
        # 只在對應平台啟用時才檢查
        channel_map = {'telegram': 'telegram', 'discord': 'discord', 'linebot': 'line'}
        ch_key = channel_map.get(mod, '')
        ch_cfg = config_mgr.get(f'channels.{ch_key}', {}) if ch_key else {}
        if not ch_cfg.get('enabled', False):
            continue
        try:
            __import__(mod)
            results.append({'name': label, 'status': 'ok', 'detail': '已安裝', 'category': 'package'})
        except ImportError:
            results.append({'name': label, 'status': 'warn', 'detail': '已啟用但未安裝，該平台可能無法運作', 'fix': f'pip3 install {mod.replace("_", "-")}', 'category': 'package'})

    # 3. API Key 設定
    api_key = config_mgr.get('apiKey', '')
    if api_key and not api_key.startswith('***'):
        results.append({'name': 'API Key', 'status': 'ok', 'detail': '已設定', 'category': 'config'})
    else:
        results.append({'name': 'API Key', 'status': 'error', 'detail': '未設定，請到設定頁面填入', 'category': 'config'})

    # 4. API 連線測試
    provider = config_mgr.get('provider', 'groq')
    api_urls = {
        'groq': 'https://api.groq.com',
        'openai': 'https://api.openai.com',
        'anthropic': 'https://api.anthropic.com',
        'deepseek': 'https://api.deepseek.com',
        'openrouter': 'https://openrouter.ai',
    }
    test_url = api_urls.get(provider, '')
    if test_url:
        try:
            import requests as req
            r = req.get(test_url, timeout=10)
            results.append({'name': f'API 連線（{provider}）', 'status': 'ok', 'detail': f'{test_url} 可連線', 'category': 'network'})
        except Exception as e:
            results.append({'name': f'API 連線（{provider}）', 'status': 'error', 'detail': f'無法連線: {str(e)[:80]}', 'category': 'network'})

    # 5. Telegram 連線
    tg_cfg = config_mgr.get('channels.telegram', {})
    if tg_cfg.get('enabled'):
        tg_token = tg_cfg.get('botToken', '')
        if tg_token:
            try:
                # 強制 IPv4
                import requests as req
                r = req.get(f'https://api.telegram.org/bot{tg_token}/getMe', timeout=15)
                if r.status_code == 200:
                    bot_info = r.json().get('result', {})
                    results.append({'name': 'Telegram Bot', 'status': 'ok',
                        'detail': f'已連線 @{bot_info.get("username", "?")}', 'category': 'channel'})
                elif r.status_code == 401:
                    results.append({'name': 'Telegram Bot', 'status': 'error',
                        'detail': 'Token 無效，請重新從 @BotFather 取得', 'category': 'channel'})
                else:
                    results.append({'name': 'Telegram Bot', 'status': 'error',
                        'detail': f'API 回傳 {r.status_code}', 'category': 'channel'})
            except Exception as e:
                results.append({'name': 'Telegram Bot', 'status': 'error',
                    'detail': f'連線失敗: {str(e)[:60]}（可能是 IPv6 問題）',
                    'fix': '系統已自動使用 IPv4，若仍失敗請檢查網路', 'category': 'channel'})
        else:
            results.append({'name': 'Telegram Bot', 'status': 'warn',
                'detail': '已啟用但未填入 Token', 'category': 'channel'})

    # 6. Discord 連線
    dc_cfg = config_mgr.get('channels.discord', {})
    if dc_cfg.get('enabled'):
        if dc_cfg.get('token'):
            results.append({'name': 'Discord Bot', 'status': 'ok', 'detail': 'Token 已設定', 'category': 'channel'})
        else:
            results.append({'name': 'Discord Bot', 'status': 'warn', 'detail': '已啟用但未填入 Token', 'category': 'channel'})

    # 7. macOS 輔助使用權限（type_text, key_press 需要）
    if platform.system() == 'Darwin':
        try:
            r = subprocess.run(['osascript', '-e', 'tell application "System Events" to return name of first process'],
                capture_output=True, text=True, timeout=5)
            if r.returncode == 0:
                results.append({'name': 'macOS 輔助使用權限', 'status': 'ok',
                    'detail': '已授權（模擬打字、鍵盤快捷鍵可用）', 'category': 'permission'})
            else:
                results.append({'name': 'macOS 輔助使用權限', 'status': 'error',
                    'detail': '未授權，模擬打字和鍵盤快捷鍵將無法使用',
                    'fix': '系統設定 → 隱私與安全性 → 輔助使用 → 加入終端機/Python', 'category': 'permission'})
        except:
            results.append({'name': 'macOS 輔助使用權限', 'status': 'warn',
                'detail': '無法檢測', 'category': 'permission'})

    # 8. 螢幕錄製權限（screenshot 需要）
    if platform.system() == 'Darwin':
        try:
            test_path = '/tmp/_autoto_diag_screenshot.png'
            r = subprocess.run(['screencapture', '-x', test_path], capture_output=True, timeout=5)
            if os.path.exists(test_path):
                sz = os.path.getsize(test_path)
                os.remove(test_path)
                if sz > 100:
                    results.append({'name': 'macOS 螢幕錄製權限', 'status': 'ok',
                        'detail': '已授權（截圖功能可用）', 'category': 'permission'})
                else:
                    results.append({'name': 'macOS 螢幕錄製權限', 'status': 'error',
                        'detail': '未授權，截圖功能將無法使用',
                        'fix': '系統設定 → 隱私與安全性 → 螢幕錄製 → 加入終端機/Python', 'category': 'permission'})
            else:
                results.append({'name': 'macOS 螢幕錄製權限', 'status': 'error',
                    'detail': '截圖失敗',
                    'fix': '系統設定 → 隱私與安全性 → 螢幕錄製', 'category': 'permission'})
        except:
            results.append({'name': 'macOS 螢幕錄製權限', 'status': 'warn', 'detail': '無法檢測', 'category': 'permission'})

    # 9. 磁碟空間
    try:
        st = os.statvfs('/')
        free_gb = (st.f_bavail * st.f_frsize) / (1024**3)
        if free_gb > 1:
            results.append({'name': '磁碟空間', 'status': 'ok', 'detail': f'剩餘 {free_gb:.1f} GB', 'category': 'system'})
        else:
            results.append({'name': '磁碟空間', 'status': 'warn', 'detail': f'剩餘 {free_gb:.1f} GB（偏低）', 'category': 'system'})
    except:
        pass

    # 10. Channel gateway 狀態
    active = gateway.get_active_channels()
    gw_status = gateway.get_status()
    for ch_name, st in gw_status.items():
        if st.get('enabled') and not st.get('running'):
            results.append({'name': f'{ch_name} 頻道', 'status': 'error',
                'detail': '已啟用但未在運行（可能啟動失敗）',
                'fix': '檢查日誌或重啟後端', 'category': 'channel'})

    return jsonify({'results': results})


@app.route('/', defaults={'asset_path': 'index.html'})
@app.route('/<path:asset_path>')
def serve_web_ui(asset_path):
    if asset_path.startswith('api/'):
        return jsonify({'error': 'not found'}), 404

    web_ui_dir = get_web_ui_dir()
    if web_ui_dir is None:
        return jsonify({
            'status': 'running',
            'service': 'AutoTo Backend',
            'message': 'Web UI not found. Please open the renderer directory or install the packaged app.'
        }), 404

    if asset_path == 'index.html':
        return send_file(str(web_ui_dir / 'index.html'))

    asset_file = web_ui_dir / asset_path
    if asset_file.exists() and asset_file.is_file():
        return send_from_directory(str(web_ui_dir), asset_path)

    return jsonify({'error': 'not found'}), 404


# ==================== 啟動 ====================

def main():
    parser = argparse.ArgumentParser(description='AutoTo Backend')
    parser.add_argument('--port', type=int, default=int(os.environ.get('AUTOTO_PORT', 5678)))
    parser.add_argument('--host', default='127.0.0.1')
    args = parser.parse_args()

    print(f'🤖 AutoTo Backend 啟動中...')
    print(f'   端口: {args.port}')

    # 啟動 channel gateways（在背景執行緒）
    gateway_thread = threading.Thread(target=gateway.start_all, daemon=True)
    gateway_thread.start()

    # 啟動排程器
    scheduler.start()

    # 注入 manager 到 __main__，讓 tools.py 的工具函式可以存取
    import __main__
    __main__._camera_mgr = camera_mgr
    __main__._smarthome_mgr = smarthome_mgr

    # 啟動攝影機串流
    camera_mgr.start_all_enabled()

    # 連線智慧家電平台
    smarthome_mgr.connect_all_enabled()

    print(f'✅ AutoTo Backend 已就緒: http://{args.host}:{args.port}')
    app.run(host=args.host, port=args.port, debug=False)


if __name__ == '__main__':
    main()
