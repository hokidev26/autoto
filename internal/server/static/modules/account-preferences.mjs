import { defaultProfilePrefs } from "./preferences-data.mjs";
import { normalizeAvatarDataUrl } from "./profile-avatar.mjs";

export const accountPreferencesImportVersion = 1;
export const accountPreferencesCurrentSetupVersion = 1;
export const accountPreferenceFields = Object.freeze(["profile", "preferredModel", "modelVisibility", "setupVersion"]);
export const accountPreferenceLegacyKeys = Object.freeze({
  profile: ["autoto.profile"],
  preferredModel: ["autoto.preferredModel"],
  modelVisibility: ["autoto.modelVisibility"],
});

const cachePrefix = "autoto.accountPreferences.cache.";
const pendingPrefix = "autoto.accountPreferences.pending.";

export function normalizeAccountProfile(value = {}) {
  const source = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  const displayName = String(source.displayName || "").trim().slice(0, 80);
  const roleLabel = String(source.roleLabel || defaultProfilePrefs.roleLabel).trim().slice(0, 80) || defaultProfilePrefs.roleLabel;
  const avatarInitials = String(source.avatarInitials || defaultProfilePrefs.avatarInitials).trim().slice(0, 4).toUpperCase() || defaultProfilePrefs.avatarInitials;
  const avatarDataUrl = normalizeAvatarDataUrl(source.avatarDataUrl);
  const gitName = String(source.gitName || "").trim().slice(0, 120);
  const gitEmail = String(source.gitEmail || "").trim().slice(0, 160);
  const workspaceLabel = String(source.workspaceLabel || defaultProfilePrefs.workspaceLabel).trim().slice(0, 80) || defaultProfilePrefs.workspaceLabel;
  return { displayName, roleLabel, avatarInitials, avatarDataUrl, gitName, gitEmail, workspaceLabel };
}

export function normalizeModelVisibility(value = {}) {
  const source = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  const rawHiddenModels = source.hiddenModels && typeof source.hiddenModels === "object" && !Array.isArray(source.hiddenModels)
    ? source.hiddenModels
    : {};
  const hiddenModels = Object.fromEntries(Object.entries(rawHiddenModels)
    .map(([key, hidden]) => [String(key || "").trim().slice(0, 240), Boolean(hidden)])
    .filter(([key, hidden]) => key && hidden));
  return { hiddenModels, showUnconfiguredProviders: Boolean(source.showUnconfiguredProviders) };
}

export function normalizeSetupVersion(value) {
  const normalized = Math.trunc(Number(value) || 0);
  return Math.min(accountPreferencesCurrentSetupVersion, Math.max(0, normalized));
}

export function normalizeAccountPreferencesSnapshot(value = {}, fallback = {}) {
  const source = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  return {
    scopeKey: String(source.scopeKey ?? fallback.scopeKey ?? "").trim(),
    profile: normalizeAccountProfile(source.profile ?? fallback.profile ?? defaultProfilePrefs),
    preferredModel: String(source.preferredModel ?? fallback.preferredModel ?? "").trim().slice(0, 240),
    modelVisibility: normalizeModelVisibility(source.modelVisibility ?? fallback.modelVisibility ?? {}),
    setupVersion: normalizeSetupVersion(source.setupVersion ?? fallback.setupVersion ?? 0),
    revision: Math.max(0, Number(source.revision ?? fallback.revision ?? 0) || 0),
    localStorageImportVersion: Math.max(0, Number(source.localStorageImportVersion ?? fallback.localStorageImportVersion ?? 0) || 0),
    updatedAt: String(source.updatedAt ?? fallback.updatedAt ?? "").trim(),
  };
}

function scopeStorageKey(prefix, scopeKey) {
  return `${prefix}${encodeURIComponent(String(scopeKey || ""))}`;
}

function safeParseJSON(raw) {
  if (!raw) return null;
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
}

function hasOwn(value, key) {
  return Object.prototype.hasOwnProperty.call(value || {}, key);
}

