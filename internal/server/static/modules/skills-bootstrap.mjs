import { t } from "./messages-skills.mjs";

const SKILL_SCOPES = new Set(["global", "project", "workspace"]);
const FILE_SOURCE_SCOPES = new Set(["user", "project"]);

export function normalizeSkillScope(scope) {
  const value = String(scope || "global").trim().toLowerCase();
  return SKILL_SCOPES.has(value) ? value : "global";
}

// Context keys deliberately include the owning ID. This prevents a response
// for one project/workline being rendered after the user switches context.
export function normalizeSkillContext(context = {}) {
  const scope = normalizeSkillScope(context.scope);
  const projectId = String(context.projectId || "").trim();
  const worklineId = String(context.worklineId || "").trim();
  if (scope === "project") return { scope, projectId, worklineId: "" };
  if (scope === "workspace") return { scope, projectId, worklineId };
  return { scope: "global", projectId: "", worklineId: "" };
}

export function skillContextKey(context) {
  const normalized = normalizeSkillContext(context);
  if (normalized.scope === "project") return `project:${normalized.projectId || "none"}`;
  if (normalized.scope === "workspace") return `workspace:${normalized.projectId || "none"}:${normalized.worklineId || "none"}`;
  return "global";
}

export function skillContextQuery(context, extra = {}) {
  const normalized = normalizeSkillContext(context);
  const params = new URLSearchParams({ scope: normalized.scope });
  if ((normalized.scope === "project" || normalized.scope === "workspace") && normalized.projectId) params.set("projectId", normalized.projectId);
  if (normalized.scope === "workspace" && normalized.worklineId) params.set("worklineId", normalized.worklineId);
  Object.entries(extra).forEach(([key, value]) => {
    if (value !== undefined && value !== null && String(value) !== "") params.set(key, String(value));
  });
  return params;
}

export function buildSkillsV2URL(context, { cursor = "", limit } = {}) {
  const params = skillContextQuery(context, { cursor, limit });
  return `/api/v2/skills/?${params.toString()}`;
}

export function buildSkillDetailV2URL(id, context) {
  return `/api/v2/skills/${encodeURIComponent(id)}?${skillContextQuery(context).toString()}`;
}

export function buildSkillImportPreviewV2URL() {
  return "/api/v2/skills/import/preview";
}

export function buildSkillImportV2URL() {
  return "/api/v2/skills/import";
}

export function normalizeSkillSourceContext(context = {}) {
  const sourceScope = String(context.sourceScope || "user").trim().toLowerCase();
  return {
    sourceScope: FILE_SOURCE_SCOPES.has(sourceScope) ? sourceScope : "user",
    projectId: sourceScope === "project" ? String(context.projectId || "").trim() : "",
  };
}

export function skillSourceContextKey(context = {}) {
  const normalized = normalizeSkillSourceContext(context);
  return normalized.sourceScope === "project" ? `project:${normalized.projectId || "none"}` : "user";
}

export function skillSourceAvailability(context = {}) {
  const normalized = normalizeSkillSourceContext(context);
  return normalized.sourceScope !== "project" || normalized.projectId
    ? { available: true, reason: "" }
    : { available: false, reason: "project" };
}

export function buildSkillSourcesV2URL(context = {}) {
  const normalized = normalizeSkillSourceContext(context);
  const params = new URLSearchParams({ sourceScope: normalized.sourceScope });
  if (normalized.sourceScope === "project" && normalized.projectId) params.set("projectId", normalized.projectId);
  return `/api/v2/skills/sources?${params.toString()}`;
}

export function buildSkillSourceImportV2URL() {
  return "/api/v2/skills/sources/import";
}

export function createSkillSourceImportPayload(candidate, sourceContext, targetContext, { enabled = false, acknowledgeRisk = false } = {}) {
  const source = normalizeSkillSourceContext(sourceContext);
  const provenance = candidate?.provenance || {};
  const payload = {
    sourceScope: source.sourceScope,
    provenance: {
      rootId: String(provenance.rootId || ""),
      adapterId: String(provenance.adapterId || ""),
      relativePath: String(provenance.relativePath || candidate?.relativePath || ""),
    },
    sourceHash: String(candidate?.sourceHash || ""),
    sidecarSourceHash: String(candidate?.sidecarSourceHash || ""),
    target: skillContextPayload(targetContext),
    enabled: Boolean(enabled),
    acknowledgeRisk: Boolean(acknowledgeRisk),
  };
  if (source.sourceScope === "project") payload.projectId = source.projectId;
  return payload;
}

