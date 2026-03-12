// ==================== 瀏覽器相容層 ====================
if (!window.autoto) {
  // 非 Electron 環境，用 localStorage 模擬
  const stored = JSON.parse(localStorage.getItem('autoto_config') || 'null');
  window.autoto = {
    getConfig: async () => stored || {
      provider: 'groq', apiKey: '', model: 'llama-3.3-70b-versatile', customUrl: '',
      channels: {
        discord: { enabled: false, token: '' },
        line: { enabled: false, channelAccessToken: '', channelSecret: '' },
        telegram: { enabled: false, botToken: '' },
        wechat: { enabled: false, appId: '', appSecret: '' },
        whatsapp: { enabled: false, phoneNumberId: '', accessToken: '', verifyToken: '' },
        slack: { enabled: false, botToken: '', signingSecret: '' },
        messenger: { enabled: false, pageAccessToken: '', verifyToken: '' },
        qq: { enabled: false, httpUrl: 'http://127.0.0.1:5700', webhookPort: 5683 },
        instagram: { enabled: false, accessToken: '' }
      },
      memory: { enabled: true, autoArchive: 50 },
      agent: {
        maxTokenBudget: 4000,
        compressionEnabled: true,
        systemPrompt: 'You are AutoTo, an open-source cross-platform AI assistant. Reply in the same language the user uses. AutoTo supports macOS and Windows. GitHub: https://github.com/hokidev26/autoto. You are a web-based AI assistant, not tied to any specific OS. Do not fabricate information you do not know.'
      },
      session: { persist: true }
    },
    saveConfig: async (cfg) => { localStorage.setItem('autoto_config', JSON.stringify(cfg)); return { success: true }; },
    getBackendPort: async () => 5678,
    restartBackend: async () => ({ success: true }),
    openExternal: (url) => window.open(url, '_blank')
  };
}

// ==================== 頁面導航 ====================
let currentPage = 'chat';
let backendPort = 5678;
let config = {};
let API = `http://127.0.0.1:${backendPort}/api`;
document.querySelectorAll('.nav-item').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.nav-item').forEach(b => b.classList.remove('active'));
    document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
    btn.classList.add('active');
    const page = btn.dataset.page;
    document.getElementById(`page-${page}`).classList.add('active');
    currentPage = page;
    if (page === 'skills') loadSkills();
    if (page === 'cameras') loadCamerasPage();
    if (page === 'smarthome') loadSmartHomePage();
    if (page === 'agents') loadAgentsPage();
    if (page === 'settings') {
      // 載入當前 tab 的資料
      const activeTab = document.querySelector('.settings-tab.active');
      if (activeTab) onSettingsTab(activeTab.dataset.stab);
    }
  });
});

// 設定子分頁切換
document.querySelectorAll('.settings-tab').forEach(tab => {
  tab.addEventListener('click', () => {
    document.querySelectorAll('.settings-tab').forEach(t => t.classList.remove('active'));
    document.querySelectorAll('.settings-panel').forEach(p => p.classList.remove('active'));
    tab.classList.add('active');
    document.getElementById(`stab-${tab.dataset.stab}`).classList.add('active');
    onSettingsTab(tab.dataset.stab);
  });
});

function onSettingsTab(tab) {
  if (tab === 'memory') loadMemories();
  if (tab === 'diagnostics') runDiagnostics();
  if (tab === 'about') loadVersionInfo();
  if (tab === 'scheduler') loadSchedulerPage();
  if (tab === 'permissions') loadPermissions();
  if (tab === 'market') loadMarket();
  if (tab === 'logs') startLogPolling(); else stopLogPolling();
}

// ==================== 服務商 & 模型選擇 ====================
const PROVIDER_MODELS = {
  groq: {
    models: ['llama-3.3-70b-versatile', 'llama-3.1-8b-instant', 'gemma2-9b-it', 'llama-3.1-70b-versatile', 'mixtral-8x7b-32768'],
    default: 'llama-3.3-70b-versatile',
    hint: '免費，速度快，有每日 token 限制',
    needsKey: true
  },
  ollama: {
    models: ['llama3.1', 'llama3.2', 'mistral', 'codellama', 'gemma2', 'phi3', 'qwen2.5', 'deepseek-r1'],
    default: 'llama3.1',
    hint: '本地運行，免費無限制。需先安裝 Ollama 並下載模型',
    needsKey: false
  },
  openrouter: {
    models: ['anthropic/claude-3.5-sonnet', 'google/gemini-pro-1.5', 'meta-llama/llama-3.1-70b-instruct', 'openai/gpt-4o', 'mistralai/mistral-large-latest'],
    default: 'anthropic/claude-3.5-sonnet',
    hint: '多模型聚合平台，按量計費',
    needsKey: true
  },
  anthropic: {
    models: ['claude-3-5-sonnet-20241022', 'claude-3-5-haiku-20241022', 'claude-3-opus-20240229'],
    default: 'claude-3-5-sonnet-20241022',
    hint: '品質最好，支援工具呼叫',
    needsKey: true
  },
  openai: {
    models: ['gpt-4o', 'gpt-4o-mini', 'gpt-4-turbo', 'o1-mini'],
    default: 'gpt-4o',
    hint: 'OpenAI 官方模型',
    needsKey: true
  },
  kimi: {
    models: ['moonshot-v1-8k', 'moonshot-v1-32k', 'moonshot-v1-128k'],
    default: 'moonshot-v1-8k',
    hint: 'Moonshot AI / Kimi，擅長中文長文理解',
    needsKey: true
  },
  deepseek: {
    models: ['deepseek-chat', 'deepseek-reasoner'],
    default: 'deepseek-chat',
    hint: '高性價比，中文能力強',
    needsKey: true
  },
  gemini: {
    models: ['gemini-pro', 'gemini-1.5-pro', 'gemini-1.5-flash'],
    default: 'gemini-pro',
    hint: 'Google AI，需要 API Key',
    needsKey: true
  },
  mistral: {
    models: ['mistral-large-latest', 'mistral-medium-latest', 'mistral-small-latest', 'open-mistral-nemo'],
    default: 'mistral-large-latest',
    hint: '歐洲 AI 公司，多語言能力好',
    needsKey: true
  },
  together: {
    models: ['meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo', 'meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo', 'mistralai/Mixtral-8x7B-Instruct-v0.1'],
    default: 'meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo',
    hint: '開源模型託管平台',
    needsKey: true
  },
  fireworks: {
    models: ['accounts/fireworks/models/llama-v3p1-70b-instruct', 'accounts/fireworks/models/mixtral-8x7b-instruct'],
    default: 'accounts/fireworks/models/llama-v3p1-70b-instruct',
    hint: '高速推理平台',
    needsKey: true
  },
  cohere: {
    models: ['command-r-plus', 'command-r', 'command-light'],
    default: 'command-r-plus',
    hint: 'RAG 和搜尋增強能力強',
    needsKey: true
  },
  qwen: {
    models: ['qwen-turbo', 'qwen-plus', 'qwen-max'],
    default: 'qwen-turbo',
    hint: '阿里雲通義千問',
    needsKey: true
  },
  openclaw: {
    models: [],
    default: '',
    hint: '本地 AI Agent 框架，需先安裝 OpenClaw',
    needsKey: false
  },
  custom: {
    models: [],
    default: '',
    hint: '填入任何 OpenAI 相容 API 端點',
    needsKey: true
  }
};

function onProviderChange() {
  const provider = document.getElementById('cfgProvider').value;
  const info = PROVIDER_MODELS[provider] || {};
  const select = document.getElementById('cfgModelSelect');
  const hint = document.getElementById('modelHint');
  const keyGroup = document.getElementById('apiKeyGroup');
  const urlGroup = document.getElementById('customUrlGroup');

  // 更新模型下拉選單
  select.innerHTML = '<option value="">' + t('api_model_custom') + '</option>';
  (info.models || []).forEach(m => {
    const opt = document.createElement('option');
    opt.value = m;
    opt.textContent = m;
    select.appendChild(opt);
  });

  // 設定預設模型
  if (info.default) {
    select.value = info.default;
    document.getElementById('cfgModel').value = info.default;
  }

  // 提示文字
  hint.textContent = info.hint || '';

  // Ollama 不需要 API Key
  keyGroup.style.display = info.needsKey === false ? 'none' : 'block';

  // 自訂端點
  urlGroup.style.display = (provider === 'custom' || provider === 'openclaw') ? 'block' : 'none';

  // Ollama 特殊提示
  if (provider === 'ollama') {
    fetchOllamaModels();
  }

  // OpenClaw：自動帶入 URL 並檢查狀態
  const ocPanel = document.getElementById('openclawPanel');
  if (provider === 'openclaw') {
    document.getElementById('cfgCustomUrl').value = 'http://127.0.0.1:18789/v1/chat/completions';
    if (ocPanel) ocPanel.style.display = 'block';
    checkOpenClawStatus();
  } else {
    if (ocPanel) ocPanel.style.display = 'none';
  }
}

function onModelSelect() {
  const val = document.getElementById('cfgModelSelect').value;
  if (val) {
    document.getElementById('cfgModel').value = val;
  }
}

async function fetchOllamaModels() {
  try {
    const res = await fetch('http://127.0.0.1:11434/api/tags', { signal: AbortSignal.timeout(3000) });
    const data = await res.json();
    if (data.models && data.models.length > 0) {
      const select = document.getElementById('cfgModelSelect');
      select.innerHTML = '<option value="">' + t('api_model_custom') + '</option>';
      data.models.forEach(m => {
        const opt = document.createElement('option');
        opt.value = m.name;
        opt.textContent = `${m.name} (${(m.size / 1e9).toFixed(1)}GB)`;
        select.appendChild(opt);
      });
      document.getElementById('modelHint').textContent = t('ollama_detected', { n: data.models.length });
      if (data.models.length > 0 && !document.getElementById('cfgModel').value) {
        select.value = data.models[0].name;
        document.getElementById('cfgModel').value = data.models[0].name;
      }
    }
  } catch {
    document.getElementById('modelHint').textContent = t('ollama_fail');
  }
}

// ==================== OpenClaw 整合 ====================

async function checkOpenClawStatus() {
  const badge = document.getElementById('ocStatusBadge');
  const detail = document.getElementById('ocStatusDetail');
  const actions = document.getElementById('ocActions');
  if (!badge) return;

  badge.textContent = '檢查中...';
  badge.style.background = 'var(--bg-card)';
  badge.style.color = 'var(--text-secondary)';

  try {
    const res = await fetch(`${API}/openclaw/status`);
    const s = await res.json();

    if (s.running) {
      badge.textContent = '運行中';
      badge.style.background = '#22c55e22';
      badge.style.color = '#22c55e';
      let verInfo = `版本: ${s.version || '未知'}`;
      if (s.update_available) {
        verInfo += ` (有新版 ${s.latest_version})`;
      }
      detail.innerHTML = `${verInfo}<br>Gateway: ${s.gateway_url}`;
      let btns = `<button class="btn-primary" onclick="connectOpenClaw()">一鍵連接</button>`;
      if (s.update_available) {
        btns += `<button class="btn-sm" onclick="updateOpenClaw()">更新到 ${s.latest_version}</button>`;
      }
      btns += `<button class="btn-sm" onclick="checkOpenClawStatus()">重新檢查</button>`;
      actions.innerHTML = btns;
      fetchOpenClawModels();
    } else if (s.installed) {
      badge.textContent = '已安裝（未啟動）';
      badge.style.background = '#f59e0b22';
      badge.style.color = '#f59e0b';
      let verInfo = `版本: ${s.version || '未知'}`;
      if (s.update_available) {
        verInfo += ` (有新版 ${s.latest_version})`;
      }
      detail.innerHTML = `${verInfo}<br>請在終端機執行 <code>openclaw</code> 啟動 Gateway`;
      let btns = '';
      if (s.update_available) {
        btns += `<button class="btn-sm" onclick="updateOpenClaw()">更新到 ${s.latest_version}</button>`;
      }
      btns += `<button class="btn-sm" onclick="checkOpenClawStatus()">重新檢查</button>`;
      actions.innerHTML = btns;
    } else if (s.node_installed) {
      badge.textContent = '未安裝';
      badge.style.background = 'var(--bg-card)';
      badge.style.color = 'var(--text-secondary)';
      detail.innerHTML = 'Node.js 已就緒，可以一鍵安裝 OpenClaw';
      actions.innerHTML = `<button class="btn-primary" onclick="installOpenClaw()">一鍵安裝 OpenClaw</button>
        <button class="btn-sm" onclick="checkOpenClawStatus()">重新檢查</button>`;
    } else {
      badge.textContent = '未安裝';
      badge.style.background = 'var(--bg-card)';
      badge.style.color = 'var(--text-secondary)';
      detail.innerHTML = '需要先安裝 <a href="https://nodejs.org/" target="_blank">Node.js 22+</a>，再安裝 OpenClaw';
      actions.innerHTML = `<button class="btn-sm" onclick="checkOpenClawStatus()">重新檢查</button>`;
    }
  } catch (e) {
    badge.textContent = '檢查失敗';
    detail.textContent = e.message;
    actions.innerHTML = `<button class="btn-sm" onclick="checkOpenClawStatus()">重試</button>`;
  }
}

async function installOpenClaw() {
  const actions = document.getElementById('ocActions');
  const detail = document.getElementById('ocStatusDetail');
  if (!actions) return;

  try {
    const res = await fetch(`${API}/openclaw/install`, { method: 'POST' });
    const data = await res.json();
    if (data.success) {
      detail.innerHTML = data.steps.map(s => escapeHtml(s)).join('<br>');
      // 加一個複製按鈕
      actions.innerHTML = `<button class="btn-primary" onclick="navigator.clipboard.writeText('${data.command}');this.textContent='已複製！'">複製安裝指令</button>
        <button class="btn-sm" onclick="checkOpenClawStatus()">安裝完成，重新檢查</button>`;
    }
  } catch (e) {
    detail.textContent = '取得安裝指令失敗: ' + e.message;
  }
}

