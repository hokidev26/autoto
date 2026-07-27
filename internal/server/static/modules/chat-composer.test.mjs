import test from "node:test";
import assert from "node:assert/strict";

globalThis.window = { AUTOTO_LOCAL_TOKEN: "", CODEHARBOR_LOCAL_TOKEN: "" };
globalThis.location = { origin: "http://localhost", protocol: "http:", host: "localhost" };

const {
  builtInSlashCommandsForContext,
  calculateMessageInputSize,
  clipboardFiles,
  createChatComposerController,
  fastModeSupportedForModel,
  interfaceLocale,
  maxChatDraftCharacters,
  mentionTrigger,
  normalizeChatDrafts,
  normalizeMessageMode,
  normalizeReasoningEffort,
  parseGoalCommandDraft,
  maxQueuedMessages,
  normalizeMessageQueue,
  parseQueueCommandDraft,
  reasoningEffortValuesForCapabilities,
  reasoningEffortValuesForModel,
  resizeMessageInputElement,
  slashCommandsForContext,
  slashCommandsForEffectivePolicy,
  truncateChatDraft,
  unicodeCharacters,
} = await import("./chat-composer.mjs");

test("chat drafts truncate on Unicode code point boundaries", () => {
  const value = `${"a".repeat(maxChatDraftCharacters - 1)}😀尾`;
  const result = truncateChatDraft(value);
  assert.equal(result.length, maxChatDraftCharacters + 1);
  assert.equal(unicodeCharacters(result.text).length, maxChatDraftCharacters);
  assert.equal(result.text.endsWith("😀"), true);
  assert.equal(result.truncated, true);
  assert.equal(normalizeChatDrafts({ agent: value }).agent, result.text);
});

test("clipboardFiles keeps file items without consuming text", () => {
  const file = { name: "screen.png", size: 12 };
  const event = {
    clipboardData: {
      files: [],
      items: [
        { kind: "string" },
        { kind: "file", getAsFile: () => file },
      ],
    },
  };
  assert.deepEqual(clipboardFiles(event), [file]);
});

test("interfaceLocale prefers the page language", () => {
  assert.equal(interfaceLocale({ documentElement: { lang: "en-US" } }, { language: "fr-FR" }), "en-US");
  assert.equal(interfaceLocale({ documentElement: { lang: "" } }, { language: "fr-FR" }), "fr-FR");
});

test("mentionTrigger supports Chinese and Unicode handles", () => {
  assert.deepEqual(mentionTrigger("请看 @张三", 6), { query: "张三", start: 3, end: 6 });
  assert.equal(mentionTrigger("mail@example.com", 16), null);
});

test("chat composer hides local templates until effective policy is authoritative", () => {
  const local = [{ id: "local", name: "/local", prompt: "local prompt", enabled: true }];
  assert.deepEqual(slashCommandsForEffectivePolicy({ hasAuthoritativeData: false, items: [] }, local), []);
  assert.deepEqual(slashCommandsForEffectivePolicy({ hasAuthoritativeData: true, items: [] }, local), [
    { id: "local-local", name: "/local", description: "", prompt: "local prompt", source: "local" },
  ]);
});

test("chat composer honors unusable effective owners as command shadows", () => {
  const commands = slashCommandsForEffectivePolicy({
    hasAuthoritativeData: true,
    items: [
      { id: "workspace-disabled", command: "/disabled", scope: "workspace", enabled: false, scanVerdict: "safe" },
      { id: "project-blocked", command: "/blocked", scope: "project", enabled: true, scanVerdict: "blocked" },
      { id: "workspace-review", command: "/review", scope: "workspace", enabled: true, scanVerdict: "review" },
      { id: "global-safe", command: "/safe", scope: "global", enabled: true, scanVerdict: "safe" },
    ],
  }, [
    { id: "local-disabled", name: "/disabled", prompt: "bypass disabled", enabled: true },
    { id: "local-blocked", name: "/blocked", prompt: "bypass blocked", enabled: true },
    { id: "local-review", name: "/review", prompt: "bypass review", enabled: true },
  ]);
  assert.deepEqual(commands, [
    { id: "server-global-safe", name: "/safe", description: "", prompt: "", source: "server" },
  ]);
});

test("project context exposes the trusted goal command and reserves it from skills", () => {
  const translate = () => "described";
  const goal = { id: "builtin-goal", name: "/goal", description: "described", prompt: "", source: "builtin" };
  // /queue is context-free: parking a follow-up is useful in a plain
  // conversation too, so unlike /goal it survives outside a project.
  const queue = { id: "builtin-queue", name: "/queue", description: "described", prompt: "", source: "builtin" };
  assert.deepEqual(builtInSlashCommandsForContext("conversation", translate), [queue]);
  assert.deepEqual(builtInSlashCommandsForContext("project", translate), [goal, queue]);
  assert.deepEqual(slashCommandsForContext("project", [
    { id: "server-goal", name: "/goal", description: "shadow", prompt: "", source: "server" },
    { id: "server-queue", name: "/queue", description: "shadow", prompt: "", source: "server" },
    { id: "server-review", name: "/review", description: "review", prompt: "", source: "server" },
  ], translate), [
    goal,
    queue,
    { id: "server-review", name: "/review", description: "review", prompt: "", source: "server" },
  ]);
  assert.deepEqual(slashCommandsForContext("conversation", [
    { id: "server-goal", name: "/goal", description: "shadow", prompt: "", source: "server" },
    { id: "server-review", name: "/review", description: "review", prompt: "", source: "server" },
  ], translate), [
    queue,
    { id: "server-review", name: "/review", description: "review", prompt: "", source: "server" },
  ]);
});

test("queue command parsing matches the goal command grammar", () => {
  assert.equal(parseQueueCommandDraft("run the tests"), null);
  assert.equal(parseQueueCommandDraft("/queued up"), null);
  assert.equal(parseQueueCommandDraft("/Queue ship it"), null);
  assert.deepEqual(parseQueueCommandDraft(" /queue "), { commandText: "/queue", queuedText: "" });
  assert.deepEqual(parseQueueCommandDraft("/queue run the tests"), { commandText: "/queue run the tests", queuedText: "run the tests" });
});

test("goal command parsing matches the backend command grammar", () => {
  assert.equal(parseGoalCommandDraft("review this"), null);
  assert.equal(parseGoalCommandDraft("/goalkeeper"), null);
  assert.equal(parseGoalCommandDraft("/Goal ship it"), null);
  assert.deepEqual(parseGoalCommandDraft(" /goal "), { commandText: "/goal", goalText: "" });
  assert.deepEqual(parseGoalCommandDraft("/goal   Ship the protected task  "), {
    commandText: "/goal   Ship the protected task",
    goalText: "Ship the protected task",
  });
});

