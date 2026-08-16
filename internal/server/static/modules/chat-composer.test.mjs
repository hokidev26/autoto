import test from "node:test";
import assert from "node:assert/strict";
import { t } from "./i18n.mjs";

globalThis.window = { AUTOTO_LOCAL_TOKEN: "" };
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
  queueCollapseThreshold,
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

test("a model's own catalog levels replace the provider list immediately", () => {
  const provider = {
    name: "anthropic",
    capabilities: { reasoningEffort: true, reasoningEfforts: ["low", "medium", "high"] },
    modelCapabilities: {
      "deepseek-chat": { reasoningEfforts: ["low", "medium", "high"] },
      "claude-opus-4-7": { reasoningEfforts: ["low", "medium", "high", "xhigh", "max"] },
    },
  };
  assert.deepEqual(
    reasoningEffortValuesForModel(provider, "anthropic:deepseek-chat"),
    ["auto", "low", "medium", "high"],
  );
  assert.deepEqual(
    reasoningEffortValuesForModel(provider, "anthropic:claude-opus-4-7"),
    ["auto", "low", "medium", "high", "xhigh", "max"],
  );
});

test("a model without an effort list keeps the provider baseline, including extra levels", () => {
  const provider = {
    capabilities: { reasoningEffort: true, reasoningEfforts: ["low", "medium", "high", "xhigh", "max"] },
    modelCapabilities: {
      "claude-opus-4-7": { fastMode: false, reasoningEffort: true },
    },
  };
  assert.deepEqual(
    reasoningEffortValuesForModel(provider, "anthropic:claude-opus-4-7"),
    ["auto", "low", "medium", "high", "xhigh", "max"],
  );
});

test("a codex model exposes its own catalog levels in every navigation context", () => {
  // The catalog reports levels per model: gpt-5.6-luna serves "max", gpt-5.5
  // stops at "xhigh". Navigation context must not change either answer.
  const provider = {
    name: "codex",
    capabilities: { reasoningEffort: true, reasoningEfforts: ["low", "medium", "high", "xhigh"] },
    modelCapabilities: {
      "gpt-5.5": { reasoningEfforts: ["low", "medium", "high", "xhigh"] },
      "gpt-5.6-luna": { reasoningEfforts: ["low", "medium", "high", "xhigh", "max"] },
    },
  };
  const expected = {
    "codex:gpt-5.5": ["auto", "low", "medium", "high", "xhigh"],
    "codex:gpt-5.6-luna": ["auto", "low", "medium", "high", "xhigh", "max"],
    // A model the catalog said nothing about falls back to the provider list
    // rather than guessing a level the model may reject.
    "codex:gpt-unknown": ["auto", "low", "medium", "high", "xhigh"],
  };
  for (const [model, values] of Object.entries(expected)) {
    assert.deepEqual(reasoningEffortValuesForModel(provider, model), values);
    for (const navigationSelectionKind of ["conversation", "project"]) {
      assert.deepEqual(reasoningEffortValuesForModel({ ...provider, navigationSelectionKind }, model), values);
    }
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
    // The compact row shows the English initial in every locale: one glyph in a
    // fixed-width cell, rather than a word whose width changes with the language.
    assert.equal(elements.reasoningEffortDisplay.dataset.mobileLabel, "A");
    assert.equal(controller.selectedReasoningEffort("basic:model"), "auto");
  } finally {
    globalThis.document = previousDocument;
  }
});

