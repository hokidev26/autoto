import { $ } from "./dom.mjs";

export const sidebarWidthPreferenceKey = "autoto.ui.sessionSidebarWidth";
export const globalRailCollapsedPreferenceKey = "autoto.ui.globalRailCollapsed";
export const sessionSidebarCollapsedPreferenceKey = "autoto.ui.sessionSidebarCollapsed";
export const legacySidebarCollapsedPreferenceKey = "autoto.ui.sidebarCollapsed";
export const defaultSidebarWidth = 296;
export const minSidebarWidth = 184;
export const compactSidebarWidth = 184;
export const compactSidebarEnterWidth = 196;
export const compactSidebarExitWidth = 216;
export const narrowSidebarMinWidth = 197;
export const narrowSidebarMaxWidth = 219;
export const maxSidebarWidth = 420;
export const globalRailExpandedWidth = 68;
export const globalRailCollapsedWidth = 48;
export const collapsedSidebarWidth = compactSidebarWidth;
export const permissionMenuPrimaryValues = Object.freeze(["default", "acceptEdits", "bypassPermissions"]);
export const permissionMenuSecondaryValues = Object.freeze(["readOnly", "dontAsk"]);

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

const permissionMenuIconMarkup = Object.freeze({
  default: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3 19 6v5c0 4.5-2.5 7.8-7 10-4.5-2.2-7-5.5-7-10V6z"></path><path d="M9.5 12h5"></path></svg>',
  acceptEdits: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m14.7 5.3 4 4"></path><path d="M5 19h4l9.7-9.7a2.8 2.8 0 0 0-4-4L5 15z"></path><path d="M13 7 17 11"></path></svg>',
  bypassPermissions: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3 19 6v5c0 4.5-2.5 7.8-7 10-4.5-2.2-7-5.5-7-10V6z"></path><path d="m8.5 8.5 7 7"></path><path d="m15.5 8.5-7 7"></path></svg>',
  readOnly: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M2.5 12s3.5-5 9.5-5 9.5 5 9.5 5-3.5 5-9.5 5-9.5-5-9.5-5z"></path><circle cx="12" cy="12" r="2.5"></circle></svg>',
  dontAsk: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 11V6.5a1.5 1.5 0 0 1 3 0V10"></path><path d="M11 10V5.5a1.5 1.5 0 0 1 3 0V10"></path><path d="M14 10V7a1.5 1.5 0 0 1 3 0v5"></path><path d="M8 10.5 6.7 9.2a1.7 1.7 0 0 0-2.4 2.4l4.2 5.1A5 5 0 0 0 12.4 19H14a5 5 0 0 0 5-5v-3.5a1.5 1.5 0 0 0-3 0"></path></svg>',
  plan: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 4h8a2 2 0 0 1 2 2v14l-3-2-3 2-3-2-3 2V6a2 2 0 0 1 2-2z"></path><path d="M9 9h6M9 13h4"></path></svg>',
  execute: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 7 9 5-9 5z"></path></svg>',
});