async function connectOpenClaw() {
  const detail = document.getElementById('ocStatusDetail');
  try {
    const res = await fetch(`${API}/openclaw/connect`, { method: 'POST' });
    const data = await res.json();
    if (data.success) {
      detail.innerHTML = `已連接！使用模型: <b>${data.model}</b>`;
      // 更新 UI
      document.getElementById('cfgProvider').value = 'openclaw';
      document.getElementById('cfgCustomUrl').value = 'http://127.0.0.1:18789/v1/chat/completions';
      document.getElementById('cfgModel').value = data.model;
      // 更新模型下拉
      const select = document.getElementById('cfgModelSelect');
      select.innerHTML = '<option value="">自訂...</option>';
      (data.models || []).forEach(m => {
        const opt = document.createElement('option');
        opt.value = m; opt.textContent = m;
        select.appendChild(opt);
      });
      if (data.model) select.value = data.model;
      alert('已成功連接 OpenClaw！點儲存設定即可開始使用。');
    } else {
      detail.textContent = '連接失敗: ' + (data.error || '');
    }
  } catch (e) {
    detail.textContent = '連接失敗: ' + e.message;
  }
}

function updateOpenClaw() {
  const detail = document.getElementById('ocStatusDetail');
  const actions = document.getElementById('ocActions');
  const cmd = navigator.platform.includes('Win') ? 'npm install -g openclaw@latest' : 'curl -fsSL https://openclaw.ai/install.sh | bash';
  detail.innerHTML = `請在終端機執行以下指令更新：<br><code>${cmd}</code>`;
  actions.innerHTML = `<button class="btn-primary" onclick="navigator.clipboard.writeText('${cmd}');this.textContent='已複製！'">複製更新指令</button>
    <button class="btn-sm" onclick="checkOpenClawStatus()">更新完成，重新檢查</button>`;
}

async function fetchOpenClawModels() {
  try {
    const res = await fetch('http://127.0.0.1:18789/v1/models', { signal: AbortSignal.timeout(3000) });
    const data = await res.json();
    if (data.data && data.data.length > 0) {
      const select = document.getElementById('cfgModelSelect');
      select.innerHTML = '<option value="">自訂...</option>';
      data.data.forEach(m => {
        const opt = document.createElement('option');
        opt.value = m.id; opt.textContent = m.id;
        select.appendChild(opt);
      });
      if (!document.getElementById('cfgModel').value && data.data.length > 0) {
        select.value = data.data[0].id;
        document.getElementById('cfgModel').value = data.data[0].id;
      }
    }
  } catch {}
}

// ==================== 初始化 ====================
let currentSessionId = null;
let convSidebarVisible = true;
let currentSessionIsDraft = false;
 const LAST_SESSION_KEY = 'autoto_last_session_id';

 function saveLastSessionId(sessionId) {
   if (sessionId) localStorage.setItem(LAST_SESSION_KEY, sessionId);
   else localStorage.removeItem(LAST_SESSION_KEY);
 }

 function getLastSessionId() {
  return localStorage.getItem(LAST_SESSION_KEY);
}

function getNewConversationLabel() {
  return (t('chat_new') || 'New Chat').replace(/^\+\s*/, '').trim();
}

function getConversationDisplayTitle(session) {
  if (!session) return getNewConversationLabel();
  const rawTitle = session.title || '';
  if ((session.messageCount || 0) === 0 && (!rawTitle || rawTitle === session.id || /^web-\d+$/.test(rawTitle))) {
    return getNewConversationLabel();
  }
  return rawTitle || getNewConversationLabel();
}

function resetConversationState() {
  currentSessionId = null;
  currentSessionIsDraft = false;
  saveLastSessionId(null);
  showWelcome();
  document.getElementById('chatTitle').textContent = t('chat_title');
  document.querySelectorAll('.conv-item').forEach(el => el.classList.remove('active'));
}

async function init() {
  try {
    backendPort = await window.autoto.getBackendPort();
    API = `http://127.0.0.1:${backendPort}/api`;
    config = await window.autoto.getConfig();
    loadConfigToUI();
    checkBackendStatus();
    loadSkills();
    setInterval(checkBackendStatus, 5000);
  } catch (e) {
    console.log('Running outside Electron, using defaults');
    backendPort = 5678;
    API = `http://127.0.0.1:${backendPort}/api`;
  }
  // 套用語言
  const langSel = document.getElementById('cfgLanguage');
  if (langSel) langSel.value = currentLang;
  applyLanguage();
  // 載入對話列表
  const sessions = await loadConversations();
  if (!currentSessionId && sessions.length > 0) {
    const preferredSessionId = getLastSessionId();
    const preferred = sessions.find(s => s.id === preferredSessionId) || sessions[0];
    if (preferred) {
      await switchConversation(preferred.id, getConversationDisplayTitle(preferred), { skipReload: true });
    }
  }
}

function loadConfigToUI() {
  // API 設定
  document.getElementById('cfgProvider').value = config.provider || 'groq';
  document.getElementById('cfgApiKey').value = config.apiKey || '';
  document.getElementById('cfgModel').value = config.model || '';
  if (config.customUrl) document.getElementById('cfgCustomUrl').value = config.customUrl;
  onProviderChange();
  // 覆蓋回使用者存的模型
  if (config.model) {
    document.getElementById('cfgModel').value = config.model;
    document.getElementById('cfgModelSelect').value = config.model;
  }
  document.getElementById('cfgMaxTokens').value = config.agent?.maxTokenBudget || 4000;
  document.getElementById('cfgCompression').checked = config.agent?.compressionEnabled !== false;
  document.getElementById('cfgSessionPersist').checked = config.session?.persist !== false;

  // 平台設定
  const ch = config.channels || {};
  if (ch.discord) {
    document.getElementById('chDiscordEnabled').checked = ch.discord.enabled;
    document.getElementById('chDiscordToken').value = ch.discord.token || '';
  }
  if (ch.line) {
    document.getElementById('chLineEnabled').checked = ch.line.enabled;
    document.getElementById('chLineToken').value = ch.line.channelAccessToken || '';
    document.getElementById('chLineSecret').value = ch.line.channelSecret || '';
  }
  if (ch.telegram) {
    document.getElementById('chTelegramEnabled').checked = ch.telegram.enabled;
    document.getElementById('chTelegramToken').value = ch.telegram.botToken || '';
  }
  if (ch.wechat) {
    document.getElementById('chWechatEnabled').checked = ch.wechat.enabled;
    document.getElementById('chWechatAppId').value = ch.wechat.appId || '';
    document.getElementById('chWechatSecret').value = ch.wechat.appSecret || '';
  }
  if (ch.whatsapp) {
    document.getElementById('chWhatsappEnabled').checked = ch.whatsapp.enabled;
    document.getElementById('chWhatsappPhoneId').value = ch.whatsapp.phoneNumberId || '';
    document.getElementById('chWhatsappToken').value = ch.whatsapp.accessToken || '';
    document.getElementById('chWhatsappVerify').value = ch.whatsapp.verifyToken || '';
  }
  if (ch.slack) {
    document.getElementById('chSlackEnabled').checked = ch.slack.enabled;
    document.getElementById('chSlackToken').value = ch.slack.botToken || '';
    document.getElementById('chSlackSecret').value = ch.slack.signingSecret || '';
  }
  if (ch.messenger) {
    document.getElementById('chMessengerEnabled').checked = ch.messenger.enabled;
    document.getElementById('chMessengerToken').value = ch.messenger.pageAccessToken || '';
    document.getElementById('chMessengerVerify').value = ch.messenger.verifyToken || '';
  }
  if (ch.qq) {
    document.getElementById('chQqEnabled').checked = ch.qq.enabled;
    document.getElementById('chQqHttpUrl').value = ch.qq.httpUrl || 'http://127.0.0.1:5700';
    document.getElementById('chQqWebhookPort').value = ch.qq.webhookPort || 5683;
  }
  if (ch.instagram) {
    document.getElementById('chIgEnabled').checked = ch.instagram.enabled;
    document.getElementById('chIgToken').value = ch.instagram.accessToken || '';
  }
  // 社群發文平台
  const fb = config.facebook || {};
  document.getElementById('chFbPageId').value = fb.pageId || '';
  document.getElementById('chFbPageToken').value = fb.pageAccessToken || '';
  const tw = config.twitter || {};
  document.getElementById('chXConsumerKey').value = tw.consumerKey || '';
  document.getElementById('chXConsumerSecret').value = tw.consumerSecret || '';
  document.getElementById('chXAccessToken').value = tw.accessToken || '';
  document.getElementById('chXAccessTokenSecret').value = tw.accessTokenSecret || '';
  const th = config.threads || {};
  document.getElementById('chThreadsToken').value = th.accessToken || '';
}

// ==================== 後端狀態 ====================
async function checkBackendStatus() {
  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/status`);
    if (res.ok) {
      setStatus('online', t('status_connected'));
    } else {
      setStatus('offline', t('status_offline'));
    }
  } catch {
    setStatus('offline', t('status_offline'));
  }
}

function setStatus(state, text) {
  const dot = document.getElementById('statusDot');
  const txt = document.getElementById('statusText');
  dot.className = `status-dot ${state}`;
  txt.textContent = text;
}

// ==================== 對話功能 ====================
const chatMessages = document.getElementById('chatMessages');
const chatInput = document.getElementById('chatInput');

// 注音/中文輸入法相容
// 關鍵：compositionend 和 keydown 的觸發順序在不同瀏覽器不一致
// Chrome macOS: keydown(Enter) → compositionend → keyup(Enter)
// 所以 keydown 時 isComposing 可能還是 true，但 compositionend 時的 Enter 不該送出
// 解法：compositionend 後加 flag 延遲，讓同一個 Enter 事件不會觸發送出
let isComposing = false;
let justFinishedComposing = false;

chatInput.addEventListener('compositionstart', () => {
  isComposing = true;
  justFinishedComposing = false;
});

chatInput.addEventListener('compositionend', () => {
  isComposing = false;
  justFinishedComposing = true;
  // 等這一輪事件全部跑完再重置
  setTimeout(() => { justFinishedComposing = false; }, 300);
});

chatInput.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey) {
    // 正在輸入中文 或 剛剛確認完字 → 不攔截（讓 IME 正常運作）
    if (isComposing || e.isComposing || justFinishedComposing) return;
    e.preventDefault();
    sendMessage();
  }
});

// 自動調整輸入框高度
chatInput.addEventListener('input', () => {
  chatInput.style.height = 'auto';
  chatInput.style.height = Math.min(chatInput.scrollHeight, 120) + 'px';
});

function sendQuick(text) {
  chatInput.value = text;
  sendMessage();
}

async function sendMessage() {
  const message = chatInput.value.trim();
  if (!message) return;

  // 如果沒有 session，自動建立一個
  const isNewSession = !currentSessionId || currentSessionIsDraft;
  if (!currentSessionId) {
    currentSessionId = 'web-' + Date.now();
    saveLastSessionId(currentSessionId);
    document.getElementById('chatTitle').textContent = message.slice(0, 24) || t('chat_title');
    currentSessionIsDraft = true;
  }

  // 清除歡迎訊息
  const welcome = chatMessages.querySelector('.welcome-msg');
  if (welcome) welcome.remove();

  // 顯示用戶訊息
  appendMessage('user', message);
  chatInput.value = '';
  chatInput.style.height = 'auto';

  // 顯示載入中
  const loadingId = appendMessage('bot', t('chat_thinking'));

  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/chat`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ message, session_id: currentSessionId })
    });

    const data = await res.json();
    removeMessage(loadingId);

    if (data.success) {
      appendMessage('bot', data.response);
    } else {
      appendMessage('bot', data.error || t('chat_error_connect'));
    }
    // 更新對話列表（新對話會出現在列表中）
    await loadConversations();
    if (isNewSession) {
      document.getElementById('chatTitle').textContent = message.slice(0, 24) || t('chat_title');
      currentSessionIsDraft = false;
    }
  } catch (err) {
    removeMessage(loadingId);
    appendMessage('bot', t('chat_error_connect'));
  }
}

let msgCounter = 0;
function appendMessage(role, text) {
  const id = `msg-${++msgCounter}`;
  const div = document.createElement('div');
  div.className = `msg ${role}`;
  div.id = id;
  const avatarSvg = role === 'user'
    ? '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="white" stroke-width="2"><path d="M20 21v-2a4 4 0 00-4-4H8a4 4 0 00-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>'
    : '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><path d="M8 14s1.5 2 4 2 4-2 4-2"/><line x1="9" y1="9" x2="9.01" y2="9"/><line x1="15" y1="9" x2="15.01" y2="9"/></svg>';
  div.innerHTML = `
    <div class="msg-avatar">${avatarSvg}</div>
    <div class="msg-bubble">${escapeHtml(text)}</div>
  `;
  chatMessages.appendChild(div);
  chatMessages.scrollTop = chatMessages.scrollHeight;
  return id;
}

function removeMessage(id) {
  const el = document.getElementById(id);
  if (el) el.remove();
}

function escapeHtml(text) {
  const div = document.createElement('div');
  div.textContent = text;
  let html = div.innerHTML;
  // 偵測截圖路徑，轉成圖片顯示
  html = html.replace(/SCREENSHOT:([^\s<]+\.png)/g, (match, path) => {
    const filename = path.split('/').pop();
    const imgUrl = `http://127.0.0.1:${backendPort}/api/screenshot/${filename}`;
    return `📸 Screenshot<br><img src="${imgUrl}" class="chat-screenshot" onclick="window.open('${imgUrl}','_blank')" alt="截圖">`;
  });
  return html;
}

