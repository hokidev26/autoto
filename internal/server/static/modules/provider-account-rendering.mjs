import { escapeAttr, escapeHtml } from "./dom.mjs";
import { formatMoney, formatNumber, formatTimestamp } from "./formatters.mjs";
import { t } from "./i18n.mjs?v=provider-draft-session-1-codex-quota-exhausted-1";
import { normalizeCodexSelectedIds } from "./provider-settings-normalization.mjs";

function codexQuotaUnauthorizedIsCurrent(account) {
  const stats = account?.stats && typeof account.stats === "object" ? account.stats : null;
  if (!stats) return false;
  const errorCode = String(stats.last_error_code ?? stats.lastErrorCode ?? "").trim().toLowerCase();
  if (errorCode !== "quota_unauthorized") return false;
  const attemptedAt = Date.parse(String(stats.last_attempt_at ?? stats.lastAttemptAt ?? ""));
  const fetchedAt = Date.parse(String(stats.quota_fetched_at ?? stats.quotaFetchedAt ?? ""));
  return !Number.isFinite(fetchedAt) || !Number.isFinite(attemptedAt) || attemptedAt > fetchedAt;
}

export function codexAccountStatus(account = {}, { now = Date.now() } = {}) {
  const quota = account?.quota && typeof account.quota === "object" ? account.quota : null;
  const expiresAt = String(account?.expires_at || account?.expiresAt || "").trim();
  const expiresAtMs = expiresAt ? Date.parse(expiresAt) : Number.NaN;
  const expired = Number.isFinite(expiresAtMs) && expiresAtMs <= now && !Boolean(account?.refreshable);
  if (Boolean(account?.disabled)) return { key: "disabled", tone: "muted", expiresAt };
  if (expired) return { key: "expired", tone: "danger", expiresAt };
  if (codexQuotaUnauthorizedIsCurrent(account)) return { key: "exhausted", tone: "danger", expiresAt };
  if (codexQuotaIsLimited(quota)) return { key: "rateLimited", tone: "warn", expiresAt };
  return { key: "available", tone: "ok", expiresAt };
}

export function codexAccountOverview(accounts, { now = Date.now() } = {}) {
  const overview = { total: 0, available: 0, rateLimited: 0, exhausted: 0, disabled: 0, expired: 0 };
  for (const account of Array.isArray(accounts) ? accounts : []) {
    overview.total += 1;
    const status = codexAccountStatus(account, { now });
    if (Object.hasOwn(overview, status.key)) overview[status.key] += 1;
  }
  return overview;
}

export function anthropicAccountStatus(account = {}) {
  if (Boolean(account?.disabled)) return { key: "disabled", tone: "muted" };
  const limit = account?.rate_limit ?? account?.rateLimit ?? account?.quota;
  if (anthropicRateLimitReached(limit)) return { key: "rateLimited", tone: "warn" };
  if (account?.configured === false) return { key: "unconfigured", tone: "warn" };
  return { key: "available", tone: "ok" };
}

export function anthropicAccountOverview(accounts) {
  const overview = { total: 0, available: 0, rateLimited: 0, disabled: 0 };
  for (const account of Array.isArray(accounts) ? accounts : []) {
    overview.total += 1;
    const status = anthropicAccountStatus(account);
    if (Object.hasOwn(overview, status.key)) overview[status.key] += 1;
  }
  return overview;
}
export function codexAccountStableID(account) {
  return String(account?.id || account?.auth_index || account?.authIndex || account?.name || "").trim();
}

function renderCodexBatchToolbar(items, selectedIds, mt, { batchBusy = false, batchPriority = 100 } = {}) {
  const selected = normalizeCodexSelectedIds(selectedIds, items);
  const selectedCount = selected.length;
  const disabled = batchBusy || selectedCount === 0;
  const disabledAttributes = disabled ? " disabled" : "";
  return `<div class="codex-batch-toolbar" aria-busy="${batchBusy ? "true" : "false"}">
    <div class="codex-batch-selection">
      <strong>${escapeHtml(mt("selectedAccounts", { count: selectedCount }))}</strong>
      <button type="button" class="codex-batch-link" data-codex-select-all-accounts${batchBusy ? " disabled" : ""}>${escapeHtml(mt("selectAllAccounts"))}</button>
      <span aria-hidden="true">·</span>
      <button type="button" class="codex-batch-link" data-codex-clear-selection${disabledAttributes}>${escapeHtml(mt("clearSelection"))}</button>
    </div>
    <div class="codex-batch-actions" role="group" aria-label="${escapeAttr(mt("batchActions"))}">
      <button class="settings-action-btn" type="button" data-codex-batch-sync${disabledAttributes}>${escapeHtml(mt("batchSync"))}</button>
      <button class="settings-action-btn" type="button" data-codex-batch-enable${disabledAttributes}>${escapeHtml(mt("batchEnable"))}</button>
      <button class="settings-action-btn" type="button" data-codex-batch-disable${disabledAttributes}>${escapeHtml(mt("batchDisable"))}</button>
      <label class="codex-batch-priority"><span>${escapeHtml(mt("batchPriority"))}</span><input class="settings-text-input" type="number" min="1" max="1000000" step="1" value="${escapeAttr(batchPriority)}" data-codex-batch-priority${disabledAttributes}></label>
      <button class="settings-action-btn" type="button" data-codex-batch-priority-apply${disabledAttributes}>${escapeHtml(mt("apply"))}</button>
      <button class="settings-action-btn danger" type="button" data-codex-batch-delete${disabledAttributes}>${escapeHtml(mt("batchDelete"))}</button>
    </div>
  </div>`;
}

export function renderCodexAccountManagementTable(accounts, {
  translate = (key, params) => t(`modelProvider.${key}`, params),
  now = Date.now(),
  editing = null,
  busy = {},
  selectedIds = [],
  batchBusy = false,
  batchPriority = 100,
} = {}) {
  const mt = translate;
  const items = Array.isArray(accounts) ? accounts : [];
  if (!items.length) return `<div class="mp-console-empty" role="status">${escapeHtml(mt("noCodexCredentials"))}</div>`;
  const selected = normalizeCodexSelectedIds(selectedIds, items);
  const selectedSet = new Set(selected);
  const allSelected = items.every((account) => selectedSet.has(codexAccountStableID(account)));
  return `<div class="codex-account-management">
    ${renderCodexBatchToolbar(items, selected, mt, { batchBusy, batchPriority })}
    <div class="codex-account-table-wrap settings-card-content">
      <table class="codex-account-table codex-oauth-account-table" aria-label="${escapeAttr(mt("importedCredentials"))}">
        <thead><tr>
          <th scope="col" class="codex-account-select-heading"><input type="checkbox" data-codex-select-all aria-label="${escapeAttr(mt("selectAllAccounts"))}" ${allSelected ? "checked" : ""}${batchBusy ? " disabled" : ""}></th>
          <th scope="col">${escapeHtml(mt("accountName"))}</th><th scope="col">${escapeHtml(mt("accountId"))}</th><th scope="col">${escapeHtml(mt("priority"))}</th><th scope="col">${escapeHtml(mt("status"))}</th>
          <th scope="col">${escapeHtml(mt("successFailure"))}</th><th scope="col">${escapeHtml(mt("usage"))}</th><th scope="col">${escapeHtml(mt("lastUsed"))}</th><th scope="col">${escapeHtml(mt("actions"))}</th>
        </tr></thead>
        <tbody>${items.map((account) => renderCodexAccountRow(account, mt, now, editing, busy, { selected: selectedSet.has(codexAccountStableID(account)), batchBusy })).join("")}</tbody>
      </table>
    </div>
  </div>`;
}