export function normalizeSidebarWidth(value, fallback = defaultSidebarWidth, { compact = false } = {}) {
  const parsed = Number.parseFloat(value);
  const normalizedFallback = Number.isFinite(Number(fallback)) ? Number(fallback) : defaultSidebarWidth;
  if (!Number.isFinite(parsed)) return Math.min(maxSidebarWidth, Math.max(minSidebarWidth, Math.round(normalizedFallback)));
  const rounded = Math.round(parsed);
  if (compact && rounded <= compactSidebarEnterWidth) return compactSidebarWidth;
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
export const minUtilityPanelWidth = 320;
export const maxUtilityPanelWidth = 620;
export const utilityPanelChatMinWidth = 420;
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

export function utilityPanelMaxAvailable({ viewportWidth, railWidth = 0, sidebarWidth = 0, chatMinWidth = utilityPanelChatMinWidth } = {}) {
  return (Number(viewportWidth) || 0) - (Number(railWidth) || 0) - (Number(sidebarWidth) || 0) - (Number(chatMinWidth) || 0);
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
  openModelSettings = () => {},
  manageContext = null,
  compactContext = () => {},
  getContextStatus = () => ({}),
  getMessageMode = () => "execute",
  setMessageMode = () => {},
  renderProjects,
  onLayoutChange = renderProjects,
  resizeTerminal,
  showError,
  translate = (key) => key,
} = {}) {
  let settingsDialogFocusReturn = null;
  const manageContextAction = typeof manageContext === "function" ? manageContext : compactContext;
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
    resizeTerminal?.();
  }

  function toggleMobileTerminal() {
    document.body.classList.toggle("mobile-terminal-open");
    if (document.body.classList.contains("mobile-terminal-open")) {
      $("terminalOutput").focus();
      resizeTerminal();
    }
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

  function bindComposerSelectMenus() {
    const triggers = [...(document.querySelectorAll?.("[data-composer-select]") || [])];
    if (!triggers.length) return () => {};

    const menu = document.createElement("div");
    menu.id = "composerSelectPopover";
    menu.className = "composer-select-popover hidden";
    menu.setAttribute("role", "listbox");
    document.body.appendChild(menu);

    const mobileBackdrop = document.createElement("div");
    mobileBackdrop.className = "mobile-select-sheet-backdrop hidden";
    mobileBackdrop.setAttribute("aria-hidden", "true");

    const mobileSheet = document.createElement("section");
    mobileSheet.id = "mobileComposerSelectSheet";
    mobileSheet.className = "mobile-select-sheet";
    mobileSheet.setAttribute("role", "dialog");
    mobileSheet.setAttribute("aria-modal", "true");
    mobileSheet.setAttribute("aria-labelledby", "mobileComposerSelectSheetTitle");

    const mobileHandle = document.createElement("div");
    mobileHandle.className = "mobile-select-sheet-drag-handle";
    mobileHandle.setAttribute("aria-hidden", "true");

    const mobileHeader = document.createElement("div");
    mobileHeader.className = "mobile-select-sheet-header";
    const mobileTitle = document.createElement("h2");
    mobileTitle.id = "mobileComposerSelectSheetTitle";
    mobileTitle.className = "mobile-select-sheet-title";
    const mobileClose = document.createElement("button");
    mobileClose.type = "button";
    mobileClose.className = "mobile-select-sheet-close";
    mobileClose.setAttribute("aria-label", translate("common.close"));
    mobileClose.textContent = "×";
    mobileHeader.append(mobileTitle, mobileClose);

    const mobileBody = document.createElement("div");
    mobileBody.className = "mobile-select-sheet-body";
    mobileSheet.append(mobileHandle, mobileHeader, mobileBody);
    mobileBackdrop.appendChild(mobileSheet);
    document.body.appendChild(mobileBackdrop);

    let active = null;
    let bodyOverflow = "";
    const observers = [];
    const bindings = triggers.map((trigger) => {
      const select = $(trigger.dataset.composerSelect);
      const valueNode = trigger.querySelector(".composer-select-value");
      const label = document.querySelector(`label[for="${trigger.dataset.composerSelect}"]`);
      const binding = {
        trigger,
        select,
        valueNode,
        label,
        ariaHaspopup: trigger.getAttribute("aria-haspopup") || "listbox",
      };
      const sync = () => {
        const option = select?.selectedOptions?.[0] || select?.options?.[select?.selectedIndex];
        if (valueNode && option) {
          const optionText = option.textContent?.trim() || option.value;
          const isModel = trigger.dataset.composerSelect === "modelSelect";
          const presentation = isModel ? modelOptionPresentation(option.value, optionText) : null;
          const displayText = presentation?.provider ? `${presentation.provider}:${presentation.name}` : optionText;
          valueNode.textContent = displayText;
          valueNode.title = displayText;
          if (isModel) {
            valueNode.dataset.mobileLabel = compactComposerModelLabel(option.value || option.textContent);
          }
          const fieldLabel = label?.textContent?.trim();
          trigger.setAttribute("aria-label", fieldLabel ? `${fieldLabel}：${displayText}` : displayText);
        }
        trigger.disabled = Boolean(select?.disabled);
      };
      binding.sync = sync;
      sync();
      select?.addEventListener("change", sync);
      if (select && globalThis.MutationObserver) {
        const observer = new MutationObserver(sync);
        observer.observe(select, { childList: true, subtree: true, attributes: true });
        observers.push(observer);
      }
      return binding;
    }).filter(({ select }) => select);

    const usesMobileSheet = (binding) => mobileViewport()
      && ["modelSelect", "reasoningEffort", "permissionMode"].includes(binding.select.id);

    const close = ({ focus = false } = {}) => {
      if (!active) return;
      const { binding, mobile, returnFocus } = active;
      active = null;
      if (mobile) {
        mobileBackdrop.classList.add("hidden");
        mobileBackdrop.setAttribute("aria-hidden", "true");
        mobileSheet.className = "mobile-select-sheet";
        mobileBody.replaceChildren();
        document.body.style.overflow = bodyOverflow;
      } else {
        menu.classList.add("hidden");
        menu.classList.remove("composer-permission-popover", "composer-model-popover");
        menu.replaceChildren();
      }
      if (binding?.trigger) {
        binding.trigger.setAttribute("aria-expanded", "false");
        binding.trigger.setAttribute("aria-haspopup", binding.ariaHaspopup || "listbox");
        binding.trigger.removeAttribute("aria-controls");
      }
      if (focus && returnFocus?.isConnected !== false) returnFocus?.focus?.();
    };

    const choose = (binding, option) => {
      binding.select.value = option.value;
      const EventConstructor = binding.select.ownerDocument?.defaultView?.Event || globalThis.Event;
      binding.select.dispatchEvent(new EventConstructor("change", { bubbles: true }));
      close({ focus: true });
    };

    const createOptionButton = (binding, option, { permission = false, mobile = false, model = false } = {}) => {
      const selected = option.value === binding.select.value;
      const button = document.createElement("button");
      button.type = "button";
      button.className = [
        "composer-select-option",
        permission ? "composer-permission-option" : "",
        model ? "composer-model-option" : "",
        mobile ? "mobile-select-sheet-option" : "",
      ].filter(Boolean).join(" ");
      button.setAttribute("role", "option");
      button.setAttribute("aria-selected", selected ? "true" : "false");
      button.disabled = option.disabled;

      const label = document.createElement("span");
      label.textContent = option.textContent?.trim() || option.value;
      if (permission) {
        const main = document.createElement("span");
        main.className = "composer-permission-option-main";
        const icon = document.createElement("span");
        icon.className = "composer-permission-option-icon";
        icon.innerHTML = permissionMenuIconMarkup[option.value] || permissionMenuIconMarkup.default;
        main.append(icon, label);
        button.appendChild(main);
      } else if (model) {
        const presentation = modelOptionPresentation(option.value, option.textContent);
        const copy = document.createElement("span");
        copy.className = "composer-model-option-copy";
        label.className = "composer-model-option-name";
        label.textContent = presentation.name;
        const provider = document.createElement("span");
        provider.className = "composer-model-option-provider";
        provider.textContent = presentation.provider || translate("chat.modelProviderFallback");
        copy.append(label, provider);
        button.appendChild(copy);
      } else {
        button.appendChild(label);
      }

      const check = document.createElement("span");
      check.className = "composer-select-option-check";
      check.setAttribute("aria-hidden", "true");
      check.textContent = selected ? "✓" : "";
      button.appendChild(check);
      button.addEventListener("click", () => choose(binding, option));
      return button;
    };

    const createMobileOptionButton = (binding, option, { model = false } = {}) => {
      const selected = option.value === binding.select.value;
      const button = document.createElement("button");
      button.type = "button";
      button.className = "composer-select-option mobile-select-sheet-option";
      button.setAttribute("role", "option");
      button.setAttribute("aria-selected", selected ? "true" : "false");
      button.disabled = option.disabled;

      if (model) {
        const presentation = modelOptionPresentation(option.value, option.textContent);
        const copy = document.createElement("span");
        copy.className = "mobile-model-option-copy";
        const name = document.createElement("span");
        name.className = "mobile-model-option-name";
        name.textContent = presentation.name;
        copy.appendChild(name);
        const provider = document.createElement("span");
        provider.className = "mobile-model-option-provider";
        provider.textContent = presentation.provider || translate("chat.modelProviderFallback");
        copy.appendChild(provider);
        button.appendChild(copy);
      } else {
        const label = document.createElement("span");
        label.textContent = option.textContent?.trim() || option.value;
        button.appendChild(label);
      }

      const check = document.createElement("span");
      check.className = "composer-select-option-check";
      check.setAttribute("aria-hidden", "true");
      check.textContent = selected ? "✓" : "";
      button.appendChild(check);
      button.addEventListener("click", () => choose(binding, option));
      return button;
    };

    const appendPermissionSafetyStatus = (target = menu) => {
      const divider = document.createElement("div");
      divider.className = "composer-permission-divider";
      divider.setAttribute("aria-hidden", "true");

      const status = document.createElement("div");
      status.className = "composer-permission-safety-status";
      status.setAttribute("role", "note");
      const icon = document.createElement("span");
      icon.className = "composer-permission-option-icon composer-permission-safety-icon";
      icon.innerHTML = permissionMenuIconMarkup.default;
      const label = document.createElement("span");
      label.className = "composer-permission-safety-label";
      label.textContent = translate("chat.permissionGuard");
      const state = document.createElement("span");
      state.className = "composer-permission-safety-state";
      state.textContent = translate("common.enabled");
      status.append(icon, label, state);
      target.append(divider, status);
    };

    const chooseMessageMode = (mode) => {
      setMessageMode?.(mode === "plan" ? "plan" : "execute");
      close({ focus: true });
    };

    const createMessageModeOption = (mode, { mobile = false } = {}) => {
      const selected = resolveMessageMode() === mode;
      const button = document.createElement("button");
      button.type = "button";
      button.className = mobile
        ? "composer-select-option mobile-select-sheet-option composer-permission-option"
        : "composer-select-option composer-permission-option";
      button.setAttribute("role", "option");
      button.setAttribute("aria-selected", selected ? "true" : "false");
      button.dataset.messageModeOption = mode;

      const main = document.createElement("span");
      main.className = "composer-permission-option-main";
      const icon = document.createElement("span");
      icon.className = "composer-permission-option-icon";
      icon.innerHTML = permissionMenuIconMarkup[mode] || permissionMenuIconMarkup.execute;
      const label = document.createElement("span");
      label.textContent = mode === "plan"
        ? translate("chat.enterPlanMode")
        : translate("chat.executeMode");
      main.append(icon, label);
      button.appendChild(main);

      const check = document.createElement("span");
      check.className = "composer-select-option-check";
      check.setAttribute("aria-hidden", "true");
      check.textContent = selected ? "✓" : "";
      button.appendChild(check);
      button.addEventListener("click", () => chooseMessageMode(mode));
      return button;
    };

    const appendMessageModeSection = (target = menu, { mobile = false } = {}) => {
      const divider = document.createElement("div");
      divider.className = "composer-permission-divider";
      divider.setAttribute("aria-hidden", "true");
      target.appendChild(divider);

      const heading = document.createElement("div");
      heading.className = "composer-select-popover-title composer-message-mode-section-title";
      heading.textContent = translate("chat.messageMode");
      target.appendChild(heading);
      target.appendChild(createMessageModeOption("execute", { mobile }));
      target.appendChild(createMessageModeOption("plan", { mobile }));
    };

    const appendPermissionOptions = (binding, target = menu, { mobile = false } = {}) => {
      const options = orderPermissionMenuOptions([...binding.select.options].filter((option) => !option.hidden));
      const primary = options.filter((option) => permissionMenuPrimaryValues.includes(option.value));
      const secondary = options.filter((option) => !permissionMenuPrimaryValues.includes(option.value));
      primary.forEach((option) => target.appendChild(createOptionButton(binding, option, { permission: true, mobile })));
      appendPermissionSafetyStatus(target);
      if (secondary.length) {
        const divider = document.createElement("div");
        divider.className = "composer-permission-divider";
        divider.setAttribute("aria-hidden", "true");
        target.appendChild(divider);
        secondary.forEach((option) => target.appendChild(createOptionButton(binding, option, { permission: true, mobile })));
      }
      appendMessageModeSection(target, { mobile });
    };

    const positionMenu = (trigger) => {
      const rect = trigger.getBoundingClientRect();
      const viewportWidth = globalThis.innerWidth || document.documentElement.clientWidth;
      const selectId = trigger.dataset.composerSelect;
      const minimumWidth = selectId === "modelSelect" ? 260 : selectId === "permissionMode" ? 228 : 190;
      const desiredWidth = Math.max(rect.width, minimumWidth);
      const width = Math.min(desiredWidth, viewportWidth - 16);
      const left = Math.min(Math.max(8, rect.left), Math.max(8, viewportWidth - width - 8));
      menu.style.left = `${left}px`;
      menu.style.width = `${width}px`;
      menu.style.bottom = `${Math.max(8, (globalThis.innerHeight || document.documentElement.clientHeight) - rect.top + 6)}px`;
    };

    const createMobileAction = (title, detail, handler, { disabled = false } = {}) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "mobile-model-sheet-action";
      button.disabled = disabled;
      const copy = document.createElement("span");
      copy.className = "mobile-model-sheet-action-copy";
      const titleNode = document.createElement("span");
      titleNode.className = "mobile-model-sheet-action-title";
      titleNode.textContent = title;
      copy.appendChild(titleNode);
      if (detail) {
        const detailNode = document.createElement("span");
        detailNode.className = "mobile-model-sheet-action-detail";
        detailNode.textContent = detail;
        copy.appendChild(detailNode);
      }
      const chevron = document.createElement("span");
      chevron.className = "mobile-model-sheet-action-chevron";
      chevron.setAttribute("aria-hidden", "true");
      chevron.textContent = "›";
      button.append(copy, chevron);
      button.addEventListener("click", handler);
      return button;
    };

    const contextActionSpec = () => {
      const status = getContextStatus?.() || {};
      const prunedPercent = Math.max(0, Math.min(100, Number(status.prunedPercent) || 0));
      const canManage = Boolean(state?.agent?.id);
      const canCompact = Boolean(status.canCompact) && String(state?.agent?.status || "") !== "running";
      return {
        disabled: !canManage,
        detail: canCompact
          ? (status.hasSummary ? translate("chat.contextCompactedDetail", { percent: prunedPercent }) : translate("chat.contextCompactReady"))
          : translate("chat.contextCompactUnavailable"),
      };
    };

    const createDesktopAction = (title, detail, handler, { disabled = false } = {}) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "composer-model-menu-action";
      button.disabled = disabled;
      const copy = document.createElement("span");
      copy.className = "composer-model-menu-action-copy";
      const titleNode = document.createElement("span");
      titleNode.className = "composer-model-menu-action-title";
      titleNode.textContent = title;
      copy.appendChild(titleNode);
      if (detail) {
        const detailNode = document.createElement("span");
        detailNode.className = "composer-model-menu-action-detail";
        detailNode.textContent = detail;
        copy.appendChild(detailNode);
      }
      const chevron = document.createElement("span");
      chevron.className = "composer-model-menu-action-chevron";
      chevron.setAttribute("aria-hidden", "true");
      chevron.textContent = "›";
      button.append(copy, chevron);
      button.addEventListener("click", handler);
      return button;
    };

    const appendDesktopModelActions = (binding) => {
      const divider = document.createElement("div");
      divider.className = "composer-model-menu-divider";
      divider.setAttribute("aria-hidden", "true");
      menu.appendChild(divider);
      const reasoningBinding = bindings.find(({ select }) => select.id === "reasoningEffort");
      if (reasoningBinding) {
        const currentReasoning = reasoningBinding.select.selectedOptions?.[0]
          || reasoningBinding.select.options?.[reasoningBinding.select.selectedIndex];
        const reasoningText = currentReasoning?.textContent?.trim() || currentReasoning?.value || "";
        menu.appendChild(createDesktopAction(translate("chat.reasoningEffort"), reasoningText, () => {
          close();
          open(reasoningBinding);
        }));
      }
      const compactSpec = contextActionSpec();
      menu.appendChild(createDesktopAction(translate("chat.compactContext"), compactSpec.detail, () => {
        close({ focus: true });
        Promise.resolve(manageContextAction({ focusAction: "compact" })).catch(showError);
      }, compactSpec));
      menu.appendChild(createDesktopAction(translate("chat.manageModels"), "", () => {
        close({ focus: true });
        openModelSettings();
      }));
    };

    const openMobile = (binding, { returnFocus = binding.trigger } = {}) => {
      const isModel = binding.select.id === "modelSelect";
      const isPermission = binding.select.id === "permissionMode";
      active = { binding, mobile: true, returnFocus };
      mobileSheet.className = `mobile-select-sheet ${isModel ? "mobile-model-sheet" : isPermission ? "mobile-permission-sheet" : "mobile-reasoning-sheet"}`;
      mobileTitle.textContent = isModel
        ? translate("chat.selectModel")
        : isPermission
          ? translate("chat.permissionMode")
          : (binding.label?.textContent?.trim() || translate("chat.reasoningEffort"));

      const options = document.createElement("div");
      options.className = "mobile-select-sheet-options";
      options.setAttribute("role", "listbox");
      options.setAttribute("aria-label", mobileTitle.textContent);
      if (isPermission) {
        appendPermissionOptions(binding, options, { mobile: true });
        mobileBody.replaceChildren(options);
      } else {
        [...binding.select.options]
          .filter((option) => !option.hidden)
          .forEach((option) => options.appendChild(createMobileOptionButton(binding, option, { model: isModel })));
        mobileBody.replaceChildren(options);
      }

      if (isModel) {
        const actions = document.createElement("div");
        actions.className = "mobile-model-sheet-actions";
        const reasoningBinding = bindings.find(({ select }) => select.id === "reasoningEffort");
        if (reasoningBinding) {
          const currentReasoning = reasoningBinding.select.selectedOptions?.[0]
            || reasoningBinding.select.options?.[reasoningBinding.select.selectedIndex];
          const reasoningText = currentReasoning?.textContent?.trim() || currentReasoning?.value || "";
          actions.appendChild(createMobileAction(translate("chat.reasoningEffort"), reasoningText, () => {
            const focusTarget = active?.returnFocus || binding.trigger;
            close();
            openMobile(reasoningBinding, { returnFocus: focusTarget });
          }));
        }
        const compactSpec = contextActionSpec();
        actions.appendChild(createMobileAction(translate("chat.compactContext"), compactSpec.detail, () => {
          close({ focus: true });
          Promise.resolve(manageContextAction({ focusAction: "compact" })).catch(showError);
        }, compactSpec));
        actions.appendChild(createMobileAction(translate("chat.manageModels"), "", () => {
          close({ focus: true });
          openModelSettings();
        }));
        mobileBody.appendChild(actions);
      }

      bodyOverflow = document.body.style.overflow;
      document.body.style.overflow = "hidden";
      mobileBackdrop.classList.remove("hidden");
      mobileBackdrop.setAttribute("aria-hidden", "false");
      binding.trigger?.setAttribute("aria-haspopup", "dialog");
      binding.trigger?.setAttribute("aria-expanded", "true");
      binding.trigger?.setAttribute("aria-controls", mobileSheet.id);
      (options.querySelector('[aria-selected="true"]') || options.querySelector("button") || mobileClose).focus?.();
    };

    const open = (binding) => {
      if (active?.binding?.trigger === binding.trigger) {
        close();
        return;
      }
      close();
      if (usesMobileSheet(binding)) {
        openMobile(binding);
        return;
      }
      active = { binding, mobile: false, returnFocus: binding.trigger };
      const isPermissionMenu = binding.select.id === "permissionMode";
      const isModelMenu = binding.select.id === "modelSelect";
      menu.classList.toggle("composer-permission-popover", isPermissionMenu);
      menu.classList.toggle("composer-model-popover", isModelMenu);
      const heading = document.createElement("div");
      heading.className = "composer-select-popover-title";
      heading.textContent = binding.label?.textContent?.trim() || binding.select.title || "";
      menu.appendChild(heading);
      if (isPermissionMenu) {
        appendPermissionOptions(binding, menu, { mobile: false });
      } else {
        let previousGroup = null;
        [...binding.select.options]
          .filter((option) => !option.hidden)
          .forEach((option) => {
            const optionButton = createOptionButton(binding, option, { model: isModelMenu });
            if (isModelMenu) {
              const group = option.dataset.provider || "";
              if (previousGroup !== null && group !== previousGroup) optionButton.classList.add("composer-model-option-group-start");
              previousGroup = group;
            }
            menu.appendChild(optionButton);
          });
        if (isModelMenu) appendDesktopModelActions(binding);
      }
      menu.classList.remove("hidden");
      positionMenu(binding.trigger);
      binding.trigger.setAttribute("aria-expanded", "true");
      binding.trigger.setAttribute("aria-controls", menu.id);
      menu.querySelector('[aria-selected="true"]')?.focus();
    };

    const openPermissionMenu = (returnFocus = null) => {
      const permissionBinding = bindings.find(({ select }) => select.id === "permissionMode");
      if (!permissionBinding) return;
      if (returnFocus) {
        close();
        if (usesMobileSheet(permissionBinding)) {
          openMobile(permissionBinding, { returnFocus });
          return;
        }
        open(permissionBinding);
        if (active) {
          active.returnFocus = returnFocus;
          positionMenu(returnFocus);
        }
        return;
      }
      open(permissionBinding);
    };

    const triggerHandlers = bindings.map((binding) => {
      const handler = (event) => {
        event.preventDefault();
        event.stopPropagation();
        open(binding);
      };
      binding.trigger.addEventListener("click", handler);
      return [binding.trigger, handler];
    });

    const messageModeToggle = $("messageModeToggle");
    const messageModeHandler = (event) => {
      event.preventDefault();
      event.stopPropagation();
      if (active?.binding?.select?.id === "permissionMode") {
        close({ focus: true });
        return;
      }
      openPermissionMenu(messageModeToggle || event.currentTarget);
    };
    messageModeToggle?.addEventListener("click", messageModeHandler);

    const handleDocumentPointer = (event) => {
      if (!active || active.mobile) return;
      if (menu.contains(event.target)) return;
      if (active.binding?.trigger?.contains?.(event.target)) return;
      if (messageModeToggle?.contains?.(event.target)) return;
      close();
    };
    const handleDocumentKey = (event) => {
      if (event.key === "Escape" && active) {
        close({ focus: true });
        event.preventDefault();
        return;
      }
      if (event.key !== "Tab" || !active?.mobile) return;
      const focusable = [...mobileSheet.querySelectorAll("button:not([disabled]), [tabindex]:not([tabindex=\"-1\"])")];
      if (!focusable.length) return;
      const currentIndex = focusable.indexOf(document.activeElement);
      const nextIndex = event.shiftKey
        ? (currentIndex <= 0 ? focusable.length - 1 : currentIndex - 1)
        : (currentIndex < 0 || currentIndex === focusable.length - 1 ? 0 : currentIndex + 1);
      focusable[nextIndex]?.focus?.();
      event.preventDefault();
    };
    const handleBackdropClick = (event) => {
      if (event.target !== mobileBackdrop || !active?.mobile) return;
      close({ focus: true });
    };
    const handleCloseClick = () => close({ focus: true });
    const handleViewportChange = () => {
      const restoreFocus = Boolean(active?.mobile);
      close({ focus: restoreFocus });
    };
    const handleDocumentScroll = (event) => {
      if (!active) return;
      if (active.mobile && mobileSheet.contains(event.target)) return;
      if (!active.mobile && (event.target === menu || menu.contains(event.target))) return;
      close();
    };
    mobileBackdrop.addEventListener("click", handleBackdropClick);
    mobileClose.addEventListener("click", handleCloseClick);
    document.addEventListener("pointerdown", handleDocumentPointer);
    document.addEventListener("keydown", handleDocumentKey);
    window.addEventListener("resize", handleViewportChange);
    window.addEventListener("orientationchange", handleViewportChange);
    window.addEventListener("scroll", handleDocumentScroll, true);

    return () => {
      close();
      triggerHandlers.forEach(([trigger, handler]) => trigger.removeEventListener("click", handler));
      messageModeToggle?.removeEventListener("click", messageModeHandler);
      bindings.forEach(({ select, sync }) => select.removeEventListener("change", sync));
      observers.forEach((observer) => observer.disconnect());
      mobileBackdrop.removeEventListener("click", handleBackdropClick);
      mobileClose.removeEventListener("click", handleCloseClick);
      document.removeEventListener("pointerdown", handleDocumentPointer);
      document.removeEventListener("keydown", handleDocumentKey);
      window.removeEventListener("resize", handleViewportChange);
      window.removeEventListener("orientationchange", handleViewportChange);
      window.removeEventListener("scroll", handleDocumentScroll, true);
      mobileBackdrop.remove();
      menu.remove();
    };
  }

  function bindSidebarResizer({ storage = globalThis.localStorage } = {}) {
    const shell = $("appShell");
    const sidebar = document.querySelector?.(".sidebar");
    const globalRail = document.querySelector?.(".global-rail");
    const separator = $("sidebarResizeHandle");
    const globalCollapseButton = $("globalRailCollapseBtn");
    const sessionCollapseButton = $("sessionSidebarCollapseBtn");
    if (!shell || !sidebar || !separator) return () => {};

    let width = defaultSidebarWidth;
    let globalRailCollapsed = false;
    let sessionSidebarCollapsed = false;
    let dragging = false;
    const originalSeparatorTabIndex = separator.getAttribute?.("tabindex");

    const desktopLayout = () => {
      const media = window.matchMedia?.("(max-width: 767px)");
      if (media) return !media.matches;
      return (globalThis.innerWidth || document.documentElement?.clientWidth || 1024) > 767;
    };
    const toggleClass = (node, name, force) => node?.classList?.toggle?.(name, Boolean(force));
    const collapseKey = (global) => global
      ? (globalRailCollapsed ? "shell.expandGlobalNavigation" : "shell.collapseGlobalNavigation")
      : (sessionSidebarCollapsed ? "shell.expandSessionSidebar" : "shell.collapseSessionSidebar");
    const syncCollapseControl = (button, global) => {
      if (!button) return;
      const key = collapseKey(global);
      const label = translate(key);
      button.setAttribute("aria-expanded", (global ? !globalRailCollapsed : !sessionSidebarCollapsed) ? "true" : "false");
      button.setAttribute("title", label);
      button.setAttribute("aria-label", label);
      button.setAttribute(`data-i18n-title`, key);
      button.setAttribute(`data-i18n-aria-label`, key);
    };
    const syncCollapseControls = () => {
      syncCollapseControl(globalCollapseButton, true);
      syncCollapseControl(sessionCollapseButton, false);
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
        storage?.setItem(globalRailCollapsedPreferenceKey, globalRailCollapsed ? "true" : "false");
        storage?.setItem(sessionSidebarCollapsedPreferenceKey, sessionSidebarCollapsed ? "true" : "false");
      } catch {
        // Browser storage is optional; collapse state still works in memory.
      }
    };
    const requestTerminalResize = () => {
      if (typeof globalThis.requestAnimationFrame === "function") globalThis.requestAnimationFrame(() => resizeTerminal?.());
      else resizeTerminal?.();
    };
    const sidebarMode = () => sessionSidebarCollapsed ? "compact" : width <= narrowSidebarMaxWidth ? "narrow" : "expanded";
    const applyLayoutState = ({ saveWidth = false, saveCollapse = false } = {}) => {
      const desktop = desktopLayout();
      const mode = desktop ? sidebarMode() : "expanded";
      const effectiveWidth = mode === "compact" ? compactSidebarWidth : width;
      shell.style?.setProperty?.("--session-sidebar-width", `${effectiveWidth}px`);
      separator.setAttribute?.("aria-valuenow", String(effectiveWidth));
      toggleClass(shell, "global-rail-collapsed", desktop && globalRailCollapsed);
      toggleClass(shell, "session-sidebar-collapsed", desktop && mode === "compact");
      toggleClass(shell, "session-sidebar-narrow", desktop && mode === "narrow");
      toggleClass(globalRail, "global-rail-collapsed", desktop && globalRailCollapsed);
      toggleClass(sidebar, "session-sidebar-collapsed", desktop && mode === "compact");
      toggleClass(sidebar, "session-sidebar-narrow", desktop && mode === "narrow");
      separator.removeAttribute?.("aria-hidden");
      if (originalSeparatorTabIndex == null) separator.removeAttribute?.("tabindex");
      else separator.setAttribute?.("tabindex", originalSeparatorTabIndex);
      syncCollapseControls();
      if (saveWidth && mode !== "compact") persistWidth();
      if (saveCollapse) persistCollapseState();
      requestTerminalResize();
      onLayoutChange?.({ sessionSidebarMode: mode, sessionSidebarWidth: effectiveWidth, globalRailCollapsed: desktop && globalRailCollapsed });
      return effectiveWidth;
    };
    const applyWidth = (nextWidth, { save = false, dragging = false } = {}) => {
      const candidate = normalizeSidebarWidth(nextWidth);
      if (dragging) {
        if (sessionSidebarCollapsed && candidate > compactSidebarExitWidth) sessionSidebarCollapsed = false;
        else if (!sessionSidebarCollapsed && candidate <= compactSidebarEnterWidth) sessionSidebarCollapsed = true;
      }
      if (!sessionSidebarCollapsed) width = candidate;
      return applyLayoutState({ saveWidth: save, saveCollapse: save });
    };
    const applyCollapseState = ({ save = false } = {}) => applyLayoutState({ saveCollapse: save });
    const toggleGlobalRail = () => {
      if (!desktopLayout()) return false;
      const expandAll = globalRailCollapsed && sessionSidebarCollapsed;
      globalRailCollapsed = !expandAll;
      sessionSidebarCollapsed = !expandAll;
      applyCollapseState({ save: true });
      return globalRailCollapsed;
    };
    const toggleSessionSidebar = () => {
      if (!desktopLayout()) return false;
      if (sessionSidebarCollapsed) {
        sessionSidebarCollapsed = false;
        applyCollapseState({ save: true });
        return false;
      }
      finishDrag();
      sessionSidebarCollapsed = true;
      applyCollapseState({ save: true });
      return true;
    };
    try {
      const storedWidth = normalizeSidebarWidth(storage?.getItem(sidebarWidthPreferenceKey));
      const compactFromWidth = storedWidth <= compactSidebarEnterWidth;
      width = compactFromWidth ? defaultSidebarWidth : storedWidth;
      globalRailCollapsed = normalizeCollapsedPreference(storage?.getItem(globalRailCollapsedPreferenceKey));
      const legacyCollapsed = normalizeCollapsedPreference(storage?.getItem(legacySidebarCollapsedPreferenceKey), false);
      const storedSessionCollapsed = storage?.getItem(sessionSidebarCollapsedPreferenceKey);
      sessionSidebarCollapsed = storedSessionCollapsed == null
        ? legacyCollapsed || compactFromWidth
        : normalizeCollapsedPreference(storedSessionCollapsed) || compactFromWidth;
      if (storedSessionCollapsed == null && storage?.setItem) storage.setItem(sessionSidebarCollapsedPreferenceKey, sessionSidebarCollapsed ? "true" : "false");
    } catch {
      width = defaultSidebarWidth;
      globalRailCollapsed = false;
      sessionSidebarCollapsed = false;
    }
    applyLayoutState();

    const finishDrag = (event) => {
      if (!dragging) return;
      dragging = false;
      separator.classList?.remove?.("is-dragging");
      document.body?.classList?.remove?.("sidebar-resizing");
      separator.releasePointerCapture?.(event?.pointerId);
      if (!sessionSidebarCollapsed) persistWidth();
      persistCollapseState();
      applyLayoutState();
    };
    const handlePointerMove = (event) => {
      if (!dragging) return;
      applyWidth(sidebarWidthFromPointer(event.clientX, sidebar.getBoundingClientRect().left), { dragging: true });
      event.preventDefault();
    };
    const handlePointerDown = (event) => {
      if (!desktopLayout() || (event.button !== undefined && event.button !== 0)) return;
      dragging = true;
      separator.classList?.add?.("is-dragging");
      document.body?.classList?.add?.("sidebar-resizing");
      separator.setPointerCapture?.(event.pointerId);
      handlePointerMove(event);
    };
    const handleKeyDown = (event) => {
      if (!desktopLayout()) return;
      const step = event.shiftKey ? 24 : 8;
      if (event.key === "Home") {
        sessionSidebarCollapsed = true;
        applyLayoutState({ saveCollapse: true });
      } else if (event.key === "End") {
        sessionSidebarCollapsed = false;
        width = maxSidebarWidth;
        applyLayoutState({ saveWidth: true, saveCollapse: true });
      } else if (sessionSidebarCollapsed && event.key === "ArrowRight") {
        sessionSidebarCollapsed = false;
        width = 220;
        applyLayoutState({ saveWidth: true, saveCollapse: true });
      } else if (sessionSidebarCollapsed && event.key === "ArrowLeft") {
        applyLayoutState();
      } else if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
        applyWidth(width + (event.key === "ArrowLeft" ? -step : step), { save: true, dragging: true });
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
      sessionSidebarCollapsed = false;
      width = defaultSidebarWidth;
      applyLayoutState({ saveWidth: true, saveCollapse: true });
    };
    const handleGlobalCollapseClick = (event) => {
      event.preventDefault();
      event.stopPropagation();
      toggleGlobalRail();
    };
    const handleSessionCollapseClick = (event) => {
      event.preventDefault();
      event.stopPropagation();
      toggleSessionSidebar();
    };
    const handleViewportChange = () => {
      if (!desktopLayout()) finishDrag();
      applyCollapseState();
    };

    separator.addEventListener("pointerdown", handlePointerDown);
    separator.addEventListener("keydown", handleKeyDown);
    separator.addEventListener("wheel", handleWheel, { passive: false });
    separator.addEventListener("dblclick", resetWidth);
    globalCollapseButton?.addEventListener?.("click", handleGlobalCollapseClick);
    sessionCollapseButton?.addEventListener?.("click", handleSessionCollapseClick);
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
      sessionCollapseButton?.removeEventListener?.("click", handleSessionCollapseClick);
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
      shell.style?.setProperty?.("--utility-panel-width", `${width}px`);
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
      shell.style?.removeProperty?.("--utility-panel-width");
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
    toggleMobileTerminal,
    toggleProjectSearch,
    toggleSidebarSettingsMenu,
  };
}