export function skillContextPayload(context) {
  const normalized = normalizeSkillContext(context);
  const payload = { scope: normalized.scope };
  if (normalized.scope === "project" || normalized.scope === "workspace") payload.projectId = normalized.projectId;
  if (normalized.scope === "workspace") payload.worklineId = normalized.worklineId;
  return payload;
}

export function skillContextAvailability(context) {
  const normalized = normalizeSkillContext(context);
  if (normalized.scope === "project" && !normalized.projectId) return { available: false, reason: "project" };
  if (normalized.scope === "workspace" && (!normalized.projectId || !normalized.worklineId)) return { available: false, reason: normalized.projectId ? "workspace" : "project" };
  return { available: true, reason: "" };
}

export function buildSkillRevisionsV2URL(id, context, { cursor = "", limit } = {}) {
  const params = skillContextQuery(context, { cursor, limit });
  return `/api/v2/skills/${encodeURIComponent(id)}/revisions?${params.toString()}`;
}

export function buildSkillRevisionDetailV2URL(id, revisionId, context) {
  return `/api/v2/skills/${encodeURIComponent(id)}/revisions/${encodeURIComponent(revisionId)}?${skillContextQuery(context).toString()}`;
}

export function buildSkillRevisionRestoreV2URL(id, revisionId, context) {
  return `/api/v2/skills/${encodeURIComponent(id)}/revisions/${encodeURIComponent(revisionId)}/restore?${skillContextQuery(context).toString()}`;
}

export function buildEffectiveSkillsV2URL(agentId, context, { cursor = "", limit } = {}) {
  const params = skillContextQuery(context, { cursor, limit });
  return `/api/v2/agents/${encodeURIComponent(agentId)}/skills/effective?${params.toString()}`;
}

export function normalizeSkillsPage(response = {}) {
  const source = Array.isArray(response) ? { items: response } : (response || {});
  return {
    items: Array.isArray(source.items) ? source.items : [],
    nextCursor: String(source.nextCursor || ""),
    snapshotSequence: source.snapshotSequence ?? null,
  };
}

export function createSkillRestorePayload(revision, { expectedUpdatedAt, acknowledgeRisk = false, acknowledgedContentHash = "" } = {}) {
  const revisionNo = Number(revision?.revisionNo ?? revision?.revision);
  if (!Number.isSafeInteger(revisionNo) || revisionNo < 1) throw new Error(t("skillsWorkbench.errors.restoreRevisionMissing"));
  const updatedAt = String(expectedUpdatedAt || "").trim();
  if (!updatedAt) throw new Error(t("skillsWorkbench.errors.restoreTimestampMissing"));
  return {
    revisionNo,
    expectedUpdatedAt: updatedAt,
    acknowledgeRisk: Boolean(acknowledgeRisk),
    acknowledgedContentHash: String(acknowledgedContentHash || "").trim(),
  };
}

export function loadServerSkillsWithFallback(load, previousSkills = [], { hadServerData = false } = {}) {
  const previous = Array.isArray(previousSkills) ? previousSkills : [];
  return Promise.resolve().then(load).then((skills) => ({ skills: Array.isArray(skills) ? skills : [], status: "ready", error: "" })).catch((err) => ({
    skills: previous,
    status: hadServerData || previous.length ? "stale" : "error",
    error: err?.message || String(err),
  }));
}

// Returns false for a stale completion, making state writes safe when callers
// deliberately issue a newer refresh before an earlier request resolves.
export function applyServerSkillsLoadResult(state, seq, result) {
  if (seq !== state.serverSkillsLoadSeq) return false;
  state.serverSkills = Array.isArray(result?.skills) ? result.skills : [];
  state.serverSkillsStatus = result?.status || "error";
  state.serverSkillsError = result?.error || "";
  return true;
}

export function isOptimisticSkillConflict(error) {
  const detail = String(error?.body?.error || error?.message || "");
  return error?.status === 409 && /updated by another client/i.test(detail);
}