export function renderAnthropicAccountManagementTable(accounts, {
  translate = (key, params) => t(`modelProvider.${key}`, params),
  editing = null,
  busy = {},
} = {}) {
  const mt = translate;
  const items = Array.isArray(accounts) ? accounts : [];
  if (!items.length) return `<div class="mp-console-empty" role="status">${escapeHtml(mt("anthropic.noAccounts"))}</div>`;
  return `<div class="codex-account-table-wrap anthropic-account-table-wrap settings-card-content">
    <table class="codex-account-table anthropic-account-table" aria-label="${escapeAttr(mt("anthropic.accountsTitle"))}">
      <thead><tr>
        <th scope="col">${escapeHtml(mt("accountName"))}</th><th scope="col">${escapeHtml(mt("anthropic.authType"))}</th><th scope="col">${escapeHtml(mt("priority"))}</th><th scope="col">${escapeHtml(mt("status"))}</th>
        <th scope="col">${escapeHtml(mt("successFailure"))}</th><th scope="col">${escapeHtml(mt("usage"))}</th><th scope="col">${escapeHtml(mt("lastUsed"))}</th><th scope="col">${escapeHtml(mt("actions"))}</th>
      </tr></thead>
      <tbody>${items.map((account) => renderAnthropicAccountRow(account, mt, editing, busy)).join("")}</tbody>
    </table>
  </div>`;
}

function renderAnthropicAccountRow(account, mt, editing, busy) {
  const id = String(account?.id || "");
  const alias = String(account?.alias || "");
  const priority = finiteNumber(account?.priority, 100);
  const disabled = Boolean(account?.disabled);
  const managed = account?.managed !== false;
  const isEditing = managed && editing?.id === id;
  const isBusy = managed && Boolean(busy?.[id]);
  const authType = String(account?.auth_type || account?.authType || "profile").toLowerCase();
  const profile = String(account?.profile || "");
  const source = managed ? String(account?.source || "") : mt("anthropic.existingConfigSource");
  const fallbackName = profile || source || id || mt("unknown");
  const displayName = alias || fallbackName;
  const secondaryName = [alias && fallbackName !== alias ? fallbackName : "", source && source !== fallbackName ? source : ""].filter(Boolean).join(" · ");
  const stats = account?.stats && typeof account.stats === "object" ? account.stats : {};
  const success = Math.max(0, finiteNumber(stats.success_count ?? stats.successCount, 0));
  const failure = Math.max(0, finiteNumber(stats.failure_count ?? stats.failureCount, 0));
  const lastUsed = String(stats.last_use_at || stats.lastUseAt || stats.last_used_at || stats.lastUsedAt || stats.last_attempt_at || stats.lastAttemptAt || "");
  const status = anthropicAccountStatus(account);
  const disabledAttributes = isBusy ? ` disabled aria-busy="true"` : "";
  const editAlias = String(isEditing ? editing.alias ?? alias : alias);
  const editPriority = finiteNumber(isEditing ? editing.priority : priority, priority);
  const modelCount = Array.isArray(account?.models) ? account.models.filter(Boolean).length : 0;
  return `<tr data-anthropic-account-row="${escapeAttr(id)}" class="${isEditing ? "is-editing" : ""}" aria-busy="${isBusy ? "true" : "false"}">
    <td data-label="${escapeAttr(mt("accountName"))}">${isEditing
      ? `<label class="codex-inline-edit-field"><span class="mp-visually-hidden">${escapeHtml(mt("accountName"))}</span><input class="codex-account-alias settings-text-input settings-form-field" value="${escapeAttr(editAlias)}" placeholder="${escapeAttr(fallbackName)}" maxlength="200" data-anthropic-edit-alias="${escapeAttr(id)}" data-select-on-focus="true"${disabledAttributes}></label>`
      : `<strong class="codex-account-name">${escapeHtml(displayName)}</strong>`}${secondaryName ? `<div class="codex-account-secondary">${escapeHtml(secondaryName)}</div>` : ""}${modelCount ? `<div class="codex-account-secondary">${escapeHtml(mt("anthropic.modelCount", { count: modelCount }))}</div>` : ""}</td>
    <td data-label="${escapeAttr(mt("anthropic.authType"))}"><span class="settings-badge">${escapeHtml(mt(authType === "api_key" ? "anthropic.apiKeyAuth" : "anthropic.profileAuth"))}</span></td>
    <td data-label="${escapeAttr(mt("priority"))}">${isEditing
      ? `<label class="codex-inline-edit-field"><span class="mp-visually-hidden">${escapeHtml(mt("priority"))}</span><input class="codex-priority-input settings-text-input settings-form-field" type="number" min="1" max="1000000" step="1" value="${escapeAttr(editPriority)}" data-anthropic-edit-priority="${escapeAttr(id)}" data-select-on-focus="true"${disabledAttributes}></label>`
      : `<span class="codex-priority-value">${escapeHtml(String(priority))}</span>`}</td>
    <td data-label="${escapeAttr(mt("status"))}"><span class="settings-status-pill settings-badge ${escapeAttr(status.tone)}">${escapeHtml(mt(status.key))}</span></td>
    <td data-label="${escapeAttr(mt("successFailure"))}"><span class="codex-success-count">${escapeHtml(String(success))}</span> / <span class="codex-failure-count">${escapeHtml(String(failure))}</span></td>
    <td data-label="${escapeAttr(mt("usage"))}">${renderAnthropicQuota(account?.quota ?? account?.rate_limit ?? account?.rateLimit, mt)}</td>
    <td data-label="${escapeAttr(mt("lastUsed"))}">${escapeHtml(lastUsed ? formatCodexTimestamp(lastUsed) : mt("never"))}</td>
    <td data-label="${escapeAttr(mt("actions"))}"><div class="codex-account-actions settings-inline-actions" role="group" aria-label="${escapeAttr(mt("accountActions", { account: displayName }))}">
      ${!managed ? `<span class="settings-badge muted anthropic-readonly-badge">${escapeHtml(mt("anthropic.readOnly"))}</span>` : isEditing ? `<button class="codex-icon-action save" type="button" data-anthropic-save="${escapeAttr(id)}" aria-label="${escapeAttr(mt("saveAccount"))}" title="${escapeAttr(mt("saveAccount"))}"${disabledAttributes}>${codexActionIcon("save")}<span>${escapeHtml(mt("save"))}</span></button><button class="codex-icon-action cancel" type="button" data-anthropic-edit-cancel="${escapeAttr(id)}" aria-label="${escapeAttr(mt("cancelEdit"))}" title="${escapeAttr(mt("cancelEdit"))}"${disabledAttributes}>${codexActionIcon("cancel")}<span>${escapeHtml(mt("cancel"))}</span></button>` : `<button class="codex-icon-action edit" type="button" data-anthropic-edit="${escapeAttr(id)}" aria-label="${escapeAttr(mt("editAccount"))}" title="${escapeAttr(mt("editAccount"))}"${disabledAttributes}>${codexActionIcon("edit")}<span>${escapeHtml(mt("edit"))}</span></button><button class="codex-icon-action sync" type="button" data-anthropic-sync="${escapeAttr(id)}" aria-label="${escapeAttr(mt("syncAccount"))}" title="${escapeAttr(mt("syncAccount"))}"${disabledAttributes}>${codexActionIcon("sync")}<span>${escapeHtml(mt("sync"))}</span></button><button class="codex-icon-action toggle" type="button" data-anthropic-toggle="${escapeAttr(id)}" data-disabled="${disabled ? "true" : "false"}" aria-label="${escapeAttr(disabled ? mt("enableAccount") : mt("disableAccount"))}" title="${escapeAttr(disabled ? mt("enableAccount") : mt("disableAccount"))}"${disabledAttributes}>${codexActionIcon(disabled ? "enable" : "disable")}<span>${escapeHtml(disabled ? mt("enable") : mt("disable"))}</span></button><button class="codex-icon-action delete" type="button" data-anthropic-delete="${escapeAttr(id)}" aria-label="${escapeAttr(mt("deleteAccount"))}" title="${escapeAttr(mt("deleteAccount"))}"${disabledAttributes}>${codexActionIcon("delete")}<span>${escapeHtml(mt("delete"))}</span></button>`}
    </div></td>
  </tr>`;
}