// ==================== 設定儲存 ====================
async function saveSettings() {
  config.provider = document.getElementById('cfgProvider').value;
  const apiKeyInput = document.getElementById('cfgApiKey').value;
  if (apiKeyInput && !apiKeyInput.startsWith('***')) {
    config.apiKey = apiKeyInput;
  }
  config.model = document.getElementById('cfgModel').value;
  config.customUrl = document.getElementById('cfgCustomUrl').value;
  config.agent = config.agent || {};
  config.agent.maxTokenBudget = parseInt(document.getElementById('cfgMaxTokens').value);
  config.agent.compressionEnabled = document.getElementById('cfgCompression').checked;
  config.session = config.session || {};
  config.session.persist = document.getElementById('cfgSessionPersist').checked;

  await saveConfigToBackend();
  alert(t('api_save'));
}

async function saveChannels() {
  config.channels = {
    discord: {
      enabled: document.getElementById('chDiscordEnabled').checked,
      token: document.getElementById('chDiscordToken').value
    },
    line: {
      enabled: document.getElementById('chLineEnabled').checked,
      channelAccessToken: document.getElementById('chLineToken').value,
      channelSecret: document.getElementById('chLineSecret').value
    },
    telegram: {
      enabled: document.getElementById('chTelegramEnabled').checked,
      botToken: document.getElementById('chTelegramToken').value
    },
    wechat: {
      enabled: document.getElementById('chWechatEnabled').checked,
      appId: document.getElementById('chWechatAppId').value,
      appSecret: document.getElementById('chWechatSecret').value
    },
    whatsapp: {
      enabled: document.getElementById('chWhatsappEnabled').checked,
      phoneNumberId: document.getElementById('chWhatsappPhoneId').value,
      accessToken: document.getElementById('chWhatsappToken').value,
      verifyToken: document.getElementById('chWhatsappVerify').value
    },
    slack: {
      enabled: document.getElementById('chSlackEnabled').checked,
      botToken: document.getElementById('chSlackToken').value,
      signingSecret: document.getElementById('chSlackSecret').value
    },
    messenger: {
      enabled: document.getElementById('chMessengerEnabled').checked,
      pageAccessToken: document.getElementById('chMessengerToken').value,
      verifyToken: document.getElementById('chMessengerVerify').value
    },
    qq: {
      enabled: document.getElementById('chQqEnabled').checked,
      httpUrl: document.getElementById('chQqHttpUrl').value,
      webhookPort: parseInt(document.getElementById('chQqWebhookPort').value) || 5683
    },
    instagram: {
      enabled: document.getElementById('chIgEnabled').checked,
      accessToken: document.getElementById('chIgToken').value
    }
  };
  // 社群發文平台
  config.facebook = {
    pageId: document.getElementById('chFbPageId').value,
    pageAccessToken: document.getElementById('chFbPageToken').value
  };
  config.twitter = {
    consumerKey: document.getElementById('chXConsumerKey').value,
    consumerSecret: document.getElementById('chXConsumerSecret').value,
    accessToken: document.getElementById('chXAccessToken').value,
    accessTokenSecret: document.getElementById('chXAccessTokenSecret').value
  };
  config.threads = {
    accessToken: document.getElementById('chThreadsToken').value
  };

  await saveConfigToBackend();
  alert(t('channels_save'));
}

async function saveConfigToBackend() {
  try {
    // 儲存到 Electron store
    if (window.autoto?.saveConfig) {
      await window.autoto.saveConfig(config);
    }
    // 同步到後端
    await fetch(`http://127.0.0.1:${backendPort}/api/config`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(config)
    });
  } catch (e) {
    console.error('Save config error:', e);
  }
}

// ==================== 記憶管理 ====================
async function addMemory() {
  const text = prompt(t('memory_add'));
  if (!text) return;

  try {
    await fetch(`http://127.0.0.1:${backendPort}/api/memory`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content: text })
    });
    loadMemories();
  } catch (e) {
    alert(t('alert_memory_fail'));
  }
}

async function loadMemories() {
  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/memory`);
    const data = await res.json();
    const list = document.getElementById('memoryList');

    if (!data.memories || data.memories.length === 0) {
      list.innerHTML = `<div class="empty-state">${t('memory_empty')}</div>`;
      return;
    }

    list.innerHTML = data.memories.map(m => {
      const sourceLabel = m.source === 'auto' ? t('memory_source_auto') : t('memory_source_manual');
      const sourceClass = m.source === 'auto' ? 'memory-source-auto' : 'memory-source-manual';
      const pinClass = m.pinned ? 'memory-pin active' : 'memory-pin';
      const pinTitle = m.pinned ? t('memory_unpin') : t('memory_pin_hint');
      return `
      <div class="memory-item ${m.pinned ? 'pinned' : ''}">
        <button class="${pinClass}" onclick="togglePin('${m.id}')" title="${pinTitle}">
          <svg viewBox="0 0 24 24" width="14" height="14"><path d="M12 2l2.09 6.26L21 9.27l-5 3.64L17.18 20 12 16.77 6.82 20 8 12.91l-5-3.64 6.91-1.01z"/></svg>
        </button>
        <div class="memory-content">
          <span class="memory-text">${escapeHtml(m.content)}</span>
          <div class="memory-meta">
            <span class="memory-badge ${sourceClass}">${sourceLabel}</span>
            <span class="memory-time">${m.timestamp || ''}</span>
          </div>
        </div>
        <button class="memory-delete" onclick="deleteMemory('${m.id}')" title="Delete">
          <svg viewBox="0 0 24 24" width="14" height="14"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6m3 0V4a2 2 0 012-2h4a2 2 0 012 2v2"/></svg>
        </button>
      </div>`;
    }).join('');
  } catch {
    // 後端未啟動
  }
}

async function deleteMemory(id) {
  try {
    await fetch(`http://127.0.0.1:${backendPort}/api/memory/${id}`, { method: 'DELETE' });
    loadMemories();
  } catch {
    alert(t('alert_delete_fail'));
  }
}

async function togglePin(id) {
  try {
    await fetch(`http://127.0.0.1:${backendPort}/api/memory/${id}/pin`, { method: 'POST' });
    loadMemories();
  } catch {
    alert(t('alert_op_fail'));
  }
}

// ==================== 日誌 ====================
let logPollTimer = null;

function addLog(text, type = '') {
  const container = document.getElementById('logContainer');
  const entry = document.createElement('div');
  entry.className = `log-entry ${type}`;
  entry.textContent = `[${new Date().toLocaleTimeString()}] ${text}`;
  container.appendChild(entry);
  container.scrollTop = container.scrollHeight;
}

async function loadLogs() {
  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/logs`);
    const data = await res.json();
    const container = document.getElementById('logContainer');
    if (!data.logs || data.logs.length === 0) {
      container.innerHTML = '<div class="log-entry">[System] ' + t('log_no_logs') + '</div>';
      return;
    }
    container.innerHTML = data.logs.map(l => {
      let cls = '';
      if (l.text.includes('Error') || l.text.includes('error')) cls = 'error';
      else if (l.text.includes('已啟動')) cls = 'success';
      return `<div class="log-entry ${cls}">[${l.time}] ${escapeHtml(l.text)}</div>`;
    }).join('');
    container.scrollTop = container.scrollHeight;
  } catch {
    // 後端未啟動
  }
}

function startLogPolling() {
  if (logPollTimer) return;
  loadLogs();
  logPollTimer = setInterval(loadLogs, 3000);
}

function stopLogPolling() {
  if (logPollTimer) {
    clearInterval(logPollTimer);
    logPollTimer = null;
  }
}

async function clearLogs() {
  try {
    await fetch(`http://127.0.0.1:${backendPort}/api/logs/clear`, { method: 'POST' });
    document.getElementById('logContainer').innerHTML = '<div class="log-entry">[System] ' + t('log_cleared') + '</div>';
  } catch {
    document.getElementById('logContainer').innerHTML = '';
    addLog(t('log_cleared'));
  }
}

// 監聽後端日誌
if (window.autoto?.onBackendLog) {
  window.autoto.onBackendLog((data) => addLog(data));
}
if (window.autoto?.onBackendStatus) {
  window.autoto.onBackendStatus((status) => {
    if (status === 'stopped') {
      addLog(t('status_offline'), 'error');
      setStatus('offline', t('status_offline'));
    }
  });
}

// ==================== 深色模式 ====================
const themeToggle = document.getElementById('themeToggle');
const themeIcon = document.getElementById('themeIcon');

// 太陽 icon（淺色模式下顯示，點擊切換到深色）
const moonPath = '<path d="M21 12.79A9 9 0 1111.21 3 7 7 0 0021 12.79z"/>';
const sunPath = '<circle cx="12" cy="12" r="5"/><line x1="12" y1="1" x2="12" y2="3"/><line x1="12" y1="21" x2="12" y2="23"/><line x1="4.22" y1="4.22" x2="5.64" y2="5.64"/><line x1="18.36" y1="18.36" x2="19.78" y2="19.78"/><line x1="1" y1="12" x2="3" y2="12"/><line x1="21" y1="12" x2="23" y2="12"/><line x1="4.22" y1="19.78" x2="5.64" y2="18.36"/><line x1="18.36" y1="5.64" x2="19.78" y2="4.22"/>';

function setTheme(dark) {
  if (dark) {
    document.documentElement.setAttribute('data-theme', 'dark');
    themeIcon.innerHTML = sunPath;
    localStorage.setItem('autoto_theme', 'dark');
  } else {
    document.documentElement.removeAttribute('data-theme');
    themeIcon.innerHTML = moonPath;
    localStorage.setItem('autoto_theme', 'light');
  }
}

themeToggle.addEventListener('click', () => {
  const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
  setTheme(!isDark);
});

// 載入儲存的主題
const savedTheme = localStorage.getItem('autoto_theme');
if (savedTheme === 'dark') {
  setTheme(true);
}

