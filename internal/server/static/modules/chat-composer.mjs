import { $, escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { formatBytes, formatNumber } from "./formatters.mjs";
import { chatDraftsKey, promptHistoryKey } from "./preferences-data.mjs";
import { api } from "./runtime.mjs";
import { t } from "./i18n.mjs?v=goal-command-2";
import { mergeAuthoritativeEffectiveCommands, mergeBuiltInSlashCommands, mergeSlashCommands, normalizeSlashCommandName, slashCommandInsertion } from "./skills-commands.mjs?v=goal-command-2";

export const defaultReasoningEffortValues = Object.freeze(["auto", "low", "medium", "high"]);
export const knownReasoningEffortValues = Object.freeze([...defaultReasoningEffortValues, "xhigh"]);

function normalizedReasoningEffortList(values = defaultReasoningEffortValues) {
  const source = Array.isArray(values) ? values : defaultReasoningEffortValues;
  const normalized = source
    .map((value) => String(value || "").trim().toLowerCase())
    .filter((value) => knownReasoningEffortValues.includes(value));
  return ["auto", ...normalized.filter((value, index) => value !== "auto" && normalized.indexOf(value) === index)];
}

export function normalizeReasoningEffort(value, supportedValues = defaultReasoningEffortValues) {
  const effort = String(value || "").trim().toLowerCase();
  const normalized = effort === "" || effort === "default" || effort === "inherit" ? "auto" : effort;
  return normalizedReasoningEffortList(supportedValues).includes(normalized) ? normalized : "auto";
}

export function reasoningEffortValuesForCapabilities(capabilities = {}) {
  const source = capabilities && typeof capabilities === "object" ? capabilities : {};
  const explicit = [
    source.reasoningEfforts,
    source.reasoningEffortValues,
    source.effortValues,
    Array.isArray(source.reasoningEffort) ? source.reasoningEffort : undefined,
    source.reasoningEffort?.values,
    source.reasoningEffort?.supportedValues,
  ].find(Array.isArray);
  if (explicit) return normalizedReasoningEffortList(explicit);
  return source.reasoningEffort === true ? [...defaultReasoningEffortValues] : ["auto"];
}

export function reasoningEffortValuesForModel(provider, modelValue) {
  const value = String(modelValue || "").trim();
  const separator = value.indexOf(":");
  const providerName = separator >= 0 ? value.slice(0, separator).trim() : String(provider?.name || "").trim();
  const model = separator >= 0 ? value.slice(separator + 1).trim() : value;
  const modelCapabilities = provider?.modelCapabilities && typeof provider.modelCapabilities === "object"
    ? provider.modelCapabilities[model]
    : null;
  const hasModelReasoningCapabilities = Boolean(modelCapabilities && [
    "reasoningEffort", "reasoningEfforts", "reasoningEffortValues", "effortValues",
  ].some((key) => Object.hasOwn(modelCapabilities, key)));
  if (hasModelReasoningCapabilities) return reasoningEffortValuesForCapabilities(modelCapabilities);
  if (providerName === "codex" && model === "gpt-5.5") return [...knownReasoningEffortValues];
  return reasoningEffortValuesForCapabilities(provider?.capabilities || {});
}

export function fastModeSupportedForModel(provider, modelValue) {
  const value = String(modelValue || "").trim();
  const separator = value.indexOf(":");
  const model = separator >= 0 ? value.slice(separator + 1).trim() : value;
  const modelCapabilities = provider?.modelCapabilities && typeof provider.modelCapabilities === "object"
    ? provider.modelCapabilities
    : {};
  return Boolean(model && modelCapabilities[model]?.fastMode === true);
}

export function normalizeMessageMode(value, fallback = "execute") {
  const mode = String(value || "").trim().toLowerCase();
  if (mode === "plan" || mode === "execute") return mode;
  return fallback === "plan" ? "plan" : "execute";
}

export function calculateMessageInputSize({ scrollHeight, minHeight = 0, maxHeight = 180 } = {}) {
  const minimum = Math.max(0, Number(minHeight) || 0);
  const maximum = Math.max(minimum, Number(maxHeight) || 180);
  const contentHeight = Math.max(minimum, Number(scrollHeight) || 0);
  return {
    height: Math.min(contentHeight, maximum),
    scrollable: contentHeight > maximum,
  };
}

function cssPixelValue(value, fallback) {
  const parsed = Number.parseFloat(String(value || ""));
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback;
}

export function resizeMessageInputElement(input, computedStyle = globalThis.getComputedStyle?.(input)) {
  if (!input) return { height: 0, scrollable: false };
  input.style.height = "auto";
  const minHeight = cssPixelValue(
    computedStyle?.getPropertyValue?.("--composer-input-min-height") || computedStyle?.minHeight,
    0,
  );
  const maxHeight = cssPixelValue(
    computedStyle?.getPropertyValue?.("--composer-input-max-height") || computedStyle?.maxHeight,
    180,
  );
  const size = calculateMessageInputSize({ scrollHeight: input.scrollHeight, minHeight, maxHeight });
  input.style.height = `${size.height}px`;
  input.style.overflowY = size.scrollable ? "auto" : "hidden";
  input.classList?.toggle("message-input-scrollable", size.scrollable);
  return size;
}

export function slashCommandsForEffectivePolicy(policy, localTemplates) {
  return mergeAuthoritativeEffectiveCommands(policy, localTemplates);
}

export function builtInSlashCommandsForContext(context, translate = t) {
  if (String(context || "").trim().toLowerCase() !== "project") return [];
  return [{
    id: "builtin-goal",
    name: "/goal",
    description: translate("workspace.chat.goalCommandDescription"),
    prompt: "",
    source: "builtin",
  }];
}

export function slashCommandsForContext(context, commands, translate = t) {
  const externalCommands = (Array.isArray(commands) ? commands : [])
    .filter((command) => normalizeSlashCommandName(command?.name) !== "/goal");
  return mergeBuiltInSlashCommands(builtInSlashCommandsForContext(context, translate), externalCommands);
}

export function parseGoalCommandDraft(value = "") {
  const commandText = String(value || "").trim();
  if (commandText !== "/goal" && !commandText.startsWith("/goal ")) return null;
  return {
    commandText,
    goalText: commandText.slice("/goal".length).trim(),
  };
}

export const maxChatDraftCharacters = 8000;

export function interfaceLocale(documentRef = globalThis.document, navigatorRef = globalThis.navigator) {
  return documentRef?.documentElement?.lang || navigatorRef?.language || "zh-CN";
}

export function unicodeCharacters(value = "") {
  return Array.from(String(value || ""));
}

export function truncateChatDraft(value = "", max = maxChatDraftCharacters) {
  const characters = unicodeCharacters(value);
  return {
    text: characters.slice(0, Math.max(0, max)).join(""),
    length: characters.length,
    truncated: characters.length > max,
  };
}

export function mentionTrigger(value = "", cursor = String(value || "").length) {
  const text = String(value || "").slice(0, Math.max(0, cursor));
  const match = text.match(/(?:^|\s)@([^\s@]{0,64})$/u);
  if (!match) return null;
  const query = match[1] || "";
  return { query, start: text.length - query.length - 1, end: text.length };
}

export function clipboardFiles(event) {
  const files = Array.from(event?.clipboardData?.files || []).filter(Boolean);
  if (files.length) return files;
  return Array.from(event?.clipboardData?.items || [])
    .filter((item) => item?.kind === "file")
    .map((item) => item.getAsFile?.())
    .filter(Boolean);
}

export function normalizeChatDrafts(value = {}) {
  const source = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  return Object.entries(source).reduce((acc, [key, draft]) => {
    const id = String(key || "").trim().slice(0, 120);
    const { text } = truncateChatDraft(draft);
    if (id && text.trim()) acc[id] = text;
    return acc;
  }, {});
}

export function normalizePromptHistory(value = []) {
  const seen = new Set();
  return (Array.isArray(value) ? value : [])
    .map((item) => String(item || "").trim())
    .filter(Boolean)
    .filter((item) => {
      const key = item.toLowerCase();
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    })
    .map((item) => item.slice(0, 4000))
    .slice(0, 30);
}

export function createChatComposerController({
  state,
  attachmentKind,
  currentProviderConfig,
  currentSkillsPreferences,
  getEffectiveSkillsPolicy,
  isComposingInput,
  isCurrentModelConfigured,
  awaitAgentSettingsSaved = async () => {},
  saveAgentSettings = async () => {},
  loadMessages,
  notifyTerminal,
  openDirectoryChooser,
  request = api,
  scheduleMessageRefresh,
  showModelSetupNotice,
  showToast,
  onMessageAccepted,
} = {}) {
  const pendingReasoningEfforts = new Map();
  const savingReasoningEfforts = new Set();
  const pendingFastModes = new Map();
  const savingFastModes = new Set();

  function loadChatDrafts() {
    try {
      return normalizeChatDrafts(JSON.parse(localStorage.getItem(chatDraftsKey) || "{}"));
    } catch {
      return {};
    }
  }

  function currentChatDrafts() {
    if (!state.chatDrafts || typeof state.chatDrafts !== "object") state.chatDrafts = loadChatDrafts();
    return state.chatDrafts;
  }

  function currentChatDraftKey() {
    return state.agent?.id || state.workline?.id || state.project?.id || "global";
  }

  function serverDraftState(agentId = state.agent?.id) {
    if (!state.serverDrafts || typeof state.serverDrafts !== "object") state.serverDrafts = {};
    if (!agentId) return null;
    if (!state.serverDrafts[agentId]) state.serverDrafts[agentId] = { enabled: false, version: 0, seq: 0, timer: null };
    return state.serverDrafts[agentId];
  }

  function writeChatDrafts(drafts) {
    state.chatDrafts = normalizeChatDrafts(drafts);
    try {
      localStorage.setItem(chatDraftsKey, JSON.stringify(state.chatDrafts));
    } catch {}
  }

  function saveChatDraftForKey(key, value) {
    const id = String(key || "").trim();
    if (!id) return;
    const drafts = { ...currentChatDrafts() };
    const { text } = truncateChatDraft(value);
    if (text.trim()) drafts[id] = text;
    else delete drafts[id];
    writeChatDrafts(drafts);
  }

  async function persistServerDraft(agentId, value) {
    const draftState = serverDraftState(agentId);
    if (!draftState?.enabled) return;
    const result = await api(`/api/agents/${agentId}/draft`, {
      method: "PUT",
      body: JSON.stringify({ text: truncateChatDraft(value).text, version: draftState.version }),
    });
    if (state.agent?.id === agentId) draftState.version = Number(result?.version || draftState.version + 1);
  }

  function scheduleServerDraftSave(agentId, value) {
    const draftState = serverDraftState(agentId);
    if (!draftState?.enabled) return;
    window.clearTimeout(draftState.timer);
    draftState.timer = window.setTimeout(() => {
      persistServerDraft(agentId, value).catch(async (error) => {
        if (error?.status === 409) {
          try {
            const latest = await api(`/api/agents/${agentId}/draft`);
            draftState.version = Number(latest?.version || 0);
            await persistServerDraft(agentId, value);
            return;
          } catch (retryError) {
            error = retryError;
          }
        }
        notifyTerminal?.(`[warn] 私有草稿保存失败：${error?.message || error}\n`);
      });
    }, 400);
  }

  function saveCurrentChatDraft() {
    const input = $("messageText");
    if (!input) return;
    const agentId = state.agent?.id;
    const draftState = serverDraftState(agentId);
    if (agentId && draftState?.enabled) {
      scheduleServerDraftSave(agentId, input.value);
      return;
    }
    saveChatDraftForKey(currentChatDraftKey(), input.value);
  }

  async function restoreCurrentChatDraft() {
    const agentId = state.agent?.id;
    const localDraft = currentChatDrafts()[currentChatDraftKey()] || "";
    setMessageInputValue(localDraft, { saveDraft: false });
    if (agentId && state.authUser) {
      const draftState = serverDraftState(agentId);
      const seq = ++draftState.seq;
      try {
        const draft = await api(`/api/agents/${agentId}/draft`);
        if (state.agent?.id !== agentId || draftState.seq !== seq) return;
        draftState.enabled = true;
        draftState.version = Number(draft?.version || 0);
        saveChatDraftForKey(currentChatDraftKey(), "");
        setMessageInputValue(draft?.contentText || "", { saveDraft: false });
        return;
      } catch (error) {
        if (state.agent?.id !== agentId || draftState.seq !== seq) return;
        if (error?.status === 404) {
          draftState.enabled = true;
          draftState.version = 0;
          saveChatDraftForKey(currentChatDraftKey(), "");
          setMessageInputValue("", { saveDraft: false });
          return;
        }
        if (error?.status !== 401) notifyTerminal?.(`[warn] 私有草稿读取失败，已回退到浏览器草稿：${error?.message || error}\n`);
      }
    }
  }

  function clearChatDraftForKey(key) {
    const agentId = state.agent?.id;
    const draftState = serverDraftState(agentId);
    if (agentId && draftState?.enabled) {
      window.clearTimeout(draftState.timer);
      draftState.version = 0;
      api(`/api/agents/${agentId}/draft`, { method: "DELETE" }).catch(() => {});
      return;
    }
    saveChatDraftForKey(key, "");
  }

  function reasoningEffortLabel(value) {
    return {
      auto: t("modelProvider.automatic"),
      low: t("modelProvider.low"),
      medium: t("modelProvider.medium"),
      high: t("modelProvider.high"),
      xhigh: t("staticExtra.chat.ultraHighEffort"),
    }[value] || t("modelProvider.automatic");
  }

  function reasoningEffortMobileLabel(value) {
    return {
      auto: "Auto",
      low: "Low",
      medium: "Med",
      high: "High",
      xhigh: "XH",
    }[value] || "Auto";
  }

  function reasoningEffortValues(modelValue = $("modelSelect")?.value || state.agent?.model || "") {
    const provider = currentProviderConfig?.(modelValue) || null;
    return reasoningEffortValuesForModel(provider, modelValue);
  }

  function currentReasoningEffort(modelValue) {
    const values = reasoningEffortValues(modelValue);
    return normalizeReasoningEffort(state.agent?.reasoningEffort, values);
  }

  function reasoningEffortSavingFor(agentId = state.agent?.id) {
    return Boolean(agentId && savingReasoningEfforts.has(agentId));
  }

  function syncReasoningEffortSavingState() {
    state.reasoningEffortSaving = reasoningEffortSavingFor();
  }

  function refreshReasoningEffortControl({ modelValue, requestedValue } = {}) {
    const select = $("reasoningEffort");
    if (!select) return "auto";
    const values = reasoningEffortValues(modelValue);
    const selected = requestedValue === undefined
      ? currentReasoningEffort(modelValue)
      : normalizeReasoningEffort(requestedValue, values);
    const saving = reasoningEffortSavingFor();
    syncReasoningEffortSavingState();
    select.innerHTML = values.map((value) => `<option value="${escapeAttr(value)}">${escapeHtml(reasoningEffortLabel(value))}</option>`).join("");
    select.value = selected;
    select.disabled = !state.agent || values.length <= 1 || saving;
    select.setAttribute("aria-busy", saving ? "true" : "false");
    select.dataset.supported = values.length > 1 ? "true" : "false";
    const display = $("reasoningEffortDisplay");
    if (display) {
      display.textContent = reasoningEffortLabel(selected);
      (display.dataset ||= {}).mobileLabel = reasoningEffortMobileLabel(selected);
    }
    // The visible control is the custom trigger, not the native select, so it
    // has to mirror the disabled state or it looks clickable while offering
    // nothing to choose (e.g. providers without reasoning-effort support).
    const trigger = select.parentElement?.querySelector?.('[data-composer-select="reasoningEffort"]');
    if (trigger) {
      const unsupported = values.length <= 1;
      trigger.disabled = Boolean(select.disabled);
      trigger.classList?.toggle?.("is-unsupported", unsupported);
      if (unsupported) trigger.title = t("chat.reasoningEffortUnsupported");
      else trigger.removeAttribute?.("title");
    }
    const pill = select.closest?.(".reasoning-effort-pill");
    pill?.classList.toggle("reasoning-effort-unsupported", values.length <= 1);
    pill?.classList.toggle("reasoning-effort-saving", saving);
    return selected;
  }

  function selectedReasoningEffort(modelValue) {
    const select = $("reasoningEffort");
    const values = reasoningEffortValues(modelValue);
    return normalizeReasoningEffort(select?.value ?? state.agent?.reasoningEffort, values);
  }

  function agentEntityGeneration(agent = state.agent) {
    const generation = Number(agent?.entityGeneration);
    return Number.isSafeInteger(generation) && generation >= 0 ? generation : 0;
  }

  async function saveReasoningEffort(value = $("reasoningEffort")?.value) {
    const agentId = state.agent?.id || "";
    if (!agentId) return null;
    const selected = normalizeReasoningEffort(value, reasoningEffortValues());
    const agentAtStart = state.agent;
    const persistedAtStart = agentAtStart.reasoningEffort;
    pendingReasoningEfforts.set(agentId, {
      effort: selected,
      model: String(agentAtStart.model || ""),
      entityGeneration: agentEntityGeneration(agentAtStart),
    });
    state.agent = { ...agentAtStart, reasoningEffort: selected };
    refreshReasoningEffortControl({ requestedValue: selected });
    if (reasoningEffortSavingFor(agentId)) return null;

    let updatedAgent = null;
    let persistedEffort = persistedAtStart;
    let lastRequest = null;
    savingReasoningEfforts.add(agentId);
    syncReasoningEffortSavingState();
    try {
      while (pendingReasoningEfforts.has(agentId)) {
        const next = pendingReasoningEfforts.get(agentId);
        pendingReasoningEfforts.delete(agentId);
        const current = state.agent;
        // A model change makes an already queued effort request stale. Do not
        // let it mutate the new model, and do not let its eventual response
        // replace the new model's authoritative state.
        if (!current || current.id !== agentId || current.model !== next.model) continue;
        if (agentEntityGeneration(current) !== next.entityGeneration) {
          next.entityGeneration = agentEntityGeneration(current);
        }
        lastRequest = { ...next };
        refreshReasoningEffortControl({ requestedValue: next.effort });
        updatedAgent = await request(`/api/agents/${agentId}/reasoning-effort`, {
          method: "PATCH",
          body: JSON.stringify({
            reasoningEffort: next.effort,
            model: next.model,
            entityGeneration: next.entityGeneration,
          }),
        });
        const stillCurrent = state.agent?.id === agentId
          && state.agent.model === next.model
          && agentEntityGeneration(state.agent) === next.entityGeneration;
        if (!stillCurrent) continue;
        if (updatedAgent?.id === agentId && updatedAgent.model === next.model) {
          state.agent = updatedAgent;
          persistedEffort = updatedAgent.reasoningEffort ?? next.effort;
        } else {
          state.agent = { ...state.agent, reasoningEffort: next.effort };
          persistedEffort = next.effort;
        }
      }
      return updatedAgent;
    } catch (error) {
      pendingReasoningEfforts.delete(agentId);
      const current = state.agent;
      const requestIsStillCurrent = current?.id === agentId
        && current.model === lastRequest?.model
        && agentEntityGeneration(current) === lastRequest?.entityGeneration;
      if (requestIsStillCurrent) {
        state.agent = { ...current, reasoningEffort: persistedEffort };
      }
      // A stale request may legitimately lose its race to a model change. It
      // is already obsolete, so preserve the new state without surfacing it as
      // a failed update for the current model.
      if (!requestIsStillCurrent) return null;
      throw error;
    } finally {
      savingReasoningEfforts.delete(agentId);
      syncReasoningEffortSavingState();
      if (state.agent?.id === agentId) refreshReasoningEffortControl();
    }
  }

  function fastModeSupported(modelValue = $("modelSelect")?.value || state.agent?.model || "") {
    return fastModeSupportedForModel(currentProviderConfig?.(modelValue) || null, modelValue);
  }

  function fastModeSavingFor(agentId = state.agent?.id) {
    return Boolean(agentId && savingFastModes.has(agentId));
  }

  function syncFastModeSavingState() {
    state.fastModeSaving = fastModeSavingFor();
  }

  function refreshFastModeControl({ modelValue, requestedValue } = {}) {
    const button = $("openProviderLoginBtn");
    if (!button) return false;
    const supported = Boolean(state.agent) && fastModeSupported(modelValue);
    const active = supported && Boolean(requestedValue === undefined ? state.agent?.fastMode : requestedValue);
    const saving = fastModeSavingFor();
    syncFastModeSavingState();
    button.classList.toggle("hidden", !supported);
    button.classList.toggle("fast-mode-active", active);
    button.disabled = !supported || saving;
    button.dataset.supported = supported ? "true" : "false";
    button.setAttribute("aria-pressed", active ? "true" : "false");
    button.setAttribute("aria-busy", saving ? "true" : "false");
    const label = active ? t("chat.fastModeEnabled") : t("chat.fastModeDisabled");
    button.title = label;
    button.setAttribute("aria-label", label);
    return active;
  }

  async function saveFastMode(value) {
    const agentId = state.agent?.id || "";
    if (!agentId) return null;
    const agentAtStart = state.agent;
    const selected = Boolean(value) && fastModeSupported(agentAtStart.model);
    const persistedAtStart = Boolean(agentAtStart.fastMode);
    pendingFastModes.set(agentId, {
      fastMode: selected,
      model: String(agentAtStart.model || ""),
      entityGeneration: agentEntityGeneration(agentAtStart),
    });
    state.agent = { ...agentAtStart, fastMode: selected };
    refreshFastModeControl({ requestedValue: selected });
    if (fastModeSavingFor(agentId)) return null;

    let updatedAgent = null;
    let persistedFastMode = persistedAtStart;
    let lastRequest = null;
    savingFastModes.add(agentId);
    syncFastModeSavingState();
    try {
      while (pendingFastModes.has(agentId)) {
        const next = pendingFastModes.get(agentId);
        pendingFastModes.delete(agentId);
        const current = state.agent;
        if (!current || current.id !== agentId || current.model !== next.model) continue;
        if (agentEntityGeneration(current) !== next.entityGeneration) {
          next.entityGeneration = agentEntityGeneration(current);
        }
        lastRequest = { ...next };
        refreshFastModeControl({ requestedValue: next.fastMode });
        updatedAgent = await request(`/api/agents/${agentId}/fast-mode`, {
          method: "PATCH",
          body: JSON.stringify({
            fastMode: next.fastMode,
            model: next.model,
            entityGeneration: next.entityGeneration,
          }),
        });
        const stillCurrent = state.agent?.id === agentId
          && state.agent.model === next.model
          && agentEntityGeneration(state.agent) === next.entityGeneration;
        if (!stillCurrent) continue;
        if (updatedAgent?.id === agentId && updatedAgent.model === next.model) {
          state.agent = updatedAgent;
          persistedFastMode = Boolean(updatedAgent.fastMode);
        } else {
          state.agent = { ...state.agent, fastMode: next.fastMode };
          persistedFastMode = next.fastMode;
        }
      }
      return updatedAgent;
    } catch (error) {
      pendingFastModes.delete(agentId);
      const current = state.agent;
      const requestIsStillCurrent = current?.id === agentId
        && current.model === lastRequest?.model
        && agentEntityGeneration(current) === lastRequest?.entityGeneration;
      if (requestIsStillCurrent) {
        state.agent = { ...current, fastMode: persistedFastMode };
      }
      if (!requestIsStillCurrent) return null;
      throw error;
    } finally {
      savingFastModes.delete(agentId);
      syncFastModeSavingState();
      if (state.agent?.id === agentId) refreshFastModeControl();
    }
  }

  function toggleFastMode() {
    if (!state.agent || !fastModeSupported()) return Promise.resolve(null);
    return saveFastMode(!Boolean(state.agent.fastMode));
  }

  function loadPromptHistory() {
    try {
      return normalizePromptHistory(JSON.parse(localStorage.getItem(promptHistoryKey) || "[]"));
    } catch {
      return [];
    }
  }

  function currentPromptHistory() {
    if (!Array.isArray(state.promptHistory)) state.promptHistory = loadPromptHistory();
    return state.promptHistory;
  }

  function savePromptHistory(history) {
    state.promptHistory = normalizePromptHistory(history);
    state.promptHistoryIndex = -1;
    state.promptHistoryDraft = "";
    try {
      localStorage.setItem(promptHistoryKey, JSON.stringify(state.promptHistory));
    } catch {}
    updatePromptHistoryHint();
  }

  function rememberPromptHistory(text) {
    const prompt = String(text || "").trim();
    if (!prompt) return;
    const next = [prompt, ...currentPromptHistory().filter((item) => item.toLowerCase() !== prompt.toLowerCase())];
    savePromptHistory(next);
  }

  function resetPromptHistoryNavigation() {
    state.promptHistoryIndex = -1;
    state.promptHistoryDraft = "";
  }

  function messageModeFor(agentId = state.agent?.id) {
    const saved = agentId ? state.messageModes?.[agentId] : "";
    return normalizeMessageMode(saved, state.agent?.planMode === true ? "plan" : "execute");
  }

  function refreshMessageModeControl({ requestedMode } = {}) {
    const mode = normalizeMessageMode(requestedMode ?? messageModeFor(), state.agent?.planMode === true ? "plan" : "execute");
    const toggle = $("messageModeToggle");
    toggle?.querySelectorAll?.("[data-message-mode]").forEach((button) => {
      const active = button.dataset.messageMode === mode;
      button.classList.toggle("active", active);
      button.setAttribute("aria-pressed", active ? "true" : "false");
      button.disabled = isMessageSendingFor();
    });
    if (toggle) toggle.dataset.mode = mode;
    return mode;
  }

  function setMessageMode(value) {
    const agentId = state.agent?.id;
    const mode = normalizeMessageMode(value, state.agent?.planMode === true ? "plan" : "execute");
    if (agentId) state.messageModes = { ...(state.messageModes || {}), [agentId]: mode };
    refreshMessageModeControl({ requestedMode: mode });
    return mode;
  }

  function isMessageSendingFor(agentId = state.agent?.id) {
    return Boolean(agentId && state.messageSendingByAgent?.[agentId]);
  }

  function syncMessageComposerBusy() {
    const busy = isMessageSendingFor();
    const input = $("messageText");
    if (input) input.disabled = busy;
    const attachButton = $("attachFileBtn");
    if (attachButton) attachButton.disabled = busy;
    const attachInput = $("attachFileInput");
    if (attachInput) attachInput.disabled = busy;
    refreshMessageModeControl();
    setButtonBusy($("sendMessageBtn"), busy, t("workspace.chat.sending"));
  }

  function setMessageSendingFor(agentId, sending) {
    if (!agentId) return;
    const next = { ...(state.messageSendingByAgent || {}) };
    if (sending) next[agentId] = true;
    else delete next[agentId];
    state.messageSendingByAgent = next;
    syncMessageComposerBusy();
  }

  async function sendMessage(event) {
    event.preventDefault();
    if (!state.agent) {
      await openDirectoryChooser();
      return;
    }
    const agentId = state.agent.id;
    if (isMessageSendingFor(agentId)) return;
    const draftKey = currentChatDraftKey();
    const input = $("messageText");
    const text = input.value.trim();
    const context = state.navigationSelectionKind === "project" ? "project" : "conversation";
    const mode = context === "project" ? messageModeFor(agentId) : "execute";
    const attachments = [...(state.pendingAttachments || [])];
    if (!text && !attachments.length) return;
    const goalCommand = parseGoalCommandDraft(text);
    if (goalCommand && context !== "project") {
      showToast?.(t("workspace.chat.goalProjectOnly"), "warn", { force: true });
      return;
    }
    if (goalCommand && !goalCommand.goalText) {
      showToast?.(t("workspace.chat.goalTextRequired"), "warn", { force: true });
      return;
    }
    if (goalCommand && attachments.length) {
      showToast?.(t("workspace.chat.goalAttachmentsUnsupported"), "warn", { force: true });
      return;
    }
    const isGoalCommand = Boolean(goalCommand);
    setMessageSendingFor(agentId, true);
    try {
      if (!isGoalCommand) {
        await awaitAgentSettingsSaved(agentId);
        if (state.agent?.id !== agentId) {
          throw new Error("The active conversation changed before the message was sent.");
        }
        let selectedModel = String($("modelSelect")?.value || state.agent.model || "").trim();
        let persistedModel = String(state.agent.model || "").trim();
        if (selectedModel && selectedModel !== persistedModel) {
          // The picker selection never reached the agent -- e.g. it was chosen
          // while this conversation had no agent yet, so the save only updated
          // the model preference and never issued a model PATCH. Force one save
          // pass now that the agent exists, then re-check before refusing.
          await saveAgentSettings();
          await awaitAgentSettingsSaved(agentId);
          if (state.agent?.id !== agentId) {
            throw new Error("The active conversation changed before the message was sent.");
          }
          selectedModel = String($("modelSelect")?.value || state.agent.model || "").trim();
          persistedModel = String(state.agent.model || "").trim();
          if (selectedModel && selectedModel !== persistedModel) {
            throw new Error("The selected model could not be synchronized. Please try again.");
          }
        }
        if (!isCurrentModelConfigured()) {
          showModelSetupNotice();
          return;
        }
      }
      input.value = "";
      autoResizeMessageInput();
      let accepted;
      if (attachments.length) {
        const form = new FormData();
        form.append("text", text);
        form.append("mode", mode);
        form.append("context", context);
        attachments.forEach((item) => form.append("files", item.file, item.file?.name || "attachment"));
        accepted = await request(`/api/agents/${agentId}/messages`, {
          method: "POST",
          body: form,
        });
      } else {
        accepted = await request(`/api/agents/${agentId}/messages`, {
          method: "POST",
          body: JSON.stringify({ text, mode, context }),
        });
      }
      await onMessageAccepted?.(accepted, agentId);
      if (text) rememberPromptHistory(text);
      clearChatDraftForKey(draftKey);
      if (attachments.length) clearPendingAttachments();
      if (!isGoalCommand) {
        await loadMessages(agentId);
        scheduleMessageRefresh(1200, agentId);
      }
    } catch (err) {
      const stillCurrent = state.agent?.id === agentId;
      saveChatDraftForKey(draftKey, text);
      if (stillCurrent) {
        if (!input.value.trim()) input.value = text;
        autoResizeMessageInput();
        throw err;
      }
      notifyTerminal(`[warn] Message delivery to the previous agent failed; the draft was kept: ${err.message || err}\n`);
    } finally {
      setMessageSendingFor(agentId, false);
      if (state.agent?.id === agentId) input.focus();
    }
  }

  function updateDraftLimitHint() {
    const input = $("messageText");
    const hint = $("chatDraftLimitHint");
    if (!input || !hint) return;
    const length = unicodeCharacters(input.value).length;
    const locale = interfaceLocale();
    const over = Math.max(0, length - maxChatDraftCharacters);
    hint.classList.toggle("warn", over > 0);
    hint.textContent = over > 0
      ? `已超出 ${formatNumber(over, locale)} 个字符；草稿只保存前 ${formatNumber(maxChatDraftCharacters, locale)} 个字符。`
      : `${formatNumber(length, locale)} / ${formatNumber(maxChatDraftCharacters, locale)} 个字符`;
  }

  function autoResizeMessageInput() {
    const size = resizeMessageInputElement($("messageText"));
    updatePromptHistoryHint();
    updateDraftLimitHint();
    return size;
  }

  function scheduleMessageInputResize() {
    const schedule = globalThis.requestAnimationFrame || ((callback) => globalThis.setTimeout(callback, 0));
    schedule(() => autoResizeMessageInput());
  }

  function openAttachmentPicker() {
    const input = $("attachFileInput");
    if (!input || input.disabled) return;
    input.value = "";
    input.click();
  }

  function attachmentId() {
    return `att-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
  }

  function addPendingAttachmentFiles(files) {
    const pickedFiles = Array.from(files || []).filter(Boolean);
    if (!pickedFiles.length) return;
    const maxFileBytes = 10 * 1024 * 1024;
    const maxTotalBytes = 25 * 1024 * 1024;
    const currentTotal = state.pendingAttachments.reduce((sum, item) => sum + (item.file?.size || 0), 0);
    let nextTotal = currentTotal;
    const skipped = [];
    const added = [];
    for (const file of pickedFiles) {
      const name = file.name || t("workspace.chat.unnamedFile");
      if (file.size > maxFileBytes) {
        skipped.push(`${name}（${formatBytes(file.size)}）`);
        continue;
      }
      if (nextTotal + file.size > maxTotalBytes) {
        skipped.push(`${name} (${t("workspace.chat.attachmentsSkipped", { count: 0, files: "", suffix: "" }).replace(/^.*：/, "").replace(/。$/, "")}${formatBytes(maxTotalBytes)})`);
        continue;
      }
      nextTotal += file.size;
      const kind = attachmentKind(file);
      added.push({
        id: attachmentId(),
        file,
        kind,
        previewUrl: kind === "image" ? URL.createObjectURL(file) : "",
      });
    }
    if (added.length) {
      state.pendingAttachments = [...state.pendingAttachments, ...added];
      renderPendingAttachments();
      showToast(t("workspace.chat.attachmentsAdded", { count: added.length }), "success", { force: true });
    }
    if (skipped.length) {
      const preview = skipped.slice(0, 3).join("、");
      showToast(t("workspace.chat.attachmentsSkipped", { count: skipped.length, files: preview, suffix: skipped.length > 3 ? t("workspace.chat.more") : "" }), "warn", { force: true });
    }
  }

  async function importAttachmentFiles(event) {
    const picker = event?.target;
    addPendingAttachmentFiles(picker?.files || []);
    if (picker) picker.value = "";
  }

  function handleMessagePaste(event) {
    const files = clipboardFiles(event);
    if (!files.length) return false;
    addPendingAttachmentFiles(files);
    // Keep the browser's normal text paste and undo stack intact when the
    // clipboard contains both text and files.
    return true;
  }

  function removePendingAttachment(id) {
    const removed = state.pendingAttachments.find((item) => item.id === id);
    if (removed?.previewUrl) URL.revokeObjectURL(removed.previewUrl);
    state.pendingAttachments = state.pendingAttachments.filter((item) => item.id !== id);
    renderPendingAttachments();
  }

  function clearPendingAttachments() {
    state.pendingAttachments.forEach((item) => {
      if (item.previewUrl) URL.revokeObjectURL(item.previewUrl);
    });
    state.pendingAttachments = [];
    renderPendingAttachments();
  }

  function renderPendingAttachments() {
    const wrap = $("pendingAttachments");
    if (!wrap) return;
    const attachments = state.pendingAttachments || [];
    wrap.classList.toggle("hidden", attachments.length === 0);
    wrap.innerHTML = attachments.map((item) => pendingAttachmentCardHTML(item)).join("");
    wrap.querySelectorAll("[data-remove-attachment]").forEach((button) => {
      button.addEventListener("click", () => removePendingAttachment(button.dataset.removeAttachment));
    });
  }

  function pendingAttachmentCardHTML(item) {
    const file = item.file || {};
    const name = file.name || t("workspace.chat.unnamedFile");
    if (item.kind === "image" && item.previewUrl) {
      return `
        <div class="pending-image-card" title="${escapeAttr(name)}">
          <img class="pending-image-thumb" src="${escapeAttr(item.previewUrl)}" alt="${escapeAttr(name)}" />
          <button class="pending-attachment-remove" type="button" title="${escapeAttr(t("workspace.chat.removeAttachment"))}" aria-label="${escapeAttr(t("workspace.chat.removeAttachment"))}" data-remove-attachment="${escapeAttr(item.id)}">×</button>
        </div>
      `;
    }
    const subtitle = formatBytes(file.size || 0);
    return `
      <div class="pending-file-chip" title="${escapeAttr(name)}">
        <span class="pending-file-icon">▯</span>
        <span class="pending-file-name">${escapeHtml(name)}</span>
        <span class="pending-file-size">${escapeHtml(subtitle)}</span>
        <button class="pending-attachment-remove" type="button" title="${escapeAttr(t("workspace.chat.removeAttachment"))}" aria-label="${escapeAttr(t("workspace.chat.removeAttachment"))}" data-remove-attachment="${escapeAttr(item.id)}">×</button>
      </div>
    `;
  }

  function setComposerDragging(active) {
    $("composerInputShell")?.classList.toggle("dragging", Boolean(active));
  }

  function eventHasFiles(event) {
    return Array.from(event?.dataTransfer?.types || []).includes("Files");
  }

  function handleAttachmentDragOver(event) {
    if (!eventHasFiles(event)) return;
    event.preventDefault();
    setComposerDragging(true);
  }

  function handleAttachmentDragLeave(event) {
    const shell = $("composerInputShell");
    if (!shell || shell.contains(event.relatedTarget)) return;
    setComposerDragging(false);
  }

  function handleAttachmentDrop(event) {
    if (!eventHasFiles(event)) return;
    event.preventDefault();
    setComposerDragging(false);
    addPendingAttachmentFiles(event.dataTransfer?.files || []);
  }

  function setMessageInputValue(value, { saveDraft = true } = {}) {
    const input = $("messageText");
    input.value = value;
    autoResizeMessageInput();
    updateSlashCommandPalette();
    if (saveDraft) saveCurrentChatDraft();
    window.setTimeout(() => {
      input.selectionStart = input.value.length;
      input.selectionEnd = input.value.length;
    }, 0);
  }

  function updatePromptHistoryHint() {
    const hint = $("promptHistoryHint");
    if (!hint) return;
    const count = currentPromptHistory().length;
    const commandCount = enabledSlashCommands().length;
    const active = state.promptHistoryIndex >= 0;
    hint.textContent = active
      ? t("workspace.chat.historyActive", { index: state.promptHistoryIndex + 1, count })
      : commandCount
        ? t("workspace.chat.historyCommands", { count: commandCount })
        : count
          ? t("workspace.chat.historySaved", { count })
          : t("workspace.chat.historyEmpty");
  }

  function hideMentionPalette() {
    state.mentionOpen = false;
    state.mentionUsers = [];
    state.mentionIndex = 0;
    const palette = $("mentionPalette");
    if (palette) {
      palette.classList.add("hidden");
      palette.innerHTML = "";
    }
  }

  function insertMention(user) {
    const input = $("messageText");
    const trigger = mentionTrigger(input?.value || "", input?.selectionStart || 0);
    if (!input || !trigger || !user?.handle) return false;
    input.setRangeText(`@${user.handle} `, trigger.start, trigger.end, "end");
    hideMentionPalette();
    handleMessageInput();
    input.focus();
    return true;
  }

  function renderMentionPalette() {
    const palette = $("mentionPalette");
    if (!palette) return;
    const users = Array.isArray(state.mentionUsers) ? state.mentionUsers : [];
    if (!state.mentionOpen || !users.length) {
      hideMentionPalette();
      return;
    }
    state.mentionIndex = Math.max(0, Math.min(Number(state.mentionIndex || 0), users.length - 1));
    palette.classList.remove("hidden");
    palette.innerHTML = users.map((user, index) => `
      <button class="slash-command-item ${index === state.mentionIndex ? "active" : ""}" type="button" data-mention-user="${escapeAttr(user.id || user.handle)}">
        <span class="slash-command-name">@${escapeHtml(user.handle || "")}</span>
        <span class="slash-command-desc">${escapeHtml(user.role || "user")}</span>
      </button>
    `).join("");
    palette.querySelectorAll("[data-mention-user]").forEach((button, index) => {
      button.addEventListener("mousedown", (event) => event.preventDefault());
      button.addEventListener("click", () => insertMention(users[index]));
    });
  }

  async function updateMentionPalette() {
    if (state.mentionComposing) return;
    const input = $("messageText");
    const trigger = mentionTrigger(input?.value || "", input?.selectionStart || 0);
    if (!trigger) {
      hideMentionPalette();
      return;
    }
    const seq = Number(state.mentionSeq || 0) + 1;
    state.mentionSeq = seq;
    try {
      const users = await api(`/api/users?handlePrefix=${encodeURIComponent(trigger.query)}&limit=8`);
      if (seq !== state.mentionSeq) return;
      state.mentionUsers = Array.isArray(users) ? users : [];
      state.mentionOpen = state.mentionUsers.length > 0;
      state.mentionIndex = 0;
      renderMentionPalette();
    } catch (error) {
      if (seq === state.mentionSeq && error?.status === 401) hideMentionPalette();
    }
  }

  function handleMentionKeydown(event) {
    if (!state.mentionOpen || state.mentionComposing) return false;
    const users = Array.isArray(state.mentionUsers) ? state.mentionUsers : [];
    if (!users.length) return false;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      state.mentionIndex = event.key === "ArrowDown"
        ? (state.mentionIndex + 1) % users.length
        : (state.mentionIndex - 1 + users.length) % users.length;
      renderMentionPalette();
      event.preventDefault();
      return true;
    }
    if (event.key === "Enter" || event.key === "Tab") {
      if (insertMention(users[state.mentionIndex])) event.preventDefault();
      return true;
    }
    if (event.key === "Escape") {
      hideMentionPalette();
      event.preventDefault();
      return true;
    }
    return false;
  }

  function enabledSlashCommands() {
    const localTemplates = currentSkillsPreferences().commands;
    const skillCommands = typeof getEffectiveSkillsPolicy === "function"
      ? slashCommandsForEffectivePolicy(getEffectiveSkillsPolicy(), localTemplates)
      : mergeSlashCommands(state.serverSkills, localTemplates);
    const context = state.navigationSelectionKind === "project" ? "project" : "conversation";
    return slashCommandsForContext(context, skillCommands);
  }

  function slashCommandTrigger(value) {
    const text = String(value || "");
    const match = text.match(/^\s*\/([^\s]*)$/);
    if (!match) return null;
    return {
      prefix: text.slice(0, match.index || 0),
      query: match[1].toLowerCase(),
    };
  }

  function slashCommandMatches() {
    const input = $("messageText");
    const trigger = slashCommandTrigger(input?.value || "");
    if (!trigger) return [];
    const query = trigger.query;
    return enabledSlashCommands().filter((command) => {
      const haystack = `${command.name} ${command.description}`.toLowerCase();
      return !query || haystack.includes(query);
    }).slice(0, 8);
  }

  function slashCommandOptionId(command, index) {
    return `slash-command-option-${String(command?.id || index).replace(/[^a-zA-Z0-9_-]/g, "-")}`;
  }

  function updateSlashCommandPalette() {
    const palette = $("slashCommandPalette");
    if (!palette) return;
    const input = $("messageText");
    const trigger = slashCommandTrigger(input?.value || "");
    const matches = trigger ? slashCommandMatches() : [];
    state.slashCommandOpen = Boolean(trigger && matches.length);
    state.slashCommandQuery = trigger?.query || "";
    if (!state.slashCommandOpen) {
      state.slashCommandIndex = 0;
      input?.setAttribute("aria-expanded", "false");
      input?.removeAttribute("aria-activedescendant");
      palette.classList.add("hidden");
      palette.innerHTML = "";
      return;
    }
    state.slashCommandIndex = Math.max(0, Math.min(state.slashCommandIndex, matches.length - 1));
    input?.setAttribute("aria-expanded", "true");
    input?.setAttribute("aria-activedescendant", slashCommandOptionId(matches[state.slashCommandIndex], state.slashCommandIndex));
    palette.classList.remove("hidden");
    palette.innerHTML = `
      <div class="slash-command-head">${escapeHtml(t("workspace.chat.slashCommands"))}</div>
      ${matches.map((command, index) => `
        <button id="${escapeAttr(slashCommandOptionId(command, index))}" class="slash-command-item ${index === state.slashCommandIndex ? "active" : ""}" type="button" role="option" aria-selected="${index === state.slashCommandIndex ? "true" : "false"}" data-slash-command="${escapeAttr(command.id)}">
          <span class="slash-command-name">${escapeHtml(command.name)}</span>
          <span class="slash-command-desc">${escapeHtml(command.description || command.prompt.slice(0, 120))}</span>
        </button>
      `).join("")}
    `;
    palette.querySelectorAll("[data-slash-command]").forEach((node) => {
      node.addEventListener("mousedown", (event) => event.preventDefault());
      node.addEventListener("click", () => applySlashCommand(node.dataset.slashCommand));
    });
  }

  function hideSlashCommandPalette() {
    state.slashCommandOpen = false;
    state.slashCommandIndex = 0;
    state.slashCommandQuery = "";
    const input = $("messageText");
    input?.setAttribute("aria-expanded", "false");
    input?.removeAttribute("aria-activedescendant");
    const palette = $("slashCommandPalette");
    if (palette) {
      palette.classList.add("hidden");
      palette.innerHTML = "";
    }
  }

  function applySlashCommand(id) {
    const command = enabledSlashCommands().find((item) => item.id === id) || slashCommandMatches()[state.slashCommandIndex];
    if (!command) return false;
    const input = $("messageText");
    const value = input?.value || "";
    const insertion = slashCommandInsertion(command);
    const next = value.replace(/^\s*\/[^\s]*$/, insertion);
    setMessageInputValue(next);
    hideSlashCommandPalette();
    resetPromptHistoryNavigation();
    input?.focus();
    showToast(t("workspace.chat.slashInserted", { name: command.name }), "success");
    return true;
  }

  function handleSlashCommandKeydown(event) {
    if (!state.slashCommandOpen || isComposingInput(event)) return false;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      const count = slashCommandMatches().length;
      if (!count) return false;
      state.slashCommandIndex = event.key === "ArrowDown"
        ? (state.slashCommandIndex + 1) % count
        : (state.slashCommandIndex - 1 + count) % count;
      updateSlashCommandPalette();
      event.preventDefault();
      return true;
    }
    if (event.key === "Enter" || event.key === "Tab") {
      const selected = slashCommandMatches()[state.slashCommandIndex];
      if (selected && applySlashCommand(selected.id)) {
        event.preventDefault();
        return true;
      }
    }
    if (event.key === "Escape") {
      hideSlashCommandPalette();
      event.preventDefault();
      return true;
    }
    return false;
  }

  function handlePromptHistoryNavigation(event) {
    if (isComposingInput(event) || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return false;
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown" && event.key !== "Escape") return false;
    const input = $("messageText");
    const history = currentPromptHistory();
    if (event.key === "Escape" && state.promptHistoryIndex >= 0) {
      setMessageInputValue(state.promptHistoryDraft || "");
      resetPromptHistoryNavigation();
      updatePromptHistoryHint();
      event.preventDefault();
      return true;
    }
    if (!history.length || (input.value.trim() && state.promptHistoryIndex < 0)) return false;
    if (event.key === "ArrowUp") {
      if (state.promptHistoryIndex < 0) state.promptHistoryDraft = input.value;
      state.promptHistoryIndex = Math.min(history.length - 1, state.promptHistoryIndex + 1);
      setMessageInputValue(history[state.promptHistoryIndex] || "");
      event.preventDefault();
      return true;
    }
    if (event.key === "ArrowDown" && state.promptHistoryIndex >= 0) {
      state.promptHistoryIndex -= 1;
      setMessageInputValue(state.promptHistoryIndex >= 0 ? history[state.promptHistoryIndex] : state.promptHistoryDraft || "");
      if (state.promptHistoryIndex < 0) resetPromptHistoryNavigation();
      updatePromptHistoryHint();
      event.preventDefault();
      return true;
    }
    return false;
  }

  function handleMessageInput() {
    resetPromptHistoryNavigation();
    autoResizeMessageInput();
    updateSlashCommandPalette();
    updateMentionPalette();
    saveCurrentChatDraft();
  }

  function handleMessageKeydown(event) {
    if (handleMentionKeydown(event)) return;
    if (handleSlashCommandKeydown(event)) return;
    if (handlePromptHistoryNavigation(event)) return;
    if (isComposingInput(event) || event.key !== "Enter" || event.shiftKey || event.metaKey || event.ctrlKey || event.altKey) {
      return;
    }
    event.preventDefault();
    $("messageForm").requestSubmit();
  }

  return {
    autoResizeMessageInput,
    handleAttachmentDragLeave,
    handleAttachmentDragOver,
    handleAttachmentDrop,
    handleMessageInput,
    handleMessageKeydown,
    handleMessagePaste,
    hideMentionPalette,
    hideSlashCommandPalette,
    importAttachmentFiles,
    loadChatDrafts,
    loadPromptHistory,
    openAttachmentPicker,
    refreshFastModeControl,
    refreshMessageModeControl,
    refreshReasoningEffortControl,
    restoreCurrentChatDraft,
    saveCurrentChatDraft,
    saveFastMode,
    saveReasoningEffort,
    scheduleMessageInputResize,
    setMessageMode,
    selectedReasoningEffort,
    sendMessage,
    setMessageInputValue,
    syncMessageComposerBusy,
    toggleFastMode,
    updateDraftLimitHint,
    updateMentionPalette,
    updatePromptHistoryHint,
    updateSlashCommandPalette,
  };
}
