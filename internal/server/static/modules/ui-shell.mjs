import { $ } from "./dom.mjs";
import { api } from "./runtime.mjs";
import { createComposerSelectMenus } from "./composer-select-menus.mjs";

export const sidebarWidthPreferenceKey = "autoto.ui.sessionSidebarWidth";
export const globalRailCollapsedPreferenceKey = "autoto.ui.globalRailCollapsed";
export const sessionSidebarCollapsedPreferenceKey = "autoto.ui.sessionSidebarCollapsed";
export const legacySidebarCollapsedPreferenceKey = "autoto.ui.sidebarCollapsed";
export const navigationLayoutModePreferenceKey = "autoto.ui.navigationLayoutMode";
// Three navigation layouts, collapsing left to right:
//   columns - the rail plus a separate conversation column (the long-standing layout)
//   docked  - one wide rail; the conversation list sits under the "conversation" entry
//   icons   - a 48px icon-only rail; the conversation list is hidden
export const navigationLayoutModes = Object.freeze(["columns", "docked", "icons"]);

export function normalizeNavigationLayoutMode(value, fallback = "columns") {
  const mode = String(value ?? "").trim().toLowerCase();
  return navigationLayoutModes.includes(mode) ? mode : fallback;
}

// Collapsing runs columns -> docked -> icons, then wraps back to columns. The
// wrap is what keeps a single control able to reach every layout.
export function nextNavigationLayoutMode(current) {
  const index = navigationLayoutModes.indexOf(normalizeNavigationLayoutMode(current));
  return navigationLayoutModes[(index + 1) % navigationLayoutModes.length];
}

// Dragging the divider reaches all three layouts too, so the collapse button is a
// shortcut rather than the only route. The thresholds below are measured on the
// divider itself -- the total width of the navigation area, which is what the user
// is physically holding -- not on the stored sidebar width.
//
// Enter and exit differ on purpose. A single boundary makes the layout flip back
// and forth while the pointer sits on it, and each flip reflows the whole rail.
// The gap between the pair is the distance you must travel before the layout
// changes its mind, which is what makes the drag feel settled.
export const navigationDragIconsEnterWidth = 150;
export const navigationDragIconsExitWidth = 196;
export const navigationDragColumnsEnterWidth = 436;
// The columns exit must sit below the tightest divider position the columns
// layout can rest at: the compact column parks the divider at rail (68) +
// compact width (184) = 252, and the narrow band a little above that. The old
// 300 sat above both, so grabbing a compact or narrow column flipped the whole
// layout to docked on the first pixel of travel. 228 leaves ~24px of
// deliberate travel from the compact resting point before the layout docks,
// and an expanded column dragged left now passes through its narrow and
// compact stages before leaving the layout instead of jumping out at 300.
export const navigationDragColumnsExitWidth = 228;

// Resolves the layout a drag should land in. `total` is the divider position;
// `current` is the layout being dragged, so the thresholds can be applied in the
// direction the user is actually moving.
export function navigationLayoutModeFromDragWidth(total, current = "columns") {
  const width = Number(total);
  const mode = normalizeNavigationLayoutMode(current);
  if (!Number.isFinite(width)) return mode;
  if (mode === "icons") {
    // Still inside the icon rail's band: stay put until the pointer clears it.
    if (width <= navigationDragIconsExitWidth) return "icons";
    return width >= navigationDragColumnsEnterWidth ? "columns" : "docked";
  }
  if (width <= navigationDragIconsEnterWidth) return "icons";
  if (mode === "columns") return width < navigationDragColumnsExitWidth ? "docked" : "columns";
  return width >= navigationDragColumnsEnterWidth ? "columns" : "docked";
}
export const defaultSidebarWidth = 296;
export const minSidebarWidth = 184;
// The column's own compact stage is retired -- the rail's three-way cycle is
// the collapse control now -- but stored widths from that era sit in this band
// and are migrated back to the default width on load.
export const compactSidebarEnterWidth = 196;
export const narrowSidebarMinWidth = 197;
export const narrowSidebarMaxWidth = 219;
export const maxSidebarWidth = 420;
export const globalRailExpandedWidth = 68;
export const globalRailCollapsedWidth = 48;
// readOnly joined the primary group (least- to most-permissive order); the
// redundant dontAsk mode is gone, so the secondary section renders nothing.
export const permissionMenuPrimaryValues = Object.freeze(["readOnly", "default", "acceptEdits", "bypassPermissions"]);
export const permissionMenuSecondaryValues = Object.freeze([]);

export function orderPermissionMenuOptions(options = []) {
  const remaining = new Map([...options].map((option) => [option.value, option]));
  const ordered = [];
  [...permissionMenuPrimaryValues, ...permissionMenuSecondaryValues].forEach((value) => {
    const option = remaining.get(value);
    if (!option) return;
    ordered.push(option);
    remaining.delete(value);
  });
  remaining.forEach((option) => ordered.push(option));
  return ordered;
}

export function compactComposerModelLabel(value, fallback = "模型") {
  const raw = String(value || "").trim();
  if (!raw) return fallback;
  let model = raw.includes(":") ? raw.slice(raw.lastIndexOf(":") + 1) : raw;
  model = model
    .replace(/^claude-/i, "")
    .replace(/-(latest|\d{8})$/i, "")
    .replace(/-(\d+)-(\d+)(?=$|-)/, "-$1.$2")
    .trim();
  if (!model) return fallback;
  const anthropicFamily = model.match(/^(sonnet|opus|haiku)/i)?.[1];
  if (anthropicFamily) return anthropicFamily.toLowerCase();
  const gptFamily = model.match(/^(gpt[-_.]?\d+(?:[-_.]\d+)?)/i)?.[1];
  if (gptFamily) return gptFamily.replace(/_/g, "-");
  if (model.length <= 9) return model;
  const family = model.split("-")[0];
  if (family.length >= 4 && family.length <= 9) return family;
  return `${model.slice(0, 8)}…`;
}

