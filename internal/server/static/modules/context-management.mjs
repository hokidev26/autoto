import { setTextIfChanged } from "./dom.mjs";

// Threshold defaults mirror the backend (config.defaultStandardContextWindow /
// defaultLargeContextWindow): token counts are rough estimates and the reply
// needs room too, so both actions fire well before the window is full.
export const defaultContextSettings = Object.freeze({
  retainTurns: 2,
  maxPrunePercent: 80,
  minPrunePercent: 30,
  standardPruneStart: 80,
  standardCompactStart: 90,
  largeWindowTokens: 600000,
  largePruneStart: 85,
  largeCompactStart: 92,
});

const contextToneColors = Object.freeze({
  normal: "#4b5563",
  warning: "#d97706",
  danger: "#dc2626",
  unknown: "#a8adb7",
});

function firstDefined(...values) {
  return values.find((value) => value !== undefined && value !== null && value !== "");
}

function finiteNumber(...values) {
  const value = firstDefined(...values);
  if (value === undefined) return null;
  const number = Number(value);
  return Number.isFinite(number) ? number : null;
}

function boundedNumber(value, minimum, maximum, fallback) {
  const number = Number(value);
  if (!Number.isFinite(number)) return fallback;
  return Math.min(maximum, Math.max(minimum, number));
}

function percentageValue(value, fallback) {
  const number = Number(value);
  if (!Number.isFinite(number)) return fallback;
  const percentage = number > 0 && number <= 1 ? number * 100 : number;
  return boundedNumber(percentage, 0, 100, fallback);
}

function booleanValue(...values) {
  const value = firstDefined(...values);
  if (value === undefined) return null;
  if (typeof value === "string") {
    const normalized = value.trim().toLowerCase();
    if (["true", "1", "yes", "on", "enabled"].includes(normalized)) return true;
    if (["false", "0", "no", "off", "disabled"].includes(normalized)) return false;
  }
  return Boolean(value);
}

export function normalizeContextSettings(value = {}) {
  const source = value?.contextManagement || value?.contextSettings || value?.settings || value || {};
  return {
    retainTurns: Math.round(boundedNumber(firstDefined(source.retainTurns, source.compactKeepTurns, source.keepTurns, source.preserveTurns), 1, 100, defaultContextSettings.retainTurns)),
    maxPrunePercent: percentageValue(firstDefined(source.maxPrunePercent, source.maximumPrunePercent), defaultContextSettings.maxPrunePercent),
    minPrunePercent: percentageValue(firstDefined(source.minPrunePercent, source.minimumPrunePercent), defaultContextSettings.minPrunePercent),
    standardPruneStart: percentageValue(firstDefined(source.standardPruneStart, source.standard?.pruneStart), defaultContextSettings.standardPruneStart),
    standardCompactStart: percentageValue(firstDefined(source.standardCompactStart, source.standard?.compactStart), defaultContextSettings.standardCompactStart),
    largeWindowTokens: defaultContextSettings.largeWindowTokens,
    largePruneStart: percentageValue(firstDefined(source.largePruneStart, source.large?.pruneStart), defaultContextSettings.largePruneStart),
    largeCompactStart: percentageValue(firstDefined(source.largeCompactStart, source.large?.compactStart), defaultContextSettings.largeCompactStart),
  };
}

export function contextUsagePercentage(value = {}) {
  const explicit = finiteNumber(value.percentage, value.usagePercent, value.usedPercent, value.percent);
  // Values in (0,1) are fractions (0.99 -> 99%); a whole 1 is one percent, not
  // 100% — the backend sends integer percentages, so usagePercent:1 means 1%.
  // Overflow past 100% is real signal (estimates can outgrow the window), so
  // the number survives up to 999%; only the ring visual clamps at 100%.
  if (explicit !== null) return boundedNumber(explicit > 0 && explicit < 1 ? explicit * 100 : explicit, 0, 999, 0);
  const estimatedTokens = finiteNumber(value.estimatedTokens, value.usedTokens, value.tokens, value.tokenCount);
  const limit = finiteNumber(value.limit, value.limitTokens, value.tokenLimit, value.contextLimit, value.contextWindow);
  if (estimatedTokens === null || limit === null || limit <= 0) return null;
  return boundedNumber((estimatedTokens / limit) * 100, 0, 999, 0);
}