// ==================== 啟動 ====================
// 技能圖標對應表
const SKILL_ICONS = {
  exec: '<svg viewBox="0 0 24 24"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>',
  read_file: '<svg viewBox="0 0 24 24"><path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>',
  write_file: '<svg viewBox="0 0 24 24"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 013 3L7 19l-4 1 1-4L16.5 3.5z"/></svg>',
  edit_file: '<svg viewBox="0 0 24 24"><path d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7"/><path d="M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>',
  delete_file: '<svg viewBox="0 0 24 24"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6m3 0V4a2 2 0 012-2h4a2 2 0 012 2v2"/><line x1="10" y1="11" x2="10" y2="17"/><line x1="14" y1="11" x2="14" y2="17"/></svg>',
  list_dir: '<svg viewBox="0 0 24 24"><path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z"/></svg>',
  open_url: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 014 10 15.3 15.3 0 01-4 10 15.3 15.3 0 01-4-10 15.3 15.3 0 014-10z"/></svg>',
  open_app: '<svg viewBox="0 0 24 24"><rect x="3" y="3" width="18" height="18" rx="2"/><line x1="3" y1="9" x2="21" y2="9"/><line x1="9" y1="21" x2="9" y2="9"/></svg>',
  screenshot: '<svg viewBox="0 0 24 24"><path d="M23 19a2 2 0 01-2 2H3a2 2 0 01-2-2V8a2 2 0 012-2h4l2-3h6l2 3h4a2 2 0 012 2z"/><circle cx="12" cy="13" r="4"/></svg>',
  type_text: '<svg viewBox="0 0 24 24"><polyline points="4 7 4 4 20 4 20 7"/><line x1="9" y1="20" x2="15" y2="20"/><line x1="12" y1="4" x2="12" y2="20"/></svg>',
  key_press: '<svg viewBox="0 0 24 24"><rect x="2" y="4" width="20" height="16" rx="2"/><path d="M6 8h.01M10 8h.01M14 8h.01M18 8h.01M8 12h.01M12 12h.01M16 12h.01M7 16h10"/></svg>',
  web_search: '<svg viewBox="0 0 24 24"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>',
  web_fetch: '<svg viewBox="0 0 24 24"><path d="M21 12a9 9 0 01-9 9m9-9a9 9 0 00-9-9m9 9H3m9 9a9 9 0 01-9-9m9 9c1.66 0 3-4.03 3-9s-1.34-9-3-9m0 18c-1.66 0-3-4.03-3-9s1.34-9 3-9m-9 9a9 9 0 019-9"/></svg>',
  clipboard_read: '<svg viewBox="0 0 24 24"><path d="M16 4h2a2 2 0 012 2v14a2 2 0 01-2 2H6a2 2 0 01-2-2V6a2 2 0 012-2h2"/><rect x="8" y="2" width="8" height="4" rx="1"/></svg>',
  clipboard_write: '<svg viewBox="0 0 24 24"><path d="M16 4h2a2 2 0 012 2v14a2 2 0 01-2 2H6a2 2 0 01-2-2V6a2 2 0 012-2h2"/><rect x="8" y="2" width="8" height="4" rx="1"/><line x1="12" y1="11" x2="12" y2="17"/><line x1="9" y1="14" x2="15" y2="14"/></svg>',
  process_list: '<svg viewBox="0 0 24 24"><line x1="8" y1="6" x2="21" y2="6"/><line x1="8" y1="12" x2="21" y2="12"/><line x1="8" y1="18" x2="21" y2="18"/><line x1="3" y1="6" x2="3.01" y2="6"/><line x1="3" y1="12" x2="3.01" y2="12"/><line x1="3" y1="18" x2="3.01" y2="18"/></svg>',
  process_kill: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg>',
  notification: '<svg viewBox="0 0 24 24"><path d="M18 8A6 6 0 006 8c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.73 21a2 2 0 01-3.46 0"/></svg>',
  cron_list: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>',
  cron_add: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="16"/><line x1="8" y1="12" x2="16" y2="12"/></svg>',
  cron_remove: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><line x1="8" y1="12" x2="16" y2="12"/></svg>',
  memory_search: '<svg viewBox="0 0 24 24"><path d="M12 2a7 7 0 017 7c0 2.38-1.19 4.47-3 5.74V17a2 2 0 01-2 2h-4a2 2 0 01-2-2v-2.26C6.19 13.47 5 11.38 5 9a7 7 0 017-7z"/><line x1="9" y1="21" x2="15" y2="21"/></svg>',
  system_info: '<svg viewBox="0 0 24 24"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>',
  summarize: '<svg viewBox="0 0 24 24"><line x1="17" y1="10" x2="3" y2="10"/><line x1="21" y1="6" x2="3" y2="6"/><line x1="21" y1="14" x2="3" y2="14"/><line x1="17" y1="18" x2="3" y2="18"/></svg>',
  weather: '<svg viewBox="0 0 24 24"><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/><circle cx="12" cy="12" r="5"/></svg>',
  click: '<svg viewBox="0 0 24 24"><path d="M4 4l7.07 17 2.51-7.39L21 11.07z"/></svg>',
  move_mouse: '<svg viewBox="0 0 24 24"><path d="M12 2v20"/><path d="M2 12h20"/><circle cx="12" cy="12" r="3"/></svg>',
  drag_mouse: '<svg viewBox="0 0 24 24"><path d="M4 4l7 7"/><path d="M8 4H4v4"/><path d="M20 20l-7-7"/><path d="M16 20h4v-4"/></svg>',
  scroll: '<svg viewBox="0 0 24 24"><path d="M12 4v16"/><polyline points="8 8 12 4 16 8"/><polyline points="8 16 12 20 16 16"/></svg>',
  focus_app: '<svg viewBox="0 0 24 24"><rect x="3" y="3" width="18" height="18" rx="2"/><path d="M9 9h6v6H9z"/></svg>',
  screen_size: '<svg viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="12" rx="2"/><line x1="8" y1="20" x2="16" y2="20"/><line x1="12" y1="16" x2="12" y2="20"/></svg>',
  scan_media_folder: '<svg viewBox="0 0 24 24"><path d="M3 7h5l2 2h11v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><circle cx="16" cy="14" r="3"/></svg>',
  video_probe: '<svg viewBox="0 0 24 24"><rect x="3" y="5" width="18" height="14" rx="2"/><circle cx="10" cy="12" r="2"/><line x1="15" y1="10" x2="18" y2="10"/><line x1="15" y1="14" x2="18" y2="14"/></svg>',
  video_cut: '<svg viewBox="0 0 24 24"><circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><line x1="8.5" y1="8.5" x2="19" y2="19"/><line x1="8.5" y1="15.5" x2="19" y2="5"/></svg>',
  video_concat: '<svg viewBox="0 0 24 24"><rect x="3" y="6" width="7" height="12" rx="1"/><rect x="14" y="6" width="7" height="12" rx="1"/><line x1="10" y1="12" x2="14" y2="12"/></svg>',
  video_extract_audio: '<svg viewBox="0 0 24 24"><polygon points="11 5 6 9 3 9 3 15 6 15 11 19 11 5"/><path d="M15 9a5 5 0 0 1 0 6"/><path d="M18 6a9 9 0 0 1 0 12"/></svg>',
  transcribe_media: '<svg viewBox="0 0 24 24"><path d="M4 4h16v12H7l-3 3z"/><line x1="8" y1="8" x2="16" y2="8"/><line x1="8" y1="12" x2="14" y2="12"/></svg>',
  youtube_play: '<svg viewBox="0 0 24 24"><rect x="2" y="4" width="20" height="16" rx="2"/><polygon points="10 8 16 12 10 16 10 8"/></svg>',
  ig_get_posts: '<svg viewBox="0 0 24 24"><rect x="2" y="2" width="20" height="20" rx="5"/><circle cx="12" cy="12" r="5"/><circle cx="17.5" cy="6.5" r="1.5"/></svg>',
  ig_get_comments: '<svg viewBox="0 0 24 24"><rect x="2" y="2" width="20" height="20" rx="5"/><path d="M12 8v4M10 16h4"/></svg>',
  ig_reply_comment: '<svg viewBox="0 0 24 24"><rect x="2" y="2" width="20" height="20" rx="5"/><polyline points="9 14 12 17 15 14"/><line x1="12" y1="8" x2="12" y2="17"/></svg>',
  ig_post_comment: '<svg viewBox="0 0 24 24"><rect x="2" y="2" width="20" height="20" rx="5"/><line x1="12" y1="8" x2="12" y2="16"/><line x1="8" y1="12" x2="16" y2="12"/></svg>',
  ig_delete_comment: '<svg viewBox="0 0 24 24"><rect x="2" y="2" width="20" height="20" rx="5"/><line x1="8" y1="12" x2="16" y2="12"/></svg>',
  ig_publish_media: '<svg viewBox="0 0 24 24"><rect x="2" y="2" width="20" height="20" rx="5"/><polygon points="10 8 16 12 10 16 10 8"/></svg>',
  fb_post: '<svg viewBox="0 0 24 24"><path d="M18 2h-3a5 5 0 00-5 5v3H7v4h3v8h4v-8h3l1-4h-4V7a1 1 0 011-1h3z"/></svg>',
  x_post: '<svg viewBox="0 0 24 24"><path d="M4 4l6.5 8L4 20h2l5.5-6.8L16 20h4l-7-8.5L19.5 4H18l-5 6.2L9 4H4z"/></svg>',
  threads_publish: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" stroke-width="2"/><path d="M12 8c2.2 0 4 1.8 4 4s-1.8 4-4 4"/></svg>',
  web_scrape_structured: '<svg viewBox="0 0 24 24"><rect x="3" y="3" width="18" height="18" rx="2"/><line x1="7" y1="8" x2="17" y2="8"/><line x1="7" y1="12" x2="13" y2="12"/><line x1="7" y1="16" x2="15" y2="16"/></svg>',
  web_download_file: '<svg viewBox="0 0 24 24"><path d="M12 3v12"/><polyline points="8 11 12 15 16 11"/><path d="M20 21H4"/></svg>',
};
const SKILL_NAMES = {
  exec: '終端命令', read_file: '讀取檔案', write_file: '寫入檔案',
  edit_file: '編輯檔案', delete_file: '刪除檔案', list_dir: '瀏覽資料夾',
  open_url: '開啟網頁', open_app: '開啟應用', screenshot: '螢幕截圖',
  type_text: '模擬打字', key_press: '鍵盤快捷鍵',
  web_search: '網頁搜尋', web_fetch: '網頁擷取',
  clipboard_read: '讀取剪貼簿', clipboard_write: '寫入剪貼簿',
  process_list: '程序列表', process_kill: '終止程序',
  notification: '系統通知',
  cron_list: '排程列表', cron_add: '新增排程', cron_remove: '移除排程',
  memory_search: '搜尋記憶', system_info: '系統資訊',
  summarize: '文字摘要', weather: '天氣查詢',
  click: '點擊螢幕', youtube_play: 'YouTube 播放',
  move_mouse: '移動滑鼠', drag_mouse: '拖曳滑鼠', scroll: '滾動畫面',
  focus_app: '切到前景 App', screen_size: '螢幕尺寸',
  scan_media_folder: '掃描媒體資料夾', video_probe: '影片資訊探測',
  video_cut: '影片裁切', video_concat: '影片串接',
  video_extract_audio: '抽出音訊', transcribe_media: '媒體轉字幕',
  ig_get_posts: 'IG 貼文列表', ig_get_comments: 'IG 留言列表',
  ig_reply_comment: 'IG 回覆留言', ig_post_comment: 'IG 發表留言',
  ig_delete_comment: 'IG 刪除留言',
  ig_publish_media: 'IG 發佈貼文',
  fb_post: 'Facebook 發文',
  x_post: 'X/Twitter 發推',
  threads_publish: 'Threads 發文',
  web_scrape_structured: '結構化爬蟲',
  web_download_file: '下載檔案',
};

async function loadSkills() {
  const grid = document.getElementById('skillsGrid');
  try {
    const [skillsRes, customRes] = await Promise.all([
      fetch(`http://127.0.0.1:${backendPort}/api/skills`),
      fetch(`http://127.0.0.1:${backendPort}/api/custom-tools`)
    ]);
    const data = await skillsRes.json();
    const customData = await customRes.json();
    const customNames = new Set((customData.tools || []).map(t => t.name));

    if (!data.skills || data.skills.length === 0) {
      grid.innerHTML = '<div class="empty-state">' + t('skills_empty') + '</div>';
      return;
    }
    grid.innerHTML = data.skills.map(s => {
      const isCustom = customNames.has(s.name);
      const icon = SKILL_ICONS[s.name] || '<svg viewBox="0 0 24 24"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/></svg>';
      const label = SKILL_NAMES[s.name] || s.name;
      const checked = s.enabled ? 'checked' : '';
      const customClass = isCustom ? ' skill-card-custom' : '';
      const deleteBtn = isCustom ? `<button class="skill-card-delete" onclick="event.stopPropagation();deleteCustomTool('${s.name}')" title="Delete"><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>` : '';
      return `
        <div class="skill-card${customClass}">
          ${deleteBtn}
          ${icon}
          <h4>${escapeHtml(label)}${isCustom ? ' <span style="font-size:10px;color:var(--primary)">' + t('skills_custom') + '</span>' : ''}</h4>
          <p>${escapeHtml(s.description.substring(0, 40))}${s.description.length > 40 ? '...' : ''}</p>
          <label class="toggle">
            <input type="checkbox" ${checked} onchange="toggleSkill('${s.name}', this.checked)">
            <span class="toggle-slider"></span>
          </label>
        </div>`;
    }).join('');
  } catch {
    grid.innerHTML = '<div class="empty-state">' + t('skills_offline') + '</div>';
  }
}

async function toggleSkill(name, enabled) {
  try {
    await fetch(`http://127.0.0.1:${backendPort}/api/skills`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, enabled })
    });
  } catch {
    alert(t('alert_skill_fail'));
  }
}

// ==================== 自訂技能 ====================
function showAddSkillForm() {
  document.getElementById('customToolForm').style.display = 'block';
  document.getElementById('customToolFormTitle').textContent = t('skills_custom_title');
  document.getElementById('ctName').value = '';
  document.getElementById('ctDesc').value = '';
  document.getElementById('ctCommand').value = '';
  document.getElementById('ctParams').value = '';
  document.getElementById('ctName').disabled = false;
}

function hideAddSkillForm() {
  document.getElementById('customToolForm').style.display = 'none';
}

async function saveCustomTool() {
  const name = document.getElementById('ctName').value.trim();
  const desc = document.getElementById('ctDesc').value.trim();
  const command = document.getElementById('ctCommand').value.trim();
  const paramsText = document.getElementById('ctParams').value.trim();

  if (!name || !command) {
    alert(t('ct_validate_required'));
    return;
  }
  if (!/^[a-z_][a-z0-9_]*$/.test(name)) {
    alert(t('ct_validate_name'));
    return;
  }

  const params = [];
  if (paramsText) {
    for (const seg of paramsText.split(',')) {
      const parts = seg.trim().split('|');
      if (parts[0]) {
        params.push({ name: parts[0].trim(), description: parts[1]?.trim() || parts[0].trim(), required: true });
      }
    }
  }

  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/custom-tools`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, description: desc || name, command, params })
    });
    const data = await res.json();
    if (data.success) {
      hideAddSkillForm();
      loadSkills();
    } else {
      alert((data.error || t('alert_save_fail')));
    }
  } catch {
    alert(t('alert_connect_fail'));
  }
}

async function deleteCustomTool(name) {
  if (!confirm(t('skills_ct_delete_confirm', { name }))) return;
  try {
    await fetch(`http://127.0.0.1:${backendPort}/api/custom-tools/${encodeURIComponent(name)}`, {
      method: 'DELETE'
    });
    loadSkills();
  } catch {
    alert(t('alert_delete_fail'));
  }
}

// 每次載入時清空聊天（避免瀏覽器快取舊 DOM）
function showWelcome() {
  chatMessages.innerHTML = `
    <div class="welcome-msg">
      <h2>${t('chat_welcome_title')}</h2>
      <p>${t('chat_welcome_sub')}</p>
      <div class="quick-actions">
        <button class="quick-btn" onclick="sendQuick('你好，介紹一下你自己')">${t('chat_quick_hello')}</button>
        <button class="quick-btn" onclick="sendQuick('幫我查一下今天天氣')">${t('chat_quick_weather')}</button>
        <button class="quick-btn" onclick="sendQuick('打開瀏覽器')">${t('chat_quick_browser')}</button>
      </div>
    </div>`;
  msgCounter = 0;
}

function clearChat() {
  showWelcome();
  msgCounter = 0;
}

async function newConversation() {
  const fallbackSession = {
    id: 'web-' + Date.now(),
    title: '',
    messageCount: 0
  };
  let session = fallbackSession;
  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/sessions`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session_id: fallbackSession.id })
    });
    const data = await res.json();
    if (data.success && data.session) {
      session = data.session;
    }
  } catch {
    // 後端不可用時仍保留前端草稿 session
  }
  currentSessionId = session.id;
  currentSessionIsDraft = true;
  saveLastSessionId(session.id);
  showWelcome();
  document.getElementById('chatTitle').textContent = getConversationDisplayTitle(session);
  await loadConversations();
  document.querySelectorAll('.conv-item').forEach(el => {
    el.classList.toggle('active', el.dataset.sessionId === session.id);
  });
}

async function loadConversations() {
  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/sessions`);
    const data = await res.json();
    const list = document.getElementById('convList');
    if (!data.sessions || data.sessions.length === 0) {
      list.innerHTML = `<div style="padding:12px;color:var(--text-secondary);font-size:12px;text-align:center">${t('conv_empty')}</div>`;
      return [];
    }
    list.innerHTML = '';
    data.sessions.forEach(s => {
      const displayTitle = getConversationDisplayTitle(s);
      const div = document.createElement('div');
      div.className = 'conv-item' + (s.id === currentSessionId ? ' active' : '');
      div.dataset.sessionId = s.id;
      div.innerHTML = `
        <span class="conv-item-title">${escapeHtml(displayTitle)}</span>
        <button class="conv-item-delete" onclick="event.stopPropagation();deleteConversation('${s.id}')" title="Delete">
          <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
        </button>`;
      div.addEventListener('click', () => switchConversation(s.id, displayTitle));
      list.appendChild(div);
    });
    return data.sessions;
  } catch {
    // 後端未啟動時靜默失敗
    return [];
  }
}

