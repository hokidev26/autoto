import test from "node:test";
import assert from "node:assert/strict";

import {
  createRemoteAccessSettingsController,
  isEnvironmentCredential,
  localizeRemoteAccessPasswordError,
  normalizeRemoteAccess,
  passwordPayload,
  policyPayload,
} from "./remote-access-settings.mjs";

const baseAccess = {
  credential: { configured: true, source: "config" },
  policy: { allowFullAccess: false, defaultMode: "restricted", allowRemoteNativePicker: false, revision: 1 },
  session: { remote: true, authenticated: true, mode: "restricted", expiresAt: "2026-07-17T00:00:00Z" },
  capabilities: { maxPermissionMode: "acceptEdits", terminalAllowed: false, filesystemScope: "project", nativePickerAllowed: false, securityAdminAllowed: false },
};

const localAccess = {
  ...baseAccess,
  session: { remote: false, authenticated: true, mode: "local", expiresAt: "" },
  capabilities: { maxPermissionMode: "bypassPermissions", terminalAllowed: true, filesystemScope: "host", nativePickerAllowed: true, securityAdminAllowed: true },
  tunnel: { available: true, installable: false, status: "idle", publicUrl: "", error: "", startedAt: "" },
};

test("normalizes remote access state and recognizes environment credentials", () => {
  assert.deepEqual(normalizeRemoteAccess({ policy: { defaultMode: "unexpected" } }).policy, {
    allowFullAccess: false,
    defaultMode: "restricted",
    allowRemoteNativePicker: false,
    revision: 0,
  });
  assert.equal(normalizeRemoteAccess({ policy: { allowFullAccess: true, defaultMode: "restricted" } }).policy.defaultMode, "full");
  assert.equal(isEnvironmentCredential("environment"), true);
  assert.equal(isEnvironmentCredential("env"), true);
  assert.equal(isEnvironmentCredential("stored"), false);
});

test("policy and password payloads carry currentPassword only when supplied", () => {
  assert.deepEqual(policyPayload(baseAccess, baseAccess.policy, "current"), {
    allowFullAccess: false,
    defaultMode: "restricted",
    allowRemoteNativePicker: false,
    revision: 1,
    currentPassword: "current",
  });
  assert.deepEqual(policyPayload(baseAccess, { defaultMode: "full", allowRemoteNativePicker: true }), {
    allowFullAccess: true,
    defaultMode: "full",
    allowRemoteNativePicker: true,
    revision: 1,
  });
  assert.deepEqual(policyPayload(baseAccess, { allowFullAccess: true, defaultMode: "restricted" }), {
    allowFullAccess: true,
    defaultMode: "full",
    allowRemoteNativePicker: false,
    revision: 1,
  });
  assert.deepEqual(passwordPayload("generate"), { strategy: "generate" });
  assert.deepEqual(passwordPayload("custom", "new-password", "current"), {
    strategy: "custom",
    password: "new-password",
    currentPassword: "current",
  });
});

test("invalid custom password rules are shown in the UI locale", async () => {
  assert.equal(
    localizeRemoteAccessPasswordError("password must include at least three character classes"),
    "密码需包含大写、小写、数字、符号其中至少三种。",
  );
  assert.equal(
    localizeRemoteAccessPasswordError("password must be between 12 and 256 characters"),
    "密码需为 12 到 256 个字符。",
  );
  assert.equal(
    localizeRemoteAccessPasswordError("password must not contain whitespace or control characters"),
    "密码不能包含空白或控制字符。",
  );
  const controller = createRemoteAccessSettingsController({
    state: { remoteAccess: localAccess },
    request: async () => {
      throw Object.assign(new Error("password must include at least three character classes"), { status: 400 });
    },
  });
  await assert.rejects(
    () => controller.updatePassword("custom", "aaaaaaaaaaaa"),
    { message: "密码需包含大写、小写、数字、符号其中至少三种。" },
  );
});

