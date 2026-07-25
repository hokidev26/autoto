import test from "node:test";
import assert from "node:assert/strict";

import { lifecycleHookMessage } from "./messages-lifecycle-hooks.mjs";
import {
  buildLifecycleHookPayload,
  createLifecycleHooksController,
  normalizeLifecycleHook,
  redactLifecycleText,
  renderLifecycleHooks,
} from "./lifecycle-hooks.mjs";

function shellDraft(overrides = {}) {
  return {
    name: " Audit hook ",
    description: " after tools ",
    enabled: true,
    event: "tool.after",
    scope: { kind: "project", id: "project-1" },
    priority: 40,
    filter: { toolNames: ["read_*", "read_*"], attributes: { result: ["ok"] } },
    mode: "async",
    failurePolicy: "retry",
    action: { kind: "shell", shell: { executable: "audit-helper", args: ["--json"], cwd: "tools/hooks", secretRefs: { AUDIT_TOKEN: "env:AUDIT_TOKEN" }, canonicalStdinV1: true } },
    ...overrides,
  };
}

function serverHook(overrides = {}) {
  return {
    id: "hook-1",
    ...shellDraft(),
    action: { kind: "shell", shell: { executable: "audit-helper", args: ["--json"], cwd: "tools/hooks", timeoutSeconds: 30, canonicalStdinV1: true }, secretConfigured: { AUDIT_TOKEN: true } },
    revision: 3,
    createdAt: "2026-07-24T00:00:00Z",
    updatedAt: "2026-07-24T01:00:00Z",
    ...overrides,
  };
}

function requestQueue(items) {
  const calls = [];
  const request = async (path, options = {}) => {
    calls.push({ path, options, body: options.body ? JSON.parse(options.body) : null });
    const item = items.shift();
    if (item instanceof Error) throw item;
    if (typeof item === "function") return item(path, options, calls);
    return structuredClone(item ?? {});
  };
  return { request, calls };
}

test("server hook normalization never retains secret references or raw secret fields", () => {
  const normalized = normalizeLifecycleHook({
    ...serverHook(),
    action: {
      kind: "shell",
      shell: { executable: "audit-helper", secretRefs: { AUDIT_TOKEN: "env:PRIVATE_NAME" }, token: "plaintext" },
      secretConfigured: { AUDIT_TOKEN: true },
      secret: "private",
    },
  });
  assert.deepEqual(normalized.action.secretConfigured, { AUDIT_TOKEN: true });
  assert.equal(Object.hasOwn(normalized.action.shell, "secretRefs"), false);
  assert.equal(Object.hasOwn(normalized.action.shell, "token"), false);
  assert.equal(JSON.stringify(normalized).includes("PRIVATE_NAME"), false);
  assert.equal(JSON.stringify(normalized).includes("plaintext"), false);
});

test("payload builder enforces sync rules and safe shell execution", () => {
  const payload = buildLifecycleHookPayload(shellDraft());
  assert.equal(payload.name, "Audit hook");
  assert.deepEqual(payload.filter.toolNames, ["read_*"]);
  assert.equal(payload.action.shell.cwd, "tools/hooks");
  assert.equal(payload.action.shell.canonicalStdinV1, true);
  assert.deepEqual(payload.action.shell.secretRefs, { AUDIT_TOKEN: "env:AUDIT_TOKEN" });
  assert.equal(Object.hasOwn(payload.action.shell, "detached"), false);

  assert.throws(() => buildLifecycleHookPayload(shellDraft({ event: "run.before", mode: "async" })), /synchronous/i);
  assert.throws(() => buildLifecycleHookPayload(shellDraft({ action: { kind: "shell", shell: { executable: "bash", cwd: "tools" } } })), /interpreters/i);
  assert.throws(() => buildLifecycleHookPayload(shellDraft({ action: { kind: "shell", shell: { executable: "audit-helper", cwd: "C:\\workspace" } } })), /workspace-relative/i);
  assert.throws(() => buildLifecycleHookPayload(shellDraft({ action: { kind: "shell", shell: { executable: "audit-helper", args: ["&"] } } })), /background/i);
  assert.throws(() => buildLifecycleHookPayload(shellDraft({ action: { kind: "shell", shell: { executable: "audit-helper", secretRefs: { TOKEN: "plaintext" } } } })), /references/i);
  assert.throws(() => buildLifecycleHookPayload(shellDraft({ action: { kind: "shell", shell: { executable: "audit-helper", secretRefs: { TOKEN: "vault:AUDIT_TOKEN" } } } })), /env:VARIABLE_NAME/i);
  assert.throws(() => buildLifecycleHookPayload(shellDraft({ action: { kind: "shell", shell: { executable: "audit-helper", env: { "BAD-NAME": "value" } } } })), /Environment variable name/i);
  assert.throws(() => buildLifecycleHookPayload(shellDraft({ action: { kind: "shell", shell: { executable: "audit-helper", env: { TOKEN: "value" }, secretRefs: { token: "env:AUDIT_TOKEN" } } } })), /both env and secretRefs/i);
  assert.throws(() => buildLifecycleHookPayload(shellDraft({ action: { kind: "shell", shell: { executable: "audit-helper", env: { TOKEN: "one", token: "two" } } } })), /unique ignoring case/i);
});

