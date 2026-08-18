import { $ } from "./dom.mjs";
// These stay exported from ui-shell.mjs because callers and tests import them
// from there. The resulting import cycle is safe: every one of them is read
// inside bindComposerSelectMenus at call time, never while this module is
// still initialising.
import {
  compactComposerModelLabel,
  groupModelSelectOptions,
  modelOptionPresentation,
  orderPermissionMenuOptions,
  permissionMenuPrimaryValues,
} from "./ui-shell.mjs";

// The composer's select menus (model, permission, message mode, summary
// model) with their mobile bottom-sheet variant. Split out of ui-shell.mjs
// to keep that file inside the source size budget. Everything the menus need
// from the shell closure arrives as a parameter, so this module holds no
// state of its own beyond the menu DOM it builds.
// Inline SVG for each permission and message-mode option. Moved here with the
// menus; ui-shell.mjs no longer references it.
const permissionMenuIconMarkup = Object.freeze({
  default: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3 19 6v5c0 4.5-2.5 7.8-7 10-4.5-2.2-7-5.5-7-10V6z"></path><path d="M9.5 12h5"></path></svg>',
  acceptEdits: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m14.7 5.3 4 4"></path><path d="M5 19h4l9.7-9.7a2.8 2.8 0 0 0-4-4L5 15z"></path><path d="M13 7 17 11"></path></svg>',
  bypassPermissions: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3 19 6v5c0 4.5-2.5 7.8-7 10-4.5-2.2-7-5.5-7-10V6z"></path><path d="m8.5 8.5 7 7"></path><path d="m15.5 8.5-7 7"></path></svg>',
  readOnly: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M2.5 12s3.5-5 9.5-5 9.5 5 9.5 5-3.5 5-9.5 5-9.5-5-9.5-5z"></path><circle cx="12" cy="12" r="2.5"></circle></svg>',
  plan: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 4h8a2 2 0 0 1 2 2v14l-3-2-3 2-3-2-3 2V6a2 2 0 0 1 2-2z"></path><path d="M9 9h6M9 13h4"></path></svg>',
  execute: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 7 9 5-9 5z"></path></svg>',
});

// Shared with the composer popover and its tests. CSS min-width on the model
// menu is 290px; if layout used a smaller width, a right-side chip would push
// the panel past the viewport and clip the checkmarks.
export function composerSelectMenuLayout({
  triggerRect = {},
  viewportWidth = 0,
  viewportHeight = 0,
  selectId = "",
  measuredWidth = 0,
} = {}) {
  const viewW = Math.max(0, Number(viewportWidth) || 0);
  const viewH = Math.max(0, Number(viewportHeight) || 0);
  const isModel = selectId === "modelSelect";
  const minimumWidth = isModel ? 290 : selectId === "permissionMode" ? 228 : 190;
  const maxWidth = Math.max(160, viewW - 16);
  const triggerWidth = Math.max(0, Number(triggerRect.width) || 0);
  const width = Math.min(Math.max(triggerWidth, minimumWidth, Number(measuredWidth) || 0), maxWidth);
  const triggerLeft = Number(triggerRect.left) || 0;
  const maxLeft = Math.max(8, viewW - width - 8);
  const left = Math.min(Math.max(8, triggerLeft), maxLeft);
  const triggerTop = Number(triggerRect.top) || 0;
  const bottom = Math.max(8, viewH - triggerTop + 6);
  return { width, left, bottom };
}