function renderCodexAccountRow(account, mt, now, editing, busy, { selected = false, batchBusy = false } = {}) {
  const id = codexAccountStableID(account);
  const alias = String(account?.alias || "");
  const priority = finiteNumber(account?.priority, 100);
  const disabled = Boolean(account?.disabled);
  const isEditing = editing?.id === id;
  const isBusy = batchBusy || Boolean(busy?.[id]);
  const stats = account?.stats && typeof account.stats === "object" ? account.stats : {};
  const quota = account?.quota && typeof account.quota === "object" ? account.quota : null;
  const plan = String(quota?.plan_type || quota?.planType || account?.plan_type || account?.planType || "").trim();
  const status = codexAccountStatus(account, { now });
  const accountLabel = String(account?.account_id || account?.accountID || id || mt("unknown"));
  const fallbackName = String(account?.email || account?.name || mt("unknown"));
  const displayName = alias || fallbackName;
  const success = Math.max(0, finiteNumber(stats.success_count ?? stats.successCount, 0));
  const failure = Math.max(0, finiteNumber(stats.failure_count ?? stats.failureCount, 0));
  const lastUsed = String(stats.last_use_at || stats.lastUseAt || stats.last_attempt_at || stats.lastAttemptAt || "");
  const disabledAttributes = isBusy ? ` disabled aria-busy="true"` : "";
  const secondaryName = alias && fallbackName !== alias ? fallbackName : "";
  const editAlias = String(isEditing ? editing.alias ?? alias : alias);
  const editPriority = finiteNumber(isEditing ? editing.priority : priority, priority);
  return `<tr data-codex-account-row="${escapeAttr(id)}" class="${[isEditing ? "is-editing" : "", selected ? "is-selected" : ""].filter(Boolean).join(" ")}" aria-busy="${isBusy ? "true" : "false"}">
    <td class="codex-account-select-cell" data-label="${escapeAttr(mt("selectAccount"))}"><input type="checkbox" data-codex-select="${escapeAttr(id)}" aria-label="${escapeAttr(mt("selectAccountNamed", { account: displayName }))}" ${selected ? "checked" : ""}${isBusy ? " disabled" : ""}></td>
    <td data-label="${escapeAttr(mt("accountName"))}">
      ${isEditing
        ? `<label class="codex-inline-edit-field"><span class="mp-visually-hidden">${escapeHtml(mt("accountName"))}</span><input class="codex-account-alias settings-text-input settings-form-field" value="${escapeAttr(editAlias)}" placeholder="${escapeAttr(fallbackName)}" maxlength="200" data-codex-edit-alias="${escapeAttr(id)}" data-select-on-focus="true"${disabledAttributes}></label>`
        : `<div class="codex-account-name-row"><strong class="codex-account-name">${escapeHtml(displayName)}</strong>${plan ? `<span class="codex-plan-badge settings-badge">${escapeHtml(plan)}</span>` : ""}</div>`}
      ${secondaryName ? `<div class="codex-account-secondary">${escapeHtml(secondaryName)}</div>` : ""}
    </td>
    <td data-label="${escapeAttr(mt("accountId"))}"><code class="codex-account-id">${escapeHtml(accountLabel)}</code></td>
    <td data-label="${escapeAttr(mt("priority"))}">${isEditing
      ? `<label class="codex-inline-edit-field"><span class="mp-visually-hidden">${escapeHtml(mt("priority"))}</span><input class="codex-priority-input settings-text-input settings-form-field" type="number" min="1" max="1000000" step="1" value="${escapeAttr(editPriority)}" data-codex-edit-priority="${escapeAttr(id)}" data-select-on-focus="true"${disabledAttributes}></label>`
      : `<span class="codex-priority-value">${escapeHtml(String(priority))}</span>`}</td>
    <td data-label="${escapeAttr(mt("status"))}"><span class="settings-status-pill settings-badge ${escapeAttr(status.tone)}">${escapeHtml(mt(status.key))}</span></td>
    <td data-label="${escapeAttr(mt("successFailure"))}"><span class="codex-success-count">${escapeHtml(String(success))}</span> / <span class="codex-failure-count">${escapeHtml(String(failure))}</span></td>
    <td data-label="${escapeAttr(mt("usage"))}">${renderCodexUsage(account, mt, now)}</td>
    <td data-label="${escapeAttr(mt("lastUsed"))}">${escapeHtml(lastUsed ? formatCodexTimestamp(lastUsed) : mt("never"))}</td>
    <td data-label="${escapeAttr(mt("actions"))}"><div class="codex-account-actions settings-inline-actions" role="group" aria-label="${escapeAttr(mt("accountActions", { account: displayName }))}">
      ${isEditing ? `
        <button class="codex-icon-action save" type="button" data-codex-save="${escapeAttr(id)}" aria-label="${escapeAttr(mt("saveAccount"))}" title="${escapeAttr(mt("saveAccount"))}"${disabledAttributes}>${codexActionIcon("save")}<span>${escapeHtml(mt("save"))}</span></button>
        <button class="codex-icon-action cancel" type="button" data-codex-edit-cancel="${escapeAttr(id)}" aria-label="${escapeAttr(mt("cancelEdit"))}" title="${escapeAttr(mt("cancelEdit"))}"${disabledAttributes}>${codexActionIcon("cancel")}<span>${escapeHtml(mt("cancel"))}</span></button>` : `
        <button class="codex-icon-action edit" type="button" data-codex-edit="${escapeAttr(id)}" aria-label="${escapeAttr(mt("editAccount"))}" title="${escapeAttr(mt("editAccount"))}"${disabledAttributes}>${codexActionIcon("edit")}<span>${escapeHtml(mt("edit"))}</span></button>
        <button class="codex-icon-action sync" type="button" data-codex-sync="${escapeAttr(id)}" aria-label="${escapeAttr(mt("syncAccount"))}" title="${escapeAttr(mt("syncAccount"))}"${disabledAttributes}>${codexActionIcon("sync")}<span>${escapeHtml(mt("sync"))}</span></button>
        <button class="codex-icon-action export" type="button" data-codex-export="${escapeAttr(id)}" aria-label="${escapeAttr(mt("exportAccountJSON"))}" title="${escapeAttr(mt("exportAccountJSON"))}"${disabledAttributes}>${codexActionIcon("export")}<span>${escapeHtml(mt("exportAccount"))}</span></button>
        <span class="codex-account-action-divider" aria-hidden="true"></span>
        <button class="codex-icon-action toggle" type="button" data-codex-toggle="${escapeAttr(id)}" data-disabled="${disabled ? "true" : "false"}" aria-label="${escapeAttr(disabled ? mt("enableAccount") : mt("disableAccount"))}" title="${escapeAttr(disabled ? mt("enableAccount") : mt("disableAccount"))}"${disabledAttributes}>${codexActionIcon(disabled ? "enable" : "disable")}<span>${escapeHtml(disabled ? mt("enable") : mt("disable"))}</span></button>
        <button class="codex-icon-action delete" type="button" data-codex-delete="${escapeAttr(id)}" aria-label="${escapeAttr(mt("deleteAccount"))}" title="${escapeAttr(mt("deleteAccount"))}"${disabledAttributes}>${codexActionIcon("delete")}<span>${escapeHtml(mt("delete"))}</span></button>`}
    </div></td>
  </tr>`;
}

