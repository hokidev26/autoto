import { $, escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { formatBytes, formatDuration, formatNumber, formatTimestamp } from "./formatters.mjs";
import { currentUILocale, t as baseT } from "./i18n.mjs";
import systemSettingsMessages from "./messages-system-settings.mjs?v=about-brand-license-1-desktop-shell-1-execution-budget-2-background-task-settings-1-settings-ui-cleanup-1";
import { localPreferenceBackupVersion } from "./preferences-data.mjs";
import { api } from "./runtime.mjs";
import {
  clearPendingDesktopUpdate,
  disableAutostart,
  enableAutostart,
  getAutostartStatus,
  getPendingDesktopUpdate,
  isDesktopShell,
  stageDesktopUpdate,
} from "./desktop-shell-ui.mjs";

function lookupMessage(catalog, key) {
  return String(key || "").split(".").reduce((value, part) => (
    value && typeof value === "object" ? value[part] : undefined
  ), catalog);
}

function interpolateMessage(message, params = {}) {
  return String(message).replace(/\{([A-Za-z0-9_]+)\}/g, (match, name) => (
    Object.prototype.hasOwnProperty.call(params, name) ? String(params[name] ?? "") : match
  ));
}

function t(key, params = {}) {
  const locale = currentUILocale();
  const message = lookupMessage(systemSettingsMessages[locale], key)
    ?? lookupMessage(systemSettingsMessages["zh-CN"], key);
  return message === undefined ? baseT(key, params) : interpolateMessage(message, params);
}

// Ranges mirror strictContinuationSettings in internal/server. `scale` converts
// the UI unit into the wire unit, so duration is edited in minutes.
const executionBudgetFields = [
  { key: "segmentTurns", id: "runtimeBudgetSegmentTurns", labelKey: "segmentTurnsBudget", unitKey: "turnsUnit", fallback: 40, min: 1, max: 1000, scale: 1 },
  { key: "maxTotalTurns", id: "runtimeBudgetTotalTurns", labelKey: "totalTurnsBudget", unitKey: "turnsUnit", fallback: 200, min: 1, max: 10000, scale: 1, minFollowsSegmentTurns: true },
  { key: "maxRunTokens", id: "runtimeBudgetTokens", labelKey: "tokenBudget", unitKey: "tokensUnit", fallback: 2000000, min: 1000, max: 10000000, scale: 1 },
  { key: "maxRunDurationMs", id: "runtimeBudgetDurationMinutes", labelKey: "durationBudget", unitKey: "minutesUnit", fallback: 60, min: 1, max: 1440, scale: 60000 },
  { key: "maxContinuations", id: "runtimeBudgetContinuations", labelKey: "continuationsBudget", unitKey: "timesUnit", fallback: 8, min: 0, max: 64, scale: 1 },
];

const defaultContinuationSegmentTurns = 40;

// Remembers the last real number per field so toggling "unlimited" off restores
// what the user typed instead of snapping back to the generic fallback.
const executionBudgetDrafts = {};

// Mirrors the bounds in internal/config so the UI rejects what the endpoint
// would reject, instead of showing a pattern that disappears after saving.
const retryPolicyMaxPatterns = 32;
const retryPolicyPatternMinLength = 3;
const retryPolicyPatternMaxLength = 200;

const defaultBackgroundTaskSettings = {
  workerCount: 8,
  perAgentLimit: 4,
  allowNestedSubagents: false,
  maxSubagentDepth: 2,
};

const backgroundTaskSettingFields = [
  { key: "workerCount", id: "runtimeBackgroundWorkerCount", min: 1, max: 16 },
  { key: "perAgentLimit", id: "runtimeBackgroundPerAgentLimit", min: 1, max: 8 },
  { key: "maxSubagentDepth", id: "runtimeMaxSubagentDepth", min: 2, max: 4 },
];

export function createSystemSettingsController({
  state,
  copyText,
  loadLicenseSummary,
  loadRuntimeSummary,
  loadStorageSummary,
  loadUpdateStatus,
  localPreferencesBackupSummary,
  localPreferencesBackupText,
  notifyTerminal,
  refreshActiveSettingsPanel,
  restoreLocalPreferencesBackup,
  showError,
  showToast,
} = {}) {
  // Unsaved edits to the permanent-error list. Null means "show what the server
  // returned"; an array means the user has pending changes that a summary reload
  // must not silently discard.
  //
  // Scoped to the controller rather than the module: a module-level draft
  // outlives the panel that created it, so a stale list would shadow the stored
  // one after the value changed elsewhere.
  let retryPolicyDraft = null;

  function renderServerSystemSettingsContent() {
    const summary = state.runtimeSummary;
    const server = summary?.server || {};
    const process = summary?.process || {};
    const go = summary?.go || {};
    const address = server.address || `${state.settings?.server?.host || "localhost"}:${state.settings?.server?.port || "16888"}`;
    return `
    <div class="settings-live-page runtime-page">
      <section class="settings-hero-card settings-page-section settings-card">
        <div>
          <div class="settings-hero-kicker">${escapeHtml(t("systemSettings.serverSystem.kicker"))}</div>
          <div class="settings-hero-title">${escapeHtml(address)}</div>
          <p data-settings-help-copy>${escapeHtml(t("systemSettings.serverSystem.description"))}</p>
        </div>
        <div class="settings-action-row settings-toolbar">
          <button id="refreshRuntimeSummaryBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("systemSettings.serverSystem.refresh"))}</button>
        </div>
      </section>
      <div class="settings-status-strip settings-stat-grid">
        <div class="settings-stat-card"><strong>${escapeHtml(summary?.version || state.settings?.version || "0.1.0-dev")}</strong><span>${escapeHtml(t("systemSettings.serverSystem.version"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(process.pid ? `#${process.pid}` : t("systemSettings.serverSystem.unavailable"))}</strong><span>${escapeHtml(t("systemSettings.serverSystem.processId"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(go.version || t("systemSettings.serverSystem.notLoaded"))}</strong><span>${escapeHtml(t("systemSettings.serverSystem.goVersion"))}</span></div>
      </div>
      ${state.runtimeError ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(state.runtimeError)}</div>` : ""}
      ${summary ? renderServerSystemSummary(summary) : `<div class="settings-empty-card settings-empty-state">${escapeHtml(t("systemSettings.serverSystem.loading"))}</div>`}
    </div>
  `;
  }

  function renderServerSystemSummary(summary) {
    const server = summary.server || {};
    const process = summary.process || {};
    const go = summary.go || {};
    const providers = summary.providers || {};
    const backends = summary.backends || {};
    const security = summary.security || {};
    return `
    <div class="usage-summary-grid settings-stat-grid">
      ${/* The hint names where the value comes from, as the sibling cards do with
           their environment variables. It used to repeat serverSystem.description,
           the whole panel's help copy, which is already rendered above as
           data-settings-help-copy -- two sentences inside a 170px stat card. */""}
      ${renderUsageMetricCard(t("systemSettings.serverSystem.listenAddress"), server.address || t("systemSettings.serverSystem.notConfigured"), "config.json", "identifier")}
      ${renderUsageMetricCard(t("systemSettings.serverSystem.accessMode"), security.remoteAccessRequired ? t("systemSettings.serverSystem.tunnelHardened") : t("systemSettings.serverSystem.local"), security.message || t("systemSettings.serverSystem.securityFallback"))}
      ${renderUsageMetricCard(t("systemSettings.serverSystem.autoExecution"), security.bypassPermissionsAllowed ? t("systemSettings.serverSystem.allowed") : t("systemSettings.serverSystem.disabled"), t("systemSettings.serverSystem.permissionCap", { mode: security.maxPermissionMode || "bypassPermissions" }))}
      ${renderUsageMetricCard(t("systemSettings.serverSystem.remoteTerminal"), security.remoteTerminalAllowed ? t("systemSettings.serverSystem.allowed") : t("systemSettings.serverSystem.disabled"), "AUTOTO_REMOTE_TERMINAL")}
      ${renderUsageMetricCard(t("systemSettings.serverSystem.accessPassword"), security.accessPasswordConfigured ? t("systemSettings.serverSystem.configured") : t("systemSettings.serverSystem.notConfigured"), "AUTOTO_ACCESS_PASSWORD")}
      ${renderUsageMetricCard(t("systemSettings.serverSystem.uptime"), formatUptime(process.uptimeSeconds || 0), t("systemSettings.serverSystem.startedAt", { timestamp: formatTimestamp(process.startedAt) }))}
      ${renderUsageMetricCard(t("systemSettings.serverSystem.cpu"), go.cpus || 0, `${go.os || t("systemSettings.serverSystem.unknown")}/${go.arch || t("systemSettings.serverSystem.unknown")}`)}
      ${renderUsageMetricCard(t("systemSettings.serverSystem.provider"), providers.total || 0, t("systemSettings.serverSystem.providersConfigured", { count: formatNumber(providers.configured || 0) }))}
      ${renderUsageMetricCard(t("systemSettings.serverSystem.backendSeeds"), backends.configured || 0, t("systemSettings.serverSystem.backendsActive", { count: formatNumber(backends.active || 0) }))}
      ${renderUsageMetricCard(t("systemSettings.serverSystem.generatedAt"), formatTimestamp(summary.generatedAt), t("systemSettings.serverSystem.resampleHint"))}
    </div>
    <div class="usage-detail-grid">
      <section class="settings-info-card settings-card settings-card-content">
        <div class="settings-info-title">${escapeHtml(t("systemSettings.serverSystem.serviceConfig"))}</div>
        <div class="runtime-kv-list settings-data-list">
          ${renderRuntimeKeyValue(t("systemSettings.serverSystem.host"), server.host || "localhost")}
          ${renderRuntimeKeyValue(t("systemSettings.serverSystem.port"), server.port || 16888)}
          ${renderRuntimeKeyValue(t("systemSettings.serverSystem.config"), server.configPath || t("systemSettings.serverSystem.notConfigured"))}
          ${renderRuntimeKeyValue(t("systemSettings.serverSystem.executable"), process.executable || t("systemSettings.serverSystem.unknown"))}
        </div>
      </section>
      <section class="settings-info-card settings-card settings-card-content">
        <div class="settings-info-title">${escapeHtml(t("systemSettings.serverSystem.localPaths"))}</div>
        <div class="runtime-kv-list settings-data-list">
          ${(summary.paths || []).map((entry) => renderRuntimeKeyValue(entry.label || entry.key, entry.path || t("systemSettings.serverSystem.notConfigured"))).join("")}
        </div>
      </section>
    </div>
  `;
  }

  function renderRuntimeSettingsContent() {
    const summary = state.runtimeSummary;
    const memory = summary?.memory || {};
    const go = summary?.go || {};
    return `
    <div class="settings-live-page runtime-page">
      <section class="settings-hero-card settings-page-section settings-card">
        <div>
          <div class="settings-hero-kicker">${escapeHtml(t("systemSettings.runtimeResources.kicker"))}</div>
          <div class="settings-hero-title">${escapeHtml(formatBytes(memory.allocBytes || 0))} · ${escapeHtml(t("systemSettings.runtimeResources.goroutinesValue", { count: formatNumber(go.goroutines || 0) }))}</div>
          <p data-settings-help-copy>${escapeHtml(t("systemSettings.runtimeResources.description"))}</p>
        </div>
        <div class="settings-action-row settings-toolbar">
          <button id="refreshRuntimeSummaryBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("systemSettings.runtimeResources.refresh"))}</button>
        </div>
      </section>
      <div class="settings-status-strip settings-stat-grid">
        <div class="settings-stat-card"><strong>${escapeHtml(formatBytes(memory.sysBytes || 0))}</strong><span>${escapeHtml(t("systemSettings.runtimeResources.sysMemory"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(formatNumber(go.goroutines || 0))}</strong><span>${escapeHtml(t("systemSettings.runtimeResources.goroutines"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(formatNumber(memory.gcCycles || 0))}</strong><span>${escapeHtml(t("systemSettings.runtimeResources.gcCycles"))}</span></div>
      </div>
      ${state.runtimeError ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(state.runtimeError)}</div>` : ""}
      ${summary ? renderRuntimeResourceSummary(summary) : `<div class="settings-empty-card settings-empty-state">${escapeHtml(t("systemSettings.runtimeResources.loading"))}</div>`}
    </div>
  `;
  }

  function renderRuntimeResourceSummary(summary) {
    const memory = summary.memory || {};
    const go = summary.go || {};
    const agent = summary.agent || {};
    const security = summary.security || {};
    return `
    <div class="usage-summary-grid settings-stat-grid">
      ${renderUsageMetricCard(t("systemSettings.runtimeResources.currentAlloc"), formatBytes(memory.allocBytes || 0), t("systemSettings.runtimeResources.heapObjectsHint"))}
      ${renderUsageMetricCard(t("systemSettings.runtimeResources.heapInUse"), formatBytes(memory.heapInuseBytes || 0), t("systemSettings.runtimeResources.heapAllocHint", { size: formatBytes(memory.heapAllocBytes || 0) }))}
      ${renderUsageMetricCard(t("systemSettings.runtimeResources.stackInUse"), formatBytes(memory.stackInuseBytes || 0), t("systemSettings.runtimeResources.stackHint"))}
      ${renderUsageMetricCard(t("systemSettings.runtimeResources.nextGc"), formatBytes(memory.nextGcBytes || 0), t("systemSettings.runtimeResources.gcTimes", { count: formatNumber(memory.gcCycles || 0) }))}
      ${renderUsageMetricCard(t("systemSettings.runtimeResources.goroutines"), go.goroutines || 0, t("systemSettings.runtimeResources.cpusAvailable", { count: formatNumber(go.cpus || 0) }))}
      ${renderUsageMetricCard(t("systemSettings.runtimeResources.totalAlloc"), formatBytes(memory.totalAllocBytes || 0), t("systemSettings.runtimeResources.sinceStart"))}
    </div>
    <div class="usage-detail-grid">
      <section class="settings-info-card settings-card settings-card-content">
        <div class="settings-info-title">${escapeHtml(t("systemSettings.runtimeResources.agentDefaults"))}</div>
        <div class="runtime-kv-list settings-data-list">
          ${renderRuntimeKeyValue(t("systemSettings.runtimeResources.defaultModel"), agent.defaultModel || t("systemSettings.runtimeResources.notConfigured"))}
          ${renderRuntimeKeyValue(t("systemSettings.runtimeResources.summaryModel"), agent.summaryModel || t("systemSettings.runtimeResources.notConfigured"))}
          ${renderRuntimeKeyValue(t("systemSettings.runtimeResources.defaultPermission"), agent.defaultPermissionMode || "acceptEdits")}
          ${renderRuntimeKeyValue(t("systemSettings.runtimeResources.currentPermissionCap"), security.maxPermissionMode || "bypassPermissions")}
          ${renderRuntimeKeyValue(t("systemSettings.runtimeResources.defaultPlanMode"), agent.defaultStartInPlanMode ? t("systemSettings.runtimeResources.enabled") : t("systemSettings.runtimeResources.disabled"))}
        </div>
      </section>
      <section class="settings-info-card settings-card settings-card-content">
        <div class="settings-info-title">${escapeHtml(t("systemSettings.runtimeResources.runLimits"))}</div>
        <div class="runtime-kv-list settings-data-list">
          ${renderRuntimeKeyValue(t("systemSettings.runtimeResources.maxTurns"), formatNumber(agent.maxTurns || 0))}
          ${renderRuntimeKeyValue(t("systemSettings.runtimeResources.firstTokenTimeout"), formatDuration(agent.firstTokenTimeoutMs || 0))}
          ${renderRuntimeKeyValue(t("systemSettings.runtimeResources.transientRetries"), formatNumber(agent.maxTransientRetries || 0))}
          ${renderRuntimeKeyValue(t("systemSettings.runtimeResources.sampleTime"), formatTimestamp(summary.generatedAt))}
        </div>
      </section>
      ${renderBackgroundTaskSettingsCard(summary.backgroundTasks)}
      ${renderExecutionBudgetCard(agent.continuation || {})}
      ${renderRetryPolicyCard(agent.nonRetryableErrorPatterns)}
    </div>
  `;
  }

  // Two groups: plain concurrency limits, then the nesting switch that owns the
  // depth field and its cost warning. Depth and warning stay in the DOM but are
  // hidden while nesting is off — a disabled-but-visible control reads as broken,
  // and keeping the input mounted means turning nesting off and saving cannot
  // silently reset a depth the user already chose.
  function renderBackgroundTaskSettingsCard(backgroundTasks) {
    const settings = normalizedBackgroundTaskSettings(backgroundTasks);
    const nested = settings.allowNestedSubagents;
    const depthField = backgroundTaskSettingFields.find((field) => field.key === "maxSubagentDepth");
    return `
      <section class="settings-info-card settings-card settings-card-content background-task-settings-card">
        <div class="settings-info-title">${escapeHtml(t("systemSettings.runtimeResources.backgroundTaskSettings.title"))}</div>
        <p class="settings-card-description" data-settings-help-copy>${escapeHtml(t("systemSettings.runtimeResources.backgroundTaskSettings.description"))}</p>
        <div class="background-task-concurrency-grid settings-form-grid">
          ${backgroundTaskSettingFields.filter((field) => field.key !== "maxSubagentDepth").map((field) => renderBackgroundTaskNumberField(field, settings[field.key])).join("")}
        </div>
        <div class="background-task-nested-group${nested ? " is-on" : ""}" data-background-task-nested-group>
          <label class="settings-switch-row" for="runtimeAllowNestedSubagents">
            <span>
              <strong>${escapeHtml(t("systemSettings.runtimeResources.backgroundTaskSettings.allowNestedSubagents"))}</strong>
              <small data-settings-help-copy>${escapeHtml(t("systemSettings.runtimeResources.backgroundTaskSettings.allowNestedSubagentsHint"))}</small>
            </span>
            <input id="runtimeAllowNestedSubagents" type="checkbox" data-background-task-field="allowNestedSubagents" ${nested ? "checked" : ""} />
          </label>
          <div class="background-task-nested-detail" data-background-task-nested-detail ${nested ? "" : "hidden"}>
            ${renderBackgroundTaskNumberField(depthField, settings.maxSubagentDepth)}
            <p class="skill-security-note" data-settings-help-copy>${escapeHtml(t("systemSettings.runtimeResources.backgroundTaskSettings.nestedWarning"))}</p>
          </div>
        </div>
        <div class="settings-action-row settings-form-actions settings-card-footer settings-inline-actions">
          <button id="saveBackgroundTaskSettingsBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("systemSettings.runtimeResources.backgroundTaskSettings.save"))}</button>
        </div>
      </section>`;
  }

  function renderBackgroundTaskNumberField(field, value, disabled = false) {
    return `
          <label class="settings-form-field" for="${escapeAttr(field.id)}">
            ${escapeHtml(t(`systemSettings.runtimeResources.backgroundTaskSettings.${field.key}`))}
            <input id="${escapeAttr(field.id)}" class="settings-field" type="number" inputmode="numeric"
              data-background-task-field="${escapeAttr(field.key)}" min="${escapeAttr(String(field.min))}" max="${escapeAttr(String(field.max))}" step="1"
              value="${escapeAttr(String(value))}" ${disabled ? "disabled" : ""} />
            <small>${escapeHtml(t("systemSettings.runtimeResources.backgroundTaskSettings.range", { min: field.min, max: field.max }))}</small>
          </label>`;
  }

  function normalizedBackgroundTaskSettings(backgroundTasks = {}) {
    const values = backgroundTasks && typeof backgroundTasks === "object" ? backgroundTasks : {};
    return {
      workerCount: normalizedBackgroundTaskInteger(values.workerCount, defaultBackgroundTaskSettings.workerCount, 1, 16),
      perAgentLimit: normalizedBackgroundTaskInteger(values.perAgentLimit, defaultBackgroundTaskSettings.perAgentLimit, 1, 8),
      allowNestedSubagents: values.allowNestedSubagents === true,
      maxSubagentDepth: normalizedBackgroundTaskInteger(values.maxSubagentDepth, defaultBackgroundTaskSettings.maxSubagentDepth, 2, 4),
    };
  }

  function normalizedBackgroundTaskInteger(value, fallback, min, max) {
    const number = Number(value);
    if (!Number.isFinite(number)) return fallback;
    return Math.min(max, Math.max(min, Math.round(number)));
  }

  // Budgets persist as -1 for "no ceiling". The checkbox owns that state so the
  // number input never has to represent a sentinel the user could mistype.
  function renderExecutionBudgetCard(continuation) {
    const mode = String(continuation.mode || "off").toLowerCase() === "safe" ? "safe" : "off";
    // Raw, not coerced: -1 has to stay -1 so the total-turns floor knows the
    // segment cap is unlimited and imposes no minimum.
    const segmentTurns = Number(continuation?.segmentTurns);
    return `
      <section class="settings-info-card settings-card settings-card-content execution-budget-card">
        <div class="settings-info-title">${escapeHtml(t("systemSettings.runtimeResources.executionBudget"))}</div>
        <label class="execution-budget-mode" for="runtimeBudgetMode">
          <span class="execution-budget-mode-text">
            <strong>${escapeHtml(t("systemSettings.runtimeResources.autoContinuation"))}</strong>
            <small>${escapeHtml(t("systemSettings.runtimeResources.autoContinuationHint"))}</small>
          </span>
          <select id="runtimeBudgetMode" class="settings-field">
            <option value="off" ${mode === "off" ? "selected" : ""}>${escapeHtml(t("systemSettings.runtimeResources.continuationOff"))}</option>
            <option value="safe" ${mode === "safe" ? "selected" : ""}>${escapeHtml(t("systemSettings.runtimeResources.continuationSafe"))}</option>
          </select>
        </label>
        <div class="execution-budget-list">
          ${executionBudgetFields.map((field) => renderExecutionBudgetField(field, continuation[field.key], segmentTurns)).join("")}
        </div>
        <div class="settings-action-row execution-budget-actions">
          <p data-settings-help-copy>${escapeHtml(t("systemSettings.runtimeResources.executionBudgetHelp"))}</p>
          <button id="saveExecutionBudgetBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("systemSettings.runtimeResources.saveBudget"))}</button>
        </div>
      </section>`;
  }

  function renderExecutionBudgetField(field, rawValue, segmentTurns) {
    const value = Number(rawValue);
    const limited = Number.isFinite(value) && value >= 0;
    const uiValue = limited ? value / (field.scale || 1) : (executionBudgetDrafts[field.key] ?? field.fallback);
    if (limited) executionBudgetDrafts[field.key] = uiValue;
    const min = executionBudgetFieldMin(field, segmentTurns);
    return `
          <div class="execution-budget-row${limited ? "" : " is-unlimited"}" data-budget-row="${escapeAttr(field.key)}">
            <div class="execution-budget-row-head">
              <label class="execution-budget-label" for="${escapeAttr(field.id)}">${escapeHtml(t(`systemSettings.runtimeResources.${field.labelKey}`))}</label>
              <label class="execution-budget-toggle" for="${escapeAttr(field.id)}Unlimited">
                <input id="${escapeAttr(field.id)}Unlimited" type="checkbox" data-budget-field="${escapeAttr(field.key)}" ${limited ? "" : "checked"} />
                <span>${escapeHtml(t("systemSettings.runtimeResources.unlimited"))}</span>
              </label>
            </div>
            <div class="execution-budget-control">
              <input id="${escapeAttr(field.id)}" class="settings-field" type="number" inputmode="numeric"
                min="${escapeAttr(String(min))}" max="${escapeAttr(String(field.max))}" step="1"
                value="${escapeAttr(String(uiValue))}" ${limited ? "" : "disabled"} />
              <span class="execution-budget-unit">${escapeHtml(t(`systemSettings.runtimeResources.${field.unitKey}`))}</span>
            </div>
            <small class="execution-budget-range">${escapeHtml(t("systemSettings.runtimeResources.budgetRange", {
              min: formatNumber(min),
              max: formatNumber(field.max),
            }))}</small>
          </div>`;
  }

  function executionBudgetSegmentTurns(continuation) {
    const value = Number(continuation?.segmentTurns);
    return Number.isFinite(value) && value >= 1 ? Math.round(value) : defaultContinuationSegmentTurns;
  }

  // What the segment-turns control is about to send. Unlimited means the total
  // turns field has no segment floor to respect.
  function executionBudgetSubmittedSegmentTurns() {
    const toggle = $("runtimeBudgetSegmentTurnsUnlimited");
    if (!toggle || toggle.checked) return -1;
    const raw = Number($("runtimeBudgetSegmentTurns")?.value);
    return Number.isFinite(raw) && raw >= 1 && raw <= 1000 ? Math.round(raw) : defaultContinuationSegmentTurns;
  }

  // The server rejects maxTotalTurns below segmentTurns, so the input must not
  // advertise a floor the save call would refuse.
  function executionBudgetFieldMin(field, segmentTurns) {
    if (!field.minFollowsSegmentTurns) return field.min;
    // An unlimited segment cap imposes no floor on the total.
    if (!Number.isFinite(segmentTurns) || segmentTurns < 1) return field.min;
    return Math.min(Math.max(field.min, Math.round(segmentTurns)), field.max);
  }

  function renderRuntimeKeyValue(label, value) {
    return `
    <div class="runtime-kv-row settings-data-row">
      <span>${escapeHtml(label)}</span>
      <strong class="settings-data-value">${escapeHtml(String(value ?? ""))}</strong>
    </div>
  `;
  }

  function bindRuntimeSettingsActions() {
    $("refreshRuntimeSummaryBtn")?.addEventListener("click", () => loadRuntimeSummary({ notify: true }).catch(showError));
    bindBackgroundTaskSettingsActions();
    bindExecutionBudgetActions();
    bindRetryPolicyActions();
    if (!state.runtimeSummary && !state.runtimeError) {
      loadRuntimeSummary().catch(showError);
    }
  }

  function bindBackgroundTaskSettingsActions() {
    const nestedToggle = $("runtimeAllowNestedSubagents");
    // Reveal in place rather than re-rendering the panel: a re-render would throw
    // away unsaved edits in the concurrency inputs and the execution budget card
    // that shares this page.
    nestedToggle?.addEventListener("change", () => {
      const enabled = Boolean(nestedToggle.checked);
      const group = nestedToggle.closest?.("[data-background-task-nested-group]");
      group?.classList?.toggle("is-on", enabled);
      const detail = group?.querySelector?.("[data-background-task-nested-detail]");
      if (detail) detail.hidden = !enabled;
    });
    $("saveBackgroundTaskSettingsBtn")?.addEventListener("click", (event) => (
      saveBackgroundTaskSettings(event.currentTarget).catch(showError)
    ));
  }

  function bindExecutionBudgetActions() {
    for (const field of executionBudgetFields) {
      const input = $(field.id);
      const toggle = $(`${field.id}Unlimited`);
      // Read live: turning the segment cap unlimited must relax the total-turns
      // floor straight away, without waiting for a save and re-render.
      const min = executionBudgetFieldMin(field, executionBudgetSubmittedSegmentTurns());
      input?.addEventListener("change", () => {
        const value = Number(input.value);
        if (Number.isFinite(value) && value >= 0) executionBudgetDrafts[field.key] = value;
      });
      toggle?.addEventListener("change", () => {
        if (!input) return;
        setExecutionBudgetRowUnlimited(toggle, toggle.checked);
        if (toggle.checked) {
          input.disabled = true;
          return;
        }
        input.disabled = false;
        // Never leave an empty or invalid box behind: an unchecked box submits a
        // real number, so seed a usable value rather than failing validation.
        const current = Number(input.value);
        if (!Number.isFinite(current) || current < min || current > field.max) {
          const remembered = Number(executionBudgetDrafts[field.key]);
          const seed = Number.isFinite(remembered) && remembered >= min && remembered <= field.max
            ? remembered
            : Math.max(min, field.fallback);
          input.value = String(seed);
        }
        input.focus?.();
      });
    }
    $("saveExecutionBudgetBtn")?.addEventListener("click", (event) => {
      saveExecutionBudget(event.currentTarget).catch(showError);
    });
  }

  function setExecutionBudgetRowUnlimited(toggle, unlimited) {
    toggle?.closest?.("[data-budget-row]")?.classList?.toggle("is-unlimited", Boolean(unlimited));
  }

  // Provider errors the runner must treat as permanent. The built-in rule already
  // refuses plain 4xx, so this list is for upstreams that answer 200 or 500 and
  // put the real reason only in the body.
  function renderRetryPolicyCard(patterns) {
    const rows = retryPolicyDraft ?? normalizedRetryPolicyPatterns(patterns);
    retryPolicyDraft = rows;
    return `
      <section class="settings-info-card settings-card settings-card-content retry-policy-card">
        <div class="settings-info-title">${escapeHtml(t("systemSettings.runtimeResources.retryPolicy.title"))}</div>
        <p class="settings-card-description" data-settings-help-copy>${escapeHtml(t("systemSettings.runtimeResources.retryPolicy.description"))}</p>
        <ul class="retry-policy-list" data-retry-policy-list>
          ${rows.length
            ? rows.map((pattern, index) => renderRetryPolicyRow(pattern, index)).join("")
            : `<li class="retry-policy-empty">${escapeHtml(t("systemSettings.runtimeResources.retryPolicy.empty"))}</li>`}
        </ul>
        <div class="retry-policy-add">
          <label class="settings-form-field" for="retryPolicyPatternInput">
            ${escapeHtml(t("systemSettings.runtimeResources.retryPolicy.addLabel"))}
            <input id="retryPolicyPatternInput" class="settings-field" type="text"
              maxlength="${escapeAttr(String(retryPolicyPatternMaxLength))}"
              placeholder="${escapeAttr(t("systemSettings.runtimeResources.retryPolicy.placeholder"))}" />
            <small>${escapeHtml(t("systemSettings.runtimeResources.retryPolicy.range", {
              min: retryPolicyPatternMinLength,
              max: retryPolicyPatternMaxLength,
              count: retryPolicyMaxPatterns,
            }))}</small>
          </label>
          <button id="addRetryPolicyPatternBtn" class="settings-action-btn" type="button">${escapeHtml(t("systemSettings.runtimeResources.retryPolicy.add"))}</button>
        </div>
        <div class="settings-action-row settings-form-actions settings-card-footer settings-inline-actions">
          <p data-settings-help-copy>${escapeHtml(t("systemSettings.runtimeResources.retryPolicy.help"))}</p>
          <button id="saveRetryPolicyBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("systemSettings.runtimeResources.retryPolicy.save"))}</button>
        </div>
      </section>`;
  }

  function renderRetryPolicyRow(pattern, index) {
    return `
          <li class="retry-policy-row">
            <code class="retry-policy-pattern">${escapeHtml(pattern)}</code>
            <button type="button" class="retry-policy-remove" data-retry-policy-remove="${escapeAttr(String(index))}"
              title="${escapeAttr(t("systemSettings.runtimeResources.retryPolicy.remove"))}"
              aria-label="${escapeAttr(t("systemSettings.runtimeResources.retryPolicy.removeNamed", { pattern }))}">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="m6 6 12 12"></path><path d="m18 6-12 12"></path></svg>
            </button>
          </li>`;
  }

  // Mirrors NormalizeNonRetryableErrorPatterns in internal/config: collapse
  // whitespace, fold case, drop duplicates and anything outside the length
  // bounds. Matching the server here means the list shown is the list stored.
  function normalizedRetryPolicyPatterns(patterns) {
    if (!Array.isArray(patterns)) return [];
    const seen = new Set();
    const rows = [];
    for (const raw of patterns) {
      const candidate = String(raw ?? "").trim().replace(/\s+/g, " ").toLowerCase();
      if (candidate.length < retryPolicyPatternMinLength || candidate.length > retryPolicyPatternMaxLength) continue;
      if (seen.has(candidate)) continue;
      seen.add(candidate);
      rows.push(candidate);
      if (rows.length === retryPolicyMaxPatterns) break;
    }
    return rows;
  }

  function bindRetryPolicyActions() {
    bindRetryPolicyRemoveButtons();
    const input = $("retryPolicyPatternInput");
    $("addRetryPolicyPatternBtn")?.addEventListener("click", () => addRetryPolicyPattern(input));
    // Enter is the natural way to add another entry, and without this it submits
    // nothing and looks broken.
    input?.addEventListener("keydown", (event) => {
      if (event.key !== "Enter") return;
      event.preventDefault();
      addRetryPolicyPattern(input);
    });
    $("saveRetryPolicyBtn")?.addEventListener("click", (event) => {
      saveRetryPolicy(event.currentTarget).catch(showError);
    });
  }

  function addRetryPolicyPattern(input) {
    const candidate = String(input?.value ?? "").trim().replace(/\s+/g, " ").toLowerCase();
    if (!candidate) return;
    // Rejected reasons are reported rather than silently swallowed: a pattern
    // that vanishes on add reads as a bug.
    if (candidate.length < retryPolicyPatternMinLength) {
      showToast?.(t("systemSettings.runtimeResources.retryPolicy.tooShort", { min: retryPolicyPatternMinLength }), "warn", { force: true });
      return;
    }
    if (retryPolicyDraft.length >= retryPolicyMaxPatterns) {
      showToast?.(t("systemSettings.runtimeResources.retryPolicy.tooMany", { count: retryPolicyMaxPatterns }), "warn", { force: true });
      return;
    }
    if (retryPolicyDraft.includes(candidate)) {
      showToast?.(t("systemSettings.runtimeResources.retryPolicy.duplicate"), "warn", { force: true });
      return;
    }
    retryPolicyDraft.push(candidate);
    if (input) input.value = "";
    refreshRetryPolicyList();
    input?.focus?.();
  }

  // Repaints just the list and rebinds it, so adding a pattern cannot discard
  // unsaved edits in the budget or background-task cards on the same page.
  function refreshRetryPolicyList() {
    const list = document.querySelector?.("[data-retry-policy-list]");
    if (!list) return;
    list.innerHTML = retryPolicyDraft.length
      ? retryPolicyDraft.map((pattern, index) => renderRetryPolicyRow(pattern, index)).join("")
      : `<li class="retry-policy-empty">${escapeHtml(t("systemSettings.runtimeResources.retryPolicy.empty"))}</li>`;
    bindRetryPolicyRemoveButtons();
  }

  function bindRetryPolicyRemoveButtons() {
    const list = document.querySelector?.("[data-retry-policy-list]");
    list?.querySelectorAll("[data-retry-policy-remove]").forEach((button) => {
      button.addEventListener("click", () => {
        const index = Number(button.dataset.retryPolicyRemove);
        if (!Array.isArray(retryPolicyDraft) || !Number.isInteger(index) || index < 0 || index >= retryPolicyDraft.length) return;
        retryPolicyDraft.splice(index, 1);
        refreshRetryPolicyList();
      });
    });
  }

  async function saveRetryPolicy(button) {
    setButtonBusy(button, true);
    try {
      await api("/api/runtime/retry-policy-settings", {
        method: "PATCH",
        body: JSON.stringify({ nonRetryableErrorPatterns: retryPolicyDraft }),
      });
      showToast?.(t("systemSettings.runtimeResources.retryPolicy.saved"), "success", { force: true });
      // Drop the draft so the reload shows what the server actually stored.
      retryPolicyDraft = null;
      await loadRuntimeSummary();
    } finally {
      setButtonBusy(button, false);
    }
  }

  function collectBackgroundTaskSettings() {
    return {
      workerCount: readBackgroundTaskInteger("runtimeBackgroundWorkerCount", defaultBackgroundTaskSettings.workerCount, 1, 16),
      perAgentLimit: readBackgroundTaskInteger("runtimeBackgroundPerAgentLimit", defaultBackgroundTaskSettings.perAgentLimit, 1, 8),
      allowNestedSubagents: Boolean($("runtimeAllowNestedSubagents")?.checked),
      maxSubagentDepth: readBackgroundTaskInteger("runtimeMaxSubagentDepth", defaultBackgroundTaskSettings.maxSubagentDepth, 2, 4),
    };
  }

  function readBackgroundTaskInteger(id, fallback, min, max) {
    return normalizedBackgroundTaskInteger($(id)?.value, fallback, min, max);
  }

  async function saveBackgroundTaskSettings(button) {
    const payload = collectBackgroundTaskSettings();
    setButtonBusy(button, true, t("systemSettings.runtimeResources.backgroundTaskSettings.saving"));
    try {
      await api("/api/runtime/background-task-settings", { method: "PATCH", body: JSON.stringify(payload) });
      showToast?.(t("systemSettings.runtimeResources.backgroundTaskSettings.saved"), "success", { force: true });
      await loadRuntimeSummary();
    } finally {
      setButtonBusy(button, false);
    }
  }

  function collectExecutionBudget() {
    const payload = {
      mode: $("runtimeBudgetMode")?.value === "safe" ? "safe" : "off",
    };
    // segmentTurns is a normal field now, so read the control the user sees
    // rather than replaying whatever was persisted. The maxTotalTurns floor
    // follows the value being submitted, not the stored one.
    const segmentTurns = executionBudgetSubmittedSegmentTurns();
    for (const field of executionBudgetFields) {
      const toggle = $(`${field.id}Unlimited`);
      if (!toggle || toggle.checked) {
        payload[field.key] = -1;
        continue;
      }
      const min = executionBudgetFieldMin(field, segmentTurns);
      const raw = Number($(field.id)?.value);
      const uiValue = Number.isFinite(raw) && raw >= min && raw <= field.max ? raw : Math.max(min, field.fallback);
      executionBudgetDrafts[field.key] = uiValue;
      payload[field.key] = Math.round(uiValue * (field.scale || 1));
    }
    return payload;
  }

  async function saveExecutionBudget(button) {
    const payload = collectExecutionBudget();
    setButtonBusy(button, true);
    try {
      await api("/api/runtime/continuation-settings", { method: "PATCH", body: JSON.stringify(payload) });
      showToast?.(t("systemSettings.runtimeResources.budgetSaved"), "success", { force: true });
      await loadRuntimeSummary();
    } finally {
      setButtonBusy(button, false);
    }
  }

  function formatUptime(seconds) {
    const value = Number(seconds || 0);
    if (!Number.isFinite(value) || value <= 0) return t("systemSettings.serverSystem.uptimeSeconds", { seconds: 0 });
    if (value < 60) return t("systemSettings.serverSystem.uptimeSeconds", { seconds: Math.round(value) });
    if (value < 3600) return t("systemSettings.serverSystem.uptimeMinutes", {
      minutes: Math.floor(value / 60),
      seconds: Math.round(value % 60),
    });
    return t("systemSettings.serverSystem.uptimeHours", {
      hours: Math.floor(value / 3600),
      minutes: Math.floor((value % 3600) / 60),
    });
  }
  function aboutUpdatePresentation() {
    const status = state.updateStatus;
    const currentVersion = status?.currentVersion || state.settings?.version || "0.1.0-dev";
    if (state.updateError) {
      return { currentVersion, latestVersion: "—", label: t("systemSettings.about.checkFailed"), tone: "error" };
    }
    if (!status) {
      return { currentVersion, latestVersion: "—", label: t("systemSettings.about.notChecked"), tone: "idle" };
    }
    if (status.status === "update_available") {
      return { currentVersion, latestVersion: status.targetVersion || "—", label: t("systemSettings.about.updateAvailable"), tone: "available" };
    }
    if (status.status === "up_to_date") {
      return { currentVersion, latestVersion: status.targetVersion || currentVersion, label: t("systemSettings.about.upToDate"), tone: "current" };
    }
    if (status.status === "development_build") {
      return { currentVersion, latestVersion: "—", label: t("systemSettings.about.developmentBuild"), tone: "idle" };
    }
    return { currentVersion, latestVersion: status.targetVersion || "—", label: t("systemSettings.about.unavailable"), tone: "idle" };
  }

  function renderAboutSettingsContent() {
    const summary = state.licenseSummary;
    const modules = Array.isArray(summary?.modules) ? summary.modules : [];
    const directCount = modules.filter((module) => module.relation === "direct").length;
    const unknownCount = modules.filter((module) => !module.license || module.license === "unknown").length;
    const update = aboutUpdatePresentation();
    return `
    <div class="settings-live-page about-page legacy-about-page">
      <section class="legacy-about-overview" aria-labelledby="legacyAboutProductName">
        <section class="legacy-about-brand-card settings-page-section settings-card settings-card-content">
        <div class="legacy-about-brand">
          <span class="legacy-about-logo" aria-hidden="true">
            <img src="/ui/autoto-logo.svg?v=about-brand-license-1" alt="" />
          </span>
          <div>
            <h2 id="legacyAboutProductName">Autoto</h2>
            <p data-settings-help-copy>${escapeHtml(t("systemSettings.about.productTagline"))}</p>
          </div>
        </div>
        </section>
        <section class="legacy-about-update-card settings-page-section settings-card" aria-label="${escapeHtml(t("systemSettings.about.versionInfo"))}">
        <div class="legacy-about-version-table settings-data-list" aria-label="${escapeHtml(t("systemSettings.about.versionInfo"))}">
          <div class="legacy-about-version-row">
            <span>${escapeHtml(t("systemSettings.about.currentVersion"))}</span>
            <strong>${escapeHtml(update.currentVersion)}</strong>
          </div>
          <div class="legacy-about-version-row">
            <span>${escapeHtml(t("systemSettings.about.latestVersion"))}</span>
            <strong>${escapeHtml(update.latestVersion)}</strong>
          </div>
          <div class="legacy-about-version-row">
            <span>${escapeHtml(t("systemSettings.about.updateStatus"))}</span>
            <strong class="legacy-about-update-state settings-badge ${escapeHtml(update.tone)}">${escapeHtml(update.label)}</strong>
          </div>
        </div>
        <button id="checkForUpdatesBtn" class="legacy-about-update-button" type="button">${escapeHtml(t("systemSettings.about.checkUpdates"))}</button>
        <p class="legacy-about-update-note" data-settings-help-copy>${escapeHtml(t("systemSettings.about.updateNote"))}</p>
        ${state.updateError ? `<div class="settings-inline-alert settings-alert legacy-about-update-error" role="alert">${escapeHtml(state.updateError)}</div>` : ""}
        </section>
      </section>
      <details class="legacy-about-more">
        <summary>${escapeHtml(t("systemSettings.about.advanced"))}</summary>
        <div class="legacy-about-more-content">
          ${isDesktopShell() ? renderDesktopShellAboutExtras() : ""}
          ${renderLocalPreferencesBackupSection()}
          <section class="settings-provider-section legacy-about-license-section settings-page-section settings-card">
            <div class="settings-provider-section-head settings-card-header">
              <div>
                <div class="settings-provider-title settings-card-title">${escapeHtml(t("systemSettings.license.openSourceTitle"))}</div>
                <div class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(t("systemSettings.license.openSourceMeta"))}</div>
              </div>
              <button id="refreshLicensesBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("systemSettings.license.refresh"))}</button>
            </div>
            <div class="legacy-about-license-metrics settings-card-content" aria-label="${escapeHtml(t("systemSettings.license.openSourceTitle"))}">
              <div><strong>${escapeHtml(formatNumber(modules.length))}</strong><span>${escapeHtml(t("systemSettings.license.modules"))}</span></div>
              <div><strong>${escapeHtml(formatNumber(directCount))}</strong><span>${escapeHtml(t("systemSettings.license.direct"))}</span></div>
              <div class="${unknownCount ? "warn" : ""}"><strong>${escapeHtml(formatNumber(unknownCount))}</strong><span>${escapeHtml(t("systemSettings.license.unknownCount"))}</span></div>
            </div>
            ${state.licenseError ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(state.licenseError)}</div>` : ""}
            ${summary ? renderLicenseSummary(summary) : `<div class="settings-empty-card settings-empty-state">${escapeHtml(t("systemSettings.license.loading"))}</div>`}
          </section>
        </div>
      </details>
    </div>
  `;
  }

  function renderLocalPreferencesBackupSection() {
    const summary = localPreferencesBackupSummary();
    const labels = summary.labels.length ? summary.labels : [t("systemSettings.localBackup.emptyLabels")];
    return `
    <section class="settings-provider-section settings-backup-section settings-page-section settings-card">
      <div class="settings-provider-section-head settings-card-header">
        <div>
          <div class="settings-provider-title settings-card-title">${escapeHtml(t("systemSettings.localBackup.title"))}</div>
          <div class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(t("systemSettings.localBackup.meta"))}</div>
        </div>
        <div class="settings-action-row compact-actions">
          <button id="copyLocalPrefsBackupBtn" class="settings-action-btn subtle" type="button">${escapeHtml(t("systemSettings.localBackup.copy"))}</button>
          <button id="downloadLocalPrefsBackupBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("systemSettings.localBackup.download"))}</button>
        </div>
      </div>
      <div class="settings-backup-stats settings-stat-grid settings-card-content">
        <div class="settings-stat-card"><strong>${escapeHtml(formatNumber(summary.count))}</strong><span>${escapeHtml(t("systemSettings.localBackup.savedCount"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(formatBytes(summary.bytes))}</strong><span>${escapeHtml(t("systemSettings.localBackup.estimatedSize"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(String(localPreferenceBackupVersion))}</strong><span>${escapeHtml(t("systemSettings.localBackup.formatVersion"))}</span></div>
      </div>
      <div class="settings-backup-key-list settings-data-list">
        ${labels.map((label) => `<span>${escapeHtml(label)}</span>`).join("")}
      </div>
      <div class="settings-inline-success settings-alert" role="status">${escapeHtml(t("systemSettings.localBackup.safetyNote"))}</div>
      <textarea id="localPrefsImportText" class="settings-token-input settings-backup-import" placeholder="${escapeHtml(t("systemSettings.localBackup.importPlaceholder"))}"></textarea>
      <div class="settings-action-row settings-form-actions">
        <button id="clearLocalPrefsImportBtn" class="settings-action-btn subtle" type="button">${escapeHtml(t("systemSettings.localBackup.clearInput"))}</button>
        <button id="importLocalPrefsBackupBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("systemSettings.localBackup.import"))}</button>
      </div>
    </section>
  `;
  }

  function renderLicenseSummary(summary) {
    const modules = Array.isArray(summary.modules) ? summary.modules : [];
    const groups = groupLicenseModules(modules);
    const initiallyOpenLicense = groups.find(([license]) => license === "unknown")?.[0] || groups[0]?.[0] || "";
    return `
      <div class="legacy-about-license-note" role="note">
        <strong>${escapeHtml(t("systemSettings.license.complianceTitle"))}</strong>
        <span>${escapeHtml(t("systemSettings.license.defaultNotice"))}</span>
      </div>
      <div class="license-accordion-list settings-card-content">
        ${groups.length ? groups.map(([license, items]) => renderLicenseGroup(license, items, license === initiallyOpenLicense)).join("") : `<div class="settings-empty-card compact">${escapeHtml(t("systemSettings.license.empty"))}</div>`}
      </div>
    `;
  }

  function groupLicenseModules(modules) {
    const grouped = modules.reduce((acc, module) => {
      const license = String(module.license || "unknown").trim() || "unknown";
      acc[license] = acc[license] || [];
      acc[license].push(module);
      return acc;
    }, {});
    return Object.entries(grouped).sort(([left], [right]) => {
      if (left === "unknown") return -1;
      if (right === "unknown") return 1;
      return left.localeCompare(right);
    });
  }

  function renderLicenseGroup(license, items, open) {
    const sortedItems = [...items].sort((left, right) => {
      const leftDirect = left.relation === "direct" ? 0 : 1;
      const rightDirect = right.relation === "direct" ? 0 : 1;
      return leftDirect - rightDirect || String(left.path || "").localeCompare(String(right.path || ""));
    });
    const directCount = sortedItems.filter((module) => module.relation === "direct").length;
    const label = license === "unknown" ? t("systemSettings.license.unknownCount") : license;
    return `
      <details class="license-accordion ${license === "unknown" ? "warn" : ""}" ${open ? "open" : ""}>
        <summary>
          <span class="license-accordion-copy">
            <strong>${escapeHtml(label)}</strong>
            <small>${escapeHtml(`${formatNumber(sortedItems.length)} ${t("systemSettings.license.modules")} · ${formatNumber(directCount)} ${t("systemSettings.license.direct")}`)}</small>
          </span>
          <span class="license-accordion-count">${escapeHtml(formatNumber(sortedItems.length))}</span>
        </summary>
        <div class="license-module-list">
          ${sortedItems.map(renderLicenseModule).join("")}
        </div>
      </details>
    `;
  }

  function renderLicenseModule(module) {
    const direct = module.relation === "direct";
    return `
      <div class="license-module-row">
        <div class="license-module-main">
          <div class="license-module-name">${escapeHtml(module.path || t("systemSettings.license.pathUnknown"))}</div>
          <div class="license-module-meta">${escapeHtml(module.version || t("systemSettings.license.versionUnknown"))}</div>
        </div>
        <span class="license-relation-badge ${direct ? "direct" : "indirect"}">${escapeHtml(t(direct ? "systemSettings.license.direct" : "systemSettings.license.indirect"))}</span>
      </div>
    `;
  }

  function downloadLocalPreferencesBackup() {
    const text = localPreferencesBackupText();
    const blob = new Blob([text], { type: "application/json;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `autoto-local-preferences-${new Date().toISOString().slice(0, 10)}.json`;
    document.body.appendChild(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    showToast(t("systemSettings.localBackup.downloaded"), "success", { force: true });
    notifyTerminal?.(`[info] ${t("systemSettings.localBackup.downloaded")}\n`);
  }

  async function importLocalPreferencesBackupFromPanel() {
    const textarea = $("localPrefsImportText");
    const button = $("importLocalPrefsBackupBtn");
    const text = textarea?.value.trim() || "";
    if (!text) throw new Error(t("systemSettings.localBackup.importRequired"));
    setButtonBusy(button, true, t("systemSettings.localBackup.importing"));
    if (textarea) textarea.disabled = true;
    try {
      const imported = await restoreLocalPreferencesBackup(text);
      if (textarea) textarea.value = "";
      refreshActiveSettingsPanel();
      const message = t("systemSettings.localBackup.imported", { count: imported });
      showToast(message, "success", { force: true });
      notifyTerminal?.(`[info] ${message}\n`);
    } finally {
      setButtonBusy(button, false, t("systemSettings.localBackup.importing"));
      if (textarea) textarea.disabled = false;
    }
  }

  function renderDesktopShellAboutExtras() {
    const auto = state.desktopAutostart || {};
    const pending = state.desktopPendingUpdate || {};
    const autoLabel = auto.enabled
      ? t("systemSettings.desktop.autostartOn")
      : t("systemSettings.desktop.autostartOff");
    const pendingLabel = pending.pending
      ? t("systemSettings.desktop.pendingVersion", { version: pending.version || "—" })
      : t("systemSettings.desktop.noPending");
    return `
        <section class="settings-provider-section legacy-about-desktop-shell settings-page-section settings-card" aria-label="${escapeHtml(t("systemSettings.desktop.title"))}">
          <div class="settings-provider-section-head settings-card-header">
            <div>
              <div class="settings-provider-title settings-card-title">${escapeHtml(t("systemSettings.desktop.title"))}</div>
            </div>
          </div>
          <div class="legacy-about-version-table settings-data-list">
            <div class="legacy-about-version-row">
              <span>${escapeHtml(t("systemSettings.desktop.loginItem"))}</span>
              <strong class="settings-badge ${auto.enabled ? "ok" : "idle"}">${escapeHtml(autoLabel)}</strong>
            </div>
            <div class="legacy-about-version-row">
              <span>${escapeHtml(t("systemSettings.desktop.stagedUpdate"))}</span>
              <strong>${escapeHtml(pendingLabel)}</strong>
            </div>
          </div>
          <div class="settings-action-row" style="margin-top:10px;gap:8px;flex-wrap:wrap">
            <button id="desktopAutostartEnableBtn" class="settings-action-btn subtle" type="button">${escapeHtml(t("systemSettings.desktop.enableAutostart"))}</button>
            <button id="desktopAutostartDisableBtn" class="settings-action-btn subtle" type="button">${escapeHtml(t("systemSettings.desktop.disableAutostart"))}</button>
            <button id="desktopRefreshShellStatusBtn" class="settings-action-btn subtle" type="button">${escapeHtml(t("systemSettings.desktop.refresh"))}</button>
          </div>
          <div class="settings-form-grid" style="margin-top:12px;gap:8px">
            <label class="settings-form-field">${escapeHtml(t("systemSettings.desktop.localBinaryPath"))}
              <input id="desktopStageSourcePath" class="settings-field" type="text" autocomplete="off" placeholder="/path/to/autoto-desktop" value="${escapeHtml(state.desktopStageDraft?.sourcePath || "")}" />
            </label>
            <label class="settings-form-field">${escapeHtml(t("systemSettings.desktop.version"))}
              <input id="desktopStageVersion" class="settings-field" type="text" autocomplete="off" placeholder="0.2.0" value="${escapeHtml(state.desktopStageDraft?.version || "")}" />
            </label>
            <label class="settings-form-field">${escapeHtml(t("systemSettings.desktop.sha256Optional"))}
              <input id="desktopStageSha256" class="settings-field" type="text" autocomplete="off" placeholder="64-char hex" value="${escapeHtml(state.desktopStageDraft?.sha256 || "")}" />
            </label>
          </div>
          <div class="settings-action-row" style="margin-top:10px;gap:8px;flex-wrap:wrap">
            <button id="desktopStageUpdateBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("systemSettings.desktop.stageLocal"))}</button>
            <button id="desktopClearPendingBtn" class="settings-action-btn subtle" type="button" ${pending.pending ? "" : "disabled"}>${escapeHtml(t("systemSettings.desktop.clearPending"))}</button>
          </div>
          <p class="legacy-about-update-note" data-settings-help-copy>${escapeHtml(t("systemSettings.desktop.stageNote"))}</p>
          ${state.desktopShellError ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(state.desktopShellError)}</div>` : ""}
        </section>`;
  }

  async function refreshDesktopShellStatus({ notify = false } = {}) {
    if (!isDesktopShell()) return;
    try {
      const [auto, pending] = await Promise.all([
        getAutostartStatus().catch((err) => {
          if (err?.status === 404) return { enabled: false, unavailable: true };
          throw err;
        }),
        getPendingDesktopUpdate().catch((err) => {
          if (err?.status === 404) return { pending: false, unavailable: true };
          throw err;
        }),
      ]);
      state.desktopAutostart = auto;
      state.desktopPendingUpdate = pending;
      state.desktopShellError = "";
      if (notify) showToast?.(t("systemSettings.desktop.refreshed"), "success");
    } catch (err) {
      state.desktopShellError = err.message || String(err);
      if (notify) showError?.(err);
    }
    if (state.activeSettingsPanel === "about") refreshActiveSettingsPanel?.();
  }

  function bindAboutSettingsActions() {
    $("checkForUpdatesBtn")?.addEventListener("click", () => loadUpdateStatus({ notify: true }).catch(showError));
    $("refreshLicensesBtn")?.addEventListener("click", () => loadLicenseSummary({ notify: true }).catch(showError));
    $("copyLocalPrefsBackupBtn")?.addEventListener("click", () => copyText(localPreferencesBackupText()));
    $("downloadLocalPrefsBackupBtn")?.addEventListener("click", downloadLocalPreferencesBackup);
    $("importLocalPrefsBackupBtn")?.addEventListener("click", () => importLocalPreferencesBackupFromPanel().catch(showError));
    $("clearLocalPrefsImportBtn")?.addEventListener("click", () => {
      const textarea = $("localPrefsImportText");
      if (textarea) textarea.value = "";
    });
    $("desktopAutostartEnableBtn")?.addEventListener("click", async (event) => {
      setButtonBusy(event.currentTarget, true);
      try {
        await enableAutostart();
        await refreshDesktopShellStatus();
        showToast?.(t("systemSettings.desktop.autostartEnabled"), "success");
      } catch (err) {
        showError?.(err);
      } finally {
        setButtonBusy(event.currentTarget, false);
      }
    });
    $("desktopAutostartDisableBtn")?.addEventListener("click", async (event) => {
      setButtonBusy(event.currentTarget, true);
      try {
        await disableAutostart();
        await refreshDesktopShellStatus();
        showToast?.(t("systemSettings.desktop.autostartDisabled"), "success");
      } catch (err) {
        showError?.(err);
      } finally {
        setButtonBusy(event.currentTarget, false);
      }
    });
    $("desktopRefreshShellStatusBtn")?.addEventListener("click", () => refreshDesktopShellStatus({ notify: true }).catch(showError));
    $("desktopStageUpdateBtn")?.addEventListener("click", async (event) => {
      const sourcePath = $("desktopStageSourcePath")?.value?.trim() || "";
      const version = $("desktopStageVersion")?.value?.trim() || "";
      const sha256 = $("desktopStageSha256")?.value?.trim() || "";
      state.desktopStageDraft = { sourcePath, version, sha256 };
      if (!sourcePath || !version) {
        showError?.(new Error(t("systemSettings.desktop.stageMissingFields") || "source path and version are required"));
        return;
      }
      if (!sha256 || !/^[0-9a-fA-F]{64}$/.test(sha256)) {
        showError?.(new Error(t("systemSettings.desktop.stageMissingSha") || "a 64-character SHA-256 hex digest is required"));
        return;
      }
      setButtonBusy(event.currentTarget, true, t("systemSettings.desktop.staging"));
      try {
        await stageDesktopUpdate({ sourcePath, version, sha256 });
        await refreshDesktopShellStatus();
        showToast?.(t("systemSettings.desktop.staged"), "success", { force: true });
      } catch (err) {
        showError?.(err);
      } finally {
        setButtonBusy(event.currentTarget, false);
      }
    });
    $("desktopClearPendingBtn")?.addEventListener("click", async (event) => {
      setButtonBusy(event.currentTarget, true);
      try {
        await clearPendingDesktopUpdate();
        await refreshDesktopShellStatus();
        showToast?.(t("systemSettings.desktop.pendingCleared"), "success");
      } catch (err) {
        showError?.(err);
      } finally {
        setButtonBusy(event.currentTarget, false);
      }
    });
    if (!state.licenseSummary && !state.licenseError) {
      loadLicenseSummary().catch(showError);
    }
    if (isDesktopShell() && !state.desktopAutostart && !state.desktopShellError) {
      refreshDesktopShellStatus().catch(() => {});
    }
  }

  function renderStorageSettingsContent() {
    const summary = state.storageSummary;
    const entries = Array.isArray(summary?.entries) ? summary.entries : [];
    const dbEntry = storageEntryByKey(entries, "database");
    const projectEntry = storageEntryByKey(entries, "projects");
    const generatedAt = summary?.generatedAt ? formatTimestamp(summary.generatedAt) : t("systemSettings.storage.notScanned");
    return `
    <div class="settings-live-page storage-page">
      <section class="settings-hero-card settings-page-section settings-card">
        <div>
          <div class="settings-hero-kicker">${escapeHtml(t("systemSettings.storage.kicker"))}</div>
          <div class="settings-hero-title">${escapeHtml(t("systemSettings.storage.heroTitle"))}</div>
          <p data-settings-help-copy>${escapeHtml(t("systemSettings.storage.description"))}</p>
        </div>
        <div class="settings-action-row settings-toolbar">
          <button id="refreshStorageSummaryBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("systemSettings.storage.refresh"))}</button>
        </div>
      </section>
      <div class="settings-status-strip settings-stat-grid">
        <div class="settings-stat-card"><strong>${escapeHtml(formatBytes(summary?.totalKnownBytes || 0))}</strong><span>${escapeHtml(t("systemSettings.storage.knownUsage"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(formatBytes(dbEntry?.sizeBytes || 0))}</strong><span>${escapeHtml(t("systemSettings.storage.databaseFile"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(generatedAt)}</strong><span>${escapeHtml(t("systemSettings.storage.scanTime"))}</span></div>
      </div>
      ${state.storageError ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(state.storageError)}</div>` : ""}
      ${summary ? renderStorageSummary(summary, projectEntry) : `<div class="settings-empty-card settings-empty-state">${escapeHtml(t("systemSettings.storage.loading"))}</div>`}
    </div>
  `;
  }

  function renderStorageSummary(summary, projectEntry) {
    const entries = Array.isArray(summary.entries) ? summary.entries : [];
    return `
    <div class="usage-summary-grid settings-stat-grid">
      ${renderUsageMetricCard(t("systemSettings.storage.scanLimit"), summary.scanLimit || 0, t("systemSettings.storage.scanLimitHint"))}
      ${renderUsageMetricCard(t("systemSettings.storage.projectFiles"), projectEntry?.fileCount || 0, `${formatBytes(projectEntry?.sizeBytes || 0)} · ${projectEntry?.truncated ? t("systemSettings.storage.truncated") : t("systemSettings.storage.fullScan")}`)}
      ${renderUsageMetricCard(t("systemSettings.storage.directoryCount"), entries.reduce((sum, entry) => sum + Number(entry.directoryCount || 0), 0), t("systemSettings.storage.acrossEntries"))}
      ${renderUsageMetricCard(t("systemSettings.storage.fileCount"), entries.reduce((sum, entry) => sum + Number(entry.fileCount || 0), 0), t("systemSettings.storage.acrossEntries"))}
    </div>
    <div class="storage-entry-list settings-data-list">
      ${entries.map(renderStorageEntry).join("")}
    </div>
  `;
  }

  function renderStorageEntry(entry) {
    const status = entry.error ? entry.error : (entry.exists ? (entry.truncated ? t("systemSettings.storage.statusPartial") : t("systemSettings.storage.statusScanned")) : t("systemSettings.storage.statusMissing"));
    return `
    <section class="storage-entry-card settings-card settings-data-row">
      <div class="settings-provider-section-head settings-card-header">
        <div>
          <div class="settings-provider-title settings-card-title">${escapeHtml(storageEntryLabel(entry))}</div>
          <div class="settings-provider-meta settings-card-description path settings-data-value">${escapeHtml(entry.path || t("systemSettings.storage.notConfigured"))}</div>
        </div>
        <span class="settings-status-pill settings-badge ${entry.error ? "warn" : (entry.exists ? "ok" : "muted")}">${escapeHtml(entry.exists ? (entry.truncated ? t("systemSettings.storage.pillPartial") : t("systemSettings.storage.pillExists")) : t("systemSettings.storage.pillMissing"))}</span>
      </div>
      <div class="storage-entry-grid settings-stat-grid settings-card-content">
        <div class="settings-stat-card"><strong>${escapeHtml(formatBytes(entry.sizeBytes || 0))}</strong><span>${escapeHtml(t("systemSettings.storage.size"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(formatNumber(entry.fileCount || 0))}</strong><span>${escapeHtml(t("systemSettings.storage.files"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(formatNumber(entry.directoryCount || 0))}</strong><span>${escapeHtml(t("systemSettings.storage.directories"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(formatNumber(entry.entriesScanned || 0))}</strong><span>${escapeHtml(t("systemSettings.storage.scannedEntries"))}</span></div>
      </div>
      <div class="settings-info-text">${escapeHtml(status)}</div>
    </section>
  `;
  }

  function storageEntryByKey(entries, key) {
    return (entries || []).find((entry) => entry.key === key) || null;
  }

  function storageEntryLabel(entry) {
    const labels = {
      home: t("systemSettings.storage.labelHome"),
      database: t("systemSettings.storage.labelDatabase"),
      config: t("systemSettings.storage.labelConfig"),
      projects: t("systemSettings.storage.labelProjects"),
    };
    return entry.label || labels[entry.key] || entry.key || t("systemSettings.storage.labelFallback");
  }

  function bindStorageSettingsActions() {
    $("refreshStorageSummaryBtn")?.addEventListener("click", () => loadStorageSummary({ notify: true }).catch(showError));
    if (!state.storageSummary && !state.storageError) {
      loadStorageSummary().catch(showError);
    }
  }

  // valueKind marks a value that is an identifier rather than a headline
  // number. "localhost:16888" needs 199px at the default 24px/900 and the card
  // is 141px wide, so it wrapped mid-number to "localhost:1688" + "8" -- a first
  // line that reads as a plausible, wrong port rather than as truncation.
  function renderUsageMetricCard(title, value, subtitle, valueKind = "") {
    const valueClass = valueKind === "identifier" ? "usage-metric-value is-identifier" : "usage-metric-value";
    return `
    <section class="usage-metric-card settings-stat-card">
      <div class="${valueClass}">${escapeHtml(formatMetricValue(value))}</div>
      <div class="usage-metric-title">${escapeHtml(title)}</div>
      <div class="usage-metric-subtitle">${escapeHtml(subtitle || "—")}</div>
    </section>
  `;
  }

  function formatMetricValue(value) {
    if (typeof value === "number") return formatNumber(value);
    if (typeof value === "bigint") return formatNumber(Number(value));
    if (value === null || value === undefined || value === "") return "0";
    return String(value);
  }

  return {
    bindAboutSettingsActions,
    bindRuntimeSettingsActions,
    bindStorageSettingsActions,
    renderAboutSettingsContent,
    renderRuntimeSettingsContent,
    renderServerSystemSettingsContent,
    renderStorageSettingsContent,
    renderUsageMetricCard,
  };
}
