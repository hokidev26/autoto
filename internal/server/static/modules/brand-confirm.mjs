import { $ } from "./dom.mjs";

function defaultFallbackConfirm(message) {
  if (typeof globalThis.window?.confirm === "function") {
    return Promise.resolve(Boolean(globalThis.window.confirm(String(message ?? ""))));
  }
  return Promise.resolve(false);
}

function defaultFallbackAlert(message) {
  if (typeof globalThis.window?.alert === "function") {
    globalThis.window.alert(String(message ?? ""));
  }
  return Promise.resolve();
}

// In-app Autoto dialog. Confirm/alert stay inside the app chrome so the desktop
// shell does not fall back to the host OS MessageBox. API mirrors platform:
// confirm(message) resolves true/false; alert(message) resolves when dismissed.
export function createBrandConfirm({ fallback = null, alertFallback = null } = {}) {
  let activeResolve = null;
  let restoreFocusTo = null;
  let bound = false;
  let mode = "confirm";

  function elements() {
    const backdrop = $("brandConfirmBackdrop");
    const message = $("brandConfirmMessage");
    const ok = $("brandConfirmOk");
    const cancel = $("brandConfirmCancel");
    return backdrop && message && ok && cancel ? { backdrop, message, ok, cancel } : null;
  }

  function settle(accepted) {
    const resolve = activeResolve;
    activeResolve = null;
    const backdrop = $("brandConfirmBackdrop");
    backdrop?.classList.add("hidden");
    backdrop?.classList.remove("brand-confirm-alert");
    backdrop?.setAttribute("aria-hidden", "true");
    restoreFocusTo?.focus?.();
    restoreFocusTo = null;
    resolve?.(accepted);
  }

  function bindOnce(found) {
    if (bound) return;
    bound = true;
    found.ok.addEventListener("click", () => settle(true));
    found.cancel.addEventListener("click", () => settle(false));
    found.backdrop.addEventListener("click", (event) => {
      if (event.target !== found.backdrop) return;
      settle(mode === "alert");
    });
    globalThis.document?.addEventListener?.("keydown", (event) => {
      if (event.key !== "Escape" || !activeResolve) return;
      event.preventDefault();
      event.stopPropagation();
      settle(mode === "alert");
    }, { capture: true });
  }

  function applyMode(found, nextMode) {
    mode = nextMode;
    const isAlert = nextMode === "alert";
    found.backdrop.classList.toggle("brand-confirm-alert", isAlert);
    found.cancel.hidden = isAlert;
    found.cancel.setAttribute("aria-hidden", isAlert ? "true" : "false");
  }

  async function open(message, nextMode) {
    const found = elements();
    if (!found) {
      if (nextMode === "alert") {
        if (typeof alertFallback === "function") return alertFallback(message);
        return defaultFallbackAlert(message);
      }
      if (typeof fallback === "function") return fallback(message);
      return defaultFallbackConfirm(message);
    }
    bindOnce(found);
    if (activeResolve) settle(false);
    applyMode(found, nextMode);
    found.message.textContent = String(message ?? "");
    found.backdrop.classList.remove("hidden");
    found.backdrop.setAttribute("aria-hidden", "false");
    restoreFocusTo = globalThis.document?.activeElement || null;
    if (nextMode === "alert") found.ok.focus?.();
    else found.cancel.focus?.();
    return new Promise((resolve) => {
      activeResolve = resolve;
    });
  }

  async function confirm(message) {
    return open(message, "confirm");
  }

  async function alert(message) {
    await open(message, "alert");
  }

  return { confirm, alert };
}
