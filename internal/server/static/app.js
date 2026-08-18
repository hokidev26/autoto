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
      for (const key of ["autoto.regional"]) {
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
      // unawaited promise during startup is recorded rather than lost. Locale
      // and the error boundary are independent, so they start together instead
      // of a two-hop waterfall before app-main.
      const [{ installGlobalErrorReporting }, { setUILocale }] = await Promise.all([
        import("./modules/error-boundary.mjs"),
        import("./modules/i18n.mjs"),
      ]);
      installGlobalErrorReporting();
      setUILocale(activeBootLocale);
      // Opt-in transcript scroll diagnostic. Skip the extra module fetch unless
      // localStorage["autoto.scrollTrace"] === "1". When it is on, load it
      // before the app so it can wrap the container before the first render.
      try {
        if (globalThis.localStorage?.getItem?.("autoto.scrollTrace") === "1") {
          const { installScrollTrace } = await import("./modules/scroll-trace.mjs");
          installScrollTrace();
        }
      } catch {}
      const appReady = waitForAppReady();
      await import("./modules/app-main.mjs");
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