test("switching models rebuilds thinking-effort options from per-model catalog levels immediately", () => {
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
  elements.modelSelect = { value: "relay:deepseek-chat" };
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  const provider = {
    capabilities: { reasoningEffort: true, reasoningEfforts: ["low", "medium", "high"] },
    modelCapabilities: {
      "deepseek-chat": { reasoningEfforts: ["low", "medium", "high"] },
      "claude-opus-4-7": { reasoningEfforts: ["low", "medium", "high", "xhigh", "max"] },
    },
  };
  try {
    const controller = createChatComposerController({
      state: { agent: { id: "agent-1", model: "relay:deepseek-chat", reasoningEffort: "high" } },
      currentProviderConfig: () => provider,
    });

    controller.refreshReasoningEffortControl({ modelValue: "relay:deepseek-chat" });
    assert.equal(elements.reasoningEffort.innerHTML.includes('value="xhigh"'), false);
    controller.refreshReasoningEffortControl({ modelValue: "relay:claude-opus-4-7" });
    assert.match(elements.reasoningEffort.innerHTML, /value="xhigh"/);
    assert.match(elements.reasoningEffort.innerHTML, /value="max"/);
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
    // The trigger keeps the localized word; the compact row shows the initial.
    assert.equal(elements.reasoningEffortDisplay.dataset.mobileLabel, "H");
    assert.ok(pillClasses.some(([name]) => name === "reasoning-effort-saving"));
  } finally {
    globalThis.document = previousDocument;
  }
});

