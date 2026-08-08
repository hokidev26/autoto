// Stateless helpers for the chat composer: draft parsing and truncation, queue and
// attachment normalisation, reasoning-effort and message-mode coercion, slash-command
// assembly, and input sizing.
//
// These were split out of chat-composer.mjs, which had grown past the size budget in
// scripts/check.sh. The seam is deliberate rather than arbitrary: nothing here reads
// or writes controller state, so all of it is directly testable, while everything left
// behind is bound to a live composer instance. chat-composer.mjs re-exports the whole
// surface, so existing importers do not need to know this file exists.
import { t } from "./i18n.mjs?v=goal-command-2-queue-command-1-reasoning-steps-1-reasoning-history-1-markdown-2";
import { mergeAuthoritativeEffectiveCommands, mergeBuiltInSlashCommands, normalizeSlashCommandName } from "./skills-commands.mjs?v=goal-command-2-queue-command-1-reasoning-steps-1-reasoning-history-1-markdown-2";

export const defaultReasoningEffortValues = Object.freeze(["auto", "low", "medium", "high"]);
// Ordered weakest to strongest. "xhigh", "max" and "ultra" are Codex levels
// and are offered per model, from the catalog, not provider-wide.
export const knownReasoningEffortValues = Object.freeze([...defaultReasoningEffortValues, "xhigh", "max", "ultra"]);

function normalizedReasoningEffortList(values = defaultReasoningEffortValues) {
  const source = Array.isArray(values) ? values : defaultReasoningEffortValues;
  const normalized = source
    .map((value) => String(value || "").trim().toLowerCase())
    .filter((value) => knownReasoningEffortValues.includes(value));
  return ["auto", ...normalized.filter((value, index) => value !== "auto" && normalized.indexOf(value) === index)];
}

export function normalizeReasoningEffort(value, supportedValues = defaultReasoningEffortValues) {
  const effort = String(value || "").trim().toLowerCase();
  const normalized = effort === "" || effort === "default" || effort === "inherit" ? "auto" : effort;
  return normalizedReasoningEffortList(supportedValues).includes(normalized) ? normalized : "auto";
}

export function reasoningEffortValuesForCapabilities(capabilities = {}) {
  const source = capabilities && typeof capabilities === "object" ? capabilities : {};
  const explicit = [
    source.reasoningEfforts,
    source.reasoningEffortValues,
    source.effortValues,
    Array.isArray(source.reasoningEffort) ? source.reasoningEffort : undefined,
    source.reasoningEffort?.values,
    source.reasoningEffort?.supportedValues,
  ].find(Array.isArray);
  if (explicit) return normalizedReasoningEffortList(explicit);
  return source.reasoningEffort === true ? [...defaultReasoningEffortValues] : ["auto"];
}

// Per-model levels win over the provider list: Codex serves "max" and "ultra"
// on some models only, and the authenticated catalog reports the exact set.
export function reasoningEffortValuesForModel(provider, modelValue) {
  const value = String(modelValue || "").trim();
  const separator = value.indexOf(":");
  const model = separator >= 0 ? value.slice(separator + 1).trim() : value;
  const modelCapabilities = provider?.modelCapabilities && typeof provider.modelCapabilities === "object"
    ? provider.modelCapabilities[model]
    : null;
  const hasModelReasoningCapabilities = Boolean(modelCapabilities && [
    "reasoningEffort", "reasoningEfforts", "reasoningEffortValues", "effortValues",
  ].some((key) => Object.hasOwn(modelCapabilities, key)));
  if (hasModelReasoningCapabilities) return reasoningEffortValuesForCapabilities(modelCapabilities);
  return reasoningEffortValuesForCapabilities(provider?.capabilities || {});
}

export function fastModeSupportedForModel(provider, modelValue) {
  const value = String(modelValue || "").trim();
  const separator = value.indexOf(":");
  const model = separator >= 0 ? value.slice(separator + 1).trim() : value;
  const modelCapabilities = provider?.modelCapabilities && typeof provider.modelCapabilities === "object"
    ? provider.modelCapabilities
    : {};
  return Boolean(model && modelCapabilities[model]?.fastMode === true);
}

