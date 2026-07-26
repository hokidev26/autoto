import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { confirm as platformConfirm } from "./platform.mjs";
import { api } from "./runtime.mjs";
import { t } from "./i18n.mjs?v=provider-draft-session-1";

export const agentModelRoles = Object.freeze(["explore", "plan", "general", "search"]);
export const defaultReasoningEffortValues = Object.freeze(["auto", "low", "medium", "high"]);

export function normalizeDefaultReasoningEffort(value) {
  const normalized = String(value || "").trim().toLowerCase();
  return defaultReasoningEffortValues.includes(normalized) ? normalized : "auto";
}

export function isAgentModelReference(value) {
  const normalized = String(value || "").trim();
  const separator = normalized.indexOf(":");
  return separator > 0 && separator < normalized.length - 1 && !/[\0\r\n]/.test(normalized);
}

export function normalizeAgentModelSettings(value = {}) {
  const source = value && typeof value === "object" ? value : {};
  const defaultModel = String(source.defaultModel || "").trim();
  const summaryModel = String(source.summaryModel || defaultModel).trim();
  // Blank is meaningful: it means "follow the summary model", so it must not
  // fall back to defaultModel the way summaryModel does.
  const safetyModel = String(source.safetyModel || "").trim();
  const defaultReasoningEffort = normalizeDefaultReasoningEffort(source.defaultReasoningEffort);
  const rawModels = source.subagentModels && typeof source.subagentModels === "object" ? source.subagentModels : {};
  const rawPools = source.subagentModelPools && typeof source.subagentModelPools === "object" ? source.subagentModelPools : {};
  const subagentModels = {};
  const subagentModelPools = {};
  for (const role of agentModelRoles) {
    const preferred = String(rawModels[role] || "").trim();
    if (preferred) subagentModels[role] = preferred;
    const pool = [...new Set((Array.isArray(rawPools[role]) ? rawPools[role] : [])
      .map((model) => String(model || "").trim())
      .filter(Boolean))];
    if (pool.length) subagentModelPools[role] = pool;
  }
  return { defaultModel, summaryModel, safetyModel, defaultReasoningEffort, subagentModels, subagentModelPools };
}

export function agentModelSettingsPayload(value = {}) {
  const normalized = normalizeAgentModelSettings(value);
  const subagentModels = {};
  const subagentModelPools = {};
  for (const role of agentModelRoles) {
    const preferred = normalized.subagentModels[role] || "";
    const pool = [...(normalized.subagentModelPools[role] || [])];
    if (preferred) subagentModels[role] = preferred;
    if (pool.length) {
      if (preferred && !pool.includes(preferred)) pool.unshift(preferred);
      subagentModelPools[role] = pool;
    }
  }
  return {
    defaultModel: normalized.defaultModel,
    summaryModel: normalized.summaryModel,
    safetyModel: normalized.safetyModel,
    subagentModels,
    subagentModelPools,
  };
}

export function normalizeModelAggregateList(value) {
  const items = Array.isArray(value) ? value : Array.isArray(value?.aggregates) ? value.aggregates : [];
  return items.map((item) => ({
    id: String(item?.id || ""),
    name: String(item?.name || "").trim(),
    mode: String(item?.mode || "priority").trim() || "priority",
    members: (Array.isArray(item?.members) ? item.members : []).map((member) => String(member || "").trim()).filter(Boolean),
    revision: Math.max(0, Math.trunc(Number(item?.revision) || 0)),
    updatedAt: String(item?.updatedAt || item?.updated_at || ""),
  })).filter((item) => item.name);
}

export function modelAggregateMembers(value) {
  const source = Array.isArray(value) ? value : String(value || "").split(/\r?\n/);
  return source.map((member) => String(member || "").trim()).filter(Boolean);
}

export function modelAggregateActionRequest(action, aggregate = {}, values = {}) {
  const name = String(values.name ?? aggregate?.name ?? "").trim();
  const path = `/api/model-aggregates/${encodeURIComponent(name)}`;
  const revision = Math.max(0, Math.trunc(Number(values.revision ?? aggregate?.revision) || 0));
  if (action === "save") {
    return {
      path,
      options: {
        method: "PUT",
        body: JSON.stringify({ mode: "priority", members: modelAggregateMembers(values.members), revision }),
      },
    };
  }
  if (action === "delete") return { path, options: { method: "DELETE", body: JSON.stringify({ revision }) } };
  throw new Error(`unsupported model aggregate action: ${action}`);
}

export function runtimeReasoningSettingsRequest(value, runtimeSettings = {}) {
  return {
    path: "/api/runtime/model-settings",
    options: {
      method: "PATCH",
      body: JSON.stringify({
        defaultReasoningEffort: normalizeDefaultReasoningEffort(value),
        revision: Math.max(0, Math.trunc(Number(runtimeSettings?.revision) || 0)),
      }),
    },
  };
}


