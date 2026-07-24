import { $ } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { settingsItemByKey, settingsItems, settingsSections } from "./settings-data.mjs";

const mobileSettingsSectionSpecs = Object.freeze([
  {
    key: "personal-interface",
    labelKey: "settings.mobile.section.personalInterface",
    items: ["profile", "appearance", "notifications", "archive"],
  },
  {
    key: "ai-capabilities",
    labelKey: "settings.mobile.section.aiCapabilities",
    items: ["models", "providers", "skills", "memory", "im-gateway", "shared-api"],
  },
  {
    key: "network",
    labelKey: "settings.category.network",
    items: ["network-search", "remote-access"],
  },
  {
    key: "system-security",
    labelKey: "settings.mobile.section.systemSecurity",
    items: ["servers-system", "terminals", "storage", "runtime", "usage", "about"],
  },
]);

// Layout/visibility helpers for the settings modal shell: inline-style
// snapshot/restore used while promoting the modal into the app shell grid,
// mobile viewport detection, and the mobile index/detail view switch.
export function createSettingsShellHelpers({
  state,
  isMobileAppViewport,
  selectSettingsPanel,
  renderSettingsNav,
  renderMobileSettingsIndex,
  syncSettingsCloseControl,
  saveCurrentChatDraft,
  hideSlashCommandPalette,
  closeMobileSidebar,
  applyPrimaryWorkbench,
}) {
  // The docking session belongs to this module: it is the only thing that
  // moves the settings modal into the app shell, and the only thing that can
  // put the DOM back the way it was.
  let settingsShellSession = null;
  function captureInlineProperties(element, properties) {
    if (!element) return null;
    return {
      element,
      properties: Object.fromEntries(properties.map((property) => [property, {
        value: element.style.getPropertyValue(property),
        priority: element.style.getPropertyPriority(property),
      }])),
    };
  }

  function restoreInlineProperties(snapshot) {
    if (!snapshot?.element) return;
    Object.entries(snapshot.properties).forEach(([property, entry]) => {
      if (entry.value) snapshot.element.style.setProperty(property, entry.value, entry.priority);
      else snapshot.element.style.removeProperty(property);
    });
  }

  function setSettingsShellNodeHidden(element, hidden) {
    if (!element) return null;
    const snapshot = {
      element,
      display: element.style.getPropertyValue("display"),
      displayPriority: element.style.getPropertyPriority("display"),
      ariaHidden: element.getAttribute("aria-hidden"),
    };
    if (hidden) {
      element.style.setProperty("display", "none", "important");
      element.setAttribute("aria-hidden", "true");
    }
    return snapshot;
  }

  function restoreSettingsShellNode(snapshot) {
    if (!snapshot?.element) return;
    if (snapshot.display) snapshot.element.style.setProperty("display", snapshot.display, snapshot.displayPriority);
    else snapshot.element.style.removeProperty("display");
    if (snapshot.ariaHidden == null) snapshot.element.removeAttribute("aria-hidden");
    else snapshot.element.setAttribute("aria-hidden", snapshot.ariaHidden);
  }

  function isMobileSettingsViewport() {
    return isMobileAppViewport();
  }

  function settingsModalOpen() {
    const modal = $("settingsModal");
    return Boolean(modal && !modal.classList.contains("hidden"));
  }

  function resolvedMobileSettingsSections() {
    const itemByKey = new Map(settingsItems.map((item) => [item.key, item]));
    const canonicalItems = [];
    const seen = new Set();
    [...settingsSections.flatMap((section) => section.items || []), ...settingsItems].forEach((item) => {
      if (!item?.key || seen.has(item.key)) return;
      seen.add(item.key);
      canonicalItems.push(itemByKey.get(item.key) || item);
    });
    const assigned = new Set(mobileSettingsSectionSpecs.flatMap((section) => section.items));
    const sections = mobileSettingsSectionSpecs.map((section) => ({
      ...section,
      label: t(section.labelKey),
      items: section.items.map((key) => itemByKey.get(key)).filter(Boolean),
    }));
    const unassigned = canonicalItems.filter((item) => !assigned.has(item.key));
    if (unassigned.length) sections[sections.length - 1].items.push(...unassigned);
    return sections.filter((section) => section.items.length);
  }

  function applyMobileSettingsViewClasses() {
    const modal = $("settingsModal");
    if (!modal) return;
    const mobile = state.settingsMobileViewport && isMobileSettingsViewport();
    const index = mobile && state.mobileSettingsView === "index";
    modal.classList.toggle("mobile-settings-index", index);
    modal.classList.toggle("mobile-settings-detail", mobile && !index);
    if (mobile) modal.dataset.mobileSettingsView = index ? "index" : "detail";
    else delete modal.dataset.mobileSettingsView;
    syncSettingsCloseControl();
  }

  function syncSettingsViewportState() {
    const mobile = isMobileSettingsViewport();
    const wasMobile = state.settingsMobileViewport;
    const wasIndex = state.mobileSettingsView === "index";
    state.settingsMobileViewport = mobile;
    if ($("settingsModalTitle")) $("settingsModalTitle").textContent = mobile && state.mobileSettingsView === "detail"
      ? (settingsItemByKey(state.activeSettingsPanel)?.label || t("settings.dialogTitle"))
      : (mobile ? t("settings.mobile.indexTitle") : t("settings.dialogTitle"));
    if (!settingsModalOpen()) {
      state.mobileSettingsView = "detail";
      applyMobileSettingsViewClasses();
      return;
    }
    if (mobile) {
      if (state.settingsShellOpen) exitSettingsShell();
      applyMobileSettingsViewClasses();
      if (state.mobileSettingsView === "index") renderMobileSettingsIndex();
      else renderSettingsNav(state.activeSettingsPanel || "providers");
      return;
    }
    state.mobileSettingsView = "detail";
    applyMobileSettingsViewClasses();
    enterSettingsShell();
    if (wasMobile && wasIndex) selectSettingsPanel(state.activeSettingsPanel || "providers");
    else renderSettingsNav(state.activeSettingsPanel || "providers");
  }

  function layoutSettingsShell() {
    if (!state.settingsShellOpen || !settingsShellSession) return;
    const { appShell, modal, card } = settingsShellSession;
    const desktop = globalThis.matchMedia?.("(min-width: 768px)")?.matches !== false;
    if (desktop) {
      const railWidth = globalThis.matchMedia?.("(min-width: 1280px)")?.matches ? "76px" : "68px";
      appShell.style.setProperty("grid-template-columns", `${railWidth} var(--session-sidebar-width, 296px) minmax(0, 1fr)`);
      modal.style.setProperty("grid-column", "2 / -1");
      modal.style.setProperty("grid-row", "1");
      card.style.setProperty("grid-template-columns", "var(--session-sidebar-width, 296px) minmax(0, 1fr)");
      card.style.setProperty("grid-template-rows", "minmax(0, 1fr)");
    } else {
      appShell.style.removeProperty("grid-template-columns");
      modal.style.setProperty("grid-column", "1 / -1");
      modal.style.setProperty("grid-row", "2");
      card.style.setProperty("grid-template-columns", "minmax(0, 1fr)");
      card.style.setProperty("grid-template-rows", "minmax(220px, 40vh) minmax(0, 1fr)");
    }
  }

  function enterSettingsShell() {
    if (state.settingsShellOpen) {
      layoutSettingsShell();
      return;
    }
    const appShell = $("appShell");
    const modal = $("settingsModal");
    const card = modal?.querySelector(".settings-dialog-shell");
    if (!appShell || !modal || !card) return;

    saveCurrentChatDraft();
    hideSlashCommandPalette();
    closeMobileSidebar();
    const originalParent = modal.parentNode;
    const originalNextSibling = modal.nextSibling;
    const hiddenNodes = [
      "sessionSidebar",
      "sidebarResizeHandle",
      "overviewDashboard",
      "conversationPanel",
      "workbenchPanel",
      "schedulePanel",
      "terminalPanel",
      "conversationDetailsPanel",
      "backgroundTaskTray",
      "expandTerminalBtn",
      "utilityPanelResizeHandle",
    ].map((id) => setSettingsShellNodeHidden($(id), true)).filter(Boolean);
    const appShellStyle = captureInlineProperties(appShell, ["grid-template-columns"]);
    const modalStyle = captureInlineProperties(modal, [
      "position", "inset", "width", "height", "min-width", "min-height", "display", "grid-column", "grid-row",
      "align-items", "justify-content", "overflow", "padding", "background", "backdrop-filter", "z-index",
    ]);
    const cardStyle = captureInlineProperties(card, [
      "width", "height", "max-width", "max-height", "display", "grid-template-columns", "grid-template-rows",
      "overflow", "border", "border-radius", "box-shadow",
    ]);
    settingsShellSession = {
      appShell,
      modal,
      card,
      originalParent,
      originalNextSibling,
      originalRole: modal.getAttribute("role"),
      originalAriaModal: modal.getAttribute("aria-modal"),
      hiddenNodes,
      appShellStyle,
      modalStyle,
      cardStyle,
    };
    state.settingsShellOpen = true;
    appShell.appendChild(modal);
    modal.setAttribute("role", "region");
    modal.removeAttribute("aria-modal");
    modal.style.setProperty("position", "relative");
    modal.style.setProperty("inset", "auto");
    modal.style.setProperty("width", "auto");
    modal.style.setProperty("height", "100%");
    modal.style.setProperty("min-width", "0");
    modal.style.setProperty("min-height", "0");
    modal.style.setProperty("display", "flex");
    modal.style.setProperty("align-items", "stretch");
    modal.style.setProperty("justify-content", "stretch");
    modal.style.setProperty("overflow", "hidden");
    modal.style.setProperty("padding", "0");
    modal.style.setProperty("background", "transparent");
    modal.style.setProperty("backdrop-filter", "none");
    modal.style.setProperty("z-index", "10");
    card.style.setProperty("width", "100%");
    card.style.setProperty("height", "100%");
    card.style.setProperty("max-width", "none");
    card.style.setProperty("max-height", "none");
    card.style.setProperty("display", "grid");
    card.style.setProperty("overflow", "hidden");
    card.style.setProperty("border", "0");
    card.style.setProperty("border-radius", "0");
    card.style.setProperty("box-shadow", "none");
    layoutSettingsShell();
  }

  function exitSettingsShell() {
    const session = settingsShellSession;
    state.settingsShellOpen = false;
    settingsShellSession = null;
    if (!session) return;
    const { modal, originalParent, originalNextSibling } = session;
    restoreInlineProperties(session.appShellStyle);
    restoreInlineProperties(session.modalStyle);
    restoreInlineProperties(session.cardStyle);
    session.hiddenNodes.forEach(restoreSettingsShellNode);
    if (session.originalRole == null) modal.removeAttribute("role");
    else modal.setAttribute("role", session.originalRole);
    if (session.originalAriaModal == null) modal.removeAttribute("aria-modal");
    else modal.setAttribute("aria-modal", session.originalAriaModal);
    if (originalParent) {
      if (originalNextSibling?.parentNode === originalParent) originalParent.insertBefore(modal, originalNextSibling);
      else originalParent.appendChild(modal);
    }
    applyPrimaryWorkbench(state.activeWorkbench);
  }

  return {
    captureInlineProperties,
    restoreInlineProperties,
    setSettingsShellNodeHidden,
    restoreSettingsShellNode,
    isMobileSettingsViewport,
    settingsModalOpen,
    resolvedMobileSettingsSections,
    applyMobileSettingsViewClasses,
    syncSettingsViewportState,
    layoutSettingsShell,
    enterSettingsShell,
    exitSettingsShell,
  };
}
