import { t } from "./i18n.mjs";

const settingsIconPaths = Object.freeze({
  profile: `<circle cx="12" cy="8" r="3.25"/><path d="M5.5 20c.7-3.8 3-5.8 6.5-5.8s5.8 2 6.5 5.8"/>`,
  archive: `<path d="M4 7.5h16V20H4z"/><path d="M3 4h18v3.5H3z"/><path d="M9 11h6"/>`,
  memory: `<path d="M9 5.5A3.2 3.2 0 0 0 4.8 9a3 3 0 0 0 .7 5.5A3.4 3.4 0 0 0 9 19M15 5.5A3.2 3.2 0 0 1 19.2 9a3 3 0 0 1-.7 5.5A3.4 3.4 0 0 1 15 19M9 5.5V19M15 5.5V19M9 9h2M13 12h2M9 15h2"/>`,
  models: `<rect x="7" y="7" width="10" height="10" rx="2"/><path d="M9.5 1.5v3M14.5 1.5v3M9.5 19.5v3M14.5 19.5v3M1.5 9.5h3M1.5 14.5h3M19.5 9.5h3M19.5 14.5h3"/><circle cx="12" cy="12" r="2.25"/>`,
  skills: `<path d="m4 20 10.8-10.8M13.2 4.8l6 6M18 3v3M16.5 4.5h3M6 5v4M4 7h4M17 16v4M15 18h4"/>`,
  notifications: `<path d="M6.5 10a5.5 5.5 0 0 1 11 0c0 6 2.5 6 2.5 6H4s2.5 0 2.5-6M10 20h4"/>`,
  appearance: `<circle cx="12" cy="12" r="3.5"/><path d="M12 2v2M12 20v2M2 12h2M20 12h2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M19.1 4.9l-1.4 1.4M6.3 17.7l-1.4 1.4"/>`,
  "im-gateway": `<path d="M5 5h14v10H9l-4 4z"/><circle cx="9" cy="10" r=".7" fill="currentColor" stroke="none"/><circle cx="12" cy="10" r=".7" fill="currentColor" stroke="none"/><circle cx="15" cy="10" r=".7" fill="currentColor" stroke="none"/>`,
  providers: `<path d="M7 18h10a4 4 0 0 0 .6-8A6 6 0 0 0 6.2 8.6 4.7 4.7 0 0 0 7 18Z"/>`,
  "shared-api": `<path d="M4 8h14M14 4l4 4-4 4M20 16H6M10 12l-4 4 4 4"/>`,
  "network-search": `<circle cx="10.5" cy="10.5" r="6.5"/><path d="M4 10.5h13M10.5 4a10 10 0 0 1 0 13M10.5 4a10 10 0 0 0 0 13M15.5 15.5 20 20"/>`,
  "servers-system": `<rect x="3" y="4" width="18" height="6" rx="2"/><rect x="3" y="14" width="18" height="6" rx="2"/><path d="M7 7h.01M7 17h.01M11 7h6M11 17h6"/>`,
  storage: `<path d="M5 5h14l2 5v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-8l2-5ZM3 10h18M8 15h.01M12 15h4"/>`,
  runtime: `<circle cx="12" cy="12" r="9"/><path d="m10 8 6 4-6 4z"/>`,
  "remote-access": `<path d="M5 9.5a10 10 0 0 1 14 0M8 13a6 6 0 0 1 8 0M10.5 16.5a2.5 2.5 0 0 1 3 0"/><circle cx="12" cy="20" r=".9" fill="currentColor" stroke="none"/>`,
  "peer-collaboration": `<rect x="2.5" y="6" width="8" height="12" rx="1.5"/><rect x="13.5" y="8.5" width="8" height="7" rx="1.5"/><path d="M10.5 12h3"/><circle cx="6.5" cy="15" r=".9" fill="currentColor" stroke="none"/>`,
  usage: `<path d="M3 20h18M5 17v-6M10 17V5M15 17v-9M20 17v-3"/>`,
  about: `<circle cx="12" cy="12" r="9"/><path d="M12 10v6M12 7h.01"/>`,
  users: `<circle cx="9" cy="8" r="3"/><path d="M3.5 19c.6-3.2 2.6-5 5.5-5s4.9 1.8 5.5 5"/><circle cx="17" cy="9" r="2.4"/><path d="M21.5 19c-.5-2.6-2-4.1-4.5-4.1-.6 0-1.2.1-1.7.3"/>`,
});

const fallbackSettingsIcon = `<path d="M4 7h10M18 7h2M4 17h2M10 17h10"/><circle cx="16" cy="7" r="2"/><circle cx="8" cy="17" r="2"/>`;

export function settingsIconSVG(key) {
  const paths = settingsIconPaths[String(key || "")] || fallbackSettingsIcon;
  return `<svg class="settings-icon-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${paths}</svg>`;
}

function settingItem(key, messageKey) {
  return {
    key,
    icon: key,
    label: t(`settings.item.${messageKey}.label`),
    subtitle: t(`settings.item.${messageKey}.subtitle`),
  };
}

export const settingsSections = [
  {
    title: t("settings.section.personal"),
    items: [
      settingItem("profile", "profile"),
      settingItem("archive", "archive"),
      settingItem("memory", "memory"),
      settingItem("models", "models"),
      settingItem("skills", "skills"),
      settingItem("notifications", "notifications"),
      settingItem("appearance", "appearance"),
      settingItem("im-gateway", "imGateway"),
    ],
  },
  {
    title: t("settings.section.instance"),
    items: [
      settingItem("providers", "providers"),
      settingItem("shared-api", "sharedAPI"),
      settingItem("network-search", "networkSearch"),
      settingItem("servers-system", "serversSystem"),
      settingItem("storage", "storage"),
      settingItem("runtime", "runtime"),
      settingItem("remote-access", "remoteAccess"),
      settingItem("peer-collaboration", "peerCollaboration"),
      settingItem("users", "users"),
      settingItem("usage", "usage"),
      settingItem("about", "about"),
    ],
  },
];

export const settingsItems = settingsSections.flatMap((section) => section.items);
export const settingsItemsByKey = new Map(settingsItems.map((item) => [item.key, item]));

export function settingsItemByKey(key) {
  return settingsItemsByKey.get(String(key || "")) || null;
}

export const skillTabs = [
  { key: "commands", messageKey: "commands" },
  { key: "optional-tools", messageKey: "optionalTools" },
  { key: "tool-permissions", messageKey: "toolPermissions" },
  { key: "global-skills", messageKey: "globalSkills" },
  { key: "project-skills", messageKey: "projectSkills" },
  { key: "subagents", messageKey: "subagents" },
  { key: "global-prompts", messageKey: "globalPrompts" },
  { key: "system-prompts", messageKey: "systemPrompts" },
  { key: "mcp-tools", messageKey: "mcpTools" },
  { key: "plugins", messageKey: "plugins" },
  { key: "hooks", messageKey: "hooks" },
].map(({ messageKey, ...tab }) => ({
  ...tab,
  label: t(`skills.tabs.${messageKey}.label`),
  description: t(`skills.tabs.${messageKey}.description`),
  empty: t(`skills.tabs.${messageKey}.empty`),
  action: t(`skills.tabs.${messageKey}.action`),
}));