test("HTTP configuration rejects userinfo, illegal protocols, remote plaintext HTTP, and static auth", () => {
  const base = { name: "webhook", event: "run.after", scope: { kind: "global" }, mode: "async", failurePolicy: "continue", action: { kind: "http", http: { url: "https://hooks.example.test/event", method: "POST", secretRefs: { Authorization: "env:HOOK_TOKEN" } } } };
  assert.equal(buildLifecycleHookPayload(base).action.http.url, "https://hooks.example.test/event");
  assert.throws(() => buildLifecycleHookPayload({ ...base, action: { kind: "http", http: { url: "https://user:pass@example.test/event" } } }), /userinfo/i);
  assert.throws(() => buildLifecycleHookPayload({ ...base, action: { kind: "http", http: { url: "https://example.test/event#fragment" } } }), /fragment/i);
  assert.throws(() => buildLifecycleHookPayload({ ...base, action: { kind: "http", http: { url: "ftp://example.test/event" } } }), /HTTPS/i);
  assert.throws(() => buildLifecycleHookPayload({ ...base, action: { kind: "http", http: { url: "http://example.test/event" } } }), /HTTPS/i);
  assert.throws(() => buildLifecycleHookPayload({ ...base, action: { kind: "http", http: { url: "https://example.test", headers: { Authorization: "Bearer raw" } } } }), /secretRefs/i);
  assert.throws(() => buildLifecycleHookPayload({ ...base, action: { kind: "http", http: { url: "https://example.test", headers: { Host: "other.example" } } } }), /Reserved HTTP headers/i);
  assert.throws(() => buildLifecycleHookPayload({ ...base, action: { kind: "http", http: { url: "https://example.test", headers: { "Bad Header": "value" } } } }), /header name/i);
  assert.throws(() => buildLifecycleHookPayload({ ...base, action: { kind: "http", http: { url: "https://example.test", headers: { "X-Test": "one", "x-test": "two" } } } }), /unique ignoring case/i);
  assert.throws(() => buildLifecycleHookPayload({ ...base, action: { kind: "http", http: { url: "https://example.test", secretRefs: { Authorization: "keychain:HOOK_TOKEN" } } } }), /env:VARIABLE_NAME/i);
  assert.throws(() => buildLifecycleHookPayload({ ...base, action: { kind: "http", http: { url: "https://example.test", secretRefs: { Accept: "env:HOOK_TOKEN" } } } }), /Secret HTTP header name/i);
  assert.equal(buildLifecycleHookPayload({ ...base, action: { kind: "http", http: { url: "http://127.0.0.1:7788/event", method: "PATCH" } } }).action.http.method, "PATCH");
});

