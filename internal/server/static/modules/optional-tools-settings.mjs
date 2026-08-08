import { escapeHtml } from "./dom.mjs";

function text(value) {
  return String(value ?? "").trim();
}

function normalizeTarget(target = {}) {
  const scope = text(target.scope) || "global";
  const projectId = text(target.projectId);
  const workspaceId = text(target.workspaceId);
  if (scope === "global") return { scope, projectId: "", workspaceId: "" };
  if (scope === "project") return { scope, projectId, workspaceId: "" };
  return { scope: "workspace", projectId, workspaceId };
}

function targetQuery(target, extra = {}) {
  const normalized = normalizeTarget(target);
  const values = new URLSearchParams({ scope: normalized.scope });
  if (normalized.projectId) values.set("projectId", normalized.projectId);
  if (normalized.workspaceId) values.set("workspaceId", normalized.workspaceId);
  Object.entries(extra).forEach(([key, value]) => values.set(key, String(value)));
  return values.toString();
}

function listFrom(payload, key) {
  if (Array.isArray(payload)) return payload;
  return Array.isArray(payload?.[key]) ? payload[key] : [];
}

export function normalizeOptionalToolsSettings(catalogPayload = {}, rulesPayload = {}, effectivePayload = {}) {
  const catalog = listFrom(catalogPayload, "tools");
  const rules = listFrom(rulesPayload, "rules");
  const effective = listFrom(effectivePayload, "tools");
  const byName = new Map();

  for (const item of catalog) {
    const name = text(item?.name || item?.toolName);
    if (!name || byName.has(name)) continue;
    byName.set(name, {
      name,
      displayName: text(item?.displayName) || name,
      description: text(item?.description),
      domain: text(item?.domain) || "core",
      source: text(item?.source) || "core",
      sourceId: text(item?.sourceId),
      orphan: false,
    });
  }

  const ruleByName = new Map();
  for (const rule of rules) {
    const name = text(rule?.toolName);
    const revision = Number(rule?.revision || 0);
    if (!name || !Number.isSafeInteger(revision) || revision < 1) continue;
    const existing = ruleByName.get(name);
    if (!existing || revision > existing.revision) {
      ruleByName.set(name, {
        id: text(rule?.id),
        toolName: name,
        state: rule?.state === "disabled" ? "disabled" : "enabled",
        revision,
        deleted: Boolean(text(rule?.deletedAt)),
        orphan: Boolean(rule?.orphan),
      });
    }
  }

  const effectiveByName = new Map();
  for (const item of effective) {
    const name = text(item?.name || item?.toolName);
    if (!name) continue;
    effectiveByName.set(name, {
      enabled: item?.enabled !== false,
      state: item?.state === "disabled" ? "disabled" : "enabled",
      sourceScope: text(item?.sourceScope),
      sourceRuleId: text(item?.sourceRuleId),
      sourceRevision: Number(item?.sourceRevision || 0),
      default: Boolean(item?.default),
      orphan: Boolean(item?.orphan),
      domain: text(item?.domain),
      displayName: text(item?.displayName),
      description: text(item?.description),
    });
    if (!byName.has(name)) {
      byName.set(name, {
        name,
        displayName: text(item?.displayName) || name,
        description: text(item?.description),
        domain: text(item?.domain) || "orphan",
        source: text(item?.source) || "orphan",
        sourceId: "",
        orphan: true,
      });
    }
  }

  for (const [name, rule] of ruleByName) {
    if (!byName.has(name)) {
      byName.set(name, {
        name,
        displayName: name,
        description: "",
        domain: "orphan",
        source: "orphan",
        sourceId: "",
        orphan: true,
      });
    }
    if (rule.orphan) byName.get(name).orphan = true;
  }

  return [...byName.values()].map((item) => {
    const rule = ruleByName.get(item.name);
    const resolved = effectiveByName.get(item.name) || { enabled: true, state: "enabled", default: true };
    return {
      ...item,
      domain: resolved.domain || item.domain,
      displayName: resolved.displayName || item.displayName,
      description: resolved.description || item.description,
      orphan: item.orphan || resolved.orphan || false,
      selection: rule && !rule.deleted ? rule.state : "inherit",
      ruleId: rule?.id || "",
      ruleRevision: rule?.revision || 0,
      ruleDeleted: Boolean(rule?.deleted),
      effectiveEnabled: resolved.enabled !== false,
      effectiveState: resolved.state,
      effectiveSourceScope: resolved.sourceScope || "default",
      effectiveDefault: Boolean(resolved.default),
    };
  }).sort((a, b) => a.displayName.localeCompare(b.displayName, undefined, { sensitivity: "base" }));
}