function renderAnthropicQuota(value, mt) {
  if (!value || typeof value !== "object") return `<span class="codex-no-quota">${escapeHtml(mt("anthropic.noQuotaData"))}</span>`;
  const requests = value.requests && typeof value.requests === "object" ? value.requests : value;
  const buckets = [
    [mt("anthropic.quotaRequests"), requests],
    [mt("anthropic.quotaInputTokens"), value.input_tokens || value.inputTokens],
    [mt("anthropic.quotaOutputTokens"), value.output_tokens || value.outputTokens],
  ].map(([label, bucket]) => renderAnthropicQuotaBucket(label, bucket, mt)).filter(Boolean);
  const meta = [];
  const retryAfter = value.retry_after ?? value.retryAfter;
  const fetchedAt = value.fetched_at || value.fetchedAt;
  if (retryAfter !== undefined && retryAfter !== null && retryAfter !== "") meta.push(mt("anthropic.quotaRetryAfter", { time: formatAnthropicLimitTime(retryAfter, { duration: true }) }));
  if (fetchedAt) meta.push(mt("anthropic.quotaFetchedAt", { time: formatAnthropicLimitTime(fetchedAt) }));
  if (!buckets.length && !meta.length) return `<span class="codex-no-quota">${escapeHtml(mt("anthropic.noQuotaData"))}</span>`;
  return `<div class="codex-quota-stack anthropic-quota-stack">${buckets.join("")}${meta.map((row) => `<div class="codex-quota-meta">${escapeHtml(row)}</div>`).join("")}</div>`;
}

function renderAnthropicQuotaBucket(label, bucket, mt) {
  if (!bucket || typeof bucket !== "object") return "";
  const remainingValue = bucket.remaining ?? bucket.remaining_requests ?? bucket.remainingRequests ?? bucket.requests_remaining ?? bucket.requestsRemaining;
  const limitValue = bucket.limit ?? bucket.request_limit ?? bucket.requestLimit ?? bucket.total;
  const usedPercentValue = bucket.used_percent ?? bucket.usedPercent;
  const resetValue = bucket.reset ?? bucket.reset_at ?? bucket.resetAt ?? bucket.resets_at ?? bucket.resetsAt;
  const hasRemaining = remainingValue !== null && remainingValue !== "" && Number.isFinite(Number(remainingValue));
  const hasLimit = limitValue !== null && limitValue !== "" && Number.isFinite(Number(limitValue));
  const hasUsedPercent = usedPercentValue !== null && usedPercentValue !== "" && Number.isFinite(Number(usedPercentValue));
  if (!hasRemaining && !hasLimit && !hasUsedPercent && (resetValue === undefined || resetValue === null || resetValue === "")) return "";
  const rows = [];
  if (hasRemaining) rows.push(mt("anthropic.quotaRemaining", { count: Math.max(0, Number(remainingValue)) }));
  if (hasLimit) rows.push(mt("anthropic.quotaLimit", { count: Math.max(0, Number(limitValue)) }));
  if (hasUsedPercent) rows.push(mt("anthropic.quotaUsed", { percent: formatPercent(Math.max(0, Math.min(100, Number(usedPercentValue)))) }));
  if (resetValue !== undefined && resetValue !== null && resetValue !== "") rows.push(mt("anthropic.quotaResetAt", { time: formatAnthropicLimitTime(resetValue) }));
  return `<div class="anthropic-quota-bucket"><strong>${escapeHtml(label)}</strong>${rows.map((row) => `<div class="codex-quota-meta">${escapeHtml(row)}</div>`).join("")}</div>`;
}

function formatAnthropicLimitTime(value, { duration = false } = {}) {
  const number = Number(value);
  if (Number.isFinite(number)) {
    if (duration || number < 1000000000) return `${Math.max(0, number)}s`;
    return formatCodexTimestamp(new Date(number > 1000000000000 ? number : number * 1000).toISOString());
  }
  const raw = String(value || "").trim();
  const parsed = Date.parse(raw);
  return Number.isFinite(parsed) ? formatCodexTimestamp(raw) : raw;
}

function anthropicRateLimitReached(value) {
  if (!value || typeof value !== "object") return Boolean(value);
  const hasRequestsBucket = Boolean(value.requests && typeof value.requests === "object");
  const requests = hasRequestsBucket ? value.requests : value;
  if (requests.limited === true || requests.rate_limited === true || requests.rateLimited === true || requests.reached === true) return true;
  const remaining = requests.remaining ?? requests.remaining_requests ?? requests.remainingRequests ?? requests.requests_remaining ?? requests.requestsRemaining;
  if (remaining !== undefined && remaining !== null && remaining !== "" && Number.isFinite(Number(remaining))) return Number(remaining) <= 0;
  if (hasRequestsBucket) return false;
  return value.limited === true || value.rate_limited === true || value.rateLimited === true || value.reached === true;
}

function normalizeCodexLocalUsageWindow(value) {
  const source = value && typeof value === "object" ? value : {};
  const inputTokens = Math.max(0, finiteNumber(source.inputTokens ?? source.input_tokens, 0));
  const outputTokens = Math.max(0, finiteNumber(source.outputTokens ?? source.output_tokens, 0));
  return {
    requestCount: Math.max(0, finiteNumber(source.requestCount ?? source.request_count, 0)),
    inputTokens,
    outputTokens,
    totalTokens: Math.max(0, finiteNumber(source.totalTokens ?? source.total_tokens, inputTokens + outputTokens)),
    costUsd: Math.max(0, finiteNumber(source.costUsd ?? source.cost_usd, 0)),
  };
}

function codexLocalUsageHasData(value) {
  return Boolean(value.requestCount || value.totalTokens || value.costUsd);
}

function codexQuotaWindowKey(window, fallback) {
  const seconds = finiteNumber(window?.limit_window_seconds ?? window?.limitWindowSeconds ?? window?.windowSeconds, 0);
  if (seconds === 18000) return "5h";
  if (seconds === 604800) return "7d";
  return fallback;
}

