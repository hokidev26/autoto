import subprocess, os, re, platform, webbrowser, json, signal
from pathlib import Path
from datetime import datetime
from urllib.request import urlopen, Request
from urllib.parse import quote_plus
from urllib.error import URLError
import html as html_mod
import shutil, tempfile

DENY = [r"\brm\s+-[rf]{1,2}\b", r"\bdel\s+/[fq]\b", r"\brmdir\s+/s\b",
        r"(?:^|[;&|]\s*)format\b", r"\b(mkfs|diskpart)\b", r"\bdd\s+if=",
        r">\s*/dev/sd", r"\b(shutdown|reboot|poweroff)\b"]
HOME = str(Path.home())
SYS = platform.system()

def _guard(cmd):
    lo = cmd.strip().lower()
    for p in DENY:
        if re.search(p, lo):
            return 'Error: blocked by safety guard'
    return None

class ToolRegistry:
    def __init__(self):
        self._t = {}
        self._custom_file = os.path.join(HOME, '.autoto', 'custom_tools.json')

    def register(self, n, d, p, fn):
        self._t[n] = {'name': n, 'description': d, 'parameters': p, 'fn': fn}

    def get_schemas(self):
        return [{'type': 'function', 'function': {'name': t['name'],
                'description': t['description'], 'parameters': t['parameters']}}
                for t in self._t.values()]

    def execute(self, n, a):
        t = self._t.get(n)
        if not t:
            return 'Unknown tool: ' + n
        try:
            return t['fn'](**a)
        except Exception as e:
            return 'Error: ' + str(e)

    def load_custom_tools(self):
        if not os.path.exists(self._custom_file):
            return 0
        try:
            with open(self._custom_file, 'r', encoding='utf-8') as f:
                tools = json.load(f)
            count = 0
            for t in tools:
                name = t.get('name', '')
                if not name or name in self._t:
                    continue
                self._register_custom(t)
                count += 1
            return count
        except Exception as e:
            print(f'  \u26a0\ufe0f 載入自訂技能失敗: {e}')
            return 0

    def _register_custom(self, t):
        name = t['name']
        desc = t.get('description', name)
        cmd_template = t.get('command', '')
        params = t.get('params', [])
        props = {}
        required = []
        for p in params:
            pname = p.get('name', '')
            if not pname: continue
            props[pname] = {'type': 'string', 'description': p.get('description', pname)}
            if p.get('required', True): required.append(pname)
        schema = {'type': 'object', 'properties': props, 'required': required}
        def make_fn(tmpl):
            def fn(**kwargs):
                cmd = tmpl
                for k, v in kwargs.items():
                    cmd = cmd.replace('{' + k + '}', str(v))
                g = _guard(cmd)
                if g: return g
                try:
                    res = subprocess.run(cmd, shell=True, capture_output=True,
                                         text=True, timeout=60, cwd=HOME)
                    parts = []
                    if res.stdout: parts.append(res.stdout)
                    if res.stderr and res.stderr.strip(): parts.append('STDERR:\n' + res.stderr)
                    if res.returncode != 0: parts.append('\nExit code: ' + str(res.returncode))
                    out = '\n'.join(parts) if parts else '(completed, no output)'
                    return out[:10000]
                except subprocess.TimeoutExpired:
                    return 'Error: Command timed out (60s)'
            return fn
        self.register(name, desc, schema, make_fn(cmd_template))

    def get_custom_tools(self):
        if not os.path.exists(self._custom_file): return []
        try:
            with open(self._custom_file, 'r', encoding='utf-8') as f:
                return json.load(f)
        except: return []

    def save_custom_tool(self, tool_data):
        tools = self.get_custom_tools()
        found = False
        for i, t in enumerate(tools):
            if t['name'] == tool_data['name']:
                tools[i] = tool_data
                found = True
                break
        if not found: tools.append(tool_data)
        os.makedirs(os.path.dirname(self._custom_file), exist_ok=True)
        with open(self._custom_file, 'w', encoding='utf-8') as f:
            json.dump(tools, f, ensure_ascii=False, indent=2)
        self._register_custom(tool_data)
        return True

    def delete_custom_tool(self, name):
        tools = self.get_custom_tools()
        tools = [t for t in tools if t['name'] != name]
        with open(self._custom_file, 'w', encoding='utf-8') as f:
            json.dump(tools, f, ensure_ascii=False, indent=2)
        if name in self._t: del self._t[name]
        return True


# ========== Social API helpers ==========
_social_config_file = os.path.join(HOME, '.autoto', 'config.json')

def _load_social_config():
    try:
        with open(_social_config_file, 'r') as f:
            return json.load(f)
    except Exception:
        return {}

def _ig_token():
    """Read Instagram access token from config"""
    cfg = _load_social_config()
    return cfg.get('instagram', {}).get('accessToken', '') or cfg.get('ig_access_token', '')

def _fb_page_info():
    """Get Facebook Page ID and access token from config or via Graph API"""
    cfg = _load_social_config()
    fb = cfg.get('facebook', {})
    page_id = fb.get('pageId', '')
    page_token = fb.get('pageAccessToken', '')
    if page_id and page_token:
        return page_id, page_token, None
    token = _ig_token() or fb.get('accessToken', '')
    if not token:
        return None, None, 'Error: 請先在設定中填入 Facebook Access Token'
    try:
        url = f'https://graph.facebook.com/v19.0/me/accounts?access_token={token}'
        with urlopen(Request(url), timeout=10) as resp:
            pages = json.loads(resp.read().decode('utf-8'))
        for page in pages.get('data', []):
            return page['id'], page.get('access_token', token), None
        return None, None, 'Error: No Facebook Page found for this token'
    except Exception as e:
        return None, None, 'Error: ' + str(e)

def _x_token():
    """Read Twitter/X bearer token from config"""
    cfg = _load_social_config()
    return cfg.get('twitter', {}).get('bearerToken', '') or cfg.get('x_bearer_token', '')

def _x_oauth():
    """Read Twitter/X OAuth 1.0a credentials from config"""
    cfg = _load_social_config()
    tw = cfg.get('twitter', {})
    return {
        'consumer_key': tw.get('consumerKey', '') or tw.get('apiKey', ''),
        'consumer_secret': tw.get('consumerSecret', '') or tw.get('apiSecret', ''),
        'access_token': tw.get('accessToken', ''),
        'access_token_secret': tw.get('accessTokenSecret', ''),
    }

def _threads_token():
    """Read Threads access token from config"""
    cfg = _load_social_config()
    return cfg.get('threads', {}).get('accessToken', '') or cfg.get('threads_access_token', '')

def _threads_user_id(token):
    """Get Threads user ID"""
    try:
        url = f'https://graph.threads.net/v1.0/me?access_token={token}'
        with urlopen(Request(url), timeout=10) as resp:
            data = json.loads(resp.read().decode('utf-8'))
        return data.get('id', ''), None
    except Exception as e:
        return None, 'Error: ' + str(e)

def _ig_user_id(token):
    """Get Instagram Business Account ID via Facebook Page"""
    try:
        url = f'https://graph.facebook.com/v19.0/me/accounts?access_token={token}'
        req = Request(url)
        with urlopen(req, timeout=10) as resp:
            pages = json.loads(resp.read().decode('utf-8'))
        for page in pages.get('data', []):
            page_id = page['id']
            page_token = page.get('access_token', token)
            ig_url = f'https://graph.facebook.com/v19.0/{page_id}?fields=instagram_business_account&access_token={page_token}'
            req2 = Request(ig_url)
            with urlopen(req2, timeout=10) as resp2:
                ig_data = json.loads(resp2.read().decode('utf-8'))
            ig_account = ig_data.get('instagram_business_account', {}).get('id')
            if ig_account:
                return ig_account
        return 'Error: No Instagram Business Account found linked to your Facebook pages'
    except Exception as e:
        return 'Error: ' + str(e)

def _oauth1_sign(method, url, params, consumer_key, consumer_secret, token, token_secret):
    """Generate OAuth 1.0a signature for Twitter API"""
    import hmac, hashlib, base64, time as _time, uuid
    oauth_params = {
        'oauth_consumer_key': consumer_key,
        'oauth_nonce': uuid.uuid4().hex,
        'oauth_signature_method': 'HMAC-SHA1',
        'oauth_timestamp': str(int(_time.time())),
        'oauth_token': token,
        'oauth_version': '1.0',
    }
    all_params = {**params, **oauth_params}
    sorted_params = '&'.join(f'{quote_plus(k)}={quote_plus(str(v))}' for k, v in sorted(all_params.items()))
    base_string = f'{method.upper()}&{quote_plus(url)}&{quote_plus(sorted_params)}'
    signing_key = f'{quote_plus(consumer_secret)}&{quote_plus(token_secret)}'
    signature = base64.b64encode(hmac.new(signing_key.encode(), base_string.encode(), hashlib.sha1).digest()).decode()
    oauth_params['oauth_signature'] = signature
    auth_header = 'OAuth ' + ', '.join(f'{k}="{quote_plus(v)}"' for k, v in sorted(oauth_params.items()))
    return auth_header