export function normalizeContextStatus(value = {}, options = {}) {
  const source = value?.context && typeof value.context === "object" ? value.context : (value || {});
  const preferences = source.preferences || source.preference || {};
  const estimatedTokens = finiteNumber(source.estimatedTokens, source.usedTokens, source.tokenCount, source.tokens);
  const limit = finiteNumber(source.limit, source.limitTokens, source.tokenLimit, source.contextLimit, source.contextWindow, source.maxContextTokens);
  const percentage = contextUsagePercentage(source);
  const loading = Boolean(options.loading ?? source.loading);
  const estimated = booleanValue(source.estimated);
  const known = !loading && estimated !== false && estimatedTokens !== null && estimatedTokens >= 0 && limit !== null && limit > 0 && percentage !== null;
  const largeWindow = String(firstDefined(source.windowKind, source.windowClass, "")).toLowerCase() === "large"
    || (limit !== null && limit > defaultContextSettings.largeWindowTokens);
  const thresholds = source.thresholds || {};
  const thresholdPruneStart = percentageValue(firstDefined(thresholds.pruneStartPercent, thresholds.pruneStart), null);
  const thresholdCompactStart = percentageValue(firstDefined(thresholds.compactStartPercent, thresholds.compactStart), null);
  let settings = normalizeContextSettings(source);
  settings = {
    ...settings,
    retainTurns: Math.round(boundedNumber(firstDefined(thresholds.keepTurns, settings.retainTurns), 0, 100, defaultContextSettings.retainTurns)),
    maxPrunePercent: percentageValue(firstDefined(thresholds.maxPrunePercent, settings.maxPrunePercent), defaultContextSettings.maxPrunePercent),
    minPrunePercent: percentageValue(firstDefined(thresholds.minPrunePercent, settings.minPrunePercent), defaultContextSettings.minPrunePercent),
    ...(largeWindow && thresholdPruneStart !== null ? { largePruneStart: thresholdPruneStart } : {}),
    ...(largeWindow && thresholdCompactStart !== null ? { largeCompactStart: thresholdCompactStart } : {}),
    ...(!largeWindow && thresholdPruneStart !== null ? { standardPruneStart: thresholdPruneStart } : {}),
    ...(!largeWindow && thresholdCompactStart !== null ? { standardCompactStart: thresholdCompactStart } : {}),
  };
  const pruneStart = largeWindow ? settings.largePruneStart : settings.standardPruneStart;
  const compactStart = largeWindow ? settings.largeCompactStart : settings.standardCompactStart;
  const messageCount = Math.max(0, Math.round(finiteNumber(source.messageCount, source.messages) || 0));
  const autoPrune = booleanValue(source.autoPrune, source.autoPruneEnabled, source.pruneEnabled, preferences.autoPrune, preferences.autoPruneEnabled, preferences.pruneEnabled);
  return {
    agentId: String(firstDefined(options.agentId, source.agentId, "") || ""),
    entityGeneration: Math.max(0, Math.round(finiteNumber(source.entityGeneration, options.entityGeneration) || 0)),
    estimatedTokens: estimatedTokens === null ? null : Math.max(0, Math.round(estimatedTokens)),
    limit: limit === null ? null : Math.max(0, Math.round(limit)),
    percentage: known ? percentage : null,
    known,
    loading,
    windowKind: largeWindow ? "large" : "standard",
    pruneStart,
    compactStart,
    settings,
    autoPrune: autoPrune ?? false,
    hasSummary: Boolean(source.hasSummary),
    prunedPercent: percentageValue(source.prunedPercent, 0),
    messageCount,
    canCompact: booleanValue(source.canCompact) ?? (known && estimatedTokens > 0),
    canClear: booleanValue(source.canClear) ?? (messageCount > 0 || (estimatedTokens || 0) > 0),
    summaryModelConfigured: booleanValue(source.summaryModelConfigured) ?? true,
    summaryText: String(firstDefined(source.summaryText, "") || ""),
    estimateBasis: String(firstDefined(source.estimateBasis, "") || "").trim().toLowerCase() === "calibrated" ? "calibrated" : "heuristic",
    lastActualInputTokens: Math.max(0, Math.round(finiteNumber(source.lastActualInputTokens) || 0)),
    latestMessageId: String(firstDefined(source.latestMessageId, source.latestMessageID, "") || ""),
    updatedAt: String(firstDefined(source.updatedAt, source.measuredAt, "") || ""),
  };
}

// Pure gate for the "no summary model" notice so the render decision is
// testable: only an explicit false from the server shows the warning; a
// missing field keeps it hidden because compaction then uses a model summary.
export function summaryModelWarningVisible(value = {}) {
  const status = value?.settings ? value : normalizeContextStatus(value);
  return status.summaryModelConfigured === false;
}

export function contextUsageTone(value = {}) {
  const status = value?.settings ? value : normalizeContextStatus(value);
  if (!status.known || status.loading || status.percentage === null) return "unknown";
  if (status.percentage >= 100 || status.percentage >= status.compactStart) return "danger";
  const warningStart = status.pruneStart < status.compactStart ? status.pruneStart : Math.max(0, status.compactStart - 5);
  if (status.percentage >= warningStart) return "warning";
  return "normal";
}

const contextRingCircumference = 2 * Math.PI * 9;

