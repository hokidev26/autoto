import { withErrorBoundary } from "./error-boundary.mjs?v=error-boundary-1";

export class SettingsPanelRegistry {
  constructor() {
    this.panels = new Map();
  }

  register(key, definition = {}) {
    const normalizedKey = String(key ?? "").trim();
    if (!normalizedKey) throw new TypeError("Settings panel key must not be empty");
    if (this.panels.has(normalizedKey)) throw new Error(`Settings panel already registered: ${normalizedKey}`);
    if (typeof definition?.render !== "function") throw new TypeError(`Settings panel render must be a function: ${normalizedKey}`);
    if (definition.bind != null && typeof definition.bind !== "function") {
      throw new TypeError(`Settings panel bind must be a function: ${normalizedKey}`);
    }
    if (definition.layout != null && (!String(definition.layout).trim() || typeof definition.layout !== "string")) {
      throw new TypeError(`Settings panel layout must be a non-empty string: ${normalizedKey}`);
    }

    // Boundaries are applied here rather than at each registration site so a
    // panel added later cannot forget one: a throw in any panel shows a card in
    // that panel's place instead of taking the surrounding shell down with it.
    const panel = Object.freeze(withErrorBoundary({
      render: definition.render,
      ...(definition.bind ? { bind: definition.bind } : {}),
      ...(definition.layout ? { layout: definition.layout.trim() } : {}),
    }, `settings.${normalizedKey}`));
    this.panels.set(normalizedKey, panel);
    return this;
  }

  resolve(key) {
    return this.panels.get(String(key ?? "").trim());
  }
}

export function createSettingsPanelRegistry() {
  return new SettingsPanelRegistry();
}
