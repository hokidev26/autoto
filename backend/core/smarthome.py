#!/usr/bin/env python3
"""
智慧家電控制器 — 通用抽象層
支援多平台 adapter：Home Assistant、Tuya、MQTT、HTTP 等
每個 adapter 實作 connect / get_devices / control / get_state
"""

import json
import threading
import time
import uuid
from datetime import datetime
from pathlib import Path

try:
    import requests as _req
except ImportError:
    _req = None


class SmartHomeManager:
    """統一管理所有智慧家電平台"""

    def __init__(self, config_mgr):
        self.config = config_mgr
        self._lock = threading.Lock()
        self._adapters = {}  # platform_id -> BaseAdapter instance
        self._device_cache = {}  # device_id -> device dict
        self._cache_time = 0

    # ==================== 平台管理 ====================

    def get_platforms(self):
        return self.config.get('smarthome.platforms', [])

    def add_platform(self, data):
        plat = {
            'id': str(uuid.uuid4())[:8],
            'name': data.get('name', ''),
            'type': data.get('type', 'homeassistant'),  # homeassistant | tuya | mqtt | http
            'host': data.get('host', ''),
            'token': data.get('token', ''),
            'extra': data.get('extra', {}),
            'enabled': data.get('enabled', True),
            'created': datetime.now().isoformat(),
        }
        platforms = self.config.get('smarthome.platforms', [])
        platforms.append(plat)
        self.config.update({'smarthome': {'platforms': platforms}})
        return plat

    def update_platform(self, plat_id, data):
        platforms = self.config.get('smarthome.platforms', [])
        for p in platforms:
            if p['id'] == plat_id:
                for k in ('name', 'host', 'token', 'type', 'enabled', 'extra'):
                    if k in data:
                        p[k] = data[k]
                self.config.update({'smarthome': {'platforms': platforms}})
                # 重新連線
                self._disconnect(plat_id)
                if p.get('enabled'):
                    self._connect(p)
                return p
        return None

    def delete_platform(self, plat_id):
        self._disconnect(plat_id)
        platforms = self.config.get('smarthome.platforms', [])
        platforms = [p for p in platforms if p['id'] != plat_id]
        self.config.update({'smarthome': {'platforms': platforms}})

    # ==================== 裝置操作 ====================

    def get_devices(self, force_refresh=False):
        """取得所有平台的裝置列表"""
        now = time.time()
        if not force_refresh and self._device_cache and now - self._cache_time < 30:
            return list(self._device_cache.values())

        devices = []
        for plat in self.get_platforms():
            if not plat.get('enabled'):
                continue
            adapter = self._get_adapter(plat)
            if adapter:
                try:
                    devs = adapter.get_devices()
                    for d in devs:
                        d['platform_id'] = plat['id']
                        d['platform_name'] = plat['name']
                        devices.append(d)
                except Exception as e:
                    print(f'  ⚠️ 取得 {plat["name"]} 裝置失敗: {e}')

        with self._lock:
            self._device_cache = {d['id']: d for d in devices}
            self._cache_time = now
        return devices

    def control_device(self, device_id, action, params=None):
        """控制裝置（開/關/調整）"""
        device = self._find_device(device_id)
        if not device:
            return {'success': False, 'error': f'找不到裝置 {device_id}'}

        plat = self._find_platform(device.get('platform_id'))
        if not plat:
            return {'success': False, 'error': '平台不存在'}

        adapter = self._get_adapter(plat)
        if not adapter:
            return {'success': False, 'error': '平台未連線'}

        try:
            result = adapter.control(device_id, action, params or {})
            # 更新快取
            self._invalidate_cache()
            return {'success': True, 'result': result}
        except Exception as e:
            return {'success': False, 'error': str(e)}

    def get_device_state(self, device_id):
        """取得單一裝置狀態"""
        device = self._find_device(device_id)
        if not device:
            return None
        plat = self._find_platform(device.get('platform_id'))
        if not plat:
            return None
        adapter = self._get_adapter(plat)
        if adapter:
            try:
                return adapter.get_state(device_id)
            except Exception:
                pass
        return device

    def find_device_by_name(self, name):
        """用名稱模糊搜尋裝置（給 agent 用）"""
        name_lower = name.lower()
        devices = self.get_devices()
        # 精確匹配
        for d in devices:
            if d.get('name', '').lower() == name_lower:
                return d
        # 模糊匹配
        for d in devices:
            if name_lower in d.get('name', '').lower():
                return d
        return None

    # ==================== 內部方法 ====================

    def _find_device(self, device_id):
        if device_id in self._device_cache:
            return self._device_cache[device_id]
        # 重新載入
        self.get_devices(force_refresh=True)
        return self._device_cache.get(device_id)

    def _find_platform(self, plat_id):
        for p in self.get_platforms():
            if p['id'] == plat_id:
                return p
        return None

    def _get_adapter(self, plat):
        pid = plat['id']
        if pid not in self._adapters:
            self._connect(plat)
        return self._adapters.get(pid)

    def _connect(self, plat):
        ptype = plat.get('type', '')
        adapter = None
        if ptype == 'homeassistant':
            adapter = HomeAssistantAdapter(plat)
        elif ptype == 'tuya':
            adapter = TuyaAdapter(plat)
        elif ptype == 'mqtt':
            adapter = MQTTAdapter(plat)
        elif ptype == 'http':
            adapter = HTTPAdapter(plat)

        if adapter:
            try:
                adapter.connect()
                self._adapters[plat['id']] = adapter
                print(f'  🏠 已連線 {plat["name"]} ({ptype})')
            except Exception as e:
                print(f'  ❌ 連線 {plat["name"]} 失敗: {e}')

    def _disconnect(self, plat_id):
        adapter = self._adapters.pop(plat_id, None)
        if adapter and hasattr(adapter, 'disconnect'):
            try:
                adapter.disconnect()
            except Exception:
                pass

    def _invalidate_cache(self):
        self._cache_time = 0

    def connect_all_enabled(self):
        """啟動時連線所有已啟用的平台"""
        for plat in self.get_platforms():
            if plat.get('enabled'):
                self._connect(plat)


