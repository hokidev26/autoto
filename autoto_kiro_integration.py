#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
AutoTo Kiro Integration Module
讓 AutoTo 可以直接操作 Kiro AI
"""

import subprocess
import os

KIRO_CLI = "/Applications/Kiro.app/Contents/Resources/app/bin/code"

class KiroIntegration:
    """Kiro AI 整合類"""
    
    def __init__(self):
        self.workspace_path = os.getcwd()
        self.kiro_cli = KIRO_CLI
    
    def send_chat(self, prompt, mode='agent'):
        """
        直接發送指令給 Kiro
        
        mode: 'agent' (自動執行), 'ask' (只問答), 'edit' (編輯模式)
        """
        try:
            cmd = [
                self.kiro_cli, 'chat',
                '--mode', mode,
                '--reuse-window',
                prompt
            ]
            
            result = subprocess.Popen(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE
            )
            
            return {
                'success': True,
                'message': f'✅ 已發送指令給 Kiro（{mode} 模式）：{prompt}'
            }
        except Exception as e:
            return {'success': False, 'error': str(e)}
    
    def open_file(self, filepath):
        """讓 Kiro 打開文件"""
        try:
            subprocess.Popen([self.kiro_cli, '--reuse-window', filepath])
            return {
                'success': True,
                'message': f'✅ 已在 Kiro 中打開：{filepath}'
            }
        except Exception as e:
            return {'success': False, 'error': str(e)}
    
    def open_file_at_line(self, filepath, line, character=0):
        """讓 Kiro 打開文件並跳到指定行"""
        try:
            subprocess.Popen([
                self.kiro_cli, '--reuse-window',
                '--goto', f'{filepath}:{line}:{character}'
            ])
            return {
                'success': True,
                'message': f'✅ 已在 Kiro 中打開：{filepath} 第 {line} 行'
            }
        except Exception as e:
            return {'success': False, 'error': str(e)}
    
    def ask_kiro(self, question):
        """向 Kiro 提問（ask 模式，不會修改文件）"""
        return self.send_chat(question, mode='ask')
    
    def edit_with_kiro(self, instruction):
        """讓 Kiro 編輯（edit 模式）"""
        return self.send_chat(instruction, mode='edit')
    
    def agent_kiro(self, task):
        """讓 Kiro 自動執行任務（agent 模式，會修改文件）"""
        return self.send_chat(task, mode='agent')
    
    def create_file_with_kiro(self, filepath, description):
        """讓 Kiro 創建文件"""
        prompt = f"請創建文件 {filepath}，內容：{description}"
        return self.send_chat(prompt, mode='agent')
    
    def fix_bug_with_kiro(self, filepath, error):
        """讓 Kiro 修復 bug"""
        prompt = f"請修復 {filepath} 中的錯誤：{error}"
        return self.send_chat(prompt, mode='agent')


if __name__ == '__main__':
    kiro = KiroIntegration()
    
    print("🤖 AutoTo x Kiro 整合測試")
    print("=" * 50)
    
    result = kiro.send_chat("你好，請告訴我目前的專案結構", mode='ask')
    print(f"結果：{result['message'] if result['success'] else result['error']}")