export function modelOptionPresentation(value, label) {
  const rawValue = String(value || "").trim();
  const rawLabel = String(label || "").trim();
  const separator = rawValue.indexOf(":");
  const provider = separator > 0 ? rawValue.slice(0, separator).trim() : "";
  const model = separator >= 0 ? rawValue.slice(separator + 1).trim() : rawValue;
  return {
    name: rawLabel || model || rawValue || "模型",
    provider,
  };
}

export function groupModelSelectOptions(options = []) {
  const groups = [];
  const byProvider = new Map();
  for (const option of options) {
    const presentation = modelOptionPresentation(option?.value, option?.textContent);
    const provider = String(option?.dataset?.provider || option?.parentElement?.label || presentation.provider || "").trim();
    if (!byProvider.has(provider)) {
      const group = { provider, options: [] };
      byProvider.set(provider, group);
      groups.push(group);
    }
    byProvider.get(provider).options.push(option);
  }
  return groups;
}

export function normalizeSidebarWidth(value, fallback = defaultSidebarWidth) {
  const parsed = Number.parseFloat(value);
  const normalizedFallback = Number.isFinite(Number(fallback)) ? Number(fallback) : defaultSidebarWidth;
  if (!Number.isFinite(parsed)) return Math.min(maxSidebarWidth, Math.max(minSidebarWidth, Math.round(normalizedFallback)));
  const rounded = Math.round(parsed);
  return Math.min(maxSidebarWidth, Math.max(minSidebarWidth, rounded));
}

export function normalizeCollapsedPreference(value, fallback = false) {
  if (value === true || value === 1 || value === "1" || value === "true") return true;
  if (value === false || value === 0 || value === "0" || value === "false") return false;
  if (value === null || value === undefined || value === "") return Boolean(fallback);
  return Boolean(fallback);
}

export function sidebarWidthFromPointer(clientX, sidebarLeft) {
  return normalizeSidebarWidth(Number(clientX) - Number(sidebarLeft));
}

// The utility panel (conversation details / background tasks / workspace
// preview) shares the app shell's 4th grid column. It only joins the grid at
// the >=1280px breakpoint used by workbench.css/extras.css; below that it is
// a fixed-position overlay with its own width, so the resize handle has no
// effect (and stays hidden) there.
export const utilityPanelWidthPreferenceKey = "autoto.ui.utilityPanelWidth";
// Low enough to reach the panel's narrow, phone-shaped tier. The old 320 floor
// stopped the drag before the compact layout could ever apply, which read as the
// handle jamming partway.
export const minUtilityPanelWidth = 260;
// Raised from 900, which had become the thing that ended the drag on a wide
// screen rather than a deliberate limit: at 1568px with the sidebar anywhere near
// its default, 900 was reached while the chat column was still wide, so the panel
// could not compress the chat the way dragging the sidebar can. The viewport-aware
// ceiling below is the real guard, so this only has to be past any layout the
// available space will actually permit.
export const maxUtilityPanelWidth = 1200;
// The chat column's floor, held back from the panel. Lowered from 420 so dragging
// from the right can take the composer into its narrow, phone-shaped tier, which
// begins at a 480px container. A 420 floor left the chat sitting just above that
// tier, which is why only the sidebar appeared able to produce the compact layout.
export const utilityPanelChatMinWidth = 360;
export const utilityPanelDesktopBreakpoint = 1280;

export function normalizeUtilityPanelWidth(value, fallback = maxUtilityPanelWidth, { maxAvailable } = {}) {
  const normalizedFallback = Number.isFinite(Number(fallback)) ? Number(fallback) : maxUtilityPanelWidth;
  const cap = Number.isFinite(Number(maxAvailable))
    ? Math.max(minUtilityPanelWidth, Math.min(maxUtilityPanelWidth, Math.floor(Number(maxAvailable))))
    : maxUtilityPanelWidth;
  const parsed = Number.parseFloat(value);
  if (!Number.isFinite(parsed)) return Math.min(cap, Math.max(minUtilityPanelWidth, Math.round(normalizedFallback)));
  return Math.min(cap, Math.max(minUtilityPanelWidth, Math.round(parsed)));
}

export function utilityPanelWidthFromPointer(clientX, viewportRight) {
  return normalizeUtilityPanelWidth(Number(viewportRight) - Number(clientX));
}

// `sidebarInsideRail` describes the docked navigation layout, where the
// conversation list is a real DOM child of the rail instead of its own grid
// column. The rail's measured width already contains the list there, and the
// shell's second grid column is 0px, so subtracting the list as well removed
// roughly a sidebar's worth of travel from the drag. That missing travel is why
// the middle column could not be pushed into its phone-shaped tier from the
// docked layout the way it can from the two-column one.
export function utilityPanelMaxAvailable({
  viewportWidth,
  railWidth = 0,
  sidebarWidth = 0,
  chatMinWidth = utilityPanelChatMinWidth,
  sidebarInsideRail = false,
} = {}) {
  const sidebarColumn = sidebarInsideRail ? 0 : (Number(sidebarWidth) || 0);
  return (Number(viewportWidth) || 0) - (Number(railWidth) || 0) - sidebarColumn - (Number(chatMinWidth) || 0);
}

export function elementVisible(id) {
  const node = $(id);
  return Boolean(node && !node.classList.contains("hidden"));
}

export function isComposingInput(event) {
  return Boolean(event.isComposing || event.keyCode === 229);
}

