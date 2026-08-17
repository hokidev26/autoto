import { $, setButtonBusy } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { appMainT as am } from "./messages-app-main-extra.mjs";
import { api } from "./runtime.mjs";
import {
  applyServerSkillsLoadResult,
  hydrateServerSkillSummaries,
  isOptimisticSkillConflict,
  loadServerSkillsWithFallback,
} from "./skills-bootstrap.mjs";

// Loads and mutates the various server-backed settings resources shown in
// the settings panels: health/runtime status, notification settings, server
// skills CRUD, workflow preferences, tool permission rules, model catalog,
// storage/license/update summaries. Pure data-fetching + state mutation;
// no DOM rendering beyond the small health/runtime status badges.
export function createServerResourceLoaders({
  state,
  showToast,
  showError,
  notifyTerminal,
  refreshActiveSettingsPanel,
  updateSecurityModeUI,
  invalidateAndRefreshEffectiveSkillsPolicy,
  renderModelOptions,
  refreshReasoningEffortControl,
  refreshFastModeControl,
  updateSlashCommandPalette,
  updatePromptHistoryHint,
  loadProviderAuthFiles,
  loadRemoteAccess,
}) {
  function updateRuntimeStatusButton() {
    const button = $("runtimeStatusBtn");
    const indicator = $("runtimeStatusIndicator");
    if (!button || !indicator) return;
    const streamStatus = state.agentStreamStatus || "idle";
    const security = state.runtimeSummary?.security || {};
    const remoteWarning = Boolean(security.remoteAccessRequired && !security.accessPasswordConfigured);
    let tone = "idle";
    if (state.healthOK === false || state.runtimeError || streamStatus === "offline" || remoteWarning) tone = "error";
    else if (["syncing", "resyncing", "connecting", "reconnecting"].includes(streamStatus)) tone = "busy";
    else if (state.healthOK === true && (!state.agent || streamStatus === "connected")) tone = "ok";
    if (state.holdRuntimeStatusTone) {
      if (tone === "error") state.holdRuntimeStatusTone = false;
      else if (tone === "ok" && streamStatus === "connected") state.holdRuntimeStatusTone = false;
      else tone = "ok";
    }
    indicator.className = `header-tool-indicator ${tone}`;
    button.classList.toggle("tool-error", tone === "error");
    const streamLabel = state.agent ? `Agent ${streamStatus}` : t("chat.noAgent");
    button.title = `${t("workspace.main.conversationDetails")} · ${state.healthLabel || "checking"} · ${streamLabel}${remoteWarning ? " · access password is not configured" : ""}`;
  }

  function setHealth(ok, text) {
    state.healthOK = Boolean(ok);
    state.healthLabel = text;
    const badge = $("healthBadge");
    if (badge) {
      badge.textContent = text;
      badge.classList.toggle("ok", ok);
      badge.classList.toggle("err", !ok);
    }
    const globalHealthDot = $("globalHealthDot");
    const globalHealthText = $("globalHealthText");
    globalHealthDot?.classList.toggle("ok", ok);
    globalHealthDot?.classList.toggle("err", !ok);
    if (globalHealthText) globalHealthText.textContent = t(ok ? "shell.online" : "shell.offline");
    updateRuntimeStatusButton();
  }

  async function loadHealth() {
    const seq = ++state.healthSeq;
    try {
      const health = await api("/api/health");
      if (seq !== state.healthSeq) return;
      setHealth(true, `healthy ${health.version}`);
    } catch {
      if (seq !== state.healthSeq) return;
      setHealth(false, "offline");
    }
  }

  async function loadServerNotificationSettings({ notify = false } = {}) {
    state.serverNotificationLoading = true;
    state.serverNotificationError = "";
    try {
      state.serverNotificationSettings = await api("/api/notifications/settings");
      if (notify) notifyTerminal(`[info] ${am("notificationSettingsRefreshed")}\n`);
    } catch (err) {
      state.serverNotificationSettings = null;
      state.serverNotificationError = err.message || String(err);
      if (notify) notifyTerminal(`[warn] ${am("notificationSettingsRefreshFailed", { message: state.serverNotificationError })}\n`);
    } finally {
      state.serverNotificationLoading = false;
    }
    if (state.activeSettingsPanel === "notifications") refreshActiveSettingsPanel();
  }

  async function saveServerNotificationSettings(payload) {
    state.serverNotificationSaving = true;
    state.serverNotificationError = "";
    try {
      state.serverNotificationSettings = await api("/api/notifications/settings", { method: "PUT", body: JSON.stringify(payload) });
      showToast(am("notificationSettingsSaved"), "success", { force: true });
      notifyTerminal(`[info] ${am("notificationSettingsSaved")}\n`);
    } catch (err) {
      state.serverNotificationError = err.message || String(err);
      showError(err);
    } finally {
      state.serverNotificationSaving = false;
      if (state.activeSettingsPanel === "notifications") refreshActiveSettingsPanel();
    }
  }

  function sortServerSkills(a, b) {
    return Number(Boolean(b?.enabled)) - Number(Boolean(a?.enabled))
      || String(a?.command || "").localeCompare(String(b?.command || ""));
  }

  function refreshServerSkillsUI() {
    updateSlashCommandPalette();
    updatePromptHistoryHint();
    if (state.activeSettingsPanel === "skills") refreshActiveSettingsPanel();
  }

  async function loadServerSkills({ notify = false } = {}) {
    const seq = ++state.serverSkillsLoadSeq;
    const previous = Array.isArray(state.serverSkills) ? state.serverSkills : [];
    if (state.serverSkillsStatus === "ready" || state.serverSkillsStatus === "stale") state.serverSkillsHadServerData = true;
    const hadServerData = state.serverSkillsHadServerData;
    state.serverSkillsStatus = "loading";
    state.serverSkillsError = "";
    refreshServerSkillsUI();
    const result = await loadServerSkillsWithFallback(async () => {
      const summaries = await api("/api/skills");
      return hydrateServerSkillSummaries(summaries, (id) => api(`/api/skills/${encodeURIComponent(id)}`), 4);
    }, previous, { hadServerData });
    if (seq !== state.serverSkillsLoadSeq) return state.serverSkills;
    applyServerSkillsLoadResult(state, seq, { ...result, skills: result.skills.sort(sortServerSkills) });
    if (result.status === "ready") state.serverSkillsHadServerData = true;
    if (notify) {
      const fallback = hadServerData ? am("serverSkillsFallbackKept") : am("serverSkillsNoAuthoritative");
      notifyTerminal(result.error
        ? `[warn] ${am("serverSkillsRefreshFailed", { fallback, message: result.error })}\n`
        : `[info] ${am("serverSkillsRefreshed")}\n`);
    }
    refreshServerSkillsUI();
    return state.serverSkills;
  }

  async function loadServerSkillDetail(id) {
    const skill = (state.serverSkills || []).find((item) => item.id === id);
    if (!skill) throw new Error(am("serverSkillMissing"));
    if (skill.detailLoaded && String(skill.prompt || "").trim()) return skill;
    const requestedUpdatedAt = skill.updatedAt;
    try {
      const detail = await api(`/api/skills/${encodeURIComponent(id)}`);
      const latest = (state.serverSkills || []).find((item) => item.id === id);
      if (!latest) throw new Error(am("serverSkillMissing"));
      if (latest.updatedAt !== requestedUpdatedAt && detail.updatedAt !== latest.updatedAt) {
        throw new Error(am("serverSkillChanged"));
      }
      const merged = { ...latest, ...detail, detailLoaded: true, detailError: "" };
      state.serverSkills = (state.serverSkills || []).map((item) => item.id === id ? merged : item).sort(sortServerSkills);
      refreshServerSkillsUI();
      return merged;
    } catch (err) {
      const message = err?.message || String(err);
      state.serverSkills = (state.serverSkills || []).map((item) => {
        if (item.id !== id || item.updatedAt !== requestedUpdatedAt) return item;
        return { ...item, detailLoaded: false, detailError: message };
      }).sort(sortServerSkills);
      refreshServerSkillsUI();
      throw err;
    }
  }

  async function createServerSkill(payload, { silent = false } = {}) {
    state.serverSkillsSaving = true;
    state.serverSkillsError = "";
    try {
      const created = await api("/api/skills", { method: "POST", body: JSON.stringify(payload) });
      state.serverSkills = [{ ...created, detailLoaded: true }, ...(state.serverSkills || []).filter((item) => item.id !== created.id)].sort(sortServerSkills);
      state.serverSkillsStatus = "ready";
      await invalidateAndRefreshEffectiveSkillsPolicy();
      if (!silent) showToast(am("serverSkillSaved"), "success", { force: true });
      return created;
    } catch (err) {
      state.serverSkillsError = err.message || String(err);
      throw err;
    } finally {
      state.serverSkillsSaving = false;
      refreshServerSkillsUI();
    }
  }

  async function updateServerSkill(id, payload, { silent = false } = {}) {
    state.serverSkillsSaving = true;
    state.serverSkillsError = "";
    try {
      const current = (state.serverSkills || []).find((item) => item.id === id);
      if (!current?.updatedAt) throw new Error(am("serverSkillVersionMissing"));
      const updated = await api(`/api/skills/${encodeURIComponent(id)}`, {
        method: "PATCH",
        body: JSON.stringify({ ...payload, expectedUpdatedAt: current.updatedAt }),
      });
      state.serverSkills = (state.serverSkills || []).map((item) => item.id === updated.id ? { ...updated, detailLoaded: true } : item).sort(sortServerSkills);
      state.serverSkillsStatus = "ready";
      await invalidateAndRefreshEffectiveSkillsPolicy();
      if (!silent) showToast(am("serverSkillUpdated"), "success", { force: true });
      return updated;
    } catch (err) {
      if (isOptimisticSkillConflict(err)) {
        await loadServerSkills();
        const message = state.serverSkillsStatus === "ready"
          ? am("serverSkillConflictRefreshed")
          : am("serverSkillConflictRefreshFailed");
        state.serverSkillsError = message;
        throw new Error(message);
      }
      state.serverSkillsError = err.message || String(err);
      throw err;
    } finally {
      state.serverSkillsSaving = false;
      refreshServerSkillsUI();
    }
  }

  async function deleteServerSkill(id) {
    state.serverSkillsSaving = true;
    state.serverSkillsError = "";
    try {
      await api(`/api/skills/${encodeURIComponent(id)}`, { method: "DELETE" });
      state.serverSkills = (state.serverSkills || []).filter((item) => item.id !== id);
      await invalidateAndRefreshEffectiveSkillsPolicy();
      showToast(am("serverSkillDeleted"), "success", { force: true });
    } catch (err) {
      state.serverSkillsError = err.message || String(err);
      throw err;
    } finally {
      state.serverSkillsSaving = false;
      refreshServerSkillsUI();
    }
  }

  async function previewServerSkillImport(content) {
    return api("/api/skills/import/preview", { method: "POST", body: JSON.stringify({ content }) });
  }

  async function importServerSkill(content) {
    state.serverSkillsSaving = true;
    state.serverSkillsError = "";
    try {
      const imported = await api("/api/skills/import", { method: "POST", body: JSON.stringify({ content }) });
      state.serverSkills = [{ ...imported, detailLoaded: true }, ...(state.serverSkills || []).filter((item) => item.id !== imported.id)].sort(sortServerSkills);
      state.serverSkillsStatus = "ready";
      await invalidateAndRefreshEffectiveSkillsPolicy();
      showToast(am("skillImportedDisabled"), "success", { force: true });
      return imported;
    } catch (err) {
      state.serverSkillsError = err.message || String(err);
      throw err;
    } finally {
      state.serverSkillsSaving = false;
      refreshServerSkillsUI();
    }
  }

  async function loadWorkflowPreferences({ notify = false } = {}) {
    state.workflowLoading = true;
    state.workflowError = "";
    try {
      state.workflowPreferences = await api("/api/workflow/preferences");
      if (notify) notifyTerminal(`[info] ${am("workflowPreferencesRefreshed")}\n`);
    } catch (err) {
      state.workflowError = err.message || String(err);
      if (notify) notifyTerminal(`[warn] ${am("workflowPreferencesRefreshFailed", { message: state.workflowError })}\n`);
    } finally {
      state.workflowLoading = false;
      if (state.activeSettingsPanel === "skills") refreshActiveSettingsPanel();
    }
  }

  async function saveWorkflowPreferences(payload) {
    state.workflowSaving = true;
    state.workflowError = "";
    try {
      state.workflowPreferences = await api("/api/workflow/preferences", { method: "PUT", body: JSON.stringify(payload) });
      showToast(am("toolPermissionPreferencesSaved"), "success", { force: true });
    } catch (err) {
      state.workflowError = err.message || String(err);
      showError(err);
    } finally {
      state.workflowSaving = false;
      if (state.activeSettingsPanel === "skills") refreshActiveSettingsPanel();
    }
  }

  async function loadToolPermissionRules({ notify = false } = {}) {
    state.toolPermissionRulesLoading = true;
    state.toolPermissionRulesError = "";
    try {
      state.toolPermissionRules = await api("/api/workflow/tool-permissions");
      if (notify) notifyTerminal(`[info] ${am("toolPermissionRulesRefreshed")}\n`);
    } catch (err) {
      state.toolPermissionRulesError = err.message || String(err);
      if (notify) notifyTerminal(`[warn] ${am("toolPermissionRulesRefreshFailed", { message: state.toolPermissionRulesError })}\n`);
    } finally {
      state.toolPermissionRulesLoading = false;
      if (state.activeSettingsPanel === "skills") refreshActiveSettingsPanel();
    }
  }

  async function createToolPermissionRule(payload) {
    state.toolPermissionRulesSaving = true;
    state.toolPermissionRulesError = "";
    try {
      const rule = await api("/api/workflow/tool-permissions", { method: "POST", body: JSON.stringify(payload) });
      state.toolPermissionRules = [rule, ...(state.toolPermissionRules || [])].sort(toolPermissionRuleSort);
      showToast(am("toolPermissionRuleAdded"), "success", { force: true });
    } catch (err) {
      state.toolPermissionRulesError = err.message || String(err);
      showError(err);
    } finally {
      state.toolPermissionRulesSaving = false;
      if (state.activeSettingsPanel === "skills") refreshActiveSettingsPanel();
    }
  }

  async function updateToolPermissionRule(id, payload) {
    state.toolPermissionRulesSaving = true;
    state.toolPermissionRulesError = "";
    try {
      const rule = await api(`/api/workflow/tool-permissions/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(payload) });
      state.toolPermissionRules = (state.toolPermissionRules || []).map((item) => item.id === id ? rule : item).sort(toolPermissionRuleSort);
      showToast(am("toolPermissionRuleUpdated"), "success", { force: true });
    } catch (err) {
      state.toolPermissionRulesError = err.message || String(err);
      showError(err);
    } finally {
      state.toolPermissionRulesSaving = false;
      if (state.activeSettingsPanel === "skills") refreshActiveSettingsPanel();
    }
  }

  async function deleteToolPermissionRule(id) {
    state.toolPermissionRulesSaving = true;
    state.toolPermissionRulesError = "";
    try {
      await api(`/api/workflow/tool-permissions/${encodeURIComponent(id)}`, { method: "DELETE" });
      state.toolPermissionRules = (state.toolPermissionRules || []).filter((item) => item.id !== id);
      showToast(am("toolPermissionRuleDeleted"), "success", { force: true });
    } catch (err) {
      state.toolPermissionRulesError = err.message || String(err);
      showError(err);
    } finally {
      state.toolPermissionRulesSaving = false;
      if (state.activeSettingsPanel === "skills") refreshActiveSettingsPanel();
    }
  }

  function toolPermissionRuleSort(a, b) {
    return Number(b?.priority || 0) - Number(a?.priority || 0) || String(a?.createdAt || "").localeCompare(String(b?.createdAt || ""));
  }

  async function loadWorkflowPolicy({ notify = false } = {}) {
    await Promise.allSettled([loadWorkflowPreferences({ notify }), loadToolPermissionRules({ notify })]);
  }

  async function loadModelCatalog() {
    const seq = ++state.modelCatalogSeq;
    try {
      const catalog = await api("/api/models");
      if (seq !== state.modelCatalogSeq) return;
      state.modelCatalog = catalog;
    } catch (err) {
      if (seq !== state.modelCatalogSeq) return;
      state.modelCatalog = { providers: [], error: err.message };
    }
    renderModelOptions();
    refreshReasoningEffortControl();
    refreshFastModeControl();
    refreshActiveSettingsPanel();
  }

  async function loadStorageSummary({ notify = false } = {}) {
    const seq = ++state.storageSeq;
    const button = $("refreshStorageSummaryBtn");
    setButtonBusy(button, true, am("scanning"));
    try {
      const summary = await api("/api/storage/summary");
      if (seq !== state.storageSeq) return;
      state.storageSummary = summary;
      state.storageError = "";
      if (notify) notifyTerminal(`[info] ${am("storageRefreshed")}\n`);
    } catch (err) {
      if (seq !== state.storageSeq) return;
      state.storageSummary = null;
      state.storageError = err.message || String(err);
      if (notify) notifyTerminal(`[warn] ${am("storageRefreshFailed", { message: state.storageError })}\n`);
    } finally {
      if (seq === state.storageSeq) setButtonBusy(button, false, am("scanning"));
    }
    if (seq === state.storageSeq && state.activeSettingsPanel === "storage") refreshActiveSettingsPanel();
  }

  async function loadLicenseSummary({ notify = false } = {}) {
    const seq = ++state.licenseSeq;
    const button = $("refreshLicensesBtn");
    setButtonBusy(button, true, am("refreshing"));
    try {
      const summary = await api("/api/licenses");
      if (seq !== state.licenseSeq) return;
      state.licenseSummary = summary;
      state.licenseError = "";
      if (notify) notifyTerminal(`[info] ${am("licensesRefreshed")}\n`);
    } catch (err) {
      if (seq !== state.licenseSeq) return;
      state.licenseSummary = null;
      state.licenseError = err.message || String(err);
      if (notify) notifyTerminal(`[warn] ${am("licensesRefreshFailed", { message: state.licenseError })}\n`);
    } finally {
      if (seq === state.licenseSeq) setButtonBusy(button, false, am("refreshing"));
    }
    if (seq === state.licenseSeq && state.activeSettingsPanel === "about") refreshActiveSettingsPanel();
  }

  async function loadUpdateStatus({ notify = false } = {}) {
    const seq = ++state.updateSeq;
    const button = $("checkForUpdatesBtn");
    setButtonBusy(button, true, am("checking"));
    try {
      const status = await api("/api/update/status");
      if (seq !== state.updateSeq) return;
      state.updateStatus = status;
      state.updateError = "";
      if (notify) notifyTerminal(`[info] ${am("updateStatus", { status: status?.status || "unknown" })}\n`);
    } catch (err) {
      if (seq !== state.updateSeq) return;
      state.updateStatus = null;
      state.updateError = err.message || String(err);
      if (notify) notifyTerminal(`[warn] ${am("updateCheckFailed", { message: state.updateError })}\n`);
    } finally {
      if (seq === state.updateSeq) setButtonBusy(button, false, am("checking"));
    }
    if (seq === state.updateSeq && state.activeSettingsPanel === "about") refreshActiveSettingsPanel();
  }

  async function loadRuntimeSummary({ notify = false } = {}) {
    const seq = ++state.runtimeSeq;
    const button = $("refreshRuntimeSummaryBtn");
    setButtonBusy(button, true, am("refreshing"));
    try {
      const summary = await api("/api/runtime/summary");
      if (seq !== state.runtimeSeq) return;
      state.runtimeSummary = summary;
      state.runtimeError = "";
      updateSecurityModeUI();
      if (notify) notifyTerminal(`[info] ${am("runtimeRefreshed")}\n`);
    } catch (err) {
      if (seq !== state.runtimeSeq) return;
      state.runtimeSummary = null;
      state.runtimeError = err.message || String(err);
      updateSecurityModeUI();
      if (notify) notifyTerminal(`[warn] ${am("runtimeRefreshFailed", { message: state.runtimeError })}\n`);
    } finally {
      if (seq === state.runtimeSeq) setButtonBusy(button, false, am("refreshing"));
    }
    if (seq === state.runtimeSeq && ["servers-system", "runtime"].includes(state.activeSettingsPanel)) refreshActiveSettingsPanel();
  }

  function warmSettingsData() {
    if (state.settingsWarmupStarted) return;
    state.settingsWarmupStarted = true;
    const tasks = [];
    if (!state.runtimeSummary && !state.runtimeError) tasks.push(loadRuntimeSummary());
    if (!state.remoteAccess && !state.remoteAccessError) tasks.push(loadRemoteAccess());
    if (!state.storageSummary && !state.storageError) tasks.push(loadStorageSummary());
    if (!state.licenseSummary && !state.licenseError) tasks.push(loadLicenseSummary());
    if (!state.providerAuthFiles && !state.providerAuthError) tasks.push(loadProviderAuthFiles({ silent: true }));
    if (!state.serverNotificationSettings && !state.serverNotificationError) tasks.push(loadServerNotificationSettings());
    if (!state.workflowPreferences && !state.workflowError) tasks.push(loadWorkflowPreferences());
    if (!state.toolPermissionRules.length && !state.toolPermissionRulesError) tasks.push(loadToolPermissionRules());
    if (state.serverSkillsStatus === "idle") tasks.push(loadServerSkills());
    Promise.allSettled(tasks).catch(() => {});
  }

  return {
    updateRuntimeStatusButton,
    setHealth,
    loadHealth,
    loadServerNotificationSettings,
    saveServerNotificationSettings,
    sortServerSkills,
    refreshServerSkillsUI,
    loadServerSkills,
    loadServerSkillDetail,
    createServerSkill,
    updateServerSkill,
    deleteServerSkill,
    previewServerSkillImport,
    importServerSkill,
    loadWorkflowPreferences,
    saveWorkflowPreferences,
    loadToolPermissionRules,
    createToolPermissionRule,
    updateToolPermissionRule,
    deleteToolPermissionRule,
    toolPermissionRuleSort,
    loadWorkflowPolicy,
    loadModelCatalog,
    loadStorageSummary,
    loadLicenseSummary,
    loadUpdateStatus,
    loadRuntimeSummary,
    warmSettingsData,
  };
}