// Creates the model-routing controller: default/subagent model selection, per-role
// model pools, reasoning-effort defaults, and model-aggregate CRUD. `ctx` supplies the
// shared provider-console primitives (model catalog + refresh helpers) via injection.
export function createModelRoutingController(ctx) {
  const {
    state,
    requestAPI,
    notifyTerminal,
    refreshActiveSettingsPanel,
    showError,
    openSettingsModal,
    refreshModelCatalog,
    applyPreferredModel,
    clearVisibleConfiguredModelHides,
    isModelHidden,
    setModelHidden,
    renderModelOptions,
    modelProvidersForUI,
    providerModelList,
    providerRuntimeSelectable,
    providerLabel,
    modelOptionValue,
  } = ctx;
  const mt = (key, params) => t(`modelProvider.${key}`, params);

  async function loadModelAggregates({ force = false } = {}) {
    if (state.modelAggregatesLoading) return false;
    if (!force && state.modelAggregatesLoaded === true) return true;
    const seq = (state.modelAggregateSeq || 0) + 1;
    state.modelAggregateSeq = seq;
    state.modelAggregatesLoading = true;
    state.modelAggregatesError = "";
    if (state.activeSettingsPanel === "models") refreshActiveSettingsPanel?.();
    try {
      const response = await requestAPI("/api/model-aggregates");
      if (seq !== state.modelAggregateSeq) return false;
      state.modelAggregates = normalizeModelAggregateList(response);
      state.modelAggregatesLoaded = true;
      return true;
    } catch (error) {
      if (seq !== state.modelAggregateSeq) return false;
      state.modelAggregatesError = error?.message || mt("unknown");
      state.modelAggregatesLoaded = true;
      return false;
    } finally {
      if (seq === state.modelAggregateSeq) {
        state.modelAggregatesLoading = false;
        if (state.activeSettingsPanel === "models") refreshActiveSettingsPanel?.();
      }
    }
  }

  function agentModelSettingsSource() {
    return normalizeAgentModelSettings({
      ...(state.settings?.agent || {}),
      defaultReasoningEffort: state.settings?.runtimeSettings?.defaultReasoningEffort || "auto",
    });
  }

  function agentModelSettingsState() {
    const source = agentModelSettingsSource();
    const sourceSignature = JSON.stringify(source);
    const current = state.agentModelSettings;
    if (!current || (!current.dirty && current.sourceSignature !== sourceSignature)) {
      state.agentModelSettings = {
        draft: source,
        sourceSignature,
        dirty: false,
        saving: false,
        result: null,
      };
    } else {
      current.draft = normalizeAgentModelSettings(current.draft || source);
      current.saving = Boolean(current.saving);
    }
    return state.agentModelSettings;
  }

  function agentSettingsAvailableModels(draft = {}) {
    const referenced = new Set([
      draft.defaultModel,
      draft.summaryModel,
      draft.safetyModel,
      ...Object.values(draft.subagentModels || {}),
      ...Object.values(draft.subagentModelPools || {}).flat(),
    ].map((value) => String(value || "").trim()).filter(Boolean));
    const records = [];
    const seen = new Set();
    for (const provider of modelProvidersForUI()) {
      for (const model of providerModelList(provider)) {
        const value = modelOptionValue(provider, model);
        if (seen.has(value)) continue;
        const available = Boolean(provider.enabled && providerRuntimeSelectable(provider));
        if (!available && !referenced.has(value)) continue;
        seen.add(value);
        records.push({ value, provider: providerLabel(provider), model, available });
      }
    }
    for (const aggregate of normalizeModelAggregateList(state.modelAggregates)) {
      const value = `aggregate:${aggregate.name}`;
      if (seen.has(value)) continue;
      seen.add(value);
      records.push({ value, provider: mt("routing.aggregateProvider"), model: aggregate.name, available: true, aggregate: true });
    }
    for (const value of referenced) {
      if (seen.has(value)) continue;
      seen.add(value);
      const [provider, ...modelParts] = value.split(":");
      records.push({ value, provider, model: modelParts.join(":"), available: false });
    }
    return records;
  }

  // inheritLabel names what an empty value falls back to. Subagent roles inherit
  // the global default; the safety model inherits the summary model, so the two
  // cannot share one label without misdescribing one of them.
  function renderAgentModelSelectOptions(current, options, { allowInherited = false, inheritLabel = "" } = {}) {
    const selected = String(current || "").trim();
    const inherited = allowInherited || inheritLabel
      ? `<option value="" ${selected ? "" : "selected"}>${escapeHtml(inheritLabel || mt("routing.inheritDefault"))}</option>`
      : "";
    return inherited + options.map((item) => {
      const suffix = item.available ? "" : ` · ${mt("routing.currentlyUnavailable")}`;
      return `<option value="${escapeAttr(item.value)}" ${item.value === selected ? "selected" : ""}>${escapeHtml(item.value + suffix)}</option>`;
    }).join("");
  }

  function agentModelPoolSummary(unrestricted, count) {
    return unrestricted ? mt("routing.unrestricted") : mt("modelCount", { count });
  }

  function renderAgentRolePreferenceField(role, draft, options) {
    const preferred = draft.subagentModels?.[role] || "";
    return `<label class="settings-form-field compact-settings-field"><span>${escapeHtml(mt(`routing.roles.${role}.label`))}</span><select name="subagentModel_${escapeAttr(role)}" data-agent-role-model="${escapeAttr(role)}">${renderAgentModelSelectOptions(preferred, options, { allowInherited: true })}</select><small data-settings-help-copy>${escapeHtml(mt(`routing.roles.${role}.description`))}</small></label>`;
  }

  function renderAgentModelPoolControl(role, draft, options) {
    const pool = draft.subagentModelPools?.[role] || [];
    const unrestricted = pool.length === 0;
    return `<details class="compact-multi-select agent-model-pool-details" data-agent-model-pool-details="${escapeAttr(role)}">
      <summary class="compact-multi-select-summary"><span>${escapeHtml(mt(`routing.roles.${role}.label`))}</span><strong class="agent-model-pool-state ${unrestricted ? "muted" : "ok"}" data-agent-model-pool-summary="${escapeAttr(role)}">${escapeHtml(agentModelPoolSummary(unrestricted, pool.length))}</strong></summary>
      <fieldset class="compact-multi-select-panel agent-model-pool-fieldset">
        <legend class="sr-only">${escapeHtml(mt("routing.allowedModels"))}</legend>
        <label class="compact-multi-select-all agent-model-pool-all"><input type="checkbox" data-agent-model-pool-all="${escapeAttr(role)}" ${unrestricted ? "checked" : ""}><span><strong>${escapeHtml(mt("routing.allowAllModels"))}</strong><small data-settings-help-copy>${escapeHtml(mt("routing.allowAllModelsHelp"))}</small></span></label>
        <div class="compact-multi-select-options agent-model-pool-options" data-agent-model-pool-options="${escapeAttr(role)}">
          ${options.map((item) => `<label class="compact-multi-select-option agent-model-pool-option"><input type="checkbox" value="${escapeAttr(item.value)}" data-agent-model-pool-option="${escapeAttr(role)}" ${pool.includes(item.value) ? "checked" : ""} ${unrestricted ? "disabled" : ""}><span><strong>${escapeHtml(item.value)}</strong><small>${escapeHtml(item.available ? item.provider : mt("routing.currentlyUnavailable"))}</small></span></label>`).join("") || `<div class="settings-empty-card compact">${escapeHtml(mt("routing.noModelsForPool"))}</div>`}
        </div>
      </fieldset>
    </details>`;
  }

  function renderDefaultReasoningOptions(current) {
    const selected = normalizeDefaultReasoningEffort(current);
    return defaultReasoningEffortValues.map((value) => `<option value="${escapeAttr(value)}" ${value === selected ? "selected" : ""}>${escapeHtml(mt(value === "auto" ? "automatic" : value))}</option>`).join("");
  }

  function renderModelAggregateEditor(editor = {}) {
    const editing = editor.mode === "edit";
    const name = String(editor.name || "");
    const members = modelAggregateMembers(editor.members).join("\n");
    return `<form id="modelAggregateForm" class="model-aggregate-editor compact-settings-editor" data-model-aggregate-mode="${editing ? "edit" : "create"}">
      <div class="compact-settings-grid two-column">
        <label class="settings-form-field compact-settings-field"><span>${escapeHtml(mt("routing.aggregateName"))}</span><input name="aggregateName" value="${escapeAttr(name)}" maxlength="120" pattern="[A-Za-z0-9][A-Za-z0-9._-]{0,119}" ${editing ? "readonly" : "required"}><small data-settings-help-copy>${escapeHtml(mt("routing.aggregateNameHelp"))}</small></label>
        <label class="settings-form-field compact-settings-field"><span>${escapeHtml(mt("routing.aggregateStrategy"))}</span><select name="aggregateMode" disabled><option value="priority" selected>${escapeHtml(mt("routing.aggregatePriority"))}</option></select><small data-settings-help-copy>${escapeHtml(mt("routing.aggregateStrategyHelp"))}</small></label>
        <label class="settings-form-field compact-settings-field full-width"><span>${escapeHtml(mt("routing.aggregateMembers"))}</span><textarea name="aggregateMembers" rows="5" required placeholder="openai:gpt-5\ncodex:gpt-5.5">${escapeHtml(members)}</textarea><small data-settings-help-copy>${escapeHtml(mt("routing.aggregateMembersHelp"))}</small></label>
      </div>
      <div class="compact-settings-editor-actions settings-inline-actions"><button class="settings-action-btn subtle" type="button" data-model-aggregate-cancel>${escapeHtml(mt("cancel"))}</button><button class="settings-action-btn primary" type="submit" data-model-aggregate-save>${escapeHtml(editing ? mt("routing.updateAggregate") : mt("routing.createAggregate"))}</button></div>
    </form>`;
  }

  function renderModelAggregateSection() {
    const aggregates = normalizeModelAggregateList(state.modelAggregates);
    const editor = state.modelAggregateEditor && typeof state.modelAggregateEditor === "object" ? state.modelAggregateEditor : null;
    const busy = Boolean(state.modelAggregateBusy);
    let content = "";
    if (state.modelAggregatesLoading || state.modelAggregatesLoaded !== true) content = `<div class="settings-empty-card compact" role="status">${escapeHtml(mt("routing.loadingAggregates"))}</div>`;
    else if (state.modelAggregatesError) content = `<div class="settings-alert" role="alert">${escapeHtml(state.modelAggregatesError)}</div>`;
    else if (!aggregates.length) content = `<div class="settings-empty-card compact" role="status">${escapeHtml(mt("routing.noAggregates"))}</div>`;
    else content = `<div class="model-aggregate-list">${aggregates.map((aggregate) => `<article class="model-aggregate-row" data-model-aggregate-row="${escapeAttr(aggregate.name)}"><div class="model-aggregate-main"><strong>aggregate:${escapeHtml(aggregate.name)}</strong><ol>${aggregate.members.map((member) => `<li><code>${escapeHtml(member)}</code></li>`).join("")}</ol></div><div class="model-aggregate-actions settings-inline-actions"><button class="settings-action-btn subtle" type="button" data-model-aggregate-edit="${escapeAttr(aggregate.name)}" ${busy ? "disabled" : ""}>${escapeHtml(mt("edit"))}</button><button class="settings-action-btn danger" type="button" data-model-aggregate-delete="${escapeAttr(aggregate.name)}" ${busy ? "disabled" : ""}>${escapeHtml(mt("delete"))}</button></div></article>`).join("")}</div>`;
    return `<section class="compact-settings-section model-aggregate-section" aria-labelledby="model-aggregate-title"><div class="compact-settings-section-copy"><h2 id="model-aggregate-title">${escapeHtml(mt("routing.aggregatesTitle"))}</h2><p data-settings-help-copy>${escapeHtml(mt("routing.aggregatesDescription"))}</p></div><div class="compact-settings-section-controls"><div class="compact-settings-section-toolbar"><span>${escapeHtml(mt("routing.aggregateCount", { count: aggregates.length }))}</span><button class="settings-action-btn subtle" type="button" data-model-aggregate-add ${busy || editor ? "disabled" : ""}>＋ ${escapeHtml(mt("routing.addAggregate"))}</button></div>${content}${editor ? renderModelAggregateEditor(editor) : ""}</div></section>`;
  }

  function renderModelSettingsContent() {
    const settingsState = agentModelSettingsState();
    const draft = settingsState.draft;
    const options = agentSettingsAvailableModels(draft);
    const runtimeRevision = Math.max(0, Math.trunc(Number(state.settings?.runtimeSettings?.revision) || 0));
    const result = settingsState.result && typeof settingsState.result === "object"
      ? `<div class="agent-model-settings-result settings-alert ${escapeAttr(settingsState.result.tone || "info")}" role="status" aria-live="polite">${escapeHtml(settingsState.result.message || "")}</div>`
      : "";
    return `<div class="settings-live-page compact-settings-page agent-model-settings-page" aria-labelledby="settings-model-page-title">
      <header class="compact-settings-header"><div class="compact-settings-heading"><div class="settings-hero-kicker">${escapeHtml(mt("routing.kicker"))}</div><h1 id="settings-model-page-title">${escapeHtml(mt("routing.title"))}</h1><p data-settings-help-copy>${escapeHtml(mt("routing.description"))}</p></div><div class="compact-settings-header-actions settings-inline-actions"><button id="settingsRefreshModelsBtn" class="settings-action-btn" type="button">${escapeHtml(mt("refreshModels"))}</button><button id="settingsOpenLoginBtn" class="settings-action-btn" type="button">${escapeHtml(mt("providerSettings"))}</button></div></header>
      ${result}
      <form id="agentModelSettingsForm" class="compact-settings-form agent-model-settings-form" aria-busy="${settingsState.saving ? "true" : "false"}">
        <section class="compact-settings-section" aria-labelledby="agent-model-defaults-title"><div class="compact-settings-section-copy"><h2 id="agent-model-defaults-title">${escapeHtml(mt("routing.globalDefaults"))}</h2><p data-settings-help-copy>${escapeHtml(mt("routing.globalDefaultsDescription"))}</p></div><div class="compact-settings-section-controls"><div class="compact-settings-grid two-column"><label class="settings-form-field compact-settings-field"><span>${escapeHtml(mt("routing.defaultModel"))}</span><select name="defaultModel" required>${renderAgentModelSelectOptions(draft.defaultModel, options)}</select><small data-settings-help-copy>${escapeHtml(mt("routing.defaultModelHelp"))}</small></label><label class="settings-form-field compact-settings-field"><span>${escapeHtml(mt("routing.summaryModel"))}</span><select name="summaryModel" required>${renderAgentModelSelectOptions(draft.summaryModel, options)}</select><small data-settings-help-copy>${escapeHtml(mt("routing.summaryModelHelp"))}</small></label><label class="settings-form-field compact-settings-field"><span>${escapeHtml(mt("routing.safetyModel"))}</span><select name="safetyModel">${renderAgentModelSelectOptions(draft.safetyModel, options, { inheritLabel: mt("routing.safetyModelInherit") })}</select><small data-settings-help-copy>${escapeHtml(mt("routing.safetyModelHelp"))}</small></label><label class="settings-form-field compact-settings-field full-width"><span>${escapeHtml(mt("defaultReasoningEffort"))}</span><select name="defaultReasoningEffort" ${runtimeRevision > 0 ? "" : "disabled"}>${renderDefaultReasoningOptions(draft.defaultReasoningEffort)}</select><small${runtimeRevision > 0 ? " data-settings-help-copy" : ""}>${escapeHtml(runtimeRevision > 0 ? mt("routing.defaultReasoningHelp") : mt("routing.runtimeSettingsUnavailable"))}</small></label></div></div></section>
        <section class="compact-settings-section" aria-labelledby="agent-model-preferences-title"><div class="compact-settings-section-copy"><h2 id="agent-model-preferences-title">${escapeHtml(mt("routing.subagentPreferences"))}</h2><p data-settings-help-copy>${escapeHtml(mt("routing.subagentPreferencesDescription"))}</p></div><div class="compact-settings-section-controls"><div class="compact-settings-grid two-column">${agentModelRoles.map((role) => renderAgentRolePreferenceField(role, draft, options)).join("")}</div></div></section>
        <section class="compact-settings-section" aria-labelledby="agent-model-pools-title"><div class="compact-settings-section-copy"><h2 id="agent-model-pools-title">${escapeHtml(mt("routing.subagentPools"))}</h2><p data-settings-help-copy>${escapeHtml(mt("routing.subagentPoolsDescription"))}</p></div><div class="compact-settings-section-controls"><div class="compact-settings-grid two-column">${agentModelRoles.map((role) => renderAgentModelPoolControl(role, draft, options)).join("")}</div></div></section>
        <footer class="compact-settings-footer agent-model-settings-footer"><div><span id="agentModelSettingsDirtyBadge" class="settings-badge ${settingsState.dirty ? "warn" : "ok"}">${escapeHtml(settingsState.dirty ? mt("routing.unsaved") : mt("routing.saved"))}</span><small data-settings-help-copy>${escapeHtml(mt("routing.persistenceDescription"))}</small></div><div class="settings-inline-actions"><button id="resetAgentModelSettingsBtn" class="settings-action-btn subtle" type="button" ${settingsState.saving ? "disabled" : ""}>${escapeHtml(mt("routing.reset"))}</button><button id="saveAgentModelSettingsBtn" class="settings-action-btn primary" type="submit" ${settingsState.saving ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(settingsState.saving ? mt("saving") : mt("routing.save"))}</button></div></footer>
      </form>
    </div>`;
  }

  function readAgentModelSettingsForm(form) {
    const current = agentModelSettingsState().draft;
    const subagentModels = {};
    const subagentModelPools = {};
    for (const role of agentModelRoles) {
      const preferred = String(form?.elements?.[`subagentModel_${role}`]?.value || "").trim();
      if (preferred) subagentModels[role] = preferred;
      const unrestricted = Boolean(form?.querySelector?.(`[data-agent-model-pool-all="${role}"]`)?.checked);
      if (unrestricted) continue;
      const pool = [...(form?.querySelectorAll?.(`[data-agent-model-pool-option="${role}"]:checked`) || [])]
        .map((node) => String(node.value || "").trim())
        .filter(Boolean);
      if (pool.length) subagentModelPools[role] = pool;
    }
    return normalizeAgentModelSettings({
      ...current,
      defaultModel: form?.elements?.defaultModel?.value || "",
      summaryModel: form?.elements?.summaryModel?.value || "",
      safetyModel: form?.elements?.safetyModel?.value || "",
      defaultReasoningEffort: form?.elements?.defaultReasoningEffort?.value || current.defaultReasoningEffort || "auto",
      subagentModels,
      subagentModelPools,
    });
  }

  function validateAgentModelSettings(draft) {
    const checks = [
      [mt("routing.defaultModel"), draft.defaultModel],
      [mt("routing.summaryModel"), draft.summaryModel],
    ];
    if (draft.safetyModel) checks.push([mt("routing.safetyModel"), draft.safetyModel]);
    for (const role of agentModelRoles) {
      if (draft.subagentModels?.[role]) checks.push([mt(`routing.roles.${role}.label`), draft.subagentModels[role]]);
      for (const model of draft.subagentModelPools?.[role] || []) checks.push([mt(`routing.roles.${role}.label`), model]);
    }
    const invalid = checks.find(([, model]) => !isAgentModelReference(model));
    if (invalid) throw new Error(mt("routing.invalidModelReference", { field: invalid[0] }));
  }

  function updateAgentModelDirtyUI(dirty) {
    const badge = $("agentModelSettingsDirtyBadge");
    if (!badge) return;
    badge.classList.toggle("warn", dirty);
    badge.classList.toggle("ok", !dirty);
    badge.textContent = mt(dirty ? "routing.unsaved" : "routing.saved");
  }

  function syncAgentModelSettingsForm(form) {
    const settingsState = agentModelSettingsState();
    settingsState.draft = readAgentModelSettingsForm(form);
    settingsState.dirty = true;
    settingsState.result = null;
    updateAgentModelDirtyUI(true);
    return settingsState.draft;
  }

  function updateAgentModelPoolSummary(form, role) {
    if (!role) return;
    const unrestricted = Boolean(form.querySelector(`[data-agent-model-pool-all="${role}"]`)?.checked);
    const selectedCount = form.querySelectorAll(`[data-agent-model-pool-option="${role}"]:checked`).length;
    const summary = form.querySelector(`[data-agent-model-pool-summary="${role}"]`);
    if (!summary) return;
    summary.textContent = agentModelPoolSummary(unrestricted, selectedCount);
    summary.classList.toggle("muted", unrestricted);
    summary.classList.toggle("ok", !unrestricted);
  }

  function handleAgentModelSettingsChange(event, form) {
    const target = event.target;
    const role = target?.dataset?.agentModelPoolAll || target?.dataset?.agentRoleModel || target?.dataset?.agentModelPoolOption || "";
    if (target?.dataset?.agentModelPoolAll) {
      const unrestricted = Boolean(target.checked);
      const details = form.querySelector(`[data-agent-model-pool-details="${role}"]`);
      if (details) details.open = !unrestricted;
      form.querySelectorAll(`[data-agent-model-pool-option="${role}"]`).forEach((node) => { node.disabled = unrestricted; });
      if (!unrestricted && !form.querySelector(`[data-agent-model-pool-option="${role}"]:checked`)) {
        const preferred = String(form.elements?.[`subagentModel_${role}`]?.value || form.elements?.defaultModel?.value || "").trim();
        const preferredOption = [...form.querySelectorAll(`[data-agent-model-pool-option="${role}"]`)].find((node) => node.value === preferred);
        if (preferredOption) preferredOption.checked = true;
      }
    } else if (target?.dataset?.agentRoleModel) {
      const unrestricted = form.querySelector(`[data-agent-model-pool-all="${role}"]`)?.checked;
      if (!unrestricted && target.value) {
        const preferredOption = [...form.querySelectorAll(`[data-agent-model-pool-option="${role}"]`)].find((node) => node.value === target.value);
        if (preferredOption) preferredOption.checked = true;
      }
    }
    updateAgentModelPoolSummary(form, role);
    syncAgentModelSettingsForm(form);
  }

  async function persistDefaultReasoningEffort(value) {
    const desired = normalizeDefaultReasoningEffort(value);
    let runtimeSettings = state.settings?.runtimeSettings || {};
    if (Math.max(0, Math.trunc(Number(runtimeSettings?.revision) || 0)) < 1) return runtimeSettings;
    if (normalizeDefaultReasoningEffort(runtimeSettings.defaultReasoningEffort) === desired) return runtimeSettings;
    let request = runtimeReasoningSettingsRequest(desired, runtimeSettings);
    try {
      return await requestAPI(request.path, request.options);
    } catch (error) {
      if (error?.status !== 409) throw error;
      const latestSettings = await requestAPI("/api/settings");
      runtimeSettings = latestSettings?.runtimeSettings || {};
      state.settings = { ...(state.settings || {}), runtimeSettings };
      if (normalizeDefaultReasoningEffort(runtimeSettings.defaultReasoningEffort) === desired) return runtimeSettings;
      if (Math.max(0, Math.trunc(Number(runtimeSettings?.revision) || 0)) < 1) throw error;
      request = runtimeReasoningSettingsRequest(desired, runtimeSettings);
      return api(request.path, request.options);
    }
  }

  async function saveAgentModelSettings(form) {
    const settingsState = agentModelSettingsState();
    if (settingsState.saving) return;
    const draft = readAgentModelSettingsForm(form);
    validateAgentModelSettings(draft);
    const payload = agentModelSettingsPayload(draft);
    settingsState.draft = draft;
    settingsState.dirty = true;
    settingsState.saving = true;
    settingsState.result = null;
    refreshActiveSettingsPanel?.();
    try {
      const response = await requestAPI(state.settings?.agentModelSettingsEndpoint || "/api/runtime/agent-model-settings", {
        method: "PATCH",
        body: JSON.stringify(payload),
      });
      const savedAgent = response?.agent || payload;
      state.settings = { ...(state.settings || {}), agent: { ...(state.settings?.agent || {}), ...savedAgent } };
      const savedRuntime = await persistDefaultReasoningEffort(draft.defaultReasoningEffort);
      state.settings = { ...(state.settings || {}), runtimeSettings: savedRuntime };
      const saved = normalizeAgentModelSettings({ ...savedAgent, defaultReasoningEffort: savedRuntime?.defaultReasoningEffort || draft.defaultReasoningEffort });
      state.agentModelSettings = {
        draft: saved,
        sourceSignature: JSON.stringify(saved),
        dirty: false,
        saving: false,
        result: { tone: "success", message: mt("routing.savedMessage") },
      };
      renderModelOptions();
      notifyTerminal?.(`[info] ${mt("routing.savedMessage")}\n`);
    } catch (error) {
      const latest = agentModelSettingsState();
      latest.saving = false;
      latest.result = { tone: "attention", message: mt("routing.saveFailed", { message: error?.message || mt("unknown") }) };
      throw error;
    } finally {
      if (state.agentModelSettings) state.agentModelSettings.saving = false;
      refreshActiveSettingsPanel?.();
    }
  }

  function resetAgentModelSettings() {
    const draft = agentModelSettingsSource();
    state.agentModelSettings = {
      draft,
      sourceSignature: JSON.stringify(draft),
      dirty: false,
      saving: false,
      result: null,
    };
    refreshActiveSettingsPanel?.();
  }

  function modelAggregateByName(name) {
    return normalizeModelAggregateList(state.modelAggregates).find((aggregate) => aggregate.name === String(name || "")) || null;
  }

  function openModelAggregateEditor(name = "") {
    const aggregate = modelAggregateByName(name);
    state.modelAggregateEditor = aggregate
      ? { mode: "edit", name: aggregate.name, members: [...aggregate.members], revision: aggregate.revision }
      : { mode: "create", name: "", members: [], revision: 0 };
    refreshActiveSettingsPanel?.();
  }

  function cancelModelAggregateEditor() {
    state.modelAggregateEditor = null;
    refreshActiveSettingsPanel?.();
  }

  function readModelAggregateForm(form) {
    const name = String(form?.elements?.aggregateName?.value || "").trim();
    const members = modelAggregateMembers(form?.elements?.aggregateMembers?.value || "");
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,119}$/.test(name)) throw new Error(mt("routing.aggregateNameInvalid"));
    if (!members.length) throw new Error(mt("routing.aggregateMembersRequired"));
    if (new Set(members).size !== members.length) throw new Error(mt("routing.aggregateMembersUnique"));
    if (members.some((member) => !/^[^:\s]+:.+$/.test(member) || member.toLowerCase().startsWith("aggregate:"))) throw new Error(mt("routing.aggregateMembersInvalid"));
    return { name, members };
  }

  async function saveModelAggregate(form) {
    if (state.modelAggregateBusy) return;
    const values = readModelAggregateForm(form);
    const current = state.modelAggregateEditor || {};
    const editing = current.mode === "edit";
    const aggregate = editing ? modelAggregateByName(current.name) || current : { revision: 0 };
    state.modelAggregateEditor = { mode: editing ? "edit" : "create", name: values.name, members: values.members, revision: Math.max(0, Math.trunc(Number(aggregate.revision) || 0)) };
    state.modelAggregateBusy = true;
    refreshActiveSettingsPanel?.();
    try {
      const request = modelAggregateActionRequest("save", aggregate, { ...values, revision: aggregate.revision || 0 });
      const response = await requestAPI(request.path, request.options);
      const saved = normalizeModelAggregateList([response])[0];
      const remaining = normalizeModelAggregateList(state.modelAggregates).filter((item) => item.name !== saved.name);
      state.modelAggregates = [...remaining, saved].sort((left, right) => left.name.localeCompare(right.name));
      state.modelAggregatesLoaded = true;
      state.modelAggregatesError = "";
      state.modelAggregateEditor = null;
      notifyTerminal?.(`[info] ${mt("routing.aggregateSavedMessage", { name: saved.name })}\n`);
    } catch (error) {
      if (error?.status === 409) {
        await loadModelAggregates({ force: true });
        const latest = modelAggregateByName(values.name);
        state.modelAggregateEditor = { mode: latest ? "edit" : "create", name: values.name, members: values.members, revision: latest?.revision || 0 };
        throw new Error(mt("routing.aggregateConflict"));
      }
      throw error;
    } finally {
      state.modelAggregateBusy = false;
      refreshActiveSettingsPanel?.();
    }
  }

  async function deleteModelAggregate(name) {
    if (state.modelAggregateBusy) return;
    const aggregate = modelAggregateByName(name);
    if (!aggregate) return;
    if (!(await platformConfirm(mt("routing.deleteAggregateConfirm", { name: aggregate.name })))) return;
    state.modelAggregateBusy = true;
    refreshActiveSettingsPanel?.();
    try {
      const request = modelAggregateActionRequest("delete", aggregate, { revision: aggregate.revision });
      await requestAPI(request.path, request.options);
      state.modelAggregates = normalizeModelAggregateList(state.modelAggregates).filter((item) => item.name !== aggregate.name);
      state.modelAggregatesLoaded = true;
      if (state.modelAggregateEditor?.name === aggregate.name) state.modelAggregateEditor = null;
      notifyTerminal?.(`[info] ${mt("routing.aggregateDeletedMessage", { name: aggregate.name })}\n`);
    } catch (error) {
      if (error?.status === 409) {
        await loadModelAggregates({ force: true });
        throw new Error(mt("routing.aggregateConflict"));
      }
      throw error;
    } finally {
      state.modelAggregateBusy = false;
      refreshActiveSettingsPanel?.();
    }
  }

  function bindModelSettingsActions() {
    $("settingsRefreshModelsBtn")?.addEventListener("click", () => refreshModelCatalog().catch(showError));
    $("settingsOpenLoginBtn")?.addEventListener("click", () => openSettingsModal?.("providers"));
    $("settingsClearPreferredModelBtn")?.addEventListener("click", () => applyPreferredModel("").catch(showError));
    $("settingsShowConfiguredModelsBtn")?.addEventListener("click", clearVisibleConfiguredModelHides);
    $("resetAgentModelSettingsBtn")?.addEventListener("click", resetAgentModelSettings);
    const root = $("settingsContentBody");
    const form = $("agentModelSettingsForm");
    form?.addEventListener("submit", (event) => {
      event.preventDefault();
      saveAgentModelSettings(form).catch(showError);
    });
    form?.addEventListener("change", (event) => handleAgentModelSettingsChange(event, form));
    root?.querySelectorAll("[data-toggle-model-visibility]").forEach((node) => {
      node.addEventListener("click", () => setModelHidden(node.dataset.toggleModelVisibility, !isModelHidden(node.dataset.toggleModelVisibility)));
    });
    root?.querySelectorAll("[data-apply-model]").forEach((node) => {
      node.addEventListener("click", () => applyPreferredModel(node.dataset.applyModel).catch(showError));
    });
    root?.querySelector("[data-model-aggregate-add]")?.addEventListener("click", () => openModelAggregateEditor());
    root?.querySelectorAll("[data-model-aggregate-edit]").forEach((node) => node.addEventListener("click", () => openModelAggregateEditor(node.dataset.modelAggregateEdit)));
    root?.querySelectorAll("[data-model-aggregate-delete]").forEach((node) => node.addEventListener("click", () => deleteModelAggregate(node.dataset.modelAggregateDelete).catch(showError)));
    root?.querySelector("[data-model-aggregate-cancel]")?.addEventListener("click", cancelModelAggregateEditor);
    const aggregateForm = $("modelAggregateForm");
    aggregateForm?.addEventListener("submit", (event) => {
      event.preventDefault();
      saveModelAggregate(aggregateForm).catch(showError);
    });
    loadModelAggregates().catch(showError);
  }

  return {
    loadModelAggregates,
    renderModelSettingsContent,
    bindModelSettingsActions,
  };
}
