// OS-level notifications for runs that finish while Autoto is not on screen.
//
// The in-page toast only helps when the page is visible. When the tab is in the
// background or the window is behind an editor, a finished run produced no
// signal at all. The Notification API is the only way to reach the user there.
//
// Permission is requested from a user gesture, never on load: an unprompted
// permission dialog is hostile, and Chrome ignores requests made without a
// gesture anyway. Until permission is granted this module is inert.

export const systemNotificationDefaults = Object.freeze({
  tag: "autoto-run",
  // Renotify on the same tag would stack duplicates for one run; a stable tag
  // per agent collapses repeats instead.
  requireInteraction: false,
});

function api(scope) {
  return scope?.Notification || null;
}

export function systemNotificationPermission(scope = globalThis) {
  return String(api(scope)?.permission || "unsupported");
}

export function createSystemNotifications({
  scope = globalThis,
  isEnabled = () => true,
  onError,
  onActivate,
} = {}) {
  let denied = false;

  function supported() {
    return Boolean(api(scope));
  }

  function permission() {
    return systemNotificationPermission(scope);
  }

  // Must be called from a gesture handler. Returns the resulting permission so
  // the settings UI can reflect a denial without polling.
  async function request() {
    const Ctor = api(scope);
    if (!Ctor) return "unsupported";
    if (Ctor.permission === "granted" || Ctor.permission === "denied") return Ctor.permission;
    try {
      const result = await Ctor.requestPermission();
      return String(result || Ctor.permission || "default");
    } catch (error) {
      onError?.(error);
      return "default";
    }
  }

  // The page being visible is the signal that a toast is enough. Only reach for
  // an OS notification when the user cannot see the page.
  function pageHidden() {
    const doc = scope?.document;
    if (!doc) return true;
    if (typeof doc.visibilityState === "string") return doc.visibilityState !== "visible";
    return Boolean(doc.hidden);
  }

  function show(title, { body = "", tag = systemNotificationDefaults.tag, force = false, data } = {}) {
    const Ctor = api(scope);
    if (!Ctor || denied) return null;
    if (Ctor.permission !== "granted") return null;
    if (!isEnabled()) return null;
    if (!force && !pageHidden()) return null;
    const text = String(title || "").trim();
    if (!text) return null;
    try {
      const notification = new Ctor(text, {
        body: String(body || "").slice(0, 300),
        tag: String(tag || systemNotificationDefaults.tag),
        requireInteraction: systemNotificationDefaults.requireInteraction,
        silent: true,
      });
      // Clicking an OS notification should bring the user back to the run it is
      // about, which is the only reason to click it.
      notification.onclick = () => {
        try {
          scope?.focus?.();
          notification.close?.();
        } catch (error) {
          onError?.(error);
        }
        onActivate?.(data);
      };
      return notification;
    } catch (error) {
      // Some platforms throw on construction rather than reporting a denial.
      denied = true;
      onError?.(error);
      return null;
    }
  }

  return { supported, permission, request, show, pageHidden };
}
