#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
AutoTo Computer Control Module
電腦操作控制模組
"""

import subprocess
import os
import platform
from pathlib import Path

class ComputerControl:
    """電腦操作控制類"""
    
    def __init__(self):
        self.system = platform.system()
        
    # ==================== 文件操作 ====================
    
    def read_file(self, filepath):
        """讀取文件"""
        try:
            with open(filepath, 'r', encoding='utf-8') as f:
                return {'success': True, 'content': f.read()}
        except Exception as e:
            return {'success': False, 'error': str(e)}
    
    def write_file(self, filepath, content):
        """寫入文件"""
        try:
            with open(filepath, 'w', encoding='utf-8') as f:
                f.write(content)
            return {'success': True, 'message': f'文件已寫入：{filepath}'}
        except Exception as e:
            return {'success': False, 'error': str(e)}
    
    def list_files(self, directory='.'):
        """列出文件"""
        try:
            files = os.listdir(directory)
            return {'success': True, 'files': files}
        except Exception as e:
            return {'success': False, 'error': str(e)}
    
    # ==================== Shell 命令 ====================
    
    def run_command(self, command, safe_mode=True):
        """執行 Shell 命令"""
        # 安全模式：限制危險命令
        dangerous_commands = ['rm -rf', 'sudo', 'format', 'del /f']
        
        if safe_mode:
            for dangerous in dangerous_commands:
                if dangerous in command.lower():
                    return {
                        'success': False, 
                        'error': f'安全模式：禁止執行危險命令 "{dangerous}"'
                    }
        
        try:
            result = subprocess.run(
                command,
                shell=True,
                capture_output=True,
                text=True,
                timeout=30
            )
            return {
                'success': True,
                'stdout': result.stdout,
                'stderr': result.stderr,
                'returncode': result.returncode
            }
        except subprocess.TimeoutExpired:
            return {'success': False, 'error': '命令執行超時（30秒）'}
        except Exception as e:
            return {'success': False, 'error': str(e)}
    
    # ==================== 應用程式控制 ====================
    
    def open_application(self, app_name):
        """開啟應用程式"""
        try:
            if self.system == 'Darwin':  # macOS
                subprocess.Popen(['open', '-a', app_name])
                return {'success': True, 'message': f'已開啟：{app_name}'}
            elif self.system == 'Windows':
                subprocess.Popen(['start', app_name], shell=True)
                return {'success': True, 'message': f'已開啟：{app_name}'}
            else:  # Linux
                subprocess.Popen([app_name])
                return {'success': True, 'message': f'已開啟：{app_name}'}
        except Exception as e:
            return {'success': False, 'error': str(e)}
    
    def open_url(self, url):
        """開啟網址"""
        try:
            if self.system == 'Darwin':
                subprocess.Popen(['open', url])
            elif self.system == 'Windows':
                subprocess.Popen(['start', url], shell=True)
            else:
                subprocess.Popen(['xdg-open', url])
            return {'success': True, 'message': f'已開啟：{url}'}
        except Exception as e:
            return {'success': False, 'error': str(e)}
    
    # ==================== 系統資訊 ====================
    
    def get_system_info(self):
        """獲取系統資訊"""
        import psutil
        
        try:
            cpu_percent = psutil.cpu_percent(interval=1)
            memory = psutil.virtual_memory()
            disk = psutil.disk_usage('/')
            
            return {
                'success': True,
                'system': self.system,
                'cpu_usage': f'{cpu_percent}%',
                'memory_usage': f'{memory.percent}%',
                'memory_available': f'{memory.available / (1024**3):.2f} GB',
                'disk_usage': f'{disk.percent}%',
                'disk_free': f'{disk.free / (1024**3):.2f} GB'
            }
        except ImportError:
            return {
                'success': False,
                'error': '需要安裝 psutil：pip3 install psutil'
            }
        except Exception as e:
            return {'success': False, 'error': str(e)}
    
    # ==================== Mac 專用功能 ====================
    
    def mac_say(self, text):
        """Mac 語音朗讀"""
        if self.system != 'Darwin':
            return {'success': False, 'error': '此功能僅支援 macOS'}
        
        try:
            subprocess.run(['say', text])
            return {'success': True, 'message': f'已朗讀：{text}'}
        except Exception as e:
            return {'success': False, 'error': str(e)}
    
    def mac_screenshot(self, filepath='screenshot.png'):
        """Mac 截圖"""
        if self.system != 'Darwin':
            return {'success': False, 'error': '此功能僅支援 macOS'}
        
        try:
            subprocess.run(['screencapture', filepath])
            return {'success': True, 'message': f'截圖已儲存：{filepath}'}
        except Exception as e:
            return {'success': False, 'error': str(e)}
    
    # ==================== 常用操作 ====================
    
    def open_finder(self, path=None):
        """開啟 Finder（Mac）"""
        if self.system != 'Darwin':
            return {'success': False, 'error': '此功能僅支援 macOS'}
        
        try:
            if path:
                subprocess.Popen(['open', path])
            else:
                subprocess.Popen(['open', '.'])
            return {'success': True, 'message': 'Finder 已開啟'}
        except Exception as e:
            return {'success': False, 'error': str(e)}
    
    def open_terminal(self):
        """開啟終端機"""
        try:
            if self.system == 'Darwin':
                subprocess.Popen(['open', '-a', 'Terminal'])
            elif self.system == 'Windows':
                subprocess.Popen(['cmd'])
            else:
                subprocess.Popen(['gnome-terminal'])
            return {'success': True, 'message': '終端機已開啟'}
        except Exception as e:
            return {'success': False, 'error': str(e)}


# ==================== 使用範例 ====================

if __name__ == '__main__':
    control = ComputerControl()
    
    print("🖥️ AutoTo 電腦操作測試")
    print("=" * 50)
    
    # 測試系統資訊
    print("\n1. 系統資訊：")
    info = control.get_system_info()
    if info['success']:
        print(f"   系統：{info['system']}")
        print(f"   CPU 使用率：{info['cpu_usage']}")
        print(f"   記憶體使用率：{info['memory_usage']}")
    
    # 測試文件操作
    print("\n2. 文件操作：")
    result = control.write_file('test.txt', 'Hello AutoTo!')
    print(f"   {result['message'] if result['success'] else result['error']}")
    
    # 測試命令執行
    print("\n3. 執行命令：")
    result = control.run_command('echo "Hello from AutoTo"')
    if result['success']:
        print(f"   輸出：{result['stdout'].strip()}")
    
    print("\n" + "=" * 50)
    print("測試完成！")
