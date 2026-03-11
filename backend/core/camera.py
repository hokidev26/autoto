#!/usr/bin/env python3
"""
攝影機管理器
支援 RTSP/IP 攝影機串流 + 本機 Webcam
透過 MJPEG proxy 讓瀏覽器直接顯示即時畫面
"""

import json
import os
import platform
import subprocess
import threading
import time
import uuid
from datetime import datetime
from pathlib import Path

IS_MAC = platform.system() == 'Darwin'

SNAPSHOT_DIR = Path.home() / '.autoto' / 'snapshots'
SNAPSHOT_DIR.mkdir(parents=True, exist_ok=True)


class CameraManager:
    def __init__(self, config_mgr):
        self.config = config_mgr
        self._lock = threading.Lock()
        self._streams = {}  # cam_id -> StreamWorker

    # ==================== 攝影機 CRUD ====================

    def get_cameras(self):
        """取得所有攝影機設定"""
        return self.config.get('cameras', [])

    def add_camera(self, data):
        cam = {
            'id': str(uuid.uuid4())[:8],
            'name': data.get('name', '未命名攝影機'),
            'type': data.get('type', 'rtsp'),  # rtsp | webcam
            'url': data.get('url', ''),          # RTSP URL（webcam 留空）
            'device': data.get('device', 0),     # webcam device index
            'enabled': data.get('enabled', True),
            'record': data.get('record', False),
            'created': datetime.now().isoformat(),
        }
        cameras = self.config.get('cameras', [])
        cameras.append(cam)
        self.config.update({'cameras': cameras})
        return cam

    def update_camera(self, cam_id, data):
        cameras = self.config.get('cameras', [])
        for cam in cameras:
            if cam['id'] == cam_id:
                for k in ('name', 'url', 'type', 'device', 'enabled', 'record'):
                    if k in data:
                        cam[k] = data[k]
                self.config.update({'cameras': cameras})
                # 如果正在串流，重啟
                if cam_id in self._streams:
                    self.stop_stream(cam_id)
                    if cam.get('enabled'):
                        self.start_stream(cam_id)
                return cam
        return None

    def delete_camera(self, cam_id):
        self.stop_stream(cam_id)
        cameras = self.config.get('cameras', [])
        cameras = [c for c in cameras if c['id'] != cam_id]
        self.config.update({'cameras': cameras})

    def get_camera(self, cam_id):
        for cam in self.config.get('cameras', []):
            if cam['id'] == cam_id:
                return cam
        return None

    # ==================== 串流控制 ====================

    def start_stream(self, cam_id):
        """啟動攝影機串流（背景執行緒）"""
        cam = self.get_camera(cam_id)
        if not cam:
            return False, '攝影機不存在'
        if cam_id in self._streams:
            return True, '已在串流中'

        worker = StreamWorker(cam)
        worker.start()
        with self._lock:
            self._streams[cam_id] = worker
        return True, '串流已啟動'

    def stop_stream(self, cam_id):
        with self._lock:
            worker = self._streams.pop(cam_id, None)
        if worker:
            worker.stop()
            return True
        return False

    def get_stream_status(self):
        """取得所有串流狀態"""
        cameras = self.config.get('cameras', [])
        result = []
        for cam in cameras:
            cid = cam['id']
            worker = self._streams.get(cid)
            result.append({
                **cam,
                'streaming': worker is not None and worker.is_alive(),
                'fps': worker.fps if worker else 0,
            })
        return result

    def get_frame(self, cam_id):
        """取得最新一幀（JPEG bytes），供 MJPEG 串流用"""
        worker = self._streams.get(cam_id)
        if worker:
            return worker.get_frame()
        return None

    def snapshot(self, cam_id):
        """擷取一張快照並存檔"""
        frame = self.get_frame(cam_id)
        if frame:
            fname = f'{cam_id}_{int(time.time())}.jpg'
            fpath = SNAPSHOT_DIR / fname
            with open(fpath, 'wb') as f:
                f.write(frame)
            return str(fpath)

        # 沒有串流中，嘗試直接擷取
        cam = self.get_camera(cam_id)
        if not cam:
            return None
        return self._snapshot_direct(cam)

    def _snapshot_direct(self, cam):
        """用 ffmpeg 直接擷取一幀"""
        fname = f'{cam["id"]}_{int(time.time())}.jpg'
        fpath = SNAPSHOT_DIR / fname
        if cam['type'] == 'rtsp':
            cmd = [
                'ffmpeg', '-y', '-rtsp_transport', 'tcp',
                '-i', cam['url'],
                '-frames:v', '1', '-q:v', '2',
                str(fpath)
            ]
        else:
            dev_idx = str(cam.get('device', 0))
            if IS_MAC:
                cmd = [
                    'ffmpeg', '-y',
                    '-f', 'avfoundation',
                    '-i', f'{dev_idx}:none',
                    '-frames:v', '1', '-q:v', '2',
                    str(fpath)
                ]
            else:
                cmd = [
                    'ffmpeg', '-y',
                    '-f', 'v4l2',
                    '-i', f'/dev/video{dev_idx}',
                    '-frames:v', '1', '-q:v', '2',
                    str(fpath)
                ]
        try:
            subprocess.run(cmd, capture_output=True, timeout=15)
            if fpath.exists():
                return str(fpath)
        except Exception:
            pass
        return None

    def start_all_enabled(self):
        """啟動所有已啟用的攝影機"""
        for cam in self.config.get('cameras', []):
            if cam.get('enabled'):
                self.start_stream(cam['id'])

    # ==================== AI 畫面分析 ====================

    def analyze_frame(self, cam_id, prompt='', agent=None):
        """
        截取一幀，送給視覺模型分析
        回傳 AI 的分析結果文字
        """
        import base64

        frame = self.get_frame(cam_id)
        if not frame:
            # 嘗試直接截圖
            cam = self.get_camera(cam_id)
            if cam:
                path = self._snapshot_direct(cam)
                if path:
                    with open(path, 'rb') as f:
                        frame = f.read()
        if not frame:
            return '無法取得攝影機畫面，請確認攝影機是否正常運作。'

        b64 = base64.b64encode(frame).decode('utf-8')
        cam = self.get_camera(cam_id)
        cam_name = cam['name'] if cam else cam_id

        if not prompt:
            prompt = '請描述這個攝影機畫面中你看到的內容。如果有任何異常或值得注意的事情，請特別指出。'

        # 用視覺模型分析
        return self._call_vision_llm(b64, prompt, cam_name, agent)

    def _call_vision_llm(self, image_b64, prompt, cam_name, agent=None):
        """呼叫支援視覺的 LLM 分析圖片"""
        import requests

        provider = self.config.get('provider', 'groq')
        api_key = self.config.get('apiKey', '')
        model = self.config.get('model', '')

        if not api_key:
            return '⚠️ 請先設定 API Key'

        system_msg = f'你是一個智慧監控助理。你正在查看「{cam_name}」攝影機的即時畫面。請用繁體中文回覆。'

        messages = [
            {'role': 'system', 'content': system_msg},
            {'role': 'user', 'content': [
                {'type': 'text', 'text': prompt},
                {'type': 'image_url', 'image_url': {
                    'url': f'data:image/jpeg;base64,{image_b64}'
                }}
            ]}
        ]

        # 根據 provider 選擇支援視覺的模型
        vision_models = {
            'openai': 'gpt-4o',
            'anthropic': 'claude-3-5-sonnet-20241022',
            'groq': 'llama-3.2-90b-vision-preview',
            'openrouter': 'openai/gpt-4o',
            'deepseek': 'deepseek-chat',
            'gemini': 'gemini-1.5-flash',
            'ollama': 'llava',
        }

        if provider in ('anthropic', 'claude'):
            return self._call_anthropic_vision(api_key, image_b64, prompt, system_msg)
        elif provider == 'gemini':
            return self._call_gemini_vision(api_key, image_b64, prompt)

        # OpenAI 相容 API（groq, openai, openrouter, deepseek 等）
        urls = {
            'groq': 'https://api.groq.com/openai/v1/chat/completions',
            'openai': 'https://api.openai.com/v1/chat/completions',
            'openrouter': 'https://openrouter.ai/api/v1/chat/completions',
            'deepseek': 'https://api.deepseek.com/v1/chat/completions',
            'ollama': 'http://127.0.0.1:11434/v1/chat/completions',
        }
        url = urls.get(provider, urls.get('openai'))
        used_model = vision_models.get(provider, model or 'gpt-4o')

        headers = {'Content-Type': 'application/json'}
        if provider != 'ollama' and api_key:
            headers['Authorization'] = f'Bearer {api_key}'

        body = {
            'model': used_model,
            'messages': messages,
            'max_tokens': 1000,
            'temperature': 0.3,
        }

        try:
            res = requests.post(url, headers=headers, json=body, timeout=60)
            if res.status_code != 200:
                return f'⚠️ 視覺分析失敗 ({res.status_code}): {res.text[:200]}'
            return res.json()['choices'][0]['message']['content']
        except Exception as e:
            return f'⚠️ 視覺分析錯誤: {e}'

    def _call_anthropic_vision(self, api_key, image_b64, prompt, system_msg):
        import requests
        try:
            res = requests.post('https://api.anthropic.com/v1/messages',
                headers={'x-api-key': api_key, 'anthropic-version': '2023-06-01',
                         'Content-Type': 'application/json'},
                json={
                    'model': 'claude-3-5-sonnet-20241022',
                    'max_tokens': 1000,
                    'system': system_msg,
                    'messages': [{'role': 'user', 'content': [
                        {'type': 'text', 'text': prompt},
                        {'type': 'image', 'source': {
                            'type': 'base64', 'media_type': 'image/jpeg',
                            'data': image_b64
                        }}
                    ]}]
                }, timeout=60)
            res.raise_for_status()
            return res.json()['content'][0]['text']
        except Exception as e:
            return f'⚠️ Claude 視覺分析錯誤: {e}'

    def _call_gemini_vision(self, api_key, image_b64, prompt):
        import requests
        try:
            res = requests.post(
                f'https://generativelanguage.googleapis.com/v1/models/gemini-1.5-flash:generateContent?key={api_key}',
                json={'contents': [{'parts': [
                    {'text': prompt},
                    {'inline_data': {'mime_type': 'image/jpeg', 'data': image_b64}}
                ]}]}, timeout=60)
            res.raise_for_status()
            return res.json()['candidates'][0]['content']['parts'][0]['text']
        except Exception as e:
            return f'⚠️ Gemini 視覺分析錯誤: {e}'

    # ==================== 持續監控（自動巡檢） ====================

    def start_watch(self, cam_id, interval=60, alert_prompt='', notify_callback=None):
        """
        啟動持續監控：每隔 interval 秒截圖分析一次
        如果 AI 判斷有異常，呼叫 notify_callback 通知用戶
        """
        cam = self.get_camera(cam_id)
        if not cam:
            return False, '攝影機不存在'
        key = f'watch_{cam_id}'
        if key in self._streams:
            return True, '已在監控中'

        watcher = CameraWatcher(
            camera_mgr=self,
            cam_id=cam_id,
            interval=interval,
            alert_prompt=alert_prompt,
            notify_callback=notify_callback,
        )
        watcher.start()
        with self._lock:
            self._streams[key] = watcher
        return True, '已啟動 AI 監控'

    def stop_watch(self, cam_id):
        key = f'watch_{cam_id}'
        with self._lock:
            watcher = self._streams.pop(key, None)
        if watcher:
            watcher.stop()
            return True
        return False

    def get_watch_status(self, cam_id):
        key = f'watch_{cam_id}'
        watcher = self._streams.get(key)
        if watcher and watcher.is_alive():
            return {
                'watching': True,
                'interval': watcher.interval,
                'last_check': watcher.last_check,
                'alert_count': watcher.alert_count,
                'last_alert': watcher.last_alert,
            }
        return {'watching': False}

    def get_watch_logs(self, cam_id, limit=20):
        key = f'watch_{cam_id}'
        watcher = self._streams.get(key)
        if watcher:
            return watcher.logs[-limit:]
        return []


