const { app, BrowserWindow, ipcMain, shell, Tray, Menu, nativeImage } = require('electron');
const path = require('path');
const { spawn, spawnSync } = require('child_process');
const Store = require('electron-store');

const store = new Store();
let mainWindow = null;
let tray = null;
let backendProcess = null;

// 後端服務端口
const BACKEND_PORT = 5678;
const BACKEND_URL = `http://127.0.0.1:${BACKEND_PORT}`;

function resolvePythonPath() {
  const configuredPath = store.get('pythonPath');
  const candidates = [configuredPath, 'python3.11', 'python3', 'python'].filter(Boolean);

  for (const candidate of candidates) {
    const result = spawnSync(candidate, ['-c', 'import sys; raise SystemExit(0 if sys.version_info >= (3, 11) else 1)'], {
      stdio: 'ignore'
    });
    if (result.status === 0) {
      return candidate;
    }
  }

  return null;
}

function loadBackendUI(retries = 20) {
  if (!mainWindow) {
    return;
  }

  mainWindow.loadURL(BACKEND_URL).catch(() => {
    if (retries > 0) {
      setTimeout(() => loadBackendUI(retries - 1), 1000);
    }
  });
}

function createWindow() {
  mainWindow = new BrowserWindow({
    width: 1200,
    height: 800,
    minWidth: 900,
    minHeight: 600,
    title: 'AutoTo',
    titleBarStyle: 'hiddenInset',
    webPreferences: {
      nodeIntegration: false,
      contextIsolation: true,
      preload: path.join(__dirname, 'preload.js')
    },
    backgroundColor: '#0f0f23',
    show: false
  });

  loadBackendUI();

  mainWindow.once('ready-to-show', () => {
    mainWindow.show();
  });

  mainWindow.on('close', (e) => {
    if (process.platform === 'darwin' && !app.isQuitting) {
      e.preventDefault();
      mainWindow.hide();
    }
  });
}

function startBackend() {
  const pythonPath = resolvePythonPath();
  const backendScript = path.join(__dirname, '..', 'backend', 'server.py');

  if (!pythonPath) {
    console.error('[Backend Error] 未找到可用的 Python 3.11+');
    return;
  }

  backendProcess = spawn(pythonPath, [backendScript, '--port', String(BACKEND_PORT)], {
    env: { ...process.env, AUTOTO_PORT: String(BACKEND_PORT) },
    stdio: ['pipe', 'pipe', 'pipe']
  });

  backendProcess.stdout.on('data', (data) => {
    console.log(`[Backend] ${data}`);
    if (mainWindow) {
      mainWindow.webContents.send('backend-log', data.toString());
    }
  });

  backendProcess.stderr.on('data', (data) => {
    console.error(`[Backend Error] ${data}`);
  });

  backendProcess.on('close', (code) => {
    console.log(`[Backend] 已停止 (code: ${code})`);
    if (mainWindow) {
      mainWindow.webContents.send('backend-status', 'stopped');
    }
  });
}

function stopBackend() {
  if (backendProcess) {
    backendProcess.kill();
    backendProcess = null;
  }
}

function createTray() {
  // 使用簡單的文字圖標作為 tray
  tray = new Tray(nativeImage.createEmpty());
  const contextMenu = Menu.buildFromTemplate([
    { label: '開啟 AutoTo', click: () => mainWindow && mainWindow.show() },
    { type: 'separator' },
    { label: '重啟後端', click: () => { stopBackend(); startBackend(); } },
    { type: 'separator' },
    { label: '結束', click: () => { stopBackend(); app.quit(); } }
  ]);
  tray.setToolTip('AutoTo AI 助理');
  tray.setContextMenu(contextMenu);
  tray.on('click', () => mainWindow && mainWindow.show());
}

// ==================== IPC 處理 ====================

ipcMain.handle('get-config', () => {
  return store.get('config', {
    provider: 'groq',
    apiKey: '',
    model: 'llama-3.3-70b-versatile',
    customUrl: '',
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
  });
});

ipcMain.handle('save-config', (event, config) => {
  store.set('config', config);
  return { success: true };
});

ipcMain.handle('get-backend-port', () => BACKEND_PORT);

ipcMain.handle('restart-backend', () => {
  stopBackend();
  startBackend();
  return { success: true };
});

ipcMain.handle('open-external', (event, url) => {
  shell.openExternal(url);
});

// ==================== App 生命週期 ====================

app.whenReady().then(() => {
  startBackend();
  createWindow();
  createTray();
});

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') {
    stopBackend();
    app.quit();
  }
});

app.on('activate', () => {
  if (mainWindow) {
    mainWindow.show();
  } else {
    createWindow();
  }
});

app.on('before-quit', () => {
  app.isQuitting = true;
  stopBackend();
});