export async function hydrateServerSkillSummaries(summaries, loadDetail, concurrency = 4) {
  const items = Array.isArray(summaries) ? summaries.map((item) => ({ ...item, detailLoaded: false })) : [];
  const enabledIndexes = items.map((item, index) => item?.enabled ? index : -1).filter((index) => index >= 0);
  const workerCount = Math.min(Math.max(1, Number(concurrency) || 1), enabledIndexes.length);
  let next = 0;
  async function worker() {
    while (next < enabledIndexes.length) {
      const index = enabledIndexes[next++];
      const summary = items[index];
      try {
        const detail = await loadDetail(summary.id);
        items[index] = { ...summary, ...detail, detailLoaded: true, detailError: "" };
      } catch (err) {
        // Retain the authoritative command shadow so it still reserves its
        // name, but intentionally omit the prompt to fail closed.
        items[index] = { ...summary, detailLoaded: false, detailError: err?.message || String(err) };
      }
    }
  }
  await Promise.all(Array.from({ length: workerCount }, worker));
  return items;
}

function phaseBRoot(state) {
  if (!state.skillsV2 || typeof state.skillsV2 !== "object") state.skillsV2 = { contexts: {}, effective: {}, sources: {} };
  if (!state.skillsV2.contexts || typeof state.skillsV2.contexts !== "object") state.skillsV2.contexts = {};
  if (!state.skillsV2.effective || typeof state.skillsV2.effective !== "object") state.skillsV2.effective = {};
  if (!state.skillsV2.sources || typeof state.skillsV2.sources !== "object") state.skillsV2.sources = {};
  return state.skillsV2;
}

export function ensureSkillSourceState(state, context) {
  const root = phaseBRoot(state);
  const normalized = normalizeSkillSourceContext(context);
  const key = skillSourceContextKey(normalized);
  if (!root.sources[key]) {
    root.sources[key] = {
      context: normalized,
      candidates: [], conflicts: [], diagnostics: [], truncated: false, bytesRead: 0,
      status: "idle", error: "", requestSequence: 0, importing: {},
    };
  }
  const sourceState = root.sources[key];
  if (!sourceState.importing || typeof sourceState.importing !== "object") sourceState.importing = {};
  return sourceState;
}

export function ensureSkillsContextState(state, context) {
  const root = phaseBRoot(state);
  const normalized = normalizeSkillContext(context);
  const key = skillContextKey(normalized);
  if (!root.contexts[key]) {
    root.contexts[key] = {
      context: normalized, items: [], nextCursor: "", snapshotSequence: null,
      status: "idle", error: "", requestSequence: 0, revisions: {}, drawer: null,
      mutations: {}, importPreview: { status: "idle", error: "", requestSequence: 0, content: "", value: null },
      drafts: { create: {}, edits: {}, importContent: "" },
    };
  }
  const bucket = root.contexts[key];
  if (!bucket.mutations || typeof bucket.mutations !== "object") bucket.mutations = {};
  if (!bucket.importPreview || typeof bucket.importPreview !== "object") bucket.importPreview = { status: "idle", error: "", requestSequence: 0, content: "", value: null };
  if (!bucket.drafts || typeof bucket.drafts !== "object") bucket.drafts = { create: {}, edits: {}, importContent: "" };
  if (!bucket.drafts.create || typeof bucket.drafts.create !== "object") bucket.drafts.create = {};
  if (!bucket.drafts.edits || typeof bucket.drafts.edits !== "object") bucket.drafts.edits = {};
  return bucket;
}

function sameSnapshotSequence(expected, actual) {
  return expected !== null && actual !== null && String(expected) === String(actual);
}

function resetPageCursor(pageState, { clearItems = false } = {}) {
  pageState.requestSequence += 1;
  pageState.nextCursor = "";
  pageState.snapshotSequence = null;
  if (clearItems) pageState.items = [];
}