async function switchConversation(sessionId, title, options = {}) {
  currentSessionId = sessionId;
  saveLastSessionId(sessionId);
  document.getElementById('chatTitle').textContent = title || t('chat_title');
  // 更新 active 狀態
  document.querySelectorAll('.conv-item').forEach(el => {
    el.classList.toggle('active', el.dataset.sessionId === sessionId);
  });
  // 載入訊息
  chatMessages.innerHTML = '';
  msgCounter = 0;
  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/sessions/${encodeURIComponent(sessionId)}/messages`);
    const data = await res.json();
    if (data.messages && data.messages.length > 0) {
      currentSessionIsDraft = false;
      data.messages.forEach(m => {
        appendMessage(m.role === 'user' ? 'user' : 'bot', m.content);
      });
    } else {
      currentSessionIsDraft = true;
      showWelcome();
    }
  } catch {
    currentSessionIsDraft = true;
    showWelcome();
  }
  // 重新載入列表以更新 active
  if (!options.skipReload) {
    loadConversations();
  }
}

async function deleteConversation(sessionId) {
  try {
    await fetch(`http://127.0.0.1:${backendPort}/api/session/clear`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session_id: sessionId })
    });
    // 如果刪的是當前對話，回到新對話
    if (sessionId === currentSessionId) {
      resetConversationState();
    }
    loadConversations();
  } catch {}
}

function toggleConvSidebar() {
  const sidebar = document.getElementById('convSidebar');
  convSidebarVisible = !convSidebarVisible;
  sidebar.classList.toggle('collapsed', !convSidebarVisible);
}
// ==================== 清除所有對話紀錄 ====================
async function clearAllSessions() {
  if (!confirm(t('api_clear_confirm'))) return;
  try {
    await fetch(`http://127.0.0.1:${backendPort}/api/session/clear`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ clear_all: true })
    });
    resetConversationState();
    loadConversations();
    alert(t('all_cleared'));
  } catch {
    alert(t('alert_delete_fail'));
  }
}

// ==================== 系統診斷 ====================
async function runDiagnostics() {
  const container = document.getElementById('diagResults');
  const btn = document.getElementById('diagRunBtn');
  btn.disabled = true;
  document.getElementById('diagBtnText').textContent = t('diag_running');
  container.innerHTML = '<div class="diag-loading"><svg viewBox="0 0 24 24"><path d="M21 12a9 9 0 11-6.219-8.56"/></svg><p>' + t('diag_checking_env') + '</p></div>';

  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/diagnostics`);
    const data = await res.json();
    const results = data.results || [];

    const counts = { ok: 0, warn: 0, error: 0 };
    results.forEach(r => counts[r.status]++);

    const icons = {
      ok: '<svg viewBox="0 0 24 24"><polyline points="20 6 9 17 4 12"/></svg>',
      warn: '<svg viewBox="0 0 24 24"><path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>',
      error: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg>'
    };

    const categoryLabels = {
      system: t('diag_cat_system'),
      package: t('diag_cat_package'),
      config: t('diag_cat_config'),
      network: t('diag_cat_network'),
      channel: t('diag_cat_channel'),
      permission: t('diag_cat_permission')
    };

    // 按 category 分組
    const grouped = {};
    results.forEach(r => {
      const cat = r.category || 'other';
      if (!grouped[cat]) grouped[cat] = [];
      grouped[cat].push(r);
    });

    let html = `<div class="diag-summary">
      <div class="diag-stat"><div class="diag-stat-num ok">${counts.ok}</div><div class="diag-stat-label">${t('diag_stat_pass')}</div></div>
      <div class="diag-stat"><div class="diag-stat-num warn">${counts.warn}</div><div class="diag-stat-label">${t('diag_stat_warn')}</div></div>
      <div class="diag-stat"><div class="diag-stat-num error">${counts.error}</div><div class="diag-stat-label">${t('diag_stat_error')}</div></div>
    </div>`;

    const catOrder = ['system', 'package', 'config', 'network', 'channel', 'permission'];
    for (const cat of catOrder) {
      const items = grouped[cat];
      if (!items) continue;
      html += `<div class="diag-category">${categoryLabels[cat] || cat}</div>`;
      for (const r of items) {
        html += `<div class="diag-item">
          <div class="diag-icon ${r.status}">${icons[r.status]}</div>
          <div class="diag-info">
            <div class="diag-name">${escapeHtml(r.name)}</div>
            <div class="diag-detail">${escapeHtml(r.detail)}</div>
            ${r.fix ? `<div class="diag-fix">${escapeHtml(r.fix)}</div>` : ''}
          </div>
        </div>`;
      }
    }

    container.innerHTML = html;
  } catch (e) {
    container.innerHTML = '<div class="empty-state">' + t('alert_connect_fail') + '</div>';
  }

  btn.disabled = false;
  document.getElementById('diagBtnText').textContent = t('diag_rerun');
}

// ==================== 版本更新 ====================
async function loadVersionInfo() {
  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/version`);
    const data = await res.json();
    document.getElementById('aboutCurrentVer').textContent = data.version + (data.commit ? ` (${data.commit})` : '');
  } catch {}
}

async function checkForUpdate() {
  const btn = document.getElementById('aboutCheckBtn');
  const statusEl = document.getElementById('aboutUpdateStatus');
  btn.disabled = true;
  btn.textContent = t('about_checking');
  statusEl.textContent = t('about_checking');

  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/update/check`);
    const data = await res.json();

    if (data.error) {
      statusEl.textContent = data.error;
      btn.disabled = false;
      btn.textContent = t('about_recheck');
      return;
    }

    document.getElementById('aboutCurrentVer').textContent = 'v1.0.0 (' + data.currentCommit + ')';
    document.getElementById('aboutLatestVer').textContent = data.latestCommit;

    if (data.hasUpdate) {
      statusEl.textContent = t('about_has_update', { n: data.behindCount || '?' });
      document.getElementById('aboutUpdateBtn').style.display = 'inline-flex';
      if (data.changelog) {
        document.getElementById('aboutUpdateInfo').style.display = 'block';
        document.getElementById('aboutUpdateTitle').textContent = t('about_changelog');
        document.getElementById('aboutUpdateBody').textContent = data.changelog;
      }
    } else {
      statusEl.textContent = t('about_up_to_date');
      document.getElementById('aboutUpdateBtn').style.display = 'none';
      document.getElementById('aboutUpdateInfo').style.display = 'none';
    }
  } catch {
    statusEl.textContent = t('chat_error_connect');
  }
  btn.disabled = false;
  btn.textContent = t('about_recheck');
}

async function doUpdate() {
  const btn = document.getElementById('aboutUpdateBtn');
  const statusEl = document.getElementById('aboutUpdateStatus');
  btn.disabled = true;
  btn.textContent = t('about_updating');
  statusEl.textContent = t('about_updating');

  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/update/apply`, { method: 'POST' });
    const data = await res.json();
    if (data.success) {
      statusEl.textContent = data.message;
      btn.style.display = 'none';
      document.getElementById('aboutUpdateInfo').style.display = 'none';
      alert(t('about_update_done'));
    } else {
      statusEl.textContent = (data.error || '');
      btn.disabled = false;
      btn.textContent = t('about_update_now');
    }
  } catch {
    statusEl.textContent = t('alert_connect_fail');
    btn.disabled = false;
    btn.textContent = t('about_update_now');
  }
}


// ==================== 排程管理（Orbit 風格） ====================

let _schedTasks = [];
let _schedSelected = null;
let _schedEditorMode = 'simple'; // simple | advanced

async function loadSchedulerPage() {
  try {
    const res = await fetch(`${API}/schedules`);
    const data = await res.json();
    _schedTasks = data.schedules || [];
  } catch { _schedTasks = []; }
  renderSchedTaskList();
  if (_schedSelected) {
    const task = _schedTasks.find(t => t.id === _schedSelected);
    if (task) renderSchedEditor(task);
    else { _schedSelected = null; renderSchedEditorEmpty(); }
  } else {
    renderSchedEditorEmpty();
  }
}

function renderSchedTaskList() {
  const el = document.getElementById('schedTaskList');
  if (!el) return;
  if (_schedTasks.length === 0) {
    el.innerHTML = '<div class="empty-state" style="font-size:13px;padding:20px">尚無排程任務</div>';
    return;
  }
  el.innerHTML = _schedTasks.map(s => {
    const isActive = s.id === _schedSelected;
    const model = s.model && s.model !== 'default' ? `<span class="sched-task-model">${escapeHtml(s.model)}</span>` : '';
    const schedDesc = s.type === 'simple' ? _schedSimpleDesc(s.schedule) : s.expression;
    return `<div class="sched-task-item ${isActive ? 'active' : ''}" onclick="schedSelectTask('${s.id}')">
      <div class="sched-task-info">
        <div class="sched-task-name">${escapeHtml(s.name)}</div>
        <div class="sched-task-sub">${model} ${escapeHtml(schedDesc || '')}</div>
      </div>
      <div class="sched-task-actions">
        <label class="toggle" style="transform:scale(0.75)" onclick="event.stopPropagation()">
          <input type="checkbox" ${s.enabled ? 'checked' : ''} onchange="schedToggle('${s.id}')">
          <span class="toggle-slider"></span>
        </label>
        <button onclick="event.stopPropagation();schedRunNow('${s.id}')" title="立即執行">執行</button>
        <button onclick="event.stopPropagation();schedDelete('${s.id}')" title="刪除">刪除</button>
      </div>
    </div>`;
  }).join('');
}

function _schedSimpleDesc(sched) {
  if (!sched) return '';
  const mode = sched.mode || 'daily';
  const time = sched.time || '09:00';
  const days = ['一','二','三','四','五','六','日'];
  if (mode === 'interval') return `每 ${sched.interval_minutes || 30} 分鐘`;
  if (mode === 'daily') return `每天 ${time}`;
  if (mode === 'weekly') {
    const wd = (sched.weekdays || []).map(d => days[d]).join(', ');
    return `每週 ${wd} ${time}`;
  }
  if (mode === 'monthly') return `每月 ${sched.month_day || 1} 日 ${time}`;
  return '';
}

function renderSchedEditorEmpty() {
  const el = document.getElementById('schedEditor');
  if (el) el.innerHTML = '<div class="sched-editor-empty"><p>選擇一個任務來編輯，或點「+ 新增任務」新增</p></div>';
}

function schedSelectTask(id) {
  _schedSelected = id;
  renderSchedTaskList();
  const task = _schedTasks.find(t => t.id === id);
  if (task) renderSchedEditor(task);
}

function schedNewTask() {
  _schedSelected = '__new__';
  renderSchedTaskList();
  renderSchedEditor({
    id: '__new__', name: '', description: '', type: 'simple',
    schedule: { mode: 'daily', time: '09:00', weekdays: [0], interval_minutes: 30, month_day: 1 },
    expression: '', action: 'agent_message', payload: { message: '' }, model: 'default', enabled: true
  });
}

function renderSchedEditor(task) {
  const el = document.getElementById('schedEditor');
  if (!el) return;
  const isNew = task.id === '__new__';
  const sched = task.schedule || {};
  const mode = sched.mode || 'daily';
  const time = sched.time || '09:00';
  const weekdays = sched.weekdays || [0];
  const intervalMin = sched.interval_minutes || 30;
  const monthDay = sched.month_day || 1;
  const isSimple = task.type !== 'cron';
  _schedEditorMode = isSimple ? 'simple' : 'advanced';

  const days = ['一','二','三','四','五','六','日'];
  const prompt = task.action === 'agent_message' ? (task.payload?.message || '') : (task.payload?.command || '');

  el.innerHTML = `
    <h3>${isNew ? '新增任務' : '編輯任務'}</h3>
    <div class="sched-subtitle">設定你的排程 AI 任務</div>

    <div class="form-group">
      <label>任務名稱 <span class="required">*</span></label>
      <input type="text" id="se-name" value="${escapeHtml(task.name)}" placeholder="例：每日天氣報告">
    </div>
    <div class="form-group">
      <label>描述（選填）</label>
      <input type="text" id="se-desc" value="${escapeHtml(task.description || '')}" placeholder="任務描述">
    </div>

    <div class="form-group">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">
        <label style="margin:0">排程 <span class="required">*</span></label>
        <div class="sched-adv-toggle">
          <button class="sched-adv-btn ${isSimple ? 'active' : ''}" onclick="schedSetMode('simple')">簡易</button>
          <button class="sched-adv-btn ${!isSimple ? 'active' : ''}" onclick="schedSetMode('advanced')">進階</button>
        </div>
      </div>

      <div id="se-simple" style="${isSimple ? '' : 'display:none'}">
        <div class="sched-mode-tabs">
          <button class="sched-mode-tab ${mode === 'interval' ? 'active' : ''}" onclick="schedModeTab('interval')">間隔</button>
          <button class="sched-mode-tab ${mode === 'daily' ? 'active' : ''}" onclick="schedModeTab('daily')">每日</button>
          <button class="sched-mode-tab ${mode === 'weekly' ? 'active' : ''}" onclick="schedModeTab('weekly')">每週</button>
          <button class="sched-mode-tab ${mode === 'monthly' ? 'active' : ''}" onclick="schedModeTab('monthly')">每月</button>
        </div>

        <div id="se-interval-fields" style="${mode === 'interval' ? '' : 'display:none'}">
          <div style="display:flex;align-items:center;gap:8px;margin:10px 0">
            每 <input type="number" id="se-interval" value="${intervalMin}" min="1" style="width:70px"> 分鐘
          </div>
        </div>

        <div id="se-time-fields" style="${mode === 'interval' ? 'display:none' : ''}">
          <div style="display:flex;align-items:center;gap:8px;margin:10px 0">
            時間 <input type="time" id="se-time" value="${time}" style="width:120px">
            ${mode === 'weekly' ? `每 <select id="se-week-every" style="width:60px"><option>1</option><option>2</option></select> 週` : ''}
            ${mode === 'monthly' ? `日 <input type="number" id="se-monthday" value="${monthDay}" min="1" max="31" style="width:60px">` : ''}
          </div>
        </div>

        <div id="se-weekday-picker" style="${mode === 'weekly' ? '' : 'display:none'}">
          <div style="font-size:12px;color:var(--text-secondary);margin-bottom:6px">選擇星期</div>
          <div class="sched-weekdays">
            ${days.map((d, i) => `<button class="sched-weekday ${weekdays.includes(i) ? 'active' : ''}" onclick="schedToggleDay(this,${i})">${d}</button>`).join('')}
          </div>
        </div>

        <div class="sched-summary" id="se-summary">${_schedSimpleDesc({mode, time, weekdays, interval_minutes: intervalMin, month_day: monthDay})}</div>
      </div>

      <div id="se-advanced" style="${!isSimple ? '' : 'display:none'}">
        <input type="text" id="se-cron" value="${escapeHtml(task.expression || '')}" placeholder="0 9 * * * （分 時 日 月 週）">
        <small style="color:var(--text-secondary)">Cron 表達式：分 時 日 月 週（0=Sun）</small>
      </div>
    </div>

    <div class="form-group">
      <label>模型</label>
      <select id="se-model">
        <option value="default" ${task.model === 'default' ? 'selected' : ''}>預設（使用全域設定）</option>
        <option value="claude" ${task.model === 'claude' ? 'selected' : ''}>Claude</option>
        <option value="gemini" ${task.model === 'gemini' ? 'selected' : ''}>Gemini</option>
        <option value="gpt-4o" ${task.model === 'gpt-4o' ? 'selected' : ''}>GPT-4o</option>
        <option value="deepseek" ${task.model === 'deepseek' ? 'selected' : ''}>DeepSeek</option>
      </select>
    </div>

    <div class="form-group">
      <label>動作</label>
      <select id="se-action" onchange="schedActionChange()">
        <option value="agent_message" ${task.action === 'agent_message' ? 'selected' : ''}>發送訊息給 AI</option>
        <option value="command" ${task.action === 'command' ? 'selected' : ''}>執行 Shell 指令</option>
      </select>
    </div>

    <div class="form-group">
      <label>${task.action === 'command' ? 'Shell 指令' : 'AI 指令'} <span class="required">*</span></label>
      <textarea id="se-prompt" placeholder="${task.action === 'command' ? '例：cd /project && git pull' : '例：今天台北天氣如何？'}">${escapeHtml(prompt)}</textarea>
    </div>

    <div class="sched-editor-actions">
      <button class="btn-sm" onclick="renderSchedEditorEmpty();_schedSelected=null;renderSchedTaskList()">取消</button>
      <button class="btn-primary" onclick="schedSave('${task.id}')">儲存</button>
    </div>
  `;
}

