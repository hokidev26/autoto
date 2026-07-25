export const agentRoleDefinitionsEndpoint = "/api/agent-role-definitions";
export const effectiveChildRolesEndpoint = "/api/effective-child-roles";
export const canonicalChildRoles = Object.freeze(["general", "executor", "explorer", "reviewer", "tester", "plan", "search"]);

const keyPattern = /^[a-z0-9][a-z0-9._-]{0,63}$/;

export function normalizeDefinitionScope(value = {}) {
  const source = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  const scope = String(source.scope || "").trim().toLowerCase();
  const projectId = String(source.projectId || "").trim();
  const workspaceId = String(source.workspaceId || "").trim();
  if (scope === "global" && !projectId && !workspaceId) return { scope };
  if (scope === "project" && projectId && !workspaceId) return { scope, projectId };
  if (scope === "workspace" && projectId && workspaceId) return { scope, projectId, workspaceId };
  throw new TypeError("scope must be a valid global, project, or workspace target");
}

function normalizeToolList(value, name) {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > 128) throw new TypeError(`${name} must be an array with at most 128 entries`);
  const result = value.map((entry) => String(entry || "").trim());
  if (result.some((entry) => !entry || entry.length > 128 || /[\0\r\n]/.test(entry))) throw new TypeError(`${name} contains an invalid tool name`);
  const keys = result.map((entry) => entry.toLowerCase());
  if (new Set(keys).size !== keys.length) throw new TypeError(`${name} contains duplicate tools`);
  return result.sort((left, right) => left.localeCompare(right, undefined, { sensitivity: "base" }));
}

export function normalizeAgentRoleDraft(value = {}) {
  const source = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  const key = String(source.key || "").trim();
  const displayName = String(source.displayName || "").trim();
  const summary = String(source.summary || "").trim();
  const baseRole = String(source.baseRole || "").trim().toLowerCase();
  const roleExtension = String(source.roleExtension || "").trim();
  if (!keyPattern.test(key)) throw new TypeError("key is invalid");
  if (!displayName || new TextEncoder().encode(displayName).length > 120 || /[\0\r\n]/.test(displayName)) throw new TypeError("displayName is invalid");
  if (new TextEncoder().encode(summary).length > 500 || summary.includes("\0")) throw new TypeError("summary is invalid");
  if (!canonicalChildRoles.includes(baseRole)) throw new TypeError("baseRole is not canonical");
  if (new TextEncoder().encode(roleExtension).length > 16 * 1024 || roleExtension.includes("\0")) throw new TypeError("roleExtension is invalid");
  const toolAllowlist = normalizeToolList(source.toolAllowlist, "toolAllowlist");
  const deniedTools = normalizeToolList(source.deniedTools, "deniedTools");
  const denied = new Set(deniedTools.map((entry) => entry.toLowerCase()));
  if (toolAllowlist.some((entry) => denied.has(entry.toLowerCase()))) throw new TypeError("a tool cannot be both allowed and denied");
  return {
    key, displayName, summary, baseRole, roleExtension, toolAllowlist, deniedTools,
    readOnly: Boolean(source.readOnly), disableExec: Boolean(source.disableExec),
  };
}

function roleRequestBody(draft, scope, expectedRevision) {
  const normalized = normalizeAgentRoleDraft(draft);
  const body = {
    key: normalized.key,
    displayName: normalized.displayName,
    summary: normalized.summary,
    definition: {
      version: 1,
      key: normalized.key,
      displayName: normalized.displayName,
      baseRole: normalized.baseRole,
      roleExtension: normalized.roleExtension,
      toolAllowlist: normalized.toolAllowlist,
      deniedTools: normalized.deniedTools,
      readOnly: normalized.readOnly,
      disableExec: normalized.disableExec,
    },
  };
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

export function createAgentRoleSettingsController({ request } = {}) {
  if (typeof request !== "function") throw new TypeError("request is required");
  return Object.freeze({
    list: async (scope) => request(`${agentRoleDefinitionsEndpoint}?${scopeQuery(scope)}`),
    get: async (id) => request(`${agentRoleDefinitionsEndpoint}/${encodeURIComponent(String(id || ""))}`),
    create: async (scope, draft) => request(agentRoleDefinitionsEndpoint, { method: "POST", body: JSON.stringify(roleRequestBody(draft, scope)) }),
    update: async (id, expectedRevision, draft) => request(`${agentRoleDefinitionsEndpoint}/${encodeURIComponent(String(id || ""))}`, { method: "PUT", body: JSON.stringify(roleRequestBody(draft, null, expectedRevision)) }),
    remove: async (id, expectedRevision) => request(`${agentRoleDefinitionsEndpoint}/${encodeURIComponent(String(id || ""))}`, { method: "DELETE", body: JSON.stringify({ expectedRevision }) }),
    revisions: async (id) => request(`${agentRoleDefinitionsEndpoint}/${encodeURIComponent(String(id || ""))}/revisions`),
    restore: async (id, expectedRevision, sourceRevision) => request(`${agentRoleDefinitionsEndpoint}/${encodeURIComponent(String(id || ""))}/restore`, { method: "POST", body: JSON.stringify({ expectedRevision, sourceRevision }) }),
    effective: async (agentId) => request(`${effectiveChildRolesEndpoint}?agentId=${encodeURIComponent(String(agentId || ""))}`),
  });
}
