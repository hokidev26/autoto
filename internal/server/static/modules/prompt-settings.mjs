import { normalizeDefinitionScope } from "./agent-role-settings.mjs";

export const promptDefinitionsEndpoint = "/api/prompt-definitions";
export const effectivePromptEndpoint = "/api/effective-prompt";
export const promptTrustLayers = Object.freeze(["system_extension", "global_user"]);

const keyPattern = /^[a-z0-9][a-z0-9._-]{0,63}$/;

export function normalizePromptDraft(value = {}) {
  const source = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  const key = String(source.key || "").trim();
  const displayName = String(source.displayName || "").trim();
  const summary = String(source.summary || "").trim();
  const layer = String(source.layer || "").trim().toLowerCase();
  const content = String(source.content || "").trim();
  if (!keyPattern.test(key)) throw new TypeError("key is invalid");
  if (!displayName || new TextEncoder().encode(displayName).length > 120 || /[\0\r\n]/.test(displayName)) throw new TypeError("displayName is invalid");
  if (new TextEncoder().encode(summary).length > 500 || summary.includes("\0")) throw new TypeError("summary is invalid");
  if (!promptTrustLayers.includes(layer)) throw new TypeError("layer must be system_extension or global_user");
  if (!content || new TextEncoder().encode(content).length > 64 * 1024 || content.includes("\0")) throw new TypeError("content is invalid");
  return { key, displayName, summary, layer, content };
}

function promptRequestBody(draft, scope, expectedRevision) {
  const body = normalizePromptDraft(draft);
  if (scope) body.scope = normalizeDefinitionScope(scope);
  if (expectedRevision !== undefined) {
    const revision = Number(expectedRevision);
    if (!Number.isSafeInteger(revision) || revision < 1) throw new TypeError("expectedRevision must be positive");
    body.expectedRevision = revision;
  }
  return body;
}

function scopeQuery(scope) {
  return new URLSearchParams(normalizeDefinitionScope(scope)).toString();
}

export function promptLayerPresentation(layer) {
  if (layer === "system_extension") return { role: "system", trust: "trusted_extension", warning: "Cannot replace immutable platform, run, role, or closing layers." };
  if (layer === "global_user") return { role: "user", trust: "untrusted_user", warning: "Rendered as explicit untrusted user context, never system text." };
  throw new TypeError("unknown prompt layer");
}

export function createPromptSettingsController({ request } = {}) {
  if (typeof request !== "function") throw new TypeError("request is required");
  return Object.freeze({
    list: async (scope) => request(`${promptDefinitionsEndpoint}?${scopeQuery(scope)}`),
    get: async (id) => request(`${promptDefinitionsEndpoint}/${encodeURIComponent(String(id || ""))}`),
    create: async (scope, draft) => request(promptDefinitionsEndpoint, { method: "POST", body: JSON.stringify(promptRequestBody(draft, scope)) }),
    update: async (id, expectedRevision, draft) => request(`${promptDefinitionsEndpoint}/${encodeURIComponent(String(id || ""))}`, { method: "PUT", body: JSON.stringify(promptRequestBody(draft, null, expectedRevision)) }),
    remove: async (id, expectedRevision) => request(`${promptDefinitionsEndpoint}/${encodeURIComponent(String(id || ""))}`, { method: "DELETE", body: JSON.stringify({ expectedRevision }) }),
    revisions: async (id) => request(`${promptDefinitionsEndpoint}/${encodeURIComponent(String(id || ""))}/revisions`),
    restore: async (id, expectedRevision, sourceRevision) => request(`${promptDefinitionsEndpoint}/${encodeURIComponent(String(id || ""))}/restore`, { method: "POST", body: JSON.stringify({ expectedRevision, sourceRevision }) }),
    effective: async (agentId) => request(`${effectivePromptEndpoint}?agentId=${encodeURIComponent(String(agentId || ""))}`),
  });
}
