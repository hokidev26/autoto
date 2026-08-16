import { $ } from "./dom.mjs";
import { defaultTerminalPrefs, terminalPrefsKey } from "./preferences-data.mjs";
import { webSocketURL } from "./runtime.mjs";
import { t } from "./i18n.mjs";
import { terminalAccessAllowed } from "./remote-access-capabilities.mjs";
import { shellExtraT as sx } from "./messages-shell-extra.mjs";

export function createTerminalController({
  state,
  copyToClipboard,
  formatNumber,
  notifyTerminal,
  refreshActiveSettingsPanel,
  showError,
  showToast,
} = {}) {
  function loadTerminalPreferences() {
    try {
      return normalizeTerminalPreferences(JSON.parse(localStorage.getItem(terminalPrefsKey) || "{}"));
    } catch {
      return normalizeTerminalPreferences({});
    }
  }

  function normalizeTerminalPreferences(value = {}) {
    const maxLines = Number(value.maxLines || defaultTerminalPrefs.maxLines);
    return {
      clearOnReconnect: value.clearOnReconnect !== undefined ? Boolean(value.clearOnReconnect) : defaultTerminalPrefs.clearOnReconnect,
      focusOnConnect: value.focusOnConnect !== undefined ? Boolean(value.focusOnConnect) : defaultTerminalPrefs.focusOnConnect,
      maxLines: [1000, 3000, 5000, 10000].includes(maxLines) ? maxLines : defaultTerminalPrefs.maxLines,
    };
  }

  function currentTerminalPreferences() {
    if (!state.terminalPrefs) state.terminalPrefs = loadTerminalPreferences();
    return state.terminalPrefs;
  }

  function saveTerminalPreferences(next, { notify = false } = {}) {
    state.terminalPrefs = normalizeTerminalPreferences(next);
    try {
      localStorage.setItem(terminalPrefsKey, JSON.stringify(state.terminalPrefs));
    } catch {}
    trimTerminalOutput();
    syncTerminalPanelPrefs();
    if (notify) showToast(t("workspace.terminal.preferencesSaved"), "success", { force: true });
  }

  function setTerminalPreference(field, value) {
    const prefs = { ...currentTerminalPreferences() };
    if (field === "maxLines") prefs.maxLines = Number(value || defaultTerminalPrefs.maxLines);
    else prefs[field] = value === true || value === "true";
    saveTerminalPreferences(prefs, { notify: true });
  }

  function terminalOutputText() {
    return $("terminalOutput")?.textContent || "";
  }

  function terminalOutputStats() {
    const text = terminalOutputText();
    return {
      chars: text.length,
      lines: text ? text.split("\n").length : 0,
    };
  }

  function clearTerminalOutput({ notify = true } = {}) {
    const output = $("terminalOutput");
    if (!output) return;
    output.textContent = `${sx("terminalExtras.clearedOutput")}\n`;
    if (notify) showToast(t("workspace.terminal.cleared"), "success");
  }

  async function copyTerminalOutput() {
    const text = terminalOutputText();
    if (!text.trim()) throw new Error(t("workspace.terminal.noCopy"));
    if (await copyToClipboard(text)) {
      showToast(t("workspace.terminal.copied"), "success");
      notifyTerminal(`[info] ${sx("terminalExtras.copiedOutputNotice")}\n`);
      return;
    }
    showToast(t("workspace.terminal.copyFailed"), "warn");
    notifyTerminal(`[warn] ${sx("terminalExtras.copyFailedOutputNotice")}\n`);
  }

  function remoteTerminalLocked() {
    return !terminalAccessAllowed(state);
  }

  function remoteTerminalLockedMessage() {
    return t("workspace.terminal.remoteLocked");
  }

  function enforceAccessPolicy({ notify = false } = {}) {
    if (!remoteTerminalLocked()) {
      renderTerminalButtonState();
      return true;
    }
    const socket = state.terminalWS;
    state.terminalWS = null;
    if (socket && typeof socket.close === "function") {
      try {
        socket.close(1008, "remote access capability changed");
      } catch {}
    }
    if (notify) appendTerminal(`[terminal] ${remoteTerminalLockedMessage()}\n`);
    setTerminalStatus("remote-locked");
    return false;
  }

  function focusTerminalPanel() {
    if (remoteTerminalLocked()) {
      showToast(remoteTerminalLockedMessage(), "warn", { force: true });
      appendTerminal(`[terminal] ${remoteTerminalLockedMessage()}\n`);
      return;
    }
    toggleTerminal(false);
    $("terminalOutput")?.focus();
    resizeTerminal();
  }

  function reconnectTerminalFromSettings() {
    if (!state.agent) {
      showToast(t("workspace.terminal.selectAgent"), "warn");
      return;
    }
    if (remoteTerminalLocked()) {
      showToast(remoteTerminalLockedMessage(), "warn", { force: true });
      appendTerminal(`[terminal] ${remoteTerminalLockedMessage()}\n`);
      setTerminalStatus("remote-locked");
      return;
    }
    connectTerminal();
  }

  function trimTerminalOutput() {
    const output = $("terminalOutput");
    if (!output) return;
    const maxLines = currentTerminalPreferences().maxLines;
    if (!maxLines || maxLines <= 0) return;
    const lines = output.textContent.split("\n");
    if (lines.length <= maxLines) return;
    output.textContent = lines.slice(lines.length - maxLines).join("\n");
  }

  function syncTerminalPanelPrefs() {
    const prefs = currentTerminalPreferences();
    const clearOnReconnect = $("terminalClearOnReconnect");
    const focusOnConnect = $("terminalFocusOnConnect");
    const maxLines = $("terminalMaxLines");
    if (clearOnReconnect) clearOnReconnect.checked = prefs.clearOnReconnect;
    if (focusOnConnect) focusOnConnect.checked = prefs.focusOnConnect;
    if (maxLines) maxLines.value = String(prefs.maxLines);
  }

  function bindTerminalPanelActions() {
    $("clearTerminalBtn")?.addEventListener("click", () => clearTerminalOutput());
    $("copyTerminalBtn")?.addEventListener("click", () => copyTerminalOutput().catch(showError));
    $("terminalClearOnReconnect")?.addEventListener("change", (event) => {
      setTerminalPreference("clearOnReconnect", Boolean(event.currentTarget?.checked));
    });
    $("terminalFocusOnConnect")?.addEventListener("change", (event) => {
      setTerminalPreference("focusOnConnect", Boolean(event.currentTarget?.checked));
    });
    $("terminalMaxLines")?.addEventListener("change", (event) => {
      setTerminalPreference("maxLines", event.currentTarget?.value);
    });
    syncTerminalPanelPrefs();
  }

  function terminalStatusLabel(status) {
    const key = String(status || "closed").replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
    const message = sx(`terminalExtras.status.${key}`);
    return message === `terminalExtras.status.${key}` ? String(status || "closed") : message;
  }

  function connectTerminal() {
    if (!state.agent) return;
    if (remoteTerminalLocked()) {
      enforceAccessPolicy({ notify: true });
      return;
    }
    if (state.terminalWS) state.terminalWS.close();
    const agentId = state.agent.id;
    const output = $("terminalOutput");
    if (currentTerminalPreferences().clearOnReconnect) output.textContent = `${sx("terminalExtras.connectingOutput")}\n`;
    else appendTerminal(`\n[terminal] ${sx("terminalExtras.reconnectingOutput")}\n`);
    setTerminalStatus("connecting");
    const socket = new WebSocket(webSocketURL(`/ws/terminal?agentId=${encodeURIComponent(agentId)}`));
    state.terminalWS = socket;
    const isCurrentSocket = () => state.terminalWS === socket && state.agent?.id === agentId;
    socket.onopen = () => {
      if (!isCurrentSocket()) return;
      setTerminalStatus("connected");
      resizeTerminal(socket);
      if (currentTerminalPreferences().focusOnConnect) output.focus();
    };
    socket.onclose = () => {
      if (!isCurrentSocket()) return;
      setTerminalStatus("closed");
    };
    socket.onerror = () => {
      if (!isCurrentSocket()) return;
      setTerminalStatus("error");
    };
    socket.onmessage = (message) => {
      if (!isCurrentSocket()) return;
      try {
        const event = JSON.parse(message.data);
        if (event.type === "output") appendTerminal(cleanTerminalOutput(event.data || ""));
        if (event.type === "error") appendTerminal(`\n[terminal error] ${event.data || sx("terminalExtras.unknownError")}\n`);
      } catch {
        appendTerminal(message.data);
      }
    };
  }

  function setTerminalStatus(text) {
    state.terminalStatus = text;
    const status = $("terminalStatus");
    if (status) status.textContent = sx("terminalExtras.statusText", { status: terminalStatusLabel(text) });
    const connected = text === "connected";
    const commandInput = $("terminalCommandInput");
    const commandButton = $("terminalCommandRunBtn");
    if (commandInput) {
      commandInput.disabled = !connected;
      commandInput.placeholder = connected ? t("workspace.terminal.commandPlaceholder") : state.agent ? t("workspace.terminal.connecting") : t("workspace.terminal.selectAgent");
    }
    if (commandButton) commandButton.disabled = !connected;
    renderTerminalButtonState();
  }

  function sendTerminalInput(data) {
    if (remoteTerminalLocked()) return false;
    if (!state.agent || !state.terminalWS || state.terminalWS.readyState !== WebSocket.OPEN) return false;
    state.terminalWS.send(JSON.stringify({ type: "input", data }));
    return true;
  }

  function resizeTerminal(socket = state.terminalWS) {
    if (!socket || socket.readyState !== WebSocket.OPEN) return;
    if (socket !== state.terminalWS) return;
    const output = $("terminalOutput");
    const cols = Math.max(40, Math.floor(output.clientWidth / 8));
    const rows = Math.max(10, Math.floor(output.clientHeight / 18));
    socket.send(JSON.stringify({ type: "resize", cols, rows }));
  }

  function handleTerminalKeydown(event) {
    if (!state.agent || remoteTerminalLocked()) return;
    const keyMap = {
      Enter: "\r",
      Backspace: "\x7f",
      Tab: "\t",
      Escape: "\x1b",
      ArrowUp: "\x1b[A",
      ArrowDown: "\x1b[B",
      ArrowRight: "\x1b[C",
      ArrowLeft: "\x1b[D",
      Delete: "\x1b[3~",
      Home: "\x1b[H",
      End: "\x1b[F",
      PageUp: "\x1b[5~",
      PageDown: "\x1b[6~",
    };
    if (event.ctrlKey && event.key.length === 1) {
      event.preventDefault();
      event.stopPropagation();
      sendTerminalInput(String.fromCharCode(event.key.toUpperCase().charCodeAt(0) - 64));
      return;
    }
    if (keyMap[event.key]) {
      event.preventDefault();
      event.stopPropagation();
      sendTerminalInput(keyMap[event.key]);
      return;
    }
    if (!event.metaKey && !event.ctrlKey && !event.altKey && event.key.length === 1) {
      event.preventDefault();
      event.stopPropagation();
      sendTerminalInput(event.key);
    }
  }

  function appendTerminal(text) {
    const output = $("terminalOutput");
    output.textContent += text;
    trimTerminalOutput();
    output.scrollTop = output.scrollHeight;
  }

  function cleanTerminalOutput(text) {
    return String(text || "")
      .replace(/\x1b\[[0-9;?]*[A-Za-z]/g, "")
      .replace(/\x1b\][^\x07]*(\x07|\x1b\\)/g, "");
  }

  function renderTerminalButtonState() {
    const collapsed = $("appShell")?.classList.contains("terminal-collapsed") ?? true;
    const locked = remoteTerminalLocked();
    const button = $("toggleTerminalBtn");
    if (button) {
      button.classList.toggle("active", !collapsed && !locked);
      button.setAttribute("aria-pressed", !collapsed && !locked ? "true" : "false");
      button.setAttribute("aria-expanded", !collapsed && !locked ? "true" : "false");
      if (!locked) button.title = collapsed ? t("chat.expandTerminal") : t("terminal.collapse");
      button.setAttribute("aria-label", locked ? t("workspace.terminal.remoteLocked") : collapsed ? t("chat.expandTerminal") : t("terminal.collapse"));
    }
    $("expandTerminalBtn")?.classList.toggle("hidden", !collapsed || locked);
  }

  function toggleTerminal(collapsed) {
    if (remoteTerminalLocked() && collapsed === false) {
      showToast(remoteTerminalLockedMessage(), "warn", { force: true });
      appendTerminal(`[terminal] ${remoteTerminalLockedMessage()}\n`);
      setTerminalStatus("remote-locked");
      renderTerminalButtonState();
      return false;
    }
    const shouldCollapse = collapsed ?? !$("appShell").classList.contains("terminal-collapsed");
    $("appShell").classList.toggle("terminal-collapsed", shouldCollapse);
    if (shouldCollapse) document.body.classList.remove("mobile-terminal-open");
    renderTerminalButtonState();
    if (!shouldCollapse) globalThis.requestAnimationFrame?.(() => resizeTerminal());
    return true;
  }

  return {
    appendTerminal,
    bindTerminalPanelActions,
    clearTerminalOutput,
    connectTerminal,
    copyTerminalOutput,
    currentTerminalPreferences,
    enforceAccessPolicy,
    focusTerminalPanel,
    handleTerminalKeydown,
    loadTerminalPreferences,
    normalizeTerminalPreferences,
    reconnectTerminalFromSettings,
    renderTerminalButtonState,
    resizeTerminal,
    saveTerminalPreferences,
    sendTerminalInput,
    setTerminalPreference,
    setTerminalStatus,
    syncTerminalPanelPrefs,
    terminalOutputStats,
    terminalOutputText,
    toggleTerminal,
    trimTerminalOutput,
  };
}