export function createComposerSelectMenus({
  translate = (key) => key,
  showError = () => {},
  requestAPI,
  getSummaryModel = () => "",
  setSummaryModel = null,
  setMessageMode = () => {},
  resolveMessageMode = () => "execute",
  getPlanReflection = () => true,
  setPlanReflection = () => {},
  getAgentId = () => "",
  canEditWorkflowPreferences = () => true,
  mobileViewport = () => false,
} = {}) {
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

    // Workflow preferences back the danger-reflection level selector appended in
    // appendPermissionSafetyStatus(). The permission menu is rebuilt from scratch
    // on every open, while the preference itself is fetched once and cached here.
    let workflowPreferences = null;
    let workflowPreferencesPromise = null;
    const dangerReflectionLevelNodes = new Set();
    const dangerReflectionLevels = Object.freeze(["off", "loose", "medium", "strict"]);

    const dangerReflectionLevel = () => {
      const level = String(workflowPreferences?.dangerReflectionLevel || "").trim().toLowerCase();
      return dangerReflectionLevels.includes(level) ? level : "medium";
    };

    const syncDangerReflectionLevelNodes = () => {
      const selectedLevel = dangerReflectionLevel();
      for (const selector of [...dangerReflectionLevelNodes]) {
        if (selector.isConnected === false) {
          dangerReflectionLevelNodes.delete(selector);
          continue;
        }
        selector.querySelectorAll(".composer-permission-danger-reflection-level").forEach((button) => {
          const selected = button.dataset.dangerReflectionLevel === selectedLevel;
          button.classList.toggle("is-selected", selected);
          button.setAttribute("aria-checked", selected ? "true" : "false");
        });
      }
    };

    const loadWorkflowPreferences = () => {
      if (workflowPreferences) return Promise.resolve(workflowPreferences);
      if (workflowPreferencesPromise) return workflowPreferencesPromise;
      if (typeof requestAPI !== "function") return Promise.resolve(null);
      workflowPreferencesPromise = requestAPI("/api/workflow/preferences")
        .then((data) => {
          workflowPreferences = data && typeof data === "object" ? data : {};
          syncDangerReflectionLevelNodes();
          return workflowPreferences;
        })
        .catch(() => null)
        .finally(() => {
          workflowPreferencesPromise = null;
        });
      return workflowPreferencesPromise;
    };

    const setDangerReflectionLevel = async (selector, nextLevel) => {
      if (!canEditWorkflowPreferences?.()) return;
      if (!dangerReflectionLevels.includes(nextLevel) || nextLevel === dangerReflectionLevel()) return;
      let previousPreferences = workflowPreferences;
      selector.setAttribute("aria-busy", "true");
      selector.querySelectorAll(".composer-permission-danger-reflection-level").forEach((button) => { button.disabled = true; });
      try {
        // PUT requires the full preferences object, so the other fields must be
        // known first — if they can't be loaded, refuse rather than risk
        // resending the safety-critical confirmation flags with wrong defaults.
        const current = workflowPreferences || (await loadWorkflowPreferences());
        if (!current || typeof requestAPI !== "function") throw new Error(translate("chat.workflowPreferencesUnavailable"));
        previousPreferences ||= current;
        const payload = {
          requireConfirmationForExec: Boolean(current.requireConfirmationForExec),
          requireConfirmationForWrites: Boolean(current.requireConfirmationForWrites),
          allowReadOnlyByDefault: Boolean(current.allowReadOnlyByDefault),
          dangerReflectionLevel: nextLevel,
        };
        const response = await requestAPI("/api/workflow/preferences", { method: "PUT", body: JSON.stringify(payload) });
        workflowPreferences = response && typeof response === "object" ? response : payload;
      } catch (error) {
        workflowPreferences = previousPreferences;
        showError?.(error);
      } finally {
        selector.removeAttribute("aria-busy");
        const editable = canEditWorkflowPreferences?.() !== false;
        selector.querySelectorAll(".composer-permission-danger-reflection-level").forEach((button) => { button.disabled = !editable; });
        syncDangerReflectionLevelNodes();
      }
    };

    const createDangerReflectionRow = () => {
      const row = document.createElement("div");
      row.className = "composer-permission-safety-status composer-permission-danger-reflection";
      const editable = canEditWorkflowPreferences?.() !== false;
      row.title = translate(editable ? "chat.dangerReflectionDescription" : "chat.dangerReflectionHostOnly");

      const heading = document.createElement("div");
      heading.className = "composer-permission-danger-reflection-heading";
      const icon = document.createElement("span");
      icon.className = "composer-permission-option-icon composer-permission-safety-icon";
      icon.innerHTML = permissionMenuIconMarkup.default;
      const label = document.createElement("span");
      label.className = "composer-permission-safety-label";
      label.textContent = translate("chat.dangerReflection");
      heading.append(icon, label);

      const selector = document.createElement("div");
      selector.className = "composer-permission-danger-reflection-levels";
      selector.setAttribute("role", "radiogroup");
      selector.setAttribute("aria-label", `${translate("chat.dangerReflection")} — ${translate("chat.dangerReflectionDescription")}`);
      const selectedLevel = dangerReflectionLevel();
      dangerReflectionLevels.forEach((level) => {
        const button = document.createElement("button");
        button.type = "button";
        button.className = "composer-permission-danger-reflection-level";
        button.dataset.dangerReflectionLevel = level;
        button.setAttribute("role", "radio");
        const selected = level === selectedLevel;
        button.classList.toggle("is-selected", selected);
        button.setAttribute("aria-checked", selected ? "true" : "false");
        button.disabled = !editable;
        button.textContent = translate(`chat.dangerReflectionLevels.${level}`);
        button.addEventListener("click", () => setDangerReflectionLevel(selector, level));
        selector.appendChild(button);
      });
      dangerReflectionLevelNodes.add(selector);
      row.append(heading, selector);
      loadWorkflowPreferences();
      return row;
    };

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
          const fullValue = presentation?.provider ? `${presentation.provider}:${presentation.name}` : optionText;
          // The menu is already grouped by provider, so the trigger only needs
          // the model name. `codex:gpt-5.6-luna` made the chip look like a
          // stuffed grey card; the full id stays on the tooltip.
          const displayText = presentation?.name || optionText;
          valueNode.textContent = displayText;
          valueNode.title = fullValue;
          if (isModel) {
            valueNode.dataset.mobileLabel = compactComposerModelLabel(option.value || option.textContent);
          }
          const fieldLabel = label?.textContent?.trim();
          const triggerLabel = fieldLabel ? `${fieldLabel}：${fullValue}` : fullValue;
          trigger.setAttribute("aria-label", triggerLabel);
          trigger.title = triggerLabel;
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

    const permissionMenuHost = () => {
      if (!active) return null;
      if (active.mobile) return mobileBody.querySelector(".mobile-select-sheet-options") || mobileBody;
      return menu;
    };

    const syncOptionMarks = (buttons, isSelected) => {
      buttons.forEach((button) => {
        const selected = isSelected(button);
        button.setAttribute("aria-selected", selected ? "true" : "false");
        const check = button.querySelector(".composer-select-option-check");
        if (check) check.textContent = selected ? "✓" : "";
      });
    };

    const syncPermissionOptionMarks = (binding) => {
      const host = permissionMenuHost();
      if (!host) return;
      syncOptionMarks(
        [...host.querySelectorAll(".composer-permission-option")].filter((button) => button.dataset.permissionOption),
        (button) => button.dataset.permissionOption === binding.select.value,
      );
    };

    const syncMessageModeMarks = () => {
      const host = permissionMenuHost();
      if (!host) return;
      const current = resolveMessageMode();
      syncOptionMarks(
        [...host.querySelectorAll(".composer-permission-option")].filter((button) => button.dataset.messageModeOption),
        (button) => button.dataset.messageModeOption === current,
      );
    };

    // Model and reasoning menus still close after a pick. The permission menu
    // holds several independent controls, so choosing a mode must not dismiss
    // danger reflection, message mode, or plan reflection.
    const choose = (binding, option, { keepOpen = false } = {}) => {
      const changed = binding.select.value !== option.value;
      if (changed) {
        binding.select.value = option.value;
        const EventConstructor = binding.select.ownerDocument?.defaultView?.Event || globalThis.Event;
        binding.select.dispatchEvent(new EventConstructor("change", { bubbles: true }));
      }
      if (keepOpen) {
        if (binding.select.id === "permissionMode") syncPermissionOptionMarks(binding);
        return;
      }
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
      if (permission) button.dataset.permissionOption = option.value;
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
        copy.appendChild(label);
        button.appendChild(copy);
      } else {
        button.appendChild(label);
      }

      const check = document.createElement("span");
      check.className = "composer-select-option-check";
      check.setAttribute("aria-hidden", "true");
      check.textContent = selected ? "✓" : "";
      button.appendChild(check);
      button.addEventListener("click", () => choose(binding, option, { keepOpen: permission }));
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

    const appendModelOptionGroups = (binding, target, { mobile = false } = {}) => {
      const options = [...binding.select.options].filter((option) => !option.hidden);
      groupModelSelectOptions(options).forEach((group, index) => {
        const heading = document.createElement("div");
        heading.className = [
          "composer-model-group-heading",
          mobile ? "mobile-model-group-heading" : "",
          index > 0 ? "composer-model-group-start" : "",
        ].filter(Boolean).join(" ");
        heading.setAttribute("role", "presentation");
        heading.textContent = group.provider || translate("chat.modelProviderFallback");
        target.appendChild(heading);
        group.options.forEach((option) => target.appendChild(mobile
          ? createMobileOptionButton(binding, option, { model: true })
          : createOptionButton(binding, option, { model: true })));
      });
    };

    // The old "permission guard / enabled" line was a static note that restated
    // something always true and could not be acted on, so it is gone; the
    // divider now introduces the one control in this section that does anything.
    const appendPermissionSafetyStatus = (target = menu) => {
      const divider = document.createElement("div");
      divider.className = "composer-permission-divider";
      divider.setAttribute("aria-hidden", "true");
      target.append(divider, createDangerReflectionRow());
    };

    const planReflectionEnabled = () => getPlanReflection?.() !== false;
    const planReflectionNodes = new Set();

    const syncPlanReflectionNodes = () => {
      const enabled = planReflectionEnabled();
      const inPlanMode = resolveMessageMode() === "plan";
      const agentId = String(getAgentId?.() || "").trim();
      for (const selector of [...planReflectionNodes]) {
        if (selector.isConnected === false) {
          planReflectionNodes.delete(selector);
          continue;
        }
        selector.querySelectorAll(".composer-permission-plan-reflection-level").forEach((button) => {
          const selected = (button.dataset.planReflection === "true") === enabled;
          button.classList.toggle("is-selected", selected);
          button.setAttribute("aria-checked", selected ? "true" : "false");
          button.disabled = !inPlanMode || !agentId;
        });
      }
    };

    const setPlanReflectionEnabled = async (selector, enabled) => {
      const agentId = String(getAgentId?.() || "").trim();
      if (!agentId || resolveMessageMode() !== "plan") return;
      if (selector.getAttribute("aria-busy") === "true") return;
      const previous = planReflectionEnabled();
      if (previous === enabled) return;
      setPlanReflection?.(enabled);
      syncPlanReflectionNodes();
      selector.setAttribute("aria-busy", "true");
      selector.querySelectorAll(".composer-permission-plan-reflection-level").forEach((button) => { button.disabled = true; });
      try {
        if (typeof requestAPI !== "function") throw new Error("Plan reflection is unavailable");
        const response = await requestAPI(`/api/agents/${encodeURIComponent(agentId)}/plan-reflection`, {
          method: "PATCH",
          body: JSON.stringify({ planReflection: enabled }),
        });
        const saved = response && typeof response === "object" && "planReflection" in response
          ? response.planReflection !== false
          : enabled;
        setPlanReflection?.(saved);
      } catch (error) {
        setPlanReflection?.(previous);
        showError?.(error);
      } finally {
        selector.removeAttribute("aria-busy");
        syncPlanReflectionNodes();
      }
    };

    const createPlanReflectionRow = () => {
      const row = document.createElement("div");
      row.className = "composer-permission-safety-status composer-permission-danger-reflection composer-permission-plan-reflection";
      row.title = translate("chat.planReflectionDescription");

      const heading = document.createElement("div");
      heading.className = "composer-permission-danger-reflection-heading";
      const icon = document.createElement("span");
      icon.className = "composer-permission-option-icon composer-permission-safety-icon";
      icon.innerHTML = permissionMenuIconMarkup.plan;
      const label = document.createElement("span");
      label.className = "composer-permission-safety-label";
      label.textContent = translate("chat.planReflection");
      heading.append(icon, label);

      const selector = document.createElement("div");
      selector.className = "composer-permission-danger-reflection-levels composer-permission-plan-reflection-levels";
      selector.setAttribute("role", "radiogroup");
      selector.setAttribute("aria-label", `${translate("chat.planReflection")} — ${translate("chat.planReflectionDescription")}`);
      const enabled = planReflectionEnabled();
      const inPlanMode = resolveMessageMode() === "plan";
      const agentId = String(getAgentId?.() || "").trim();
      [["true", "chat.planReflectionOn"], ["false", "chat.planReflectionOff"]].forEach(([value, key]) => {
        const button = document.createElement("button");
        button.type = "button";
        button.className = "composer-permission-plan-reflection-level";
        button.dataset.planReflection = value;
        button.setAttribute("role", "radio");
        const selected = (value === "true") === enabled;
        button.classList.toggle("is-selected", selected);
        button.setAttribute("aria-checked", selected ? "true" : "false");
        button.disabled = !inPlanMode || !agentId;
        button.textContent = translate(key);
        button.addEventListener("click", () => setPlanReflectionEnabled(selector, value === "true"));
        selector.appendChild(button);
      });
      planReflectionNodes.add(selector);
      row.append(heading, selector);
      return row;
    };

    const syncPlanReflectionRow = () => {
      const host = permissionMenuHost();
      if (!host) return;
      const existing = host.querySelector(".composer-permission-plan-reflection");
      if (resolveMessageMode() === "plan") {
        if (!existing) host.appendChild(createPlanReflectionRow());
        else syncPlanReflectionNodes();
        return;
      }
      existing?.remove();
    };

    const chooseMessageMode = (mode) => {
      const next = mode === "plan" ? "plan" : "execute";
      if (resolveMessageMode() === next) return;
      setMessageMode?.(next);
      syncMessageModeMarks();
      syncPlanReflectionRow();
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
      if (resolveMessageMode() === "plan") target.appendChild(createPlanReflectionRow());
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
      const viewportHeight = globalThis.innerHeight || document.documentElement.clientHeight;
      const apply = (measuredWidth = 0) => {
        const layout = composerSelectMenuLayout({
          triggerRect: rect,
          viewportWidth,
          viewportHeight,
          selectId: trigger.dataset.composerSelect,
          measuredWidth,
        });
        menu.style.left = `${layout.left}px`;
        menu.style.width = `${layout.width}px`;
        menu.style.bottom = `${layout.bottom}px`;
      };
      apply();
      // CSS min-width can still widen the used box past the width we wrote.
      const actualWidth = menu.getBoundingClientRect?.().width;
      if (actualWidth > 0) apply(actualWidth);
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


    // Summary model picker. It is global runtime configuration rather than a
    // per-conversation choice, but it is surfaced here so compaction can be
    // retargeted without opening Settings.
    const summaryModelOptions = (binding) => [...binding.select.options]
      .filter((option) => !option.hidden && String(option.value || "").trim());

    // The same model name can be served by several providers, so the summary
    // picker mirrors the main model menu: provider headings above their models,
    // and the provider spelled out next to the current selection.
    const summaryModelDescriptor = (binding, value) => {
      const target = String(value || "").trim();
      if (!target) return null;
      const option = summaryModelOptions(binding).find((candidate) => String(candidate.value) === target) || null;
      const presentation = modelOptionPresentation(target, option?.textContent);
      const provider = String(option?.dataset?.provider || option?.parentElement?.label || presentation.provider || "").trim();
      return { name: presentation.name || target, provider };
    };

    const applySummaryModel = async (value) => {
      if (typeof setSummaryModel !== "function") return;
      try {
        await setSummaryModel(value);
      } catch (error) {
        showError?.(error);
      }
    };

    const openSummaryModelPicker = (binding) => {
      const current = String(getSummaryModel?.() || "");
      menu.replaceChildren();
      const heading = document.createElement("div");
      heading.className = "composer-select-popover-title";
      heading.textContent = translate("chat.summaryModel");
      menu.appendChild(heading);
      groupModelSelectOptions(summaryModelOptions(binding)).forEach((group, index) => {
        const groupHeading = document.createElement("div");
        groupHeading.className = [
          "composer-model-group-heading",
          index > 0 ? "composer-model-group-start" : "",
        ].filter(Boolean).join(" ");
        groupHeading.setAttribute("role", "presentation");
        groupHeading.textContent = group.provider || translate("chat.modelProviderFallback");
        menu.appendChild(groupHeading);
        group.options.forEach((option) => {
          const value = String(option.value);
          const button = document.createElement("button");
          button.type = "button";
          button.className = "composer-select-option composer-model-option";
          button.setAttribute("role", "option");
          button.setAttribute("aria-selected", value === current ? "true" : "false");
          const copy = document.createElement("span");
          copy.className = "composer-model-option-copy";
          const name = document.createElement("span");
          name.className = "composer-model-option-name";
          name.textContent = modelOptionPresentation(value, option.textContent).name;
          copy.appendChild(name);
          button.appendChild(copy);
          button.addEventListener("click", () => {
            close({ focus: true });
            void applySummaryModel(value);
          });
          menu.appendChild(button);
        });
      });
      positionMenu(binding.trigger);
      menu.querySelector('[aria-selected="true"]')?.focus();
    };

    const appendSummaryModelRow = (binding) => {
      if (typeof setSummaryModel !== "function") return;
      const divider = document.createElement("div");
      divider.className = "composer-model-menu-divider";
      divider.setAttribute("aria-hidden", "true");
      menu.appendChild(divider);
      const button = document.createElement("button");
      button.type = "button";
      button.className = "composer-model-menu-action";
      const copy = document.createElement("span");
      copy.className = "composer-model-menu-action-copy";
      const title = document.createElement("span");
      title.className = "composer-model-menu-action-title";
      title.textContent = translate("chat.summaryModel");
      copy.appendChild(title);
      const current = String(getSummaryModel?.() || "");
      if (current) {
        const descriptor = summaryModelDescriptor(binding, current);
        const detail = document.createElement("span");
        detail.className = "composer-model-menu-action-detail";
        detail.textContent = descriptor?.provider
          ? `${descriptor.provider} · ${descriptor.name}`
          : (descriptor?.name || current);
        copy.appendChild(detail);
      }
      const chevron = document.createElement("span");
      chevron.className = "composer-model-menu-action-chevron";
      chevron.setAttribute("aria-hidden", "true");
      chevron.textContent = "›";
      button.append(copy, chevron);
      button.addEventListener("click", () => openSummaryModelPicker(binding));
      menu.appendChild(button);
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
        if (isModel) appendModelOptionGroups(binding, options, { mobile: true });
        else [...binding.select.options]
          .filter((option) => !option.hidden)
          .forEach((option) => options.appendChild(createMobileOptionButton(binding, option)));
        mobileBody.replaceChildren(options);
      }

      // The model sheet used to append 思考强度 / 压缩上下文 / 管理模型 shortcuts here.
      // They are reachable from the composer's own controls (effort trigger, context
      // ring, model manager), so the sheet now shows only the model list.

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
        if (isModelMenu) appendModelOptionGroups(binding, menu);
        else [...binding.select.options]
          .filter((option) => !option.hidden)
          .forEach((option) => menu.appendChild(createOptionButton(binding, option)));
      }
      if (isModelMenu) appendSummaryModelRow(binding);
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

    const handleDocumentPointer = (event) => {
      if (!active || active.mobile) return;
      if (menu.contains(event.target)) return;
      if (active.binding?.trigger?.contains?.(event.target)) return;
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

  return { bindComposerSelectMenus };
}
