const runtimeWindow = globalThis.window || {};
const localAPIToken = String(runtimeWindow.AUTOTO_LOCAL_TOKEN || "").trim();
const apiAuthorizationFailureListeners = new Set();

export function onAPIAuthorizationFailure(listener) {
  if (typeof listener !== "function") return () => {};
  apiAuthorizationFailureListeners.add(listener);
  return () => apiAuthorizationFailureListeners.delete(listener);
}

function notifyAPIAuthorizationFailure(detail) {
  for (const listener of apiAuthorizationFailureListeners) {
    try {
      listener(detail);
    } catch {}
  }
}

export function withLocalToken(path) {
  // The server no longer accepts query tokens. api() already sends
  // X-Autoto-Token; this helper must not put the secret in the URL.
  return path;
}

export function webSocketURL(path) {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  return `${proto}://${location.host}${path}`;
}

async function throwAPIResponseError(res, fallback, path, authorizationError) {
  let text;
  try {
    text = await res.text();
  } catch (cause) {
    if (authorizationError) {
      authorizationError.cause = cause;
      throw authorizationError;
    }
    throw cause;
  }
  let body = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = text;
    }
  }
  const message = typeof body === "string" ? body : (body?.error || body?.message || fallback);
  const error = authorizationError || new Error(message);
  error.message = message;
  error.status = res.status;
  error.body = body;
  error.path = path;
  throw error;
}

export async function api(path, options = {}) {
  const baseHeaders = options.body instanceof FormData
    ? { ...(options.headers || {}) }
    : { "Content-Type": "application/json", ...(options.headers || {}) };
  const headers = localAPIToken ? { ...baseHeaders, "X-Autoto-Token": localAPIToken } : baseHeaders;
  const res = await fetch(path, {
    ...options,
    headers,
  });
  const fallback = `${res.status} ${res.statusText}`;
  let authorizationError = null;
  if (!res.ok && (res.status === 401 || res.status === 403)) {
    authorizationError = new Error(fallback);
    authorizationError.status = res.status;
    authorizationError.body = null;
    authorizationError.path = path;
    // Authorization changes are transport-level facts. Notify listeners as soon
    // as response headers arrive, before a slow or broken response body can keep
    // privileged WebSockets alive.
    notifyAPIAuthorizationFailure({ status: res.status, path, error: authorizationError });
  }

  let text;
  try {
    text = await res.text();
  } catch (cause) {
    if (authorizationError) {
      authorizationError.cause = cause;
      throw authorizationError;
    }
    throw cause;
  }
  let body = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = text;
    }
  }
  if (!res.ok) {
    const message = typeof body === "string" ? body : (body?.error || body?.message || fallback);
    const error = authorizationError || new Error(message);
    error.message = message;
    error.status = res.status;
    error.body = body;
    error.path = path;
    throw error;
  }
  return body ?? {};
}

// Returns the untouched successful response for downloads. Callers should
// consume it as a Blob or ArrayBuffer rather than placing sensitive content in
// application state.
export async function apiDownload(path, options = {}) {
  const baseHeaders = options.body instanceof FormData
    ? { ...(options.headers || {}) }
    : { "Content-Type": "application/json", ...(options.headers || {}) };
  const headers = localAPIToken ? { ...baseHeaders, "X-Autoto-Token": localAPIToken } : baseHeaders;
  const res = await fetch(path, {
    ...options,
    headers,
  });
  const fallback = `${res.status} ${res.statusText}`;
  let authorizationError = null;
  if (!res.ok && (res.status === 401 || res.status === 403)) {
    authorizationError = new Error(fallback);
    authorizationError.status = res.status;
    authorizationError.body = null;
    authorizationError.path = path;
    notifyAPIAuthorizationFailure({ status: res.status, path, error: authorizationError });
  }
  if (!res.ok) await throwAPIResponseError(res, fallback, path, authorizationError);
  return res;
}
