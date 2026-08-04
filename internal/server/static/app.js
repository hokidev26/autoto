(() => {
  const normalizeBootLocale = (value) => {
    const locale = String(value || "").trim().toLowerCase();
    if (locale === "zh-tw" || locale === "zh-hant" || locale.startsWith("zh-hant-") || locale === "zh-hk" || locale.startsWith("zh-hk-") || locale === "zh-mo" || locale.startsWith("zh-mo-")) return "zh-TW";
    if (locale === "zh" || locale === "zh-cn" || locale === "zh-hans" || locale.startsWith("zh-hans-") || locale === "zh-sg" || locale.startsWith("zh-sg-")) return "zh-CN";
    if (locale === "en" || locale.startsWith("en-")) return "en";
    return "";
  };

  const bootLocale = () => {
    try {
      for (const key of ["autoto.regional", "codeharbor.regional"]) {
        const saved = JSON.parse(globalThis.localStorage?.getItem?.(key) || "null");
        const preference = String(saved?.locale ?? saved?.language ?? saved?.lang ?? "").trim();
        if (preference && preference.toLowerCase() !== "auto") {
          const resolved = normalizeBootLocale(preference);
          if (resolved) return resolved;
        }
      }
    } catch {}
    const candidates = [
      ...(Array.isArray(globalThis.navigator?.languages) ? globalThis.navigator.languages : []),
      globalThis.navigator?.language,
    ].filter(Boolean);
    try {
      candidates.push(new Intl.DateTimeFormat().resolvedOptions().locale);
    } catch {}
    for (const candidate of candidates) {
      const resolved = normalizeBootLocale(candidate);
      if (resolved) return resolved;
    }
    return "en";
  };

  const applyBootLocale = () => {
    const locale = bootLocale();
    const root = globalThis.document?.documentElement;
    if (root) {
      root.lang = locale === "zh-TW" ? "zh-Hant-TW" : locale === "zh-CN" ? "zh-Hans-CN" : "en";
      root.dataset.uiLocale = locale;
    }
    const title = {
      "zh-CN": "正在加载项目",
      "zh-TW": "正在載入專案",
      en: "Loading project",
    }[locale];
    const labels = globalThis.document?.querySelectorAll?.('[data-i18n="workspace.main.loadingProjectTitle"]') || [];
    labels.forEach((label) => { label.textContent = title; });
    return locale;
  };

  const activeBootLocale = applyBootLocale();
  const appReadyEventName = "autoto:app-ready";

  const waitForAppReady = ({ timeout = 12000 } = {}) => {
    if (globalThis.document?.documentElement?.dataset?.autotoAppReady === "true") return Promise.resolve();
    if (typeof globalThis.addEventListener !== "function") return Promise.resolve();
    return new Promise((resolve) => {
      let settled = false;
      let timer = 0;
      const finish = () => {
        if (settled) return;
        settled = true;
        globalThis.removeEventListener?.(appReadyEventName, finish);
        if (timer) globalThis.clearTimeout?.(timer);
        resolve();
      };
      globalThis.addEventListener(appReadyEventName, finish, { once: true });
      timer = globalThis.setTimeout?.(finish, timeout) || 0;
    });
  };

  const showBootError = (error) => {
    console.error("Failed to load Autoto frontend", error);
    const message = error && error.message ? error.message : String(error || "unknown error");
    const target = document.getElementById("messages") || document.body;
    if (target) {
      const card = document.createElement("div");
      card.className = "empty-workspace-card";
      const title = document.createElement("div");
      title.className = "empty-workspace-title";
      title.textContent = {
        "zh-CN": "前端加载失败",
        "zh-TW": "前端載入失敗",
        en: "Frontend failed to load",
      }[activeBootLocale];
      const text = document.createElement("div");
      text.className = "empty-workspace-text";
      text.textContent = message;
      card.append(title, text);
      target.prepend(card);
    }
  };

  const revealLocalizedUI = () => {
    globalThis.document?.documentElement?.removeAttribute("data-ui-locale-pending");
  };

  const bootstrap = async () => {
    try {
      // Installed before anything else loads so a throw inside a listener or an
      // unawaited promise during startup is recorded rather than lost.
      const { installGlobalErrorReporting } = await import("./modules/error-boundary.mjs?v=error-boundary-1");
      installGlobalErrorReporting();
      const { setUILocale } = await import("./modules/i18n.mjs?v=context-ring-3-dual-rail-collapse-1-background-upload-1-goal-command-2-first-run-setup-1-standalone-removed-2");
      setUILocale(activeBootLocale);
      // Opt-in transcript scroll diagnostic. Does nothing unless
      // localStorage["autoto.scrollTrace"] === "1", and is loaded before the
      // app so it can wrap the container before the first render anchors it.
      try {
        const { installScrollTrace } = await import("./modules/scroll-trace.mjs?v=scroll-trace-1");
        installScrollTrace();
      } catch {}
      const appReady = waitForAppReady();
      await import("./modules/app-main.mjs?v=legacy-ui-1-settings-dock-1-rail-1-about-1-i18n-static-2-conversation-fig2-1-scroll-skills-1-market-layout-1-native-codex-3-provider-console-1-compact-composer-5-custom-select-1-right-toolbar-1-sidebar-resize-2-permission-panel-2-plan-mode-panel-1-composer-activity-1-mobile-header-composer-1-fast-mode-1-throughput-1-usage-history-1-workbench-2-rail-compact-1-message-editing-1-message-thread-1-mode-boundaries-1-settings-shadcn-1-tool-activity-1-folder-picker-remote-2-root-card-1-provider-model-discovery-1-conversation-switch-1-user-message-fit-1-plan-mode-2-background-tasks-2-remote-access-1-mobile-short-labels-1-task-toolbar-2-provider-account-wide-1-model-compact-1-codex-export-1-settings-flat-1-aggregates-1-user-message-left-1-workbench-header-1-mobile-toolbar-right-3-icon-rail-1-codex-import-open-1-terminal-actions-compact-2-users-panel-removed-1-remote-control-full-4-remote-full-toggle-3-about-brand-license-1-security-banner-hidden-1-workbench-title-edit-1-provider-create-page-2-codex-browser-login-1-shared-api-1-apple-theme-1-autoto-themes-1-project-flat-1-simple-composer-1-settings-help-1-task-workspace-1-provider-secrets-1-model-picker-1-navigation-state-2-agent-admin-removed-1-archive-1-switch-fix-3-hide-run-loading-1-provider-full-page-2-provider-placeholders-1-settings-icons-1-mobile-viewport-1-i18n-shared-1-root-shortcut-removed-1-hidden-toggle-removed-1-project-context-1-mobile-settings-compact-1-boot-ready-transition-1-contextual-create-1-navigation-split-1-sidebar-wheel-1-subagent-cards-1-task-summary-activity-1-formal-theme-assets-1-settings-nav-flat-1-codex-usage-clean-1-shared-api-compact-2-no-alias-safety-1-model-sections-hidden-1-nav-schedules-1-settings-cleanup-1-mobile-no-home-1-mobile-title-1-schedule-workspace-2-browser-boundary-1-message-lifecycle-1-subagent-incremental-1-recent-sync-1-default-openai-responses-1-provider-draft-session-1-profile-message-identity-1-profile-avatar-1-context-ring-3-provider-errors-1-tunnel-busy-1-compact-run-error-1-global-background-1-theme-v2-1-compact-navigation-1-global-rail-2-settings-identity-removed-1-model-save-scroll-1-background-upload-1-goal-command-2-tool-activity-lazy-1-drawer-conversation-1-provider-auto-name-1-model-provider-groups-1-first-run-setup-1-tool-protocol-filter-1-provider-hidden-models-1-conversation-order-1-home-launcher-1-home-launcher-bottom-1-home-launcher-minimal-1-provider-quota-overview-1-optional-tools-compact-1-skills-density-1-project-run-history-1-git-init-repair-1-gateway-tunnel-1-image-gen-scroll-1-shared-api-key-delete-1-gateway-models-1-provider-fold-1-tunnel-ui-1-tunnel-state-fix-1-startup-navigation-guard-4-top-project-1-standalone-removed-2-background-task-settings-1");
      await appReady;
      // Reveal only after data and persisted layout values are ready behind the loading layer.
      revealLocalizedUI();
    } catch (error) {
      revealLocalizedUI();
      showBootError(error);
    }
  };

  bootstrap();
})();