export function contextRingGeometry(value = {}) {
  const status = value?.settings ? value : normalizeContextStatus(value);
  const tone = contextUsageTone(status);
  const percentage = tone === "unknown" ? null : boundedNumber(status.percentage, 0, 100, 0);
  return {
    circumference: contextRingCircumference,
    offset: contextRingCircumference * (1 - (percentage ?? 0) / 100),
    trackColor: "var(--ws-border, #d6d9df)",
    toneColor: tone === "unknown" ? null : contextToneColors[tone],
    tone,
    percentage,
  };
}

export function validateContextSettings(value = {}, translate = (key) => key) {
  const raw = value || {};
  const fields = {
    retainTurns: Number(raw.retainTurns),
    maxPrunePercent: Number(raw.maxPrunePercent),
    minPrunePercent: Number(raw.minPrunePercent),
    standardPruneStart: Number(raw.standardPruneStart),
    standardCompactStart: Number(raw.standardCompactStart),
    largeWindowTokens: Number(raw.largeWindowTokens),
    largePruneStart: Number(raw.largePruneStart),
    largeCompactStart: Number(raw.largeCompactStart),
  };
  if (Object.values(fields).some((number) => !Number.isFinite(number))) {
    return { valid: false, error: translate("context.validation.number") };
  }
  if (!Number.isInteger(fields.retainTurns) || fields.retainTurns < 1 || fields.retainTurns > 100) {
    return { valid: false, error: translate("context.validation.retainTurns") };
  }
  for (const key of ["maxPrunePercent", "minPrunePercent", "standardPruneStart", "standardCompactStart", "largePruneStart", "largeCompactStart"]) {
    if (fields[key] < 1 || fields[key] > 100) return { valid: false, error: translate("context.validation.percentage") };
  }
  if (fields.largeWindowTokens < 1 || fields.largeWindowTokens > 100000000 || !Number.isInteger(fields.largeWindowTokens)) {
    return { valid: false, error: translate("context.validation.largeWindow") };
  }
  if (fields.minPrunePercent > fields.maxPrunePercent) {
    return { valid: false, error: translate("context.validation.pruneRange") };
  }
  return { valid: true, value: fields, error: "" };
}

export function contextSettingsPayload(value = {}) {
  const settings = normalizeContextSettings(value);
  return {
    compactKeepTurns: settings.retainTurns,
    maxPrunePercent: settings.maxPrunePercent,
    minPrunePercent: settings.minPrunePercent,
    standard: {
      pruneStart: settings.standardPruneStart,
      compactStart: settings.standardCompactStart,
    },
    large: {
      pruneStart: settings.largePruneStart,
      compactStart: settings.largeCompactStart,
    },
  };
}

function focusableElements(root) {
  if (!root?.querySelectorAll) return [];
  return [...root.querySelectorAll('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')]
    .filter((node) => !node.closest?.(".hidden, [hidden], [aria-hidden=\"true\"]"));
}

function trapFocus(event, root) {
  if (event.key !== "Tab") return false;
  const items = focusableElements(root);
  if (!items.length) {
    event.preventDefault();
    root?.focus?.();
    return true;
  }
  const index = items.indexOf(globalThis.document?.activeElement);
  const next = event.shiftKey
    ? items[index <= 0 ? items.length - 1 : index - 1]
    : items[index < 0 || index === items.length - 1 ? 0 : index + 1];
  next?.focus?.();
  event.preventDefault();
  return true;
}