test("slash palette opens on / and includes the built-in goal only in project context", () => {
  const previousDocument = globalThis.document;
  const attributes = {};
  const input = {
    value: "/",
    setAttribute(name, value) { attributes[name] = value; },
    removeAttribute(name) { delete attributes[name]; },
  };
  const hiddenClasses = new Set(["hidden"]);
  const palette = {
    innerHTML: "",
    classList: {
      add(name) { hiddenClasses.add(name); },
      remove(name) { hiddenClasses.delete(name); },
    },
    querySelectorAll() { return []; },
  };
  globalThis.document = { getElementById(id) { return { messageText: input, slashCommandPalette: palette }[id] || null; } };
  try {
    const state = {
      agent: { id: "agent-goal" },
      navigationSelectionKind: "project",
      promptHistory: [],
      serverSkills: [{ id: "review", command: "/review", description: "Review changes", enabled: true, scanVerdict: "safe" }],
    };
    const controller = createChatComposerController({
      state,
      currentSkillsPreferences: () => ({ commands: [
        { id: "local-goal", name: "/goal", prompt: "shadow", enabled: true },
        { id: "local-tests", name: "/write-tests", prompt: "Write tests", enabled: true },
      ] }),
    });

    controller.updateSlashCommandPalette();
    assert.equal(state.slashCommandOpen, true);
    assert.equal(hiddenClasses.has("hidden"), false);
    assert.match(palette.innerHTML, /data-slash-command="builtin-goal"/);
    assert.match(palette.innerHTML, />\/goal</);
    assert.match(palette.innerHTML, />\/review</);
    assert.match(palette.innerHTML, />\/write-tests</);
    assert.equal(attributes["aria-expanded"], "true");

    state.navigationSelectionKind = "conversation";
    input.value = "/";
    controller.updateSlashCommandPalette();
    assert.equal(state.slashCommandOpen, true);
    assert.doesNotMatch(palette.innerHTML, />\/goal</);
    assert.match(palette.innerHTML, />\/review</);
    assert.match(palette.innerHTML, />\/write-tests</);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("reasoning effort normalizes legacy and unknown values against backend capabilities", () => {
  assert.equal(normalizeReasoningEffort(""), "auto");
  assert.equal(normalizeReasoningEffort("inherit"), "auto");
  assert.equal(normalizeReasoningEffort("MEDIUM"), "medium");
  assert.equal(normalizeReasoningEffort("extreme"), "auto");
  assert.deepEqual(reasoningEffortValuesForCapabilities({ reasoningEffort: false }), ["auto"]);
  assert.deepEqual(reasoningEffortValuesForCapabilities({ reasoningEffort: true }), ["auto", "low", "medium", "high"]);
  assert.deepEqual(reasoningEffortValuesForCapabilities({ reasoningEfforts: ["low", "xhigh", "unknown"] }), ["auto", "low", "xhigh"]);
  assert.deepEqual(reasoningEffortValuesForCapabilities({ reasoningEffortValues: ["medium", "unknown"] }), ["auto", "medium"]);
  assert.deepEqual(reasoningEffortValuesForCapabilities({ reasoningEffort: { supportedValues: ["high", "xhigh"] } }), ["auto", "high", "xhigh"]);
  assert.deepEqual(reasoningEffortValuesForCapabilities({ reasoningEffort: ["low", "xhigh"] }), ["auto", "low", "xhigh"]);
  assert.equal(normalizeReasoningEffort("xhigh", ["auto", "low", "xhigh"]), "xhigh");
  assert.equal(normalizeReasoningEffort("xhigh", ["auto", "low", "high"]), "auto");
});

test("codex gpt-5.5 exposes the same five reasoning efforts in every navigation context", () => {
  const provider = { name: "codex", capabilities: { reasoningEffort: true } };
  assert.deepEqual(reasoningEffortValuesForModel(provider, "codex:gpt-5.5"), ["auto", "low", "medium", "high", "xhigh"]);
  for (const navigationSelectionKind of ["conversation", "project"]) {
    assert.deepEqual(reasoningEffortValuesForModel({ ...provider, navigationSelectionKind }, "codex:gpt-5.5"), ["auto", "low", "medium", "high", "xhigh"]);
  }
});

test("Fast mode support comes from the selected model capability only", () => {
  const provider = {
    modelCapabilities: {
      "gpt-fast": { fastMode: true },
      "gpt-basic": { fastMode: false },
    },
  };
  assert.equal(fastModeSupportedForModel(provider, "codex:gpt-fast"), true);
  assert.equal(fastModeSupportedForModel(provider, "codex:gpt-basic"), false);
  assert.equal(fastModeSupportedForModel(provider, "codex:unknown"), false);
  assert.equal(fastModeSupportedForModel(null, "codex:gpt-fast"), false);
});

test("message textarea autosize clamps to bounds and toggles internal scrolling", () => {
  assert.deepEqual(calculateMessageInputSize({ scrollHeight: 20, minHeight: 44, maxHeight: 128 }), { height: 44, scrollable: false });
  assert.deepEqual(calculateMessageInputSize({ scrollHeight: 96, minHeight: 44, maxHeight: 128 }), { height: 96, scrollable: false });
  assert.deepEqual(calculateMessageInputSize({ scrollHeight: 220, minHeight: 44, maxHeight: 128 }), { height: 128, scrollable: true });

  const toggles = [];
  const input = {
    scrollHeight: 220,
    style: {},
    classList: { toggle(name, active) { toggles.push([name, active]); } },
  };
  const computedStyle = {
    minHeight: "44px",
    maxHeight: "128px",
    getPropertyValue() { return ""; },
  };
  assert.deepEqual(resizeMessageInputElement(input, computedStyle), { height: 128, scrollable: true });
  assert.equal(input.style.height, "128px");
  assert.equal(input.style.overflowY, "auto");
  assert.deepEqual(toggles.at(-1), ["message-input-scrollable", true]);

  input.scrollHeight = 30;
  assert.deepEqual(resizeMessageInputElement(input, computedStyle), { height: 44, scrollable: false });
  assert.equal(input.style.height, "44px");
  assert.equal(input.style.overflowY, "hidden");
  assert.deepEqual(toggles.at(-1), ["message-input-scrollable", false]);
});

test("an empty composer rests at the minimum height whatever scrollHeight reports", () => {
  // A stale or mid-layout scrollHeight must not leave an empty input tall: the
  // measured value has come back as the maximum in some layout states, and
  // nothing recomputes it afterwards.
  const input = {
    value: "",
    scrollHeight: 220,
    style: {},
    classList: { toggle() {} },
  };
  const computedStyle = { minHeight: "36px", maxHeight: "116px", getPropertyValue() { return ""; } };
  assert.deepEqual(resizeMessageInputElement(input, computedStyle), { height: 36, scrollable: false });
  assert.equal(input.style.height, "36px");
  assert.equal(input.style.overflowY, "hidden");

  // Once it holds text the measurement is used again.
  input.value = "several lines of text";
  assert.deepEqual(resizeMessageInputElement(input, computedStyle), { height: 116, scrollable: true });
});

test("reasoning effort control crops unsupported values when the selected model changes", () => {
  const elements = {};
  const pill = { classList: { toggle() {} } };
  elements.reasoningEffort = {
    value: "auto",
    innerHTML: "",
    disabled: false,
    dataset: {},
    setAttribute(name, value) { this[name] = value; },
    closest() { return pill; },
  };
  elements.reasoningEffortDisplay = { textContent: "" };
  elements.modelSelect = { value: "reasoning:model" };
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  try {
    const controller = createChatComposerController({
      state: { agent: { id: "agent-1", model: "reasoning:model", reasoningEffort: "high" } },
      currentProviderConfig: (model) => ({ capabilities: model === "basic:model" ? { reasoningEffort: false } : { reasoningEffort: true } }),
    });

    assert.equal(controller.refreshReasoningEffortControl(), "high");
    assert.equal(controller.refreshReasoningEffortControl({ modelValue: "basic:model" }), "auto");
    assert.equal(elements.reasoningEffort.value, "auto");
    assert.equal(elements.reasoningEffort.disabled, true);
    assert.equal(elements.reasoningEffortDisplay.textContent, "自动");
    // Mobile shows the English initial only.
    assert.equal(elements.reasoningEffortDisplay.dataset.mobileLabel, "A");
    assert.equal(controller.selectedReasoningEffort("basic:model"), "auto");
  } finally {
    globalThis.document = previousDocument;
  }
});

test("reasoning effort control persists the selected Agent override", async () => {
  const elements = {};
  const pillClasses = [];
  const pill = { classList: { toggle(name, active) { pillClasses.push([name, active]); } } };
  elements.reasoningEffort = {
    value: "auto",
    innerHTML: "",
    disabled: false,
    dataset: {},
    setAttribute(name, value) { this[name] = value; },
    closest() { return pill; },
  };
  elements.reasoningEffortDisplay = { textContent: "" };
  elements.modelSelect = { value: "openai:gpt-5" };
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  const requests = [];
  const state = {
    agent: { id: "agent-1", model: "openai:gpt-5", reasoningEffort: "low" },
    reasoningEffortSaving: false,
    reasoningEffortPending: undefined,
  };
  try {
    const controller = createChatComposerController({
      state,
      currentProviderConfig: () => ({ capabilities: { reasoningEffort: true } }),
      request: async (path, options) => {
        requests.push({ path, options });
        return { ...state.agent, reasoningEffort: JSON.parse(options.body).reasoningEffort };
      },
    });

    assert.equal(controller.refreshReasoningEffortControl(), "low");
    assert.equal(elements.reasoningEffort.value, "low");
    await controller.saveReasoningEffort("high");

    assert.equal(requests.length, 1);
    assert.equal(requests[0].path, "/api/agents/agent-1/reasoning-effort");
    assert.deepEqual(JSON.parse(requests[0].options.body), {
      reasoningEffort: "high",
      model: "openai:gpt-5",
      entityGeneration: 0,
    });
    assert.equal(state.agent.reasoningEffort, "high");
    assert.equal(elements.reasoningEffortDisplay.textContent, "高");
    assert.equal(elements.reasoningEffortDisplay.dataset.mobileLabel, "H");
    assert.ok(pillClasses.some(([name]) => name === "reasoning-effort-saving"));
  } finally {
    globalThis.document = previousDocument;
  }
});

test("Fast mode button follows model support and persists the Agent override", async () => {
  const classes = new Set(["hidden"]);
  const attributes = new Map();
  const elements = {
    openProviderLoginBtn: {
      classList: {
        toggle(name, active) {
          if (active) classes.add(name);
          else classes.delete(name);
        },
      },
      dataset: {},
      disabled: false,
      title: "",
      setAttribute(name, value) { attributes.set(name, value); },
    },
    modelSelect: { value: "codex:gpt-fast" },
  };
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  const requests = [];
  const state = { agent: { id: "agent-fast", model: "codex:gpt-fast", fastMode: false, entityGeneration: 3 } };
  try {
    const controller = createChatComposerController({
      state,
      currentProviderConfig: () => ({ modelCapabilities: { "gpt-fast": { fastMode: true } } }),
      request: async (path, options) => {
        requests.push({ path, options });
        return { ...state.agent, fastMode: JSON.parse(options.body).fastMode, entityGeneration: 4 };
      },
    });

    assert.equal(controller.refreshFastModeControl(), false);
    assert.equal(classes.has("hidden"), false);
    assert.equal(attributes.get("aria-pressed"), "false");
    await controller.saveFastMode(true);
    assert.equal(requests[0].path, "/api/agents/agent-fast/fast-mode");
    assert.deepEqual(JSON.parse(requests[0].options.body), {
      fastMode: true,
      model: "codex:gpt-fast",
      entityGeneration: 3,
    });
    assert.equal(state.agent.fastMode, true);
    assert.equal(classes.has("fast-mode-active"), true);
    assert.equal(attributes.get("aria-pressed"), "true");

    state.agent = { ...state.agent, model: "codex:gpt-basic", fastMode: true };
    elements.modelSelect.value = "codex:gpt-basic";
    controller.refreshFastModeControl();
    assert.equal(classes.has("hidden"), true);
    assert.equal(classes.has("fast-mode-active"), false);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("reasoning effort saves remain isolated when switching Agents", async () => {
  const elements = {};
  const pill = { classList: { toggle() {} } };
  elements.reasoningEffort = {
    value: "auto",
    innerHTML: "",
    disabled: false,
    dataset: {},
    setAttribute(name, value) { this[name] = value; },
    closest() { return pill; },
  };
  elements.reasoningEffortDisplay = { textContent: "" };
  elements.modelSelect = { value: "openai:model-a" };
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  const requests = [];
  const deferred = () => {
    let resolve;
    const promise = new Promise((done) => { resolve = done; });
    return { promise, resolve };
  };
  const state = { agent: { id: "agent-a", model: "openai:model-a", reasoningEffort: "low" } };
  try {
    const controller = createChatComposerController({
      state,
      currentProviderConfig: () => ({ capabilities: { reasoningEffort: true } }),
      request: (path, options) => {
        const pending = deferred();
        requests.push({ path, options, ...pending });
        return pending.promise;
      },
    });

    const savingA = controller.saveReasoningEffort("high");
    assert.equal(requests.length, 1);
    assert.equal(requests[0].path, "/api/agents/agent-a/reasoning-effort");

    state.agent = { id: "agent-b", model: "openai:model-b", reasoningEffort: "medium" };
    elements.modelSelect.value = "openai:model-b";
    const savingB = controller.saveReasoningEffort("low");
    assert.equal(requests.length, 2);
    assert.equal(requests[1].path, "/api/agents/agent-b/reasoning-effort");

    requests[0].resolve({ id: "agent-a", model: "openai:model-a", reasoningEffort: "high" });
    await savingA;
    assert.equal(state.agent.id, "agent-b");
    assert.equal(state.agent.reasoningEffort, "low");

    requests[1].resolve({ id: "agent-b", model: "openai:model-b", reasoningEffort: "low" });
    await savingB;
    assert.equal(state.agent.id, "agent-b");
    assert.equal(state.agent.reasoningEffort, "low");
  } finally {
    globalThis.document = previousDocument;
  }
});

test("reasoning effort responses cannot overwrite a newer model state", async () => {
  const elements = {};
  const pill = { classList: { toggle() {} } };
  elements.reasoningEffort = {
    value: "auto",
    innerHTML: "",
    disabled: false,
    dataset: {},
    setAttribute(name, value) { this[name] = value; },
    closest() { return pill; },
  };
  elements.reasoningEffortDisplay = { textContent: "" };
  elements.modelSelect = { value: "reasoning:model" };
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  let resolveRequest;
  const state = {
    agent: { id: "agent-1", model: "reasoning:model", reasoningEffort: "low", entityGeneration: 7 },
  };
  try {
    const controller = createChatComposerController({
      state,
      currentProviderConfig: (model) => ({ capabilities: model === "basic:model" ? { reasoningEffort: false } : { reasoningEffort: true } }),
      request: () => new Promise((resolve) => { resolveRequest = resolve; }),
    });

    const saving = controller.saveReasoningEffort("high");
    state.agent = { id: "agent-1", model: "basic:model", reasoningEffort: "auto", entityGeneration: 8 };
    elements.modelSelect.value = "basic:model";
    resolveRequest({ id: "agent-1", model: "reasoning:model", reasoningEffort: "high", entityGeneration: 8 });
    await saving;

    assert.deepEqual(state.agent, { id: "agent-1", model: "basic:model", reasoningEffort: "auto", entityGeneration: 8 });
  } finally {
    globalThis.document = previousDocument;
  }
});

test("message textarea autosizes restored drafts, input, scheduled paste measurement, and send reset", async () => {
  const previousDocument = globalThis.document;
  const previousWindow = globalThis.window;
  const previousGetComputedStyle = globalThis.getComputedStyle;
  const previousRequestAnimationFrame = globalThis.requestAnimationFrame;
  const classChanges = [];
  let messageValue = "";
  const input = {
    scrollHeight: 46,
    style: {},
    classList: { toggle(name, active) { classChanges.push([name, active]); } },
    get value() { return messageValue; },
    set value(value) {
      messageValue = String(value || "");
      this.scrollHeight = messageValue ? 220 : 46;
    },
    setAttribute() {},
    removeAttribute() {},
    focus() {},
  };
  const elements = {
    messageText: input,
    messageForm: { requestSubmit() {} },
    promptHistoryHint: { textContent: "" },
    slashCommandPalette: { classList: { add() {}, remove() {} }, innerHTML: "" },
  };
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  globalThis.window = { ...previousWindow, setTimeout(callback) { callback(); } };
  globalThis.getComputedStyle = () => ({ minHeight: "46px", maxHeight: "128px", getPropertyValue() { return ""; } });
  globalThis.requestAnimationFrame = (callback) => callback();
  const state = {
    agent: { id: "agent-1", model: "openai:model" },
    chatDrafts: { "agent-1": "saved draft" },
    promptHistory: [],
    pendingAttachments: [],
    serverSkills: [],
  };
  try {
    const controller = createChatComposerController({
      state,
      currentSkillsPreferences: () => ({ commands: [] }),
      isCurrentModelConfigured: () => true,
      loadMessages: async () => {},
      onMessageAccepted: async () => {},
      request: async () => ({}),
      scheduleMessageRefresh() {},
    });

    controller.restoreCurrentChatDraft();
    assert.equal(input.value, "saved draft");
    assert.equal(input.style.height, "128px");
    assert.equal(input.style.overflowY, "auto");

    input.value = "typed";
    input.scrollHeight = 96;
    controller.handleMessageInput();
    assert.equal(input.style.height, "96px");
    assert.equal(input.style.overflowY, "hidden");
    assert.equal(state.chatDrafts["agent-1"], "typed");

    input.scrollHeight = 220;
    controller.scheduleMessageInputResize();
    assert.equal(input.style.height, "128px");
    assert.equal(input.style.overflowY, "auto");

    await controller.sendMessage({ preventDefault() {} });
    assert.equal(input.value, "");
    assert.equal(input.style.height, "46px");
    assert.equal(input.style.overflowY, "hidden");
    assert.equal(state.chatDrafts["agent-1"], undefined);
    assert.deepEqual(classChanges.at(-1), ["message-input-scrollable", false]);
  } finally {
    globalThis.document = previousDocument;
    globalThis.window = previousWindow;
    globalThis.getComputedStyle = previousGetComputedStyle;
    globalThis.requestAnimationFrame = previousRequestAnimationFrame;
  }
});

test("message textarea Enter submission preserves IME and Shift+Enter behavior", () => {
  const previousDocument = globalThis.document;
  let submitted = 0;
  const input = {
    value: "hello",
    scrollHeight: 46,
    style: {},
    classList: { toggle() {} },
    setAttribute() {},
    removeAttribute() {},
  };
  const elements = {
    messageText: input,
    messageForm: { requestSubmit() { submitted += 1; } },
    promptHistoryHint: { textContent: "" },
    slashCommandPalette: { classList: { add() {}, remove() {} }, innerHTML: "" },
  };
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  try {
    const controller = createChatComposerController({
      state: { agent: { id: "agent-1" }, promptHistory: [], serverSkills: [] },
      currentSkillsPreferences: () => ({ commands: [] }),
      isComposingInput: (event) => Boolean(event.isComposing || event.keyCode === 229),
    });
    const keydown = (extra = {}) => {
      let prevented = false;
      controller.handleMessageKeydown({ key: "Enter", preventDefault() { prevented = true; }, ...extra });
      return prevented;
    };

    assert.equal(keydown({ isComposing: true }), false);
    assert.equal(keydown({ keyCode: 229 }), false);
    assert.equal(keydown({ shiftKey: true }), false);
    assert.equal(keydown(), true);
    assert.equal(submitted, 1);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("Composer sends project controls and caps ordinary conversations to execute-only context", async () => {
  const previousDocument = globalThis.document;
  const previousGetComputedStyle = globalThis.getComputedStyle;
  const input = {
    value: "Review the auth flow",
    scrollHeight: 46,
    style: {},
    classList: { toggle() {} },
    focus() {},
  };
  const elements = {
    messageText: input,
    promptHistoryHint: { textContent: "" },
    slashCommandPalette: { classList: { add() {}, remove() {} }, innerHTML: "" },
  };
  const requests = [];
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  globalThis.getComputedStyle = () => ({ minHeight: "46px", maxHeight: "128px", getPropertyValue() { return ""; } });
  try {
    const state = {
      agent: { id: "agent-1", model: "openai:model", planMode: false },
      navigationSelectionKind: "project",
      messageModes: {},
      promptHistory: [],
      pendingAttachments: [],
      serverSkills: [],
    };
    const controller = createChatComposerController({
      state,
      currentSkillsPreferences: () => ({ commands: [] }),
      isCurrentModelConfigured: () => true,
      loadMessages: async () => {},
      onMessageAccepted: async () => {},
      request: async (path, options) => {
        requests.push({ path, options });
        return { accepted: true };
      },
      scheduleMessageRefresh() {},
    });

    assert.equal(normalizeMessageMode("PLAN"), "plan");
    assert.equal(normalizeMessageMode("unknown", "plan"), "plan");
    assert.equal(controller.setMessageMode("plan"), "plan");
    await controller.sendMessage({ preventDefault() {} });

    assert.equal(requests.length, 1);
    assert.equal(requests[0].path, "/api/agents/agent-1/messages");
    assert.deepEqual(JSON.parse(requests[0].options.body), { text: "Review the auth flow", mode: "plan", context: "project" });

    state.navigationSelectionKind = "conversation";
    input.value = "Summarize the documentation";
    await controller.sendMessage({ preventDefault() {} });
    assert.equal(requests.length, 2);
    assert.deepEqual(JSON.parse(requests[1].options.body), { text: "Summarize the documentation", mode: "execute", context: "conversation" });
  } finally {
    globalThis.document = previousDocument;
    globalThis.getComputedStyle = previousGetComputedStyle;
  }
});

test("Composer waits for the full model reference to persist before posting a message", async () => {
  const previousDocument = globalThis.document;
  const previousGetComputedStyle = globalThis.getComputedStyle;
  let releaseModelSave;
  const modelSaveGate = new Promise((resolve) => { releaseModelSave = resolve; });
  const input = {
    value: "Use the selected model",
    disabled: false,
    scrollHeight: 46,
    style: {},
    classList: { toggle() {} },
    focus() {},
  };
  const elements = {
    messageText: input,
    modelSelect: { value: "zzz:gpt-5.6-sol" },
    promptHistoryHint: { textContent: "" },
    slashCommandPalette: { classList: { add() {}, remove() {} }, innerHTML: "" },
  };
  const requests = [];
  const state = {
    agent: { id: "agent-model", model: "zz:grok-4.5" },
    navigationSelectionKind: "conversation",
    promptHistory: [],
    pendingAttachments: [],
    serverSkills: [],
  };
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  globalThis.getComputedStyle = () => ({ minHeight: "46px", maxHeight: "128px", getPropertyValue() { return ""; } });
  try {
    let saveStarted = false;
    const controller = createChatComposerController({
      state,
      awaitAgentSettingsSaved: async (agentId) => {
        assert.equal(agentId, "agent-model");
        saveStarted = true;
        await modelSaveGate;
        state.agent = { ...state.agent, model: "zzz:gpt-5.6-sol" };
      },
      currentSkillsPreferences: () => ({ commands: [] }),
      isCurrentModelConfigured: () => true,
      loadMessages: async () => {},
      onMessageAccepted: async () => {},
      request: async (path, options) => {
        assert.equal(state.agent.model, "zzz:gpt-5.6-sol");
        requests.push({ path, options });
        return { accepted: true };
      },
      scheduleMessageRefresh() {},
    });

    const sending = controller.sendMessage({ preventDefault() {} });
    await Promise.resolve();
    assert.equal(saveStarted, true);
    assert.equal(requests.length, 0);
    assert.equal(input.disabled, true);

    releaseModelSave();
    await sending;

    assert.equal(requests.length, 1);
    assert.equal(requests[0].path, "/api/agents/agent-model/messages");
    assert.equal(input.value, "");
    assert.equal(input.disabled, false);
  } finally {
    globalThis.document = previousDocument;
    globalThis.getComputedStyle = previousGetComputedStyle;
  }
});

test("Composer uses the final selected model when it changes during settings persistence", async () => {
  const previousDocument = globalThis.document;
  const previousGetComputedStyle = globalThis.getComputedStyle;
  let releaseModelSave;
  const modelSaveGate = new Promise((resolve) => { releaseModelSave = resolve; });
  const input = {
    value: "Continue with the latest model",
    disabled: false,
    scrollHeight: 46,
    style: {},
    classList: { toggle() {} },
    focus() {},
  };
  const elements = {
    messageText: input,
    modelSelect: { value: "openai:model-b" },
    promptHistoryHint: { textContent: "" },
    slashCommandPalette: { classList: { add() {}, remove() {} }, innerHTML: "" },
  };
  const requests = [];
  const state = {
    agent: { id: "agent-model-switch", model: "openai:model-a" },
    navigationSelectionKind: "conversation",
    promptHistory: [],
    pendingAttachments: [],
    serverSkills: [],
  };
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  globalThis.getComputedStyle = () => ({ minHeight: "46px", maxHeight: "128px", getPropertyValue() { return ""; } });
  try {
    let saveStarted = false;
    const controller = createChatComposerController({
      state,
      awaitAgentSettingsSaved: async (agentId) => {
        assert.equal(agentId, "agent-model-switch");
        saveStarted = true;
        await modelSaveGate;
        state.agent = { ...state.agent, model: elements.modelSelect.value };
      },
      currentSkillsPreferences: () => ({ commands: [] }),
      isCurrentModelConfigured: () => elements.modelSelect.value === state.agent.model,
      loadMessages: async () => {},
      onMessageAccepted: async () => {},
      request: async (path, options) => {
        requests.push({ path, options, model: state.agent.model });
        return { accepted: true };
      },
      scheduleMessageRefresh() {},
    });

    const sending = controller.sendMessage({ preventDefault() {} });
    await Promise.resolve();
    assert.equal(saveStarted, true);
    assert.equal(requests.length, 0);
    assert.equal(input.disabled, true);

    elements.modelSelect.value = "anthropic:model-c";
    releaseModelSave();
    await sending;

    assert.equal(requests.length, 1);
    assert.equal(requests[0].path, "/api/agents/agent-model-switch/messages");
    assert.equal(requests[0].model, "anthropic:model-c");
    assert.deepEqual(JSON.parse(requests[0].options.body), {
      text: "Continue with the latest model",
      mode: "execute",
      context: "conversation",
    });
    assert.equal(input.value, "");
    assert.equal(input.disabled, false);
  } finally {
    globalThis.document = previousDocument;
    globalThis.getComputedStyle = previousGetComputedStyle;
  }
});

test("Composer does not send when the selected and persisted models remain inconsistent", async () => {
  const previousDocument = globalThis.document;
  const previousGetComputedStyle = globalThis.getComputedStyle;
  const input = {
    value: "Keep this draft",
    disabled: false,
    scrollHeight: 46,
    style: {},
    classList: { toggle() {} },
    focus() {},
  };
  const elements = {
    messageText: input,
    modelSelect: { value: "anthropic:model-b" },
    promptHistoryHint: { textContent: "" },
    slashCommandPalette: { classList: { add() {}, remove() {} }, innerHTML: "" },
  };
  const requests = [];
  const state = {
    agent: { id: "agent-model-mismatch", model: "openai:model-a" },
    navigationSelectionKind: "conversation",
    promptHistory: [],
    pendingAttachments: [],
    serverSkills: [],
  };
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  globalThis.getComputedStyle = () => ({ minHeight: "46px", maxHeight: "128px", getPropertyValue() { return ""; } });
  try {
    const controller = createChatComposerController({
      state,
      awaitAgentSettingsSaved: async () => {},
      currentSkillsPreferences: () => ({ commands: [] }),
      isCurrentModelConfigured: () => true,
      loadMessages: async () => {},
      request: async (...args) => {
        requests.push(args);
        return { accepted: true };
      },
      scheduleMessageRefresh() {},
    });

    await assert.rejects(
      controller.sendMessage({ preventDefault() {} }),
      /The selected model could not be synchronized/,
    );

    assert.equal(requests.length, 0);
    assert.equal(input.value, "Keep this draft");
    assert.equal(input.disabled, false);
  } finally {
    globalThis.document = previousDocument;
    globalThis.getComputedStyle = previousGetComputedStyle;
  }
});

test("Composer creates a goal without waiting for or configuring a model run", async () => {
  const previousDocument = globalThis.document;
  const previousGetComputedStyle = globalThis.getComputedStyle;
  const input = {
    value: "/goal Ship the protected task",
    disabled: false,
    scrollHeight: 46,
    style: {},
    classList: { toggle() {} },
    focus() {},
  };
  const elements = {
    messageText: input,
    promptHistoryHint: { textContent: "" },
    slashCommandPalette: { classList: { add() {}, remove() {} }, innerHTML: "" },
  };
  const state = {
    agent: { id: "agent-goal", model: "", planMode: false },
    navigationSelectionKind: "project",
    messageModes: {},
    promptHistory: [],
    pendingAttachments: [],
    serverSkills: [],
  };
  const requests = [];
  let modelSaveWaits = 0;
  let modelChecks = 0;
  let modelNotices = 0;
  let messageLoads = 0;
  let refreshes = 0;
  let acceptedResult = null;
  const response = { goal: { confirmation: { taskId: "task-goal" } } };
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  globalThis.getComputedStyle = () => ({ minHeight: "46px", maxHeight: "128px", getPropertyValue() { return ""; } });
  try {
    const controller = createChatComposerController({
      state,
      awaitAgentSettingsSaved: async () => { modelSaveWaits += 1; },
      currentSkillsPreferences: () => ({ commands: [] }),
      isCurrentModelConfigured: () => { modelChecks += 1; return false; },
      loadMessages: async () => { messageLoads += 1; },
      onMessageAccepted: async (accepted, agentId) => { acceptedResult = { accepted, agentId }; },
      request: async (path, options) => {
        requests.push({ path, options });
        return response;
      },
      scheduleMessageRefresh() { refreshes += 1; },
      showModelSetupNotice() { modelNotices += 1; },
    });

    await controller.sendMessage({ preventDefault() {} });

    assert.equal(modelSaveWaits, 0);
    assert.equal(modelChecks, 0);
    assert.equal(modelNotices, 0);
    assert.equal(messageLoads, 0);
    assert.equal(refreshes, 0);
    assert.equal(requests.length, 1);
    assert.equal(requests[0].path, "/api/agents/agent-goal/messages");
    assert.deepEqual(JSON.parse(requests[0].options.body), {
      text: "/goal Ship the protected task",
      mode: "execute",
      context: "project",
    });
    assert.deepEqual(acceptedResult, { accepted: response, agentId: "agent-goal" });
    assert.equal(state.promptHistory[0], "/goal Ship the protected task");
    assert.equal(input.value, "");
    assert.equal(input.disabled, false);
  } finally {
    globalThis.document = previousDocument;
    globalThis.getComputedStyle = previousGetComputedStyle;
  }
});

test("video processing blocks sending, adds only readable derived files, and releases preview URLs", async () => {
  const previousDocument = globalThis.document;
  const previousURL = globalThis.URL;
  const previousGetComputedStyle = globalThis.getComputedStyle;
  let finishProcessing;
  const processingGate = new Promise((resolve) => { finishProcessing = resolve; });
  const createdUrls = [];
  const revokedUrls = [];
  globalThis.URL = {
    createObjectURL(file) {
      const url = `blob:${file.name}`;
      createdUrls.push(url);
      return url;
    },
    revokeObjectURL(url) { revokedUrls.push(url); },
  };
  const classes = { toggle() {} };
  const attributes = new Map();
  const input = { value: "send after frames", disabled: false, scrollHeight: 46, style: {}, classList: classes, focus() {} };
  const sendButton = {
    textContent: "Send",
    disabled: false,
    dataset: {},
    setAttribute(name, value) { attributes.set(name, value); },
    removeAttribute(name) { attributes.delete(name); },
  };
  const elements = {
    messageText: input,
    sendMessageBtn: sendButton,
    attachFileBtn: { disabled: false },
    attachFileInput: { disabled: false },
    pendingAttachments: { classList: classes, innerHTML: "", querySelectorAll() { return []; } },
    promptHistoryHint: { textContent: "" },
    slashCommandPalette: { classList: { add() {}, remove() {} }, innerHTML: "" },
  };
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  globalThis.getComputedStyle = () => ({ minHeight: "46px", maxHeight: "128px", getPropertyValue() { return ""; } });
  const state = {
    agent: { id: "agent-video", model: "openai:model" },
    navigationSelectionKind: "conversation",
    pendingAttachments: [],
    promptHistory: [],
    serverSkills: [],
  };
  const requests = [];
  const toasts = [];
  try {
    const controller = createChatComposerController({
      state,
      attachmentKind: (file) => file.type.startsWith("image/") ? "image" : file.type.startsWith("video/") ? "video" : "text",
      currentSkillsPreferences: () => ({ commands: [] }),
      isCurrentModelConfigured: () => true,
      prepareVideoAttachment: async (file) => {
        await processingGate;
        const frames = [
          { name: "clip.frame-01.jpg", type: "image/jpeg", size: 100 },
          { name: "clip.frame-02.jpg", type: "image/jpeg", size: 120 },
        ];
        const manifest = { name: "clip.keyframes.txt", type: "text/plain;charset=utf-8", size: 80 };
        return { files: [...frames, manifest], frameFiles: frames, originalIncluded: false, totalBytes: 300 };
      },
      request: async (...args) => { requests.push(args); return {}; },
      showToast: (message, tone) => toasts.push({ message, tone }),
    });

    const adding = controller.addPendingAttachmentFiles([{ name: "clip.mp4", type: "video/mp4", size: 1024 }]);
    await Promise.resolve();
    assert.equal(state.pendingAttachmentProcessing, 1);
    assert.equal(sendButton.disabled, true);
    assert.equal(elements.attachFileBtn.disabled, true);
    assert.equal(attributes.get("aria-busy"), "true");

    await controller.sendMessage({ preventDefault() {} });
    assert.equal(requests.length, 0);
    assert.equal(input.value, "send after frames");
    assert.equal(toasts.some((toast) => toast.tone === "warn"), true);

    finishProcessing();
    await adding;
    assert.equal(state.pendingAttachmentProcessing, 0);
    assert.equal(sendButton.disabled, false);
    assert.equal(elements.attachFileBtn.disabled, false);
    assert.equal(state.pendingAttachments.length, 3);
    assert.deepEqual(createdUrls, ["blob:clip.frame-01.jpg", "blob:clip.frame-02.jpg"]);

    const firstFrame = state.pendingAttachments.find((item) => item.file.name === "clip.frame-01.jpg");
    controller.removePendingAttachment(firstFrame.id);
    assert.deepEqual(revokedUrls, ["blob:clip.frame-01.jpg"]);
    controller.clearPendingAttachments();
    assert.deepEqual(revokedUrls, ["blob:clip.frame-01.jpg", "blob:clip.frame-02.jpg"]);
    assert.deepEqual(state.pendingAttachments, []);
  } finally {
    globalThis.document = previousDocument;
    globalThis.URL = previousURL;
    globalThis.getComputedStyle = previousGetComputedStyle;
  }
});

test("stale video work cannot overwrite a newer agent generation", async () => {
  const previousDocument = globalThis.document;
  const previousURL = globalThis.URL;
  const deferred = () => {
    let resolve;
    const promise = new Promise((done) => { resolve = done; });
    return { promise, resolve };
  };
  const gates = { "a.mp4": deferred(), "b.mp4": deferred() };
  const signals = {};
  const revoked = [];
  globalThis.URL = {
    createObjectURL: (file) => `blob:${file.name}`,
    revokeObjectURL: (url) => revoked.push(url),
  };
  const elements = {
    messageText: { value: "", disabled: false },
    sendMessageBtn: { textContent: "Send", disabled: false, dataset: {}, setAttribute() {}, removeAttribute() {} },
    attachFileBtn: { disabled: false },
    attachFileInput: { disabled: false },
    pendingAttachments: { classList: { toggle() {} }, innerHTML: "", querySelectorAll() { return []; } },
  };
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  const state = { agent: { id: "agent-a" }, navigationSelectionKind: "conversation", pendingAttachments: [] };
  try {
    const controller = createChatComposerController({
      state,
      attachmentKind: (file) => file.type.startsWith("image/") ? "image" : "video",
      prepareVideoAttachment: async (file, options) => {
        signals[file.name] = options.signal;
        return gates[file.name].promise;
      },
      showToast() {},
    });
    const sourceA = { name: "a.mp4", type: "video/mp4", size: 100 };
    const sourceB = { name: "b.mp4", type: "video/mp4", size: 100 };
    const addingA = controller.addPendingAttachmentFiles([sourceA]);
    await Promise.resolve();
    assert.equal(state.pendingAttachmentProcessing, 1);

    state.agent = { id: "agent-b" };
    controller.syncMessageComposerBusy();
    assert.equal(signals["a.mp4"].aborted, true);
    assert.equal(state.pendingAttachmentProcessing, 0);

    const addingB = controller.addPendingAttachmentFiles([sourceB]);
    await Promise.resolve();
    assert.equal(state.pendingAttachmentProcessing, 1);
    const frameA = { name: "a.frame.jpg", type: "image/jpeg", size: 10 };
    gates["a.mp4"].resolve({ files: [frameA], frameFiles: [frameA], originalIncluded: false, totalBytes: 10 });
    const resultA = await addingA;
    assert.equal(resultA.cancelled, true);
    assert.equal(state.pendingAttachmentProcessing, 1);
    assert.deepEqual(state.pendingAttachments, []);

    const frameB = { name: "b.frame.jpg", type: "image/jpeg", size: 10 };
    gates["b.mp4"].resolve({ files: [frameB], frameFiles: [frameB], originalIncluded: false, totalBytes: 10 });
    await addingB;
    assert.equal(state.pendingAttachmentProcessing, 0);
    assert.deepEqual(state.pendingAttachments.map((item) => item.file.name), ["b.frame.jpg"]);
    assert.deepEqual(revoked, []);
  } finally {
    globalThis.document = previousDocument;
    globalThis.URL = previousURL;
  }
});

test("a context switch during preview creation revokes every stale object URL", async () => {
  const previousDocument = globalThis.document;
  const previousURL = globalThis.URL;
  const revoked = [];
  const elements = {
    messageText: { value: "", disabled: false },
    sendMessageBtn: { textContent: "Send", disabled: false, dataset: {}, setAttribute() {}, removeAttribute() {} },
    attachFileBtn: { disabled: false },
    attachFileInput: { disabled: false },
    pendingAttachments: { classList: { toggle() {} }, innerHTML: "", querySelectorAll() { return []; } },
  };
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  const state = { agent: { id: "agent-preview-a" }, navigationSelectionKind: "conversation", pendingAttachments: [] };
  let controller;
  let created = 0;
  globalThis.URL = {
    createObjectURL(file) {
      created += 1;
      const url = `blob:${file.name}`;
      if (created === 1) {
        state.agent = { id: "agent-preview-b" };
        controller.syncMessageComposerBusy();
      }
      return url;
    },
    revokeObjectURL(url) { revoked.push(url); },
  };
  try {
    controller = createChatComposerController({
      state,
      attachmentKind: () => "image",
      prepareVideoAttachment: async () => {
        const frames = [
          { name: "stale-1.jpg", type: "image/jpeg", size: 10 },
          { name: "stale-2.jpg", type: "image/jpeg", size: 10 },
        ];
        return { files: frames, frameFiles: frames, originalIncluded: false, totalBytes: 20 };
      },
      showToast() {},
    });
    const result = await controller.addPendingAttachmentFiles([{ name: "stale.mp4", type: "video/mp4", size: 100 }]);
    assert.equal(result.cancelled, true);
    assert.equal(created, 1);
    assert.deepEqual(revoked, ["blob:stale-1.jpg"]);
    assert.deepEqual(state.pendingAttachments, []);
    assert.equal(state.pendingAttachmentProcessing, 0);
  } finally {
    globalThis.document = previousDocument;
    globalThis.URL = previousURL;
  }
});

test("clearing or deleting attachments cancels active video work and prevents late writes", async () => {
  const previousDocument = globalThis.document;
  const previousURL = globalThis.URL;
  const deferred = () => {
    let resolve;
    const promise = new Promise((done) => { resolve = done; });
    return { promise, resolve };
  };
  const gates = [deferred(), deferred()];
  const signals = [];
  const revoked = [];
  globalThis.URL = {
    createObjectURL: (file) => `blob:${file.name}`,
    revokeObjectURL: (url) => revoked.push(url),
  };
  const elements = {
    messageText: { value: "", disabled: false },
    sendMessageBtn: { textContent: "Send", disabled: false, dataset: {}, setAttribute() {}, removeAttribute() {} },
    attachFileBtn: { disabled: false },
    attachFileInput: { disabled: false },
    pendingAttachments: { classList: { toggle() {} }, innerHTML: "", querySelectorAll() { return []; } },
  };
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  const state = {
    agent: { id: "agent-cancel" },
    navigationSelectionKind: "conversation",
    pendingAttachments: [{ id: "existing", file: { name: "existing.jpg", type: "image/jpeg", size: 10 }, kind: "image", previewUrl: "blob:existing.jpg" }],
  };
  let call = 0;
  try {
    const controller = createChatComposerController({
      state,
      attachmentKind: () => "image",
      prepareVideoAttachment: async (_file, options) => {
        signals.push(options.signal);
        return gates[call++].promise;
      },
      showToast() {},
    });

    const clearing = controller.addPendingAttachmentFiles([{ name: "clear.mp4", type: "video/mp4", size: 100 }]);
    await Promise.resolve();
    controller.clearPendingAttachments();
    assert.equal(signals[0].aborted, true);
    assert.equal(state.pendingAttachmentProcessing, 0);
    gates[0].resolve({ files: [{ name: "late-clear.jpg", type: "image/jpeg", size: 10 }], frameFiles: [], totalBytes: 10 });
    assert.equal((await clearing).cancelled, true);
    assert.deepEqual(state.pendingAttachments, []);

    state.pendingAttachments = [{ id: "remove-me", file: { name: "remove.jpg", type: "image/jpeg", size: 10 }, kind: "image", previewUrl: "blob:remove.jpg" }];
    const deleting = controller.addPendingAttachmentFiles([{ name: "delete.mp4", type: "video/mp4", size: 100 }]);
    await Promise.resolve();
    controller.removePendingAttachment("remove-me");
    assert.equal(signals[1].aborted, true);
    assert.equal(state.pendingAttachmentProcessing, 0);
    gates[1].resolve({ files: [{ name: "late-delete.jpg", type: "image/jpeg", size: 10 }], frameFiles: [], totalBytes: 10 });
    assert.equal((await deleting).cancelled, true);
    assert.deepEqual(state.pendingAttachments, []);
    assert.deepEqual(revoked, ["blob:existing.jpg", "blob:remove.jpg"]);
  } finally {
    globalThis.document = previousDocument;
    globalThis.URL = previousURL;
  }
});

test("failed, over-limit, or timed-out video processing adds no unreadable pending attachment", async () => {
  const previousDocument = globalThis.document;
  const elements = {
    messageText: { value: "", disabled: false },
    sendMessageBtn: { textContent: "Send", dataset: {}, setAttribute() {}, removeAttribute() {} },
    attachFileBtn: { disabled: false },
    attachFileInput: { disabled: false },
    pendingAttachments: { classList: { toggle() {} }, innerHTML: "", querySelectorAll() { return []; } },
  };
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  const state = { agent: { id: "agent-video" }, pendingAttachments: [] };
  try {
    // Use a rejecting dependency without constructing browser media objects.
    const controller = createChatComposerController({
      state,
      attachmentKind: () => "video",
      prepareVideoAttachment: async () => { throw Object.assign(new Error("deadline exceeded"), { code: "processing-timeout" }); },
      showToast() {},
    });
    const result = await controller.addPendingAttachmentFiles([{ name: "long.mp4", type: "video/mp4", size: 1024 }]);
    assert.equal(result.added.length, 0);
    assert.equal(result.skipped.length, 1);
    assert.deepEqual(state.pendingAttachments, []);
    assert.equal(state.pendingAttachmentProcessing, 0);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("Composer keeps invalid goal drafts and attachments without sending", async () => {
  const previousDocument = globalThis.document;
  const input = {
    value: "/goal",
    disabled: false,
    scrollHeight: 46,
    style: {},
    classList: { toggle() {} },
    focus() {},
  };
  const elements = {
    messageText: input,
    promptHistoryHint: { textContent: "" },
    slashCommandPalette: { classList: { add() {}, remove() {} }, innerHTML: "" },
  };
  const state = {
    agent: { id: "agent-goal", model: "" },
    navigationSelectionKind: "project",
    messageModes: {},
    promptHistory: [],
    pendingAttachments: [],
    serverSkills: [],
  };
  const requests = [];
  const toasts = [];
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  try {
    const controller = createChatComposerController({
      state,
      awaitAgentSettingsSaved: async () => { throw new Error("goal validation must happen first"); },
      currentSkillsPreferences: () => ({ commands: [] }),
      isCurrentModelConfigured: () => { throw new Error("goal validation must happen first"); },
      request: async (...args) => { requests.push(args); return {}; },
      showToast: (message, tone) => toasts.push({ message, tone }),
    });

    await controller.sendMessage({ preventDefault() {} });
    assert.equal(requests.length, 0);
    assert.equal(input.value, "/goal");

    state.navigationSelectionKind = "conversation";
    input.value = "/goal Conversation task";
    await controller.sendMessage({ preventDefault() {} });
    assert.equal(requests.length, 0);
    assert.equal(input.value, "/goal Conversation task");

    state.navigationSelectionKind = "project";
    state.pendingAttachments = [{ id: "attachment-1", file: { name: "notes.txt" } }];
    input.value = "/goal Project task";
    await controller.sendMessage({ preventDefault() {} });
    assert.equal(requests.length, 0);
    assert.equal(input.value, "/goal Project task");
    assert.equal(state.pendingAttachments.length, 1);
    assert.deepEqual(toasts.map((toast) => toast.tone), ["warn", "warn", "warn"]);
    assert.equal(toasts.every((toast) => Boolean(toast.message)), true);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("/queue holds messages until the turn ends, then sends them in order", async () => {
  const queueHost = { className: "", innerHTML: "", classList: { toggle() {} }, querySelectorAll: () => [] };
  const input = { value: "/queue second thing", style: {}, focus() {}, classList: { toggle() {} } };
  const elements = { messageQueue: queueHost, messageText: input, pendingAttachments: null };
  const state = {
    agent: { id: "agent-1", status: "running", model: "m" },
    navigationSelectionKind: "conversation",
    messageQueue: [],
    pendingToolApprovals: {},
    liveToolOutputs: {},
    pendingAttachments: [],
    chatDrafts: {},
    promptHistory: [],
  };
  const sent = [];
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  try {
    const controller = createChatComposerController({
      state,
      isCurrentModelConfigured: () => true,
      loadMessages: async () => {},
      scheduleMessageRefresh() {},
      notifyTerminal() {},
      showToast() {},
      onMessageAccepted: async () => {},
      request: async (url, options) => {
        sent.push(JSON.parse(options.body).text);
        return { id: `m${sent.length}` };
      },
    });

    await controller.sendMessage({ preventDefault() {} });
    assert.deepEqual(sent, [], "a queued message must not be posted while the turn is running");
    assert.deepEqual(state.messageQueue.map((item) => item.text), ["second thing"]);

    // With something already parked, a plain send joins the back of the line
    // rather than overtaking it.
    input.value = "third thing";
    await controller.sendMessage({ preventDefault() {} });
    assert.deepEqual(sent, []);
    assert.deepEqual(state.messageQueue.map((item) => item.text), ["second thing", "third thing"]);

    // Still blocked while a tool is mid-flight, even though the agent record
    // has already flipped away from "running".
    state.agent.status = "idle";
    state.liveToolOutputs = { t1: { status: "running" } };
    await controller.drainMessageQueue();
    assert.deepEqual(sent, []);

    state.liveToolOutputs = {};
    await controller.drainMessageQueue();
    await controller.drainMessageQueue();
    assert.deepEqual(sent, ["second thing", "third thing"]);
    assert.deepEqual(state.messageQueue, []);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("a restored queue is re-typed and bounded before anything reads it", () => {
  assert.deepEqual(normalizeMessageQueue(null), []);
  assert.deepEqual(normalizeMessageQueue([
    { id: "a", agentId: "agent-1", text: "keep", mode: "plan", context: "project" },
    { id: "a", agentId: "agent-1", text: "duplicate id" },
    { id: "", agentId: "agent-1", text: "no id" },
    { id: "b", agentId: "", text: "no agent" },
    { id: "c", agentId: "agent-1", text: "   " },
    { id: "d", agentId: "agent-1", text: "defaults", mode: "nonsense", context: "nonsense" },
    "not an object",
  ]), [
    { id: "a", agentId: "agent-1", text: "keep", mode: "plan", context: "project" },
    { id: "d", agentId: "agent-1", text: "defaults", mode: "execute", context: "conversation" },
  ]);
  const flood = Array.from({ length: 40 }, (unused, index) => ({ id: `q${index}`, agentId: "agent-1", text: `m${index}` }));
  assert.equal(normalizeMessageQueue(flood).length, maxQueuedMessages);
});
