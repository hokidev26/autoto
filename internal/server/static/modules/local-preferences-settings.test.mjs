import assert from "node:assert/strict";
import test from "node:test";

import { createLocalPreferencesSettingsController } from "./local-preferences-settings.mjs";

function controller(options = {}) {
  return createLocalPreferencesSettingsController({
    currentProfilePreferences: () => ({
      displayName: "Ada",
      roleLabel: "Dev",
      workspaceLabel: "Autoto Local",
      avatarInitials: "AT",
      gitName: "",
      gitEmail: "",
      avatarDataUrl: "",
      ...options.profile,
    }),
    profileDisplayName: () => options.profile?.displayName || "Ada",
    currentSearchPreferences: () => ({
      enabled: true,
      provider: "duckduckgo",
      maxResults: 5,
      safeSearch: true,
      confirmBeforeSearch: true,
      preferGitHub: true,
      allowedDomains: "",
      blockedDomains: "",
      customEndpoint: "",
      ...options.search,
    }),
    currentNotificationPreferences: () => ({
      toastEnabled: true,
      infoToasts: true,
      successToasts: true,
      warningToasts: true,
      errorToasts: true,
      errorToastsPersist: true,
      duration: "normal",
      soundEnabled: true,
      soundOnDone: true,
      soundOnApproval: true,
      soundOnError: true,
      soundPreset: "soft",
      soundVolume: 100,
      soundMaxConcurrent: 2,
      systemNotifications: false,
      hapticFeedback: true,
      terminalNotices: true,
      ...options.notifications,
    }),
    currentIMGatewayPreferences: () => ({
      enabled: false,
      channel: "webhook",
      inboundConfirm: true,
      requireSignature: true,
      redactSecrets: true,
      allowInboundMessages: true,
      notifyOnTaskDone: true,
      notifyOnErrors: true,
      notifyOnToolCalls: false,
      maxPayloadKB: 64,
      endpointUrl: "",
      allowedOrigins: "",
      blockedSenders: "",
      ...options.im,
    }),
    currentAppearancePreferences: () => ({
      themePreset: "light",
      density: "comfortable",
      backgroundMode: "theme",
      ...options.appearance,
    }),
    currentRegionalPreferences: () => ({ locale: "zh-CN", timezone: "auto" }),
    imGatewayChannelLabel: (channel) => ({ webhook: "Webhook", discord: "Discord", slack: "Slack", telegram: "Telegram", lark: "Lark", wecom: "WeCom", custom: "Custom" }[channel] || channel),
    renderThemeLibrarySection: () => "",
    state: options.state || {},
  });
}

test("search settings hide the custom endpoint unless the provider is custom, and escape domains", () => {
  // Showing the endpoint field for DuckDuckGo implied a URL the backend would
  // ignore. Domain lists are free text and must not inject markup.
  const duck = controller({
    search: { allowedDomains: `<img src=x>`, blockedDomains: `a.com</textarea>` },
  }).renderNetworkSearchSettingsContent();
  assert.doesNotMatch(duck, /id="searchCustomEndpoint"/);
  assert.match(duck, /<option value="duckduckgo" selected>/);
  assert.match(duck, /&lt;img src=x&gt;/);
  assert.match(duck, /a.com&lt;\/textarea&gt;/);
  assert.doesNotMatch(duck, /<img src=x>/);

  const custom = controller({
    search: { provider: "custom", customEndpoint: `https://x.example/"onclick` },
  }).renderNetworkSearchSettingsContent();
  assert.match(custom, /id="searchCustomEndpoint"/);
  assert.match(custom, /https:\/\/x\.example\/&quot;onclick/);
});

test("notification settings expose persist-until-dismissed and escape the webhook URL", () => {
  // Without the persist toggle the panel could not turn off the hold-until-dismiss
  // behaviour. The webhook URL is pasted from chat and must be attribute-escaped.
  const html = controller({
    notifications: { errorToastsPersist: true, duration: "long" },
    state: {
      serverNotificationSettings: { enabled: true, webhookUrl: `https://hooks.example/"onfocus=alert(1)` },
      serverNotificationError: `<img src=x>`,
    },
  }).renderNotificationSettingsContent();
  assert.match(html, /data-notification-toggle="errorToastsPersist"/);
  assert.match(html, /data-notification-toggle="soundOnApproval"/);
  assert.match(html, /id="requestSystemNotificationBtn"/);
  assert.match(html, /id="notificationSoundPreset"/);
  assert.match(html, /data-notification-sound-source="preset"/);
  assert.match(html, /data-notification-sound-source="custom"/);
  assert.doesNotMatch(html, /id="notificationSoundFile"/);
  assert.match(html, /id="notificationSoundVolume"/);
  assert.match(html, /id="notificationSoundMaxConcurrent"/);
  assert.match(html, /data-notification-duration="long"/);
  assert.match(html, /aria-checked="true"[^>]*data-notification-duration="long"|data-notification-duration="long"[^>]*aria-checked="true"/);
  assert.match(html, /https:\/\/hooks\.example\/&quot;onfocus=alert\(1\)/);
  assert.match(html, /&lt;img src=x&gt;/);
  assert.doesNotMatch(html, /<img src=x>/);
});

test("custom sound source shows the local file picker and escapes the clip name", () => {
  const html = controller({
    notifications: { soundSource: "custom", soundCustomName: `ding"<img src=x>` },
  }).renderNotificationSettingsContent();
  assert.match(html, /data-notification-sound-source="custom"[^>]*aria-selected="true"|aria-selected="true"[^>]*data-notification-sound-source="custom"/);
  assert.match(html, /id="notificationSoundFile"/);
  assert.match(html, /id="chooseNotificationSoundFileBtn"/);
  assert.match(html, /ding&quot;&lt;img src=x&gt;/);
  assert.doesNotMatch(html, /<img src=x>/);
  assert.doesNotMatch(html, /id="notificationSoundPreset"/);
});

test("IM gateway marks the active channel and escapes the endpoint plus origin lists", () => {
  // An inactive channel that still looked selected sent events to the wrong
  // adapter. Origins are newline-separated free text.
  const html = controller({
    im: {
      channel: "slack",
      endpointUrl: `javascript:alert(1)`,
      allowedOrigins: `<script>x</script>`,
    },
  }).renderIMGatewaySettingsContent();
  assert.match(html, /class="appearance-choice active"[^>]*data-im-channel="slack"/);
  assert.match(html, /data-im-channel="webhook"/);
  assert.doesNotMatch(html, /class="appearance-choice active"[^>]*data-im-channel="webhook"/);
  assert.match(html, /javascript:alert\(1\)/);
  assert.match(html, /&lt;script&gt;x&lt;\/script&gt;/);
  assert.doesNotMatch(html, /<script>x<\/script>/);
});

test("profile settings escape display fields and omit the remove-avatar control without an image", () => {
  // Display name and git identity are typed by the user. A leftover remove button
  // with no image still ran the delete path and cleared a saved avatar.
  const html = controller({
    profile: {
      displayName: `<b>Ada</b>`,
      roleLabel: `Dev & QA`,
      gitName: `"quoted"`,
      gitEmail: `ada@example.com`,
      avatarDataUrl: "",
    },
  }).renderProfileSettingsContent();
  assert.match(html, /id="profileDisplayName"[^>]*value="&lt;b&gt;Ada&lt;\/b&gt;"/);
  assert.match(html, /Dev &amp; QA/);
  assert.match(html, /value="&quot;quoted&quot;"/);
  assert.doesNotMatch(html, /id="removeProfileAvatarBtn"/);
  assert.match(html, /id="chooseProfileAvatarBtn"/);
});