export function createContextManagementController({
  request,
  getAgent = () => null,
  onStatusChange = () => {},
  onAgentChange = () => {},
  showToast = () => {},
  showError = () => {},
  canManage = () => true,
  translate = (key) => key,
  document: documentImpl = globalThis.document,
  window: windowImpl = globalThis.window,
} = {}) {
  if (typeof request !== "function") throw new Error("createContextManagementController requires request");

  const element = (id) => documentImpl?.getElementById?.(id) || null;
  const mobileViewport = () => windowImpl?.matchMedia?.("(max-width: 767px)")?.matches
    ?? (Number(windowImpl?.innerWidth) || 1024) <= 767;
  let agentId = "";
  let epoch = 0;
  let statusRevision = 0;
  let status = normalizeContextStatus({}, { loading: false });
  let busy = "";
  let error = "";
  let open = false;
  let thresholdOpen = false;
  let clearConfirmation = false;
  let summaryOpen = false;
  let summaryLoading = false;
  // The backend never timestamps context payloads, so the panel records the
  // client-side moment a status was applied and shows that as "updated at".
  let statusAppliedAt = null;
  let focusReturn = null;
  let thresholdFocusReturn = null;
  let bodyOverflow = "";
  let bound = false;
  const listeners = [];

  const listen = (node, name, handler, options) => {
    node?.addEventListener?.(name, handler, options);
    if (node) listeners.push(() => node.removeEventListener?.(name, handler, options));
  };

  const currentAgent = () => {
    const agent = getAgent?.();
    return agent && String(agent.id || "") === agentId ? agent : null;
  };

  const currentRequest = (expectedEpoch, expectedAgentId) => epoch === expectedEpoch && agentId === expectedAgentId;

  function formatTokens(value) {
    if (value === null || value === undefined || !Number.isFinite(Number(value))) return translate("context.unknown");
    return new Intl.NumberFormat(documentImpl?.documentElement?.lang || undefined, { maximumFractionDigits: 0 }).format(Number(value));
  }

  function formatUpdatedTime(timestamp) {
    return new Intl.DateTimeFormat(documentImpl?.documentElement?.lang || undefined, { timeStyle: "medium" }).format(timestamp);
  }

  function thresholdSummary(next = status) {
    // Mirrors the runner's gate: progressive pruning only runs when the switch
    // is on and its threshold sits strictly below compaction. Compaction is an
    // automatic safety action either way, and the summary must say so instead
    // of implying that the unchecked toggle silences both thresholds.
    const pruneActive = Boolean(next.autoPrune) && next.pruneStart < next.compactStart;
    return translate(pruneActive ? "context.thresholdSummary" : "context.thresholdSummaryPruneOff", {
      prune: next.pruneStart,
      compact: next.compactStart,
    });
  }

  // Assigning textContent replaces the text node even when the string is
  // identical. render() runs several times per conversation switch, so every
  // context readout was rewritten five times with the same value and flickered.
  function setText(id, value) {
    const node = element(id);
    if (!node) return;
    const text = String(value);
    if (node.textContent !== text) node.textContent = text;
  }

  function render() {
    const button = element("contextUsageBtn");
    const ring = element("contextUsageRing");
    const label = element("contextUsageLabel");
    const panel = element("contextUsagePanel");
    const overlay = element("contextUsageOverlay");
    const tone = contextUsageTone(status);
    const hasAgent = Boolean(agentId);
    const manageAllowed = Boolean(canManage?.());
    const actionBusy = Boolean(busy);
    const percentageLabel = status.known ? `${Math.round(status.percentage)}%` : translate(status.loading ? "context.loading" : "context.unknown");

    if (button) {
      button.disabled = !hasAgent;
      button.setAttribute("aria-expanded", open ? "true" : "false");
      button.setAttribute("aria-label", translate("context.openAria", { percentage: percentageLabel }));
      button.title = translate("context.openAria", { percentage: percentageLabel });
      button.dataset.tone = tone;
    }
    if (ring) {
      const geometry = contextRingGeometry(status);
      const track = ring.querySelector?.(".context-usage-track");
      const progress = ring.querySelector?.(".context-usage-progress");
      track?.setAttribute("stroke", geometry.trackColor);
      progress?.setAttribute("stroke", geometry.toneColor || "none");
      progress?.setAttribute("stroke-dasharray", String(geometry.circumference));
      progress?.setAttribute("stroke-dashoffset", String(geometry.offset));
      ring.dataset.tone = tone;
      ring.setAttribute("aria-hidden", "true");
      ring.setAttribute("focusable", "false");
    }
    if (label) setTextIfChanged(label, status.known ? `${Math.round(status.percentage)}%` : "—");
    if (overlay) {
      overlay.classList.toggle("hidden", !open);
      overlay.classList.toggle("is-mobile", open && mobileViewport());
      overlay.setAttribute("aria-hidden", open ? "false" : "true");
    }
    if (panel) {
      panel.setAttribute("aria-modal", mobileViewport() ? "true" : "false");
      panel.setAttribute("aria-busy", actionBusy ? "true" : "false");
    }

    setText("contextEstimatedTokens", formatTokens(status.estimatedTokens));
    setText("contextTokenLimit", formatTokens(status.limit));
    setText("contextUsagePercentage", percentageLabel);
    setText("contextWindowKind", translate(status.windowKind === "large" ? "context.largeWindow" : "context.standardWindow"));
    setText("contextThresholdSummary", thresholdSummary());
    setText("contextUpdatedAt", statusAppliedAt ? translate("context.updatedAt", { time: formatUpdatedTime(statusAppliedAt) }) : "");

    const estimateBasisNode = element("contextEstimateBasis");
    if (estimateBasisNode) {
      const calibrated = status.estimateBasis === "calibrated" && status.lastActualInputTokens > 0;
      estimateBasisNode.classList.toggle("hidden", !calibrated);
      setTextIfChanged(estimateBasisNode, calibrated ? translate("context.estimateCalibrated", { tokens: formatTokens(status.lastActualInputTokens) }) : "");
    }
    const summaryModelWarning = element("contextSummaryModelWarning");
    if (summaryModelWarning) summaryModelWarning.classList.toggle("hidden", !hasAgent || !summaryModelWarningVisible(status));

    const autoPrune = element("contextAutoPrune");
    if (autoPrune) {
      autoPrune.checked = Boolean(status.autoPrune);
      autoPrune.disabled = !hasAgent || !manageAllowed || actionBusy;
    }
    const compactButton = element("contextCompactBtn");
    if (compactButton) {
      compactButton.disabled = !hasAgent || !manageAllowed || actionBusy || !status.canCompact || String(currentAgent()?.status || "") === "running";
      setTextIfChanged(compactButton, busy === "compact" ? translate("context.compacting") : translate("context.compactNow"));
    }
    const retainButton = element("contextRetainBtn");
    if (retainButton) {
      // Only a conversation that actually holds a summary has something to
      // retain, so the affordance stays disabled until compaction produced one.
      retainButton.disabled = !hasAgent || !manageAllowed || actionBusy || !status.hasSummary;
      setTextIfChanged(retainButton, busy === "retain" ? translate("context.retaining") : translate("context.retainSummary"));
    }
    const retainHint = element("contextRetainHint");
    if (retainHint) retainHint.classList.toggle("hidden", !hasAgent || !manageAllowed);
    // Viewing the summary is read-only, so unlike the actions above it stays
    // available in view-only sessions; it only needs an existing summary.
    const summaryVisible = summaryOpen && hasAgent && Boolean(status.hasSummary);
    const summaryViewButton = element("contextSummaryViewBtn");
    if (summaryViewButton) {
      summaryViewButton.disabled = !hasAgent || actionBusy || !status.hasSummary;
      summaryViewButton.setAttribute("aria-expanded", summaryVisible ? "true" : "false");
      setTextIfChanged(summaryViewButton, translate(summaryVisible ? "context.hideSummary" : "context.viewSummary"));
    }
    const summaryView = element("contextSummaryView");
    if (summaryView) {
      summaryView.classList.toggle("hidden", !summaryVisible);
      summaryView.setAttribute("aria-hidden", summaryVisible ? "false" : "true");
    }
    setText("contextSummaryText", summaryLoading ? translate("context.loading") : (status.summaryText || translate("context.summaryUnavailable")));
    const clearButton = element("contextClearBtn");
    if (clearButton) {
      clearButton.disabled = !hasAgent || !manageAllowed || actionBusy || !status.canClear;
      setTextIfChanged(clearButton, translate("context.clear"));
    }
    const settingsButton = element("contextThresholdBtn");
    if (settingsButton) settingsButton.disabled = !hasAgent || !manageAllowed || actionBusy;
    const readOnly = element("contextManagementReadOnly");
    if (readOnly) readOnly.classList.toggle("hidden", !hasAgent || manageAllowed);

    const errorNode = element("contextManagementError");
    if (errorNode) {
      errorNode.textContent = error;
      errorNode.classList.toggle("hidden", !error);
    }
    const confirm = element("contextClearConfirmation");
    if (confirm) confirm.classList.toggle("hidden", !clearConfirmation);
    const confirmButton = element("contextClearConfirmBtn");
    if (confirmButton) {
      confirmButton.disabled = busy === "clear";
      setTextIfChanged(confirmButton, busy === "clear" ? translate("context.clearing") : translate("context.clearConfirmAction"));
    }
  }

  function emit(nextStatus, options = {}) {
    status = normalizeContextStatus(nextStatus, { agentId, loading: options.loading });
    statusRevision += 1;
    onStatusChange(status);
    render();
    return status;
  }

  function mergeStatus(patch = {}) {
    return emit({
      ...status,
      ...patch,
      settings: patch.settings || status.settings,
      preferences: { autoPrune: patch.autoPrune ?? status.autoPrune },
    });
  }

  function reset(nextAgent = null, { load = false } = {}) {
    epoch += 1;
    agentId = String(nextAgent?.id || "").trim();
    busy = "";
    error = "";
    clearConfirmation = false;
    summaryOpen = false;
    summaryLoading = false;
    statusAppliedAt = null;
    closePanel({ restoreFocus: false });
    closeThresholds({ restoreFocus: false });
    status = normalizeContextStatus({}, { agentId, loading: Boolean(agentId && load) });
    statusRevision += 1;
    onStatusChange(status);
    render();
    if (agentId && load) return loadStatus();
    return Promise.resolve(status);
  }

  async function loadStatus() {
    if (!agentId) return status;
    const expectedEpoch = epoch;
    const expectedAgentId = agentId;
    if (!status.known) emit(status, { loading: true });
    const acceptedRevision = statusRevision;
    try {
      const response = await request(`/api/agents/${encodeURIComponent(expectedAgentId)}/context`, { method: "GET" });
      if (!currentRequest(expectedEpoch, expectedAgentId) || statusRevision !== acceptedRevision) return status;
      error = "";
      statusAppliedAt = Date.now();
      return emit(response?.context || response || {});
    } catch (cause) {
      if (!currentRequest(expectedEpoch, expectedAgentId)) return status;
      error = cause?.message || String(cause);
      emit(status, { loading: false });
      return status;
    }
  }

  function applyStatus(nextStatus, options = {}) {
    const expectedAgentId = String(options.agentId || nextStatus?.agentId || agentId || "").trim();
    if (!agentId || (expectedAgentId && expectedAgentId !== agentId)) return false;
    error = "";
    statusAppliedAt = Date.now();
    const next = options.partial ? {
      ...status,
      ...(nextStatus || {}),
      settings: nextStatus?.settings || status.settings,
      thresholds: nextStatus?.thresholds,
    } : (nextStatus || {});
    emit(next);
    return true;
  }

  function positionDesktopPanel() {
    const panel = element("contextUsagePanel");
    const button = element("contextUsageBtn");
    if (!panel || !button || mobileViewport()) return;
    const rect = button.getBoundingClientRect?.();
    const width = Math.min(380, Math.max(300, (Number(windowImpl?.innerWidth) || 1024) - 16));
    const left = Math.min(Math.max(8, Number(rect?.left) || 8), Math.max(8, (Number(windowImpl?.innerWidth) || 1024) - width - 8));
    panel.style.width = `${width}px`;
    panel.style.left = `${left}px`;
    panel.style.bottom = `${Math.max(8, (Number(windowImpl?.innerHeight) || 768) - (Number(rect?.top) || 0) + 8)}px`;
  }

  function openPanel(options = {}) {
    if (!agentId) return false;
    focusReturn = options.trigger || documentImpl?.activeElement || element("contextUsageBtn");
    open = true;
    error = "";
    clearConfirmation = false;
    summaryOpen = false;
    if (mobileViewport()) {
      bodyOverflow = documentImpl?.body?.style?.overflow || "";
      if (documentImpl?.body?.style) documentImpl.body.style.overflow = "hidden";
    }
    render();
    positionDesktopPanel();
    const requestedFocus = options.focusAction === "compact" ? element("contextCompactBtn") : null;
    const focusTarget = requestedFocus && !requestedFocus.disabled ? requestedFocus : element("contextUsagePanel");
    focusTarget?.focus?.();
    if (!status.known && !status.loading) loadStatus();
    return true;
  }

  function closePanel({ restoreFocus = true } = {}) {
    if (!open) return false;
    if (thresholdOpen) closeThresholds({ restoreFocus: false });
    open = false;
    clearConfirmation = false;
    if (documentImpl?.body?.style) documentImpl.body.style.overflow = bodyOverflow;
    render();
    const target = focusReturn;
    focusReturn = null;
    if (restoreFocus && target?.isConnected !== false) target?.focus?.();
    return true;
  }

  function openThresholds() {
    if (!agentId || !canManage?.() || busy) return false;
    thresholdFocusReturn = documentImpl?.activeElement || element("contextThresholdBtn");
    thresholdOpen = true;
    const modal = element("contextThresholdModal");
    modal?.classList.remove("hidden");
    modal?.setAttribute("aria-hidden", "false");
    fillThresholdForm(status.settings);
    setText("contextThresholdError", "");
    element("contextRetainTurns")?.focus?.();
    return true;
  }

  function closeThresholds({ restoreFocus = true } = {}) {
    if (!thresholdOpen) return false;
    thresholdOpen = false;
    const modal = element("contextThresholdModal");
    modal?.classList.add("hidden");
    modal?.setAttribute("aria-hidden", "true");
    const target = thresholdFocusReturn;
    thresholdFocusReturn = null;
    if (restoreFocus && target?.isConnected !== false) target?.focus?.();
    return true;
  }

  function fillThresholdForm(settings = defaultContextSettings) {
    const normalized = normalizeContextSettings(settings);
    const ids = {
      contextRetainTurns: "retainTurns",
      contextMaxPrunePercent: "maxPrunePercent",
      contextMinPrunePercent: "minPrunePercent",
      contextStandardPruneStart: "standardPruneStart",
      contextStandardCompactStart: "standardCompactStart",
      contextLargePruneStart: "largePruneStart",
      contextLargeCompactStart: "largeCompactStart",
    };
    Object.entries(ids).forEach(([id, key]) => {
      const input = element(id);
      if (input) input.value = String(normalized[key]);
    });
  }

  function thresholdFormValue() {
    return {
      retainTurns: element("contextRetainTurns")?.value,
      maxPrunePercent: element("contextMaxPrunePercent")?.value,
      minPrunePercent: element("contextMinPrunePercent")?.value,
      standardPruneStart: element("contextStandardPruneStart")?.value,
      standardCompactStart: element("contextStandardCompactStart")?.value,
      largeWindowTokens: defaultContextSettings.largeWindowTokens,
      largePruneStart: element("contextLargePruneStart")?.value,
      largeCompactStart: element("contextLargeCompactStart")?.value,
    };
  }

  async function runAgentAction(name, path, body = {}) {
    const agent = currentAgent();
    if (!agent?.id || !canManage?.() || busy) return null;
    const expectedEpoch = epoch;
    const expectedAgentId = agentId;
    busy = name;
    error = "";
    render();
    try {
      const response = await request(`/api/agents/${encodeURIComponent(expectedAgentId)}/context/${path}`, {
        method: "POST",
        body: JSON.stringify({
          ...(Number.isInteger(agent.entityGeneration) ? { entityGeneration: agent.entityGeneration } : {}),
          ...body,
        }),
      });
      if (!currentRequest(expectedEpoch, expectedAgentId)) return null;
      if (response?.context) applyStatus(response.context, { agentId: expectedAgentId });
      return response;
    } catch (cause) {
      if (currentRequest(expectedEpoch, expectedAgentId)) {
        error = cause?.message || String(cause);
        showError(cause);
      }
      return null;
    } finally {
      if (currentRequest(expectedEpoch, expectedAgentId)) {
        busy = "";
        render();
      }
    }
  }

  async function compact() {
    const response = await runAgentAction("compact", "compact", {
      ...(status.latestMessageId ? { expectedLatestMessageId: status.latestMessageId } : {}),
    });
    if (!response) return null;
    showToast(translate(response.compacted === false ? "context.compactNoop" : "context.compactSuccess"), response.compacted === false ? "warn" : "success");
    return response;
  }

  async function retainSummary() {
    if (!agentId || !canManage?.() || busy || !status.hasSummary) return null;
    const response = await runAgentAction("retain", "retain", {});
    if (!response) return null;
    showToast(translate("context.retainSuccess"), "success");
    return response;
  }

  // Opening the viewer re-fetches status because context.updated events stay
  // lightweight and never carry summaryText; only a fresh GET has the full
  // text. loadStatus already guards against stale epochs and races.
  async function toggleSummaryView() {
    if (summaryOpen) {
      summaryOpen = false;
      render();
      return false;
    }
    if (!agentId || !status.hasSummary) return false;
    summaryOpen = true;
    summaryLoading = true;
    render();
    try {
      await loadStatus();
    } finally {
      summaryLoading = false;
      render();
    }
    return true;
  }

  function requestClearConfirmation() {
    if (!agentId || busy || !status.canClear) return false;
    clearConfirmation = true;
    render();
    element("contextClearConfirmBtn")?.focus?.();
    return true;
  }

  async function clear() {
    if (!clearConfirmation) return requestClearConfirmation();
    if (!status.latestMessageId) {
      error = translate("context.clearStale");
      render();
      return null;
    }
    const response = await runAgentAction("clear", "clear", { expectedLatestMessageId: status.latestMessageId });
    if (!response) return null;
    clearConfirmation = false;
    if (!response.context) await loadStatus();
    showToast(translate("context.clearSuccess"), "success");
    return response;
  }

  async function setAutoPrune(enabled) {
    if (!agentId || !canManage?.() || busy) return null;
    const expectedEpoch = epoch;
    const expectedAgentId = agentId;
    const previous = status.autoPrune;
    busy = "preferences";
    error = "";
    render();
    try {
      const agent = currentAgent();
      const response = await request(`/api/agents/${encodeURIComponent(expectedAgentId)}/context/preferences`, {
        method: "PATCH",
        body: JSON.stringify({
          pruneEnabled: Boolean(enabled),
          ...(Number.isInteger(agent?.entityGeneration) ? { entityGeneration: agent.entityGeneration } : {}),
        }),
      });
      if (!currentRequest(expectedEpoch, expectedAgentId)) return null;
      if (response?.agent) onAgentChange(response.agent);
      if (response?.context) applyStatus(response.context, { agentId: expectedAgentId });
      else mergeStatus({ autoPrune: Boolean(enabled) });
      showToast(translate(enabled ? "context.autoPruneEnabled" : "context.autoPruneDisabled"), "success");
      return response;
    } catch (cause) {
      if (currentRequest(expectedEpoch, expectedAgentId)) {
        error = cause?.message || String(cause);
        mergeStatus({ autoPrune: previous });
        showError(cause);
      }
      return null;
    } finally {
      if (currentRequest(expectedEpoch, expectedAgentId)) {
        busy = "";
        render();
      }
    }
  }

  async function saveThresholds(event) {
    event?.preventDefault?.();
    if (!canManage?.() || busy) return null;
    const validation = validateContextSettings(thresholdFormValue(), translate);
    if (!validation.valid) {
      setText("contextThresholdError", validation.error);
      return null;
    }
    const expectedEpoch = epoch;
    const expectedAgentId = agentId;
    busy = "thresholds";
    const saveButton = element("contextThresholdSaveBtn");
    if (saveButton) {
      saveButton.disabled = true;
      saveButton.textContent = translate("context.saving");
    }
    try {
      const payload = contextSettingsPayload(validation.value);
      const response = await request("/api/runtime/context-settings", {
        method: "PATCH",
        body: JSON.stringify(payload),
      });
      if (!currentRequest(expectedEpoch, expectedAgentId)) return null;
      const settings = normalizeContextSettings(response?.contextManagement || response?.settings || response?.contextSettings || payload);
      mergeStatus({ settings });
      showToast(translate("context.thresholdSaved"), "success");
      closeThresholds();
      return response;
    } catch (cause) {
      if (currentRequest(expectedEpoch, expectedAgentId)) {
        setText("contextThresholdError", cause?.message || String(cause));
        showError(cause);
      }
      return null;
    } finally {
      if (currentRequest(expectedEpoch, expectedAgentId)) {
        busy = "";
        if (saveButton) {
          saveButton.disabled = false;
          saveButton.textContent = translate("context.saveThresholds");
        }
        render();
      }
    }
  }

  function handleDocumentPointer(event) {
    if (!open || mobileViewport() || thresholdOpen) return;
    if (element("contextUsagePanel")?.contains?.(event.target) || element("contextUsageBtn")?.contains?.(event.target)) return;
    closePanel({ restoreFocus: false });
  }

  function handleDocumentKeydown(event) {
    if (event.key === "Escape") {
      if (thresholdOpen) {
        closeThresholds();
        event.preventDefault();
        return;
      }
      if (open) {
        closePanel();
        event.preventDefault();
      }
      return;
    }
    if (thresholdOpen) trapFocus(event, element("contextThresholdDialog"));
    else if (open && mobileViewport()) trapFocus(event, element("contextUsagePanel"));
  }

  const contextShowPercentStorageKey = "autoto.ui.contextShowPercent";
  function readShowPercentPreference() {
    try {
      return windowImpl?.localStorage?.getItem?.(contextShowPercentStorageKey) !== "0";
    } catch {
      return true;
    }
  }
  function applyShowPercentPreference(show) {
    documentImpl?.body?.classList?.toggle?.("context-hide-usage-percent", !show);
    const toggle = element("contextShowPercent");
    if (toggle) toggle.checked = show;
  }
  function setShowPercent(show) {
    try {
      windowImpl?.localStorage?.setItem?.(contextShowPercentStorageKey, show ? "1" : "0");
    } catch {
      /* ignore storage failures */
    }
    applyShowPercentPreference(show);
  }

  function bind() {
    if (bound || !documentImpl) return () => {};
    bound = true;
    listen(element("contextUsageBtn"), "click", (event) => {
      if (open) closePanel();
      else openPanel({ trigger: event.currentTarget });
    });
    listen(element("closeContextUsageBtn"), "click", () => closePanel());
    listen(element("contextUsageBackdrop"), "click", () => closePanel());
    listen(element("contextAutoPrune"), "change", (event) => setAutoPrune(event.currentTarget.checked));
    listen(element("contextShowPercent"), "change", (event) => setShowPercent(event.currentTarget.checked));
    applyShowPercentPreference(readShowPercentPreference());
    listen(element("contextCompactBtn"), "click", () => compact());
    listen(element("contextRetainBtn"), "click", () => retainSummary());
    listen(element("contextSummaryViewBtn"), "click", () => toggleSummaryView());
    listen(element("contextClearBtn"), "click", requestClearConfirmation);
    listen(element("contextClearCancelBtn"), "click", () => {
      clearConfirmation = false;
      render();
      element("contextClearBtn")?.focus?.();
    });
    listen(element("contextClearConfirmBtn"), "click", () => clear());
    listen(element("contextThresholdBtn"), "click", openThresholds);
    listen(element("contextThresholdCancelBtn"), "click", () => closeThresholds());
    listen(element("closeContextThresholdBtn"), "click", () => closeThresholds());
    listen(element("contextThresholdBackdrop"), "click", () => closeThresholds());
    listen(element("contextThresholdDefaultsBtn"), "click", () => fillThresholdForm(defaultContextSettings));
    listen(element("contextThresholdForm"), "submit", saveThresholds);
    listen(documentImpl, "pointerdown", handleDocumentPointer);
    listen(documentImpl, "keydown", handleDocumentKeydown);
    listen(windowImpl, "resize", () => {
      if (!open) return;
      closePanel({ restoreFocus: false });
    });
    render();
    return destroy;
  }

  function destroy() {
    closePanel({ restoreFocus: false });
    closeThresholds({ restoreFocus: false });
    while (listeners.length) listeners.pop()?.();
    bound = false;
  }

  return {
    applyStatus,
    bind,
    clear,
    close: closePanel,
    compact,
    destroy,
    getStatus: () => ({ ...status, settings: { ...status.settings } }),
    load: loadStatus,
    open: openPanel,
    openThresholds,
    reset,
    saveThresholds,
    setAgent(nextAgent, options = {}) {
      const nextAgentId = String(nextAgent?.id || "").trim();
      if (nextAgentId && nextAgentId === agentId) return Promise.resolve(status);
      return reset(nextAgent, { load: options.load !== false });
    },
    setAutoPrune,
    toggleSummary: toggleSummaryView,
  };
}