export function codexAccountUsageWindows(account = {}) {
  const quota = account?.quota && typeof account.quota === "object" ? account.quota : {};
  const usage = account?.usage && typeof account.usage === "object" ? account.usage : {};
  const upstream = { "5h": null, "7d": null };
  for (const [window, fallback] of [
    [quota.primary_window || quota.primaryWindow, "5h"],
    [quota.secondary_window || quota.secondaryWindow, "7d"],
  ]) {
    if (!window || typeof window !== "object") continue;
    const key = codexQuotaWindowKey(window, fallback);
    if (!upstream[key]) upstream[key] = window;
  }
  const local = {
    "5h": normalizeCodexLocalUsageWindow(usage.last5Hours || usage.last_5_hours),
    "7d": normalizeCodexLocalUsageWindow(usage.last7Days || usage.last_7_days),
  };
  return ["5h", "7d"].map((key) => ({
    key,
    quota: upstream[key],
    usage: local[key],
    hasQuota: Boolean(upstream[key]),
    hasUsage: codexLocalUsageHasData(local[key]),
  })).filter((item) => item.hasQuota || item.hasUsage);
}

function renderCodexUsageStats(stats, mt) {
  if (!codexLocalUsageHasData(stats)) return "";
  return `<div class="codex-usage-window-stats" title="${escapeAttr(mt("recordedCostHint"))}">
    <span>${escapeHtml(formatNumber(stats.requestCount))} ${escapeHtml(mt("usageRequests"))}</span>
    <span>${escapeHtml(formatNumber(stats.totalTokens, { notation: "compact", maximumFractionDigits: 1 }))} ${escapeHtml(mt("usageTokens"))}</span>
    <span>${escapeHtml(formatMoney(stats.costUsd))}</span>
  </div>`;
}

function renderCodexUsageWindow(item, mt, now) {
  const window = item.quota;
  const used = window ? Math.max(0, Math.min(100, finiteNumber(window.used_percent ?? window.usedPercent, 0))) : 0;
  const reset = window ? quotaResetText(window, mt, now) : "";
  const tone = used >= 100 ? "danger" : used >= 80 ? "warning" : "healthy";
  const meter = window
    ? `<div class="codex-usage-window-meter">
        <span class="codex-usage-window-badge is-${escapeAttr(item.key)}">${escapeHtml(item.key)}</span>
        <div class="codex-quota-progress ${tone}" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${escapeAttr(used)}"><span style="width:${escapeAttr(used)}%"></span></div>
        <strong class="codex-usage-window-percent ${tone}">${escapeHtml(`${formatPercent(used)}%`)}</strong>
        ${reset ? `<span class="codex-quota-meta">${escapeHtml(reset)}</span>` : ""}
      </div>`
    : `<div class="codex-usage-window-meter is-local-only"><span class="codex-usage-window-badge is-${escapeAttr(item.key)}">${escapeHtml(item.key)}</span><span class="codex-quota-meta">${escapeHtml(mt("usageLocalOnly"))}</span></div>`;
  return `<div class="codex-quota-window">${renderCodexUsageStats(item.usage, mt)}${meter}</div>`;
}

function renderCodexUsage(account, mt, now) {
  const windows = codexAccountUsageWindows(account);
  if (!windows.length) return `<span class="codex-no-quota">${escapeHtml(mt("noQuota"))}</span>`;
  return `<div class="codex-quota-stack">${windows.map((item) => renderCodexUsageWindow(item, mt, now)).join("")}</div>`;
}

function codexActionIcon(name) {
  const paths = {
    edit: '<path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/>',
    sync: '<path d="M21 12a9 9 0 1 1-2.64-6.36"/><path d="M21 3v6h-6"/>',
    enable: '<path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.8 0"/>',
    disable: '<circle cx="12" cy="12" r="9"/><path d="m5.64 5.64 12.72 12.72"/>',
    delete: '<path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="M19 6l-1 14H6L5 6"/><path d="M10 11v5"/><path d="M14 11v5"/>',
    save: '<path d="m5 12 4 4L19 6"/>',
    cancel: '<path d="m6 6 12 12"/><path d="m18 6-12 12"/>',
    export: '<path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/>',
  };
  return `<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${paths[name] || paths.edit}</svg>`;
}

function codexQuotaIsLimited(quota) {
  if (!quota || typeof quota !== "object") return false;
  if (quota.rate_limit_reached_type || quota.rateLimitReachedType) return true;
  return [quota.primary_window, quota.primaryWindow, quota.secondary_window, quota.secondaryWindow]
    .some((window) => window && finiteNumber(window.used_percent ?? window.usedPercent, 0) >= 100);
}

export function finiteNumber(value, fallback = 0) {
  const number = Number(value);
  return Number.isFinite(number) ? number : fallback;
}

export function subscriptionAccountStatus(account = {}, { now = Date.now() } = {}) {
  const expiresAt = String(account?.expires_at || account?.expiresAt || "").trim();
  const expiresAtMs = expiresAt ? Date.parse(expiresAt) : Number.NaN;
  const refreshable = Boolean(String(account?.refresh_token || account?.refreshToken || "")) || account?.refreshable === true;
  if (Boolean(account?.disabled)) return { key: "statusDisabled", tone: "muted", expiresAt };
  if (Number.isFinite(expiresAtMs) && expiresAtMs <= now && !refreshable) return { key: "expired", tone: "danger", expiresAt };
  return { key: "available", tone: "ok", expiresAt };
}

export function subscriptionAccountOverview(accounts, { now = Date.now() } = {}) {
  const overview = { total: 0, available: 0, rateLimited: 0, disabled: 0, expired: 0 };
  for (const account of Array.isArray(accounts) ? accounts : []) {
    overview.total += 1;
    const status = subscriptionAccountStatus(account, { now });
    if (status.key === "statusDisabled") overview.disabled += 1;
    else if (status.key === "expired") overview.expired += 1;
    else overview.available += 1;
  }
  return overview;
}

export function subscriptionAccountStableID(account) {
  return String(account?.id || account?.name || "").trim();
}

// renderSubscriptionAccountManagementTable renders one provider's account list.
// data attributes always carry the provider so events can be routed without a
// shared/stacked account surface.
// quotaCount accepts the string counts the server echoes from upstream
// rate-limit headers and returns null for anything that is not a real count, so
// a missing budget never renders as 0.
function quotaCount(value) {
  if (value === null || value === undefined || value === "") return null;
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : null;
}

// subscriptionAccountQuotaBudgets flattens a quota snapshot into the buckets
// that actually carry data. An account that has never made a request has no
// snapshot at all, which is reported as pending rather than as zero quota.
export function subscriptionAccountQuotaBudgets(account = {}) {
  const quota = account?.quota;
  if (!quota || typeof quota !== "object") return [];
  const budgets = [];
  for (const [labelKey, bucket] of [["quotaRequests", quota.requests], ["quotaTokens", quota.tokens]]) {
    const remaining = quotaCount(bucket?.remaining);
    const limit = quotaCount(bucket?.limit);
    if (remaining === null && limit === null) continue;
    budgets.push({ labelKey, remaining, limit, reset: String(bucket?.reset || "") });
  }
  return budgets;
}

// Providers whose upstream reports a plan allowance rather than live
// consumption. Measured against a live Grok account: five requests inside 22
// seconds all came back remaining == limit, on a 21-request limit, so the
// figure never counts down. Rendering it as "remaining / limit" promised a
// countdown that does not exist, so these providers show the allowance alone.
// Nothing else changes: the headers are still captured, and if xAI starts
// reporting real consumption the value simply becomes accurate again.
const nominalQuotaProviders = new Set(["grok"]);