function schedSetMode(mode) {
  _schedEditorMode = mode;
  document.getElementById('se-simple').style.display = mode === 'simple' ? '' : 'none';
  document.getElementById('se-advanced').style.display = mode === 'advanced' ? '' : 'none';
  document.querySelectorAll('.sched-adv-btn').forEach(b => b.classList.toggle('active', (b.textContent.trim() === '簡易' && mode === 'simple') || (b.textContent.trim() === '進階' && mode === 'advanced')));
}

function schedModeTab(mode) {
  document.querySelectorAll('.sched-mode-tab').forEach(b => b.classList.remove('active'));
  event.target.classList.add('active');
  document.getElementById('se-interval-fields').style.display = mode === 'interval' ? '' : 'none';
  document.getElementById('se-time-fields').style.display = mode === 'interval' ? 'none' : '';
  document.getElementById('se-weekday-picker').style.display = mode === 'weekly' ? '' : 'none';
  schedUpdateSummary();
}

function schedToggleDay(btn, day) {
  btn.classList.toggle('active');
  schedUpdateSummary();
}

function schedUpdateSummary() {
  const modeMap = {'間隔':'interval','每日':'daily','每週':'weekly','每月':'monthly'};
  const rawText = document.querySelector('.sched-mode-tab.active')?.textContent.trim().split(' ').pop() || '每日';
  const mode = modeMap[rawText] || 'daily';
  const time = document.getElementById('se-time')?.value || '09:00';
  const weekdays = [...document.querySelectorAll('.sched-weekday.active')].map((_, i) => i);
  const interval = document.getElementById('se-interval')?.value || 30;
  const monthDay = document.getElementById('se-monthday')?.value || 1;
  const el = document.getElementById('se-summary');
  if (el) el.textContent = _schedSimpleDesc({ mode, time, weekdays, interval_minutes: interval, month_day: monthDay });
}

function schedActionChange() {
  const action = document.getElementById('se-action').value;
  const label = action === 'command' ? 'Shell 指令' : 'AI 指令';
  const ph = action === 'command' ? '例：cd /project && git pull' : '例：今天台北天氣如何？';
  const textarea = document.getElementById('se-prompt');
  textarea.placeholder = ph;
  textarea.previousElementSibling.innerHTML = `${label} <span class="required">*</span>`;
}

async function schedSave(taskId) {
  const name = document.getElementById('se-name').value.trim();
  const desc = document.getElementById('se-desc').value.trim();
  const action = document.getElementById('se-action').value;
  const prompt = document.getElementById('se-prompt').value.trim();
  const model = document.getElementById('se-model').value;
  if (!name || !prompt) { alert('請填寫名稱和內容'); return; }

  const body = {
    name, description: desc, action, model,
    payload: action === 'agent_message' ? { message: prompt } : { command: prompt },
  };

  if (_schedEditorMode === 'advanced') {
    body.type = 'cron';
    body.expression = document.getElementById('se-cron').value.trim();
  } else {
    body.type = 'simple';
    const _modeMap = {'間隔':'interval','每日':'daily','每週':'weekly','每月':'monthly'};
    const _rawText = document.querySelector('.sched-mode-tab.active')?.textContent.trim().split(' ').pop() || '每日';
    const activeMode = _modeMap[_rawText] || 'daily';
    const weekdays = [];
    document.querySelectorAll('.sched-weekday').forEach((btn, i) => { if (btn.classList.contains('active')) weekdays.push(i); });
    body.schedule = {
      mode: activeMode,
      time: document.getElementById('se-time')?.value || '09:00',
      weekdays,
      interval_minutes: parseInt(document.getElementById('se-interval')?.value) || 30,
      month_day: parseInt(document.getElementById('se-monthday')?.value) || 1,
    };
  }

  try {
    if (taskId === '__new__') {
      const res = await fetch(`${API}/schedules`, { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
      const data = await res.json();
      if (data.success) _schedSelected = data.schedule.id;
    } else {
      await fetch(`${API}/schedules/${taskId}`, { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
    }
    loadSchedulerPage();
  } catch (e) { alert('Error: ' + e.message); }
}

async function schedToggle(id) {
  await fetch(`${API}/schedules/${id}/toggle`, { method: 'POST' });
  loadSchedulerPage();
}

async function schedRunNow(id) {
  await fetch(`${API}/schedules/${id}/run`, { method: 'POST' });
  alert('已觸發執行');
}

async function schedDelete(id) {
  if (!confirm('確定刪除此排程？')) return;
  await fetch(`${API}/schedules/${id}`, { method: 'DELETE' });
  if (_schedSelected === id) _schedSelected = null;
  loadSchedulerPage();
}

async function schedShowLogs() {
  const el = document.getElementById('schedEditor');
  if (!el) return;
  _schedSelected = null;
  renderSchedTaskList();
  try {
    const res = await fetch(`${API}/schedules/logs?limit=100`);
    const data = await res.json();
    const logs = data.logs || [];
    if (logs.length === 0) {
      el.innerHTML = '<h3>執行紀錄</h3><div class="empty-state">尚無執行紀錄</div>';
      return;
    }
    let html = `<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px">
      <h3>執行紀錄</h3>
      <button class="btn-sm btn-danger" onclick="schedClearLogs()">清除紀錄</button>
    </div>`;
    for (const log of logs.reverse()) {
      const time = new Date(log.timestamp).toLocaleString();
      html += `<div class="sched-log-item">
        <div class="sched-log-status ${log.status}"></div>
        <div class="sched-log-info">
          <div class="sched-log-name">${escapeHtml(log.name)}</div>
          <div class="sched-log-detail">${escapeHtml(log.detail || '')}</div>
        </div>
        <div class="sched-log-time">${time}</div>
      </div>`;
    }
    el.innerHTML = html;
  } catch (e) {
    el.innerHTML = `<div class="error-text">載入失敗: ${e.message}</div>`;
  }
}

async function schedClearLogs() {
  await fetch(`${API}/schedules/logs`, { method: 'DELETE' });
  schedShowLogs();
}

// ==================== 權限沙盒 ====================

function getPresetBasePermission(presetName, presetRules) {
  const preset = presetRules?.[presetName] || presetRules?.full || {};
  return {
    allowed: preset.default_allow !== false,
    confirm: preset.default_confirm === true,
    rate_limit: preset.default_rate_limit || 0,
    path_whitelist: [],
    path_blacklist: presetName === 'full' ? [] : ['/System', '/usr', '/bin', '/sbin', 'C:\\Windows', 'C:\\Program Files']
  };
}

function getEffectivePermission(toolName, permissionData) {
  const presetName = permissionData?.preset || 'full';
  const presetRules = permissionData?.preset_rules || {};
  const preset = presetRules[presetName] || {};
  const effective = {
    ...getPresetBasePermission(presetName, presetRules),
    ...(preset.overrides?.[toolName] || {}),
    ...((permissionData?.custom || {})[toolName] || {})
  };
  return effective;
}

function renderPermissionToolRow(tool, permissionData) {
  const toolName = tool.name;
  const effective = getEffectivePermission(toolName, permissionData);
  const hasCustom = toolName in (permissionData.custom || {});
  const custom = permissionData.custom?.[toolName] || {};
  return `<div style="padding:12px;border:1px solid var(--border);border-radius:10px;margin-bottom:10px;background:var(--bg-card)">
    <div style="display:flex;justify-content:space-between;align-items:center;gap:12px;margin-bottom:10px">
      <div>
        <div style="font-size:14px;font-weight:600">${SKILL_NAMES[toolName] || toolName}</div>
        <div style="font-size:11px;color:var(--text-secondary);margin-top:2px">${escapeHtml(tool.description || toolName)}</div>
      </div>
      <span style="font-size:11px;padding:4px 8px;border-radius:999px;background:${hasCustom ? 'rgba(79,110,247,0.12)' : 'rgba(107,114,128,0.12)'};color:${hasCustom ? 'var(--primary)' : 'var(--text-secondary)'}">${hasCustom ? '自訂中' : '跟隨預設'}</span>
    </div>
    <div style="display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px;margin-bottom:10px">
      <label style="font-size:12px;color:var(--text-secondary)">
        <div style="margin-bottom:6px">允許執行</div>
        <select id="perm-allowed-${toolName}">
          <option value="true" ${effective.allowed ? 'selected' : ''}>允許</option>
          <option value="false" ${!effective.allowed ? 'selected' : ''}>禁止</option>
        </select>
      </label>
      <label style="font-size:12px;color:var(--text-secondary)">
        <div style="margin-bottom:6px">需要確認</div>
        <select id="perm-confirm-${toolName}">
          <option value="false" ${!effective.confirm ? 'selected' : ''}>否</option>
          <option value="true" ${effective.confirm ? 'selected' : ''}>是</option>
        </select>
      </label>
      <label style="font-size:12px;color:var(--text-secondary)">
        <div style="margin-bottom:6px">每分鐘上限</div>
        <input id="perm-rate-${toolName}" type="number" min="0" value="${effective.rate_limit || 0}" placeholder="0">
      </label>
    </div>
    <div style="display:flex;justify-content:space-between;align-items:center;gap:10px">
      <div style="font-size:11px;color:var(--text-secondary)">
        目前生效：${effective.allowed ? '允許' : '禁止'} / ${effective.confirm ? '需確認' : '免確認'} / ${effective.rate_limit ? effective.rate_limit + '/min' : '無上限'}
      </div>
      <div style="display:flex;gap:8px">
        <button class="btn-secondary" style="padding:4px 10px;font-size:12px" onclick="resetToolPerm('${toolName}')" ${hasCustom ? '' : 'disabled'}>重設</button>
        <button class="btn-primary" style="padding:4px 10px;font-size:12px" onclick="saveToolPerm('${toolName}')">儲存</button>
      </div>
    </div>
    ${hasCustom ? `<div style="font-size:11px;color:var(--text-secondary);margin-top:8px">自訂覆蓋：allowed=${custom.allowed !== undefined ? custom.allowed : 'preset'} / confirm=${custom.confirm !== undefined ? custom.confirm : 'preset'} / rate=${custom.rate_limit !== undefined ? custom.rate_limit : 'preset'}</div>` : ''}
  </div>`;
}

async function loadPermissions() {
  const el = document.getElementById('permToolList');
  const sel = document.getElementById('permPreset');
  if (!el || !sel) return;
  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/permissions`);
    const data = await res.json();
    sel.value = data.preset || 'full';
    // 顯示各工具的有效權限
    const skillsRes = await fetch(`http://127.0.0.1:${backendPort}/api/skills`);
    const skillsData = await skillsRes.json();
    const tools = skillsData.skills || [];
    if (tools.length === 0) { el.innerHTML = ''; return; }
    el.innerHTML = '<div style="font-size:13px;color:var(--text-secondary);margin-bottom:12px">' + t('permissions_tool_hint') + '</div>' +
      tools.map(tool => renderPermissionToolRow(tool, data)).join('');
  } catch { el.innerHTML = ''; }
}

async function setPermPreset(preset) {
  await fetch(`http://127.0.0.1:${backendPort}/api/permissions/preset`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ preset })
  });
  loadPermissions();
}