test("normalizes and controls a temporary tunnel through the security endpoint", async () => {
  const requests = [];
  const state = { remoteAccess: localAccess };
  const controller = createRemoteAccessSettingsController({
    state,
    request: async (path, options) => {
      requests.push({ path, options });
      return options.method === "POST"
        ? { available: true, status: "running", publicUrl: "https://bright-sun.trycloudflare.com", startedAt: "2026-07-18T00:00:00Z" }
        : { available: true, status: "idle" };
    },
  });

  const running = await controller.startTunnel();
  assert.deepEqual(running, {
    available: true,
    installable: false,
    status: "running",
    publicUrl: "https://bright-sun.trycloudflare.com",
    error: "",
    startedAt: "2026-07-18T00:00:00Z",
    // A response without a mode is treated as a quick tunnel, so an older server
    // is never described as having a stable address.
    mode: "quick",
    namedConfigured: false,
  });
  assert.equal(state.remoteAccess.tunnel.publicUrl, "https://bright-sun.trycloudflare.com");

  const stopped = await controller.stopTunnel();
  assert.equal(stopped.status, "idle");
  assert.deepEqual(requests.map(({ path, options }) => [path, options.method]), [
    ["/api/security/remote-access/tunnel", "POST"],
    ["/api/security/remote-access/tunnel", "DELETE"],
  ]);

  // Running tunnel UI includes phone QR + scan steps for mobile follow-up.
  state.remoteAccess = {
    ...localAccess,
    tunnel: {
      available: true,
      status: "running",
      publicUrl: "https://bright-sun.trycloudflare.com",
      error: "",
      startedAt: "2026-07-18T00:00:00Z",
    },
  };
  const html = controller.render();
  assert.match(html, /remote-access-tunnel-qr/);
  assert.match(html, /<svg /);
  assert.match(html, /remote-access-phone-steps/);
  assert.match(html, /bright-sun\.trycloudflare\.com/);
});

test("shows one-click install only while cloudflared is missing, then switches to start", async () => {
  const requests = [];
  const state = {
    remoteAccess: {
      ...localAccess,
      tunnel: { available: false, installable: true, status: "unavailable", publicUrl: "", error: "cloudflared executable was not found", startedAt: "" },
    },
  };
  const controller = createRemoteAccessSettingsController({
    state,
    request: async (path, options) => {
      requests.push({ path, options });
      return { available: true, installable: false, status: "idle", publicUrl: "", error: "", startedAt: "" };
    },
  });

  const before = controller.render();
  assert.match(before, /id="installRemoteTunnelBtn"/);
  assert.match(before, />一键安装<\/button>/);
  assert.doesNotMatch(before, /id="startRemoteTunnelBtn"/);
  assert.doesNotMatch(before, /cloudflared executable was not found/);

  const installed = await controller.installTunnel();
  assert.equal(installed.available, true);
  assert.equal(installed.status, "idle");
  assert.deepEqual(requests.map(({ path, options }) => [path, options.method]), [
    ["/api/security/remote-access/tunnel/install", "POST"],
  ]);

  const after = controller.render();
  assert.doesNotMatch(after, /id="installRemoteTunnelBtn"/);
  assert.match(after, /id="startRemoteTunnelBtn"/);
  assert.match(after, />一键启动<\/button>/);

  state.remoteAccess = {
    ...localAccess,
    tunnel: { available: false, installable: false, status: "unavailable", publicUrl: "", error: "", startedAt: "" },
  };
  const unsupported = controller.render();
  assert.doesNotMatch(unsupported, /id="installRemoteTunnelBtn"/);
  assert.doesNotMatch(unsupported, /id="startRemoteTunnelBtn"/);
  assert.match(unsupported, /当前平台不支持一键安装/);
});