export function groupOptionalTools(items = [], search = "") {
  const query = text(search).toLocaleLowerCase();
  const filtered = items.filter((item) => !query || [item.name, item.displayName, item.description, item.domain, item.source]
    .some((value) => String(value || "").toLocaleLowerCase().includes(query)));
  const groups = new Map();
  for (const item of filtered) {
    const domain = text(item.domain) || "core";
    if (!groups.has(domain)) groups.set(domain, []);
    groups.get(domain).push(item);
  }
  return [...groups.entries()]
    .sort(([left], [right]) => left.localeCompare(right, undefined, { sensitivity: "base" }))
    .map(([domain, tools]) => ({ domain, tools }));
}

function renderOptionalToolCard(item, copy) {
  const displayName = text(item.displayName) || item.name;
  const distinctName = displayName !== item.name;
  const effectiveLabel = item.effectiveEnabled ? copy.enabled : copy.disabled;
  return `<article class="optional-tool-row" data-tool-name="${escapeHtml(item.name)}">
    <div class="optional-tool-main"><div class="optional-tool-title"><strong>${escapeHtml(displayName)}</strong>${distinctName ? `<code>${escapeHtml(item.name)}</code>` : ""}</div>${item.orphan ? `<small>${escapeHtml(copy.orphan)}</small>` : ""}</div>
    <div class="optional-tool-actions"><span class="settings-badge ${item.effectiveEnabled ? "success" : "warning"}">${escapeHtml(effectiveLabel)}</span><select data-optional-tool-state="${escapeHtml(item.name)}" aria-label="${escapeHtml(`${copy.effective}: ${displayName}`)}">
      <option value="inherit"${item.selection === "inherit" ? " selected" : ""}>${escapeHtml(copy.inherit)}</option>
      <option value="enabled"${item.selection === "enabled" ? " selected" : ""}>${escapeHtml(copy.enabled)}</option>
      <option value="disabled"${item.selection === "disabled" ? " selected" : ""}>${escapeHtml(copy.disabled)}</option>
    </select></div>
  </article>`;
}