export function normalizeMessageMode(value, fallback = "execute") {
  const mode = String(value || "").trim().toLowerCase();
  if (mode === "plan" || mode === "execute") return mode;
  return fallback === "plan" ? "plan" : "execute";
}

export function calculateMessageInputSize({ scrollHeight, minHeight = 0, maxHeight = 180 } = {}) {
  const minimum = Math.max(0, Number(minHeight) || 0);
  const maximum = Math.max(minimum, Number(maxHeight) || 180);
  const contentHeight = Math.max(minimum, Number(scrollHeight) || 0);
  return {
    height: Math.min(contentHeight, maximum),
    scrollable: contentHeight > maximum,
  };
}

function cssPixelValue(value, fallback) {
  const parsed = Number.parseFloat(String(value || ""));
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback;
}

export function resizeMessageInputElement(input, computedStyle = globalThis.getComputedStyle?.(input)) {
  if (!input) return { height: 0, scrollable: false };
  input.style.height = "auto";
  const minHeight = cssPixelValue(
    computedStyle?.getPropertyValue?.("--composer-input-min-height") || computedStyle?.minHeight,
    0,
  );
  const maxHeight = cssPixelValue(
    computedStyle?.getPropertyValue?.("--composer-input-max-height") || computedStyle?.maxHeight,
    180,
  );
  // An empty composer always rests at the minimum height. Measuring
  // scrollHeight has been observed to yield the maximum in some layout states,
  // and because nothing recomputes the value afterwards the input stays several
  // lines tall while holding no text. Skipping the measurement when there is
  // demonstrably nothing to measure removes that failure mode at the source.
  const empty = typeof input.value === "string" && input.value.length === 0;
  const size = empty
    ? { height: minHeight, scrollable: false }
    : calculateMessageInputSize({ scrollHeight: input.scrollHeight, minHeight, maxHeight });
  input.style.height = `${size.height}px`;
  input.style.overflowY = size.scrollable ? "auto" : "hidden";
  input.classList?.toggle("message-input-scrollable", size.scrollable);
  return size;
}

export function slashCommandsForEffectivePolicy(policy, localTemplates) {
  return mergeAuthoritativeEffectiveCommands(policy, localTemplates);
}

export function builtInSlashCommandsForContext(context, translate = t) {
  // /queue is not tied to a project: parking a follow-up while the current turn
  // finishes is just as useful in a plain conversation.
  const commands = [{
    id: "builtin-queue",
    name: "/queue",
    description: translate("workspace.chat.queueCommandDescription"),
    prompt: "",
    source: "builtin",
  }];
  if (String(context || "").trim().toLowerCase() !== "project") return commands;
  return [{
    id: "builtin-goal",
    name: "/goal",
    description: translate("workspace.chat.goalCommandDescription"),
    prompt: "",
    source: "builtin",
  }, ...commands];
}

export function slashCommandsForContext(context, commands, translate = t) {
  const externalCommands = (Array.isArray(commands) ? commands : [])
    .filter((command) => !["/goal", "/queue"].includes(normalizeSlashCommandName(command?.name)));
  return mergeBuiltInSlashCommands(builtInSlashCommandsForContext(context, translate), externalCommands);
}

export function parseGoalCommandDraft(value = "") {
  const commandText = String(value || "").trim();
  if (commandText !== "/goal" && !commandText.startsWith("/goal ")) return null;
  return {
    commandText,
    goalText: commandText.slice("/goal".length).trim(),
  };
}

export function isGoalCommandDraft(goalCommand) {
  return Boolean(goalCommand);
}

export const maxQueuedMessages = 20;

// Above this many parked messages the list collapses. The composer sits at the
// bottom of the window, so an uncapped list grows upward over the transcript.
export const queueCollapseThreshold = 3;

