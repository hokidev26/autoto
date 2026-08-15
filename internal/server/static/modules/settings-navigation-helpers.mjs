import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { appMainT as am } from "./messages-app-main-extra.mjs";
import {
  firstSettingsItemForCategory,
  groupSettingsItemsByLegacyCategory,
  legacySettingsCategories,
  settingsCategoryByKey,
} from "./settings-categories.mjs";
import { settingsItems } from "./settings-data.mjs";
import { elementVisible } from "./ui-shell.mjs";
import { setThemePageContext } from "./theme-manager.mjs";

// Settings search/category-nav filtering, keyboard arrow navigation for
// settings nav lists, the active-panel refresh entry point, clipboard copy
// helpers, and the theme "home empty" page-context sync. renderSettingsNav
// and selectSettingsPanel themselves stay in app-main.mjs (source-pinned)
// but call back into these helpers.
export function createSettingsNavigationHelpers({
  state,
  showToast,
  notifyTerminal,
  isMobileSettingsViewport,
  renderMobileSettingsIndex,
  renderSettingsNav,
  selectSettingsPanel,
  visibleSettingsItems = () => settingsItems,
}) {
  function normalizedSettingsSearchQuery() {
    return String(state.settingsSearchQuery || "").trim().toLowerCase();
  }

  function settingsSearchText(category, item) {
    return [category.label, category.key, item.key, item.label, item.subtitle]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
  }

  function filteredSettingsSections() {
    const query = normalizedSettingsSearchQuery();
    return groupSettingsItemsByLegacyCategory(visibleSettingsItems(), (item, category) => !query || settingsSearchText(category, item).includes(query));
  }

  function firstFilteredSettingsItem(sections = filteredSettingsSections()) {
    return sections[0]?.items?.[0] || null;
  }

  function filteredSettingsIncludesKey(key, sections = filteredSettingsSections()) {
    return sections.some((section) => section.items.some((item) => item.key === key));
  }

  // Which panel a search should land on. Staying put whenever the current panel
  // still matches keeps typing from yanking the reader around; when it stops
  // matching we move to the first remaining item. If nothing matches at all we
  // keep the current panel rather than blanking the pane on a typo.
  function nextFilteredSettingsKey(activeKey, sections = filteredSettingsSections()) {
    if (filteredSettingsIncludesKey(activeKey, sections)) return activeKey;
    return firstFilteredSettingsItem(sections)?.key || activeKey;
  }

  function syncSettingsSearchInput() {
    const input = $("settingsSearchInput");
    if (input && input.value !== state.settingsSearchQuery) input.value = state.settingsSearchQuery;
    $("clearSettingsSearchBtn")?.classList.toggle("visible", Boolean(normalizedSettingsSearchQuery()));
  }

  function bindSettingsArrowNavigation(nav, selector, keys) {
    nav.querySelectorAll(selector).forEach((node) => {
      node.addEventListener("keydown", (event) => {
        const direction = keys[event.key];
        if (direction == null) return;
        const nodes = [...nav.querySelectorAll(selector)];
        const index = nodes.indexOf(event.currentTarget);
        if (index < 0 || !nodes.length) return;
        const nextIndex = direction === "first" ? 0 : direction === "last" ? nodes.length - 1 : (index + direction + nodes.length) % nodes.length;
        nodes[nextIndex]?.focus();
        event.preventDefault();
      });
    });
  }

  function selectSettingsCategory(categoryKey) {
    const category = settingsCategoryByKey(categoryKey);
    state.activeSettingsCategory = category.key;
    state.settingsSearchQuery = "";
    syncSettingsSearchInput();
    const current = state.activeSettingsPanel || "";
    const nextKey = category.items.includes(current) ? current : firstSettingsItemForCategory(category.key);
    selectSettingsPanel(nextKey);
  }

  function renderSettingsCategoryNav(activeCategory = "api") {
    const nav = $("settingsCategoryNav");
    if (!nav) return;
    nav.innerHTML = legacySettingsCategories.map((category) => `
      <button class="legacy-settings-category ${category.key === activeCategory ? "active" : ""}" type="button" aria-pressed="${category.key === activeCategory ? "true" : "false"}" data-settings-category="${escapeAttr(category.key)}">${escapeHtml(category.label)}</button>
    `).join("");
    nav.querySelectorAll("[data-settings-category]").forEach((node) => {
      node.addEventListener("click", () => selectSettingsCategory(node.dataset.settingsCategory));
    });
    bindSettingsArrowNavigation(nav, "[data-settings-category]", { ArrowLeft: -1, ArrowRight: 1, Home: "first", End: "last" });
  }

  function clearSettingsSearchQuery({ focus = false } = {}) {
    state.settingsSearchQuery = "";
    renderSettingsNav(state.activeSettingsPanel || "profile");
    if (focus) focusSettingsSearchInput();
  }

  function focusSettingsSearchInput({ select = false } = {}) {
    // The search field is display:none below 768px, the same breakpoint this
    // flag tracks, so there is nothing to focus on a phone in either view.
    if (state.settingsMobileViewport) return;
    const input = $("settingsSearchInput");
    if (!input) return;
    input.focus();
    if (select) input.select();
  }

  function settingsPageScroller() {
    return $("settingsContentBody")?.closest?.(".settings-page-scroll") || null;
  }

  function captureSettingsPageScroll() {
    return Number(settingsPageScroller()?.scrollTop) || 0;
  }

  function restoreSettingsPageScroll(top) {
    const scroller = settingsPageScroller();
    if (scroller) scroller.scrollTop = Number(top) || 0;
  }

  function refreshActiveSettingsPanel() {
    const modal = $("settingsModal");
    if (!modal || modal.classList.contains("hidden")) return;
    if (state.settingsMobileViewport && state.mobileSettingsView === "index" && isMobileSettingsViewport()) {
      renderMobileSettingsIndex();
      return;
    }
    const scrollTop = captureSettingsPageScroll();
    selectSettingsPanel(state.activeSettingsPanel || "profile");
    restoreSettingsPageScroll(scrollTop);
  }

  async function copyToClipboard(text) {
    const value = String(text || "");
    if (!value) return false;
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value);
        return true;
      }
    } catch {}
    try {
      const textarea = document.createElement("textarea");
      textarea.value = value;
      textarea.setAttribute("readonly", "");
      textarea.style.position = "fixed";
      textarea.style.left = "-9999px";
      textarea.style.top = "0";
      document.body.appendChild(textarea);
      textarea.select();
      const ok = document.execCommand("copy");
      textarea.remove();
      return ok;
    } catch {
      return false;
    }
  }

  async function copyText(text) {
    if (!text) return;
    if (await copyToClipboard(text)) {
      showToast(am("copiedToClipboard"), "success");
      notifyTerminal(`[info] ${am("copiedToClipboard")}\n`);
      return;
    }
    showToast(am("copyFailed"), "warn");
    notifyTerminal(`[warn] ${am("copyFailed")}\n`);
  }

  function syncThemePageContext() {
    const settingsOpen = elementVisible("settingsModal");
    const homeEmpty = !settingsOpen
      && !state.overviewActive
      && state.activeWorkbench === "conversation"
      && !state.agent;
    setThemePageContext(homeEmpty ? "home-empty" : "");
  }

  return {
    normalizedSettingsSearchQuery,
    settingsSearchText,
    filteredSettingsSections,
    nextFilteredSettingsKey,
    firstFilteredSettingsItem,
    filteredSettingsIncludesKey,
    syncSettingsSearchInput,
    bindSettingsArrowNavigation,
    renderSettingsCategoryNav,
    selectSettingsCategory,
    clearSettingsSearchQuery,
    focusSettingsSearchInput,
    captureSettingsPageScroll,
    restoreSettingsPageScroll,
    refreshActiveSettingsPanel,
    copyToClipboard,
    copyText,
    syncThemePageContext,
  };
}
