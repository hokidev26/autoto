import { $, escapeAttr, escapeHtml } from "./dom.mjs";
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
    if (state.activeSettingsPanel === "terminals") refreshActiveSettingsPanel();
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
    if (state.activeSettingsPanel === "terminals") refreshActiveSettingsPanel();
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

  // Two sections only. Reconnect/collapse are reachable from the terminal panel
  // and workbench header, so repeating them here was noise. Clear and copy have
  // no other entry point in the UI, so they stay — promoted into the hero action
  // row rather than living in a separate card grid.
  function renderTerminalSettingsContent() {
    const prefs = currentTerminalPreferences();
    const wsLabel = terminalConnectionLabel();
    const cwd = state.agent?.cwd || state.project?.gitPath || t("workspace.terminal.agentNotSelected");
    const locked = remoteTerminalLocked();
    return `
      <div class="settings-live-page terminal-settings-page">
        <section class="settings-hero-card terminal-hero-card settings-page-section settings-card">
          <div class="settings-card-header">
            <div>
              <div class="settings-hero-kicker">${escapeHtml(t("workspace.terminal.management"))}</div>
              <div class="settings-hero-title settings-card-title">${escapeHtml(wsLabel)}</div>
              <p class="settings-card-description path">${escapeHtml(cwd)}</p>
              <p class="settings-card-description" data-settings-help-copy>${escapeHtml(locked ? remoteTerminalLockedMessage() : sx("terminal.description"))}</p>
            </div>
            <span class="settings-status-pill settings-badge ${state.agent ? "ok" : "warn"}">${escapeHtml(state.agent ? t("workspace.terminal.agentSelected") : t("workspace.terminal.agentNotSelected"))}</span>
          </div>
          <div class="settings-action-row settings-toolbar settings-inline-actions settings-card-footer">
            <button id="terminalReconnectSettingsBtn" class="settings-action-btn primary" type="button" ${locked ? "disabled" : ""}>${escapeHtml(t("workspace.terminal.reconnect"))}</button>
            <button id="terminalFocusSettingsBtn" class="settings-action-btn subtle" type="button" ${locked ? "disabled" : ""}>${escapeHtml(t("workspace.terminal.focus"))}</button>
            <button class="settings-action-btn subtle" type="button" data-terminal-action="clear">${escapeHtml(t("workspace.terminal.clear"))}</button>
            <button class="settings-action-btn subtle" type="button" data-terminal-action="copy">${escapeHtml(t("workspace.terminal.copy"))}</button>
          </div>
        </section>
        ${locked ? `<section class="settings-provider-section settings-page-section settings-card settings-alert" role="alert"><div class="settings-provider-section-head settings-card-header"><div><div class="settings-provider-title settings-card-title">${escapeHtml(t("workspace.terminal.remoteDisabled"))}</div><div class="settings-provider-meta settings-card-description">${escapeHtml(remoteTerminalLockedMessage())}</div></div></div></section>` : ""}
        <section class="settings-provider-section settings-page-section settings-card">
          <div class="settings-provider-section-head settings-card-header">
            <div>
              <div class="settings-provider-title settings-card-title">${escapeHtml(t("workspace.terminal.localPreferences"))}</div>
              <div class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(sx("terminal.localPrefsDescription"))}</div>
            </div>
          </div>
          <div class="appearance-toggle-list settings-card-content">
            ${renderTerminalToggle("clearOnReconnect", sx("terminal.clearOnReconnect"), sx("terminal.clearOnReconnectDescription"), prefs.clearOnReconnect)}
            ${renderTerminalToggle("focusOnConnect", sx("terminal.focusOnConnect"), sx("terminal.focusOnConnectDescription"), prefs.focusOnConnect)}
          </div>
          <div class="terminal-retention-block settings-card-content">
            <div class="settings-provider-title settings-card-title small">${escapeHtml(sx("terminal.outputRetention"))}</div>
            <div class="appearance-choice-grid terminal-retention-grid settings-data-list">
              ${[1000, 3000, 5000, 10000].map((value) => renderTerminalMaxLineChoice(value, prefs.maxLines)).join("")}
            </div>
          </div>
        </section>
      </div>
    `;
  }

  function terminalConnectionLabel() {
    if (!state.terminalWS) return state.agent ? sx("terminalExtras.status.disconnected") : t("workspace.terminal.agentNotSelected");
    if (state.terminalWS.readyState === WebSocket.OPEN) return sx("terminalExtras.status.connected");
    if (state.terminalWS.readyState === WebSocket.CONNECTING) return sx("terminalExtras.status.connecting");
    if (state.terminalWS.readyState === WebSocket.CLOSING) return sx("terminalExtras.status.closing");
    return terminalStatusLabel(state.terminalStatus || "closed");
  }

  function terminalStatusLabel(status) {
    const key = String(status || "closed").replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
    const message = sx(`terminalExtras.status.${key}`);
    return message === `terminalExtras.status.${key}` ? String(status || "closed") : message;
  }

  function renderTerminalToggle(field, title, description, checked) {
    return `
      <label class="appearance-toggle-row terminal-toggle-row settings-switch-row">
        <span>
          <strong>${escapeHtml(title)}</strong>
          <small data-settings-help-copy>${escapeHtml(description)}</small>
        </span>
        <input type="checkbox" data-terminal-toggle="${escapeAttr(field)}" ${checked ? "checked" : ""} />
      </label>
    `;
  }

  function renderTerminalMaxLineChoice(value, current) {
    return `
      <button class="appearance-choice settings-data-row ${current === value ? "active" : ""}" type="button" data-terminal-max-lines="${escapeAttr(value)}">
        <span>${escapeHtml(formatNumber(value))}</span>
        <small data-settings-help-copy>${escapeHtml(sx("terminal.maxOutputLines", { count: formatNumber(value) }))}</small>
      </button>
    `;
  }

  function bindTerminalSettingsActions() {
    $("terminalReconnectSettingsBtn")?.addEventListener("click", reconnectTerminalFromSettings);
    $("terminalFocusSettingsBtn")?.addEventListener("click", focusTerminalPanel);
    document.querySelectorAll("[data-terminal-action]").forEach((node) => {
      node.addEventListener("click", () => handleTerminalSettingsAction(node.dataset.terminalAction).catch(showError));
    });
    document.querySelectorAll("[data-terminal-toggle]").forEach((node) => {
      node.addEventListener("change", () => setTerminalPreference(node.dataset.terminalToggle, node.checked));
    });
    document.querySelectorAll("[data-terminal-max-lines]").forEach((node) => {
      node.addEventListener("click", () => setTerminalPreference("maxLines", node.dataset.terminalMaxLines));
    });
  }

  async function handleTerminalSettingsAction(action) {
    if (action === "clear") clearTerminalOutput();
    if (action === "copy") await copyTerminalOutput();
    if (state.activeSettingsPanel === "terminals") refreshActiveSettingsPanel();
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
    if (state.activeSettingsPanel === "terminals") refreshActiveSettingsPanel();
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
    if (state.activeSettingsPanel === "terminals") refreshActiveSettingsPanel();
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
    bindTerminalSettingsActions,
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
    renderTerminalSettingsContent,
    resizeTerminal,
    saveTerminalPreferences,
    sendTerminalInput,
    setTerminalPreference,
    setTerminalStatus,
    terminalConnectionLabel,
    terminalOutputStats,
    terminalOutputText,
    toggleTerminal,
    trimTerminalOutput,
  };
}