// Google's Cloud Code reports a per-model remaining *fraction* and no absolute
// limit, so there is no remaining/limit pair to show and no total to derive. It
// also returns ~20 models, which will not fit in a table cell. The binding
// constraint is whichever model has least left, so show that one and put the
// rest in the tooltip.
export function subscriptionAccountModelQuotas(account = {}) {
  const list = account?.quota?.model_quotas;
  if (!Array.isArray(list)) return [];
  return list
    .filter((entry) => entry && typeof entry === "object" && typeof entry.model === "string" && entry.model !== "")
    .map((entry) => ({
      model: entry.model,
      displayName: typeof entry.displayName === "string" ? entry.displayName : "",
      percent: Math.max(0, Math.min(100, Math.round(Number(entry.remainingPercent) || 0))),
      reset: typeof entry.reset === "string" ? entry.reset : "",
    }))
    .sort((left, right) => left.percent - right.percent || left.model.localeCompare(right.model));
}

function quotaSummaryPercent(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) return null;
  return Math.round(Math.max(0, Math.min(100, number)) * 10) / 10;
}

function quotaSummaryTone(percent, limited = false) {
  if (limited || percent <= 0) return "danger";
  if (percent <= 20) return "warning";
  return "healthy";
}

function quotaSummaryAccountLabel(provider, account = {}) {
  if (provider === "codex") {
    return String(account.alias || account.email || account.account_id || account.accountID || account.name || codexAccountStableID(account) || "").trim();
  }
  if (provider === "anthropic") {
    return String(account.alias || account.profile || account.source || account.id || "").trim();
  }
  return String(account.alias || account.email || account.id || account.name || "").trim();
}

function quotaSummaryUpdatedAt(account = {}, quota = null) {
  return String(quota?.fetched_at || quota?.fetchedAt || quota?.updated_at || quota?.updatedAt || account?.updated_at || account?.updatedAt || "").trim();
}

function quotaSummaryReset(value) {
  if (value === null || value === undefined || value === "") return { resetAt: "", resetAfterSeconds: 0 };
  const number = Number(value);
  if (Number.isFinite(number) && number >= 0 && number < 1000000000) return { resetAt: "", resetAfterSeconds: number };
  return { resetAt: String(value), resetAfterSeconds: 0 };
}

function quotaSummaryOverview(provider, accounts, now) {
  if (provider === "codex") return codexAccountOverview(accounts, { now });
  if (provider === "anthropic") return anthropicAccountOverview(accounts);
  return subscriptionAccountOverview(accounts, { now });
}

function quotaSummaryAccountEligible(provider, account, now) {
  const status = provider === "codex"
    ? codexAccountStatus(account, { now })
    : provider === "anthropic"
      ? anthropicAccountStatus(account)
      : subscriptionAccountStatus(account, { now });
  return status.key === "available" || status.key === "rateLimited";
}

function codexQuotaSummaryCandidates(accounts, now) {
  const candidates = [];
  for (const account of accounts) {
    if (!quotaSummaryAccountEligible("codex", account, now)) continue;
    const quota = account?.quota && typeof account.quota === "object" ? account.quota : {};
    const accountLabel = quotaSummaryAccountLabel("codex", account);
    const updatedAt = quotaSummaryUpdatedAt(account, quota);
    let hasWindow = false;
    for (const [window, fallback] of [[quota.primary_window || quota.primaryWindow, "5h"], [quota.secondary_window || quota.secondaryWindow, "7d"]]) {
      if (!window || typeof window !== "object") continue;
      const usedValue = window.used_percent ?? window.usedPercent;
      if (usedValue === null || usedValue === undefined || usedValue === "" || !Number.isFinite(Number(usedValue))) continue;
      const percent = quotaSummaryPercent(100 - Number(usedValue));
      const reset = quotaSummaryReset(window.reset_at || window.resetAt || window.reset_after_seconds || window.resetAfterSeconds);
      candidates.push({ percent, accountLabel, bucket: codexQuotaWindowKey(window, fallback), updatedAt, ...reset });
      hasWindow = true;
    }
    if (codexQuotaIsLimited(quota)) {
      candidates.push({ percent: 0, accountLabel, bucket: "rateLimited", updatedAt, resetAt: "", resetAfterSeconds: 0 });
    } else if (!hasWindow) {
      continue;
    }
  }
  return candidates;
}

function anthropicQuotaSummaryCandidates(accounts, now) {
  const candidates = [];
  for (const account of accounts) {
    if (!quotaSummaryAccountEligible("anthropic", account, now)) continue;
    const quota = account?.quota ?? account?.rate_limit ?? account?.rateLimit;
    if (!quota || typeof quota !== "object") continue;
    const accountLabel = quotaSummaryAccountLabel("anthropic", account);
    const updatedAt = quotaSummaryUpdatedAt(account, quota);
    const requestBucket = quota.requests && typeof quota.requests === "object" ? quota.requests : quota;
    const buckets = [
      ["requests", requestBucket],
      ["inputTokens", quota.input_tokens || quota.inputTokens],
      ["outputTokens", quota.output_tokens || quota.outputTokens],
    ];
    let hasCandidate = false;
    for (const [bucketName, bucket] of buckets) {
      if (!bucket || typeof bucket !== "object") continue;
      const remaining = bucket.remaining ?? bucket.remaining_requests ?? bucket.remainingRequests ?? bucket.requests_remaining ?? bucket.requestsRemaining;
      const limit = bucket.limit ?? bucket.request_limit ?? bucket.requestLimit ?? bucket.total;
      const used = bucket.used_percent ?? bucket.usedPercent;
      let percent = null;
      if (remaining !== null && remaining !== undefined && remaining !== "" && limit !== null && limit !== undefined && limit !== "" && Number.isFinite(Number(limit)) && Number(limit) > 0 && Number.isFinite(Number(remaining))) {
        percent = quotaSummaryPercent((Number(remaining) / Number(limit)) * 100);
      } else if (used !== null && used !== undefined && used !== "" && Number.isFinite(Number(used))) {
        percent = quotaSummaryPercent(100 - Number(used));
      }
      if (percent === null) continue;
      const reset = quotaSummaryReset(bucket.reset || bucket.reset_at || bucket.resetAt || bucket.resets_at || bucket.resetsAt);
      candidates.push({ percent, accountLabel, bucket: bucketName, updatedAt, ...reset });
      hasCandidate = true;
    }
    if (!hasCandidate && anthropicRateLimitReached(quota)) {
      candidates.push({ percent: 0, accountLabel, bucket: "requests", updatedAt, resetAt: "", resetAfterSeconds: 0 });
    }
  }
  return candidates;
}

