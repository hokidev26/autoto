import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { api } from "./runtime.mjs";
import { t } from "./i18n.mjs";
import { mergeSlashCommands, slashCommandInsertion } from "./skills-commands.mjs";

// The composer's @mention palette and /slash-command palette, including their
// keyboard navigation. Split out of chat-composer.mjs to keep that file
// inside the source size budget. State is shared by reference with the
// controller, so the caller passes its own `state` object in.
export function createComposerPalettes({
  currentSkillsPreferences,
  getEffectiveSkillsPolicy,
  handleMessageInput,
  isComposingInput,
  mentionTrigger,
  resetPromptHistoryNavigation,
  setMessageInputValue,
  showToast,
  slashCommandsForContext,
  slashCommandsForEffectivePolicy,
  state,
} = {}) {
  function hideMentionPalette() {
    state.mentionOpen = false;
    state.mentionUsers = [];
    state.mentionIndex = 0;
    const palette = $("mentionPalette");
    if (palette) {
      palette.classList.add("hidden");
      palette.innerHTML = "";
    }
  }

  function insertMention(user) {
    const input = $("messageText");
    const trigger = mentionTrigger(input?.value || "", input?.selectionStart || 0);
    if (!input || !trigger || !user?.handle) return false;
    input.setRangeText(`@${user.handle} `, trigger.start, trigger.end, "end");
    hideMentionPalette();
    handleMessageInput();
    input.focus();
    return true;
  }

  function renderMentionPalette() {
    const palette = $("mentionPalette");
    if (!palette) return;
    const users = Array.isArray(state.mentionUsers) ? state.mentionUsers : [];
    if (!state.mentionOpen || !users.length) {
      hideMentionPalette();
      return;
    }
    state.mentionIndex = Math.max(0, Math.min(Number(state.mentionIndex || 0), users.length - 1));
    palette.classList.remove("hidden");
    palette.innerHTML = users.map((user, index) => `
      <button class="slash-command-item ${index === state.mentionIndex ? "active" : ""}" type="button" data-mention-user="${escapeAttr(user.id || user.handle)}">
        <span class="slash-command-name">@${escapeHtml(user.handle || "")}</span>
        <span class="slash-command-desc">${escapeHtml(user.role || "user")}</span>
      </button>
    `).join("");
    palette.querySelectorAll("[data-mention-user]").forEach((button, index) => {
      button.addEventListener("mousedown", (event) => event.preventDefault());
      button.addEventListener("click", () => insertMention(users[index]));
    });
  }

  async function updateMentionPalette() {
    if (state.mentionComposing) return;
    const input = $("messageText");
    const trigger = mentionTrigger(input?.value || "", input?.selectionStart || 0);
    if (!trigger) {
      hideMentionPalette();
      return;
    }
    const seq = Number(state.mentionSeq || 0) + 1;
    state.mentionSeq = seq;
    try {
      const users = await api(`/api/users?handlePrefix=${encodeURIComponent(trigger.query)}&limit=8`);
      if (seq !== state.mentionSeq) return;
      state.mentionUsers = Array.isArray(users) ? users : [];
      state.mentionOpen = state.mentionUsers.length > 0;
      state.mentionIndex = 0;
      renderMentionPalette();
    } catch (error) {
      if (seq === state.mentionSeq && error?.status === 401) hideMentionPalette();
    }
  }

  function handleMentionKeydown(event) {
    if (!state.mentionOpen || state.mentionComposing) return false;
    const users = Array.isArray(state.mentionUsers) ? state.mentionUsers : [];
    if (!users.length) return false;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      state.mentionIndex = event.key === "ArrowDown"
        ? (state.mentionIndex + 1) % users.length
        : (state.mentionIndex - 1 + users.length) % users.length;
      renderMentionPalette();
      event.preventDefault();
      return true;
    }
    if (event.key === "Enter" || event.key === "Tab") {
      if (insertMention(users[state.mentionIndex])) event.preventDefault();
      return true;
    }
    if (event.key === "Escape") {
      hideMentionPalette();
      event.preventDefault();
      return true;
    }
    return false;
  }

  function enabledSlashCommands() {
    const localTemplates = currentSkillsPreferences().commands;
    const skillCommands = typeof getEffectiveSkillsPolicy === "function"
      ? slashCommandsForEffectivePolicy(getEffectiveSkillsPolicy(), localTemplates)
      : mergeSlashCommands(state.serverSkills, localTemplates);
    const context = state.navigationSelectionKind === "project" ? "project" : "conversation";
    return slashCommandsForContext(context, skillCommands);
  }

  function slashCommandTrigger(value) {
    const text = String(value || "");
    const match = text.match(/^\s*\/([^\s]*)$/);
    if (!match) return null;
    return {
      prefix: text.slice(0, match.index || 0),
      query: match[1].toLowerCase(),
    };
  }

  function slashCommandMatches() {
    const input = $("messageText");
    const trigger = slashCommandTrigger(input?.value || "");
    if (!trigger) return [];
    const query = trigger.query;
    return enabledSlashCommands().filter((command) => {
      const haystack = `${command.name} ${command.description}`.toLowerCase();
      return !query || haystack.includes(query);
    }).slice(0, 8);
  }

  function slashCommandOptionId(command, index) {
    return `slash-command-option-${String(command?.id || index).replace(/[^a-zA-Z0-9_-]/g, "-")}`;
  }

  function updateSlashCommandPalette() {
    const palette = $("slashCommandPalette");
    if (!palette) return;
    const input = $("messageText");
    const trigger = slashCommandTrigger(input?.value || "");
    const matches = trigger ? slashCommandMatches() : [];
    state.slashCommandOpen = Boolean(trigger && matches.length);
    state.slashCommandQuery = trigger?.query || "";
    if (!state.slashCommandOpen) {
      state.slashCommandIndex = 0;
      input?.setAttribute("aria-expanded", "false");
      input?.removeAttribute("aria-activedescendant");
      palette.classList.add("hidden");
      palette.innerHTML = "";
      return;
    }
    state.slashCommandIndex = Math.max(0, Math.min(state.slashCommandIndex, matches.length - 1));
    input?.setAttribute("aria-expanded", "true");
    input?.setAttribute("aria-activedescendant", slashCommandOptionId(matches[state.slashCommandIndex], state.slashCommandIndex));
    palette.classList.remove("hidden");
    palette.innerHTML = `
      <div class="slash-command-head">${escapeHtml(t("workspace.chat.slashCommands"))}</div>
      ${matches.map((command, index) => `
        <button id="${escapeAttr(slashCommandOptionId(command, index))}" class="slash-command-item ${index === state.slashCommandIndex ? "active" : ""}" type="button" role="option" aria-selected="${index === state.slashCommandIndex ? "true" : "false"}" data-slash-command="${escapeAttr(command.id)}">
          <span class="slash-command-name">${escapeHtml(command.name)}</span>
          <span class="slash-command-desc">${escapeHtml(command.description || command.prompt.slice(0, 120))}</span>
        </button>
      `).join("")}
    `;
    palette.querySelectorAll("[data-slash-command]").forEach((node) => {
      node.addEventListener("mousedown", (event) => event.preventDefault());
      node.addEventListener("click", () => applySlashCommand(node.dataset.slashCommand));
    });
  }

  function hideSlashCommandPalette() {
    state.slashCommandOpen = false;
    state.slashCommandIndex = 0;
    state.slashCommandQuery = "";
    const input = $("messageText");
    input?.setAttribute("aria-expanded", "false");
    input?.removeAttribute("aria-activedescendant");
    const palette = $("slashCommandPalette");
    if (palette) {
      palette.classList.add("hidden");
      palette.innerHTML = "";
    }
  }

  function applySlashCommand(id) {
    const command = enabledSlashCommands().find((item) => item.id === id) || slashCommandMatches()[state.slashCommandIndex];
    if (!command) return false;
    const input = $("messageText");
    const value = input?.value || "";
    const insertion = slashCommandInsertion(command);
    const next = value.replace(/^\s*\/[^\s]*$/, insertion);
    setMessageInputValue(next);
    hideSlashCommandPalette();
    resetPromptHistoryNavigation();
    input?.focus();
    showToast(t("workspace.chat.slashInserted", { name: command.name }), "success");
    return true;
  }

  function handleSlashCommandKeydown(event) {
    if (!state.slashCommandOpen || isComposingInput(event)) return false;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      const count = slashCommandMatches().length;
      if (!count) return false;
      state.slashCommandIndex = event.key === "ArrowDown"
        ? (state.slashCommandIndex + 1) % count
        : (state.slashCommandIndex - 1 + count) % count;
      updateSlashCommandPalette();
      event.preventDefault();
      return true;
    }
    if (event.key === "Enter" || event.key === "Tab") {
      const selected = slashCommandMatches()[state.slashCommandIndex];
      if (selected && applySlashCommand(selected.id)) {
        event.preventDefault();
        return true;
      }
    }
    if (event.key === "Escape") {
      hideSlashCommandPalette();
      event.preventDefault();
      return true;
    }
    return false;
  }

  return {
    hideMentionPalette,
    insertMention,
    renderMentionPalette,
    updateMentionPalette,
    handleMentionKeydown,
    enabledSlashCommands,
    slashCommandTrigger,
    slashCommandMatches,
    slashCommandOptionId,
    updateSlashCommandPalette,
    hideSlashCommandPalette,
    applySlashCommand,
    handleSlashCommandKeydown,
  };
}