async function saveToolPerm(toolName) {
  const allowed = document.getElementById(`perm-allowed-${toolName}`)?.value === 'true';
  const confirmVal = document.getElementById(`perm-confirm-${toolName}`)?.value === 'true';
  const rateVal = parseInt(document.getElementById(`perm-rate-${toolName}`)?.value || '0', 10) || 0;
  await fetch(`http://127.0.0.1:${backendPort}/api/permissions/tool`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ tool: toolName, overrides: { allowed, confirm: confirmVal, rate_limit: rateVal } })
  });
  loadPermissions();
}

async function resetToolPerm(toolName) {
  await fetch(`http://127.0.0.1:${backendPort}/api/permissions/tool/${encodeURIComponent(toolName)}`, {
    method: 'DELETE'
  });
  loadPermissions();
}

// ==================== 技能市集 ====================

async function loadMarket() {
  const el = document.getElementById('marketGrid');
  if (!el) return;
  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/skill-market`);
    const data = await res.json();
    const skills = data.skills || [];
    if (skills.length === 0) {
      el.innerHTML = `<div class="empty-state">${t('market_empty')}</div>`;
      return;
    }
    el.innerHTML = skills.map(s => {
      const catColors = { developer: '#4f6ef7', system: '#22c55e', network: '#f59e0b', utility: '#8b5cf6' };
      const color = catColors[s.category] || '#6b7280';
      return `<div class="skill-card" style="cursor:default">
        <div style="display:flex;justify-content:space-between;align-items:start">
          <div>
            <h4 style="margin:0 0 4px">${escapeHtml(s.name)}</h4>
            <p style="font-size:12px;color:var(--text-secondary);margin:0 0 6px">${escapeHtml(s.description)}</p>
            <span style="font-size:10px;padding:2px 6px;border-radius:4px;background:${color}20;color:${color}">${s.category || 'other'}</span>
          </div>
          <button class="btn-primary" style="padding:4px 12px;font-size:12px;white-space:nowrap" onclick='installMarketSkill(${JSON.stringify(s).replace(/'/g, "\\'")})'>${t('market_install')}</button>
        </div>
      </div>`;
    }).join('');
  } catch { el.innerHTML = `<div class="empty-state">${t('market_empty')}</div>`; }
}

async function installMarketSkill(skill) {
  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/skill-market/install`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(skill)
    });
    const data = await res.json();
    if (data.success) {
      alert('已安裝技能: ' + skill.name);
      loadMarket();
    } else {
      alert((data.error || '安裝失敗'));
    }
  } catch (e) { alert('Error: ' + e.message); }
}

function showGenerateSkill() {
  document.getElementById('generateSkillForm').style.display = 'block';
  document.getElementById('genSkillDesc').value = '';
  document.getElementById('genSkillResult').style.display = 'none';
}

async function generateSkill() {
  const desc = document.getElementById('genSkillDesc').value.trim();
  if (!desc) { alert('請輸入描述'); return; }
  const btn = document.getElementById('genSkillBtn');
  const resultEl = document.getElementById('genSkillResult');
  btn.disabled = true;
  btn.textContent = '生成中...';
  resultEl.style.display = 'none';
  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/skill-generate`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ description: desc })
    });
    const data = await res.json();
    if (data.success && data.tool) {
      const tool = data.tool;
      resultEl.style.display = 'block';
      resultEl.innerHTML = `<div style="margin-bottom:8px"><strong>${escapeHtml(tool.name)}</strong> — ${escapeHtml(tool.description)}</div>
        <code style="display:block;padding:8px;background:var(--bg-input);border-radius:4px;font-size:12px;word-break:break-all">${escapeHtml(tool.command)}</code>
        <div style="margin-top:8px;display:flex;gap:8px">
          <button class="btn-primary" style="padding:4px 12px;font-size:12px" onclick='installGeneratedSkill(${JSON.stringify(tool).replace(/'/g, "\\'")})'>安裝此技能</button>
          <button class="btn-secondary" style="padding:4px 12px;font-size:12px" onclick="document.getElementById('genSkillResult').style.display='none'">取消</button>
        </div>`;
    } else {
      alert((data.error || '生成失敗'));
    }
  } catch (e) { alert('Error: ' + e.message); }
  btn.disabled = false;
  btn.textContent = t('market_gen_btn');
}

async function installGeneratedSkill(tool) {
  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/skill-market/install`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(tool)
    });
    const data = await res.json();
    if (data.success) {
      alert('已安裝技能: ' + tool.name);
      document.getElementById('genSkillResult').style.display = 'none';
      document.getElementById('generateSkillForm').style.display = 'none';
    } else {
      alert((data.error || '安裝失敗'));
    }
  } catch (e) { alert('Error: ' + e.message); }
}


// ==================== 技能匯出/匯入 ====================

async function exportSkills() {
  try {
    const res = await fetch(`http://127.0.0.1:${backendPort}/api/custom-tools`);
    const data = await res.json();
    const tools = data.tools || [];
    if (tools.length === 0) {
      alert(t('skills_export_empty') !== 'skills_export_empty' ? t('skills_export_empty') : '沒有自訂技能可匯出');
      return;
    }
    const exportData = {
      format: 'autoto-skills',
      version: '1.0',
      exported: new Date().toISOString(),
      skills: tools
    };
    const blob = new Blob([JSON.stringify(exportData, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'autoto-skills-' + new Date().toISOString().slice(0, 10) + '.json';
    a.click();
    URL.revokeObjectURL(url);
  } catch (e) {
    alert('匯出失敗: ' + e.message);
  }
}

async function importSkills(input) {
  const file = input.files[0];
  if (!file) return;
  try {
    const text = await file.text();
    const data = JSON.parse(text);
    let skills = [];
    // Support both formats: {skills: [...]} and plain array [...]
    if (data.format === 'autoto-skills' && Array.isArray(data.skills)) {
      skills = data.skills;
    } else if (Array.isArray(data)) {
      skills = data;
    } else if (data.name && data.command) {
      skills = [data]; // single skill
    } else {
      alert('無法辨識的技能檔案格式');
      input.value = '';
      return;
    }
    let installed = 0;
    for (const s of skills) {
      if (!s.name || !s.command) continue;
      const res = await fetch(`http://127.0.0.1:${backendPort}/api/custom-tools`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(s)
      });
      const r = await res.json();
      if (r.success) installed++;
    }
    alert((t('skills_import_done') !== 'skills_import_done' ? t('skills_import_done', { n: installed }) : '已匯入 ' + installed + ' 個技能'));
    input.value = '';
    loadSkills();
  } catch (e) {
    alert('匯入失敗: ' + e.message);
    input.value = '';
  }
}

// ==================== 攝影機監控頁面 ====================

async function loadCamerasPage() {
  const container = document.getElementById('cameras-content');
  if (!container) return;
  container.innerHTML = '<div class="loading-text">載入中...</div>';
  try {
    const res = await fetch(`${API}/cameras`);
    const data = await res.json();
    const cameras = data.cameras || [];
    let html = `<div class="cam-toolbar">
      <button class="btn-primary" onclick="showAddCameraDialog()">+ 新增攝影機</button>
    </div>`;
    if (cameras.length === 0) {
      html += '<div class="empty-state">尚未設定攝影機。點擊上方按鈕新增 RTSP 攝影機或 Webcam。</div>';
    } else {
      html += '<div class="cam-grid">';
      for (const cam of cameras) {
        const streaming = cam.streaming;
        html += `<div class="cam-card">
          <div class="cam-header">
            <span class="cam-name">${escapeHtml(cam.name)}</span>
            <span class="cam-badge ${streaming ? 'live' : 'off'}">${streaming ? 'LIVE' : '停止'}</span>
          </div>
          <div class="cam-view" id="cam-view-${cam.id}">
            ${streaming
              ? `<img src="${API}/cameras/${cam.id}/mjpeg" class="cam-stream" alt="即時畫面">`
              : `<div class="cam-placeholder">未串流</div>`}
          </div>
          <div class="cam-actions">
            ${streaming
              ? `<button class="btn-sm" onclick="camAction('${cam.id}','stop')">停止</button>`
              : `<button class="btn-sm btn-primary" onclick="camAction('${cam.id}','start')">串流</button>`}
            <button class="btn-sm" onclick="camSnapshot('${cam.id}')">快照</button>
            <button class="btn-sm" onclick="camAnalyze('${cam.id}')">AI 分析</button>
            <button class="btn-sm btn-danger" onclick="camDelete('${cam.id}')">刪除</button>
          </div>
          <div class="cam-watch-row" id="cam-watch-${cam.id}"></div>
          <div class="cam-info">${cam.type === 'webcam' ? 'Webcam #' + cam.device : escapeHtml(cam.url || '')}</div>
        </div>`;
        // 載入監控狀態
        setTimeout(() => loadCamWatchStatus(cam.id), 100);
      }
      html += '</div>';
    }
    container.innerHTML = html;
  } catch (e) {
    container.innerHTML = `<div class="error-text">載入失敗: ${e.message}</div>`;
  }
}

function showAddCameraDialog() {
  const html = `<div class="modal-overlay" id="cam-modal">
    <div class="modal-box">
      <h3>新增攝影機</h3>
      <label>名稱</label>
      <input id="cam-name" placeholder="例：客廳攝影機" />
      <label>類型</label>
      <select id="cam-type" onchange="toggleCamFields()">
        <option value="rtsp">RTSP / IP 攝影機</option>
        <option value="webcam">本機 Webcam</option>
      </select>
      <div id="cam-rtsp-fields">
        <label>RTSP URL</label>
        <input id="cam-url" placeholder="rtsp://192.168.1.100:554/stream" />
      </div>
      <div id="cam-webcam-fields" style="display:none">
        <label>裝置編號</label>
        <input id="cam-device" type="number" value="0" min="0" />
      </div>
      <div class="modal-actions">
        <button class="btn-primary" onclick="submitAddCamera()">新增</button>
        <button class="btn-sm" onclick="document.getElementById('cam-modal').remove()">取消</button>
      </div>
    </div>
  </div>`;
  document.body.insertAdjacentHTML('beforeend', html);
}

function toggleCamFields() {
  const type = document.getElementById('cam-type').value;
  document.getElementById('cam-rtsp-fields').style.display = type === 'rtsp' ? '' : 'none';
  document.getElementById('cam-webcam-fields').style.display = type === 'webcam' ? '' : 'none';
}

async function submitAddCamera() {
  const body = {
    name: document.getElementById('cam-name').value || '未命名',
    type: document.getElementById('cam-type').value,
    url: document.getElementById('cam-url').value,
    device: parseInt(document.getElementById('cam-device').value) || 0,
  };
  await fetch(`${API}/cameras`, { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
  document.getElementById('cam-modal')?.remove();
  loadCamerasPage();
}

async function camAction(id, action) {
  await fetch(`${API}/cameras/${id}/stream/${action}`, { method: 'POST' });
  setTimeout(loadCamerasPage, 500);
}

async function camSnapshot(id) {
  window.open(`${API}/cameras/${id}/snapshot`, '_blank');
}

async function camDelete(id) {
  if (!confirm('確定刪除此攝影機？')) return;
  await fetch(`${API}/cameras/${id}`, { method: 'DELETE' });
  loadCamerasPage();
}

async function camAnalyze(id) {
  const prompt = window.prompt('想問 AI 什麼？（留空則自動描述畫面）', '') ?? '';
  const btn = event.target;
  btn.disabled = true;
  btn.textContent = '分析中...';
  try {
    const res = await fetch(`${API}/cameras/${id}/analyze`, {
      method: 'POST',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify({ prompt })
    });
    const data = await res.json();
    if (data.success) {
      alert(`AI 分析結果：\n\n${data.analysis}`);
    } else {
      alert(`分析失敗：${data.error}`);
    }
  } catch (e) {
    alert(`錯誤：${e.message}`);
  }
  btn.disabled = false;
  btn.textContent = 'AI 分析';
}

async function loadCamWatchStatus(id) {
  const el = document.getElementById(`cam-watch-${id}`);
  if (!el) return;
  try {
    const res = await fetch(`${API}/cameras/${id}/watch/status`);
    const status = await res.json();
    if (status.watching) {
      el.innerHTML = `<div class="cam-watch-active">
        <span>AI 監控中（每 ${status.interval} 秒）</span>
        <span class="cam-watch-alerts">${status.alert_count > 0 ? `[!] ${status.alert_count} 次異常` : '正常'}</span>
        <button class="btn-sm" onclick="camWatchLogs('${id}')">紀錄</button>
        <button class="btn-sm btn-danger" onclick="camWatchStop('${id}')">停止監控</button>
      </div>`;
    } else {
      el.innerHTML = `<div class="cam-watch-inactive">
        <button class="btn-sm btn-primary" onclick="camWatchStart('${id}')">啟動 AI 監控</button>
        <span style="font-size:11px;color:var(--text-secondary)">定期截圖分析，異常時通知你</span>
      </div>`;
    }
  } catch { el.innerHTML = ''; }
}

async function camWatchStart(id) {
  const interval = parseInt(window.prompt('多少秒檢查一次？（建議 30-300）', '60')) || 60;
  await fetch(`${API}/cameras/${id}/watch/start`, {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ interval })
  });
  loadCamWatchStatus(id);
}

async function camWatchStop(id) {
  await fetch(`${API}/cameras/${id}/watch/stop`, { method: 'POST' });
  loadCamWatchStatus(id);
}

async function camWatchLogs(id) {
  try {
    const res = await fetch(`${API}/cameras/${id}/watch/logs?limit=30`);
    const data = await res.json();
    const logs = data.logs || [];
    if (logs.length === 0) { alert('尚無監控紀錄'); return; }
    const text = logs.map(l => {
      const time = new Date(l.time).toLocaleString();
      const flag = l.is_alert ? '[!]' : '';
      return `${flag} [${time}]\n${l.result}`;
    }).join('\n\n---\n\n');
    alert(`AI 監控紀錄（最近 ${logs.length} 筆）：\n\n${text}`);
  } catch (e) { alert('載入失敗：' + e.message); }
}

// ==================== 智慧家電頁面 ====================

async function loadSmartHomePage() {
  const container = document.getElementById('smarthome-content');
  if (!container) return;
  container.innerHTML = '<div class="loading-text">載入中...</div>';
  try {
    const [platRes, devRes] = await Promise.all([
      fetch(`${API}/smarthome/platforms`),
      fetch(`${API}/smarthome/devices`)
    ]);
    const platforms = (await platRes.json()).platforms || [];
    const devices = (await devRes.json()).devices || [];

    let html = `<div class="sh-toolbar">
      <button class="btn-primary" onclick="showAddPlatformDialog()">+ 新增平台</button>
      <button class="btn-sm" onclick="loadSmartHomePage()">重新整理</button>
    </div>`;

    // 平台列表
    if (platforms.length > 0) {
      html += '<div class="sh-platforms">';
      for (const p of platforms) {
        html += `<div class="sh-platform-chip ${p.enabled ? 'active' : ''}">
          [裝置] ${escapeHtml(p.name)} (${p.type})
          <button class="btn-x" onclick="deletePlatform('${p.id}')" title="刪除">×</button>
        </div>`;
      }
      html += '</div>';
    }

    // 裝置列表
    if (devices.length === 0) {
      html += '<div class="empty-state">尚未偵測到裝置。請先新增平台並確認連線。</div>';
    } else {
      html += '<div class="sh-grid">';
      for (const d of devices) {
        const isOn = d.state === 'on';
        const icon = d.type === 'light' ? '[燈]' : d.type === 'switch' ? '[開關]' : d.type === 'climate' ? '[空調]' : d.type === 'fan' ? '[風扇]' : d.type === 'cover' ? '[窗簾]' : d.type === 'lock' ? '[鎖]' : d.type === 'media_player' ? '[媒體]' : '[裝置]';
        html += `<div class="sh-device-card ${isOn ? 'on' : 'off'}">
          <div class="sh-device-icon">${icon}</div>
          <div class="sh-device-name">${escapeHtml(d.name)}</div>
          <div class="sh-device-state">${escapeHtml(d.state)}</div>
          <div class="sh-device-actions">
            <button class="btn-sm ${isOn ? 'btn-danger' : 'btn-primary'}" onclick="shControl('${d.id}','${isOn ? 'off' : 'on'}')">${isOn ? '關閉' : '開啟'}</button>
            <button class="btn-sm" onclick="shControl('${d.id}','toggle')">切換</button>
          </div>
          ${d.type === 'light' ? `<input type="range" min="0" max="255" value="${d.attributes?.brightness || 128}" class="sh-slider" onchange="shBrightness('${d.id}', this.value)">` : ''}
          <div class="sh-device-platform">${escapeHtml(d.platform_name || '')}</div>
        </div>`;
      }
      html += '</div>';
    }
    container.innerHTML = html;
  } catch (e) {
    container.innerHTML = `<div class="error-text">載入失敗: ${e.message}</div>`;
  }
}

function showAddPlatformDialog() {
  const html = `<div class="modal-overlay" id="sh-modal">
    <div class="modal-box">
      <h3>新增智慧家電平台</h3>
      <label>名稱</label>
      <input id="sh-name" placeholder="例：我家的 Home Assistant" />
      <label>平台類型</label>
      <select id="sh-type" onchange="togglePlatformFields()">
        <option value="homeassistant">Home Assistant</option>
        <option value="mqtt">MQTT (Zigbee2MQTT / Tasmota)</option>
        <option value="tuya">Tuya / 塗鴉智能</option>
        <option value="http">自訂 HTTP API</option>
      </select>
      <div id="sh-ha-fields">
        <label>Home Assistant URL</label>
        <input id="sh-host" placeholder="http://192.168.1.100:8123" />
        <label>Long-Lived Access Token</label>
        <input id="sh-token" type="password" placeholder="在 HA 個人資料頁面產生" />
      </div>
      <div id="sh-mqtt-fields" style="display:none">
        <label>MQTT Broker Host</label>
        <input id="sh-mqtt-host" placeholder="192.168.1.100" />
        <label>Port</label>
        <input id="sh-mqtt-port" type="number" value="1883" />
        <label>Topic Prefix</label>
        <input id="sh-mqtt-prefix" value="zigbee2mqtt" />
      </div>
      <div id="sh-tuya-fields" style="display:none">
        <label>Access ID</label>
        <input id="sh-tuya-id" placeholder="Tuya IoT Platform Access ID" />
        <label>Access Secret</label>
        <input id="sh-tuya-secret" type="password" />
      </div>
      <div id="sh-http-fields" style="display:none">
        <label>API Base URL</label>
        <input id="sh-http-url" placeholder="http://192.168.1.100:3000" />
        <label>Auth Header (選填)</label>
        <input id="sh-http-auth" placeholder="Authorization: Bearer xxx" />
      </div>
      <div class="modal-actions">
        <button class="btn-primary" onclick="submitAddPlatform()">新增</button>
        <button class="btn-sm" onclick="document.getElementById('sh-modal').remove()">取消</button>
      </div>
    </div>
  </div>`;
  document.body.insertAdjacentHTML('beforeend', html);
}

function togglePlatformFields() {
  const type = document.getElementById('sh-type').value;
  document.getElementById('sh-ha-fields').style.display = type === 'homeassistant' ? '' : 'none';
  document.getElementById('sh-mqtt-fields').style.display = type === 'mqtt' ? '' : 'none';
  document.getElementById('sh-tuya-fields').style.display = type === 'tuya' ? '' : 'none';
  document.getElementById('sh-http-fields').style.display = type === 'http' ? '' : 'none';
}

async function submitAddPlatform() {
  const type = document.getElementById('sh-type').value;
  const body = {
    name: document.getElementById('sh-name').value || '未命名平台',
    type,
  };
  if (type === 'homeassistant') {
    body.host = document.getElementById('sh-host').value;
    body.token = document.getElementById('sh-token').value;
  } else if (type === 'mqtt') {
    body.host = document.getElementById('sh-mqtt-host').value;
    body.extra = {
      port: parseInt(document.getElementById('sh-mqtt-port').value) || 1883,
      topic_prefix: document.getElementById('sh-mqtt-prefix').value || 'zigbee2mqtt',
    };
  } else if (type === 'tuya') {
    body.extra = {
      access_id: document.getElementById('sh-tuya-id').value,
      access_secret: document.getElementById('sh-tuya-secret').value,
    };
  } else if (type === 'http') {
    body.host = document.getElementById('sh-http-url').value;
    body.extra = { auth_header: document.getElementById('sh-http-auth').value };
  }
  await fetch(`${API}/smarthome/platforms`, { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
  document.getElementById('sh-modal')?.remove();
  loadSmartHomePage();
}

async function shControl(deviceId, action) {
  await fetch(`${API}/smarthome/devices/${deviceId}/control`, {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ action })
  });
  setTimeout(loadSmartHomePage, 300);
}

async function shBrightness(deviceId, value) {
  await fetch(`${API}/smarthome/devices/${deviceId}/control`, {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ action: 'set_brightness', params: { brightness: parseInt(value) } })
  });
}