export function createOptionalToolsSettingsController({
  request,
  refresh,
  showError,
  basePath = "/api/optional-tools",
  target = { scope: "global" },
  labels = {},
} = {}) {
  let currentTarget = normalizeTarget(target);
  let items = [];
  let search = "";
  let loading = false;
  let loaded = false;
  let error = "";
  let loadSequence = 0;
  const mutationSequences = new Map();

  const copy = {
    loading: labels.loading || "Loading optional tools…",
    empty: labels.empty || "No optional tools match this search.",
    search: labels.search || "Search tools",
    inherit: labels.inherit || "Inherit",
    enabled: labels.enabled || "Enabled",
    disabled: labels.disabled || "Disabled",
    effective: labels.effective || "Effective",
    orphan: labels.orphan || "Unavailable catalog entry",
    forbidden: labels.forbidden || "This session is not allowed to manage optional tools (403).",
  };

  async function load() {
    const sequence = ++loadSequence;
    loading = true;
    error = "";
    refresh?.();
    const query = targetQuery(currentTarget);
    try {
      const [catalog, rules, effective] = await Promise.all([
        request(`${basePath}/catalog`),
        request(`${basePath}/rules?${targetQuery(currentTarget, { includeDeleted: true })}`),
        request(`${basePath}/effective?${query}`),
      ]);
      if (sequence !== loadSequence) return false;
      items = normalizeOptionalToolsSettings(catalog, rules, effective);
      loaded = true;
      return true;
    } catch (cause) {
      if (sequence !== loadSequence) return false;
      error = cause?.status === 403 ? copy.forbidden : cause?.message || String(cause);
      loaded = false;
      showError?.(cause);
      return false;
    } finally {
      if (sequence === loadSequence) {
        loading = false;
        refresh?.();
      }
    }
  }

  function setSearch(value) {
    search = String(value ?? "");
    refresh?.();
  }

  async function setTarget(nextTarget) {
    currentTarget = normalizeTarget(nextTarget);
    loadSequence += 1;
    loaded = false;
    return load();
  }

  async function setRule(toolName, selection) {
    const name = text(toolName);
    if (!name || !["inherit", "enabled", "disabled"].includes(selection)) return false;
    const current = items.find((item) => item.name === name);
    if (!current) return false;
    const operation = (mutationSequences.get(name) || 0) + 1;
    mutationSequences.set(name, operation);
    try {
      if (selection === "inherit") {
        if (!current.ruleId || current.ruleDeleted) return true;
        await request(`${basePath}/rules/${encodeURIComponent(current.ruleId)}`, {
          method: "DELETE",
          body: JSON.stringify({ expectedRevision: current.ruleRevision }),
        });
      } else {
        await request(`${basePath}/rules`, {
          method: "PUT",
          body: JSON.stringify({
            toolName: name,
            scope: currentTarget.scope,
            projectId: currentTarget.projectId,
            workspaceId: currentTarget.workspaceId,
            state: selection,
            expectedRevision: current.ruleRevision,
          }),
        });
      }
      if (mutationSequences.get(name) !== operation) return false;
      return load();
    } catch (cause) {
      if (mutationSequences.get(name) === operation) showError?.(cause);
      return false;
    }
  }

  function render() {
    if (loading && !loaded) return `<div class="settings-empty-state">${escapeHtml(copy.loading)}</div>`;
    if (error) return `<div class="settings-inline-alert" role="alert">${escapeHtml(error)}</div>`;
    const groups = groupOptionalTools(items, search);
    return `<div class="optional-tools-settings">
      <label class="optional-tools-search"><span class="optional-tools-search-label">${escapeHtml(copy.search)}</span><input class="optional-tools-search-input" type="search" data-optional-tools-search aria-label="${escapeHtml(copy.search)}" value="${escapeHtml(search)}"></label>
      ${groups.length ? groups.map(({ domain, tools: grouped }) => `<section class="settings-card optional-tools-domain" data-tool-domain="${escapeHtml(domain)}">
        <div class="settings-card-header"><strong>${escapeHtml(domain)}</strong><span>${grouped.length}</span></div>
        <div class="optional-tools-list">${grouped.map((item) => renderOptionalToolCard(item, copy)).join("")}</div>
      </section>`).join("") : `<div class="settings-empty-state">${escapeHtml(copy.empty)}</div>`}
    </div>`;
  }

  function bind(root = globalThis.document) {
    root?.querySelector?.("[data-optional-tools-search]")?.addEventListener("input", (event) => setSearch(event.target.value));
    root?.querySelectorAll?.("[data-optional-tool-state]")?.forEach((select) => {
      select.addEventListener("change", () => setRule(select.dataset.optionalToolState, select.value));
    });
  }

  function snapshot() {
    return { target: { ...currentTarget }, items: items.map((item) => ({ ...item })), search, loading, loaded, error };
  }

  return { bind, group: () => groupOptionalTools(items, search), load, render, setRule, setSearch, setTarget, snapshot };
}