export function createAccountPreferencesController({
  request,
  storage = globalThis.localStorage,
  eventTarget = globalThis.window,
  onChange,
} = {}) {
  if (typeof request !== "function") throw new TypeError("account preferences request is required");

  let snapshot = normalizeAccountPreferencesSnapshot();
  let status = "idle";
  let loadPromise = null;
  let savePromise = null;
  let memoryPending = {};
  let memoryPendingScopeKey = "";
  const subscribers = new Set();

  function notify() {
    const detail = { snapshot: getSnapshot(), status, pending: hasPendingPatch() };
    onChange?.(detail);
    subscribers.forEach((subscriber) => subscriber(detail));
  }

  function getSnapshot() {
    return {
      ...snapshot,
      profile: { ...snapshot.profile },
      modelVisibility: { ...snapshot.modelVisibility, hiddenModels: { ...snapshot.modelVisibility.hiddenModels } },
    };
  }

  function getStatus() {
    return status;
  }

  function activeCacheKey() {
    return snapshot.scopeKey ? scopeStorageKey(cachePrefix, snapshot.scopeKey) : "";
  }

  function activePendingKey() {
    return snapshot.scopeKey ? scopeStorageKey(pendingPrefix, snapshot.scopeKey) : "";
  }

  function readNamedJSON(key) {
    if (!key) return null;
    try {
      return safeParseJSON(storage?.getItem?.(key));
    } catch {
      return null;
    }
  }

  function writeNamedJSON(key, value) {
    if (!key) return;
    try {
      storage?.setItem?.(key, JSON.stringify(value));
    } catch {}
  }

  function removeNamed(key) {
    if (!key) return;
    try {
      storage?.removeItem?.(key);
    } catch {}
  }

  function persistCache() {
    writeNamedJSON(activeCacheKey(), snapshot);
  }

  function normalizePatch(patch = {}) {
    const normalized = {};
    if (hasOwn(patch, "profile")) normalized.profile = normalizeAccountProfile(patch.profile);
    if (hasOwn(patch, "preferredModel")) normalized.preferredModel = String(patch.preferredModel || "").trim().slice(0, 240);
    if (hasOwn(patch, "modelVisibility")) normalized.modelVisibility = normalizeModelVisibility(patch.modelVisibility);
    if (hasOwn(patch, "setupVersion")) normalized.setupVersion = normalizeSetupVersion(patch.setupVersion);
    return normalized;
  }

  function readPendingPatch() {
    const named = normalizePatch(readNamedJSON(activePendingKey()) || {});
    const memory = memoryPendingScopeKey === snapshot.scopeKey ? memoryPending : {};
    return normalizePatch({ ...named, ...memory });
  }

  function persistPendingPatch(patch) {
    const normalized = normalizePatch(patch);
    memoryPending = normalized;
    memoryPendingScopeKey = snapshot.scopeKey;
    if (snapshot.scopeKey) writeNamedJSON(activePendingKey(), normalized);
  }

  function clearPendingPatch() {
    memoryPending = {};
    memoryPendingScopeKey = "";
    removeNamed(activePendingKey());
  }

  function hasPendingPatch() {
    return Object.keys(readPendingPatch()).length > 0;
  }

  function applyPatch(patch, { persist = true } = {}) {
    const normalized = normalizePatch(patch);
    snapshot = normalizeAccountPreferencesSnapshot({ ...snapshot, ...normalized }, snapshot);
    if (persist && snapshot.scopeKey) persistCache();
    notify();
    return getSnapshot();
  }

  function applyServerSnapshot(value) {
    const incomingScopeKey = String(value?.scopeKey || "").trim();
    const previousScopeKey = snapshot.scopeKey;
    const next = normalizeAccountPreferencesSnapshot(
      value,
      incomingScopeKey && incomingScopeKey !== previousScopeKey ? {} : snapshot,
    );
    if (!next.scopeKey) throw new Error("preferences response is missing scopeKey");
    if (previousScopeKey && previousScopeKey !== next.scopeKey) {
      memoryPending = {};
      memoryPendingScopeKey = "";
    }
    snapshot = next;
    if (!previousScopeKey && memoryPendingScopeKey === "" && Object.keys(memoryPending).length) {
      memoryPendingScopeKey = snapshot.scopeKey;
      writeNamedJSON(activePendingKey(), memoryPending);
    }
    persistCache();
    notify();
    return getSnapshot();
  }

  function loadActiveScopeCache() {
    if (!snapshot.scopeKey) return false;
    const cached = readNamedJSON(activeCacheKey());
    if (!cached || String(cached.scopeKey || "") !== snapshot.scopeKey) return false;
    snapshot = normalizeAccountPreferencesSnapshot(cached, snapshot);
    const pending = readPendingPatch();
    if (Object.keys(pending).length) snapshot = normalizeAccountPreferencesSnapshot({ ...snapshot, ...pending }, snapshot);
    status = "offline";
    notify();
    return true;
  }

  function clearActiveScope() {
    const cacheKey = activeCacheKey();
    const pendingKey = activePendingKey();
    removeNamed(cacheKey);
    removeNamed(pendingKey);
    memoryPending = {};
    memoryPendingScopeKey = "";
    snapshot = normalizeAccountPreferencesSnapshot();
    status = "unauthorized";
    notify();
  }

  function handleRequestFailure(error) {
    if ([401, 403].includes(Number(error?.status))) {
      clearActiveScope();
      return;
    }
    if (!loadActiveScopeCache()) {
      status = "offline";
      notify();
    }
  }

  function readLegacyImport() {
    const values = {};
    const consumedKeys = [];
    for (const [field, keys] of Object.entries(accountPreferenceLegacyKeys)) {
      for (const key of keys) {
        let raw = null;
        try {
          raw = storage?.getItem?.(key);
        } catch {}
        if (raw === null) continue;
        consumedKeys.push(key);
        if (!hasOwn(values, field)) values[field] = raw;
      }
    }
    let profile = snapshot.profile;
    let modelVisibility = snapshot.modelVisibility;
    if (hasOwn(values, "profile")) profile = normalizeAccountProfile(safeParseJSON(values.profile) || {});
    if (hasOwn(values, "modelVisibility")) modelVisibility = normalizeModelVisibility(safeParseJSON(values.modelVisibility) || {});
    return {
      body: {
        version: accountPreferencesImportVersion,
        profile,
        preferredModel: hasOwn(values, "preferredModel") ? String(values.preferredModel || "").trim().slice(0, 240) : snapshot.preferredModel,
        modelVisibility,
      },
      consumedKeys,
    };
  }

  function removeLegacyImportKeys() {
    Object.values(accountPreferenceLegacyKeys).flat().forEach((key) => {
      try {
        storage?.removeItem?.(key);
      } catch {}
    });
  }

  async function refresh() {
    try {
      const response = await request("/api/preferences");
      status = "synced";
      return applyServerSnapshot(response);
    } catch (error) {
      handleRequestFailure(error);
      throw error;
    }
  }

  async function importLegacyIfNeeded() {
    if (snapshot.localStorageImportVersion >= accountPreferencesImportVersion) return false;
    const legacy = readLegacyImport();
    const response = await request("/api/preferences/import-local", {
      method: "POST",
      body: JSON.stringify(legacy.body),
    });
    removeLegacyImportKeys();
    if (response?.scopeKey) applyServerSnapshot(response);
    else await refresh();
    return true;
  }

  async function replayPendingPatch({ conflictRetry = true } = {}) {
    const pending = readPendingPatch();
    if (!Object.keys(pending).length || !snapshot.scopeKey) return getSnapshot();
    status = "saving";
    notify();
    try {
      const response = await request("/api/preferences", {
        method: "PATCH",
        body: JSON.stringify({ expectedRevision: snapshot.revision, ...pending }),
      });
      const currentPending = readPendingPatch();
      const remaining = Object.fromEntries(Object.entries(currentPending).filter(([field, value]) => (
        !hasOwn(pending, field) || JSON.stringify(value) !== JSON.stringify(pending[field])
      )));
      clearPendingPatch();
      status = Object.keys(remaining).length ? "pending" : "synced";
      applyServerSnapshot(response);
      if (Object.keys(remaining).length) {
        applyPatch(remaining);
        persistPendingPatch(remaining);
      }
      return getSnapshot();
    } catch (error) {
      if (Number(error?.status) === 409 && conflictRetry) {
        await refresh();
        applyPatch(pending);
        persistPendingPatch(pending);
        return replayPendingPatch({ conflictRetry: false });
      }
      persistPendingPatch(pending);
      handleRequestFailure(error);
      return getSnapshot();
    }
  }

  async function hydrate() {
    if (loadPromise) return loadPromise;
    status = "loading";
    notify();
    loadPromise = (async () => {
      try {
        await refresh();
        try {
          await importLegacyIfNeeded();
        } catch (error) {
          // 401 means the session is gone. 403 on import-local after a successful
          // GET is a narrower allowlist miss and must not wipe the snapshot that
          // refresh() just applied.
          if (Number(error?.status) === 401) clearActiveScope();
        }
        const pending = readPendingPatch();
        if (Object.keys(pending).length) {
          applyPatch(pending);
          persistPendingPatch(pending);
          await replayPendingPatch();
        }
        return getSnapshot();
      } catch {
        return getSnapshot();
      } finally {
        loadPromise = null;
      }
    })();
    return loadPromise;
  }

  function setPreferences(patch) {
    const normalized = normalizePatch(patch);
    if (!Object.keys(normalized).length) return Promise.resolve(getSnapshot());
    applyPatch(normalized);
    persistPendingPatch({ ...readPendingPatch(), ...normalized });
    status = "pending";
    notify();
    if (!snapshot.scopeKey) return Promise.resolve(getSnapshot());
    if (!savePromise) {
      savePromise = replayPendingPatch().finally(() => {
        savePromise = null;
        if (hasPendingPatch() && snapshot.scopeKey && status !== "offline" && status !== "unauthorized") retryPending();
      });
    }
    return savePromise;
  }

  async function importPreferences(value = {}) {
    const patch = normalizePatch(value);
    if (!Object.keys(patch).length) return 0;
    if (!snapshot.scopeKey) await refresh();
    applyPatch(patch);
    persistPendingPatch({ ...readPendingPatch(), ...patch });
    const before = snapshot.revision;
    await replayPendingPatch();
    if (hasPendingPatch() || snapshot.revision === before) throw new Error("Account preferences are waiting to sync");
    return Object.keys(patch).length;
  }

  function subscribe(subscriber) {
    if (typeof subscriber !== "function") return () => {};
    subscribers.add(subscriber);
    return () => subscribers.delete(subscriber);
  }

  function retryPending() {
    if (!snapshot.scopeKey || !hasPendingPatch() || savePromise) return savePromise || Promise.resolve(getSnapshot());
    savePromise = replayPendingPatch().finally(() => { savePromise = null; });
    return savePromise;
  }

  eventTarget?.addEventListener?.("online", retryPending);
  eventTarget?.addEventListener?.("pageshow", retryPending);

  return {
    clearActiveScope,
    getModelVisibility: () => getSnapshot().modelVisibility,
    getPreferredModel: () => snapshot.preferredModel,
    getProfile: () => getSnapshot().profile,
    getSetupVersion: () => snapshot.setupVersion,
    getSnapshot,
    getStatus,
    hasPendingPatch,
    hydrate,
    importPreferences,
    normalizeModelVisibility,
    normalizeProfile: normalizeAccountProfile,
    refresh,
    retryPending,
    setModelVisibility: (value) => setPreferences({ modelVisibility: value }),
    setPreferredModel: (value) => setPreferences({ preferredModel: value }),
    setProfile: (value) => setPreferences({ profile: value }),
    setSetupVersion: (value) => setPreferences({ setupVersion: value }),
    setPreferences,
    subscribe,
  };
}