test("temporary tunnel install button stays stable while installation is running", async () => {
  const previousDocument = globalThis.document;
  let clickHandler = null;
  let resolveRequest = null;
  const attributes = new Map();
  const button = {
    dataset: { remoteTunnelAction: "install", remoteTunnelBusyLabel: "安装中…" },
    textContent: "一键安装",
    disabled: false,
    addEventListener(type, handler) { if (type === "click") clickHandler = handler; },
    setAttribute(name, value) { attributes.set(name, String(value)); },
    removeAttribute(name) { attributes.delete(name); },
  };
  globalThis.document = {
    getElementById(id) { return id === "installRemoteTunnelBtn" ? button : null; },
  };
  try {
    const state = {
      remoteAccess: {
        ...localAccess,
        tunnel: { available: false, installable: true, status: "unavailable", publicUrl: "", error: "", startedAt: "" },
      },
    };
    const controller = createRemoteAccessSettingsController({
      state,
      request: async () => new Promise((resolve) => { resolveRequest = resolve; }),
    });
    controller.bind();
    assert.equal(typeof clickHandler, "function");

    const pending = clickHandler({ currentTarget: button });
    await Promise.resolve();
    assert.equal(button.textContent, "安装中…");
    assert.equal(button.disabled, true);
    assert.equal(attributes.get("aria-busy"), "true");

    resolveRequest({ available: true, installable: false, status: "idle" });
    await pending;
    assert.equal(button.textContent, "一键安装");
    assert.equal(button.disabled, false);
    assert.equal(attributes.has("aria-busy"), false);
    assert.equal(state.remoteAccess.tunnel.available, true);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("temporary tunnel start button keeps its label and width hook while starting", async () => {
  const previousDocument = globalThis.document;
  let clickHandler = null;
  let resolveRequest = null;
  const attributes = new Map();
  const button = {
    dataset: { remoteTunnelAction: "start", remoteTunnelBusyLabel: "启动中…" },
    textContent: "一键启动",
    disabled: false,
    addEventListener(type, handler) { if (type === "click") clickHandler = handler; },
    setAttribute(name, value) { attributes.set(name, String(value)); },
    removeAttribute(name) { attributes.delete(name); },
  };
  globalThis.document = {
    getElementById(id) { return id === "startRemoteTunnelBtn" ? button : null; },
  };
  try {
    const state = {
      remoteAccess: {
        ...localAccess,
        tunnel: { available: true, status: "idle", publicUrl: "", error: "", startedAt: "" },
      },
    };
    const controller = createRemoteAccessSettingsController({
      state,
      request: async () => new Promise((resolve) => { resolveRequest = resolve; }),
    });
    controller.bind();
    assert.equal(typeof clickHandler, "function");

    const pending = clickHandler({ currentTarget: button });
    await Promise.resolve();
    assert.equal(button.textContent, "启动中…");
    assert.equal(button.disabled, true);
    assert.equal(attributes.get("aria-busy"), "true");

    resolveRequest({ available: true, status: "running", publicUrl: "https://example.trycloudflare.com" });
    await pending;
    assert.equal(button.textContent, "一键启动");
    assert.equal(button.disabled, false);
    assert.equal(attributes.has("aria-busy"), false);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("a failed temporary tunnel start keeps the error on the card", async () => {
  const state = {
    remoteAccess: {
      ...localAccess,
      tunnel: { available: true, status: "idle", publicUrl: "", error: "", startedAt: "" },
    },
  };
  const controller = createRemoteAccessSettingsController({
    state,
    request: async () => {
      const error = new Error("Cloudflare Quick Tunnel 无法连线");
      error.status = 503;
      throw error;
    },
  });
  await assert.rejects(() => controller.startTunnel(), /无法连线/);
  assert.equal(state.remoteAccess.tunnel.status, "error");
  assert.match(state.remoteAccess.tunnel.error, /无法连线/);
  const html = controller.render();
  assert.match(html, /settings-inline-alert/);
  assert.match(html, /无法连线/);
  assert.doesNotMatch(html, /尚未启动临时隧道/);
});

test("saves host-local policy with revision", async () => {
  const requests = [];
  const state = { remoteAccess: localAccess };
  const controller = createRemoteAccessSettingsController({
    state,
    request: async (path, options) => {
      requests.push({ path, options });
      return { ...localAccess.policy, allowFullAccess: true, defaultMode: "full", revision: 2 };
    },
  });

  await controller.savePolicy({ ...localAccess.policy, allowFullAccess: true, defaultMode: "full" });

  assert.deepEqual(requests, [{
    path: "/api/security/remote-access/policy",
    options: {
      method: "PATCH",
      body: JSON.stringify({
        allowFullAccess: true,
        defaultMode: "full",
        allowRemoteNativePicker: false,
        revision: 1,
      }),
    },
  }]);
  assert.equal(state.remoteAccess.policy.defaultMode, "full");
  assert.equal(state.remoteAccess.session.authenticated, true);
  assert.equal(state.remoteAccess.capabilities.securityAdminAllowed, true);
  assert.doesNotMatch(controller.render(), /href="\/auth\/remote-access"/);
});

test("policy and password cards stack option rows and method tiles", () => {
  const html = createRemoteAccessSettingsController({
    state: { remoteAccess: localAccess },
  }).render();
  assert.match(html, /remote-access-option-stack/);
  assert.match(html, /data-remote-full-card/);
  assert.match(html, /data-remote-picker-card/);
  assert.match(html, /form="remoteAccessPolicyForm"[^>]*data-remote-policy-submit|data-remote-policy-submit[^>]*form="remoteAccessPolicyForm"/);
  assert.match(html, /remote-access-credential-card/);
  assert.match(html, /remote-access-password-copy/);
  assert.doesNotMatch(html, /settings-check-row/);
});

test("generated password stays inside the generate tile and custom password is labeled once", async () => {
  const controller = createRemoteAccessSettingsController({
    state: { remoteAccess: localAccess },
    request: async () => ({ credential: { configured: true, source: "config" }, revision: 2, generatedPassword: "generated-once" }),
  });
  await controller.updatePassword("generate");
  const html = controller.render();
  const generateForm = html.match(/<form id="remoteAccessGeneratePasswordForm"[\s\S]*?<\/form>/)?.[0] || "";
  const customForm = html.match(/<form id="remoteAccessCustomPasswordForm"[\s\S]*?<\/form>/)?.[0] || "";
  assert.match(generateForm, /id="copyGeneratedRemotePasswordBtn"/);
  assert.match(generateForm, /generated-once/);
  assert.match(generateForm, /has-generated/);
  assert.match(generateForm, /remote-access-generated-row/);
  assert.match(generateForm, /再生成一组|再產生一組|Generate another/);
  assert.doesNotMatch(html.replace(generateForm, ""), /id="copyGeneratedRemotePasswordBtn"/);
  assert.match(html, /remote-access-credential-status/);
  assert.doesNotMatch(html, /remote-access-credential-source/);
  assert.match(customForm, /aria-label="自定义密码"/);
  assert.doesNotMatch(customForm, /<label class="settings-form-field">自定义密码/);
});

test("policy submit drives the busy state on the footer button outside the form", async () => {
  const previousDocument = globalThis.document;
  const submitButton = {
    dataset: {},
    textContent: "保存策略",
    disabled: false,
    attributes: new Map(),
    setAttribute(name, value) { this.attributes.set(name, String(value)); },
    removeAttribute(name) { this.attributes.delete(name); },
  };
  const form = {
    listeners: new Map(),
    addEventListener(name, handler) { this.listeners.set(name, handler); },
    // The button lives in the card footer, so a form-scoped lookup finds
    // nothing. If the handler regresses to that, the busy state goes missing.
    querySelector: () => null,
  };
  // bind() also wires refresh, tunnel and password controls. They are irrelevant
  // here, so they get inert stubs rather than shaping the whole test around them.
  const inert = () => ({ checked: false, value: "", addEventListener() {}, setAttribute() {} });
  const elements = {
    remoteAccessPolicyForm: form,
    remoteAccessAllowFullAccess: { checked: true, addEventListener() {}, setAttribute() {} },
    remoteAccessNativePicker: { checked: false },
    remoteAccessPolicyCurrentPassword: { value: "" },
    refreshRemoteAccessBtn: inert(),
    remoteAccessGeneratePasswordForm: inert(),
    remoteAccessCustomPasswordForm: inert(),
  };
  let busyDuringRequest = null;
  globalThis.document = {
    getElementById: (id) => elements[id] ?? null,
    querySelector: (selector) => (selector === "[data-remote-policy-submit]" ? submitButton : null),
  };
  try {
    const state = { remoteAccess: localAccess };
    const controller = createRemoteAccessSettingsController({
      state,
      request: async () => {
        busyDuringRequest = { disabled: submitButton.disabled, aria: submitButton.attributes.get("aria-busy") };
        return { ...localAccess.policy, allowFullAccess: true, defaultMode: "full", revision: 2 };
      },
    });
    controller.bind();
    await form.listeners.get("submit")({ preventDefault() {}, currentTarget: form });

    // A form-scoped lookup resolves to null here, and setButtonBusy no-ops on
    // null, so the old code path would leave the button untouched instead of
    // throwing. Pinning both halves keeps that silent failure from returning.
    assert.equal(form.querySelector("[data-remote-policy-submit]"), null);
    assert.deepEqual(busyDuringRequest, { disabled: true, aria: "true" });
    assert.equal(submitButton.disabled, false);
    assert.equal(submitButton.attributes.has("aria-busy"), false);
    assert.equal(state.remoteAccess.policy.allowFullAccess, true);
  } finally {
    if (previousDocument === undefined) delete globalThis.document;
    else globalThis.document = previousDocument;
  }
});

test("generates and consumes a one-time password locally without retaining it in state", async () => {
  const requests = [];
  const state = { remoteAccess: localAccess };
  const controller = createRemoteAccessSettingsController({
    state,
    request: async (path, options) => {
      requests.push({ path, options });
      return { credential: { configured: true, source: "config" }, revision: 2, generatedPassword: "generated-once" };
    },
  });

  const result = await controller.updatePassword("generate");

  assert.equal(result.generatedPassword, "generated-once");
  assert.equal(controller.generatedPasswordValue(), "generated-once");
  assert.equal(state.remoteAccess.generatedPassword, undefined);
  assert.equal(controller.consumeGeneratedPassword(), "generated-once");
  assert.equal(controller.generatedPasswordValue(), "");
  assert.deepEqual(JSON.parse(requests[0].options.body), { strategy: "generate" });
});

test("renders remote security settings read-only and host-local settings editable", () => {
  const restricted = createRemoteAccessSettingsController({ state: { remoteAccess: baseAccess } });
  const restrictedHTML = restricted.render();
  assert.doesNotMatch(restrictedHTML, /id="remoteAccessDefaultMode"/);
  assert.match(restrictedHTML, /id="remoteAccessAllowFullAccess"[^>]*disabled/);
  assert.match(restrictedHTML, /id="remoteAccessNativePicker"[^>]*disabled/);
  assert.match(restrictedHTML, /data-remote-policy-submit[^>]*disabled/);
  assert.match(restrictedHTML, /data-remote-generate-submit[^>]*disabled/);
  assert.match(restrictedHTML, /data-remote-custom-submit[^>]*disabled/);

  const fullRemoteState = {
    remoteAccess: {
      ...baseAccess,
      session: { ...baseAccess.session, mode: "full" },
      capabilities: { ...localAccess.capabilities, nativePickerAllowed: false, securityAdminAllowed: false },
    },
  };
  const fullRemoteHTML = createRemoteAccessSettingsController({ state: fullRemoteState }).render();
  assert.doesNotMatch(fullRemoteHTML, /id="remoteAccessDefaultMode"/);
  assert.match(fullRemoteHTML, /id="remoteAccessAllowFullAccess"[^>]*disabled/);
  assert.match(fullRemoteHTML, /data-remote-policy-submit[^>]*disabled/);

  const localHTML = createRemoteAccessSettingsController({ state: { remoteAccess: localAccess } }).render();
  assert.match(localHTML, /class="settings-action-btn remote-access-tunnel-action primary"[^>]*data-remote-tunnel-busy-label="启动中…"/);
  assert.match(localHTML, /id="remoteAccessAllowFullAccess"[^>]*role="switch"/);
  assert.doesNotMatch(localHTML, /id="remoteAccessAllowFullAccess"[^>]*\schecked(?:\s|>)/);
  assert.doesNotMatch(localHTML, /id="remoteAccessDefaultMode"/);
  assert.doesNotMatch(localHTML, /<option value="full"/);
  for (const pattern of [
    /<input id="remoteAccessAllowFullAccess"[^>]*>/,
    /<input id="remoteAccessNativePicker"[^>]*>/,
    /<button class="settings-action-btn primary"[^>]*data-remote-policy-submit[^>]*>/,
  ]) {
    const tag = localHTML.match(pattern)?.[0] || "";
    assert.ok(tag);
    assert.equal(tag.includes("disabled"), false);
  }

  // Save moved into the policy card header, so it needs an explicit form
  // association to keep submitting from outside the <form>.
  assert.match(localHTML, /data-remote-policy-submit/);
  assert.match(localHTML, /<button class="settings-action-btn primary"[^>]*form="remoteAccessPolicyForm"[^>]*data-remote-policy-submit/);
  assert.doesNotMatch(localHTML, /策略版本/);
  assert.doesNotMatch(localHTML, /保存后请重新登录远程会话/);

  // Capabilities render as a badge grid; booleans carry a tone, enums stay plain.
  assert.match(localHTML, /class="remote-access-capability-grid settings-card-content"/);
  assert.match(localHTML, /class="remote-access-capability-cell"><span>终端<\/span><span class="settings-badge ok">允许<\/span>/);
  assert.match(localHTML, /class="remote-access-capability-cell"><span>文件系统范围<\/span><strong>full<\/strong>/);
  assert.match(restrictedHTML, /class="remote-access-capability-cell"><span>终端<\/span><span class="settings-badge warn">不允许<\/span>/);
  assert.doesNotMatch(localHTML, /runtime-kv-list/);

  const allowedButRestricted = createRemoteAccessSettingsController({
    state: { remoteAccess: { ...localAccess, policy: { ...localAccess.policy, allowFullAccess: true, defaultMode: "restricted" } } },
  }).render();
  assert.match(allowedButRestricted, /id="remoteAccessAllowFullAccess"[^>]*\schecked(?:\s|>)/);
  assert.match(allowedButRestricted, /已开启；新会话将直接使用完整权限/);
  assert.doesNotMatch(allowedButRestricted, /id="remoteAccessDefaultMode"/);
  assert.doesNotMatch(allowedButRestricted, /但新会话默认仍为受限/);

  const environmentLocal = createRemoteAccessSettingsController({
    state: { remoteAccess: { ...localAccess, credential: { configured: true, source: "environment" } } },
  });
  const environmentLocalHTML = environmentLocal.render();
  assert.match(environmentLocalHTML, /id="remoteAccessGeneratePasswordForm"/);
  assert.match(environmentLocalHTML, /id="remoteAccessCustomPasswordForm"/);
  assert.doesNotMatch(environmentLocalHTML, /data-remote-generate-submit[^>]*disabled/);
  assert.doesNotMatch(environmentLocalHTML, /data-remote-custom-submit[^>]*disabled/);

  const environmentRemote = createRemoteAccessSettingsController({
    state: { remoteAccess: { ...baseAccess, credential: { configured: true, source: "environment" } } },
  });
  const environmentRemoteHTML = environmentRemote.render();
  assert.match(environmentRemoteHTML, /data-remote-generate-submit[^>]*disabled/);
  assert.match(environmentRemoteHTML, /data-remote-custom-submit[^>]*disabled/);
});

test("updates a custom password locally without exposing a generated password", async () => {
  const requests = [];
  const state = { remoteAccess: localAccess };
  const controller = createRemoteAccessSettingsController({
    state,
    request: async (path, options) => {
      requests.push({ path, options });
      return { credential: { configured: true, source: "config" }, revision: 2 };
    },
  });

  const result = await controller.updatePassword("custom", "new-secret");

  assert.equal(result.generatedPassword, "");
  assert.equal(controller.generatedPasswordValue(), "");
  assert.deepEqual(JSON.parse(requests[0].options.body), {
    strategy: "custom",
    password: "new-secret",
  });
});

test("remote access load fails closed on 401 and notifies immediately", async () => {
  const state = { remoteAccess: { ...baseAccess, session: { ...baseAccess.session, mode: "full" }, capabilities: { ...localAccess.capabilities, securityAdminAllowed: false } } };
  let changes = 0;
  const controller = createRemoteAccessSettingsController({
    state,
    request: async () => {
      const error = new Error("session expired");
      error.status = 401;
      throw error;
    },
    onChange: () => { changes += 1; },
  });

  await assert.rejects(controller.load(), /session expired/);
  assert.equal(state.remoteAccessFailClosed, true);
  assert.equal(state.remoteAccess.session.authenticated, false);
  assert.equal(state.remoteAccess.capabilities.terminalAllowed, false);
  assert.equal(state.remoteAccess.capabilities.maxPermissionMode, "acceptEdits");
  assert.equal(changes, 1);
});

test("localhost authorization errors do not synthesize a remote fail-closed session", async () => {
  for (const status of [401, 403]) {
    const state = { remoteAccess: localAccess, remoteAccessFailClosed: false };
    const controller = createRemoteAccessSettingsController({
      state,
      request: async () => {
        const error = new Error(`local-${status}`);
        error.status = status;
        throw error;
      },
    });

    await assert.rejects(controller.load(), new RegExp(`local-${status}`));
    assert.equal(state.remoteAccessFailClosed, false);
    assert.equal(state.remoteAccess.session.remote, false);
    assert.equal(state.remoteAccess.capabilities.terminalAllowed, true);
  }
});

test("a stale successful load cannot clear a newer authorization failure", async () => {
  const state = {
    remoteAccess: {
      ...baseAccess,
      session: { ...baseAccess.session, mode: "full" },
      capabilities: { ...localAccess.capabilities, securityAdminAllowed: false },
    },
  };
  const requests = [];
  const controller = createRemoteAccessSettingsController({
    state,
    request: () => new Promise((resolve, reject) => requests.push({ resolve, reject })),
  });

  const staleSuccess = controller.load();
  const currentFailure = controller.load();
  const error = new Error("expired");
  error.status = 401;
  requests[1].reject(error);
  await assert.rejects(currentFailure, /expired/);
  requests[0].resolve(localAccess);
  await staleSuccess;

  assert.equal(state.remoteAccessFailClosed, true);
  assert.equal(state.remoteAccess.session.authenticated, false);
  assert.equal(state.remoteAccess.capabilities.terminalAllowed, false);
  assert.equal(state.remoteAccessLoading, false);
});

test("external authorization invalidation makes an in-flight success stale", async () => {
  const state = { remoteAccess: baseAccess };
  let resolveRequest;
  const controller = createRemoteAccessSettingsController({
    state,
    request: () => new Promise((resolve) => { resolveRequest = resolve; }),
  });
  const pending = controller.load();
  controller.invalidatePendingLoads();
  state.remoteAccessFailClosed = true;
  resolveRequest(localAccess);
  await pending;
  assert.equal(state.remoteAccessFailClosed, true);
  assert.equal(state.remoteAccess.session.remote, true);
});

test("incomplete remote capability responses remain restricted", async () => {
  const state = { remoteAccess: baseAccess };
  const controller = createRemoteAccessSettingsController({
    state,
    request: async () => ({
      credential: baseAccess.credential,
      policy: baseAccess.policy,
      session: { remote: true, authenticated: true, mode: "full" },
    }),
  });

  await controller.load();
  assert.equal(state.remoteAccessFailClosed, true);
  assert.equal(state.remoteAccess.session.mode, "restricted");
  assert.equal(state.remoteAccess.capabilities.terminalAllowed, false);
});

test("authoritative refresh clears a previous fail-closed state", async () => {
  const state = { remoteAccess: baseAccess, remoteAccessFailClosed: true };
  const controller = createRemoteAccessSettingsController({ state, request: async () => localAccess });

  await controller.load();
  assert.equal(state.remoteAccessFailClosed, false);
  assert.equal(state.remoteAccess.session.remote, false);
  assert.equal(state.remoteAccess.capabilities.terminalAllowed, true);
});

test("a named tunnel is described as stable and an unknown mode falls back to quick", () => {
  const named = normalizeRemoteAccess({
    ...localAccess,
    tunnel: { available: true, status: "running", publicUrl: "https://autoto.example.com", mode: "named", namedConfigured: true },
  }).tunnel;
  assert.equal(named.mode, "named");
  assert.equal(named.namedConfigured, true);

  // Anything other than "named" must read as quick. Describing an unknown mode as
  // stable would tell the user the address persists when it may not.
  for (const mode of ["quick", "", "NAMED", "unexpected", undefined, null, 7]) {
    const tunnel = normalizeRemoteAccess({ ...localAccess, tunnel: { available: true, status: "idle", mode } }).tunnel;
    assert.equal(tunnel.mode, "quick", `mode ${String(mode)} should normalize to quick`);
  }
});

test("the tunnel card names the mode it is actually running", () => {
  const state = {
    remoteAccess: {
      ...localAccess,
      tunnel: {
        available: true,
        installable: false,
        status: "running",
        publicUrl: "https://autoto.example.com",
        error: "",
        startedAt: "2026-07-18T00:00:00Z",
        mode: "named",
        namedConfigured: true,
      },
    },
  };
  const controller = createRemoteAccessSettingsController({ state, request: async () => state.remoteAccess });
  const html = controller.render();
  assert.match(html, /autoto\.example\.com/);
  assert.doesNotMatch(html, /trycloudflare/);
});

test("remote access cards collapse against the settings panel", async () => {
  const { readFile } = await import("node:fs/promises");
  const css = await readFile(new URL("../styles/workspace-tasks.css", import.meta.url), "utf8");
  assert.match(css, /container:\s*settings-network-page\s*\/\s*inline-size/);
  assert.match(css, /@container settings-network-page \(min-width: 760px\)/);
  assert.match(css, /@container settings-network-page \(max-width: 759px\)/);
  assert.doesNotMatch(css, /@media \(max-width: 760px\) \{\s*\n\s*\.remote-access-summary-grid/);
});
