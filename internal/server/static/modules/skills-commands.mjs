const serverCommandPattern = /^\/[a-z0-9][a-z0-9_-]{0,62}$/;

export function slashCommandInsertion(command) {
  if (command?.source === "server" || command?.source === "builtin") {
    return `${String(command.name || "").trim()} `;
  }
  return String(command?.prompt || "");
}

export function visibleMessageText(message) {
  if (String(message?.role || "").toLowerCase() === "user" && String(message?.commandText || "").trim()) {
    return String(message.commandText);
  }
  return String(message?.contentText || "");
}

export function normalizeSlashCommandName(value) {
  const raw = String(value || "").trim().replace(/^\/+/, "");
  if (!raw) return "";
  return `/${raw}`.toLowerCase();
}

// Application-owned commands are trusted independently of the effective Skill
// policy. They stay first and reserve their normalized names so a server Skill
// or browser-local template cannot shadow an app protocol such as /goal.
export function mergeBuiltInSlashCommands(builtInCommands, commands) {
  const merged = [];
  const seen = new Set();
  for (const command of Array.isArray(builtInCommands) ? builtInCommands : []) {
    const name = normalizeSlashCommandName(command?.name);
    if (!serverCommandPattern.test(name) || seen.has(name)) continue;
    seen.add(name);
    merged.push({
      id: String(command?.id || `builtin-${name.slice(1)}`),
      name,
      description: String(command?.description || "").trim(),
      prompt: "",
      source: "builtin",
    });
  }
  for (const command of Array.isArray(commands) ? commands : []) {
    const name = normalizeSlashCommandName(command?.name);
    if (!name || seen.has(name)) continue;
    seen.add(name);
    merged.push({ ...command, name });
  }
  return merged;
}

function legalServerCommandName(skill) {
  const command = normalizeSlashCommandName(skill?.command || skill?.name);
  return serverCommandPattern.test(command) ? command : "";
}

function commandFromServerSkill(skill, command = legalServerCommandName(skill)) {
  const verdict = String(skill?.scanVerdict || "").trim().toLowerCase();
  const reviewAcknowledged = verdict === "review"
    && String(skill?.riskAcknowledgedAt || "").trim()
    && String(skill?.riskAcknowledgedBy || "").trim()
    && String(skill?.riskAcknowledgedHash || "").trim() === String(skill?.contentHash || "").trim();
  const eligible = verdict === "safe" || Boolean(reviewAcknowledged);
  if (!skill?.enabled || !command || !eligible) return null;
  return {
    id: `server-${String(skill.id || command)}`,
    name: command,
    description: String(skill.description || "").trim(),
    prompt: "",
    source: "server",
  };
}

function commandFromLocalTemplate(command) {
  const name = normalizeSlashCommandName(command?.name);
  const prompt = String(command?.prompt || "").trim();
  if (!command?.enabled || !name || !prompt) return null;
  return {
    id: `local-${String(command.id || name)}`,
    name,
    description: String(command.description || "").trim(),
    prompt,
    source: "local",
  };
}

function commandOwner(item) {
  return item?.owner || item?.effectiveOwner || item?.skill || item;
}

// Every legal server command reserves its normalized name, even while disabled
// or blocked, so a stale browser template cannot bypass a server-side decision.
// Only enabled, safe (or explicitly acknowledged review) records are usable.
export function mergeSlashCommands(serverSkills, localTemplates) {
  const commands = [];
  const seen = new Set();
  for (const rawSkill of Array.isArray(serverSkills) ? serverSkills : []) {
    const skill = commandOwner(rawSkill);
    const name = legalServerCommandName(skill);
    if (!name || seen.has(name)) continue;
    seen.add(name);
    const command = commandFromServerSkill(skill, name);
    if (command) commands.push(command);
  }
  for (const template of Array.isArray(localTemplates) ? localTemplates : []) {
    const command = commandFromLocalTemplate(template);
    if (!command || seen.has(command.name)) continue;
    seen.add(command.name);
    commands.push(command);
  }
  return commands;
}

// The effective endpoint may return either an { items } envelope or a bare
// array. Its owner records are authoritative in exactly the same way as a
// normal server list: unusable records still shadow browser-local templates.
export function effectiveOwnerSkills(response) {
  if (Array.isArray(response)) return response.map(commandOwner).filter(Boolean);
  const items = Array.isArray(response?.items) ? response.items : Array.isArray(response?.skills) ? response.skills : [];
  return items.map(commandOwner).filter(Boolean);
}

export function mergeEffectiveOwnerCommands(effectiveResponse, localTemplates) {
  return mergeSlashCommands(effectiveOwnerSkills(effectiveResponse), localTemplates);
}

// Browser-local templates are allowed only after at least one authoritative
// effective-policy snapshot has loaded. A refresh failure may retain last-known
// data as stale, but the first failure must fail closed.
export function mergeAuthoritativeEffectiveCommands(policy, localTemplates) {
  if (!policy?.hasAuthoritativeData) return [];
  return mergeEffectiveOwnerCommands(policy, localTemplates);
}