function subscriptionQuotaSummaryData(provider, accounts, now) {
  const modelCandidates = [];
  const budgetCandidates = [];
  const allowances = [];
  for (const account of accounts) {
    if (!quotaSummaryAccountEligible(provider, account, now)) continue;
    const quota = account?.quota && typeof account.quota === "object" ? account.quota : null;
    const accountLabel = quotaSummaryAccountLabel(provider, account);
    const updatedAt = quotaSummaryUpdatedAt(account, quota);
    if (provider === "gemini") {
      for (const modelQuota of subscriptionAccountModelQuotas(account)) {
        modelCandidates.push({
          percent: modelQuota.percent,
          accountLabel,
          model: modelQuota.model,
          bucket: "model",
          updatedAt,
          ...quotaSummaryReset(modelQuota.reset),
        });
      }
    }
    for (const budget of subscriptionAccountQuotaBudgets(account)) {
      const bucket = budget.labelKey === "quotaTokens" ? "tokens" : "requests";
      const reset = quotaSummaryReset(budget.reset);
      if (provider !== "grok" && budget.remaining !== null && budget.limit !== null && budget.limit > 0) {
        budgetCandidates.push({
          percent: quotaSummaryPercent((budget.remaining / budget.limit) * 100),
          accountLabel,
          bucket,
          updatedAt,
          ...reset,
        });
      } else {
        const value = provider === "grok" && budget.limit !== null ? budget.limit : (budget.limit ?? budget.remaining);
        if (value !== null) allowances.push({ value, mode: budget.limit !== null ? "limit" : "remaining", accountLabel, bucket, updatedAt, ...reset });
      }
    }
  }
  return { candidates: modelCandidates.length ? modelCandidates : budgetCandidates, allowances };
}

// providerAccountQuotaSummary compresses the provider-specific account/quota
// shapes into one card-sized, risk-first summary. Missing snapshots stay missing:
// only explicit upstream zeroes become 0%, so the overview never invents an
// exhausted account from absent data.
export function providerAccountQuotaSummary(provider, accounts, {
  loaded = true,
  loading = false,
  error = "",
  now = Date.now(),
} = {}) {
  const kind = String(provider || "").trim().toLowerCase();
  const items = Array.isArray(accounts) ? accounts : [];
  const overview = quotaSummaryOverview(kind, items, now);
  const base = {
    provider: kind,
    loaded: Boolean(loaded),
    loading: Boolean(loading),
    error: String(error || ""),
    total: overview.total || 0,
    available: overview.available || 0,
    limited: overview.rateLimited || 0,
    disabled: overview.disabled || 0,
    expired: overview.expired || 0,
    hasQuota: false,
    percent: null,
    tone: "muted",
    accountLabel: "",
    model: "",
    bucket: "",
    resetAt: "",
    resetAfterSeconds: 0,
    updatedAt: "",
    allowance: null,
  };
  if (!base.loaded) {
    if (base.loading) return { ...base, state: "loading" };
    if (base.error) return { ...base, state: "error" };
    return { ...base, state: "idle" };
  }
  if (!items.length) {
    if (base.loading) return { ...base, state: "loading" };
    if (base.error) return { ...base, state: "error" };
    return { ...base, state: "empty" };
  }

  let candidates = [];
  let allowances = [];
  if (kind === "codex") candidates = codexQuotaSummaryCandidates(items, now);
  else if (kind === "anthropic") candidates = anthropicQuotaSummaryCandidates(items, now);
  else ({ candidates, allowances } = subscriptionQuotaSummaryData(kind, items, now));

  candidates = candidates.filter((candidate) => candidate.percent !== null);
  if (candidates.length) {
    candidates.sort((left, right) => left.percent - right.percent || left.accountLabel.localeCompare(right.accountLabel) || String(left.model || left.bucket).localeCompare(String(right.model || right.bucket)));
    const tightest = candidates[0];
    return {
      ...base,
      ...tightest,
      state: "ready",
      hasQuota: true,
      tone: quotaSummaryTone(tightest.percent, tightest.percent <= 0 || base.limited > 0),
      refreshing: base.loading,
      stale: Boolean(base.error),
    };
  }
  if (allowances.length) {
    allowances.sort((left, right) => Number(left.value) - Number(right.value) || left.accountLabel.localeCompare(right.accountLabel));
    const allowance = allowances[0];
    return {
      ...base,
      state: "allowance",
      tone: "healthy",
      allowance,
      accountLabel: allowance.accountLabel,
      bucket: allowance.bucket,
      resetAt: allowance.resetAt,
      resetAfterSeconds: allowance.resetAfterSeconds,
      updatedAt: allowance.updatedAt,
      refreshing: base.loading,
      stale: Boolean(base.error),
    };
  }
  if (base.error) return { ...base, state: "error" };
  return { ...base, state: "pending", refreshing: base.loading };
}

function renderSubscriptionModelQuotaCell(quotas, st) {
  const lowest = quotas[0];
  // Model ids rather than Google's displayName: the id is what the model picker
  // shows, and the display names are not unique — three different ids all come
  // back as "Gemini 3.1 Flash Lite", which makes a display-name list unreadable.
  const tooltip = quotas
    .slice(0, 24)
    .map((entry) => `${entry.model}: ${entry.percent}%`)
    .join("\n");
  const reset = lowest.reset ? st("quotaModelResetAt", { time: formatTimestamp(lowest.reset, { fallback: lowest.reset }) }) : "";
  const detail = [st("quotaModelsMore", { count: quotas.length }), reset].filter(Boolean).join(" · ");
  return `<div class="subscription-quota" title="${escapeAttr(tooltip)}">
    <div class="subscription-quota-line"><span class="subscription-quota-label">${escapeHtml(st("quotaModelLowest"))}</span><span class="subscription-quota-value">${escapeHtml(st("quotaModelPercent", { percent: lowest.percent }))}</span></div>
    <div class="subscription-quota-line"><span class="subscription-quota-label">${escapeHtml(lowest.model)}</span><span class="subscription-quota-value">${escapeHtml(detail)}</span></div>
  </div>`;
}

function renderSubscriptionQuotaCell(account, st, provider = "") {
  const modelQuotas = subscriptionAccountModelQuotas(account);
  if (modelQuotas.length) return renderSubscriptionModelQuotaCell(modelQuotas, st);
  const budgets = subscriptionAccountQuotaBudgets(account);
  if (!budgets.length) return `<span class="subscription-quota-empty">${escapeHtml(st("quotaPending"))}</span>`;
  const nominalOnly = nominalQuotaProviders.has(String(provider).trim().toLowerCase());
  const lines = budgets.map(({ labelKey, remaining, limit }) => {
    const value = (remaining === null || (nominalOnly && limit !== null))
      ? st("quotaLimitOnly", { limit: formatNumber(limit) })
      : limit === null
        ? formatNumber(remaining)
        : st("quotaRemainingOfLimit", { remaining: formatNumber(remaining), limit: formatNumber(limit) });
    return `<div class="subscription-quota-line"><span class="subscription-quota-label">${escapeHtml(st(labelKey))}</span><span class="subscription-quota-value">${escapeHtml(value)}</span></div>`;
  });
  return `<div class="subscription-quota">${lines.join("")}</div>`;
}