def create_default_tools():
    r = ToolRegistry()

    # ========== 1. exec ==========
    def exec_cmd(command, working_dir=None):
        g = _guard(command)
        if g: return g
        try:
            res = subprocess.run(command, shell=True, capture_output=True,
                                 text=True, timeout=60, cwd=working_dir or HOME)
            parts = []
            if res.stdout: parts.append(res.stdout)
            if res.stderr and res.stderr.strip(): parts.append('STDERR:\n' + res.stderr)
            if res.returncode != 0: parts.append('\nExit code: ' + str(res.returncode))
            out = '\n'.join(parts) if parts else '(no output)'
            return out[:10000] + '\n...(truncated)' if len(out) > 10000 else out
        except subprocess.TimeoutExpired:
            return 'Error: Command timed out (60s)'
    r.register('exec', 'Execute a shell command on the computer.', {
        'type': 'object', 'properties': {
            'command': {'type': 'string', 'description': 'Shell command'},
            'working_dir': {'type': 'string', 'description': 'Working directory'}
        }, 'required': ['command']}, exec_cmd)

    # ========== 2. read_file ==========
    def read_file(path):
        p = os.path.expanduser(path)
        if not os.path.exists(p): return 'Error: File not found: ' + p
        with open(p, 'r', encoding='utf-8', errors='replace') as f:
            return f.read(100000)
    r.register('read_file', 'Read the contents of a file.', {
        'type': 'object', 'properties': {'path': {'type': 'string', 'description': 'File path'}},
        'required': ['path']}, read_file)

    # ========== 3. write_file ==========
    def write_file(path, content):
        p = os.path.expanduser(path)
        d = os.path.dirname(p)
        if d: os.makedirs(d, exist_ok=True)
        with open(p, 'w', encoding='utf-8') as f: f.write(content)
        return 'File written: ' + p
    r.register('write_file', 'Create or overwrite a file.', {
        'type': 'object', 'properties': {
            'path': {'type': 'string', 'description': 'File path'},
            'content': {'type': 'string', 'description': 'Content'}
        }, 'required': ['path', 'content']}, write_file)

    # ========== 4. edit_file ==========
    def edit_file(path, old_text, new_text):
        p = os.path.expanduser(path)
        if not os.path.exists(p): return 'Error: File not found: ' + p
        with open(p, 'r', encoding='utf-8') as f: c = f.read()
        if old_text not in c: return 'Error: old_text not found in file'
        with open(p, 'w', encoding='utf-8') as f: f.write(c.replace(old_text, new_text, 1))
        return 'File edited: ' + p
    r.register('edit_file', 'Edit a file by finding and replacing text.', {
        'type': 'object', 'properties': {
            'path': {'type': 'string', 'description': 'File path'},
            'old_text': {'type': 'string', 'description': 'Text to find'},
            'new_text': {'type': 'string', 'description': 'Replacement'}
        }, 'required': ['path', 'old_text', 'new_text']}, edit_file)

    # ========== 5. delete_file ==========
    def delete_file(path):
        p = os.path.expanduser(path)
        if not os.path.exists(p): return 'Error: File not found: ' + p
        if os.path.isdir(p): return 'Error: Is a directory: ' + p
        os.remove(p)
        return 'File deleted: ' + p
    r.register('delete_file', 'Delete a file (not directories).', {
        'type': 'object', 'properties': {'path': {'type': 'string', 'description': 'File path'}},
        'required': ['path']}, delete_file)

    # ========== 6. list_dir ==========
    def list_dir(path=None):
        p = os.path.expanduser(path or '~/Desktop')
        if not os.path.isdir(p): return 'Error: Not a directory: ' + p
        items = sorted(os.listdir(p))[:100]
        lines = []
        for i in items:
            prefix = 'D ' if os.path.isdir(os.path.join(p, i)) else '  '
            lines.append(prefix + i)
        return '\n'.join(lines) or '(empty)'
    r.register('list_dir', 'List files and folders in a directory.', {
        'type': 'object', 'properties': {'path': {'type': 'string', 'description': 'Directory path'}},
        'required': []}, list_dir)

    # ========== 7. open_url ==========
    def open_url(url):
        if not url.startswith('http'): url = 'https://' + url
        webbrowser.open(url)
        return 'Opened: ' + url
    r.register('open_url', 'Open a URL in the default browser.', {
        'type': 'object', 'properties': {'url': {'type': 'string', 'description': 'URL'}},
        'required': ['url']}, open_url)

    # ========== 8. open_app ==========
    def open_app(app_name):
        if SYS == 'Darwin': subprocess.Popen(['open', '-a', app_name])
        elif SYS == 'Windows': subprocess.Popen(['start', app_name], shell=True)
        else: subprocess.Popen([app_name])
        return 'Opened: ' + app_name
    r.register('open_app', 'Open an application.', {
        'type': 'object', 'properties': {'app_name': {'type': 'string', 'description': 'App name'}},
        'required': ['app_name']}, open_app)

    # ========== 9. screenshot ==========
    def screenshot():
        import time as _time
        ss_dir = os.path.join(HOME, '.autoto', 'screenshots')
        os.makedirs(ss_dir, exist_ok=True)
        fname = 'screenshot_' + datetime.now().strftime('%Y%m%d_%H%M%S') + '.png'
        s = os.path.join(ss_dir, fname)
        _time.sleep(0.5)
        if SYS == 'Darwin':
            subprocess.run(['screencapture', '-x', s], timeout=10)
        elif SYS == 'Windows':
            try:
                from PIL import ImageGrab
                img = ImageGrab.grab()
                img.save(s)
            except ImportError:
                # fallback: PowerShell
                ps = 'Add-Type -AssemblyName System.Windows.Forms;[System.Windows.Forms.Screen]::PrimaryScreen|ForEach-Object{$b=$_.Bounds;$bmp=New-Object System.Drawing.Bitmap($b.Width,$b.Height);$g=[System.Drawing.Graphics]::FromImage($bmp);$g.CopyFromScreen($b.Location,[System.Drawing.Point]::Empty,$b.Size);$bmp.Save(\"' + s.replace('\\', '\\\\') + '\")}'
                subprocess.run(['powershell', '-Command', ps], timeout=15)
        else:
            # Linux: try scrot or gnome-screenshot
            for cmd in [['scrot', s], ['gnome-screenshot', '-f', s]]:
                try:
                    subprocess.run(cmd, timeout=10)
                    break
                except FileNotFoundError:
                    continue
        if os.path.exists(s) and os.path.getsize(s) > 100:
            return 'SCREENSHOT:' + s
        return 'Error: screenshot failed (may need Pillow: pip install Pillow)'
    r.register('screenshot', 'Take a screenshot.', {
        'type': 'object', 'properties': {}, 'required': []}, screenshot)

    # ========== 10. type_text ==========
    def type_text(text, app_name=None, press_enter=True):
        if SYS == 'Windows':
            # Windows: use PowerShell to set clipboard and paste
            subprocess.run(['clip'], input=text, text=True, timeout=5)
            import time as _time
            if app_name:
                # Try to activate the app
                ps_activate = 'Add-Type -AssemblyName Microsoft.VisualBasic;' + \
                    'Start-Process "' + app_name + '" -ErrorAction SilentlyContinue;' + \
                    'Start-Sleep -Milliseconds 1000'
                subprocess.run(['powershell', '-Command', ps_activate], capture_output=True, timeout=10)
            _time.sleep(0.5)
            # Ctrl+V to paste
            ps_paste = 'Add-Type -AssemblyName System.Windows.Forms;[System.Windows.Forms.SendKeys]::SendWait("^v")'
            if press_enter:
                ps_paste += ';Start-Sleep -Milliseconds 300;[System.Windows.Forms.SendKeys]::SendWait("{ENTER}")'
            subprocess.run(['powershell', '-Command', ps_paste], capture_output=True, timeout=10)
            return 'Typed "' + text + '" in ' + (app_name or 'current app')
        elif SYS != 'Darwin':
            # Linux: use xdotool
            import time as _time
            if app_name:
                try:
                    subprocess.run(['xdotool', 'search', '--name', app_name, 'windowactivate'], capture_output=True, timeout=5)
                    _time.sleep(0.5)
                except FileNotFoundError:
                    return 'Error: xdotool not installed (sudo apt install xdotool)'
            try:
                subprocess.run(['xdotool', 'type', '--clearmodifiers', '--delay', '50', text], capture_output=True, timeout=30)
                if press_enter:
                    _time.sleep(0.3)
                    subprocess.run(['xdotool', 'key', 'Return'], capture_output=True, timeout=5)
                return 'Typed "' + text + '" in ' + (app_name or 'current app')
            except FileNotFoundError:
                return 'Error: xdotool not installed (sudo apt install xdotool)'
        # macOS: 先把文字放到剪貼簿
        subprocess.run(['pbcopy'], input=text, text=True, timeout=5)
        lines = []
        if app_name:
            # 啟動 app 並等待它到前景
            lines.append('tell application "' + app_name + '"')
            lines.append('  activate')
            lines.append('  delay 1.0')
            lines.append('end tell')
            # 再等一下確保視窗完全就緒
            lines.append('delay 0.5')
        # 用 System Events 貼上
        lines.append('tell application "System Events"')
        if app_name:
            # 確保焦點在目標 app 的視窗
            safe_name = app_name.replace('"', '\\"')
            lines.append('  tell process "' + safe_name + '"')
            lines.append('    set frontmost to true')
            lines.append('  end tell')
            lines.append('  delay 0.3')
        lines.append('  keystroke "v" using command down')
        if press_enter:
            lines.append('  delay 0.5')
            lines.append('  keystroke return')
        lines.append('end tell')
        try:
            script = '\n'.join(lines)
            res = subprocess.run(['osascript', '-e', script],
                capture_output=True, text=True, timeout=20)
            if res.returncode != 0:
                # 如果 process name 不對，嘗試不指定 process 的方式
                lines2 = []
                if app_name:
                    lines2.append('tell application "' + app_name + '" to activate')
                    lines2.append('delay 1.5')
                lines2.append('tell application "System Events"')
                lines2.append('  keystroke "v" using command down')
                if press_enter:
                    lines2.append('  delay 0.5')
                    lines2.append('  keystroke return')
                lines2.append('end tell')
                res2 = subprocess.run(['osascript', '-e', '\n'.join(lines2)],
                    capture_output=True, text=True, timeout=20)
                if res2.returncode != 0:
                    return 'Error: ' + (res2.stderr or res.stderr or 'failed')
            return 'Typed "' + text + '" in ' + (app_name or 'current app')
        except subprocess.TimeoutExpired:
            return 'Error: timed out'
    r.register('type_text', 'Type text into any app via clipboard paste (supports Chinese).', {
        'type': 'object', 'properties': {
            'text': {'type': 'string', 'description': 'Text to type'},
            'app_name': {'type': 'string', 'description': 'App to activate first'},
            'press_enter': {'type': 'boolean', 'description': 'Press Enter after? Default true'}
        }, 'required': ['text']}, type_text)

    # ========== 11. key_press ==========
    def key_press(key, modifiers=None):
        if SYS == 'Windows':
            # Windows: use PowerShell SendKeys
            key_map = {'return': '{ENTER}', 'enter': '{ENTER}', 'tab': '{TAB}',
                       'space': ' ', 'delete': '{DELETE}', 'escape': '{ESC}',
                       'up': '{UP}', 'down': '{DOWN}', 'left': '{LEFT}', 'right': '{RIGHT}',
                       'backspace': '{BACKSPACE}', 'home': '{HOME}', 'end': '{END}'}
            sk = key_map.get(key.lower(), key)
            if modifiers:
                for m in [x.strip().lower() for x in modifiers.split(',')]:
                    if m in ('command', 'cmd', 'control', 'ctrl'): sk = '^' + sk
                    elif m == 'shift': sk = '+' + sk
                    elif m in ('option', 'alt'): sk = '%' + sk
            ps = 'Add-Type -AssemblyName System.Windows.Forms;[System.Windows.Forms.SendKeys]::SendWait("' + sk + '")'
            res = subprocess.run(['powershell', '-Command', ps], capture_output=True, text=True, timeout=10)
            if res.returncode != 0: return 'Error: ' + (res.stderr or 'failed')
            return 'Pressed ' + key + (' with ' + modifiers if modifiers else '')
        elif SYS != 'Darwin':
            # Linux: use xdotool
            key_map = {'return': 'Return', 'enter': 'Return', 'tab': 'Tab',
                       'space': 'space', 'delete': 'Delete', 'escape': 'Escape',
                       'up': 'Up', 'down': 'Down', 'left': 'Left', 'right': 'Right',
                       'backspace': 'BackSpace', 'home': 'Home', 'end': 'End'}
            xk = key_map.get(key.lower(), key)
            if modifiers:
                parts = []
                for m in [x.strip().lower() for x in modifiers.split(',')]:
                    if m in ('command', 'cmd', 'control', 'ctrl'): parts.append('ctrl')
                    elif m == 'shift': parts.append('shift')
                    elif m in ('option', 'alt'): parts.append('alt')
                    elif m == 'super': parts.append('super')
                xk = '+'.join(parts + [xk])
            try:
                res = subprocess.run(['xdotool', 'key', '--clearmodifiers', xk], capture_output=True, text=True, timeout=10)
                if res.returncode != 0: return 'Error: ' + (res.stderr or 'failed')
                return 'Pressed ' + key + (' with ' + modifiers if modifiers else '')
            except FileNotFoundError:
                return 'Error: xdotool not installed (sudo apt install xdotool)'
        codes = {'return':36,'enter':36,'tab':48,'space':49,'delete':51,
                 'escape':53,'up':126,'down':125,'left':123,'right':124}
        lines = ['tell application "System Events"']
        code = codes.get(key.lower())
        mod_str = ''
        if modifiers:
            mp = []
            for m in [x.strip().lower() for x in modifiers.split(',')]:
                if m in ('command','cmd'): mp.append('command down')
                elif m == 'shift': mp.append('shift down')
                elif m in ('option','alt'): mp.append('option down')
                elif m in ('control','ctrl'): mp.append('control down')
            if mp: mod_str = ' using {' + ', '.join(mp) + '}'
        if code is not None: lines.append('  key code ' + str(code) + mod_str)
        else: lines.append('  keystroke "' + key + '"' + mod_str)
        lines.append('end tell')
        try:
            res = subprocess.run(['osascript', '-e', '\n'.join(lines)],
                capture_output=True, text=True, timeout=10)
            if res.returncode != 0: return 'Error: ' + (res.stderr or 'failed')
            return 'Pressed ' + key + (' with ' + modifiers if modifiers else '')
        except subprocess.TimeoutExpired:
            return 'Error: timed out'
    r.register('key_press', 'Press a key with optional modifiers (Cmd+C, etc).', {
        'type': 'object', 'properties': {
            'key': {'type': 'string', 'description': 'Key name or character'},
            'modifiers': {'type': 'string', 'description': 'command,shift,option,control'}
        }, 'required': ['key']}, key_press)

    # ========== 12. web_search ==========
    def web_search(query, num_results=5):
        try:
            url = 'https://html.duckduckgo.com/html/?q=' + quote_plus(query)
            req = Request(url, headers={'User-Agent': 'Mozilla/5.0'})
            with urlopen(req, timeout=15) as resp:
                body = resp.read().decode('utf-8', errors='replace')
            results = []
            for m in re.finditer(r'<a[^>]+class="result__a"[^>]*href="([^"]*)"[^>]*>(.*?)</a>', body):
                link = m.group(1)
                title = re.sub(r'<[^>]+>', '', m.group(2)).strip()
                if link and title:
                    results.append(title + '\n' + link)
                if len(results) >= num_results:
                    break
            if not results:
                for m in re.finditer(r'<a[^>]+href="(https?://[^"]+)"[^>]*>(.*?)</a>', body):
                    link = m.group(1)
                    title = re.sub(r'<[^>]+>', '', m.group(2)).strip()
                    if link and title and 'duckduckgo' not in link:
                        results.append(title + '\n' + link)
                    if len(results) >= num_results:
                        break
            return '\n\n'.join(results) if results else 'No results found for: ' + query
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('web_search', 'Search the web using DuckDuckGo and return results with titles and URLs.', {
        'type': 'object', 'properties': {
            'query': {'type': 'string', 'description': 'Search query'},
            'num_results': {'type': 'integer', 'description': 'Number of results (default 5)'}
        }, 'required': ['query']}, web_search)

    # ========== 13. web_fetch ==========
    def web_fetch(url, max_chars=8000):
        try:
            req = Request(url, headers={'User-Agent': 'Mozilla/5.0'})
            with urlopen(req, timeout=20) as resp:
                ct = resp.headers.get('Content-Type', '')
                if 'text' not in ct and 'json' not in ct and 'xml' not in ct:
                    return 'Error: Not a text page (Content-Type: ' + ct + ')'
                body = resp.read().decode('utf-8', errors='replace')
            body = re.sub(r'<script[^>]*>.*?</script>', '', body, flags=re.S)
            body = re.sub(r'<style[^>]*>.*?</style>', '', body, flags=re.S)
            body = re.sub(r'<[^>]+>', ' ', body)
            body = html_mod.unescape(body)
            body = re.sub(r'\s+', ' ', body).strip()
            if len(body) > max_chars:
                body = body[:max_chars] + '\n...(truncated)'
            return body if body else '(empty page)'
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('web_fetch', 'Fetch and extract text content from a URL (web scraping).', {
        'type': 'object', 'properties': {
            'url': {'type': 'string', 'description': 'URL to fetch'},
            'max_chars': {'type': 'integer', 'description': 'Max characters to return (default 8000)'}
        }, 'required': ['url']}, web_fetch)

    # ========== 14. clipboard_read ==========
    def clipboard_read():
        if SYS == 'Darwin':
            res = subprocess.run(['pbpaste'], capture_output=True, text=True, timeout=5)
        
            return res.stdout if res.stdout else '(clipboard is empty)'
        elif SYS == 'Windows':
            res = subprocess.run(['powershell', '-Command', 'Get-Clipboard'], capture_output=True, text=True, timeout=5)
            return res.stdout.strip() if res.stdout else '(clipboard is empty)'
        else:
            try:
                res = subprocess.run(['xclip', '-selection', 'clipboard', '-o'], capture_output=True, text=True, timeout=5)
                return res.stdout if res.stdout else '(clipboard is empty)'
            except FileNotFoundError:
                return 'Error: xclip not installed'
    r.register('clipboard_read', 'Read the current clipboard content.', {
        'type': 'object', 'properties': {}, 'required': []}, clipboard_read)

    # ========== 15. clipboard_write ==========
    def clipboard_write(text):
        if SYS == 'Darwin':
            subprocess.run(['pbcopy'], input=text, text=True, timeout=5)
        elif SYS == 'Windows':
            subprocess.run(['clip'], input=text, text=True, timeout=5)
        else:
            try:
                subprocess.run(['xclip', '-selection', 'clipboard'], input=text, text=True, timeout=5)
            except FileNotFoundError:
                return 'Error: xclip not installed'
        return 'Copied to clipboard (' + str(len(text)) + ' chars)'
    r.register('clipboard_write', 'Write text to the clipboard.', {
        'type': 'object', 'properties': {
            'text': {'type': 'string', 'description': 'Text to copy'}
        }, 'required': ['text']}, clipboard_write)

    # ========== 16. process_list ==========
    def process_list(filter_name=None):
        # Sanitize filter_name to prevent shell injection
        if filter_name:
            filter_name = re.sub(r'[^a-zA-Z0-9_.\- ]', '', filter_name)
        if SYS == 'Windows':
            cmd = ['tasklist']
            if filter_name:
                cmd = ['tasklist', '/FI', f'IMAGENAME eq *{filter_name}*']
            res = subprocess.run(cmd, capture_output=True, text=True, timeout=10)
        else:
            if filter_name:
                # Use pipes safely without shell=True
                ps = subprocess.run(['ps', 'aux'], capture_output=True, text=True, timeout=10)
                lines = ps.stdout.splitlines()
                header = lines[0] if lines else ''
                matched = [l for l in lines[1:] if filter_name.lower() in l.lower()]
                return header + '\n' + '\n'.join(matched) if matched else '(no matching processes)'
            else:
                res = subprocess.run(['ps', 'aux'], capture_output=True, text=True, timeout=10)
                lines = res.stdout.splitlines()[:20]
                return '\n'.join(lines) or '(no output)'
        return res.stdout or '(no output)'
    r.register('process_list', 'List running processes. Optionally filter by name.', {
        'type': 'object', 'properties': {
            'filter_name': {'type': 'string', 'description': 'Filter by process name'}
        }, 'required': []}, process_list)

    # ========== 17. process_kill ==========
    def process_kill(pid=None, name=None):
        if pid:
            try:
                if SYS == 'Windows':
                    subprocess.run(['taskkill', '/PID', str(pid), '/F'], capture_output=True, timeout=10)
                    return 'Killed PID ' + str(pid)
                else:
                    os.kill(int(pid), signal.SIGTERM)
                    return 'Sent SIGTERM to PID ' + str(pid)
            except ProcessLookupError:
                return 'Error: No process with PID ' + str(pid)
            except PermissionError:
                return 'Error: Permission denied for PID ' + str(pid)
        elif name:
            if SYS == 'Windows':
                res = subprocess.run(['taskkill', '/IM', name, '/F'], capture_output=True, text=True, timeout=10)
            else:
                res = subprocess.run(['pkill', '-f', name], capture_output=True, text=True, timeout=10)
            if res.returncode == 0: return 'Killed processes matching: ' + name
            return 'No processes found matching: ' + name
        return 'Error: provide pid or name'
    r.register('process_kill', 'Kill a process by PID or name.', {
        'type': 'object', 'properties': {
            'pid': {'type': 'integer', 'description': 'Process ID'},
            'name': {'type': 'string', 'description': 'Process name pattern'}
        }, 'required': []}, process_kill)

    # ========== 18. notification ==========
    def notification(title, message, sound=True):
        t = title.replace('"', '\\"')
        m = message.replace('"', '\\"')
        if SYS == 'Darwin':
            script = 'display notification "' + m + '" with title "' + t + '"'
            if sound: script += ' sound name "default"'
            res = subprocess.run(['osascript', '-e', script], capture_output=True, text=True, timeout=10)
            if res.returncode != 0: return 'Error: ' + (res.stderr or 'failed')
        elif SYS == 'Windows':
            ps = "[Windows.UI.Notifications.ToastNotificationManager,Windows.UI.Notifications,ContentType=WindowsRuntime]|Out-Null;$t=[Windows.UI.Notifications.ToastNotification]::new([Windows.Data.Xml.Dom.XmlDocument]::new());$x=$t.Content;$x.LoadXml('<toast><visual><binding template=\"ToastText02\"><text id=\"1\">" + t + "</text><text id=\"2\">" + m + "</text></binding></visual></toast>');[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('AutoTo').Show($t)"
            try:
                subprocess.run(['powershell', '-Command', ps], capture_output=True, text=True, timeout=10)
            except Exception:
                # Simpler fallback using msg command
                subprocess.run(['msg', '*', title + ': ' + message], capture_output=True, timeout=5)
        else:
            try:
                subprocess.run(['notify-send', title, message], timeout=5)
            except FileNotFoundError:
                return 'Error: notify-send not installed'
        return 'Notification sent: ' + title
    r.register('notification', 'Send a system notification (macOS/Windows/Linux).', {
        'type': 'object', 'properties': {
            'title': {'type': 'string', 'description': 'Notification title'},
            'message': {'type': 'string', 'description': 'Notification body'},
            'sound': {'type': 'boolean', 'description': 'Play sound? Default true'}
        }, 'required': ['title', 'message']}, notification)

    # ========== 19. cron_list ==========
    def cron_list():
        if SYS == 'Windows':
            res = subprocess.run(['schtasks', '/Query', '/FO', 'LIST'], capture_output=True, text=True, timeout=10)
        
            return res.stdout[:5000] if res.stdout else '(no scheduled tasks)'
        res = subprocess.run(['crontab', '-l'], capture_output=True, text=True, timeout=5)
        if res.returncode != 0: return '(no crontab)'
        return res.stdout or '(empty crontab)'
    r.register('cron_list', 'List current cron scheduled tasks.', {
        'type': 'object', 'properties': {}, 'required': []}, cron_list)

    # ========== 20. cron_add ==========
    def cron_add(schedule, command, task_name=None):
        if SYS == 'Windows':
            # Windows: use schtasks. schedule format: "DAILY /ST 09:00", "HOURLY", "MINUTE /MO 30", etc.
            tn = task_name or ('AutoTo_' + str(abs(hash(command)))[:8])
            cmd = ['schtasks', '/Create', '/TN', tn, '/TR', command, '/SC'] + schedule.split()
            cmd += ['/F']  # force overwrite if exists
            res = subprocess.run(cmd, capture_output=True, text=True, timeout=10)
            if res.returncode != 0: return 'Error: ' + (res.stderr or 'failed')
            return 'Scheduled task added: ' + tn + ' (' + schedule + ' ' + command + ')'
        existing = subprocess.run(['crontab', '-l'], capture_output=True, text=True, timeout=5)
        current = existing.stdout if existing.returncode == 0 else ''
        new_line = schedule + ' ' + command
        if new_line in current: return 'Already exists: ' + new_line
        updated = current.rstrip('\n') + '\n' + new_line + '\n'
        proc = subprocess.run(['crontab', '-'], input=updated, text=True,
            capture_output=True, timeout=5)
        if proc.returncode != 0: return 'Error: ' + (proc.stderr or 'failed')
        return 'Cron job added: ' + new_line
    r.register('cron_add', 'Add a scheduled task (cron on macOS/Linux, schtasks on Windows).', {
        'type': 'object', 'properties': {
            'schedule': {'type': 'string', 'description': 'Cron: "0 9 * * *". Windows: "DAILY /ST 09:00"'},
            'command': {'type': 'string', 'description': 'Command to run'},
            'task_name': {'type': 'string', 'description': 'Windows only: task name'}
        }, 'required': ['schedule', 'command']}, cron_add)

    # ========== 21. cron_remove ==========
    def cron_remove(pattern):
        if SYS == 'Windows':
            # Windows: delete scheduled task by name
            res = subprocess.run(['schtasks', '/Delete', '/TN', pattern, '/F'],
                capture_output=True, text=True, timeout=10)
            if res.returncode != 0: return 'Error: ' + (res.stderr or 'Task not found: ' + pattern)
            return 'Removed scheduled task: ' + pattern
        existing = subprocess.run(['crontab', '-l'], capture_output=True, text=True, timeout=5)
        if existing.returncode != 0: return '(no crontab)'
        lines = existing.stdout.strip().split('\n')
        filtered = [l for l in lines if pattern not in l]
        removed = len(lines) - len(filtered)
        if removed == 0: return 'No matching cron jobs found for: ' + pattern
        proc = subprocess.run(['crontab', '-'], input='\n'.join(filtered) + '\n',
            text=True, capture_output=True, timeout=5)
        if proc.returncode != 0: return 'Error: ' + (proc.stderr or 'failed')
        return 'Removed ' + str(removed) + ' cron job(s) matching: ' + pattern
    r.register('cron_remove', 'Remove a scheduled task by name (Windows) or pattern (macOS/Linux).', {
        'type': 'object', 'properties': {
            'pattern': {'type': 'string', 'description': 'Task name (Windows) or text pattern (macOS/Linux)'}
        }, 'required': ['pattern']}, cron_remove)

    # ========== 22. memory_search ==========
    def memory_search(query):
        mem_file = os.path.join(HOME, '.autoto', 'memories.json')
        if not os.path.exists(mem_file): return 'No memories found.'
        with open(mem_file, 'r', encoding='utf-8') as f:
            memories = json.load(f)
        q = query.lower()
        matches = [m for m in memories if q in m.get('content', '').lower()]
        if not matches: return 'No memories matching: ' + query
        return '\n'.join(['- ' + m['content'] + ' (' + m.get('timestamp', '') + ')' for m in matches[:10]])
    r.register('memory_search', 'Search through saved memories/notes.', {
        'type': 'object', 'properties': {
            'query': {'type': 'string', 'description': 'Search keyword'}
        }, 'required': ['query']}, memory_search)

    # ========== 23. system_info ==========
    def system_info():
        info = []
        info.append('OS: ' + platform.system() + ' ' + platform.release())
        info.append('Machine: ' + platform.machine())
        info.append('Python: ' + platform.python_version())
        info.append('Home: ' + HOME)
        info.append('User: ' + os.environ.get('USER', os.environ.get('USERNAME', 'unknown')))
        try:
            if SYS == 'Windows':
                disk = subprocess.run(['wmic', 'logicaldisk', 'get', 'size,freespace,caption'], capture_output=True, text=True, timeout=5)
            else:
                disk = subprocess.run(['df', '-h', '/'], capture_output=True, text=True, timeout=5)
            if disk.stdout: info.append('Disk:\n' + disk.stdout.strip())
        except: pass
        try:
            if SYS == 'Darwin':
                mem = subprocess.run(['vm_stat'], capture_output=True, text=True, timeout=5)
                if mem.stdout:
                    lines = mem.stdout.strip().split('\n')
                    info.append('Memory: ' + lines[0] if lines else '')
            elif SYS == 'Windows':
                mem = subprocess.run(['wmic', 'OS', 'get', 'TotalVisibleMemorySize,FreePhysicalMemory'], capture_output=True, text=True, timeout=5)
                if mem.stdout: info.append('Memory:\n' + mem.stdout.strip())
            else:
                mem = subprocess.run(['free', '-h'], capture_output=True, text=True, timeout=5)
                if mem.stdout: info.append('Memory:\n' + mem.stdout.strip())
        except: pass
        return '\n'.join(info)
    r.register('system_info', 'Get system information (OS, disk, memory, etc).', {
        'type': 'object', 'properties': {}, 'required': []}, system_info)

    # ========== 24. summarize ==========
    def summarize(text, max_sentences=3):
        sentences = re.split(r'[.!?\u3002\uff01\uff1f]+', text)
        sentences = [s.strip() for s in sentences if len(s.strip()) > 10]
        result = '. '.join(sentences[:max_sentences])
        return result + '.' if result else '(nothing to summarize)'
    r.register('summarize', 'Summarize text by extracting key sentences.', {
        'type': 'object', 'properties': {
            'text': {'type': 'string', 'description': 'Text to summarize'},
            'max_sentences': {'type': 'integer', 'description': 'Max sentences (default 3)'}
        }, 'required': ['text']}, summarize)

    # ========== 25. weather ==========
    def weather(city='Taipei'):
        try:
            url = 'https://wttr.in/' + quote_plus(city) + '?format=3&lang=zh-tw'
            req = Request(url, headers={'User-Agent': 'curl/7.0'})
            with urlopen(req, timeout=10) as resp:
                return resp.read().decode('utf-8').strip()
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('weather', 'Get current weather for a city.', {
        'type': 'object', 'properties': {
            'city': {'type': 'string', 'description': 'City name, e.g. Taipei, Tokyo'}
        }, 'required': []}, weather)

    # ========== 26. click ==========
    def click(x, y, button='left', clicks=1):
        """Click at screen coordinates"""
        if SYS == 'Darwin':
            # macOS: use cliclick if available, otherwise AppleScript + python
            try:
                # Try cliclick first (brew install cliclick)
                btn_map = {'left': 'c', 'right': 'rc', 'double': 'dc'}
                action = btn_map.get(button, 'c')
                if clicks == 2: action = 'dc'
                subprocess.run(['cliclick', action + ':' + str(x) + ',' + str(y)],
                    capture_output=True, timeout=5)
                return 'Clicked at (' + str(x) + ', ' + str(y) + ')'
            except FileNotFoundError:
                # Fallback: AppleScript with python mouse move
                script = """
tell application "System Events"
    do shell script "python3 -c \"
import Quartz
point = (%d, %d)
event = Quartz.CGEventCreateMouseEvent(None, Quartz.kCGEventMouseMoved, point, 0)
Quartz.CGEventPost(Quartz.kCGHIDEventTap, event)
import time; time.sleep(0.1)
event = Quartz.CGEventCreateMouseEvent(None, Quartz.kCGEventLeftMouseDown, point, 0)
Quartz.CGEventPost(Quartz.kCGHIDEventTap, event)
import time; time.sleep(0.05)
event = Quartz.CGEventCreateMouseEvent(None, Quartz.kCGEventLeftMouseUp, point, 0)
Quartz.CGEventPost(Quartz.kCGHIDEventTap, event)
\""
end tell""" % (int(x), int(y))
                res = subprocess.run(['osascript', '-e', script], capture_output=True, text=True, timeout=10)
                if res.returncode != 0: return 'Error: ' + (res.stderr or 'click failed. Install cliclick: brew install cliclick')
                return 'Clicked at (' + str(x) + ', ' + str(y) + ')'
        elif SYS == 'Windows':
            ps = 'Add-Type -AssemblyName System.Windows.Forms;[System.Windows.Forms.Cursor]::Position=New-Object System.Drawing.Point(' + str(x) + ',' + str(y) + ');'
            ps += '[System.Windows.Forms.SendKeys]::SendWait("")'  # ensure focus
            # Use mouse_event for click
            ps2 = """
Add-Type @'
using System;
using System.Runtime.InteropServices;
public class Mouse {
    [DllImport("user32.dll")] public static extern void SetCursorPos(int x, int y);
    [DllImport("user32.dll")] public static extern void mouse_event(uint f, int x, int y, uint d, int i);
    public static void Click(int x, int y) { SetCursorPos(x, y); mouse_event(2, 0, 0, 0, 0); mouse_event(4, 0, 0, 0, 0); }
}
'@
[Mouse]::Click(""" + str(x) + ',' + str(y) + ')'
            res = subprocess.run(['powershell', '-Command', ps2], capture_output=True, text=True, timeout=10)
            if res.returncode != 0: return 'Error: ' + (res.stderr or 'failed')
            return 'Clicked at (' + str(x) + ', ' + str(y) + ')'
        else:
            # Linux: xdotool
            try:
                subprocess.run(['xdotool', 'mousemove', str(x), str(y)], capture_output=True, timeout=5)
                subprocess.run(['xdotool', 'click', '1' if button == 'left' else '3'], capture_output=True, timeout=5)
                return 'Clicked at (' + str(x) + ', ' + str(y) + ')'
            except FileNotFoundError:
                return 'Error: xdotool not installed (sudo apt install xdotool)'
    r.register('click', 'Click at a specific screen coordinate (x, y).', {
        'type': 'object', 'properties': {
            'x': {'type': 'integer', 'description': 'X coordinate'},
            'y': {'type': 'integer', 'description': 'Y coordinate'},
            'button': {'type': 'string', 'description': 'left, right, or double (default: left)'},
            'clicks': {'type': 'integer', 'description': 'Number of clicks (default: 1)'}
        }, 'required': ['x', 'y']}, click)

    def move_mouse(x, y):
        if SYS == 'Darwin':
            try:
                res = subprocess.run(['cliclick', f'm:{int(x)},{int(y)}'], capture_output=True, text=True, timeout=5)
                if res.returncode != 0:
                    return 'Error: ' + (res.stderr or 'failed')
                return 'Moved mouse to (' + str(x) + ', ' + str(y) + ')'
            except FileNotFoundError:
                script = (
                    'import Quartz;'
                    f'point=({int(x)},{int(y)});'
                    'event=Quartz.CGEventCreateMouseEvent(None, Quartz.kCGEventMouseMoved, point, Quartz.kCGMouseButtonLeft);'
                    'Quartz.CGEventPost(Quartz.kCGHIDEventTap, event)'
                )
                res = subprocess.run(['python3', '-c', script], capture_output=True, text=True, timeout=10)
                if res.returncode != 0:
                    return 'Error: ' + (res.stderr or 'mouse move failed. Install cliclick: brew install cliclick')
                return 'Moved mouse to (' + str(x) + ', ' + str(y) + ')'
        elif SYS == 'Windows':
            ps = """
Add-Type @'
using System.Runtime.InteropServices;
public class Mouse {
    [DllImport(\"user32.dll\")] public static extern bool SetCursorPos(int x, int y);
}
'@
[Mouse]::SetCursorPos(""" + str(int(x)) + ',' + str(int(y)) + ") | Out-Null"
            res = subprocess.run(['powershell', '-Command', ps], capture_output=True, text=True, timeout=10)
            if res.returncode != 0:
                return 'Error: ' + (res.stderr or 'failed')
            return 'Moved mouse to (' + str(x) + ', ' + str(y) + ')'
        else:
            try:
                res = subprocess.run(['xdotool', 'mousemove', str(int(x)), str(int(y))], capture_output=True, text=True, timeout=5)
                if res.returncode != 0:
                    return 'Error: ' + (res.stderr or 'failed')
                return 'Moved mouse to (' + str(x) + ', ' + str(y) + ')'
            except FileNotFoundError:
                return 'Error: xdotool not installed (sudo apt install xdotool)'
    r.register('move_mouse', 'Move the mouse cursor to a specific screen coordinate.', {
        'type': 'object', 'properties': {
            'x': {'type': 'integer', 'description': 'X coordinate'},
            'y': {'type': 'integer', 'description': 'Y coordinate'}
        }, 'required': ['x', 'y']}, move_mouse)

    def drag_mouse(from_x, from_y, to_x, to_y, button='left'):
        if SYS == 'Darwin':
            try:
                btn = 'r' if button == 'right' else ''
                res = subprocess.run([
                    'cliclick',
                    f'dd:{int(from_x)},{int(from_y)}',
                    f'dm:{int(to_x)},{int(to_y)}',
                    f'du:{int(to_x)},{int(to_y)}'
                ], capture_output=True, text=True, timeout=10)
                if res.returncode != 0:
                    return 'Error: ' + (res.stderr or 'failed')
                return 'Dragged mouse from (' + str(from_x) + ', ' + str(from_y) + ') to (' + str(to_x) + ', ' + str(to_y) + ')'
            except FileNotFoundError:
                button_expr = 'Quartz.kCGMouseButtonRight' if button == 'right' else 'Quartz.kCGMouseButtonLeft'
                down_evt = 'Quartz.kCGEventRightMouseDown' if button == 'right' else 'Quartz.kCGEventLeftMouseDown'
                drag_evt = 'Quartz.kCGEventRightMouseDragged' if button == 'right' else 'Quartz.kCGEventLeftMouseDragged'
                up_evt = 'Quartz.kCGEventRightMouseUp' if button == 'right' else 'Quartz.kCGEventLeftMouseUp'
                script = (
                    'import Quartz,time;'
                    f'start=({int(from_x)},{int(from_y)});'
                    f'end=({int(to_x)},{int(to_y)});'
                    f'button={button_expr};'
                    f'down=Quartz.CGEventCreateMouseEvent(None,{down_evt},start,button);'
                    'Quartz.CGEventPost(Quartz.kCGHIDEventTap, down);'
                    'time.sleep(0.05);'
                    f'drag=Quartz.CGEventCreateMouseEvent(None,{drag_evt},end,button);'
                    'Quartz.CGEventPost(Quartz.kCGHIDEventTap, drag);'
                    'time.sleep(0.05);'
                    f'up=Quartz.CGEventCreateMouseEvent(None,{up_evt},end,button);'
                    'Quartz.CGEventPost(Quartz.kCGHIDEventTap, up)'
                )
                res = subprocess.run(['python3', '-c', script], capture_output=True, text=True, timeout=10)
                if res.returncode != 0:
                    return 'Error: ' + (res.stderr or 'drag failed. Install cliclick: brew install cliclick')
                return 'Dragged mouse from (' + str(from_x) + ', ' + str(from_y) + ') to (' + str(to_x) + ', ' + str(to_y) + ')'
        elif SYS == 'Windows':
            flag_down = 8 if button == 'right' else 2
            flag_up = 16 if button == 'right' else 4
            ps = """
Add-Type @'
using System;
using System.Runtime.InteropServices;
using System.Threading;
public class Mouse {
    [DllImport(\"user32.dll\")] public static extern bool SetCursorPos(int x, int y);
    [DllImport(\"user32.dll\")] public static extern void mouse_event(uint f, uint dx, uint dy, uint data, UIntPtr extraInfo);
    public static void Drag(int x1, int y1, int x2, int y2, uint down, uint up) {
        SetCursorPos(x1, y1);
        mouse_event(down, 0, 0, 0, UIntPtr.Zero);
        Thread.Sleep(80);
        SetCursorPos(x2, y2);
        Thread.Sleep(80);
        mouse_event(up, 0, 0, 0, UIntPtr.Zero);
    }
}
'@
[Mouse]::Drag(""" + ', '.join([str(int(from_x)), str(int(from_y)), str(int(to_x)), str(int(to_y)), str(flag_down), str(flag_up)]) + ")"
            res = subprocess.run(['powershell', '-Command', ps], capture_output=True, text=True, timeout=10)
            if res.returncode != 0:
                return 'Error: ' + (res.stderr or 'failed')
            return 'Dragged mouse from (' + str(from_x) + ', ' + str(from_y) + ') to (' + str(to_x) + ', ' + str(to_y) + ')'
        else:
            try:
                btn = '3' if button == 'right' else '1'
                subprocess.run(['xdotool', 'mousemove', str(int(from_x)), str(int(from_y))], capture_output=True, timeout=5)
                subprocess.run(['xdotool', 'mousedown', btn], capture_output=True, timeout=5)
                subprocess.run(['xdotool', 'mousemove', str(int(to_x)), str(int(to_y))], capture_output=True, timeout=5)
                subprocess.run(['xdotool', 'mouseup', btn], capture_output=True, timeout=5)
                return 'Dragged mouse from (' + str(from_x) + ', ' + str(from_y) + ') to (' + str(to_x) + ', ' + str(to_y) + ')'
            except FileNotFoundError:
                return 'Error: xdotool not installed (sudo apt install xdotool)'
    r.register('drag_mouse', 'Drag the mouse from one coordinate to another.', {
        'type': 'object', 'properties': {
            'from_x': {'type': 'integer', 'description': 'Start X coordinate'},
            'from_y': {'type': 'integer', 'description': 'Start Y coordinate'},
            'to_x': {'type': 'integer', 'description': 'Target X coordinate'},
            'to_y': {'type': 'integer', 'description': 'Target Y coordinate'},
            'button': {'type': 'string', 'description': 'left or right (default: left)'}
        }, 'required': ['from_x', 'from_y', 'to_x', 'to_y']}, drag_mouse)

    def scroll(amount=3, direction='down'):
        steps = max(1, abs(int(amount)))
        delta = steps if str(direction).lower() == 'up' else -steps
        if SYS == 'Darwin':
            script = (
                'import Quartz;'
                f'event=Quartz.CGEventCreateScrollWheelEvent(None, Quartz.kCGScrollEventUnitLine, 1, {delta});'
                'Quartz.CGEventPost(Quartz.kCGHIDEventTap, event)'
            )
            res = subprocess.run(['python3', '-c', script], capture_output=True, text=True, timeout=10)
            if res.returncode != 0:
                return 'Error: ' + (res.stderr or 'scroll failed')
            return 'Scrolled ' + str(direction) + ' by ' + str(steps)
        elif SYS == 'Windows':
            wheel_delta = 120 * delta
            ps = """
Add-Type @'
using System;
using System.Runtime.InteropServices;
public class Mouse {
    [DllImport(\"user32.dll\")] public static extern void mouse_event(uint f, uint dx, uint dy, int data, UIntPtr extraInfo);
    public static void Scroll(int delta) {
        mouse_event(0x0800, 0, 0, delta, UIntPtr.Zero);
    }
}
'@
[Mouse]::Scroll(""" + str(wheel_delta) + ")"
            res = subprocess.run(['powershell', '-Command', ps], capture_output=True, text=True, timeout=10)
            if res.returncode != 0:
                return 'Error: ' + (res.stderr or 'failed')
            return 'Scrolled ' + str(direction) + ' by ' + str(steps)
        else:
            try:
                btn = '4' if str(direction).lower() == 'up' else '5'
                for _ in range(steps):
                    subprocess.run(['xdotool', 'click', btn], capture_output=True, timeout=5)
                return 'Scrolled ' + str(direction) + ' by ' + str(steps)
            except FileNotFoundError:
                return 'Error: xdotool not installed (sudo apt install xdotool)'
    r.register('scroll', 'Scroll vertically on the current screen.', {
        'type': 'object', 'properties': {
            'amount': {'type': 'integer', 'description': 'Scroll steps (default 3)'},
            'direction': {'type': 'string', 'description': 'up or down (default: down)'}
        }, 'required': []}, scroll)

    def focus_app(app_name):
        if SYS == 'Darwin':
            script = 'tell application "' + app_name.replace('"', '\\"') + '" to activate'
            res = subprocess.run(['osascript', '-e', script], capture_output=True, text=True, timeout=10)
            if res.returncode != 0:
                return 'Error: ' + (res.stderr or 'failed')
            return 'Focused: ' + app_name
        elif SYS == 'Windows':
            safe_name = app_name.replace("'", "''")
            ps = (
                'Add-Type -AssemblyName Microsoft.VisualBasic;'
                f"$app='{safe_name}';"
                'if (-not [Microsoft.VisualBasic.Interaction]::AppActivate($app)) '
                '{ Start-Process $app -ErrorAction SilentlyContinue | Out-Null; Start-Sleep -Milliseconds 800; '
                '[Microsoft.VisualBasic.Interaction]::AppActivate($app) | Out-Null }'
            )
            res = subprocess.run(['powershell', '-Command', ps], capture_output=True, text=True, timeout=10)
            if res.returncode != 0:
                return 'Error: ' + (res.stderr or 'failed')
            return 'Focused: ' + app_name
        else:
            try:
                res = subprocess.run(['xdotool', 'search', '--name', app_name, 'windowactivate'], capture_output=True, text=True, timeout=10)
                if res.returncode != 0:
                    return 'Error: ' + (res.stderr or 'failed')
                return 'Focused: ' + app_name
            except FileNotFoundError:
                return 'Error: xdotool not installed (sudo apt install xdotool)'
    r.register('focus_app', 'Bring an application window to the foreground.', {
        'type': 'object', 'properties': {
            'app_name': {'type': 'string', 'description': 'Application or window name'}
        }, 'required': ['app_name']}, focus_app)

    def screen_size():
        if SYS == 'Darwin':
            res = subprocess.run(['osascript', '-e', 'tell application "Finder" to get bounds of window of desktop'], capture_output=True, text=True, timeout=10)
            if res.returncode == 0 and res.stdout.strip():
                parts = [p.strip() for p in res.stdout.strip().split(',')]
                if len(parts) == 4:
                    width = int(parts[2]) - int(parts[0])
                    height = int(parts[3]) - int(parts[1])
                    return json.dumps({'width': width, 'height': height})
            return 'Error: failed to read screen size'
        elif SYS == 'Windows':
            ps = 'Add-Type -AssemblyName System.Windows.Forms;$b=[System.Windows.Forms.Screen]::PrimaryScreen.Bounds;Write-Output ($b.Width.ToString()+\",\"+$b.Height.ToString())'
            res = subprocess.run(['powershell', '-Command', ps], capture_output=True, text=True, timeout=10)
            if res.returncode == 0 and res.stdout.strip():
                width, height = [int(x) for x in res.stdout.strip().split(',')[:2]]
                return json.dumps({'width': width, 'height': height})
            return 'Error: failed to read screen size'
        else:
            try:
                res = subprocess.run(['xdpyinfo'], capture_output=True, text=True, timeout=10)
                if res.returncode == 0:
                    m = re.search(r'dimensions:\s+(\d+)x(\d+)', res.stdout)
                    if m:
                        return json.dumps({'width': int(m.group(1)), 'height': int(m.group(2))})
                return 'Error: failed to read screen size'
            except FileNotFoundError:
                return 'Error: xdpyinfo not installed'
    r.register('screen_size', 'Get the primary screen width and height in pixels.', {
        'type': 'object', 'properties': {}, 'required': []}, screen_size)

    media_extensions = {'.mp4', '.mov', '.m4v', '.avi', '.mkv', '.webm', '.mp3', '.wav', '.m4a', '.aac'}

    def _require_binary(binary, install_hint):
        path = shutil.which(binary)
        if path:
            return path, None
        return None, f'Error: {binary} not installed. {install_hint}'

    def _probe_media_file(path):
        ffprobe_bin, err = _require_binary('ffprobe', 'Install with: brew install ffmpeg')
        if err:
            return None, err
        res = subprocess.run([
            ffprobe_bin,
            '-v', 'error',
            '-print_format', 'json',
            '-show_format',
            '-show_streams',
            path,
        ], capture_output=True, text=True, timeout=60)
        if res.returncode != 0:
            return None, 'Error: ' + ((res.stderr or res.stdout or 'ffprobe failed').strip())
        try:
            return json.loads(res.stdout), None
        except json.JSONDecodeError as e:
            return None, 'Error: invalid ffprobe output: ' + str(e)

    def scan_media_folder(folder_path, recursive=False):
        root = os.path.expanduser(folder_path)
        if not os.path.isdir(root):
            return 'Error: Not a directory: ' + root
        ffprobe_available = shutil.which('ffprobe') is not None
        items = []
        if recursive:
            walker = os.walk(root)
        else:
            walker = [(root, [], os.listdir(root))]
        for base, _, names in walker:
            for name in sorted(names):
                full = os.path.join(base, name)
                if not os.path.isfile(full):
                    continue
                ext = os.path.splitext(name)[1].lower()
                if ext not in media_extensions:
                    continue
                item = {
                    'path': full,
                    'size_bytes': os.path.getsize(full),
                }
                if ffprobe_available:
                    metadata, err = _probe_media_file(full)
                    if metadata:
                        item['duration'] = metadata.get('format', {}).get('duration')
                        video_stream = next((s for s in metadata.get('streams', []) if s.get('codec_type') == 'video'), None)
                        audio_stream = next((s for s in metadata.get('streams', []) if s.get('codec_type') == 'audio'), None)
                        if video_stream:
                            item['width'] = video_stream.get('width')
                            item['height'] = video_stream.get('height')
                            item['video_codec'] = video_stream.get('codec_name')
                        if audio_stream:
                            item['audio_codec'] = audio_stream.get('codec_name')
                    elif err:
                        item['probe_error'] = err
                items.append(item)
        return json.dumps({'folder': root, 'count': len(items), 'files': items[:200]}, ensure_ascii=False, indent=2)
    r.register('scan_media_folder', 'Scan a folder for video/audio files and return basic metadata.', {
        'type': 'object', 'properties': {
            'folder_path': {'type': 'string', 'description': 'Folder path containing media files'},
            'recursive': {'type': 'boolean', 'description': 'Whether to scan subfolders'}
        }, 'required': ['folder_path']}, scan_media_folder)

    def video_probe(path):
        media_path = os.path.expanduser(path)
        if not os.path.exists(media_path):
            return 'Error: File not found: ' + media_path
        metadata, err = _probe_media_file(media_path)
        if err:
            return err
        return json.dumps(metadata, ensure_ascii=False, indent=2)[:20000]
    r.register('video_probe', 'Inspect media metadata using ffprobe.', {
        'type': 'object', 'properties': {
            'path': {'type': 'string', 'description': 'Media file path'}
        }, 'required': ['path']}, video_probe)

    def video_cut(input_path, output_path, start_time, duration):
        ffmpeg_bin, err = _require_binary('ffmpeg', 'Install with: brew install ffmpeg')
        if err:
            return err
        src = os.path.expanduser(input_path)
        dst = os.path.expanduser(output_path)
        if not os.path.exists(src):
            return 'Error: File not found: ' + src
        out_dir = os.path.dirname(dst)
        if out_dir:
            os.makedirs(out_dir, exist_ok=True)
        res = subprocess.run([
            ffmpeg_bin,
            '-y',
            '-ss', str(start_time),
            '-i', src,
            '-t', str(duration),
            '-c:v', 'libx264',
            '-c:a', 'aac',
            '-movflags', '+faststart',
            dst,
        ], capture_output=True, text=True, timeout=1800)
        if res.returncode != 0:
            return 'Error: ' + ((res.stderr or res.stdout or 'ffmpeg failed').strip()[-2000:])
        return json.dumps({'output_path': dst, 'start_time': str(start_time), 'duration': str(duration)}, ensure_ascii=False)
    r.register('video_cut', 'Cut a media file to a specific time range.', {
        'type': 'object', 'properties': {
            'input_path': {'type': 'string', 'description': 'Source media file path'},
            'output_path': {'type': 'string', 'description': 'Output media file path'},
            'start_time': {'type': 'string', 'description': 'Start time such as 00:00:12 or 12.5'},
            'duration': {'type': 'string', 'description': 'Duration such as 30 or 00:00:30'}
        }, 'required': ['input_path', 'output_path', 'start_time', 'duration']}, video_cut)

    def video_concat(input_paths, output_path):
        ffmpeg_bin, err = _require_binary('ffmpeg', 'Install with: brew install ffmpeg')
        if err:
            return err
        paths = input_paths
        if isinstance(paths, str):
            paths = [p.strip() for p in re.split(r'[\n,]', paths) if p.strip()]
        if not isinstance(paths, list) or len(paths) < 2:
            return 'Error: input_paths must contain at least 2 files'
        resolved = []
        for p in paths:
            full = os.path.expanduser(p)
            if not os.path.exists(full):
                return 'Error: File not found: ' + full
            resolved.append(full)
        dst = os.path.expanduser(output_path)
        out_dir = os.path.dirname(dst)
        if out_dir:
            os.makedirs(out_dir, exist_ok=True)
        temp_path = None
        try:
            with tempfile.NamedTemporaryFile('w', delete=False, suffix='.txt', encoding='utf-8') as f:
                temp_path = f.name
                for p in resolved:
                    f.write("file '" + p.replace("'", "'\\''") + "'\n")
            res = subprocess.run([
                ffmpeg_bin,
                '-y',
                '-f', 'concat',
                '-safe', '0',
                '-i', temp_path,
                '-c:v', 'libx264',
                '-c:a', 'aac',
                '-movflags', '+faststart',
                dst,
            ], capture_output=True, text=True, timeout=3600)
            if res.returncode != 0:
                return 'Error: ' + ((res.stderr or res.stdout or 'ffmpeg concat failed').strip()[-2000:])
            return json.dumps({'output_path': dst, 'inputs': resolved}, ensure_ascii=False)
        finally:
            if temp_path and os.path.exists(temp_path):
                os.remove(temp_path)
    r.register('video_concat', 'Concatenate multiple media files into one output video.', {
        'type': 'object', 'properties': {
            'input_paths': {'type': 'array', 'items': {'type': 'string'}, 'description': 'List of media file paths'},
            'output_path': {'type': 'string', 'description': 'Output video file path'}
        }, 'required': ['input_paths', 'output_path']}, video_concat)

    def video_extract_audio(input_path, output_path):
        ffmpeg_bin, err = _require_binary('ffmpeg', 'Install with: brew install ffmpeg')
        if err:
            return err
        src = os.path.expanduser(input_path)
        dst = os.path.expanduser(output_path)
        if not os.path.exists(src):
            return 'Error: File not found: ' + src
        out_dir = os.path.dirname(dst)
        if out_dir:
            os.makedirs(out_dir, exist_ok=True)
        ext = os.path.splitext(dst)[1].lower()
        codec_args = ['-c:a', 'libmp3lame']
        if ext == '.wav':
            codec_args = ['-c:a', 'pcm_s16le']
        elif ext in ('.m4a', '.aac'):
            codec_args = ['-c:a', 'aac']
        res = subprocess.run([
            ffmpeg_bin,
            '-y',
            '-i', src,
            '-vn',
            *codec_args,
            dst,
        ], capture_output=True, text=True, timeout=1800)
        if res.returncode != 0:
            return 'Error: ' + ((res.stderr or res.stdout or 'audio extraction failed').strip()[-2000:])
        return json.dumps({'output_path': dst, 'source': src}, ensure_ascii=False)
    r.register('video_extract_audio', 'Extract audio from a media file.', {
        'type': 'object', 'properties': {
            'input_path': {'type': 'string', 'description': 'Source media file path'},
            'output_path': {'type': 'string', 'description': 'Output audio file path'}
        }, 'required': ['input_path', 'output_path']}, video_extract_audio)

    def transcribe_media(input_path, output_dir=None, model='base', language='zh'):
        src = os.path.expanduser(input_path)
        if not os.path.exists(src):
            return 'Error: File not found: ' + src
        out_dir = os.path.expanduser(output_dir or os.path.join(HOME, '.autoto', 'transcripts'))
        os.makedirs(out_dir, exist_ok=True)
        whisper_bin = shutil.which('whisper')
        if whisper_bin:
            cmd = [whisper_bin]
        else:
            cmd = ['python3', '-m', 'whisper']
        cmd += [
            src,
            '--model', str(model),
            '--language', str(language),
            '--task', 'transcribe',
            '--output_format', 'all',
            '--output_dir', out_dir,
            '--fp16', 'False',
        ]
        res = subprocess.run(cmd, capture_output=True, text=True, timeout=7200)
        if res.returncode != 0:
            stderr = (res.stderr or res.stdout or '').strip()
            if 'No module named whisper' in stderr:
                return 'Error: whisper not installed. Install with: python3 -m pip install --user openai-whisper'
            return 'Error: ' + (stderr[-2000:] if stderr else 'whisper failed')
        base = os.path.splitext(os.path.basename(src))[0]
        files = []
        for ext in ('.txt', '.srt', '.vtt', '.tsv', '.json'):
            candidate = os.path.join(out_dir, base + ext)
            if os.path.exists(candidate):
                files.append(candidate)
        return json.dumps({'output_dir': out_dir, 'files': files}, ensure_ascii=False, indent=2)
    r.register('transcribe_media', 'Transcribe a media file into text and subtitle formats using Whisper.', {
        'type': 'object', 'properties': {
            'input_path': {'type': 'string', 'description': 'Source media file path'},
            'output_dir': {'type': 'string', 'description': 'Folder for transcript outputs'},
            'model': {'type': 'string', 'description': 'Whisper model name such as tiny, base, small, medium'},
            'language': {'type': 'string', 'description': 'Language code such as zh, en, ja'}
        }, 'required': ['input_path']}, transcribe_media)

    # ========== 27. youtube_play ==========
    def youtube_play(query):
        """Search YouTube and open the top result (skip Shorts)"""
        try:
            search_url = 'https://www.youtube.com/results?search_query=' + quote_plus(query)
            req = Request(search_url, headers={
                'User-Agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36',
                'Accept-Language': 'zh-TW,zh;q=0.9,en;q=0.8'
            })
            with urlopen(req, timeout=15) as resp:
                body = resp.read().decode('utf-8', errors='replace')
            # Method 1: Parse ytInitialData JSON for accurate results
            m = re.search(r'var ytInitialData = (\{.*?\});', body)
            if m:
                data = json.loads(m.group(1))
                try:
                    contents = data['contents']['twoColumnSearchResultsRenderer']['primaryContents']['sectionListRenderer']['contents']
                    for section in contents:
                        items = section.get('itemSectionRenderer', {}).get('contents', [])
                        for item in items:
                            if 'videoRenderer' in item:
                                vr = item['videoRenderer']
                                vid = vr.get('videoId', '')
                                # Skip if no lengthText (likely a live/premiere) — but still allow
                                if vid:
                                    video_url = 'https://www.youtube.com/watch?v=' + vid
                                    title = vr.get('title', {}).get('runs', [{}])[0].get('text', '')
                                    webbrowser.open(video_url)
                                    return 'Playing: ' + title + ' (' + video_url + ')'
                except (KeyError, IndexError):
                    pass
            # Method 2: Regex fallback — filter out Shorts
            shorts_ids = set(re.findall(r'/shorts/([a-zA-Z0-9_-]{11})', body))
            video_ids = re.findall(r'"videoId":"([a-zA-Z0-9_-]{11})"', body)
            seen = set()
            for vid in video_ids:
                if vid not in seen and vid not in shorts_ids:
                    seen.add(vid)
                    video_url = 'https://www.youtube.com/watch?v=' + vid
                    webbrowser.open(video_url)
                    return 'Playing: ' + video_url
            # All results were Shorts or empty — open search page
            webbrowser.open(search_url)
            return 'No regular video found, opened YouTube search for: ' + query
        except Exception as e:
            webbrowser.open('https://www.youtube.com/results?search_query=' + quote_plus(query))
            return 'Opened YouTube search (direct play failed: ' + str(e) + ')'
    r.register('youtube_play', 'Search YouTube and play the top result video directly (skips Shorts).', {
        'type': 'object', 'properties': {
            'query': {'type': 'string', 'description': 'Search query, e.g. "Closer The Chainsmokers" or "末班車"'}
        }, 'required': ['query']}, youtube_play)


    # ========== 28. ig_get_posts ==========
    def ig_get_posts(limit=10):
        """Get recent Instagram posts with stats"""
        token = _ig_token()
        if not token: return 'Error: 請先在設定中填入 Instagram Access Token'
        uid = _ig_user_id(token)
        if uid.startswith('Error'): return uid
        try:
            url = f'https://graph.facebook.com/v19.0/{uid}/media?fields=id,caption,timestamp,like_count,comments_count,media_type,permalink&limit={limit}&access_token={token}'
            req = Request(url)
            with urlopen(req, timeout=15) as resp:
                data = json.loads(resp.read().decode('utf-8'))
            posts = data.get('data', [])
            if not posts: return 'No posts found.'
            lines = []
            for p in posts:
                cap = (p.get('caption') or '(no caption)')[:60]
                lines.append(f"ID: {p['id']} | {p.get('media_type','?')} | ❤️{p.get('like_count',0)} 💬{p.get('comments_count',0)} | {cap} | {p.get('timestamp','')}")
            return '\n'.join(lines)
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('ig_get_posts', 'List recent Instagram posts with likes/comments count.', {
        'type': 'object', 'properties': {
            'limit': {'type': 'integer', 'description': 'Number of posts (default 10)'}
        }, 'required': []}, ig_get_posts)

    # ========== 29. ig_get_comments ==========
    def ig_get_comments(post_id, limit=20):
        """Get comments on an Instagram post"""
        token = _ig_token()
        if not token: return 'Error: 請先在設定中填入 Instagram Access Token'
        try:
            url = f'https://graph.facebook.com/v19.0/{post_id}/comments?fields=id,text,username,timestamp,replies{{id,text,username,timestamp}}&limit={limit}&access_token={token}'
            req = Request(url)
            with urlopen(req, timeout=15) as resp:
                data = json.loads(resp.read().decode('utf-8'))
            comments = data.get('data', [])
            if not comments: return 'No comments on this post.'
            lines = []
            for c in comments:
                lines.append(f"[{c.get('username','?')}] {c['text']} (ID: {c['id']}, {c.get('timestamp','')})")
                for r2 in c.get('replies', {}).get('data', []):
                    lines.append(f"  ↳ [{r2.get('username','?')}] {r2['text']} (ID: {r2['id']})")
            return '\n'.join(lines)
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('ig_get_comments', 'Get comments (with replies) on an Instagram post.', {
        'type': 'object', 'properties': {
            'post_id': {'type': 'string', 'description': 'Instagram post/media ID'},
            'limit': {'type': 'integer', 'description': 'Max comments (default 20)'}
        }, 'required': ['post_id']}, ig_get_comments)

    # ========== 30. ig_reply_comment ==========
    def ig_reply_comment(comment_id, message):
        """Reply to a specific Instagram comment"""
        token = _ig_token()
        if not token: return 'Error: 請先在設定中填入 Instagram Access Token'
        try:
            url = f'https://graph.facebook.com/v19.0/{comment_id}/replies'
            data = json.dumps({'message': message, 'access_token': token}).encode('utf-8')
            req = Request(url, data=data, headers={'Content-Type': 'application/json'}, method='POST')
            with urlopen(req, timeout=15) as resp:
                result = json.loads(resp.read().decode('utf-8'))
            return 'Reply posted (ID: ' + result.get('id', '?') + ')'
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('ig_reply_comment', 'Reply to a specific Instagram comment.', {
        'type': 'object', 'properties': {
            'comment_id': {'type': 'string', 'description': 'Comment ID to reply to'},
            'message': {'type': 'string', 'description': 'Reply text'}
        }, 'required': ['comment_id', 'message']}, ig_reply_comment)

    # ========== 31. ig_post_comment ==========
    def ig_post_comment(post_id, message):
        """Post a new comment on an Instagram post"""
        token = _ig_token()
        if not token: return 'Error: 請先在設定中填入 Instagram Access Token'
        try:
            url = f'https://graph.facebook.com/v19.0/{post_id}/comments'
            data = json.dumps({'message': message, 'access_token': token}).encode('utf-8')
            req = Request(url, data=data, headers={'Content-Type': 'application/json'}, method='POST')
            with urlopen(req, timeout=15) as resp:
                result = json.loads(resp.read().decode('utf-8'))
            return 'Comment posted (ID: ' + result.get('id', '?') + ')'
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('ig_post_comment', 'Post a new comment on an Instagram post.', {
        'type': 'object', 'properties': {
            'post_id': {'type': 'string', 'description': 'Instagram post/media ID'},
            'message': {'type': 'string', 'description': 'Comment text'}
        }, 'required': ['post_id', 'message']}, ig_post_comment)

    # ========== 32. ig_delete_comment ==========
    def ig_delete_comment(comment_id):
        """Delete an Instagram comment"""
        token = _ig_token()
        if not token: return 'Error: 請先在設定中填入 Instagram Access Token'
        try:
            url = f'https://graph.facebook.com/v19.0/{comment_id}?access_token={token}'
            req = Request(url, method='DELETE')
            with urlopen(req, timeout=15) as resp:
                result = json.loads(resp.read().decode('utf-8'))
            if result.get('success'): return 'Comment deleted: ' + comment_id
            return 'Delete may have failed: ' + json.dumps(result)
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('ig_delete_comment', 'Delete an Instagram comment (must be your own or on your post).', {
        'type': 'object', 'properties': {
            'comment_id': {'type': 'string', 'description': 'Comment ID to delete'}
        }, 'required': ['comment_id']}, ig_delete_comment)

    # ========== ig_get_commenters — 取得留言者清單 ==========
    def ig_get_commenters(post_id, limit=50):
        """Get list of users who commented on a post, with their user IDs for DM."""
        token = _ig_token()
        if not token: return 'Error: 請先在設定中填入 Instagram Access Token'
        try:
            url = f'https://graph.facebook.com/v19.0/{post_id}/comments?fields=id,text,username,from{{id,username}}&limit={limit}&access_token={token}'
            req = Request(url)
            with urlopen(req, timeout=15) as resp:
                data = json.loads(resp.read().decode('utf-8'))
            comments = data.get('data', [])
            if not comments: return 'No comments on this post.'
            seen = {}
            for c in comments:
                frm = c.get('from', {})
                uid = frm.get('id', '')
                uname = frm.get('username', c.get('username', '?'))
                if uid and uid not in seen:
                    seen[uid] = {'username': uname, 'user_id': uid, 'comment': c.get('text', '')[:60], 'comment_id': c['id']}
            lines = []
            for uid, info in seen.items():
                lines.append(f"@{info['username']} (ID: {info['user_id']}) — \"{info['comment']}\"")
            return f'Found {len(seen)} unique commenters:\n' + '\n'.join(lines)
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('ig_get_commenters', 'Get list of unique users who commented on an Instagram post, with user IDs for sending DMs.', {
        'type': 'object', 'properties': {
            'post_id': {'type': 'string', 'description': 'Instagram post/media ID'},
            'limit': {'type': 'integer', 'description': 'Max comments to scan (default 50)'}
        }, 'required': ['post_id']}, ig_get_commenters)

    # ========== ig_send_dm — 發送 IG 私訊 ==========
    def ig_send_dm(recipient_id, message):
        """Send a direct message to an Instagram user (must have interacted within 24h)."""
        token = _ig_token()
        if not token: return 'Error: 請先在設定中填入 Instagram Access Token'
        uid = _ig_user_id(token)
        if isinstance(uid, str) and uid.startswith('Error'): return uid
        try:
            url = f'https://graph.facebook.com/v19.0/{uid}/messages'
            payload = json.dumps({
                'recipient': {'id': recipient_id},
                'message': {'text': message},
                'access_token': token
            }).encode('utf-8')
            req = Request(url, data=payload, headers={'Content-Type': 'application/json'}, method='POST')
            with urlopen(req, timeout=15) as resp:
                result = json.loads(resp.read().decode('utf-8'))
            if result.get('message_id') or result.get('recipient_id'):
                return f'DM sent to user {recipient_id}'
            return 'DM may have failed: ' + json.dumps(result, ensure_ascii=False)
        except Exception as e:
            err = str(e)
            if '551' in err or 'permission' in err.lower():
                return 'Error: 需要 instagram_manage_messages 權限。請在 Facebook 開發者後台啟用此權限。'
            if '10' in err and 'within' in err.lower():
                return 'Error: 只能私訊在 24 小時內跟你互動過的用戶。'
            return 'Error: ' + err
    r.register('ig_send_dm', 'Send a direct message to an Instagram user. User must have interacted with you in the last 24 hours (e.g. commented on your post).', {
        'type': 'object', 'properties': {
            'recipient_id': {'type': 'string', 'description': 'Instagram user ID (from ig_get_commenters)'},
            'message': {'type': 'string', 'description': 'Message text to send'}
        }, 'required': ['recipient_id', 'message']}, ig_send_dm)

    # ========== ig_auto_dm — 自動群發私訊給留言者 ==========
    def ig_auto_dm(post_id, message, limit=50, delay_seconds=2):
        """Auto-send DM to all users who commented on a post."""
        token = _ig_token()
        if not token: return 'Error: 請先在設定中填入 Instagram Access Token'
        uid = _ig_user_id(token)
        if isinstance(uid, str) and uid.startswith('Error'): return uid
        try:
            # Step 1: Get commenters
            url = f'https://graph.facebook.com/v19.0/{post_id}/comments?fields=from{{id,username}}&limit={limit}&access_token={token}'
            req = Request(url)
            with urlopen(req, timeout=15) as resp:
                data = json.loads(resp.read().decode('utf-8'))
            comments = data.get('data', [])
            if not comments: return 'No comments found on this post.'

            seen = {}
            for c in comments:
                frm = c.get('from', {})
                rid = frm.get('id', '')
                uname = frm.get('username', '?')
                if rid and rid not in seen:
                    seen[rid] = uname

            if not seen: return 'No commenters with user IDs found.'

            # Step 2: Send DMs
            import time
            success = []
            failed = []
            dm_url = f'https://graph.facebook.com/v19.0/{uid}/messages'
            for rid, uname in seen.items():
                try:
                    payload = json.dumps({
                        'recipient': {'id': rid},
                        'message': {'text': message},
                        'access_token': token
                    }).encode('utf-8')
                    dm_req = Request(dm_url, data=payload, headers={'Content-Type': 'application/json'}, method='POST')
                    with urlopen(dm_req, timeout=15) as resp:
                        result = json.loads(resp.read().decode('utf-8'))
                    if result.get('message_id') or result.get('recipient_id'):
                        success.append(f'@{uname}')
                    else:
                        failed.append(f'@{uname}: unknown response')
                except Exception as e:
                    failed.append(f'@{uname}: {str(e)[:50]}')
                if float(delay_seconds) > 0:
                    time.sleep(float(delay_seconds))

            lines = [f'Auto DM complete: {len(success)} sent, {len(failed)} failed']
            if success: lines.append('Sent to: ' + ', '.join(success))
            if failed: lines.append('Failed: ' + ', '.join(failed))
            return '\n'.join(lines)
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('ig_auto_dm', 'Auto-send a direct message to all users who commented on an Instagram post.', {
        'type': 'object', 'properties': {
            'post_id': {'type': 'string', 'description': 'Instagram post/media ID'},
            'message': {'type': 'string', 'description': 'DM message text to send to each commenter'},
            'limit': {'type': 'integer', 'description': 'Max comments to scan (default 50)'},
            'delay_seconds': {'type': 'number', 'description': 'Delay between each DM in seconds (default 2, to avoid rate limit)'}
        }, 'required': ['post_id', 'message']}, ig_auto_dm)

    # ========== 33. ig_publish_media ==========
    def ig_publish_media(media_url, caption='', media_type='IMAGE'):
        """Publish a photo, video, or reel to Instagram via Graph API.
        media_url must be a publicly accessible URL."""
        token = _ig_token()
        if not token: return 'Error: 請先在設定中填入 Instagram Access Token'
        uid = _ig_user_id(token)
        if isinstance(uid, str) and uid.startswith('Error'): return uid
        mtype = media_type.upper()
        try:
            # Step 1: Create media container
            params = {
                'caption': caption,
                'access_token': token,
            }
            if mtype == 'IMAGE':
                params['image_url'] = media_url
            elif mtype in ('VIDEO', 'REELS'):
                params['video_url'] = media_url
                params['media_type'] = mtype
            else:
                return 'Error: media_type must be IMAGE, VIDEO, or REELS'
            create_url = f'https://graph.facebook.com/v19.0/{uid}/media'
            payload = json.dumps(params).encode('utf-8')
            req = Request(create_url, data=payload, headers={'Content-Type': 'application/json'}, method='POST')
            with urlopen(req, timeout=30) as resp:
                create_data = json.loads(resp.read().decode('utf-8'))
            container_id = create_data.get('id')
            if not container_id:
                return 'Error: Failed to create media container: ' + json.dumps(create_data, ensure_ascii=False)

            # Step 2: For video/reels, wait for processing
            if mtype in ('VIDEO', 'REELS'):
                import time as _time
                for _ in range(60):
                    status_url = f'https://graph.facebook.com/v19.0/{container_id}?fields=status_code&access_token={token}'
                    with urlopen(Request(status_url), timeout=10) as resp:
                        st = json.loads(resp.read().decode('utf-8'))
                    code = st.get('status_code', '')
                    if code == 'FINISHED':
                        break
                    elif code == 'ERROR':
                        return 'Error: Media processing failed: ' + json.dumps(st, ensure_ascii=False)
                    _time.sleep(5)
                else:
                    return 'Error: Media processing timed out (5 min)'

            # Step 3: Publish
            pub_url = f'https://graph.facebook.com/v19.0/{uid}/media_publish'
            pub_payload = json.dumps({'creation_id': container_id, 'access_token': token}).encode('utf-8')
            req2 = Request(pub_url, data=pub_payload, headers={'Content-Type': 'application/json'}, method='POST')
            with urlopen(req2, timeout=30) as resp:
                pub_data = json.loads(resp.read().decode('utf-8'))
            pub_id = pub_data.get('id', '?')
            return json.dumps({'success': True, 'media_id': pub_id, 'media_type': mtype}, ensure_ascii=False)
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('ig_publish_media', 'Publish a photo, video, or reel to Instagram. media_url must be a publicly accessible URL.', {
        'type': 'object', 'properties': {
            'media_url': {'type': 'string', 'description': 'Publicly accessible URL of the image or video'},
            'caption': {'type': 'string', 'description': 'Post caption text'},
            'media_type': {'type': 'string', 'description': 'IMAGE, VIDEO, or REELS (default IMAGE)'}
        }, 'required': ['media_url']}, ig_publish_media)

    # ========== 34. web_scrape_structured ==========
    def web_scrape_structured(url, extract='all', max_items=50):
        """Scrape a web page and return structured data: links, images, headings, or all."""
        try:
            req = Request(url, headers={'User-Agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36'})
            with urlopen(req, timeout=20) as resp:
                ct = resp.headers.get('Content-Type', '')
                if 'text' not in ct and 'html' not in ct and 'xml' not in ct:
                    return 'Error: Not an HTML page (Content-Type: ' + ct + ')'
                body = resp.read().decode('utf-8', errors='replace')
            result = {}
            what = extract.lower()
            if what in ('all', 'links'):
                links = []
                for m in re.finditer(r'<a[^>]+href="(https?://[^"]+)"[^>]*>(.*?)</a>', body, re.S):
                    text = re.sub(r'<[^>]+>', '', m.group(2)).strip()
                    links.append({'url': m.group(1), 'text': text[:200]})
                    if len(links) >= max_items: break
                result['links'] = links
            if what in ('all', 'images'):
                images = []
                for m in re.finditer(r'<img[^>]+src="(https?://[^"]+)"[^>]*>', body):
                    alt = ''
                    alt_m = re.search(r'alt="([^"]*)"', m.group(0))
                    if alt_m: alt = alt_m.group(1)
                    images.append({'src': m.group(1), 'alt': alt[:200]})
                    if len(images) >= max_items: break
                result['images'] = images
            if what in ('all', 'headings'):
                headings = []
                for m in re.finditer(r'<(h[1-6])[^>]*>(.*?)</\1>', body, re.S):
                    text = re.sub(r'<[^>]+>', '', m.group(2)).strip()
                    if text:
                        headings.append({'level': m.group(1), 'text': text[:300]})
                    if len(headings) >= max_items: break
                result['headings'] = headings
            if what in ('all', 'meta'):
                meta = {}
                title_m = re.search(r'<title[^>]*>(.*?)</title>', body, re.S)
                if title_m: meta['title'] = html_mod.unescape(title_m.group(1).strip())[:300]
                for m in re.finditer(r'<meta[^>]+>', body):
                    tag = m.group(0)
                    name_m = re.search(r'(?:name|property)="([^"]+)"', tag)
                    content_m = re.search(r'content="([^"]*)"', tag)
                    if name_m and content_m:
                        meta[name_m.group(1)] = html_mod.unescape(content_m.group(1))[:500]
                result['meta'] = meta
            return json.dumps(result, ensure_ascii=False, indent=2)[:20000]
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('web_scrape_structured', 'Scrape a web page and extract structured data: links, images, headings, meta.', {
        'type': 'object', 'properties': {
            'url': {'type': 'string', 'description': 'URL to scrape'},
            'extract': {'type': 'string', 'description': 'What to extract: all, links, images, headings, meta (default all)'},
            'max_items': {'type': 'integer', 'description': 'Max items per category (default 50)'}
        }, 'required': ['url']}, web_scrape_structured)

    # ========== 35. web_download_file ==========
    def web_download_file(url, output_path):
        """Download a file from a URL to a local path."""
        try:
            dst = os.path.expanduser(output_path)
            out_dir = os.path.dirname(dst)
            if out_dir:
                os.makedirs(out_dir, exist_ok=True)
            req = Request(url, headers={'User-Agent': 'Mozilla/5.0'})
            with urlopen(req, timeout=120) as resp:
                total = int(resp.headers.get('Content-Length', 0))
                with open(dst, 'wb') as f:
                    downloaded = 0
                    while True:
                        chunk = resp.read(65536)
                        if not chunk:
                            break
                        f.write(chunk)
                        downloaded += len(chunk)
            size = os.path.getsize(dst)
            return json.dumps({'output_path': dst, 'size_bytes': size, 'url': url}, ensure_ascii=False)
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('web_download_file', 'Download a file from a URL to a local path.', {
        'type': 'object', 'properties': {
            'url': {'type': 'string', 'description': 'URL of the file to download'},
            'output_path': {'type': 'string', 'description': 'Local file path to save to'}
        }, 'required': ['url', 'output_path']}, web_download_file)

    # ========== 36. fb_post ==========
    def fb_post(message='', link='', photo_url=''):
        """Post to a Facebook Page (text, link, or photo)."""
        page_id, page_token, err = _fb_page_info()
        if err: return err
        try:
            if photo_url:
                api_url = f'https://graph.facebook.com/v19.0/{page_id}/photos'
                params = {'url': photo_url, 'caption': message, 'access_token': page_token}
            else:
                api_url = f'https://graph.facebook.com/v19.0/{page_id}/feed'
                params = {'message': message, 'access_token': page_token}
                if link: params['link'] = link
            payload = json.dumps(params).encode('utf-8')
            req = Request(api_url, data=payload, headers={'Content-Type': 'application/json'}, method='POST')
            with urlopen(req, timeout=30) as resp:
                data = json.loads(resp.read().decode('utf-8'))
            return json.dumps({'success': True, 'post_id': data.get('id', data.get('post_id', '?'))}, ensure_ascii=False)
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('fb_post', 'Post text, link, or photo to a Facebook Page.', {
        'type': 'object', 'properties': {
            'message': {'type': 'string', 'description': 'Post text content'},
            'link': {'type': 'string', 'description': 'URL to share (optional)'},
            'photo_url': {'type': 'string', 'description': 'Publicly accessible photo URL (optional)'}
        }, 'required': []}, fb_post)

    # ========== 37. x_post ==========
    def x_post(text):
        """Post a tweet to Twitter/X using OAuth 1.0a."""
        creds = _x_oauth()
        if not all([creds['consumer_key'], creds['consumer_secret'], creds['access_token'], creds['access_token_secret']]):
            return 'Error: 請先在設定的 config.json 中填入 Twitter OAuth 憑證 (twitter.consumerKey, consumerSecret, accessToken, accessTokenSecret)'
        try:
            api_url = 'https://api.twitter.com/2/tweets'
            body = json.dumps({'text': text}).encode('utf-8')
            auth_header = _oauth1_sign('POST', api_url, {}, creds['consumer_key'], creds['consumer_secret'], creds['access_token'], creds['access_token_secret'])
            req = Request(api_url, data=body, headers={
                'Content-Type': 'application/json',
                'Authorization': auth_header,
            }, method='POST')
            with urlopen(req, timeout=30) as resp:
                data = json.loads(resp.read().decode('utf-8'))
            tweet_data = data.get('data', {})
            return json.dumps({'success': True, 'tweet_id': tweet_data.get('id', '?'), 'text': tweet_data.get('text', text)}, ensure_ascii=False)
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('x_post', 'Post a tweet to Twitter/X.', {
        'type': 'object', 'properties': {
            'text': {'type': 'string', 'description': 'Tweet text (max 280 chars)'}
        }, 'required': ['text']}, x_post)

    # ========== 38. threads_publish ==========
    def threads_publish(text='', image_url='', video_url=''):
        """Publish a post to Threads."""
        token = _threads_token()
        if not token: return 'Error: 請先在設定中填入 Threads Access Token'
        uid, err = _threads_user_id(token)
        if err: return err
        try:
            # Step 1: Create container
            params = {'access_token': token}
            if video_url:
                params['media_type'] = 'VIDEO'
                params['video_url'] = video_url
                if text: params['text'] = text
            elif image_url:
                params['media_type'] = 'IMAGE'
                params['image_url'] = image_url
                if text: params['text'] = text
            else:
                params['media_type'] = 'TEXT'
                params['text'] = text
            create_url = f'https://graph.threads.net/v1.0/{uid}/threads'
            payload = json.dumps(params).encode('utf-8')
            req = Request(create_url, data=payload, headers={'Content-Type': 'application/json'}, method='POST')
            with urlopen(req, timeout=30) as resp:
                create_data = json.loads(resp.read().decode('utf-8'))
            container_id = create_data.get('id')
            if not container_id:
                return 'Error: Failed to create Threads container: ' + json.dumps(create_data, ensure_ascii=False)

            # Step 2: For video, wait for processing
            if video_url:
                import time as _time
                for _ in range(60):
                    status_url = f'https://graph.threads.net/v1.0/{container_id}?fields=status&access_token={token}'
                    with urlopen(Request(status_url), timeout=10) as resp:
                        st = json.loads(resp.read().decode('utf-8'))
                    code = st.get('status', '')
                    if code == 'FINISHED':
                        break
                    elif code == 'ERROR':
                        return 'Error: Threads media processing failed'
                    _time.sleep(5)
                else:
                    return 'Error: Threads media processing timed out'

            # Step 3: Publish
            pub_url = f'https://graph.threads.net/v1.0/{uid}/threads_publish'
            pub_payload = json.dumps({'creation_id': container_id, 'access_token': token}).encode('utf-8')
            req2 = Request(pub_url, data=pub_payload, headers={'Content-Type': 'application/json'}, method='POST')
            with urlopen(req2, timeout=30) as resp:
                pub_data = json.loads(resp.read().decode('utf-8'))
            return json.dumps({'success': True, 'thread_id': pub_data.get('id', '?')}, ensure_ascii=False)
        except Exception as e:
            return 'Error: ' + str(e)
    r.register('threads_publish', 'Publish a text, image, or video post to Threads.', {
        'type': 'object', 'properties': {
            'text': {'type': 'string', 'description': 'Post text content'},
            'image_url': {'type': 'string', 'description': 'Publicly accessible image URL (optional)'},
            'video_url': {'type': 'string', 'description': 'Publicly accessible video URL (optional)'}
        }, 'required': []}, threads_publish)

    # ========== 攝影機監控工具 ==========

    def camera_list():
        """列出所有攝影機及狀態"""
        try:
            import __main__
            mgr = getattr(__main__, '_camera_mgr', None)
            if not mgr:
                return '攝影機管理器未初始化，請確認後端已啟動。'
            cams = mgr.get_stream_status()
            if not cams:
                return '目前沒有設定任何攝影機。請到監控頁面新增。'
            lines = []
            for c in cams:
                status = '🟢 串流中' if c.get('streaming') else '⚫ 停止'
                lines.append(f'- {c["name"]} ({c["type"]}) {status}')
            return '\n'.join(lines)
        except Exception as e:
            return f'Error: {e}'
    r.register('camera_list', 'List all cameras and their streaming status.', {
        'type': 'object', 'properties': {}, 'required': []}, camera_list)

    def camera_snapshot(camera_name=''):
        """擷取攝影機快照"""
        try:
            import __main__
            mgr = getattr(__main__, '_camera_mgr', None)
            if not mgr:
                return 'Error: 攝影機管理器未初始化'
            cams = mgr.get_cameras()
            target = None
            for c in cams:
                if camera_name and camera_name.lower() in c['name'].lower():
                    target = c
                    break
            if not target and cams:
                target = cams[0]
            if not target:
                return '找不到攝影機。'
            path = mgr.snapshot(target['id'])
            if path:
                return f'已擷取快照：{path}'
            return '擷取快照失敗，請確認攝影機是否正常。'
        except Exception as e:
            return f'Error: {e}'
    r.register('camera_snapshot', 'Take a snapshot from a camera.', {
        'type': 'object', 'properties': {
            'camera_name': {'type': 'string', 'description': 'Camera name to snapshot (partial match)'}
        }, 'required': []}, camera_snapshot)

    def camera_stream_control(camera_name='', action='start'):
        """啟動或停止攝影機串流"""
        try:
            import __main__
            mgr = getattr(__main__, '_camera_mgr', None)
            if not mgr:
                return 'Error: 攝影機管理器未初始化'
            cams = mgr.get_cameras()
            target = None
            for c in cams:
                if camera_name and camera_name.lower() in c['name'].lower():
                    target = c
                    break
            if not target and cams:
                target = cams[0]
            if not target:
                return '找不到攝影機。'
            if action == 'start':
                ok, msg = mgr.start_stream(target['id'])
                return f'{"✅" if ok else "❌"} {target["name"]}: {msg}'
            else:
                mgr.stop_stream(target['id'])
                return f'已停止 {target["name"]} 的串流。'
        except Exception as e:
            return f'Error: {e}'
    r.register('camera_stream_control', 'Start or stop a camera stream.', {
        'type': 'object', 'properties': {
            'camera_name': {'type': 'string', 'description': 'Camera name (partial match)'},
            'action': {'type': 'string', 'enum': ['start', 'stop'], 'description': 'start or stop'}
        }, 'required': ['action']}, camera_stream_control)

    def camera_analyze(camera_name='', prompt=''):
        """讓 AI 分析攝影機畫面"""
        try:
            import __main__
            mgr = getattr(__main__, '_camera_mgr', None)
            if not mgr:
                return 'Error: 攝影機管理器未初始化'
            cams = mgr.get_cameras()
            target = None
            for c in cams:
                if camera_name and camera_name.lower() in c['name'].lower():
                    target = c
                    break
            if not target and cams:
                target = cams[0]
            if not target:
                return '找不到攝影機。'
            result = mgr.analyze_frame(target['id'], prompt=prompt)
            return result
        except Exception as e:
            return f'Error: {e}'
    r.register('camera_analyze', 'Analyze camera feed with AI vision — describe what is seen, detect anomalies.', {
        'type': 'object', 'properties': {
            'camera_name': {'type': 'string', 'description': 'Camera name (partial match)'},
            'prompt': {'type': 'string', 'description': 'What to look for (e.g. 有沒有人, 門有沒有關)'},
        }, 'required': []}, camera_analyze)

    def camera_watch_control(camera_name='', action='start', interval=60):
        """啟動或停止 AI 持續監控"""
        try:
            import __main__
            mgr = getattr(__main__, '_camera_mgr', None)
            if not mgr:
                return 'Error: 攝影機管理器未初始化'
            cams = mgr.get_cameras()
            target = None
            for c in cams:
                if camera_name and camera_name.lower() in c['name'].lower():
                    target = c
                    break
            if not target and cams:
                target = cams[0]
            if not target:
                return '找不到攝影機。'
            if action == 'start':
                def notify(cam_name, alert_text):
                    print(f'🚨 {cam_name}: {alert_text[:100]}')
                ok, msg = mgr.start_watch(target['id'], interval=interval, notify_callback=notify)
                return f'{"✅" if ok else "❌"} {target["name"]}: {msg}'
            elif action == 'stop':
                mgr.stop_watch(target['id'])
                return f'已停止 {target["name"]} 的 AI 監控。'
            elif action == 'status':
                status = mgr.get_watch_status(target['id'])
                if status.get('watching'):
                    return f'🟢 {target["name"]} 監控中，每 {status["interval"]} 秒檢查一次，已偵測 {status["alert_count"]} 次異常'
                return f'⚫ {target["name"]} 未在監控中'
        except Exception as e:
            return f'Error: {e}'
    r.register('camera_watch_control', 'Start/stop/check AI continuous monitoring on a camera.', {
        'type': 'object', 'properties': {
            'camera_name': {'type': 'string', 'description': 'Camera name (partial match)'},
            'action': {'type': 'string', 'enum': ['start', 'stop', 'status'], 'description': 'start, stop, or status'},
            'interval': {'type': 'number', 'description': 'Check interval in seconds (default 60)'},
        }, 'required': ['action']}, camera_watch_control)

    # ========== 智慧家電工具 ==========

    def smarthome_list_devices():
        """列出所有智慧家電裝置"""
        try:
            import __main__
            mgr = getattr(__main__, '_smarthome_mgr', None)
            if not mgr:
                return '智慧家電管理器未初始化。'
            devices = mgr.get_devices()
            if not devices:
                return '目前沒有連線的智慧家電。請到家電頁面新增平台。'
            lines = []
            for d in devices:
                state = d.get('state', '?')
                icon = '💡' if d['type'] == 'light' else '🔌' if d['type'] == 'switch' else '❄️' if d['type'] == 'climate' else '📺' if d['type'] == 'media_player' else '🏠'
                lines.append(f'{icon} {d["name"]} — {state} ({d.get("platform_name", "")})')
            return '\n'.join(lines)
        except Exception as e:
            return f'Error: {e}'
    r.register('smarthome_list_devices', 'List all smart home devices and their states.', {
        'type': 'object', 'properties': {}, 'required': []}, smarthome_list_devices)

    def smarthome_control(device_name, action='toggle', brightness=None, temperature=None):
        """控制智慧家電（開/關/調整）"""
        try:
            import __main__
            mgr = getattr(__main__, '_smarthome_mgr', None)
            if not mgr:
                return 'Error: 智慧家電管理器未初始化'
            device = mgr.find_device_by_name(device_name)
            if not device:
                return f'找不到名為「{device_name}」的裝置。請用 smarthome_list_devices 查看可用裝置。'
            params = {}
            if brightness is not None:
                params['brightness'] = brightness
            if temperature is not None:
                params['temperature'] = temperature
            result = mgr.control_device(device['id'], action, params)
            if result.get('success'):
                return f'✅ 已對「{device["name"]}」執行 {action}'
            return f'❌ 控制失敗: {result.get("error", "未知錯誤")}'
        except Exception as e:
            return f'Error: {e}'
    r.register('smarthome_control', 'Control a smart home device (on/off/toggle/set_brightness/set_temperature).', {
        'type': 'object', 'properties': {
            'device_name': {'type': 'string', 'description': 'Device name (partial match, e.g. 客廳燈)'},
            'action': {'type': 'string', 'description': 'Action: on, off, toggle, set_brightness, set_temperature'},
            'brightness': {'type': 'number', 'description': 'Brightness 0-255 (for lights)'},
            'temperature': {'type': 'number', 'description': 'Temperature in Celsius (for climate)'},
        }, 'required': ['device_name']}, smarthome_control)

    def smarthome_device_state(device_name):
        """查詢單一裝置狀態"""
        try:
            import __main__
            mgr = getattr(__main__, '_smarthome_mgr', None)
            if not mgr:
                return 'Error: 智慧家電管理器未初始化'
            device = mgr.find_device_by_name(device_name)
            if not device:
                return f'找不到名為「{device_name}」的裝置。'
            state = mgr.get_device_state(device['id'])
            if state:
                attrs = state.get('attributes', {})
                lines = [f'裝置: {state.get("name", device_name)}',
                         f'狀態: {state.get("state", "?")}',
                         f'類型: {state.get("type", "?")}']
                for k, v in attrs.items():
                    if k not in ('friendly_name', 'entity_id'):
                        lines.append(f'{k}: {v}')
                return '\n'.join(lines)
            return f'無法取得「{device_name}」的狀態。'
        except Exception as e:
            return f'Error: {e}'
    r.register('smarthome_device_state', 'Get the current state of a smart home device.', {
        'type': 'object', 'properties': {
            'device_name': {'type': 'string', 'description': 'Device name (partial match)'},
        }, 'required': ['device_name']}, smarthome_device_state)

    # ==================== 瀏覽器自動化 (Playwright) ====================

    _browser_ctx = {'browser': None, 'page': None}

    def _get_page(headless=True):
        """Lazy init Playwright browser, return current page."""
        if _browser_ctx['page'] and not _browser_ctx['page'].is_closed():
            return _browser_ctx['page']
        try:
            from playwright.sync_api import sync_playwright
        except ImportError:
            return None
        pw = sync_playwright().start()
        _browser_ctx['_pw'] = pw
        _browser_ctx['browser'] = pw.chromium.launch(headless=headless)
        _browser_ctx['page'] = _browser_ctx['browser'].new_page()
        return _browser_ctx['page']

    def browser_open(url, headless=True, wait_seconds=2):
        """Open a URL in the automated browser."""
        page = _get_page(headless=headless)
        if not page:
            return 'Error: Playwright not installed. Run: pip install playwright && python -m playwright install chromium'
        try:
            page.goto(url, timeout=30000, wait_until='domcontentloaded')
            if wait_seconds and float(wait_seconds) > 0:
                page.wait_for_timeout(int(float(wait_seconds) * 1000))
            title = page.title()
            text = page.inner_text('body')[:3000]
            return f'Page loaded: {title}\nURL: {page.url}\n\nContent:\n{text}'
        except Exception as e:
            return f'Error: {e}'
    r.register('browser_open', 'Open a URL in an automated browser and return page title and text content.', {
        'type': 'object', 'properties': {
            'url': {'type': 'string', 'description': 'URL to open'},
            'headless': {'type': 'boolean', 'description': 'Run browser in background (default: true)'},
            'wait_seconds': {'type': 'number', 'description': 'Seconds to wait after page load (default: 2)'},
        }, 'required': ['url']}, browser_open)

    def browser_click(selector=None, text=None):
        """Click an element on the current page."""
        page = _browser_ctx.get('page')
        if not page or page.is_closed():
            return 'Error: No browser page open. Use browser_open first.'
        try:
            if text:
                page.get_by_text(text, exact=False).first.click(timeout=10000)
                return f'Clicked element with text: {text}'
            elif selector:
                page.click(selector, timeout=10000)
                return f'Clicked: {selector}'
            return 'Error: Provide selector or text'
        except Exception as e:
            return f'Error: {e}'
    r.register('browser_click', 'Click an element on the current browser page by CSS selector or visible text.', {
        'type': 'object', 'properties': {
            'selector': {'type': 'string', 'description': 'CSS selector (e.g. button.submit, #login-btn)'},
            'text': {'type': 'string', 'description': 'Visible text of the element to click'},
        }, 'required': []}, browser_click)

    def browser_type(selector, text, press_enter=False):
        """Type text into an input field."""
        page = _browser_ctx.get('page')
        if not page or page.is_closed():
            return 'Error: No browser page open. Use browser_open first.'
        try:
            page.fill(selector, text, timeout=10000)
            if press_enter:
                page.press(selector, 'Enter')
            return f'Typed "{text}" into {selector}' + (' and pressed Enter' if press_enter else '')
        except Exception as e:
            return f'Error: {e}'
    r.register('browser_type', 'Type text into an input field on the current browser page.', {
        'type': 'object', 'properties': {
            'selector': {'type': 'string', 'description': 'CSS selector of the input field (e.g. input[name=q], #search)'},
            'text': {'type': 'string', 'description': 'Text to type'},
            'press_enter': {'type': 'boolean', 'description': 'Press Enter after typing (default: false)'},
        }, 'required': ['selector', 'text']}, browser_type)

    def browser_screenshot(full_page=False):
        """Take a screenshot of the current browser page."""
        page = _browser_ctx.get('page')
        if not page or page.is_closed():
            return 'Error: No browser page open. Use browser_open first.'
        try:
            ss_dir = os.path.join(HOME, '.autoto', 'screenshots')
            os.makedirs(ss_dir, exist_ok=True)
            fname = 'browser_' + datetime.now().strftime('%Y%m%d_%H%M%S') + '.png'
            fpath = os.path.join(ss_dir, fname)
            page.screenshot(path=fpath, full_page=bool(full_page))
            return f'Screenshot saved: {fname}\nURL: {page.url}\nTitle: {page.title()}'
        except Exception as e:
            return f'Error: {e}'
    r.register('browser_screenshot', 'Take a screenshot of the current browser page.', {
        'type': 'object', 'properties': {
            'full_page': {'type': 'boolean', 'description': 'Capture full scrollable page (default: false)'},
        }, 'required': []}, browser_screenshot)

    def browser_get_text(selector=None):
        """Get text content from the current page or a specific element."""
        page = _browser_ctx.get('page')
        if not page or page.is_closed():
            return 'Error: No browser page open. Use browser_open first.'
        try:
            if selector:
                text = page.inner_text(selector, timeout=10000)
            else:
                text = page.inner_text('body')
            return f'Page: {page.title()}\nURL: {page.url}\n\n{text[:5000]}'
        except Exception as e:
            return f'Error: {e}'
    r.register('browser_get_text', 'Get text content from the current browser page or a specific element.', {
        'type': 'object', 'properties': {
            'selector': {'type': 'string', 'description': 'CSS selector to extract text from (default: entire page body)'},
        }, 'required': []}, browser_get_text)

    def browser_run_js(script):
        """Execute JavaScript on the current page and return the result."""
        page = _browser_ctx.get('page')
        if not page or page.is_closed():
            return 'Error: No browser page open. Use browser_open first.'
        try:
            result = page.evaluate(script)
            return f'Result: {json.dumps(result, ensure_ascii=False, default=str)[:5000]}'
        except Exception as e:
            return f'Error: {e}'
    r.register('browser_run_js', 'Execute JavaScript on the current browser page and return the result.', {
        'type': 'object', 'properties': {
            'script': {'type': 'string', 'description': 'JavaScript code to execute'},
        }, 'required': ['script']}, browser_run_js)

    def browser_close():
        """Close the automated browser."""
        try:
            if _browser_ctx.get('page') and not _browser_ctx['page'].is_closed():
                _browser_ctx['page'].close()
            if _browser_ctx.get('browser'):
                _browser_ctx['browser'].close()
            if _browser_ctx.get('_pw'):
                _browser_ctx['_pw'].stop()
            _browser_ctx['browser'] = None
            _browser_ctx['page'] = None
            _browser_ctx['_pw'] = None
            return 'Browser closed.'
        except Exception as e:
            return f'Error: {e}'
    r.register('browser_close', 'Close the automated browser.', {
        'type': 'object', 'properties': {}, 'required': []}, browser_close)

    return r