async function deletePlatform(id) {
  if (!confirm('確定刪除此平台？所有裝置將一併移除。')) return;
  await fetch(`${API}/smarthome/platforms/${id}`, { method: 'DELETE' });
  loadSmartHomePage();
}

// ==================== AI 員工總覽 ====================

const AGENTS_FILE_KEY = 'autoto_agents';

function _loadAgents() {
  try { return JSON.parse(localStorage.getItem(AGENTS_FILE_KEY) || '[]'); } catch { return []; }
}
function _saveAgents(agents) {
  localStorage.setItem(AGENTS_FILE_KEY, JSON.stringify(agents));
}

function loadAgentsPage() {
  renderAgentsGrid();
  // 也載入排程
  loadSchedulerInAgents();
}

function renderAgentsGrid() {
  const agents = _loadAgents();
  const grid = document.getElementById('agentsGrid');
  if (!grid) return;
  if (!agents.length) {
    grid.innerHTML = `<div class="empty-state" style="padding:30px;font-size:13px">${t('agents_empty')}</div>`;
    return;
  }
  grid.innerHTML = agents.map(a => {
    const statusColor = a.status === 'working' ? '#22c55e' : a.status === 'idle' ? '#94a3b8' : '#f59e0b';
    const statusText = a.status === 'working' ? (t('agents_status_working') || '工作中') :
                       a.status === 'idle' ? (t('agents_status_idle') || '待命') :
                       (t('agents_status_scheduled') || '排班中');
    return `<div class="agent-card">
      <div class="agent-card-header">
        <div class="agent-avatar">${a.name.charAt(0).toUpperCase()}</div>
        <div class="agent-info">
          <div class="agent-name">${a.name}</div>
          <div class="agent-role">${a.role || ''}</div>
        </div>
        <div class="agent-status" style="background:${statusColor}">${statusText}</div>
      </div>
      <div class="agent-card-body">
        <div class="agent-field"><span>${t('agents_job') || '工作內容'}</span><span>${a.job || '-'}</span></div>
        <div class="agent-field"><span>${t('agents_current_task') || '當前任務'}</span><span>${a.currentTask || '-'}</span></div>
        <div class="agent-field"><span>${t('agents_recent_output') || '最近產出'}</span><span>${a.recentOutput || '-'}</span></div>
        <div class="agent-field"><span>${t('agents_schedule') || '排班'}</span><span>${a.schedule || '-'}</span></div>
      </div>
      <div class="agent-card-footer">
        <button class="btn-sm" onclick="agentEdit('${a.id}')">${t('agents_edit') || '編輯'}</button>
        <button class="btn-sm" onclick="agentToggle('${a.id}')">${a.status === 'idle' ? (t('agents_activate') || '啟動') : (t('agents_pause') || '暫停')}</button>
        <button class="btn-sm btn-danger" onclick="agentDelete('${a.id}')">${t('agents_delete') || '刪除'}</button>
      </div>
    </div>`;
  }).join('');
}

function agentAdd() {
  const name = prompt(t('agents_name_prompt') || '員工名稱：');
  if (!name) return;
  const role = prompt(t('agents_role_prompt') || '職位/角色（如：社群小編、客服、排程助理）：') || '';
  const job = prompt(t('agents_job_prompt') || '工作內容（如：每天發一篇 IG 貼文、回覆客戶留言）：') || '';
  const agents = _loadAgents();
  agents.push({
    id: 'agent_' + Date.now(),
    name: name,
    role: role,
    job: job,
    status: 'idle',
    currentTask: '',
    recentOutput: '',
    schedule: ''
  });
  _saveAgents(agents);
  renderAgentsGrid();
}

function agentEdit(id) {
  const agents = _loadAgents();
  const a = agents.find(x => x.id === id);
  if (!a) return;
  const name = prompt(t('agents_name_prompt') || '員工名稱：', a.name);
  if (!name) return;
  a.name = name;
  a.role = prompt(t('agents_role_prompt') || '職位/角色：', a.role) || '';
  a.job = prompt(t('agents_job_prompt') || '工作內容：', a.job || '') || '';
  a.currentTask = prompt(t('agents_task_prompt') || '當前任務：', a.currentTask) || '';
  a.schedule = prompt(t('agents_schedule_prompt') || '排班說明：', a.schedule) || '';
  _saveAgents(agents);
  renderAgentsGrid();
}

function agentToggle(id) {
  const agents = _loadAgents();
  const a = agents.find(x => x.id === id);
  if (!a) return;
  a.status = a.status === 'idle' ? 'working' : 'idle';
  _saveAgents(agents);
  renderAgentsGrid();
}

function agentDelete(id) {
  if (!confirm(t('agents_delete_confirm') || '確定刪除此員工？')) return;
  const agents = _loadAgents().filter(x => x.id !== id);
  _saveAgents(agents);
  renderAgentsGrid();
}

async function loadSchedulerInAgents() {
  // 複用現有排程載入邏輯，但渲染到 agents 頁面的容器
  const taskList = document.getElementById('schedTaskList2');
  const editor = document.getElementById('schedEditor2');
  if (!taskList || !editor) return;
  try {
    const res = await fetch(`${API}/schedules`);
    const data = await res.json();
    _schedTasks = data.schedules || [];
    // 渲染任務列表
    if (!_schedTasks.length) {
      taskList.innerHTML = `<div class="empty-state" style="font-size:13px;padding:20px">${t('scheduler_empty')}</div>`;
    } else {
      taskList.innerHTML = _schedTasks.map(task => {
        const active = task.enabled ? 'active' : '';
        const sel = _schedSelected === task.id ? 'selected' : '';
        return `<div class="sched-task-item ${active} ${sel}" onclick="schedSelectInAgents('${task.id}')">
          <div class="sched-task-name">${task.name || 'Untitled'}</div>
          <div class="sched-task-meta">${task.schedule || task.expression || ''}</div>
        </div>`;
      }).join('');
    }
  } catch {
    taskList.innerHTML = `<div class="empty-state" style="padding:20px">無法載入排程</div>`;
  }
}

function schedSelectInAgents(id) {
  _schedSelected = id;
  // 切到設定頁的排程 tab 來編輯（複用現有 UI）
  document.querySelectorAll('.nav-item').forEach(b => b.classList.remove('active'));
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  document.querySelector('[data-page="settings"]').classList.add('active');
  document.getElementById('page-settings').classList.add('active');
  currentPage = 'settings';
  // 切到排程 tab
  document.querySelectorAll('.settings-tab').forEach(t => t.classList.remove('active'));
  document.querySelectorAll('.settings-panel').forEach(p => p.classList.remove('active'));
  document.querySelector('[data-stab="scheduler"]').classList.add('active');
  document.getElementById('stab-scheduler').classList.add('active');
  loadSchedulerPage();
  setTimeout(() => { if (typeof renderSchedEditor === 'function') renderSchedEditor(_schedTasks.find(t => t.id === id)); }, 300);
}

clearChat();
init();