# ==================== Adapter 抽象基底 ====================

class BaseAdapter:
    def __init__(self, platform_config):
        self.plat = platform_config

    def connect(self):
        raise NotImplementedError

    def disconnect(self):
        pass

    def get_devices(self):
        raise NotImplementedError

    def control(self, device_id, action, params):
        raise NotImplementedError

    def get_state(self, device_id):
        raise NotImplementedError


# ==================== Home Assistant Adapter ====================

class HomeAssistantAdapter(BaseAdapter):
    """透過 Home Assistant REST API 控制"""

    def connect(self):
        self.base_url = self.plat['host'].rstrip('/')
        self.token = self.plat['token']
        self.headers = {
            'Authorization': f'Bearer {self.token}',
            'Content-Type': 'application/json',
        }
        # 測試連線
        r = _req.get(f'{self.base_url}/api/', headers=self.headers, timeout=10)
        r.raise_for_status()

    def get_devices(self):
        r = _req.get(f'{self.base_url}/api/states', headers=self.headers, timeout=15)
        r.raise_for_status()
        devices = []
        for entity in r.json():
            eid = entity['entity_id']
            domain = eid.split('.')[0]
            # 只列出常見的可控制裝置
            if domain in ('light', 'switch', 'fan', 'climate', 'cover',
                          'media_player', 'lock', 'vacuum', 'scene', 'script'):
                devices.append({
                    'id': eid,
                    'name': entity['attributes'].get('friendly_name', eid),
                    'type': domain,
                    'state': entity['state'],
                    'attributes': entity.get('attributes', {}),
                })
        return devices

    def control(self, device_id, action, params):
        domain = device_id.split('.')[0]
        # 動作對應
        service_map = {
            'on': 'turn_on', 'off': 'turn_off', 'toggle': 'toggle',
            'open': 'open_cover', 'close': 'close_cover',
            'lock': 'lock', 'unlock': 'unlock',
            'set_temperature': 'set_temperature',
            'set_brightness': 'turn_on',  # brightness 透過 turn_on 的 data
        }
        service = service_map.get(action, action)
        data = {'entity_id': device_id}
        if action == 'set_brightness' and 'brightness' in params:
            data['brightness'] = int(params['brightness'])
        elif action == 'set_temperature' and 'temperature' in params:
            data['temperature'] = float(params['temperature'])
        data.update({k: v for k, v in params.items() if k not in data})

        r = _req.post(
            f'{self.base_url}/api/services/{domain}/{service}',
            headers=self.headers, json=data, timeout=15
        )
        r.raise_for_status()
        return {'action': service, 'device': device_id}

    def get_state(self, device_id):
        r = _req.get(
            f'{self.base_url}/api/states/{device_id}',
            headers=self.headers, timeout=10
        )
        r.raise_for_status()
        entity = r.json()
        return {
            'id': device_id,
            'name': entity['attributes'].get('friendly_name', device_id),
            'type': device_id.split('.')[0],
            'state': entity['state'],
            'attributes': entity.get('attributes', {}),
        }


# ==================== Tuya Adapter ====================

class TuyaAdapter(BaseAdapter):
    """Tuya / 塗鴉智能 — 透過 Tuya Open API"""

    def connect(self):
        # Tuya 需要 access_id + access_secret + device_id
        # 這裡提供基本框架，實際需要 Tuya Cloud SDK
        self.host = self.plat.get('host', 'https://openapi.tuyaus.com')
        self.access_id = self.plat.get('extra', {}).get('access_id', '')
        self.access_secret = self.plat.get('extra', {}).get('access_secret', '')
        if not self.access_id:
            raise Exception('請填入 Tuya Access ID')

    def get_devices(self):
        # TODO: 實作 Tuya API 取得裝置列表
        # 需要 sign 機制，建議使用 tinytuya 套件
        return []

    def control(self, device_id, action, params):
        # TODO: 實作 Tuya 裝置控制
        return {'status': 'not_implemented', 'message': '請安裝 tinytuya 套件'}

    def get_state(self, device_id):
        return None