// Queued messages survive a reload, so they arrive from storage as untrusted
// JSON: everything is re-typed and bounded here rather than at each read site.
// Parked attachments arrive as metadata only: the bytes stay on the server with
// the queue row so the backend can send them with no browser open.
export function normalizeQueuedAttachments(value) {
  if (!Array.isArray(value)) return [];
  const attachments = [];
  for (const entry of value) {
    if (!entry || typeof entry !== "object") continue;
    const filename = String(entry.filename || entry.name || "").trim();
    if (!filename) continue;
    attachments.push({
      id: String(entry.id || "").trim(),
      filename,
      kind: entry.kind === "image" ? "image" : "file",
      mimeType: String(entry.mimeType || entry.mime_type || "").trim(),
      sizeBytes: Number.isFinite(Number(entry.sizeBytes ?? entry.size_bytes)) ? Number(entry.sizeBytes ?? entry.size_bytes) : 0,
    });
  }
  return attachments;
}

export function normalizeMessageQueue(value) {
  if (!Array.isArray(value)) return [];
  const seen = new Set();
  const queue = [];
  for (const item of value) {
    if (!item || typeof item !== "object") continue;
    const id = String(item.id || "").trim();
    const agentId = String(item.agentId || "").trim();
    const text = String(item.text || "");
    const attachments = normalizeQueuedAttachments(item.attachments);
    // An attachment-only park is a real message, so empty text is accepted as
    // long as something is riding along with it.
    if (!id || !agentId || (!text.trim() && !attachments.length) || seen.has(id)) continue;
    if (text.length > maxChatDraftCharacters) continue;
    seen.add(id);
    queue.push({
      id,
      agentId,
      text,
      mode: item.mode === "plan" ? "plan" : "execute",
      context: item.context === "project" ? "project" : "conversation",
      attachments,
    });
    if (queue.length >= maxQueuedMessages) break;
  }
  return queue;
}

export function parseQueueCommandDraft(value = "") {
  const commandText = String(value || "").trim();
  if (commandText !== "/queue" && !commandText.startsWith("/queue ")) return null;
  return {
    commandText,
    queuedText: commandText.slice("/queue".length).trim(),
  };
}

export const maxChatDraftCharacters = 8000;

export function interfaceLocale(documentRef = globalThis.document, navigatorRef = globalThis.navigator) {
  return documentRef?.documentElement?.lang || navigatorRef?.language || "zh-CN";
}

export function unicodeCharacters(value = "") {
  return Array.from(String(value || ""));
}

export function truncateChatDraft(value = "", max = maxChatDraftCharacters) {
  const characters = unicodeCharacters(value);
  return {
    text: characters.slice(0, Math.max(0, max)).join(""),
    length: characters.length,
    truncated: characters.length > max,
  };
}

export function mentionTrigger(value = "", cursor = String(value || "").length) {
  const text = String(value || "").slice(0, Math.max(0, cursor));
  const match = text.match(/(?:^|\s)@([^\s@]{0,64})$/u);
  if (!match) return null;
  const query = match[1] || "";
  return { query, start: text.length - query.length - 1, end: text.length };
}

export function clipboardFiles(event) {
  const files = Array.from(event?.clipboardData?.files || []).filter(Boolean);
  if (files.length) return files;
  return Array.from(event?.clipboardData?.items || [])
    .filter((item) => item?.kind === "file")
    .map((item) => item.getAsFile?.())
    .filter(Boolean);
}

export function normalizeChatDrafts(value = {}) {
  const source = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  return Object.entries(source).reduce((acc, [key, draft]) => {
    const id = String(key || "").trim().slice(0, 120);
    const { text } = truncateChatDraft(draft);
    if (id && text.trim()) acc[id] = text;
    return acc;
  }, {});
}

export function normalizePromptHistory(value = []) {
  const seen = new Set();
  return (Array.isArray(value) ? value : [])
    .map((item) => String(item || "").trim())
    .filter(Boolean)
    .filter((item) => {
      const key = item.toLowerCase();
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    })
    .map((item) => item.slice(0, 4000))
    .slice(0, 30);
}