class CameraWatcher(threading.Thread):
    """
    持續監控執行緒：定期截圖 → AI 分析 → 有異常就通知
    """

    def __init__(self, camera_mgr, cam_id, interval=60,
                 alert_prompt='', notify_callback=None):
        super().__init__(daemon=True)
        self.camera_mgr = camera_mgr
        self.cam_id = cam_id
        self.interval = interval
        self.notify_callback = notify_callback
        self._running = False
        self.last_check = None
        self.alert_count = 0
        self.last_alert = None
        self.logs = []

        if not alert_prompt:
            self.alert_prompt = (
                '你是一個智慧監控助理。請分析這個攝影機畫面，判斷是否有以下異常情況：\n'
                '1. 有陌生人或可疑人物出現\n'
                '2. 有異常動作（闖入、打鬥、跌倒）\n'
                '3. 有火災、煙霧、水災跡象\n'
                '4. 有動物闖入\n'
                '5. 其他任何不尋常的狀況\n\n'
                '如果一切正常，只回覆「正常」兩個字。\n'
                '如果有異常，請詳細描述你看到的狀況，開頭加上「⚠️ 異常」。'
            )
        else:
            self.alert_prompt = alert_prompt

    def run(self):
        self._running = True
        cam = self.camera_mgr.get_camera(self.cam_id)
        cam_name = cam['name'] if cam else self.cam_id
        print(f'  👁️ AI 監控已啟動: {cam_name}（每 {self.interval} 秒）')

        while self._running:
            try:
                result = self.camera_mgr.analyze_frame(
                    self.cam_id, self.alert_prompt
                )
                self.last_check = datetime.now().isoformat()

                log_entry = {
                    'time': self.last_check,
                    'result': result[:300],
                    'is_alert': False,
                }

                # 判斷是否有異常
                is_alert = result and not result.strip().startswith('正常') and '異常' in result
                if is_alert:
                    self.alert_count += 1
                    self.last_alert = self.last_check
                    log_entry['is_alert'] = True
                    print(f'  🚨 {cam_name} 偵測到異常: {result[:100]}')

                    # 通知用戶
                    if self.notify_callback:
                        try:
                            self.notify_callback(cam_name, result)
                        except Exception as e:
                            print(f'  ⚠️ 通知失敗: {e}')

                self.logs.append(log_entry)
                if len(self.logs) > 100:
                    self.logs = self.logs[-100:]

            except Exception as e:
                print(f'  ⚠️ AI 監控 {cam_name} 分析失敗: {e}')

            # 等待下次檢查
            for _ in range(self.interval):
                if not self._running:
                    break
                time.sleep(1)

    def stop(self):
        self._running = False

    def is_alive(self):
        return self._running and super().is_alive()