// On a phone the letter is the whole control -- no words fit beside it -- so two
// levels sharing a letter makes them indistinguishable. "max" used to answer "M"
// like "medium", which is the worst pair to confuse: the strongest setting and
// the middle one.
test("every reasoning effort level has a distinct phone label", async () => {
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
  elements.reasoningEffortDisplay = { textContent: "", dataset: {} };
  elements.modelSelect = { value: "codex:gpt-5.6-sol" };
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  const state = { agent: { id: "agent-1", model: "codex:gpt-5.6-sol" }, reasoningEffortSaving: false };
  try {
    const controller = createChatComposerController({
      state,
      // The extra levels are per-model Codex capabilities, so the provider has to
      // advertise them or every request normalises back to auto.
      currentProviderConfig: () => ({
        capabilities: { reasoningEffort: true },
        modelCapabilities: {
          "gpt-5.6-sol": { reasoningEffortValues: ["auto", "low", "medium", "high", "xhigh", "max", "ultra"] },
        },
      }),
      request: async () => ({}),
    });

    const levels = ["auto", "low", "medium", "high", "xhigh", "max", "ultra"];
    const labels = new Map();
    for (const level of levels) {
      controller.refreshReasoningEffortControl({ requestedValue: level });
      labels.set(level, elements.reasoningEffortDisplay.dataset.mobileLabel);
    }

    // The mapping the row actually shows: an English initial per level, never a
    // localized word, so the cell keeps one width in every language.
    assert.deepEqual(Object.fromEntries(labels), {
      auto: "A", low: "L", medium: "M", high: "H", xhigh: "X", max: "MX", ultra: "UX",
    });

    // "max" used to share "M" with "medium": the strongest and the middle setting
    // looked alike, which is the one pair where confusing them costs the most.
    const seen = [...labels.values()];
    assert.equal(new Set(seen).size, seen.length, `phone labels must be unique per level, got ${seen.join(", ")}`);
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

// A throw *after* the POST is a different situation from a throw instead of it.
// The server already has the turn, so putting the text back leaves it on screen
// beside the message it produced and invites sending the same thing twice --
// which is exactly what a failed goal-confirmation reload, a localStorage write
// or a scroll during a layout pass used to do.
test("a failure after the message is accepted leaves the composer empty", async () => {
  const previousDocument = globalThis.document;
  const previousGetComputedStyle = globalThis.getComputedStyle;
  const input = {
    value: "Already delivered",
    disabled: false,
    scrollHeight: 46,
    style: {},
    classList: { toggle() {} },
    focus() {},
  };
  const elements = {
    messageText: input,
    modelSelect: { value: "openai:model-a" },
    promptHistoryHint: { textContent: "" },
    slashCommandPalette: { classList: { add() {}, remove() {} }, innerHTML: "" },
  };
  const requests = [];
  const terminal = [];
  const state = {
    agent: { id: "agent-post-send", model: "openai:model-a" },
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
      notifyTerminal: (line) => terminal.push(line),
      request: async (...args) => {
        requests.push(args);
        return { accepted: true };
      },
      // Stands in for every post-POST step: onMessageAccepted is the first of them
      // and the one that actually did this, by awaiting a spec-board reload.
      onMessageAccepted: async () => {
        throw new Error("goal confirmation reload failed");
      },
      scheduleMessageRefresh() {},
    });

    // The send itself must not report failure: the message was accepted.
    await controller.sendMessage({ preventDefault() {} });

    assert.equal(requests.length, 1, "the message must still be posted once");
    assert.equal(input.value, "", "a delivered message must not come back in the composer");
    assert.equal(input.disabled, false);
    assert.ok(
      terminal.some((line) => line.includes("already delivered")),
      `the failure must be reported rather than silently swallowed, got ${JSON.stringify(terminal)}`,
    );
  } finally {
    globalThis.document = previousDocument;
    globalThis.getComputedStyle = previousGetComputedStyle;
  }
});

  // Named rather than positional: the button toggles both is-stop and is-queue,
  // and which one is applied last is not part of the contract.
  const lastStopToggle = (entries) => entries.filter((entry) => entry.name === "is-stop").at(-1);

test("Composer makes an active run a compact, one-click stop action", async () => {
  const previousDocument = globalThis.document;
  const previousGetComputedStyle = globalThis.getComputedStyle;
  const attributes = new Map();
  const clickHandlers = [];
  const stopClasses = [];
  const input = {
    value: "",
    disabled: false,
    scrollHeight: 46,
    style: {},
    classList: { toggle() {} },
    focus() {},
  };
  const sendButton = {
    textContent: "Send",
    title: "Send",
    disabled: false,
    dataset: { mobileLabel: "↑" },
    classList: { toggle(name, enabled) { stopClasses.push({ name, enabled }); } },
    setAttribute(name, value) { attributes.set(name, value); },
    removeAttribute(name) { attributes.delete(name); },
    addEventListener(type, handler, options) { clickHandlers.push({ type, handler, options }); },
  };
  const elements = {
    messageText: input,
    sendMessageBtn: sendButton,
    attachFileBtn: { disabled: false },
    attachFileInput: { disabled: false },
  };
  const state = {
    agent: { id: "agent-stop", model: "openai:model", status: "running" },
    navigationSelectionKind: "conversation",
    messageSendingByAgent: { "agent-stop": true },
    pendingToolApprovals: {},
    liveToolOutputs: {},
    pendingAttachments: [],
    promptHistory: [],
    serverSkills: [],
  };
  const requests = [];
  const toasts = [];
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  globalThis.getComputedStyle = () => ({ minHeight: "46px", maxHeight: "128px", getPropertyValue() { return ""; } });
  try {
    const controller = createChatComposerController({
      state,
      currentSkillsPreferences: () => ({ commands: [] }),
      request: async (path, options) => {
        requests.push({ path, options });
        return { interrupted: true };
      },
      showToast: (message, tone) => toasts.push({ message, tone }),
    });

    // The initial send lock must clear before the stop label is installed.
    controller.syncMessageComposerBusy();
    assert.equal(sendButton.disabled, true);
    assert.equal(attributes.get("aria-busy"), "true");
    state.messageSendingByAgent = {};
    controller.syncMessageComposerBusy();

    assert.equal(sendButton.disabled, false);
    assert.equal(attributes.has("aria-busy"), false);
    // The test suite runs in Simplified Chinese, while the original DOM label
    // is deliberately English; this also proves setButtonBusy did not restore
    // that stale pre-send label over the localized stop action.
    assert.notEqual(sendButton.textContent, "Send");
    const stopLabel = sendButton.textContent;
    assert.equal(sendButton.dataset.mobileLabel, "■");
    assert.equal(attributes.get("aria-label"), sendButton.title);
    assert.equal(lastStopToggle(stopClasses).enabled, true);
    assert.equal(stopClasses.filter((entry) => entry.name === "is-queue").at(-1).enabled, false);
    assert.equal(clickHandlers.length, 1);
    assert.equal(clickHandlers[0].type, "click");
    assert.equal(clickHandlers[0].options, true);

    let prevented = 0;
    let stopped = 0;
    clickHandlers[0].handler({
      preventDefault() { prevented += 1; },
      stopPropagation() { stopped += 1; },
    });
    await Promise.resolve();
    await Promise.resolve();

    assert.equal(prevented, 1);
    assert.equal(stopped, 1);
    assert.deepEqual(requests, [{
      path: "/api/agents/agent-stop/interrupt",
      options: { method: "POST" },
    }]);
    assert.equal(toasts.at(-1).tone, "info");

    state.agent.status = "idle";
    controller.syncMessageComposerBusy();
    assert.notEqual(sendButton.textContent, stopLabel);
    assert.equal(sendButton.dataset.mobileLabel, "↑");
    assert.equal(attributes.get("aria-label").includes(sendButton.title), true);
    assert.equal(lastStopToggle(stopClasses).enabled, false);
    assert.equal(clickHandlers.length, 1);

    // Terminal live rows stay visible until the run summary arrives, but they
    // are display-only and must not keep the composer in Stop mode.
    state.liveToolOutputs = { "tool-finished": { status: "completed" } };
    controller.syncMessageComposerBusy();
    assert.equal(sendButton.dataset.mobileLabel, "↑");
    assert.equal(lastStopToggle(stopClasses).enabled, false);
    state.liveToolOutputs = { "tool-running": { status: "running" } };
    controller.syncMessageComposerBusy();
    assert.equal(sendButton.dataset.mobileLabel, "■");
    assert.equal(lastStopToggle(stopClasses).enabled, true);
  } finally {
    globalThis.document = previousDocument;
    globalThis.getComputedStyle = previousGetComputedStyle;
  }
});

test("an interrupted run turns an empty submit into Continue, not a rerun", async () => {
  const previousDocument = globalThis.document;
  const previousGetComputedStyle = globalThis.getComputedStyle;
  const attributes = new Map();
  const input = {
    value: "",
    disabled: false,
    scrollHeight: 46,
    style: {},
    classList: { toggle() {} },
    focus() {},
  };
  const sendButton = {
    textContent: "Send",
    title: "Send",
    disabled: false,
    dataset: { mobileLabel: "↑" },
    classList: { toggle() {} },
    setAttribute(name, value) { attributes.set(name, value); },
    removeAttribute(name) { attributes.delete(name); },
    addEventListener() {},
  };
  const elements = {
    messageText: input,
    sendMessageBtn: sendButton,
    attachFileBtn: { disabled: false },
    attachFileInput: { disabled: false },
  };
  const state = {
    agent: { id: "agent-continue", model: "openai:model", status: "interrupted" },
    navigationSelectionKind: "conversation",
    activeRunSummary: { run: { status: "interrupted" } },
    messageSendingByAgent: {},
    pendingToolApprovals: {},
    liveToolOutputs: {},
    pendingAttachments: [],
    currentMessages: [{ id: "user-1", role: "user", contentText: "ship the feature" }],
    promptHistory: [],
    chatDrafts: {},
    serverSkills: [],
  };
  const requests = [];
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  globalThis.getComputedStyle = () => ({ minHeight: "46px", maxHeight: "128px", getPropertyValue() { return ""; } });
  try {
    const controller = createChatComposerController({
      state,
      currentSkillsPreferences: () => ({ commands: [] }),
      isCurrentModelConfigured: () => true,
      loadMessages: async () => {},
      onMessageAccepted: async () => {},
      scheduleMessageRefresh() {},
      request: async (path, options) => {
        requests.push({ path, options });
        return { id: "msg-continue" };
      },
    });

    controller.syncMessageComposerBusy();
    assert.equal(sendButton.textContent, t("workspace.chat.continueRun"));
    assert.equal(sendButton.dataset.mobileLabel, "↑");
    assert.equal(attributes.get("aria-label"), t("workspace.chat.continueRun"));

    await controller.sendMessage({ preventDefault() {} });
    assert.equal(requests.length, 1);
    assert.equal(requests[0].path, "/api/agents/agent-continue/messages");
    assert.deepEqual(JSON.parse(requests[0].options.body), {
      text: t("workspace.chat.continuePrompt"),
      mode: "execute",
      context: "conversation",
    });
    assert.deepEqual(state.promptHistory, []);
    assert.equal(input.value, "");

    input.value = "finish the remaining tests";
    requests.length = 0;
    controller.syncMessageComposerBusy();
    assert.equal(sendButton.textContent, t("chat.send"));
    await controller.sendMessage({ preventDefault() {} });
    assert.equal(JSON.parse(requests[0].options.body).text, "finish the remaining tests");
  } finally {
    globalThis.document = previousDocument;
    globalThis.getComputedStyle = previousGetComputedStyle;
  }
});

test("a failed run still reruns the last user message on empty submit", async () => {
  const previousDocument = globalThis.document;
  const previousGetComputedStyle = globalThis.getComputedStyle;
  const input = {
    value: "",
    disabled: false,
    scrollHeight: 46,
    style: {},
    classList: { toggle() {} },
    focus() {},
  };
  const sendButton = {
    textContent: "Send",
    disabled: false,
    dataset: {},
    classList: { toggle() {} },
    setAttribute() {},
    removeAttribute() {},
    addEventListener() {},
  };
  const elements = {
    messageText: input,
    sendMessageBtn: sendButton,
    attachFileBtn: { disabled: false },
    attachFileInput: { disabled: false },
  };
  const state = {
    agent: { id: "agent-retry", model: "openai:model", status: "error" },
    navigationSelectionKind: "conversation",
    activeRunSummary: { run: { status: "failed" } },
    messageSendingByAgent: {},
    pendingToolApprovals: {},
    liveToolOutputs: {},
    pendingAttachments: [],
    currentMessages: [{ id: "user-1", role: "user", contentText: "ship the feature" }],
    promptHistory: [],
    serverSkills: [],
  };
  const requests = [];
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  globalThis.getComputedStyle = () => ({ minHeight: "46px", maxHeight: "128px", getPropertyValue() { return ""; } });
  try {
    const controller = createChatComposerController({
      state,
      currentSkillsPreferences: () => ({ commands: [] }),
      isCurrentModelConfigured: () => true,
      loadMessages: async () => {},
      scheduleMessageRefresh() {},
      request: async (path, options) => {
        requests.push({ path, options });
        return { id: "run-retry" };
      },
    });

    controller.syncMessageComposerBusy();
    assert.equal(sendButton.textContent, t("workspace.chat.retryRun"));
    await controller.sendMessage({ preventDefault() {} });
    assert.equal(requests.length, 1);
    assert.equal(requests[0].path, "/api/agents/agent-retry/messages/user-1/rerun");
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

// Parking a message paints it immediately and persists it in the background, so
// the composer never waits on the network to show the queue. Tests have to let
// that background call settle before asserting on it.
const settleQueue = () => new Promise((resolve) => setTimeout(resolve, 0));

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
  const posted = [];
  const queued = [];
  let serverQueue = [];
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
      request: async (url, options = {}) => {
        const method = String(options.method || "GET").toUpperCase();
        if (url.endsWith("/queue") && method === "GET") return { queue: serverQueue };
        if (url.endsWith("/queue") && method === "POST") {
          const text = JSON.parse(options.body).text;
          queued.push(text);
          serverQueue = [...serverQueue, { id: `s${queued.length}`, text }];
          return { id: `s${queued.length}`, text };
        }
        if (url.includes("/queue/")) return null;
        posted.push(JSON.parse(options.body).text);
        return { id: `m${posted.length}` };
      },
    });

    await controller.sendMessage({ preventDefault() {} });
    await settleQueue();
    assert.deepEqual(posted, [], "a queued message must never be posted to /messages by the client");
    assert.deepEqual(queued, ["second thing"], "parking a message stores it on the server");
    assert.deepEqual(state.messageQueue.map((item) => item.text), ["second thing"]);

    // With something already parked, a plain send joins the back of the line
    // rather than overtaking it.
    input.value = "third thing";
    await controller.sendMessage({ preventDefault() {} });
    await settleQueue();
    assert.deepEqual(posted, []);
    assert.deepEqual(queued, ["second thing", "third thing"]);
    assert.deepEqual(state.messageQueue.map((item) => item.text), ["second thing", "third thing"]);

    // Draining reconciles against the server rather than sending: the backend
    // owns delivery, which is what lets a queue drain with no browser open.
    serverQueue = [{ id: "s2", text: "third thing" }];
    await controller.drainMessageQueue();
    assert.deepEqual(posted, [], "the client must not send on drain");
    assert.deepEqual(state.messageQueue.map((item) => item.text), ["third thing"],
      "a message the backend already sent drops out of the local view");

    serverQueue = [];
    await controller.drainMessageQueue();
    assert.deepEqual(state.messageQueue, []);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("sending while the turn is in flight parks the message instead of posting it", async () => {
  const queueHost = { className: "", innerHTML: "", classList: { toggle() {} }, querySelectorAll: () => [], querySelector: () => null };
  const input = { value: "follow-up while running", style: {}, focus() {}, classList: { toggle() {} } };
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
  const posted = [];
  const queued = [];
  let serverQueue = [];
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
      request: async (url, options = {}) => {
        const method = String(options.method || "GET").toUpperCase();
        if (url.endsWith("/queue") && method === "GET") return { queue: serverQueue };
        if (url.endsWith("/queue") && method === "POST") {
          const text = JSON.parse(options.body).text;
          queued.push(text);
          serverQueue = [...serverQueue, { id: `s${queued.length}`, text }];
          return { id: `s${queued.length}`, text };
        }
        if (url.includes("/queue/")) return null;
        posted.push(JSON.parse(options.body).text);
        return { id: `m${posted.length}` };
      },
    });

    // No /queue prefix and nothing already parked: the running turn alone is
    // what makes this a follow-up rather than an immediate send.
    await controller.sendMessage({ preventDefault() {} });
    await settleQueue();
    assert.deepEqual(posted, [], "a message typed during a run must not be posted");
    assert.deepEqual(queued, ["follow-up while running"], "it is parked on the server instead");
    assert.deepEqual(state.messageQueue.map((item) => item.text), ["follow-up while running"]);
    assert.equal(input.value, "", "the composer clears once the message is parked");

    // Idle again: the same send goes straight out.
    state.agent.status = "idle";
    state.messageQueue = [];
    serverQueue = [];
    input.value = "immediate";
    await controller.sendMessage({ preventDefault() {} });
    assert.deepEqual(posted, ["immediate"]);
    assert.deepEqual(state.messageQueue, []);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("a long queue collapses past the threshold and the toggle expands it", () => {
  let toggleHandler = null;
  const queueHost = {
    className: "",
    innerHTML: "",
    classList: { toggle() {} },
    querySelectorAll: () => [],
    querySelector(selector) {
      if (selector !== "[data-queue-toggle]") return null;
      return { addEventListener(event, handler) { toggleHandler = handler; } };
    },
  };
  const elements = { messageQueue: queueHost, messageText: { value: "", style: {}, classList: { toggle() {} } }, pendingAttachments: null };
  const queue = Array.from({ length: queueCollapseThreshold + 4 }, (unused, index) => ({
    id: `q${index}`,
    agentId: "agent-1",
    text: `message ${index}`,
    mode: "execute",
    context: "conversation",
  }));
  const state = {
    agent: { id: "agent-1", status: "running", model: "m" },
    navigationSelectionKind: "conversation",
    messageQueue: queue,
    pendingToolApprovals: {},
    liveToolOutputs: {},
    pendingAttachments: [],
    chatDrafts: {},
    promptHistory: [],
  };
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
      request: async () => ({ id: "m1" }),
    });

    controller.renderMessageQueue();
    const collapsedRows = (queueHost.innerHTML.match(/message-queue-item/g) || []).length;
    assert.equal(collapsedRows, queueCollapseThreshold, "only the threshold rows render while collapsed");
    assert.match(queueHost.innerHTML, /data-queue-toggle aria-expanded="false"/);
    // No heading row: the order and the text carry the list on their own.
    assert.doesNotMatch(queueHost.innerHTML, /message-queue-head/);

    assert.equal(typeof toggleHandler, "function", "the toggle must be bound");
    toggleHandler();
    const expandedRows = (queueHost.innerHTML.match(/message-queue-item/g) || []).length;
    assert.equal(expandedRows, queue.length, "expanding shows every parked message");
    assert.match(queueHost.innerHTML, /data-queue-toggle aria-expanded="true"/);
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
    { id: "a", agentId: "agent-1", text: "keep", mode: "plan", context: "project", attachments: [] },
    { id: "d", agentId: "agent-1", text: "defaults", mode: "execute", context: "conversation", attachments: [] },
  ]);
  // Blank text is only rejected when nothing rides along with it: an image on
  // its own is a message the immediate send path already accepts.
  assert.deepEqual(normalizeMessageQueue([
    { id: "i", agentId: "agent-1", text: "   ", attachments: [{ id: "att-1", filename: "shot.png", kind: "image", mimeType: "image/png", sizeBytes: 2048 }] },
  ]), [
    { id: "i", agentId: "agent-1", text: "   ", mode: "execute", context: "conversation", attachments: [{ id: "att-1", filename: "shot.png", kind: "image", mimeType: "image/png", sizeBytes: 2048 }] },
  ]);
  // Nameless entries carry nothing a row could label, so they are dropped rather
  // than rendered as an empty chip.
  assert.deepEqual(normalizeMessageQueue([
    { id: "j", agentId: "agent-1", text: "   ", attachments: [{ filename: "   " }] },
  ]), []);
  const flood = Array.from({ length: 40 }, (unused, index) => ({ id: `q${index}`, agentId: "agent-1", text: `m${index}` }));
  assert.equal(normalizeMessageQueue(flood).length, maxQueuedMessages);
});


