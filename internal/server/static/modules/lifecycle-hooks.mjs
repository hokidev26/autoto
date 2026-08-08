import { escapeAttr, escapeHtml } from "./dom.mjs";
import { lifecycleHookMessage } from "./messages-lifecycle-hooks.mjs";

export const lifecycleHookEvents = Object.freeze(["run.before", "run.after", "tool.before", "tool.after"]);
export const lifecycleHookScopes = Object.freeze(["global", "project", "agent"]);
export const lifecycleHookModes = Object.freeze(["sync", "async"]);
export const lifecycleHookFailurePolicies = Object.freeze(["continue", "fail_run", "retry", "disable_hook"]);
export const lifecycleHookActionKinds = Object.freeze(["shell", "http", "llm"]);
export const lifecycleHookLimits = Object.freeze({ hooks: 200, history: 200, name: 120, description: 2000, filterEntries: 128 });

const secretRefPattern = /^env:[A-Za-z_][A-Za-z0-9_]{0,255}$/;
const identifierPattern = /^[^\x00-\x1f\x7f]{1,128}$/;
const environmentNamePattern = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;
const headerNamePattern = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]{1,128}$/;
const forbiddenHTTPHeaders = new Set(["connection", "content-length", "host", "keep-alive", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade"]);

function objectValue(value) { return value && typeof value === "object" && !Array.isArray(value) ? value : {}; }
function boundedText(value, maximum = 256) { return String(value ?? "").trim().slice(0, maximum); }
function uniqueStrings(value, maximum = 128) {
  return [...new Set((Array.isArray(value) ? value : []).map((item) => boundedText(item, 256)).filter(Boolean))].sort().slice(0, maximum);
}
function clone(value) { return globalThis.structuredClone ? structuredClone(value) : JSON.parse(JSON.stringify(value)); }
function hasOwn(value, key) { return Object.prototype.hasOwnProperty.call(value || {}, key); }


export function redactLifecycleText(value) {
  return String(value ?? "")
    .replace(/\bbearer\s+[^\s,;]+/gi, "Bearer [REDACTED]")
    .replace(/(authorization\s*[:=]\s*(?:bearer\s+)?)[^\s,;]+/gi, "$1[REDACTED]")
    .replace(/((?:api[-_]?key|token|secret|password|cookie)\s*[:=]\s*)[^\s,;]+/gi, "$1[REDACTED]")
    .replace(/\b(?:env|secret|vault|keychain):[A-Za-z0-9_./-]+\b/gi, "[REDACTED]");
}

function normalizeScope(value) {
  const source = objectValue(value);
  const kind = lifecycleHookScopes.includes(source.kind) ? source.kind : "global";
  return { kind, ...(kind === "global" ? {} : { id: boundedText(source.id, 128) }) };
}

function normalizeFilter(value) {
  const source = objectValue(value);
  const attributes = Object.fromEntries(Object.entries(objectValue(source.attributes)).slice(0, lifecycleHookLimits.filterEntries).map(([key, values]) => [boundedText(key, 64), uniqueStrings(values)]).filter(([key, values]) => key && values.length));
  return {
    projectIds: uniqueStrings(source.projectIds),
    agentIds: uniqueStrings(source.agentIds),
    toolNames: uniqueStrings(source.toolNames),
    runKinds: uniqueStrings(source.runKinds),
    attributes,
  };
}

function configuredSecrets(value) {
  return Object.fromEntries(Object.entries(objectValue(value)).filter(([key, configured]) => boundedText(key, 128) && configured === true).map(([key]) => [boundedText(key, 128), true]));
}

function normalizeAction(value) {
  const source = objectValue(value);
  const kind = lifecycleHookActionKinds.includes(source.kind) ? source.kind : "shell";
  const result = { kind, secretConfigured: configuredSecrets(source.secretConfigured) };
  if (kind === "shell") {
    const shell = objectValue(source.shell);
    result.shell = { executable: boundedText(shell.executable, 128), args: (Array.isArray(shell.args) ? shell.args : []).map((arg) => String(arg).slice(0, 4096)).slice(0, 128), cwd: boundedText(shell.cwd, 512), env: Object.fromEntries(Object.entries(objectValue(shell.env)).slice(0, 64).map(([key, item]) => [boundedText(key, 128), String(item).slice(0, 4096)])), timeoutSeconds: Math.max(1, Math.min(300, Number(shell.timeoutSeconds) || 30)), canonicalStdinV1: shell.canonicalStdinV1 !== false };
  }
  if (kind === "http") {
    const http = objectValue(source.http);
    result.http = { url: boundedText(http.url, 2048), method: boundedText(http.method || "POST", 12).toUpperCase(), headers: Object.fromEntries(Object.entries(objectValue(http.headers)).slice(0, 64).map(([key, item]) => [boundedText(key, 128), String(item).slice(0, 4096)])), timeoutSeconds: Math.max(1, Math.min(300, Number(http.timeoutSeconds) || 30)) };
  }
  if (kind === "llm") {
    const llm = objectValue(source.llm);
    result.llm = { model: boundedText(llm.model, 256), prompt: boundedText(llm.prompt, 16384), maxOutputTokens: Math.max(1, Math.min(4096, Number(llm.maxOutputTokens) || 256)), timeoutSeconds: Math.max(1, Math.min(300, Number(llm.timeoutSeconds) || 30)) };
  }
  return result;
}

export function normalizeLifecycleHook(value = {}) {
  const source = objectValue(value);
  return {
    id: boundedText(source.id, 128),
    name: boundedText(source.name, lifecycleHookLimits.name),
    description: boundedText(source.description, lifecycleHookLimits.description),
    enabled: Boolean(source.enabled),
    event: lifecycleHookEvents.includes(source.event) ? source.event : "run.before",
    scope: normalizeScope(source.scope),
    priority: Math.max(-1000, Math.min(1000, Math.trunc(Number(source.priority) || 0))),
    filter: normalizeFilter(source.filter),
    mode: lifecycleHookModes.includes(source.mode) ? source.mode : "sync",
    failurePolicy: lifecycleHookFailurePolicies.includes(source.failurePolicy) ? source.failurePolicy : "continue",
    action: normalizeAction(source.action),
    revision: Math.max(0, Math.trunc(Number(source.revision) || 0)),
    createdAt: boundedText(source.createdAt, 80),
    updatedAt: boundedText(source.updatedAt, 80),
  };
}

function validateIdentifier(value, label) { if (!identifierPattern.test(value) || value !== value.trim()) throw new Error(`${label} is invalid`); }
function validateTimeout(value) { const timeout = Number(value) || 30; if (!Number.isInteger(timeout) || timeout < 1 || timeout > 300) throw new Error("Action timeout must be between 1 and 300 seconds"); return timeout; }
function validatedEntries(value, label) {
  const entries = Object.entries(objectValue(value));
  if (entries.length > 64) throw new Error(`${label} exceed 64 entries`);
  const names = new Set();
  for (const [name] of entries) {
    const folded = String(name).toLowerCase();
    if (names.has(folded)) throw new Error(`${label} names must be unique ignoring case`);
    names.add(folded);
  }
  return entries;
}
function validateEnvironment(value) {
  return Object.fromEntries(validatedEntries(value, "Environment values").map(([key, item]) => {
    if (!environmentNamePattern.test(key)) throw new Error("Environment variable name is invalid");
    const text = String(item);
    if (text.length > 4096 || /[\x00\r\n]/.test(text)) throw new Error("Environment variable value is invalid");
    return [key, text];
  }));
}
function sensitiveHTTPHeader(name) {
  const lower = String(name).toLowerCase();
  return ["authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key"].includes(lower) || lower.includes("token") || lower.includes("secret");
}
function validateHTTPHeaders(value) {
  return Object.fromEntries(validatedEntries(value, "HTTP headers").map(([key, item]) => {
    if (!headerNamePattern.test(key)) throw new Error("HTTP header name is invalid");
    const text = String(item);
    if (text.length > 4096 || /[\x00-\x08\x0a-\x1f\x7f]/.test(text)) throw new Error("HTTP header value is invalid");
    return [key, text];
  }));
}
function validateSecretRefs(value, kind) {
  return Object.fromEntries(validatedEntries(value, "Secret references").map(([key, ref]) => {
    if (kind === "environment" && !environmentNamePattern.test(key)) throw new Error("Secret environment name is invalid");
    if (kind === "header" && (!headerNamePattern.test(key) || (!sensitiveHTTPHeader(key) && !key.toLowerCase().startsWith("x-")))) throw new Error("Secret HTTP header name is invalid");
    if (!secretRefPattern.test(String(ref))) throw new Error("Secrets must use env:VARIABLE_NAME references");
    return [key, String(ref)];
  }));
}
function caseInsensitiveOverlap(left, right) {
  const names = new Set(Object.keys(objectValue(left)).map((name) => name.toLowerCase()));
  return Object.keys(objectValue(right)).some((name) => names.has(name.toLowerCase()));
}
function containsBackgroundEscape(args) { return args.some((arg) => ["&", "nohup", "setsid", "start"].includes(String(arg).trim().toLowerCase())); }
function validateRelativeCWD(value) {
  const cwd = String(value || "").trim();
  if (!cwd) return "";
  if (/^(?:[A-Za-z]:[\\/]|[\\/])/.test(cwd)) throw new Error("Shell cwd must be workspace-relative");
  if (cwd.split(/[\\/]+/).some((part) => part === "..")) throw new Error("Shell cwd cannot escape the workspace");
  return cwd;
}
function validateHTTPURL(value) {
  let parsed;
  try { parsed = new URL(String(value)); } catch { throw new Error("HTTP action URL is invalid"); }
  if (parsed.username || parsed.password) throw new Error("HTTP action URL cannot contain userinfo");
  if (parsed.hash) throw new Error("HTTP action URL cannot contain a fragment");
  if (parsed.protocol === "https:") return parsed.toString();
  const loopback = parsed.hostname === "localhost" || parsed.hostname === "127.0.0.1" || parsed.hostname === "::1" || parsed.hostname === "[::1]";
  if (parsed.protocol !== "http:" || !loopback) throw new Error("HTTP action requires HTTPS except for loopback targets");
  return parsed.toString();
}

export function buildLifecycleHookPayload(value = {}, { expectedRevision } = {}) {
  const source = objectValue(value);
  const name = boundedText(source.name, lifecycleHookLimits.name);
  if (!name) throw new Error("Hook name is required");
  const event = source.event;
  if (!lifecycleHookEvents.includes(event)) throw new Error("Hook event is invalid");
  const scope = normalizeScope(source.scope);
  if (scope.kind !== "global") validateIdentifier(scope.id, "Scope id");
  const mode = source.mode || "sync";
  if (!lifecycleHookModes.includes(mode)) throw new Error("Hook mode is invalid");
  if (["run.before", "tool.before"].includes(event) && mode !== "sync") throw new Error("Before hooks must be synchronous");
  const failurePolicy = source.failurePolicy || "continue";
  if (!lifecycleHookFailurePolicies.includes(failurePolicy)) throw new Error("Failure policy is invalid");
  if (mode === "async" && failurePolicy === "fail_run") throw new Error("Asynchronous hooks cannot fail a run");
  const actionSource = objectValue(source.action);
  const kind = actionSource.kind;
  if (!lifecycleHookActionKinds.includes(kind)) throw new Error("Action kind is invalid");
  const action = { kind };
  if (kind === "shell") {
    const shell = objectValue(actionSource.shell);
    const executable = boundedText(shell.executable, 128);
    validateIdentifier(executable, "Shell executable");
    if (/[\\/]/.test(executable) || /^(?:ba|z|c)?sh$|^(?:cmd|powershell|pwsh)(?:\.exe)?$/i.test(executable)) throw new Error("Shell interpreters and paths are not allowed");
    const args = (Array.isArray(shell.args) ? shell.args : []).map((arg) => String(arg));
    if (args.some((arg) => /[\x00\r\n]/.test(arg)) || containsBackgroundEscape(args) || shell.detached) throw new Error("Shell background escape is not allowed");
    const env = validateEnvironment(shell.env);
    const secretRefs = hasOwn(shell, "secretRefs") ? validateSecretRefs(shell.secretRefs, "environment") : undefined;
    if (secretRefs && caseInsensitiveOverlap(env, secretRefs)) throw new Error("Shell environment keys cannot appear in both env and secretRefs");
    action.shell = { executable, args, cwd: validateRelativeCWD(shell.cwd), env, timeoutSeconds: validateTimeout(shell.timeoutSeconds), canonicalStdinV1: true };
    if (secretRefs) action.shell.secretRefs = secretRefs;
  }
  if (kind === "http") {
    const http = objectValue(actionSource.http);
    const method = boundedText(http.method || "POST", 12).toUpperCase();
    if (!['POST', 'PUT', 'PATCH'].includes(method)) throw new Error("HTTP method must be POST, PUT, or PATCH");
    const headers = validateHTTPHeaders(http.headers);
    if (Object.keys(headers).some((key) => sensitiveHTTPHeader(key))) throw new Error("Sensitive HTTP headers must use secretRefs");
    if (Object.keys(headers).some((key) => forbiddenHTTPHeaders.has(key.toLowerCase()))) throw new Error("Reserved HTTP headers cannot be configured");
    const secretRefs = hasOwn(http, "secretRefs") ? validateSecretRefs(http.secretRefs, "header") : undefined;
    if (secretRefs && Object.keys(secretRefs).some((key) => forbiddenHTTPHeaders.has(key.toLowerCase()))) throw new Error("Reserved HTTP headers cannot be configured");
    if (secretRefs && caseInsensitiveOverlap(headers, secretRefs)) throw new Error("HTTP header keys cannot appear in both headers and secretRefs");
    action.http = { url: validateHTTPURL(http.url), method, headers, timeoutSeconds: validateTimeout(http.timeoutSeconds) };
    if (secretRefs) action.http.secretRefs = secretRefs;
  }
  if (kind === "llm") {
    if (!["run.before", "tool.before"].includes(event) || mode !== "sync") throw new Error("LLM gates are only valid as synchronous before hooks");
    const llm = objectValue(actionSource.llm);
    const model = boundedText(llm.model, 256); validateIdentifier(model, "LLM model");
    const prompt = boundedText(llm.prompt, 16384); if (!prompt) throw new Error("LLM gate prompt is required");
    const maxOutputTokens = Number(llm.maxOutputTokens) || 256; if (!Number.isInteger(maxOutputTokens) || maxOutputTokens < 1 || maxOutputTokens > 4096) throw new Error("LLM max output tokens must be between 1 and 4096");
    action.llm = { model, prompt, maxOutputTokens, timeoutSeconds: validateTimeout(llm.timeoutSeconds) };
  }
  const payload = { name, description: boundedText(source.description, lifecycleHookLimits.description), enabled: source.enabled !== false, event, scope, priority: Math.max(-1000, Math.min(1000, Math.trunc(Number(source.priority) || 0))), filter: normalizeFilter(source.filter), mode, failurePolicy, action };
  if (expectedRevision !== undefined) { const revision = Number(expectedRevision); if (!Number.isInteger(revision) || revision < 1) throw new Error("Expected revision must be positive"); payload.expectedRevision = revision; }
  return payload;
}

function normalizeHistory(value) {
  return (Array.isArray(value) ? value : []).slice(0, lifecycleHookLimits.history).map((item) => {
    const source = objectValue(item); const execution = objectValue(source.execution);
    return { execution: { id: boundedText(execution.id,128), hookId: boundedText(execution.hookId,128), status: boundedText(execution.status,32), error: redactLifecycleText(execution.error), result: objectValue(execution.result), retryOfExecutionId: boundedText(execution.retryOfExecutionId,128), cancelRequested: Boolean(execution.cancelRequested), createdAt: boundedText(execution.createdAt,80), completedAt: boundedText(execution.completedAt,80) }, attempts: (Array.isArray(source.attempts) ? source.attempts : []).slice(0, 32).map((attempt) => ({ id: boundedText(attempt.id,128), status: boundedText(attempt.status,32), attemptNumber: Math.max(1,Number(attempt.attemptNumber)||1), error: redactLifecycleText(attempt.error), createdAt: boundedText(attempt.createdAt,80) })) };
  });
}

export function createLifecycleHooksController({ request, locale = "zh-CN", onChange, showError } = {}) {
  if (typeof request !== "function") throw new TypeError("Lifecycle hooks request is required");
  const state = { hooks: [], history: {}, loading: false, loaded: false, busy: {}, error: "" };
  const emit = () => onChange?.(getState());
  function getState() { return clone(state); }
  async function load() { state.loading=true;state.error="";emit();try{const response=await request("/api/lifecycle-hooks/");state.hooks=(Array.isArray(response) ? response : response?.hooks || []).slice(0,lifecycleHookLimits.hooks).map(normalizeLifecycleHook);state.loaded=true;return true;}catch(error){state.loaded=true;state.error=error?.status===403?"403 Forbidden: lifecycle hook management is not allowed for this session":redactLifecycleText(error?.message||error);showError?.(error);return false;}finally{state.loading=false;emit();} }
  async function mutate(key, callback) { if(state.busy[key])return false;state.busy[key]=true;state.error="";emit();try{await callback();return true;}catch(error){state.error=error?.status===403?"403 Forbidden: lifecycle hook management is not allowed for this session":redactLifecycleText(error?.message||error);showError?.(error);return false;}finally{delete state.busy[key];emit();} }
  const create = (value) => mutate("create", async()=>{await request("/api/lifecycle-hooks/",{method:"POST",body:JSON.stringify(buildLifecycleHookPayload(value))});await load();});
  const update = (id,value) => {const current=state.hooks.find((item)=>item.id===id);if(!current)throw new Error("Lifecycle hook not found");return mutate(`hook:${id}`,async()=>{await request(`/api/lifecycle-hooks/${encodeURIComponent(id)}`,{method:"PATCH",body:JSON.stringify(buildLifecycleHookPayload(value,{expectedRevision:current.revision}))});await load();});};
  const remove = (id) => {const current=state.hooks.find((item)=>item.id===id);if(!current)throw new Error("Lifecycle hook not found");return mutate(`hook:${id}`,async()=>{await request(`/api/lifecycle-hooks/${encodeURIComponent(id)}?expectedRevision=${current.revision}`,{method:"DELETE"});state.hooks=state.hooks.filter((item)=>item.id!==id);});};
  const test = (id,event) => mutate(`test:${id}`,async()=>{await request(`/api/lifecycle-hooks/${encodeURIComponent(id)}/test`,{method:"POST",body:JSON.stringify({event})});await loadHistory(id);});
  async function loadHistory(id){const response=await request(`/api/lifecycle-hooks/${encodeURIComponent(id)}/history?limit=${lifecycleHookLimits.history}`);state.history[id]=normalizeHistory(Array.isArray(response)?response:response?.history||[]);emit();return state.history[id];}
  const cancel = (executionId,hookId) => mutate(`execution:${executionId}`,async()=>{await request(`/api/lifecycle-hook-executions/${encodeURIComponent(executionId)}/cancel`,{method:"POST"});if(hookId)await loadHistory(hookId);});
  const retry = (executionId,hookId) => mutate(`execution:${executionId}`,async()=>{await request(`/api/lifecycle-hook-executions/${encodeURIComponent(executionId)}/retry`,{method:"POST"});if(hookId)await loadHistory(hookId);});
  return { cancel, create, getState, load, loadHistory, remove, render:()=>renderLifecycleHooks(state,{locale}), retry, test, update };
}

function statusLabel(status, locale) { return lifecycleHookMessage(["failed","succeeded","running","pending","cancelled"].includes(status)?status:"pending",{},locale); }
export function renderLifecycleHooks(value = {}, { locale = "zh-CN" } = {}) {
  const source=objectValue(value);const items=(Array.isArray(source.hooks)?source.hooks:[]).slice(0,lifecycleHookLimits.hooks).map(normalizeLifecycleHook);const history=objectValue(source.history);
  const cards=items.map((hook)=>`<article class="lifecycle-hook-card settings-card" data-hook-id="${escapeAttr(hook.id)}"><header><div><strong>${escapeHtml(hook.name)}</strong><small>${escapeHtml(hook.event)} · ${escapeHtml(lifecycleHookMessage(hook.mode,{},locale))}</small></div><span>${escapeHtml(lifecycleHookMessage(hook.enabled?"enabled":"disabled",{},locale))}</span></header><dl><div><dt>${escapeHtml(lifecycleHookMessage("scope",{},locale))}</dt><dd>${escapeHtml(hook.scope.kind)}${hook.scope.id?`:${escapeHtml(hook.scope.id)}`:""}</dd></div><div><dt>${escapeHtml(lifecycleHookMessage("priority",{},locale))}</dt><dd>${hook.priority}</dd></div><div><dt>${escapeHtml(lifecycleHookMessage("action",{},locale))}</dt><dd>${escapeHtml(hook.action.kind)}</dd></div></dl><p>${escapeHtml(hook.description)}</p><small>${escapeHtml(lifecycleHookMessage("revision",{revision:hook.revision},locale))}</small><div class="settings-inline-actions"><button data-hook-test="${escapeAttr(hook.id)}">${escapeHtml(lifecycleHookMessage("test",{},locale))}</button><button data-hook-history="${escapeAttr(hook.id)}">${escapeHtml(lifecycleHookMessage("history",{},locale))}</button><button data-hook-edit="${escapeAttr(hook.id)}">${escapeHtml(lifecycleHookMessage("edit",{},locale))}</button><button data-hook-delete="${escapeAttr(hook.id)}">${escapeHtml(lifecycleHookMessage("remove",{},locale))}</button></div>${(history[hook.id]||[]).map((item)=>`<div class="lifecycle-hook-execution"><span>${escapeHtml(statusLabel(item.execution.status,locale))}</span><small>${escapeHtml(lifecycleHookMessage("attempts",{count:item.attempts.length},locale))}</small>${item.execution.error?`<em>${escapeHtml(redactLifecycleText(item.execution.error))}</em>`:""}</div>`).join("")}</article>`).join("");
  return `<section id="lifecycleHooksPage" class="settings-page-section" data-authority="server-api"><header class="settings-card settings-card-header"><div><h2>${escapeHtml(lifecycleHookMessage("title",{},locale))}</h2><p>${escapeHtml(lifecycleHookMessage("description",{},locale))}</p></div><button data-lifecycle-refresh>${escapeHtml(lifecycleHookMessage("refresh",{},locale))}</button></header><div class="settings-alert" role="note">${escapeHtml(lifecycleHookMessage("secretWriteOnly",{},locale))}</div><div class="settings-alert" role="note">${escapeHtml(lifecycleHookMessage("gatewayOnly",{},locale))}</div>${source.error?`<div class="settings-alert" role="alert">${escapeHtml(redactLifecycleText(source.error))}</div>`:""}<div class="settings-data-list">${cards||`<p>${escapeHtml(lifecycleHookMessage("empty",{},locale))}</p>`}</div></section>`;
}