class StreamWorker(threading.Thread):
    """
    背景執行緒：用 ffmpeg 把 RTSP/Webcam 轉成 MJPEG，
    持續讀取最新幀供 HTTP 串流使用
    """

    def __init__(self, cam_config):
        super().__init__(daemon=True)
        self.cam = cam_config
        self._frame = None
        self._frame_lock = threading.Lock()
        self._running = False
        self._process = None
        self.fps = 0

    def run(self):
        self._running = True
        cam = self.cam

        if cam['type'] == 'webcam':
            dev_idx = str(cam.get('device', 0))
            if IS_MAC:
                input_args = [
                    '-f', 'avfoundation',
                    '-framerate', '15',
                    '-video_size', '1280x720',
                    '-i', f'{dev_idx}:none',
                ]
            else:
                input_args = [
                    '-f', 'v4l2',
                    '-framerate', '15',
                    '-i', f'/dev/video{dev_idx}',
                ]
        else:
            input_args = [
                '-rtsp_transport', 'tcp',
                '-i', cam['url'],
            ]

        cmd = [
            'ffmpeg', '-hide_banner', '-loglevel', 'error',
            *input_args,
            '-f', 'mjpeg',
            '-q:v', '5',
            '-r', '10',
            '-an',
            'pipe:1'
        ]

        try:
            self._process = subprocess.Popen(
                cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE
            )
            buf = b''
            frame_count = 0
            last_fps_time = time.time()

            while self._running and self._process.poll() is None:
                chunk = self._process.stdout.read(4096)
                if not chunk:
                    break
                buf += chunk

                # MJPEG: 每幀以 FFD8 開頭、FFD9 結尾
                while True:
                    start = buf.find(b'\xff\xd8')
                    end = buf.find(b'\xff\xd9', start + 2) if start >= 0 else -1
                    if start < 0 or end < 0:
                        break
                    frame = buf[start:end + 2]
                    buf = buf[end + 2:]
                    with self._frame_lock:
                        self._frame = frame
                    frame_count += 1

                now = time.time()
                if now - last_fps_time >= 1.0:
                    self.fps = frame_count
                    frame_count = 0
                    last_fps_time = now

        except Exception as e:
            print(f'  ❌ 攝影機 {cam.get("name")} 串流錯誤: {e}')
        finally:
            # 印出 ffmpeg stderr 方便除錯
            if self._process and self._process.stderr:
                try:
                    err = self._process.stderr.read().decode(errors='ignore').strip()
                    if err:
                        print(f'  ⚠️ 攝影機 {cam.get("name")} ffmpeg: {err[:200]}')
                except Exception:
                    pass
            self._cleanup()

    def get_frame(self):
        with self._frame_lock:
            return self._frame

    def stop(self):
        self._running = False
        self._cleanup()

    def _cleanup(self):
        if self._process:
            try:
                self._process.terminate()
                self._process.wait(timeout=5)
            except Exception:
                try:
                    self._process.kill()
                except Exception:
                    pass
            self._process = None

    def is_alive(self):
        return self._running and super().is_alive()