test("typing does not scroll the transcript", () => {
  // Growing the composer covers a little more of the transcript; it is not a
  // request to move it. A previous attempt kept the tail pinned across the
  // resize, which scrolled the conversation upward on every new line of a
  // draft -- exactly the movement it was meant to prevent. The send re-anchors;
  // typing must not.
  const transcript = { scrollTop: 200, scrollHeight: 800, clientHeight: 600 };
  const input = {
    value: ["one", "two", "three", "four"].join("\n"),
    style: {},
    scrollHeight: 95,
    classList: { toggle() {} },
    focus() {},
  };
  const elements = { messageText: input, messages: transcript };
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => elements[id] || null };
  try {
    const controller = createChatComposerController({
      state: {
        agent: { id: "agent-1", status: "idle" },
        navigationSelectionKind: "conversation",
        pendingToolApprovals: {},
        liveToolOutputs: {},
        pendingAttachments: [],
        chatDrafts: {},
        promptHistory: [],
        messageQueue: [],
      },
      isCurrentModelConfigured: () => true,
      loadMessages: async () => {},
      scheduleMessageRefresh() {},
      notifyTerminal() {},
      showToast() {},
      request: async () => ({}),
    });

    // The composer eats 59px of the viewport as the draft grows.
    transcript.clientHeight = 541;
    controller.autoResizeMessageInput();

    assert.equal(transcript.scrollTop, 200, "the transcript must stay exactly where the reader left it");
  } finally {
    globalThis.document = previousDocument;
  }
});