// A reusable v2 controller. It is intentionally independent from app-main so
// callers can choose their own renderer while retaining context isolation,
// snapshot-consistent pagination, and fail-closed effective-policy semantics.
export function createSkillsPhaseBController({ state = {}, api, getContext = () => ({}), pageSize = 50, onChange, onEffectiveInvalidated } = {}) {
  if (typeof api !== "function") throw new Error("createSkillsPhaseBController requires api");
  const changed = () => onChange?.();
  const currentContext = (override) => normalizeSkillContext(override || getContext() || {});
  const effectiveKey = (agentId, context) => `${String(agentId || "")}:${skillContextKey(context)}`;

  async function load(contextOverride, { append = false } = {}) {
    const context = currentContext(contextOverride);
    const bucket = ensureSkillsContextState(state, context);
    const cursor = append ? bucket.nextCursor : "";
    if (append && !cursor) return bucket;
    const requestSequence = ++bucket.requestSequence;
    bucket.status = append ? "loading-more" : "loading";
    bucket.error = "";
    changed();
    try {
      const page = normalizeSkillsPage(await api(buildSkillsV2URL(context, { cursor, limit: pageSize })));
      // A scope switch / newer refresh cannot write into the active bucket.
      if (requestSequence !== bucket.requestSequence || skillContextKey(bucket.context) !== skillContextKey(context)) return bucket;
      if (append && !sameSnapshotSequence(bucket.snapshotSequence, page.snapshotSequence)) {
        resetPageCursor(bucket, { clearItems: true });
        bucket.status = "loading";
        bucket.error = t("skillsWorkbench.errors.pageSnapshotChanged");
        changed();
        return load(context);
      }
      bucket.items = append ? [...bucket.items, ...page.items] : page.items;
      bucket.nextCursor = page.nextCursor;
      bucket.snapshotSequence = page.snapshotSequence;
      bucket.status = "ready";
      bucket.error = "";
    } catch (error) {
      if (requestSequence === bucket.requestSequence) {
        bucket.status = bucket.items.length ? "stale" : "error";
        bucket.error = error?.message || String(error);
      }
      throw error;
    } finally {
      changed();
    }
    return bucket;
  }

  async function loadMore(context) { return load(context, { append: true }); }

  async function loadSources(sourceContextOverride) {
    const sourceContext = normalizeSkillSourceContext(sourceContextOverride);
    const availability = skillSourceAvailability(sourceContext);
    const sourceState = ensureSkillSourceState(state, sourceContext);
    if (!availability.available) {
      sourceState.status = "error";
      sourceState.error = t("skillsWorkbench.errors.fileSourceProjectMissing");
      changed();
      return sourceState;
    }
    const requestSequence = ++sourceState.requestSequence;
    sourceState.status = "loading";
    sourceState.error = "";
    changed();
    try {
      const response = await api(buildSkillSourcesV2URL(sourceContext));
      if (requestSequence !== sourceState.requestSequence) return sourceState;
      const result = response?.result || {};
      sourceState.candidates = Array.isArray(result.candidates) ? result.candidates : [];
      sourceState.conflicts = Array.isArray(result.conflicts) ? result.conflicts : [];
      sourceState.diagnostics = Array.isArray(result.diagnostics) ? result.diagnostics : [];
      sourceState.truncated = Boolean(result.truncated);
      sourceState.bytesRead = Number(result.bytesRead || 0);
      sourceState.status = "ready";
      sourceState.error = "";
    } catch (error) {
      if (requestSequence === sourceState.requestSequence) {
        sourceState.status = sourceState.candidates.length ? "stale" : "error";
        sourceState.error = error?.message || String(error);
      }
      throw error;
    } finally {
      changed();
    }
    return sourceState;
  }

  async function loadDetail(id, contextOverride) {
    const context = currentContext(contextOverride);
    const bucket = ensureSkillsContextState(state, context);
    const index = bucket.items.findIndex((item) => item.id === id);
    if (index < 0) throw new Error(t("skillsWorkbench.errors.skillNotFound"));
    const requests = bucket.detailRequestSequences || (bucket.detailRequestSequences = {});
    const requestSequence = requests[id] = Number(requests[id] || 0) + 1;
    const before = bucket.items[index];
    try {
      const detail = await api(buildSkillDetailV2URL(id, context));
      if (requestSequence !== requests[id]) return null;
      const latestIndex = bucket.items.findIndex((item) => item.id === id);
      if (latestIndex >= 0) bucket.items[latestIndex] = { ...bucket.items[latestIndex], ...detail, detailLoaded: true, detailError: "" };
      changed();
      return bucket.items[latestIndex];
    } catch (error) {
      if (requestSequence !== requests[id]) return null;
      // Do not retain a previously loaded prompt after a failed v2 detail
      // refresh. The authoritative item still shadows local command names.
      const latestIndex = bucket.items.findIndex((item) => item.id === id);
      if (latestIndex >= 0) {
        const { prompt, ...summary } = bucket.items[latestIndex];
        bucket.items[latestIndex] = { ...summary, ...before, prompt: undefined, detailLoaded: false, detailError: error?.message || String(error) };
      }
      changed();
      throw error;
    }
  }

  async function loadRevisions(id, contextOverride, { append = false } = {}) {
    const context = currentContext(contextOverride);
    const bucket = ensureSkillsContextState(state, context);
    const revisionState = bucket.revisions[id] || { items: [], nextCursor: "", snapshotSequence: null, status: "idle", error: "", requestSequence: 0 };
    bucket.revisions[id] = revisionState;
    const cursor = append ? revisionState.nextCursor : "";
    if (append && !cursor) return revisionState;
    const requestSequence = ++revisionState.requestSequence;
    revisionState.status = append ? "loading-more" : "loading";
    revisionState.error = "";
    changed();
    try {
      const page = normalizeSkillsPage(await api(buildSkillRevisionsV2URL(id, context, { cursor, limit: pageSize })));
      if (requestSequence !== revisionState.requestSequence) return revisionState;
      if (append && !sameSnapshotSequence(revisionState.snapshotSequence, page.snapshotSequence)) {
        resetPageCursor(revisionState, { clearItems: true });
        revisionState.status = "loading";
        revisionState.error = t("skillsWorkbench.errors.revisionSnapshotChanged");
        changed();
        return loadRevisions(id, context);
      }
      revisionState.items = append ? [...revisionState.items, ...page.items] : page.items;
      revisionState.nextCursor = page.nextCursor;
      revisionState.snapshotSequence = page.snapshotSequence;
      revisionState.status = "ready";
      revisionState.error = "";
    } catch (error) {
      if (requestSequence === revisionState.requestSequence) {
        revisionState.status = revisionState.items.length ? "stale" : "error";
        revisionState.error = error?.message || String(error);
      }
      throw error;
    } finally {
      changed();
    }
    return revisionState;
  }

  async function loadRevisionDetail(id, revisionId, contextOverride) {
    const context = currentContext(contextOverride);
    return api(buildSkillRevisionDetailV2URL(id, revisionId, context));
  }

  function mutationEntry(bucket, key) {
    if (!bucket.mutations[key]) bucket.mutations[key] = { status: "idle", error: "", requestSequence: 0 };
    return bucket.mutations[key];
  }

  async function runMutation(key, context, execute, applyResult) {
    const bucket = ensureSkillsContextState(state, context);
    const mutation = mutationEntry(bucket, key);
    const requestSequence = ++mutation.requestSequence;
    mutation.status = "saving";
    mutation.error = "";
    changed();
    try {
      const result = await execute();
      if (requestSequence !== mutation.requestSequence) return null;
      applyResult?.(result, bucket);
      mutation.status = "ready";
      mutation.error = "";
      resetPageCursor(bucket);
      invalidateEffective({ drop: true });
      onEffectiveInvalidated?.();
      changed();
      await Promise.allSettled([load(context)]);
      return result;
    } catch (error) {
      if (requestSequence !== mutation.requestSequence) return null;
      mutation.status = "error";
      mutation.error = error?.message || String(error);
      if (isOptimisticSkillConflict(error)) await Promise.allSettled([load(context)]);
      changed();
      throw error;
    }
  }

  async function previewImport(content, contextOverride) {
    const context = currentContext(contextOverride);
    const bucket = ensureSkillsContextState(state, context);
    const previewState = bucket.importPreview;
    const requestSequence = ++previewState.requestSequence;
    previewState.status = "loading";
    previewState.error = "";
    previewState.content = String(content || "");
    changed();
    try {
      const preview = await api(buildSkillImportPreviewV2URL(), {
        method: "POST", body: JSON.stringify({ content: previewState.content }),
      });
      if (requestSequence !== previewState.requestSequence) return null;
      previewState.value = preview;
      previewState.status = "ready";
      previewState.error = "";
      changed();
      return preview;
    } catch (error) {
      if (requestSequence !== previewState.requestSequence) return null;
      previewState.value = null;
      previewState.status = "error";
      previewState.error = error?.message || String(error);
      changed();
      throw error;
    }
  }

  async function createSkill(payload, contextOverride) {
    const context = currentContext(contextOverride);
    const body = {
      ...payload,
      ...skillContextPayload(context),
      enabled: Boolean(payload?.enabled),
      acknowledgeRisk: Boolean(payload?.acknowledgeRisk),
    };
    return runMutation("create", context, () => api(buildSkillsV2URL(context), {
      method: "POST", body: JSON.stringify(body),
    }), (created, bucket) => {
      if (created?.id) bucket.items = [created, ...bucket.items.filter((item) => item.id !== created.id)];
    });
  }

  async function updateSkill(id, payload, contextOverride) {
    const context = currentContext(contextOverride);
    const bucket = ensureSkillsContextState(state, context);
    const current = bucket.items.find((item) => item.id === id);
    const expectedUpdatedAt = String(payload?.expectedUpdatedAt || current?.updatedAt || "").trim();
    if (!expectedUpdatedAt) throw new Error(t("skillsWorkbench.errors.mutationTimestampMissing"));
    const body = { ...payload, expectedUpdatedAt };
    delete body.scope;
    delete body.projectId;
    delete body.worklineId;
    return runMutation(`skill:${id}`, context, () => api(buildSkillDetailV2URL(id, context), {
      method: "PATCH", body: JSON.stringify(body),
    }), (updated, targetBucket) => {
      const index = targetBucket.items.findIndex((item) => item.id === id);
      if (index >= 0) targetBucket.items[index] = { ...targetBucket.items[index], ...updated, detailLoaded: true, detailError: "" };
    });
  }

  async function toggleSkill(id, enabled, contextOverride, { acknowledgeRisk = false, expectedUpdatedAt = "" } = {}) {
    return updateSkill(id, { enabled: Boolean(enabled), acknowledgeRisk: Boolean(acknowledgeRisk), expectedUpdatedAt }, contextOverride);
  }

  async function deleteSkill(id, contextOverride, { expectedUpdatedAt = "" } = {}) {
    const context = currentContext(contextOverride);
    const bucket = ensureSkillsContextState(state, context);
    const current = bucket.items.find((item) => item.id === id);
    const timestamp = String(expectedUpdatedAt || current?.updatedAt || "").trim();
    if (!timestamp) throw new Error(t("skillsWorkbench.errors.mutationTimestampMissing"));
    return runMutation(`skill:${id}`, context, () => api(buildSkillDetailV2URL(id, context), {
      method: "DELETE", body: JSON.stringify({ expectedUpdatedAt: timestamp }),
    }), (_deleted, targetBucket) => {
      targetBucket.items = targetBucket.items.filter((item) => item.id !== id);
      delete targetBucket.revisions[id];
      if (targetBucket.drawer?.skillId === id) targetBucket.drawer = null;
    });
  }

  async function importSkill(content, contextOverride, options = {}) {
    const context = currentContext(contextOverride);
    const body = {
      content: String(content || ""),
      ...skillContextPayload(context),
      enabled: Boolean(options.enabled),
      acknowledgeRisk: Boolean(options.acknowledgeRisk),
    };
    return runMutation("import", context, () => api(buildSkillImportV2URL(), {
      method: "POST", body: JSON.stringify(body),
    }), (created, bucket) => {
      if (created?.id) bucket.items = [created, ...bucket.items.filter((item) => item.id !== created.id)];
      bucket.importPreview = { status: "idle", error: "", requestSequence: bucket.importPreview.requestSequence + 1, content: "", value: null };
    });
  }

  async function importSource(candidate, sourceContextOverride, targetContextOverride, options = {}) {
    const sourceContext = normalizeSkillSourceContext(sourceContextOverride);
    const targetContext = currentContext(targetContextOverride);
    const sourceState = ensureSkillSourceState(state, sourceContext);
    const key = `${candidate?.provenance?.adapterId || "source"}:${candidate?.provenance?.relativePath || candidate?.relativePath || "candidate"}`;
    sourceState.importing[key] = true;
    changed();
    try {
      return await runMutation(`source:${key}`, targetContext, () => api(buildSkillSourceImportV2URL(), {
        method: "POST",
        body: JSON.stringify(createSkillSourceImportPayload(candidate, sourceContext, targetContext, options)),
      }), (created, bucket) => {
        if (created?.id) bucket.items = [created, ...bucket.items.filter((item) => item.id !== created.id)];
      });
    } finally {
      delete sourceState.importing[key];
      changed();
    }
  }

  function invalidateEffective({ drop = true } = {}) {
    const root = phaseBRoot(state);
    Object.values(root.effective).forEach((entry) => {
      entry.requestSequence = Number(entry.requestSequence || 0) + 1;
      entry.status = "idle";
      entry.error = "";
      entry.nextCursor = "";
      entry.snapshotSequence = null;
      if (drop) {
        entry.items = [];
        entry.hasAuthoritativeData = false;
      }
    });
    changed();
  }

  function getEffectivePolicy(agentId, contextOverride) {
    const context = currentContext(contextOverride);
    return phaseBRoot(state).effective[effectiveKey(agentId, context)] || {
      items: [], status: "idle", error: "", hasAuthoritativeData: false, snapshotSequence: null,
    };
  }

  async function loadEffective(agentId, contextOverride, { restarted = false } = {}) {
    const normalizedAgentId = String(agentId || "").trim();
    if (!normalizedAgentId) throw new Error(t("skillsWorkbench.errors.agentMissing"));
    const context = currentContext(contextOverride);
    const key = effectiveKey(normalizedAgentId, context);
    const root = phaseBRoot(state);
    const entry = root.effective[key] || {
      items: [], status: "idle", error: "", requestSequence: 0,
      hasAuthoritativeData: false, snapshotSequence: null, nextCursor: "",
    };
    root.effective[key] = entry;
    const hadAuthoritativeData = Boolean(entry.hasAuthoritativeData);
    const lastKnownItems = Array.isArray(entry.items) ? entry.items : [];
    const lastKnownSnapshot = entry.snapshotSequence;
    const requestSequence = ++entry.requestSequence;
    entry.status = "loading";
    entry.error = "";
    changed();
    try {
      const items = [];
      let cursor = "";
      let snapshotSequence = null;
      do {
        const page = normalizeSkillsPage(await api(buildEffectiveSkillsV2URL(normalizedAgentId, context, { cursor, limit: pageSize })));
        if (requestSequence !== entry.requestSequence) return entry.items;
        if (snapshotSequence !== null && !sameSnapshotSequence(snapshotSequence, page.snapshotSequence)) {
          if (restarted) throw new Error(t("skillsWorkbench.errors.effectiveSnapshotMismatch"));
          resetPageCursor(entry, { clearItems: true });
          entry.hasAuthoritativeData = false;
          entry.status = "loading";
          entry.error = t("skillsWorkbench.errors.effectiveSnapshotChanged");
          changed();
          return loadEffective(normalizedAgentId, context, { restarted: true });
        }
        snapshotSequence = page.snapshotSequence;
        items.push(...page.items);
        cursor = page.nextCursor;
      } while (cursor);
      if (requestSequence === entry.requestSequence) {
        entry.items = items;
        entry.nextCursor = "";
        entry.snapshotSequence = snapshotSequence;
        entry.hasAuthoritativeData = true;
        entry.status = "ready";
        entry.error = "";
      }
      return entry.items;
    } catch (error) {
      if (requestSequence === entry.requestSequence) {
        entry.items = hadAuthoritativeData ? lastKnownItems : [];
        entry.snapshotSequence = hadAuthoritativeData ? lastKnownSnapshot : null;
        entry.hasAuthoritativeData = hadAuthoritativeData;
        entry.status = hadAuthoritativeData ? "stale" : "error";
        entry.error = error?.message || String(error);
      }
      throw error;
    } finally {
      changed();
    }
  }

  async function restoreRevision(id, revision, contextOverride, options = {}) {
    const context = currentContext(contextOverride);
    const payload = createSkillRestorePayload(revision, {
      expectedUpdatedAt: options.expectedUpdatedAt,
      acknowledgeRisk: options.acknowledgeRisk,
      acknowledgedContentHash: options.acknowledgedContentHash,
    });
    const restored = await runMutation(`skill:${id}`, context, () => api(buildSkillRevisionRestoreV2URL(id, payload.revisionNo, context), {
      method: "POST", body: JSON.stringify(payload),
    }), (result, bucket) => {
      delete bucket.revisions[id];
      const index = bucket.items.findIndex((item) => item.id === id);
      if (index >= 0) bucket.items[index] = { ...bucket.items[index], ...result, detailLoaded: true, detailError: "" };
    });
    if (restored) await Promise.allSettled([loadRevisions(id, context)]);
    return restored;
  }

  return {
    ensureContext: (context) => ensureSkillsContextState(state, currentContext(context)),
    ensureSources: (context) => ensureSkillSourceState(state, normalizeSkillSourceContext(context)),
    getEffectivePolicy,
    invalidateEffective,
    load,
    loadMore,
    loadSources,
    loadDetail,
    loadRevisions,
    loadRevisionDetail,
    previewImport,
    createSkill,
    updateSkill,
    toggleSkill,
    deleteSkill,
    importSkill,
    importSource,
    restoreRevision,
    loadEffective,
  };
}
