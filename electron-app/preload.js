const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('autoto', {
  getConfig: () => ipcRenderer.invoke('get-config'),
  saveConfig: (config) => ipcRenderer.invoke('save-config', config),
  getBackendPort: () => ipcRenderer.invoke('get-backend-port'),
  restartBackend: () => ipcRenderer.invoke('restart-backend'),
  openExternal: (url) => ipcRenderer.invoke('open-external', url),
  onBackendLog: (callback) => ipcRenderer.on('backend-log', (_, data) => callback(data)),
  onBackendStatus: (callback) => ipcRenderer.on('backend-status', (_, status) => callback(status))
});