// scheduleQueueDrain existed but nothing ever called it, so the interval never
// started: the backend sent a parked message and deleted its row, while the
// browser kept rendering the entry it had already mirrored. Rendering a
// non-empty queue is what arms it now.
test("a parked message starts the drain that clears rows the backend already sent", async () => {
  const queueHost = { className: "", innerHTML: "", classList: { toggle() {} }, querySelectorAll: () => [], querySelector: () => null };
  const input = { value: "park me", style: {}, focus() {}, classList: { toggle() {} } };
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
  let serverQueue = [];
  const intervals = [];
  const previousDocument = globalThis.document;
  const previousSetInterval = globalThis.setInterval;
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  globalThis.setInterval = (handler, delay) => {
    intervals.push({ handler, delay });
    return intervals.length;
  };
  try {
    const controller = createChatComposerController({
      state,
      isCurrentModelConfigured: () => true,
      loadMessages: async () => {},
      scheduleMessageRefresh() {},
      notifyTerminal() {},
      showToast() {},
      onMessageAccepted: async () => {},
      request: async (url, options = {}) => {
        const method = String(options.method || "GET").toUpperCase();
        if (url.endsWith("/queue") && method === "GET") return { queue: serverQueue };
        if (url.endsWith("/queue") && method === "POST") {
          const text = JSON.parse(options.body).text;
          serverQueue = [...serverQueue, { id: "s1", text }];
          return { id: "s1", text };
        }
        return null;
      },
    });

    await controller.sendMessage({ preventDefault() {} });
    await settleQueue();
    assert.equal(state.messageQueue.length, 1, "the message is parked");
    assert.equal(intervals.length, 1, "parking arms the reconcile timer");

    // The backend sent it and dropped the row, so the tick must clear the mirror.
    serverQueue = [];
    await intervals[0].handler();
    await settleQueue();
    assert.deepEqual(state.messageQueue, [], "a sent message stops being rendered");
  } finally {
    globalThis.document = previousDocument;
    globalThis.setInterval = previousSetInterval;
  }
});

