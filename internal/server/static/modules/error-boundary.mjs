// Error boundaries for leaf UI.
//
// The app loads well over a hundred modules on demand. Without boundaries a
// throw anywhere in a panel's render escapes into whatever navigation call
// triggered it, and the usual result is a blank shell: the user loses the whole
// application because one screen has a bug.
//
// These wrappers keep a failure the size of the thing that failed. They are
// deliberately only applied at leaf boundaries -- a panel, a lazily loaded
// surface -- and never around core state transitions, where swallowing an error
// would turn a loud bug into a silent one.

import { escapeAttr, escapeHtml } from "./dom.mjs";

const RELOAD_ATTRIBUTE = "data-error-boundary-reload";

function errorMessage(error) {
  if (error instanceof Error && error.message) return error.message;
  const text = String(error ?? "").trim();
  return text || "unknown error";
}

function report(name, error) {
  // Kept as console.error rather than a toast: this is diagnostic detail for
  // whoever is debugging, and the visible card already tells the user.
  console.error(`[error-boundary] ${name} failed`, error);
}

// Replaces the content that could not be rendered. Reload is offered instead of
// a targeted retry because a panel that threw may have left partial state
// behind, and a reload is the one recovery that is always sound.
export function renderBoundaryErrorHTML(name, error) {
  return `
    <div class="error-boundary-card" role="alert" data-error-boundary="${escapeAttr(name)}">
      <div class="error-boundary-title">这个面板加载失败了</div>
      <p class="error-boundary-text">其他功能仍可正常使用。重新加载页面通常就能恢复。</p>
      <pre class="error-boundary-detail">${escapeHtml(`${name}: ${errorMessage(error)}`)}</pre>
      <button class="ghost-btn mini" type="button" ${RELOAD_ATTRIBUTE}>重新加载</button>
    </div>
  `;
}

// For functions that build markup. Returns the fallback card instead of letting
// the throw escape, so the caller can always assign the result.
export function guardRender(name, fn, ...args) {
  try {
    return fn(...args);
  } catch (error) {
    report(name, error);
    return renderBoundaryErrorHTML(name, error);
  }
}

// For functions whose value is their side effects. A failure here means some
// controls on an otherwise rendered panel are inert, which is worth logging but
// never worth discarding the panel over.
export function guardCall(name, fn, ...args) {
  if (typeof fn !== "function") return true;
  try {
    fn(...args);
    return true;
  } catch (error) {
    report(name, error);
    return false;
  }
}

// For dynamic imports and other async setup. Resolves to `fallback` rather than
// rejecting, so one unavailable module cannot reject the boot chain.
export async function guardAsync(name, fn, { fallback = null } = {}) {
  try {
    return await fn();
  } catch (error) {
    report(name, error);
    return fallback;
  }
}

// Wraps a settings panel definition. Applied centrally by the registry so every
// panel -- including ones added later -- gets a boundary without each call site
// remembering to ask for one.
export function withErrorBoundary(panel, name) {
  const guarded = {
    render: (...args) => guardRender(name, panel.render, ...args),
  };
  if (panel.bind) guarded.bind = (...args) => guardCall(`${name}.bind`, panel.bind, ...args);
  if (panel.layout) guarded.layout = panel.layout;
  return guarded;
}

// Global net for what the wrappers cannot see: listener callbacks, timers, and
// promises nothing awaited. This only records and surfaces; it deliberately does
// not clear or replace any DOM, because the shell is usually still usable.
export function installGlobalErrorReporting({ target = globalThis, onReport = null } = {}) {
  if (typeof target?.addEventListener !== "function") return () => {};
  if (target.__autotoErrorReportingInstalled) return () => {};
  target.__autotoErrorReportingInstalled = true;

  const handleError = (event) => {
    report("window.error", event?.error || event?.message || event);
    onReport?.(event?.error || event?.message || event);
  };
  const handleRejection = (event) => {
    report("unhandledrejection", event?.reason ?? event);
    onReport?.(event?.reason ?? event);
  };
  // Delegated so every boundary card's reload button works without each one
  // binding its own listener at render time.
  const handleClick = (event) => {
    const button = event?.target?.closest?.(`[${RELOAD_ATTRIBUTE}]`);
    if (button) target.location?.reload?.();
  };

  target.addEventListener("error", handleError);
  target.addEventListener("unhandledrejection", handleRejection);
  target.document?.addEventListener?.("click", handleClick);

  return () => {
    target.removeEventListener?.("error", handleError);
    target.removeEventListener?.("unhandledrejection", handleRejection);
    target.document?.removeEventListener?.("click", handleClick);
    target.__autotoErrorReportingInstalled = false;
  };
}