# ==================== MQTT Adapter ====================

class MQTTAdapter(BaseAdapter):
    """通用 MQTT 控制（適用 Zigbee2MQTT、Tasmota 等）"""

    def connect(self):
        self.host = self.plat.get('host', 'localhost')
        self.port = int(self.plat.get('extra', {}).get('port', 1883))
        self.topic_prefix = self.plat.get('extra', {}).get('topic_prefix', 'zigbee2mqtt')
        self._client = None
        self._device_states = {}
        # MQTT 需要 paho-mqtt
        try:
            import paho.mqtt.client as mqtt
            self._client = mqtt.Client()
            user = self.plat.get('extra', {}).get('username', '')
            pwd = self.plat.get('extra', {}).get('password', '')
            if user:
                self._client.username_pw_set(user, pwd)
            self._client.on_message = self._on_message
            self._client.connect(self.host, self.port, 60)
            self._client.subscribe(f'{self.topic_prefix}/#')
            self._client.loop_start()
        except ImportError:
            raise Exception('請安裝 paho-mqtt: pip3 install paho-mqtt')

    def disconnect(self):
        if self._client:
            self._client.loop_stop()
            self._client.disconnect()

    def _on_message(self, client, userdata, msg):
        try:
            topic = msg.topic
            payload = json.loads(msg.payload.decode())
            # zigbee2mqtt/device_name → device_name
            parts = topic.replace(self.topic_prefix + '/', '').split('/')
            device_name = parts[0] if parts else topic
            self._device_states[device_name] = payload
        except Exception:
            pass

    def get_devices(self):
        devices = []
        for name, state in self._device_states.items():
            if name in ('bridge', 'bridge/state', 'bridge/info'):
                continue
            devices.append({
                'id': f'mqtt_{name}',
                'name': name,
                'type': 'switch' if 'state' in state else 'sensor',
                'state': state.get('state', 'unknown'),
                'attributes': state,
            })
        return devices

    def control(self, device_id, action, params):
        device_name = device_id.replace('mqtt_', '')
        payload = {}
        if action in ('on', 'off', 'toggle'):
            payload['state'] = action.upper()
        if 'brightness' in params:
            payload['brightness'] = int(params['brightness'])
        if 'color_temp' in params:
            payload['color_temp'] = int(params['color_temp'])
        payload.update(params)

        topic = f'{self.topic_prefix}/{device_name}/set'
        self._client.publish(topic, json.dumps(payload))
        return {'topic': topic, 'payload': payload}

    def get_state(self, device_id):
        device_name = device_id.replace('mqtt_', '')
        state = self._device_states.get(device_name, {})
        return {
            'id': device_id,
            'name': device_name,
            'state': state.get('state', 'unknown'),
            'attributes': state,
        }


# ==================== HTTP Adapter ====================

class HTTPAdapter(BaseAdapter):
    """通用 HTTP API 控制（自訂端點）"""

    def connect(self):
        self.base_url = self.plat['host'].rstrip('/')
        self.auth_header = self.plat.get('extra', {}).get('auth_header', '')
        self.headers = {'Content-Type': 'application/json'}
        if self.auth_header:
            key, val = self.auth_header.split(':', 1) if ':' in self.auth_header else ('Authorization', self.auth_header)
            self.headers[key.strip()] = val.strip()

    def get_devices(self):
        endpoint = self.plat.get('extra', {}).get('devices_endpoint', '/devices')
        try:
            r = _req.get(f'{self.base_url}{endpoint}', headers=self.headers, timeout=10)
            r.raise_for_status()
            data = r.json()
            # 嘗試解析常見格式
            if isinstance(data, list):
                return [self._normalize(d) for d in data]
            if isinstance(data, dict) and 'devices' in data:
                return [self._normalize(d) for d in data['devices']]
            return []
        except Exception:
            return []

    def _normalize(self, d):
        return {
            'id': str(d.get('id', d.get('device_id', ''))),
            'name': d.get('name', d.get('friendly_name', '未知裝置')),
            'type': d.get('type', 'switch'),
            'state': d.get('state', 'unknown'),
            'attributes': d,
        }

    def control(self, device_id, action, params):
        endpoint = self.plat.get('extra', {}).get('control_endpoint', '/devices/{id}/control')
        url = f'{self.base_url}{endpoint}'.replace('{id}', device_id)
        payload = {'action': action, **params}
        r = _req.post(url, headers=self.headers, json=payload, timeout=15)
        r.raise_for_status()
        return r.json()

    def get_state(self, device_id):
        endpoint = self.plat.get('extra', {}).get('state_endpoint', '/devices/{id}')
        url = f'{self.base_url}{endpoint}'.replace('{id}', device_id)
        try:
            r = _req.get(url, headers=self.headers, timeout=10)
            r.raise_for_status()
            return self._normalize(r.json())
        except Exception:
            return None