test("parking carries attachments and hands the staged files over", async () => {
  const queueHost = { className: "", innerHTML: "", classList: { toggle() {} }, querySelectorAll: () => [], querySelector: () => null };
  const input = { value: "look at this", style: {}, focus() {}, classList: { toggle() {} } };
  const attachmentsHost = { className: "", innerHTML: "", classList: { toggle() {} }, querySelectorAll: () => [], querySelector: () => null };
  const elements = { messageQueue: queueHost, messageText: input, pendingAttachments: attachmentsHost };
  const file = new File(["binary"], "shot.png", { type: "image/png" });
  const state = {
    agent: { id: "agent-1", status: "running", model: "m" },
    navigationSelectionKind: "conversation",
    messageQueue: [],
    pendingToolApprovals: {},
    liveToolOutputs: {},
    pendingAttachments: [{ id: "local-1", file, kind: "image" }],
    chatDrafts: {},
    promptHistory: [],
  };
  let serverQueue = [];
  const bodies = [];
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
      request: async (url, options = {}) => {
        const method = String(options.method || "GET").toUpperCase();
        if (url.endsWith("/queue") && method === "GET") return { queue: serverQueue };
        if (url.endsWith("/queue") && method === "POST") {
          bodies.push(options.body);
          serverQueue = [{
            id: "s1",
            text: "look at this",
            attachments: [{ id: "att-1", filename: "shot.png", kind: "image", mimeType: "image/png", sizeBytes: 6 }],
          }];
          return serverQueue[0];
        }
        return null;
      },
    });

    await controller.sendMessage({ preventDefault() {} });
    await settleQueue();

    assert.equal(bodies.length, 1, "the park is posted once");
    assert.ok(bodies[0] instanceof FormData, "files have to go up as multipart");
    assert.equal(bodies[0].get("text"), "look at this");
    assert.equal(bodies[0].getAll("files").length, 1, "the staged file rides along");
    assert.deepEqual(state.pendingAttachments, [], "parking takes ownership of the staged files");
    assert.deepEqual(state.messageQueue[0].attachments, [
      { id: "att-1", filename: "shot.png", kind: "image", mimeType: "image/png", sizeBytes: 6 },
    ], "the server's view of the parked attachment replaces the optimistic one");
    assert.match(queueHost.innerHTML, /message-queue-attachment/);
    assert.match(queueHost.innerHTML, /shot\.png/);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("an image parked with no text is accepted and labelled", () => {
  const queueHost = { className: "", innerHTML: "", classList: { toggle() {} }, querySelectorAll: () => [], querySelector: () => null };
  const elements = { messageQueue: queueHost, messageText: null, pendingAttachments: null };
  const state = {
    agent: { id: "agent-1", status: "running", model: "m" },
    navigationSelectionKind: "conversation",
    messageQueue: [{
      id: "s1",
      agentId: "agent-1",
      text: "",
      mode: "execute",
      context: "conversation",
      attachments: [{ id: "att-1", filename: "diagram.pdf", kind: "file", mimeType: "application/pdf", sizeBytes: 12 }],
    }],
    pendingToolApprovals: {},
    liveToolOutputs: {},
    pendingAttachments: [],
    chatDrafts: {},
    promptHistory: [],
  };
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
      request: async () => ({ queue: [] }),
    });
    controller.renderMessageQueue();
    assert.match(queueHost.innerHTML, /message-queue-attachments-only/, "an attachment-only row says so");
    assert.match(queueHost.innerHTML, /diagram\.pdf/);
  } finally {
    globalThis.document = previousDocument;
  }
});