export function createUIShellController({
  state,
  clearSettingsSearchQuery,
  closeBackendsModal,
  closeDirectoryModal,
  closeSettingsModal,
  focusSettingsSearchInput,
  normalizedSettingsSearchQuery,
  openDirectoryChooser,
  getMessageMode = () => "execute",
  setMessageMode = () => {},
  getSummaryModel = () => "",
  setSummaryModel = null,
  renderProjects,
  onLayoutChange = renderProjects,
  resizeTerminal,
  // Injected so the shell does not need to know how metrics are fetched; it only
  // says when the drawer is visible. Default to no-ops so existing callers and
  // tests that do not pass them keep working unchanged.
  startDrawerMetrics = () => {},
  stopDrawerMetrics = () => {},
  showError,
  translate = (key) => key,
  // Overridable for tests, same convention as provider-console.mjs's requestAPI.
  requestAPI = api,
} = {}) {
  let settingsDialogFocusReturn = null;
  const mobileViewport = () => window.matchMedia?.("(max-width: 767px)")?.matches
    ?? (globalThis.innerWidth || document.documentElement.clientWidth) <= 767;
  const resolveMessageMode = () => (getMessageMode?.() === "plan" ? "plan" : "execute");

  function isVisibleDialog(node) {
    if (!node || node.classList?.contains("hidden") || node.getAttribute?.("aria-hidden") === "true" || node.closest?.("[hidden], .hidden, [aria-hidden=\"true\"]")) return false;
    const view = node.ownerDocument?.defaultView || globalThis.window;
    const style = view?.getComputedStyle?.(node);
    return !style || (style.display !== "none" && style.visibility !== "hidden");
  }

  function settingsDialogHasNestedModal() {
    const dialog = $("settingsModal");
    if (!isVisibleDialog(dialog)) return false;
    return [...(dialog.querySelectorAll?.('[role="dialog"][aria-modal="true"]') || [])]
      .some((node) => node !== dialog && isVisibleDialog(node));
  }

  function focusableDialogElements(dialog) {
    if (!dialog?.querySelectorAll) return [];
    return [...dialog.querySelectorAll('a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')]
      .filter((node) => isVisibleDialog(node) && !node.closest?.(".hidden, [aria-hidden=\"true\"]"));
  }

  function focusSettingsDialogInitialTarget() {
    const dialog = $("settingsModal");
    if (!isVisibleDialog(dialog) || settingsDialogHasNestedModal()) return;
    const input = $("settingsSearchInput");
    const [first] = focusableDialogElements(dialog);
    (isVisibleDialog(input) ? input : first || dialog).focus?.();
  }

  function beginSettingsDialogFocus(trigger = document.activeElement) {
    settingsDialogFocusReturn = trigger?.isConnected === false ? null : trigger || null;
    const schedule = globalThis.queueMicrotask || ((callback) => Promise.resolve().then(callback));
    schedule(focusSettingsDialogInitialTarget);
  }

  function restoreSettingsDialogFocus() {
    const target = settingsDialogFocusReturn;
    settingsDialogFocusReturn = null;
    if (target?.isConnected !== false) target?.focus?.();
  }

  function handleSettingsDialogKeydown(event) {
    if (event.key !== "Tab" || settingsDialogHasNestedModal()) return;
    const dialog = $("settingsModal");
    if (!isVisibleDialog(dialog)) return;
    const items = focusableDialogElements(dialog);
    if (!items.length) {
      event.preventDefault();
      dialog.focus?.();
      return;
    }
    const current = document.activeElement;
    const index = items.indexOf(current);
    const next = event.shiftKey
      ? items[index <= 0 ? items.length - 1 : index - 1]
      : items[index === items.length - 1 ? 0 : index + 1];
    if (index === -1 || next) {
      event.preventDefault();
      next?.focus?.();
    }
  }

  function sidebarSettingsMenuOpen() {
    const menu = $("sidebarSettingsMenu");
    return Boolean(menu && !menu.classList.contains("hidden"));
  }

  function setSidebarSettingsMenuOpen(open) {
    const menu = $("sidebarSettingsMenu");
    const button = $("sidebarAccountBtn");
    if (!menu) return;
    menu.classList.toggle("hidden", !open);
    button?.setAttribute("aria-expanded", open ? "true" : "false");
    button?.classList.toggle("open", open);
  }

  function toggleSidebarSettingsMenu() {
    setSidebarSettingsMenuOpen(!sidebarSettingsMenuOpen());
  }

  function closeSidebarSettingsMenu() {
    setSidebarSettingsMenuOpen(false);
  }

  function handleSidebarSettingsMenuDocumentClick(event) {
    if (!sidebarSettingsMenuOpen()) return;
    if (event.target.closest?.(".sidebar-footer")) return;
    closeSidebarSettingsMenu();
  }

  function handleDirectoryShortcutClick(event) {
    const trigger = event.target.closest?.("[data-open-directory-shortcut]");
    if (!trigger) return;
    event.preventDefault();
    event.stopPropagation();
    const path = trigger.dataset.openDirectoryShortcut === "current"
      ? (state.agent?.cwd || state.project?.gitPath || "")
      : "";
    const isMobileViewport = mobileViewport();
    if (document.body.classList.contains("mobile-sidebar-open")) closeMobileSidebar();
    openDirectoryChooser(path, { trigger, preferNative: !isMobileViewport }).catch(showError);
  }

  function handleGlobalEscape(event) {
    if (event.defaultPrevented || event.key !== "Escape" || isComposingInput(event)) return;
    if (sidebarSettingsMenuOpen()) {
      closeSidebarSettingsMenu();
      event.preventDefault();
      return;
    }
    if (elementVisible("folderModal")) {
      closeDirectoryModal();
      event.preventDefault();
      return;
    }
    if (elementVisible("backendsModal")) {
      closeBackendsModal();
      event.preventDefault();
      return;
    }
    if (elementVisible("settingsModal")) {
      if (settingsDialogHasNestedModal()) return;
      if (normalizedSettingsSearchQuery()) {
        clearSettingsSearchQuery({ focus: document.activeElement?.id === "settingsSearchInput" });
        event.preventDefault();
        return;
      }
      closeSettingsModal();
      event.preventDefault();
    }
  }

  function handleSettingsSearchShortcut(event) {
    if (!elementVisible("settingsModal") || isComposingInput(event)) return;
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "f") {
      focusSettingsSearchInput({ select: true });
      event.preventDefault();
    }
  }

  function openMobileSidebar() {
    if (!mobileViewport()) return;
    closeSidebarSettingsMenu({ restoreFocus: false });
    document.body.classList.add("mobile-sidebar-open");
    $("mobileMenuBtn")?.setAttribute("aria-expanded", "true");
    $("mobileSidebarBackdrop")?.classList.remove("hidden");
    $("sessionSidebar")?.setAttribute("aria-hidden", "false");
    $("mobileSidebarCloseBtn")?.focus();
    // The drawer becoming visible is the moment those rows are read, so the list is
    // rendered here rather than relying on whatever last happened to render it.
    // renderProjects advances the seen mark for the open conversation, and without
    // this the row for the conversation you are already looking at could stay green:
    // a reply that lands while the app is backgrounded is not picked up by the
    // navigation refresh (it skips while the page is hidden), so nothing had
    // advanced the mark by the time the drawer opened.
    renderProjects?.();
    // Metrics poll only while the drawer is on screen, which is the same rule the
    // dashboard follows: a closed drawer would otherwise keep asking the server
    // every three seconds for numbers nobody can see.
    startDrawerMetrics?.();
    resizeTerminal?.();
  }

  function closeMobileSidebar(options = {}) {
    const wasOpen = document.body.classList.contains("mobile-sidebar-open");
    document.body.classList.remove("mobile-sidebar-open");
    $("mobileMenuBtn")?.setAttribute("aria-expanded", "false");
    $("mobileSidebarBackdrop")?.classList.add("hidden");
    if (mobileViewport()) $("sessionSidebar")?.setAttribute("aria-hidden", "true");
    else $("sessionSidebar")?.removeAttribute("aria-hidden");
    closeSidebarSettingsMenu({ restoreFocus: false });
    if (wasOpen && options.restoreFocus !== false) $("mobileMenuBtn")?.focus();
    stopDrawerMetrics?.();
    resizeTerminal?.();
  }

  function openProjectSearch({ focus = true } = {}) {
    $("projectSearchWrap")?.classList.remove("hidden");
    $("projectSearchToggleBtn")?.classList.add("active");
    if (focus) setTimeout(() => $("projectSearch")?.focus(), 30);
  }

  function closeProjectSearch({ clear = false } = {}) {
    if (clear) {
      state.projectQuery = "";
      if ($("projectSearch")) $("projectSearch").value = "";
      renderProjects();
    }
    $("projectSearchWrap")?.classList.add("hidden");
    $("projectSearchToggleBtn")?.classList.remove("active");
  }

  function toggleProjectSearch() {
    const wrap = $("projectSearchWrap");
    if (!wrap || wrap.classList.contains("hidden")) openProjectSearch();
    else closeProjectSearch({ clear: !state.projectQuery.trim() });
  }

  function focusMobileSearch() {
    openMobileSidebar();
    setTimeout(() => openProjectSearch(), 160);
  }

  // The select menus live in their own module; they get what they need from
  // this closure as parameters. bindComposerSelectMenus stays on the public
  // surface below so callers do not need to know about the split.
  const { bindComposerSelectMenus } = createComposerSelectMenus({
    translate,
    showError,
    requestAPI,
    getSummaryModel,
    setSummaryModel,
    setMessageMode,
    resolveMessageMode,
    mobileViewport,
  });

  function bindSidebarResizer({ storage = globalThis.localStorage } = {}) {
    const shell = $("appShell");
    const sidebar = document.querySelector?.(".sidebar");
    const globalRail = document.querySelector?.(".global-rail");
    const separator = $("sidebarResizeHandle");
    const globalCollapseButton = $("globalRailCollapseBtn");
    const conversationDock = $("railConversationDock");
    const conversationRailRow = document.querySelector?.("[data-rail-conversation-row]");
    const sidebarActions = $("sessionSidebarActions");
    if (!shell || !sidebar || !separator) return () => {};

    let width = defaultSidebarWidth;
    let navigationMode = "columns";
    let dragging = false;
    const originalSeparatorTabIndex = separator.getAttribute?.("tabindex");
    // Where the sidebar lives outside docked mode. Captured once, before any move,
    // so returning it is exact rather than "append to whatever looks right".
    const sidebarHome = { parent: sidebar.parentNode || null, nextSibling: sidebar.nextSibling || null };
    // Same treatment for the search/create cluster: docked mode lifts it onto the
    // conversation row, and it has to go back exactly where it came from.
    const actionsHome = sidebarActions
      ? { parent: sidebarActions.parentNode || null, nextSibling: sidebarActions.nextSibling || null }
      : null;
    let sidebarDocked = false;
    // The layout to return to when leaving the icon rail via the conversation
    // entry: whichever of columns/docked the user was last actually using.
    let lastVisibleNavigationMode = "columns";

    const desktopLayout = () => {
      const media = window.matchMedia?.("(max-width: 767px)");
      if (media) return !media.matches;
      return (globalThis.innerWidth || document.documentElement?.clientWidth || 1024) > 767;
    };
    const toggleClass = (node, name, force) => node?.classList?.toggle?.(name, Boolean(force));
    // Only the icon layout is "collapsed" from the control's point of view: it is
    // the one state where the next press expands. columns and docked both still
    // have somewhere tighter to go, so both read as "collapse". This is why the
    // three-way cycle needs no third i18n key.
    const collapseKey = () => navigationMode === "icons" ? "shell.expandGlobalNavigation" : "shell.collapseGlobalNavigation";
    const syncCollapseControls = () => {
      if (!globalCollapseButton) return;
      const key = collapseKey();
      const label = translate(key);
      globalCollapseButton.setAttribute("aria-expanded", navigationMode !== "icons" ? "true" : "false");
      globalCollapseButton.setAttribute("title", label);
      globalCollapseButton.setAttribute("aria-label", label);
      globalCollapseButton.setAttribute(`data-i18n-title`, key);
      globalCollapseButton.setAttribute(`data-i18n-aria-label`, key);
    };
    const persistWidth = () => {
      try {
        storage?.setItem(sidebarWidthPreferenceKey, String(width));
      } catch {
        // Browser storage is optional; layout resizing still works in memory.
      }
    };
    const persistCollapseState = () => {
      try {
        storage?.setItem(navigationLayoutModePreferenceKey, navigationMode);
        // The legacy booleans are still written so anything reading them keeps
        // seeing a coherent view of the layout: only the icon rail is "collapsed".
        // The session column's own compact stage is gone -- the rail's three-way
        // cycle covers shrinking navigation -- so its flag is pinned false and
        // an old stored "true" can never narrow the column again.
        storage?.setItem(globalRailCollapsedPreferenceKey, navigationMode === "icons" ? "true" : "false");
        storage?.setItem(sessionSidebarCollapsedPreferenceKey, "false");
      } catch {
        // Browser storage is optional; collapse state still works in memory.
      }
    };
    const requestTerminalResize = () => {
      if (typeof globalThis.requestAnimationFrame === "function") globalThis.requestAnimationFrame(() => resizeTerminal?.());
      else resizeTerminal?.();
    };
    const sidebarMode = () => width <= narrowSidebarMaxWidth ? "narrow" : "expanded";
    // Docking is a real DOM move rather than a CSS reorder: the list has to render
    // *inside* the rail's scroll box, between the conversation entry and the
    // entries below it, and grid reordering cannot nest one column inside another.
    //
    // The phone drawer is the reason this always reverses on a narrow viewport --
    // .global-rail is display:none there, and the drawer is #sessionSidebar itself,
    // so leaving it parented to a hidden rail would make the drawer unreachable.
    const applySidebarDocking = (docked) => {
      if (!conversationDock || sidebarDocked === docked) return;
      if (docked) {
        conversationDock.removeAttribute?.("hidden");
        conversationDock.appendChild?.(sidebar);
        if (sidebarActions && conversationRailRow?.appendChild) conversationRailRow.appendChild(sidebarActions);
        sidebarDocked = true;
        return;
      }
      if (sidebarHome.parent?.insertBefore) sidebarHome.parent.insertBefore(sidebar, sidebarHome.nextSibling);
      if (sidebarActions && actionsHome?.parent?.insertBefore) actionsHome.parent.insertBefore(sidebarActions, actionsHome.nextSibling);
      conversationDock.setAttribute?.("hidden", "");
      sidebarDocked = false;
    };
    const applyLayoutState = ({ saveWidth = false, saveCollapse = false } = {}) => {
      const desktop = desktopLayout();
      const layout = desktop ? navigationMode : "columns";
      // narrow describes the standalone column, so it only applies there.
      // Docked always renders the full-width list; icons renders none of it.
      const mode = layout === "columns" && desktop ? sidebarMode() : "expanded";
      const effectiveWidth = width;
      applySidebarDocking(layout === "docked");
      shell.style?.setProperty?.("--session-sidebar-width", `${effectiveWidth}px`);
      separator.setAttribute?.("aria-valuenow", String(effectiveWidth));
      navigationLayoutModes.forEach((candidate) => {
        toggleClass(shell, `nav-mode-${candidate}`, desktop && layout === candidate);
        toggleClass(globalRail, `nav-mode-${candidate}`, desktop && layout === candidate);
      });
      // Kept in sync with the new classes so the existing icon-rail rules, the
      // arrow-flip rule, and anything else keyed on them keep working unchanged.
      toggleClass(shell, "global-rail-collapsed", desktop && layout === "icons");
      toggleClass(shell, "session-sidebar-narrow", desktop && mode === "narrow");
      toggleClass(globalRail, "global-rail-collapsed", desktop && layout === "icons");
      toggleClass(sidebar, "session-sidebar-narrow", desktop && mode === "narrow");
      separator.removeAttribute?.("aria-hidden");
      if (originalSeparatorTabIndex == null) separator.removeAttribute?.("tabindex");
      else separator.setAttribute?.("tabindex", originalSeparatorTabIndex);
      syncCollapseControls();
      if (saveWidth) persistWidth();
      if (saveCollapse) persistCollapseState();
      requestTerminalResize();
      onLayoutChange?.({
        sessionSidebarMode: mode,
        sessionSidebarWidth: effectiveWidth,
        globalRailCollapsed: desktop && layout === "icons",
        navigationLayoutMode: layout,
      });
      return effectiveWidth;
    };
    const applyWidth = (nextWidth, { save = false } = {}) => {
      width = normalizeSidebarWidth(nextWidth);
      return applyLayoutState({ saveWidth: save, saveCollapse: save });
    };
    // The divider drives the layout as well as the width. Total is the divider
    // position; the stored width is then whatever that leaves for the list, which
    // differs by layout because columns spends the first 68px on the icon rail.
    const applyDrag = (total) => {
      const resolved = navigationLayoutModeFromDragWidth(total, navigationMode);
      if (resolved !== navigationMode) {
        if (navigationMode !== "icons") lastVisibleNavigationMode = navigationMode;
        navigationMode = resolved;
      }
      // The icon rail is a fixed 48px, so there is no width to track while the
      // pointer is inside its band; only the layout switch matters.
      if (navigationMode === "icons") return applyLayoutState({ saveCollapse: false });
      const listWidth = navigationMode === "columns" ? total - globalRailExpandedWidth : total;
      return applyWidth(listWidth);
    };
    const applyCollapseState = ({ save = false } = {}) => applyLayoutState({ saveCollapse: save });
    const setNavigationMode = (mode, { save = true } = {}) => {
      const next = normalizeNavigationLayoutMode(mode, navigationMode);
      if (next === navigationMode) return navigationMode;
      finishDrag();
      if (navigationMode !== "icons") lastVisibleNavigationMode = navigationMode;
      navigationMode = next;
      applyCollapseState({ save });
      return navigationMode;
    };
    const cycleNavigationMode = () => {
      if (!desktopLayout()) return navigationMode;
      return setNavigationMode(nextNavigationLayoutMode(navigationMode));
    };
    try {
      const storedWidth = normalizeSidebarWidth(storage?.getItem(sidebarWidthPreferenceKey));
      // A width in the retired compact band means the install last saw the old
      // collapsed column; it comes back at the default width instead.
      width = storedWidth <= compactSidebarEnterWidth ? defaultSidebarWidth : storedWidth;
      const storedMode = storage?.getItem(navigationLayoutModePreferenceKey);
      // Migration: an existing install has no mode, only the old booleans. A
      // collapsed rail meant icon-only, so that maps to "icons"; everything else
      // was the two-column layout.
      navigationMode = storedMode == null
        ? (normalizeCollapsedPreference(storage?.getItem(globalRailCollapsedPreferenceKey)) ? "icons" : "columns")
        : normalizeNavigationLayoutMode(storedMode);
    } catch {
      width = defaultSidebarWidth;
      navigationMode = "columns";
    }
    // Written on the first paint rather than only on the first toggle. Migration
    // and the non-columns layouts both derive a compact flag that differs from
    // what is on disk, and leaving the old value there means anything reading
    // storage before the user touches a control sees a state that is not the one
    // on screen.
    applyLayoutState({ saveCollapse: true });

    const finishDrag = (event) => {
      if (!dragging) return;
      dragging = false;
      separator.classList?.remove?.("is-dragging");
      document.body?.classList?.remove?.("sidebar-resizing");
      separator.releasePointerCapture?.(event?.pointerId);
      persistWidth();
      persistCollapseState();
      applyLayoutState();
    };
    // Total width of the navigation area, measured from the rail's left edge. That
    // is the one quantity meaning the same thing in all three layouts, and it is
    // also what the user is physically holding, so both the layout thresholds and
    // the width follow from it. Measuring the sidebar instead would shift by the
    // rail width (columns) or by the rail's padding (docked) and the dragged edge
    // would trail the cursor.
    const dragTotalWidth = (clientX) => {
      const railLeft = globalRail?.getBoundingClientRect?.()?.left;
      if (railLeft !== undefined) return Number(clientX) - (Number(railLeft) || 0);
      // No rail element: the sidebar's own edge is the only reference available, so
      // the rail's nominal width is added back to keep `total` on the same scale
      // the thresholds and the per-layout width maths expect.
      const sidebarLeft = Number(sidebar?.getBoundingClientRect?.()?.left) || 0;
      return Number(clientX) - sidebarLeft + globalRailExpandedWidth;
    };
    const handlePointerMove = (event) => {
      if (!dragging) return;
      applyDrag(dragTotalWidth(event.clientX));
      event.preventDefault();
    };
    const handlePointerDown = (event) => {
      if (!desktopLayout() || (event.button !== undefined && event.button !== 0)) return;
      dragging = true;
      separator.classList?.add?.("is-dragging");
      document.body?.classList?.add?.("sidebar-resizing");
      separator.setPointerCapture?.(event.pointerId);
      // Deliberately not applying the press position. The divider does not always
      // rest at the stored boundary -- on the overview page it sits on the rail's
      // edge -- so applying on press alone would collapse the layout the instant
      // the divider was touched. The first actual move drives it instead, which
      // also means a stray click can no longer change the layout.
    };
    const handleKeyDown = (event) => {
      if (!desktopLayout()) return;
      const step = event.shiftKey ? 24 : 8;
      if (event.key === "Home") {
        width = minSidebarWidth;
        applyLayoutState({ saveWidth: true, saveCollapse: true });
      } else if (event.key === "End") {
        width = maxSidebarWidth;
        applyLayoutState({ saveWidth: true, saveCollapse: true });
      } else if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
        applyWidth(width + (event.key === "ArrowLeft" ? -step : step), { save: true });
      } else {
        return;
      }
      event.preventDefault();
    };
    const wheelTargetCanScroll = (node, event) => {
      if (!node || node.classList?.contains?.("hidden")) return false;
      const lineScale = event.deltaMode === 1 ? 16 : 1;
      const pageScale = event.deltaMode === 2 ? Math.max(1, Number(node.clientHeight) || 1) : 1;
      const deltaX = (Number(event.deltaX) || 0) * lineScale * pageScale;
      const deltaY = (Number(event.deltaY) || 0) * lineScale * pageScale;
      const maxLeft = Math.max(0, (Number(node.scrollWidth) || 0) - (Number(node.clientWidth) || 0));
      const maxTop = Math.max(0, (Number(node.scrollHeight) || 0) - (Number(node.clientHeight) || 0));
      const currentLeft = Number(node.scrollLeft) || 0;
      const currentTop = Number(node.scrollTop) || 0;
      const canScrollX = deltaX < 0 ? currentLeft > 0 : deltaX > 0 && currentLeft < maxLeft;
      const canScrollY = deltaY < 0 ? currentTop > 0 : deltaY > 0 && currentTop < maxTop;
      if (!canScrollX && !canScrollY) return false;
      if (canScrollX) node.scrollLeft = Math.min(maxLeft, Math.max(0, currentLeft + deltaX));
      if (canScrollY) node.scrollTop = Math.min(maxTop, Math.max(0, currentTop + deltaY));
      return true;
    };
    const handleWheel = (event) => {
      if (!desktopLayout() || dragging) return;
      const bounds = separator.getBoundingClientRect?.();
      const separatorCenter = (Number(bounds?.left) || 0) + (Number(bounds?.width) || 0) / 2;
      const sidebarSide = Number(event.clientX) < separatorCenter;
      const candidates = sidebarSide
        ? [$("projects"), document.querySelector?.(".agent-list-section")]
        : document.body?.classList?.contains?.("workbench-mode")
          ? [$("taskWorkspaceOverview"), $("projectKanbanBody")]
          : [$("messages")];
      if (!candidates.some((node) => wheelTargetCanScroll(node, event))) return;
      event.preventDefault();
    };
    const resetWidth = () => {
      if (!desktopLayout()) return;
      width = defaultSidebarWidth;
      applyLayoutState({ saveWidth: true, saveCollapse: true });
    };
    // The icon rail hides the conversation list entirely, so pressing the
    // conversation entry has to bring a layout back that can actually show it --
    // otherwise the list is unreachable without finding the collapse arrow.
    const handleConversationRailClick = () => {
      if (!desktopLayout() || navigationMode !== "icons") return;
      setNavigationMode(lastVisibleNavigationMode);
    };
    const handleGlobalCollapseClick = (event) => {
      event.preventDefault();
      event.stopPropagation();
      cycleNavigationMode();
    };
    const handleViewportChange = () => {
      if (!desktopLayout()) finishDrag();
      applyCollapseState();
    };

    const conversationRailButton = document.querySelector?.('[data-global-rail-target="conversation"]');
    separator.addEventListener("pointerdown", handlePointerDown);
    separator.addEventListener("keydown", handleKeyDown);
    separator.addEventListener("wheel", handleWheel, { passive: false });
    separator.addEventListener("dblclick", resetWidth);
    globalCollapseButton?.addEventListener?.("click", handleGlobalCollapseClick);
    conversationRailButton?.addEventListener?.("click", handleConversationRailClick);
    window.addEventListener("pointermove", handlePointerMove);
    window.addEventListener("pointerup", finishDrag);
    window.addEventListener("pointercancel", finishDrag);
    window.addEventListener("resize", handleViewportChange);
    window.addEventListener("orientationchange", handleViewportChange);

    return () => {
      finishDrag();
      separator.removeEventListener("pointerdown", handlePointerDown);
      separator.removeEventListener("keydown", handleKeyDown);
      separator.removeEventListener("wheel", handleWheel);
      separator.removeEventListener("dblclick", resetWidth);
      globalCollapseButton?.removeEventListener?.("click", handleGlobalCollapseClick);
      conversationRailButton?.removeEventListener?.("click", handleConversationRailClick);
      window.removeEventListener("pointermove", handlePointerMove);
      window.removeEventListener("pointerup", finishDrag);
      window.removeEventListener("pointercancel", finishDrag);
      window.removeEventListener("resize", handleViewportChange);
      window.removeEventListener("orientationchange", handleViewportChange);
    };
  }

  function bindUtilityPanelResizer({ storage = globalThis.localStorage } = {}) {
    const shell = $("appShell");
    const separator = $("utilityPanelResizeHandle");
    if (!shell || !separator) return () => {};

    const globalRail = document.querySelector?.(".global-rail");
    const sidebar = document.querySelector?.(".sidebar");
    const detailsPanel = $("conversationDetailsPanel");
    const backgroundPanel = $("backgroundTaskTray");

    let width = null;
    let dragging = false;

    const wideLayout = () => {
      const media = window.matchMedia?.(`(min-width: ${utilityPanelDesktopBreakpoint}px)`);
      if (media) return media.matches;
      return (globalThis.innerWidth || document.documentElement?.clientWidth || 1024) >= utilityPanelDesktopBreakpoint;
    };
    const panelOpen = () => Boolean(
      shell.classList?.contains("details-open")
      || shell.classList?.contains("background-tasks-open")
      || shell.classList?.contains("preview-open"),
    );
    const viewportRight = () => document.documentElement?.clientWidth || globalThis.innerWidth || 0;
    const currentAvailableMax = () => utilityPanelMaxAvailable({
      viewportWidth: globalThis.innerWidth || document.documentElement?.clientWidth || 0,
      railWidth: globalRail?.getBoundingClientRect?.()?.width ?? 0,
      sidebarWidth: sidebar?.getBoundingClientRect?.()?.width ?? 0,
      // Asked of the DOM rather than read off a class, because containment is the
      // double-count itself: docking moves .sidebar into the rail, so the rail's
      // rect already includes it. Re-checked on every call because docking is
      // reversible while the resizer stays bound.
      sidebarInsideRail: Boolean(globalRail && sidebar && globalRail.contains?.(sidebar)),
    });
    const currentPanelWidth = () => {
      const node = shell.classList?.contains("details-open") ? detailsPanel
        : shell.classList?.contains("background-tasks-open") ? backgroundPanel
        : shell.classList?.contains("preview-open") ? document.querySelector?.(".workspace-preview-dock-mode .workspace-modal-card")
        : null;
      const rectWidth = node?.getBoundingClientRect?.()?.width;
      return Number.isFinite(rectWidth) && rectWidth > 0 ? rectWidth : null;
    };
    const persistWidth = () => {
      try {
        storage?.setItem(utilityPanelWidthPreferenceKey, String(width));
      } catch {
        // Browser storage is optional; layout resizing still works in memory.
      }
    };
    const applyWidth = (nextWidth, { save = false } = {}) => {
      const candidate = normalizeUtilityPanelWidth(nextWidth, width ?? currentPanelWidth() ?? maxUtilityPanelWidth, { maxAvailable: currentAvailableMax() });
      width = candidate;
      // Set on the document root, not #appShell: the browser preview is a
      // fixed-position modal that is a SIBLING of #appShell, so it can only
      // inherit --utility-panel-width from a common ancestor. Setting it here
      // lets both the grid columns and the docked preview card follow the drag.
      document.documentElement?.style?.setProperty?.("--utility-panel-width", `${width}px`);
      separator.setAttribute?.("aria-valuenow", String(width));
      if (save) persistWidth();
      return width;
    };

    try {
      const stored = storage?.getItem(utilityPanelWidthPreferenceKey);
      if (stored != null) applyWidth(stored);
    } catch {
      // Browser storage is optional; the panel keeps its default responsive width.
    }

    const finishDrag = (event) => {
      if (!dragging) return;
      dragging = false;
      separator.classList?.remove?.("is-dragging");
      document.body?.classList?.remove?.("utility-panel-resizing");
      separator.releasePointerCapture?.(event?.pointerId);
      persistWidth();
    };
    const handlePointerMove = (event) => {
      if (!dragging) return;
      applyWidth(Number(viewportRight()) - Number(event.clientX));
      event.preventDefault();
    };
    const handlePointerDown = (event) => {
      if (!wideLayout() || !panelOpen() || (event.button !== undefined && event.button !== 0)) return;
      if (width == null) width = normalizeUtilityPanelWidth(currentPanelWidth() ?? maxUtilityPanelWidth, undefined, { maxAvailable: currentAvailableMax() });
      dragging = true;
      separator.classList?.add?.("is-dragging");
      document.body?.classList?.add?.("utility-panel-resizing");
      separator.setPointerCapture?.(event.pointerId);
      handlePointerMove(event);
    };
    const handleKeyDown = (event) => {
      if (!wideLayout() || !panelOpen()) return;
      const step = event.shiftKey ? 24 : 8;
      if (width == null) width = normalizeUtilityPanelWidth(currentPanelWidth() ?? maxUtilityPanelWidth, undefined, { maxAvailable: currentAvailableMax() });
      if (event.key === "Home") {
        applyWidth(minUtilityPanelWidth, { save: true });
      } else if (event.key === "End") {
        applyWidth(maxUtilityPanelWidth, { save: true });
      } else if (event.key === "ArrowLeft") {
        applyWidth(width + step, { save: true });
      } else if (event.key === "ArrowRight") {
        applyWidth(width - step, { save: true });
      } else {
        return;
      }
      event.preventDefault();
    };
    const resetWidth = () => {
      if (!wideLayout()) return;
      width = null;
      document.documentElement?.style?.removeProperty?.("--utility-panel-width");
      try {
        storage?.removeItem?.(utilityPanelWidthPreferenceKey);
      } catch {
        // Browser storage is optional; clearing in-memory state is enough.
      }
      const fallbackWidth = currentPanelWidth();
      if (fallbackWidth) separator.setAttribute?.("aria-valuenow", String(Math.round(fallbackWidth)));
    };
    const handleViewportChange = () => {
      if (!wideLayout()) finishDrag();
      if (width != null) applyWidth(width);
    };

    separator.addEventListener("pointerdown", handlePointerDown);
    separator.addEventListener("keydown", handleKeyDown);
    separator.addEventListener("dblclick", resetWidth);
    window.addEventListener("pointermove", handlePointerMove);
    window.addEventListener("pointerup", finishDrag);
    window.addEventListener("pointercancel", finishDrag);
    window.addEventListener("resize", handleViewportChange);
    window.addEventListener("orientationchange", handleViewportChange);

    return () => {
      finishDrag();
      separator.removeEventListener("pointerdown", handlePointerDown);
      separator.removeEventListener("keydown", handleKeyDown);
      separator.removeEventListener("dblclick", resetWidth);
      window.removeEventListener("pointermove", handlePointerMove);
      window.removeEventListener("pointerup", finishDrag);
      window.removeEventListener("pointercancel", finishDrag);
      window.removeEventListener("resize", handleViewportChange);
      window.removeEventListener("orientationchange", handleViewportChange);
    };
  }

  return {
    beginSettingsDialogFocus,
    bindComposerSelectMenus,
    bindSidebarResizer,
    bindUtilityPanelResizer,
    closeMobileSidebar,
    closeProjectSearch,
    closeSidebarSettingsMenu,
    focusMobileSearch,
    handleDirectoryShortcutClick,
    handleGlobalEscape,
    handleSettingsDialogKeydown,
    handleSettingsSearchShortcut,
    handleSidebarSettingsMenuDocumentClick,
    openMobileSidebar,
    restoreSettingsDialogFocus,
    settingsDialogHasNestedModal,
    toggleProjectSearch,
    toggleSidebarSettingsMenu,
  };
}
