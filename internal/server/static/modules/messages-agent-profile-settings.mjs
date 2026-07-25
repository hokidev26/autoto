const messages = Object.freeze({
  "zh-CN": Object.freeze({
    title: "子代理预设与提示词扩展",
    immutableBoundary: "固定平台、运行、基础角色和收尾边界不可覆盖。",
    narrowingOnly: "工具和权限只能在父代理授权上限内收窄。",
    globalUserWarning: "global_user 会作为明确不可信的用户上下文发送，不会进入 system。",
    integrationPending: "effective prompt / child roles 将在 Runner 与 Background 主线接点接入。",
  }),
  "zh-TW": Object.freeze({
    title: "子代理預設與提示詞擴充",
    immutableBoundary: "固定平台、執行、基礎角色與收尾邊界不可覆寫。",
    narrowingOnly: "工具與權限只能在父代理授權上限內縮減。",
    globalUserWarning: "global_user 會作為明確不受信任的使用者內容傳送，不會進入 system。",
    integrationPending: "effective prompt / child roles 將在 Runner 與 Background 主線接點整合。",
  }),
  en: Object.freeze({
    title: "Child-agent presets and prompt extensions",
    immutableBoundary: "The platform, run, base-role, and closing boundaries cannot be overridden.",
    narrowingOnly: "Tools and permissions can only narrow the parent agent's authorization ceiling.",
    globalUserWarning: "global_user is sent as explicit untrusted user context, never as system text.",
    integrationPending: "Effective prompt and child-role resolution remain Runner/Background integration points.",
  }),
});

export function agentProfileMessage(key, locale = "en") {
  return messages[locale]?.[key] ?? messages.en[key] ?? String(key || "");
}

export default messages;
