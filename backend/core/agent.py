#!/usr/bin/env python3
"""
Agent Loop
核心循環：LLM ↔ tool execution，最多 max_iterations 輪
"""

import json
import os
import platform
import time
import requests
from datetime import datetime
from pathlib import Path
from core.tools import create_default_tools
from core.context_engine import ContextEngine


class AgentLoop:
    PROVIDER_URLS = {
        'groq': 'https://api.groq.com/openai/v1/chat/completions',
        'openai': 'https://api.openai.com/v1/chat/completions',
        'deepseek': 'https://api.deepseek.com/v1/chat/completions',
        'kimi': 'https://api.moonshot.cn/v1/chat/completions',
        'openrouter': 'https://openrouter.ai/api/v1/chat/completions',
        'qwen': 'https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions',
        'ollama': 'http://127.0.0.1:11434/v1/chat/completions',
        'mistral': 'https://api.mistral.ai/v1/chat/completions',
        'together': 'https://api.together.xyz/v1/chat/completions',
        'fireworks': 'https://api.fireworks.ai/inference/v1/chat/completions',
        'cohere': 'https://api.cohere.ai/v1/chat/completions',
    }
    DEFAULT_MODELS = {
        'groq': 'llama-3.3-70b-versatile',
        'openai': 'gpt-4o',
        'deepseek': 'deepseek-chat',
        'kimi': 'moonshot-v1-8k',
        'openrouter': 'anthropic/claude-3.5-sonnet',
        'qwen': 'qwen-turbo',
        'ollama': 'llama3.1',
        'mistral': 'mistral-large-latest',
        'together': 'meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo',
        'fireworks': 'accounts/fireworks/models/llama-v3p1-70b-instruct',
        'cohere': 'command-r-plus',
    }

    def __init__(self, config_mgr, memory_mgr):
        self.config = config_mgr
        self.memory = memory_mgr
        self.context = ContextEngine(config_mgr)
        self.tools = create_default_tools()
        # 載入用戶自訂技能
        custom_count = self.tools.load_custom_tools()
        if custom_count:
            print(f'  🔧 已載入 {custom_count} 個自訂技能')
        self.sessions = {}
        self.max_iterations = 20
        self.permissions = None  # 由 server.py 注入
        self._sessions_dir = os.path.join(str(Path.home()), '.autoto', 'sessions')
        self._load_sessions()

    @property
    def _token_budget(self):
        """從 config 讀取 token 預算上限，預設 4000"""
        return int(self.config.get('agent', {}).get('maxTokenBudget', 4000))

    @staticmethod
    def _estimate_tokens(text):
        """粗估 token 數：英文 ~4 chars/token，中文 ~1.5 chars/token"""
        if not text:
            return 0
        ascii_chars = sum(1 for c in text if ord(c) < 128)
        non_ascii = len(text) - ascii_chars
        return int(ascii_chars / 4 + non_ascii / 1.5)

    def _load_sessions(self):
        """從本地載入 session 歷史"""
        if not self.config.get('session.persist', True):
            return
        if not os.path.isdir(self._sessions_dir):
            return
        try:
            for fname in os.listdir(self._sessions_dir):
                if fname.endswith('.json'):
                    sid = fname[:-5]
                    fpath = os.path.join(self._sessions_dir, fname)
                    with open(fpath, 'r', encoding='utf-8') as f:
                        data = json.load(f)
                    if isinstance(data, list):
                        self.sessions[sid] = data[-40:]
            loaded = len(self.sessions)
            if loaded:
                print(f'  📂 已載入 {loaded} 個對話紀錄')
        except Exception as e:
            print(f'  ⚠️ 載入 session 失敗: {e}')

    def _save_session(self, session_id):
        """儲存單一 session 到本地"""
        if not self.config.get('session.persist', True):
            return
        try:
            os.makedirs(self._sessions_dir, exist_ok=True)
            # 檔名用 session_id，替換不安全字元
            safe_name = session_id.replace('/', '_').replace('\\', '_')
            fpath = os.path.join(self._sessions_dir, safe_name + '.json')
            with open(fpath, 'w', encoding='utf-8') as f:
                json.dump(self.sessions.get(session_id, []), f, ensure_ascii=False, indent=1)
        except Exception as e:
            print(f'  ⚠️ 儲存 session 失敗: {e}')

    def clear_all_sessions(self):
        """清除所有本地 session"""
        self.sessions = {}
        if os.path.isdir(self._sessions_dir):
            import shutil
            shutil.rmtree(self._sessions_dir, ignore_errors=True)
        return True

    def create_session(self, session_id):
        """建立空白 session，讓前端可以先新增對話再開始發訊息"""
        if session_id not in self.sessions:
            self.sessions[session_id] = []
        self._save_session(session_id)
        return session_id

    def process_message(self, session_id, message, source='web'):
        if session_id not in self.sessions:
            self.sessions[session_id] = []
        history = self.sessions[session_id]
        history.append({'role': 'user', 'content': message})

        messages = self._build_messages(session_id, message)
        final, tool_summary = self._run_loop(messages)

        # 把工具執行摘要附加到 assistant 回覆，讓下次對話有上下文
        if tool_summary:
            history.append({'role': 'assistant', 'content': final, '_tools': tool_summary})
        else:
            history.append({'role': 'assistant', 'content': final})
        self.memory.auto_capture(message, final)

        # Context Engine：根據 token 用量動態壓縮
        if len(history) > 8:
            total_tokens = sum(self._estimate_tokens(m.get('content', '')) for m in history)
            if total_tokens > self._token_budget * 0.6:
                self.context.compress_history(session_id, history)

        if len(history) > 40:
            self.sessions[session_id] = history[-20:]
        self._save_session(session_id)
        return final

    # Groq 免費版 fallback 模型列表（rate limit 分開算）
    GROQ_FALLBACKS = ['llama-3.3-70b-versatile', 'llama-3.1-8b-instant', 'gemma2-9b-it', 'llama-3.1-70b-versatile']

    def _run_loop(self, messages):
        """Agent loop：LLM → tool calls → 結果 → 再 LLM"""
        provider = self.config.get('provider', 'groq')
        api_key = self.config.get('apiKey', '')
        model = self.config.get('model', '')
        if not api_key:
            return '⚠️ 請先在設定中配置 API Key', ''

        # 前置檢查：如果用戶請求需要停用的工具，直接攔截
        blocked = self._check_disabled_intent(messages)
        if blocked:
            return blocked, ''

        tool_summary_parts = []

        for iteration in range(self.max_iterations):
            try:
                result = self._call_llm(provider, api_key, model, messages)
            except Exception as e:
                err_str = str(e)
                # Rate limit → 自動嘗試 fallback 模型
                if '429' in err_str and provider == 'groq':
                    result = self._try_groq_fallback(api_key, model, messages)
                    if result is None:
                        return '⚠️ Groq 所有模型都達到速率限制，請稍後再試。', ''
                elif 'tool_use_failed' in err_str:
                    try:
                        skills = self.config.get('skills', {})
                        disabled = [n for n, v in (skills if isinstance(skills, dict) else {}).items() if v is False]
                        if disabled:
                            messages.append({'role': 'system', 'content':
                                f'IMPORTANT: The following tools are DISABLED by the user: {", ".join(disabled)}. '
                                f'You CANNOT use them. Tell the user: 「此功能已被停用，請到技能管理頁面啟用。」 Do NOT pretend you executed them.'})
                        result = self._call_llm_no_tools(provider, api_key, model, messages)
                    except Exception as e2:
                        return f'❌ API 錯誤: {e2}', ''
                else:
                    return f'❌ API 錯誤: {e}', ''

            content = result.get('content', '')
            tool_calls = result.get('tool_calls')

            if not tool_calls:
                return content or '(no response)', '\n'.join(tool_summary_parts)

            # 加入 assistant 的 tool_call 訊息
            messages.append({
                'role': 'assistant',
                'content': content,
                'tool_calls': tool_calls
            })

            # 執行每個工具
            for tc in tool_calls:
                name = tc['function']['name']
                try:
                    args = json.loads(tc['function']['arguments'])
                except (json.JSONDecodeError, KeyError):
                    args = {}

                # 檢查技能是否啟用
                skills = self.config.get('skills', {})
                if isinstance(skills, dict) and skills.get(name) is False:
                    tool_result = f'Error: 技能「{name}」已被停用，請到技能管理頁面啟用。'
                else:
                    # 權限沙盒檢查
                    if self.permissions:
                        allowed, reason, needs_confirm = self.permissions.check_permission(name, args)
                        if not allowed:
                            tool_result = f'Error: {reason}'
                        else:
                            self.permissions.record_call(name)
                            tool_result = self.tools.execute(name, args)
                    else:
                        tool_result = self.tools.execute(name, args)

                # 記錄工具摘要
                args_short = json.dumps(args, ensure_ascii=False)
                if len(args_short) > 100:
                    args_short = args_short[:100] + '...'
                result_short = str(tool_result)[:200]
                tool_summary_parts.append(f'[{name}({args_short}) → {result_short}]')

                # 截斷過長結果
                if len(str(tool_result)) > 5000:
                    tool_result = str(tool_result)[:5000] + '\n...(truncated)'

                messages.append({
                    'role': 'tool',
                    'tool_call_id': tc['id'],
                    'content': str(tool_result)
                })

        return (content if content else '⚠️ 達到最大工具執行輪次'), '\n'.join(tool_summary_parts)

    def _get_enabled_schemas(self):
        """只回傳啟用的工具 schemas"""
        skills = self.config.get('skills', {})
        if not isinstance(skills, dict):
            return self.tools.get_schemas()
        return [s for s in self.tools.get_schemas()
                if skills.get(s['function']['name'], True) is not False]

    def _try_shortcut(self, message):
        """偵測明確的電腦操作指令，直接執行不經過 LLM"""
        import re
        msg = message.strip()

        # 匹配「在 X 打字/輸入/回覆/打 Y」
        m = re.search(r'(?:在|到|去)\s*(\S+?)\s*(?:裡|裡面|上|上面|的聊天框|的對話框|的輸入框)?\s*(?:打字|輸入|回覆|打|寫|說|傳送?|發送?|送出)\s*(.+)', msg)
        if m:
            app_name = m.group(1)
            text = m.group(2).strip().strip('"\'「」')
            result = self.tools.execute('type_text', {'text': text, 'app_name': app_name, 'press_enter': True})
            return f'已在 {app_name} 輸入「{text}」並按下 Enter。\n工具回傳：{result}'

        # 匹配「幫我操作 X 並回覆/輸入 Y」
        m = re.search(r'操作\s*(\S+?)\s*(?:並|然後|再)?\s*(?:回覆|輸入|打字|打|寫|說)\s*(.+)', msg)
        if m:
            app_name = m.group(1)
            text = m.group(2).strip().strip('"\'「」')
            result = self.tools.execute('type_text', {'text': text, 'app_name': app_name, 'press_enter': True})
            return f'已在 {app_name} 輸入「{text}」並按下 Enter。\n工具回傳：{result}'

        return None

    def _try_groq_fallback(self, api_key, current_model, messages):
        """嘗試 Groq 的其他模型（rate limit 分開算）"""
        for fallback in self.GROQ_FALLBACKS:
            if fallback == current_model:
                continue
            try:
                result = self._call_llm('groq', api_key, fallback, messages)
                return result
            except Exception as e:
                if '429' in str(e):
                    continue
                # 非 rate limit 錯誤，嘗試無工具模式
                try:
                    return self._call_llm_no_tools('groq', api_key, fallback, messages)
                except:
                    continue
        return None

    def _check_disabled_intent(self, messages):
        """檢查用戶意圖是否需要已停用的工具，如果是就直接回覆"""
        import re
        skills = self.config.get('skills', {})
        if not isinstance(skills, dict):
            return None

        # 取最後一條 user message
        user_msg = ''
        for m in reversed(messages):
            if m['role'] == 'user':
                user_msg = m.get('content', '').lower()
                break
        if not user_msg:
            return None

        # 意圖 → 工具對應
        intent_map = {
            'open_url': [r'打開.{0,5}(網|google|谷歌|yahoo|bing|youtube|github|網頁|網站|url|http)',
                         r'開啟.{0,5}(網|google|谷歌|yahoo|bing|youtube|github|網頁|網站)',
                         r'瀏覽', r'搜尋.{0,5}(網|google)', r'上網'],
            'open_app': [r'打開.{0,5}(app|應用|safari|chrome|finder|terminal|終端|備忘錄|notes|music)',
                         r'開啟.{0,5}(app|應用|safari|chrome|finder|terminal|終端)'],
            'screenshot': [r'截圖', r'螢幕截圖', r'screenshot'],
            'type_text': [r'打字', r'輸入.{0,5}(到|在)', r'在.{1,10}(打|輸入|回覆|寫)'],
            'exec': [r'執行.{0,5}(命令|指令|command)', r'跑.{0,5}(命令|指令|script)'],
            'delete_file': [r'刪除.{0,5}(檔|文件|file)', r'移除.{0,5}(檔|文件)', r'刪掉.{0,5}(檔|文件)'],
            'web_search': [r'搜尋', r'搜索', r'查一下', r'幫我查', r'google.*一下', r'search'],
            'web_fetch': [r'抓取.{0,5}(網|頁)', r'爬.{0,5}(網|頁)', r'擷取.{0,5}(網|頁)'],
            'notification': [r'通知', r'提醒我', r'notification'],
            'weather': [r'天氣', r'氣溫', r'weather'],
            'process_kill': [r'殺掉.{0,5}(程|進|process)', r'關閉.{0,5}(程|進|process)', r'kill'],
            'cron_add': [r'排程', r'定時', r'每天.{0,5}(執行|跑|提醒)', r'cron'],
        }

        for tool_name, patterns in intent_map.items():
            if skills.get(tool_name, True) is False:
                for pat in patterns:
                    if re.search(pat, user_msg):
                        tool_labels = {
                            'open_url': '開啟網頁', 'open_app': '開啟應用',
                            'screenshot': '螢幕截圖', 'type_text': '模擬打字',
                            'exec': '終端命令', 'read_file': '讀取檔案',
                            'write_file': '寫入檔案', 'edit_file': '編輯檔案',
                            'delete_file': '刪除檔案', 'list_dir': '瀏覽資料夾',
                            'key_press': '鍵盤快捷鍵', 'web_search': '網頁搜尋',
                            'web_fetch': '網頁擷取', 'clipboard_read': '讀取剪貼簿',
                            'clipboard_write': '寫入剪貼簿', 'process_list': '程序列表',
                            'process_kill': '終止程序', 'notification': '系統通知',
                            'cron_list': '排程列表', 'cron_add': '新增排程',
                            'cron_remove': '移除排程', 'memory_search': '搜尋記憶',
                            'system_info': '系統資訊', 'summarize': '文字摘要',
                            'weather': '天氣查詢',
                        }
                        label = tool_labels.get(tool_name, tool_name)
                        return f'⚠️ 「{label}」功能已被停用。請到技能管理頁面啟用後再試。'
        return None

    def _call_llm(self, provider, api_key, model, messages):
        """呼叫 LLM with tools"""
        if provider in ('anthropic', 'claude'):
            return self._call_anthropic(api_key, model, messages)
        if provider == 'gemini':
            return self._call_gemini(api_key, model, messages)

        # 自訂端點
        if provider == 'custom':
            url = self.config.get('customUrl', '')
            if not url:
                raise Exception('請在設定中填入自訂 API 端點')
        else:
            url = self.PROVIDER_URLS.get(provider, self.PROVIDER_URLS['groq'])
        used_model = model or self.DEFAULT_MODELS.get(provider, 'llama-3.3-70b-versatile')

        # 清理 messages
        clean = []
        for m in messages:
            msg = {'role': m['role'], 'content': m.get('content') or ''}
            if 'tool_calls' in m:
                msg['tool_calls'] = m['tool_calls']
            if 'tool_call_id' in m:
                msg['tool_call_id'] = m['tool_call_id']
            clean.append(msg)

        headers = {'Content-Type': 'application/json'}
        # Ollama 不需要 API Key
        if provider != 'ollama' and api_key:
            headers['Authorization'] = f'Bearer {api_key}'

        body = {
            'model': used_model,
            'messages': clean,
            'temperature': 0.7,
            'max_tokens': min(self._token_budget, 8192),
        }

        # Ollama 的某些模型不支援 tools，先嘗試帶 tools
        schemas = self._get_enabled_schemas()
        if provider == 'ollama':
            # Ollama 支援 tools（0.4+），但某些小模型可能不支援
            body['tools'] = schemas
            body['tool_choice'] = 'auto'
        else:
            body['tools'] = schemas
            body['tool_choice'] = 'auto'

        timeout = 120 if provider == 'ollama' else 60
        res = requests.post(url,
            headers=headers,
            json=body,
            timeout=timeout
        )
        if res.status_code != 200:
            err = res.text[:300]
            # Ollama 不支援 tools 時，fallback 到無 tools 模式
            if provider == 'ollama' and ('tool' in err.lower() or res.status_code == 400):
                return self._call_llm_no_tools(provider, api_key, model, messages)
            raise Exception(f'{res.status_code}: {err}')
        choice = res.json()['choices'][0]['message']
        return {'content': choice.get('content', ''), 'tool_calls': choice.get('tool_calls')}

    def _call_anthropic(self, api_key, model, messages):
        sys_parts = [m['content'] for m in messages if m['role'] == 'system']
        chat = []
        for m in messages:
            if m['role'] == 'system':
                continue
            if m['role'] == 'tool':
                chat.append({'role': 'user', 'content': [
                    {'type': 'tool_result', 'tool_use_id': m['tool_call_id'], 'content': m['content']}
                ]})
            elif m.get('tool_calls'):
                blocks = []
                if m.get('content'):
                    blocks.append({'type': 'text', 'text': m['content']})
                for tc in m['tool_calls']:
                    blocks.append({
                        'type': 'tool_use', 'id': tc['id'],
                        'name': tc['function']['name'],
                        'input': json.loads(tc['function']['arguments'])
                    })
                chat.append({'role': 'assistant', 'content': blocks})
            else:
                chat.append({'role': m['role'], 'content': m.get('content', '')})

        tools = [{'name': s['function']['name'], 'description': s['function']['description'],
                   'input_schema': s['function']['parameters']} for s in self._get_enabled_schemas()]

        res = requests.post('https://api.anthropic.com/v1/messages',
            headers={'x-api-key': api_key, 'anthropic-version': '2023-06-01', 'Content-Type': 'application/json'},
            json={
                'model': model or 'claude-3-5-sonnet-20241022',
                'max_tokens': min(self._token_budget, 8192),
                'system': '\n\n'.join(sys_parts),
                'messages': chat or [{'role': 'user', 'content': ''}],
                'tools': tools
            },
            timeout=60
        )
        res.raise_for_status()
        text, tcs = '', []
        for block in res.json().get('content', []):
            if block['type'] == 'text':
                text += block['text']
            elif block['type'] == 'tool_use':
                tcs.append({'id': block['id'], 'type': 'function',
                    'function': {'name': block['name'], 'arguments': json.dumps(block['input'])}})
        return {'content': text, 'tool_calls': tcs or None}

    def _call_gemini(self, api_key, model, messages):
        model_name = model or 'gemini-pro'
        contents = []
        for m in messages:
            if m['role'] == 'system':
                contents.append({'role': 'user', 'parts': [{'text': f'[System] {m["content"]}'}]})
                contents.append({'role': 'model', 'parts': [{'text': 'OK.'}]})
            elif m['role'] == 'user':
                contents.append({'role': 'user', 'parts': [{'text': m['content']}]})
            elif m['role'] == 'assistant':
                contents.append({'role': 'model', 'parts': [{'text': m.get('content', '')}]})
        res = requests.post(
            f'https://generativelanguage.googleapis.com/v1/models/{model_name}:generateContent?key={api_key}',
            json={'contents': contents}, timeout=60
        )
        res.raise_for_status()
        return {'content': res.json()['candidates'][0]['content']['parts'][0]['text'], 'tool_calls': None}

    def _call_llm_no_tools(self, provider, api_key, model, messages):
        """不帶 tools 的純文字 fallback"""
        if provider == 'custom':
            url = self.config.get('customUrl', '')
            if not url:
                raise Exception('請在設定中填入自訂 API 端點')
        else:
            url = self.PROVIDER_URLS.get(provider, self.PROVIDER_URLS['groq'])
        used_model = model or self.DEFAULT_MODELS.get(provider, 'llama-3.3-70b-versatile')
        # 只保留 system 和 user messages
        clean = []
        for m in messages:
            if m['role'] in ('system', 'user', 'assistant') and 'tool_calls' not in m:
                clean.append({'role': m['role'], 'content': m.get('content') or ''})
        headers = {'Content-Type': 'application/json'}
        if provider != 'ollama' and api_key:
            headers['Authorization'] = f'Bearer {api_key}'
        timeout = 120 if provider == 'ollama' else 60
        res = requests.post(url,
            headers=headers,
            json={'model': used_model, 'messages': clean, 'temperature': 0.7, 'max_tokens': min(self._token_budget, 8192)},
            timeout=timeout
        )
        res.raise_for_status()
        text = res.json()['choices'][0]['message'].get('content', '')
        return {'content': text, 'tool_calls': None}

    def _build_messages(self, session_id, current_message):
        """根據當前 session 建立送往模型的 messages"""
        messages = []
        agent_cfg = self.config.get('agent', {})

        # System prompt
        runtime = f"{'macOS' if platform.system() == 'Darwin' else platform.system()} {platform.machine()}"
        now = datetime.now().strftime('%Y-%m-%d %H:%M (%A)')
        tz = time.strftime('%Z') or 'UTC'

        system = agent_cfg.get('systemPrompt', '你是 AutoTo，一個智能 AI 助理。')

        # 動態生成啟用/停用的工具列表
        all_tool_names = {
            'exec': 'run any shell command',
            'read_file': 'read a file',
            'write_file': 'create/overwrite a file',
            'edit_file': 'find and replace text in a file',
            'delete_file': 'delete a file',
            'list_dir': 'list directory contents',
            'open_url': 'open a URL in browser',
            'open_app': 'open an application',
            'screenshot': 'take a screenshot',
            'type_text': 'type text into any app using clipboard paste',
            'key_press': 'press keyboard keys with optional modifiers',
            'web_search': 'search the web via DuckDuckGo',
            'web_fetch': 'fetch and extract text from a URL',
            'clipboard_read': 'read clipboard content',
            'clipboard_write': 'write text to clipboard',
            'process_list': 'list running processes',
            'process_kill': 'kill a process by PID or name',
            'notification': 'send a system notification',
            'cron_list': 'list scheduled tasks',
            'cron_add': 'add a scheduled task',
            'cron_remove': 'remove a scheduled task',
            'memory_search': 'search saved memories',
            'system_info': 'get system info (OS, disk, memory)',
            'summarize': 'summarize text',
            'weather': 'get current weather',
            'click': 'click at screen coordinates (x, y)',
            'move_mouse': 'move the mouse cursor to screen coordinates',
            'drag_mouse': 'drag the mouse from one coordinate to another',
            'scroll': 'scroll the current view up or down',
            'focus_app': 'bring an application window to the foreground',
            'screen_size': 'get the primary screen width and height',
            'scan_media_folder': 'scan a folder for video/audio files and return metadata',
            'video_probe': 'inspect media metadata such as duration and streams',
            'video_cut': 'cut a media file to a target start time and duration',
            'video_concat': 'concatenate multiple media files into one output video',
            'video_extract_audio': 'extract audio from a media file into mp3/wav/m4a',
            'transcribe_media': 'transcribe a media file into text and subtitles with Whisper',
            'youtube_play': 'search YouTube and directly play the top result',
            'ig_get_posts': 'list recent Instagram posts with likes/comments',
            'ig_get_comments': 'get comments on an Instagram post',
            'ig_reply_comment': 'reply to a specific Instagram comment',
            'ig_post_comment': 'post a new comment on an Instagram post',
            'ig_delete_comment': 'delete an Instagram comment',
            'ig_publish_media': 'publish a photo, video, or reel to Instagram',
            'fb_post': 'post text, link, or photo to a Facebook Page',
            'x_post': 'post a tweet to Twitter/X',
            'threads_publish': 'publish a text, image, or video post to Threads',
            'web_scrape_structured': 'scrape a web page and extract links, images, headings, meta',
            'web_download_file': 'download a file from a URL to local path',
            'camera_list': 'list all cameras and their streaming status',
            'camera_snapshot': 'take a snapshot from a camera',
            'camera_stream_control': 'start or stop a camera stream',
            'camera_analyze': 'analyze camera feed with AI vision — describe what is seen, detect anomalies',
            'camera_watch_control': 'start/stop/check AI continuous monitoring on a camera',
            'smarthome_list_devices': 'list all smart home devices and their states',
            'smarthome_control': 'control a smart home device (on/off/toggle/brightness/temperature)',
            'smarthome_device_state': 'get the current state of a smart home device',
        }
        # 動態加入自訂技能
        for ct in self.tools.get_custom_tools():
            cname = ct.get('name', '')
            if cname and cname not in all_tool_names:
                all_tool_names[cname] = ct.get('description', cname)
        skills = self.config.get('skills', {})
        if not isinstance(skills, dict):
            skills = {}
        enabled_tools = []
        disabled_tools = []
        for name, desc in all_tool_names.items():
            if skills.get(name, True) is False:
                disabled_tools.append(name)
            else:
                enabled_tools.append(f'   - {name}: {desc}')

        enabled_list = '\n'.join(enabled_tools)
        disabled_notice = ''
        if disabled_tools:
            disabled_notice = f'\n10. DISABLED TOOLS (user turned off, DO NOT use): {", ".join(disabled_tools)}. If user asks for something that requires a disabled tool, tell them: 「此功能已被停用，請到技能管理頁面啟用。」'

        system += f'''

## Runtime
{runtime}, Python {platform.python_version()}
Current Time: {now} ({tz})

## CRITICAL RULES — YOU MUST FOLLOW
1. You have FULL control of the user's computer through tools. You MUST use tools to fulfill requests. NEVER pretend or simulate — actually execute.
2. When the user asks to do ANYTHING on the computer, you MUST call the appropriate tool. DO NOT just describe steps. DO NOT say "I can't do that". You CAN do it — use the tools.
3. Your ENABLED tools:
{enabled_list}
4. Examples:
   - "在 Kiro 打字 ok" → call type_text with text="ok", app_name="Kiro"
   - "打開 Google" → call open_url with "https://www.google.com"
   - "列出桌面檔案" → call list_dir
   - "截圖" → call screenshot
   - "按 Cmd+C" → call key_press with key="c", modifiers="command"
   - "播放 Closer" → call youtube_play with query="Closer The Chainsmokers"
   - "我要聽周杰倫的稻香" → call youtube_play with query="周杰倫 稻香"
   - "播放末班車" → call youtube_play with query="末班車"
   - "打開 YouTube 播放末班車" → call youtube_play with query="末班車" (ONE tool call only, do NOT open YouTube separately)
   - "幫我打開 YouTube 然後播放 Closer" → call youtube_play with query="Closer" (ONE tool call only)
   - "看一下門口攝影機" → call camera_analyze with camera_name="門口"
   - "門口有沒有人" → call camera_analyze with camera_name="門口", prompt="有沒有人"
   - "幫我監控門口，有人就通知我" → call camera_watch_control with camera_name="門口", action="start"
   - "停止監控" → call camera_watch_control with action="stop"
   - "打開客廳燈" → call smarthome_control with device_name="客廳燈", action="on"
   - "關掉冷氣" → call smarthome_control with device_name="冷氣", action="off"
   - "家裡有哪些裝置" → call smarthome_list_devices
5. NEVER make up fake results. If you need information, use a tool to get it.
6. 修改檔案前先用 read_file 讀取。
7. 工具執行失敗時，分析錯誤再用不同方法重試。
8. 用繁體中文回覆。語氣友善簡潔。
9. 執行完工具後，簡要告訴用戶結果。
10. IMPORTANT TOOL SELECTION RULES:
   - When user mentions YouTube, music, song, video, 播放, 聽, or any media request → ALWAYS use youtube_play as the ONLY tool call. Do NOT also call open_url or open_app for YouTube.
   - Even if user says "打開 YouTube 然後播放 XXX" or "open YouTube and play XXX", treat the ENTIRE request as a single youtube_play call. Do NOT open YouTube separately.
   - youtube_play already opens the browser and plays the video. Calling open_url for YouTube before youtube_play is WRONG and causes duplicate windows.
   - Only use open_url for YouTube if the user EXPLICITLY wants to browse YouTube without playing anything specific (e.g. "打開 YouTube 首頁").{disabled_notice}'''

        messages.append({'role': 'system', 'content': system})

        # 記憶（強化版：少量記憶全注入，讓 LLM 自己判斷相關性）
        if self.config.get('memory.enabled', True):
            relevant = self.memory.recall(current_message, max_tokens=500)
            if relevant:
                mem_lines = '\n'.join(f'- {m["content"]}' for m in relevant)
                mem = f'## 用戶記憶（請根據這些資訊個性化回覆）\n{mem_lines}'
                messages.append({'role': 'system', 'content': mem})

        # 歷史（透過 Context Engine 智慧管理）
        history = self.sessions.get(session_id, [])
        budget = self._token_budget
        used_tokens = sum(self._estimate_tokens(m['content']) for m in messages)
        used_tokens += self._estimate_tokens(current_message)

        context_msgs = self.context.build_context(
            session_id, history, current_message,
            token_budget=budget - used_tokens
        )
        messages.extend(context_msgs)

        messages.append({'role': 'user', 'content': current_message})
        return messages