export function renderSubscriptionAccountManagementTable(provider, accounts, {
  translate = (key, params) => t(`modelProvider.subscription.common.${key}`, params),
  emptyText = "",
  now = Date.now(),
  editing = null,
  busy = {},
} = {}) {
  const st = translate;
  const items = Array.isArray(accounts) ? accounts : [];
  if (!items.length) return `<div class="mp-console-empty" role="status">${escapeHtml(emptyText || st("empty"))}</div>`;
  return `<div class="codex-account-table-wrap subscription-account-table-wrap settings-card-content">
    <table class="codex-account-table subscription-account-table" data-subscription-provider="${escapeAttr(provider)}" aria-label="${escapeAttr(st("accountsTitle"))}">
      <thead><tr>
        <th scope="col">${escapeHtml(st("accountName"))}</th><th scope="col">${escapeHtml(st("accountId"))}</th><th scope="col">${escapeHtml(st("priority"))}</th><th scope="col">${escapeHtml(st("status"))}</th>
        <th scope="col">${escapeHtml(st("quota"))}</th><th scope="col">${escapeHtml(st("lastUpdated"))}</th><th scope="col">${escapeHtml(st("actions"))}</th>
      </tr></thead>
      <tbody>${items.map((account) => renderSubscriptionAccountRow(provider, account, st, now, editing, busy)).join("")}</tbody>
    </table>
  </div>`;
}

function renderSubscriptionAccountRow(provider, account, st, now, editing, busy) {
  const id = subscriptionAccountStableID(account);
  const alias = String(account?.alias || "");
  const priority = finiteNumber(account?.priority, 100);
  const disabled = Boolean(account?.disabled);
  const isEditing = editing?.id === id;
  const isBusy = Boolean(busy?.[id]);
  const email = String(account?.email || "");
  const project = String(account?.project_id || account?.projectId || "");
  const fallbackName = email || id || st("unknown");
  const displayName = alias || fallbackName;
  const secondaryName = [alias && fallbackName !== alias ? fallbackName : "", project ? st("project") + ": " + project : ""].filter(Boolean).join(" · ");
  const lastUpdated = String(account?.updated_at || account?.updatedAt || "");
  const status = subscriptionAccountStatus(account, { now });
  const disabledAttributes = isBusy ? ` disabled aria-busy="true"` : "";
  const editAlias = String(isEditing ? editing.alias ?? alias : alias);
  const editPriority = finiteNumber(isEditing ? editing.priority : priority, priority);
  const providerAttr = escapeAttr(provider);
  return `<tr data-subscription-account-row="${escapeAttr(id)}" data-subscription-provider="${providerAttr}" class="${isEditing ? "is-editing" : ""}" aria-busy="${isBusy ? "true" : "false"}">
    <td data-label="${escapeAttr(st("accountName"))}">${isEditing
      ? `<label class="codex-inline-edit-field"><span class="mp-visually-hidden">${escapeHtml(st("accountName"))}</span><input class="codex-account-alias settings-text-input settings-form-field" value="${escapeAttr(editAlias)}" placeholder="${escapeAttr(fallbackName)}" maxlength="200" data-subscription-edit-alias="${escapeAttr(id)}" data-subscription-provider="${providerAttr}" data-select-on-focus="true"${disabledAttributes}></label>`
      : `<strong class="codex-account-name">${escapeHtml(displayName)}</strong>`}${secondaryName ? `<div class="codex-account-secondary">${escapeHtml(secondaryName)}</div>` : ""}</td>
    <td data-label="${escapeAttr(st("accountId"))}"><code class="codex-account-id">${escapeHtml(id || st("unknown"))}</code></td>
    <td data-label="${escapeAttr(st("priority"))}">${isEditing
      ? `<label class="codex-inline-edit-field"><span class="mp-visually-hidden">${escapeHtml(st("priority"))}</span><input class="codex-priority-input settings-text-input settings-form-field" type="number" min="1" max="1000000" step="1" value="${escapeAttr(editPriority)}" data-subscription-edit-priority="${escapeAttr(id)}" data-subscription-provider="${providerAttr}" data-select-on-focus="true"${disabledAttributes}></label>`
      : `<span class="codex-priority-value">${escapeHtml(String(priority))}</span>`}</td>
    <td data-label="${escapeAttr(st("status"))}"><span class="settings-status-pill settings-badge ${escapeAttr(status.tone)}">${escapeHtml(st(status.key))}</span></td>
    <td data-label="${escapeAttr(st("quota"))}">${renderSubscriptionQuotaCell(account, st, provider)}</td>
    <td data-label="${escapeAttr(st("lastUpdated"))}">${escapeHtml(lastUpdated ? formatCodexTimestamp(lastUpdated) : st("never"))}</td>
    <td data-label="${escapeAttr(st("actions"))}"><div class="codex-account-actions settings-inline-actions" role="group">
      ${isEditing
        ? `<button class="codex-icon-action save" type="button" data-subscription-save="${escapeAttr(id)}" data-subscription-provider="${providerAttr}" title="${escapeAttr(st("save"))}"${disabledAttributes}>${codexActionIcon("save")}<span>${escapeHtml(st("save"))}</span></button>
        <button class="codex-icon-action cancel" type="button" data-subscription-edit-cancel="${escapeAttr(id)}" data-subscription-provider="${providerAttr}" title="${escapeAttr(st("cancel"))}"${disabledAttributes}>${codexActionIcon("cancel")}<span>${escapeHtml(st("cancel"))}</span></button>`
        : `<button class="codex-icon-action edit" type="button" data-subscription-edit="${escapeAttr(id)}" data-subscription-provider="${providerAttr}" title="${escapeAttr(st("edit"))}"${disabledAttributes}>${codexActionIcon("edit")}<span>${escapeHtml(st("edit"))}</span></button>
        <button class="codex-icon-action sync" type="button" data-subscription-sync="${escapeAttr(id)}" data-subscription-provider="${providerAttr}" title="${escapeAttr(st("sync"))}"${disabledAttributes}>${codexActionIcon("sync")}<span>${escapeHtml(st("sync"))}</span></button>
        <button class="codex-icon-action toggle" type="button" data-subscription-toggle="${escapeAttr(id)}" data-subscription-provider="${providerAttr}" data-disabled="${disabled ? "true" : "false"}" title="${escapeAttr(disabled ? st("enable") : st("disable"))}"${disabledAttributes}>${codexActionIcon(disabled ? "enable" : "disable")}<span>${escapeHtml(disabled ? st("enable") : st("disable"))}</span></button>
        <button class="codex-icon-action delete" type="button" data-subscription-delete="${escapeAttr(id)}" data-subscription-provider="${providerAttr}" title="${escapeAttr(st("delete"))}"${disabledAttributes}>${codexActionIcon("delete")}<span>${escapeHtml(st("delete"))}</span></button>`}
    </div></td>
  </tr>`;
}

function formatPercent(value) {
  return Number.isInteger(value) ? String(value) : value.toFixed(1);
}

function formatWindowSeconds(seconds) {
  if (!(seconds > 0)) return "";
  if (seconds % 86400 === 0) return `${seconds / 86400}d`;
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${Math.round(seconds)}s`;
}

function quotaResetText(window, mt, now) {
  let seconds = finiteNumber(window.reset_after_seconds ?? window.resetAfterSeconds, 0);
  const resetAtValue = window.reset_at || window.resetAt;
  if (!(seconds > 0) && resetAtValue) {
    const resetAt = Date.parse(resetAtValue);
    if (Number.isFinite(resetAt)) seconds = Math.max(0, Math.ceil((resetAt - now) / 1000));
  }
  if (!(seconds > 0)) return "";
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const compact = days ? `${days}d ${hours}h` : hours ? `${hours}h ${minutes}m` : `${Math.max(1, minutes)}m`;
  return mt("resetsIn", { time: compact });
}

function formatCodexTimestamp(value) {
  return formatTimestamp(value, { fallback: value });
}
