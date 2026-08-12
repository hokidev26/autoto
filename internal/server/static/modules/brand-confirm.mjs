import { $ } from "./dom.mjs";

// In-app confirmation dialog with the Autoto mark in its top-left corner.
// The native desktop dialog cannot show a custom icon, so flows that want the
// brand visible confirm through this instead. API mirrors platform.confirm:
// confirm(message) resolves true/false.
export function createBrandConfirm({ fallback = null } = {}) {
  let activeResolve = null;
  let restoreFocusTo = null;
  let bound = false;

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
    // Clicking the dimmed area outside the card cancels, like pressing Escape.
    found.backdrop.addEventListener("click", (event) => {
      if (event.target === found.backdrop) settle(false);
    });
    globalThis.document?.addEventListener?.("keydown", (event) => {
      if (event.key !== "Escape" || !activeResolve) return;
      event.preventDefault();
      event.stopPropagation();
      settle(false);
    }, { capture: true });
  }

  async function confirm(message) {
    const found = elements();
    if (!found) {
      // Markup missing (embedded page, test harness): degrade to the plain
      // platform dialog rather than silently refusing every action.
      if (typeof fallback === "function") return fallback(message);
      return false;
    }
    bindOnce(found);
    if (activeResolve) settle(false);
    found.message.textContent = String(message ?? "");
    found.backdrop.classList.remove("hidden");
    found.backdrop.setAttribute("aria-hidden", "false");
    restoreFocusTo = globalThis.document?.activeElement || null;
    found.cancel.focus?.();
    return new Promise((resolve) => {
      activeResolve = resolve;
    });
  }

  return { confirm };
}