test("LLM gate payload is limited to synchronous before events", () => {
  const draft = { name: "gate", event: "tool.before", scope: { kind: "agent", id: "agent-1" }, mode: "sync", failurePolicy: "fail_run", action: { kind: "llm", llm: { model: "gateway:model", prompt: "Return strict JSON", maxOutputTokens: 200 } } };
  const payload = buildLifecycleHookPayload(draft);
  assert.equal(payload.action.llm.model, "gateway:model");
  assert.equal(payload.action.llm.maxOutputTokens, 200);
  assert.throws(() => buildLifecycleHookPayload({ ...draft, event: "tool.after" }), /before hooks/i);
});

test("update from a write-only server snapshot omits secretRefs so backend preserves them", () => {
  const normalized = normalizeLifecycleHook(serverHook());
  const payload = buildLifecycleHookPayload(normalized, { expectedRevision: normalized.revision });
  assert.equal(payload.expectedRevision, 3);
  assert.equal(Object.hasOwn(payload.action.shell, "secretRefs"), false);
});

test("controller uses CAS endpoints and history test cancel retry handlers", async () => {
  const queue = requestQueue([
    { hooks: [serverHook()] },
    {}, { hooks: [serverHook({ name: "Updated", revision: 4 })] },
    { execution: { id: "execution-1", status: "succeeded" } }, { history: [{ execution: { id: "execution-1", hookId: "hook-1", status: "succeeded" }, attempts: [{ id: "attempt-1", status: "succeeded", attemptNumber: 1 }] }] },
    { execution: { id: "execution-2", status: "cancelled" } }, { history: [] },
    { execution: { id: "execution-3", status: "pending" } }, { history: [] },
    {},
  ]);
  const controller = createLifecycleHooksController({ request: queue.request });
  assert.equal(await controller.load(), true);
  assert.equal(controller.getState().loaded, true);
  const current = controller.getState().hooks[0];
  assert.equal(await controller.update(current.id, { ...current, name: "Updated" }), true);
  assert.equal(queue.calls[1].path, "/api/lifecycle-hooks/hook-1");
  assert.equal(queue.calls[1].body.expectedRevision, 3);
  assert.equal(Object.hasOwn(queue.calls[1].body.action.shell, "secretRefs"), false);

  assert.equal(await controller.test("hook-1", { name: "tool.after", agentId: "agent-1", toolName: "read_file" }), true);
  assert.equal(queue.calls[3].path, "/api/lifecycle-hooks/hook-1/test");
  assert.equal(controller.getState().history["hook-1"][0].attempts.length, 1);

  assert.equal(await controller.cancel("execution-2", "hook-1"), true);
  assert.equal(queue.calls[5].path, "/api/lifecycle-hook-executions/execution-2/cancel");
  assert.equal(await controller.retry("execution-2", "hook-1"), true);
  assert.equal(queue.calls[7].path, "/api/lifecycle-hook-executions/execution-2/retry");

  assert.equal(await controller.remove("hook-1"), true);
  assert.match(queue.calls[9].path, /expectedRevision=4$/);
  assert.equal(queue.calls[9].options.method, "DELETE");
});

test("rendering escapes content, redacts errors, and uses the independent catalogs", () => {
  const malicious = '"><img src=x onerror="boom">';
  const html = renderLifecycleHooks({ hooks: [serverHook({ id: malicious, name: `<script>boom</script>`, description: malicious })], error: "Bearer private-token" }, { locale: "en" });
  assert.doesNotMatch(html, /<script>|<img src=x/);
  assert.match(html, /&lt;script&gt;boom&lt;\/script&gt;/);
  assert.doesNotMatch(html, /private-token/);
  assert.match(html, /write-only env: references/);
  assert.match(html, /tool approval and audit gateway/);
  assert.match(renderLifecycleHooks({ hooks: [] }, { locale: "zh-CN" }), /生命周期钩子/);
  assert.match(renderLifecycleHooks({ hooks: [] }, { locale: "zh-TW" }), /生命週期鉤子/);
  assert.equal(lifecycleHookMessage("retry", {}, "en"), "Retry");
  assert.equal(redactLifecycleText("token=abc123"), "token=[REDACTED]");
});
