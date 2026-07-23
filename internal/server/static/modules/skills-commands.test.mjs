import test from "node:test";
import assert from "node:assert/strict";

import { mergeAuthoritativeEffectiveCommands, mergeBuiltInSlashCommands, mergeEffectiveOwnerCommands, mergeSlashCommands, normalizeSlashCommandName, slashCommandInsertion, visibleMessageText } from "./skills-commands.mjs";
import { applyServerSkillsLoadResult, hydrateServerSkillSummaries, isOptimisticSkillConflict, loadServerSkillsWithFallback } from "./skills-bootstrap.mjs";

test("mergeSlashCommands keeps enabled server skills before local fallbacks", () => {
  assert.deepEqual(mergeSlashCommands([
    { id: "server-1", command: "/Review", description: "server", prompt: "server prompt", enabled: true, scanVerdict: "safe" },
    { id: "server-2", command: "/disabled", prompt: "ignored", enabled: false, scanVerdict: "safe" },
  ], [
    { id: "local-1", name: "/review", description: "local", prompt: "local prompt", enabled: true },
    { id: "local-2", name: "write-tests", prompt: "test prompt", enabled: true },
  ]), [
    { id: "server-server-1", name: "/review", description: "server", prompt: "", source: "server" },
    { id: "local-local-2", name: "/write-tests", description: "", prompt: "test prompt", source: "local" },
  ]);
});

test("server records reserve commands and fail closed before local fallback", () => {
  assert.deepEqual(mergeSlashCommands([
    { id: "disabled", command: " //Review ", prompt: "server", enabled: false, scanVerdict: "safe" },
    { id: "blocked", command: "/blocked", prompt: "server", enabled: true, scanVerdict: "blocked" },
    { id: "review-unacked", command: "/review", prompt: "server", enabled: true, scanVerdict: "review" },
    { id: "review-stale", command: "/stale", prompt: "server", enabled: true, scanVerdict: "review", contentHash: "hash-new", riskAcknowledgedAt: "2025-01-01T00:00:00Z", riskAcknowledgedBy: "api_request", riskAcknowledgedHash: "hash-old" },
    { id: "review-acked", command: "/approved", prompt: "server", enabled: true, scanVerdict: "review", contentHash: "hash-1", riskAcknowledgedAt: "2025-01-01T00:00:00Z", riskAcknowledgedBy: "api_request", riskAcknowledgedHash: "hash-1" },
  ], [
    { id: "local-review", name: "/review", prompt: "local review", enabled: true },
    { id: "local-blocked", name: "/blocked", prompt: "local blocked", enabled: true },
    { id: "local-stale", name: "/stale", prompt: "local stale", enabled: true },
    { id: "local-approved", name: "/approved", prompt: "local approved", enabled: true },
  ]), [
    { id: "server-review-acked", name: "/approved", description: "", prompt: "", source: "server" },
  ]);
});

test("server and built-in command selection inserts only the command while local templates insert prompts", () => {
  assert.equal(slashCommandInsertion({ name: "/review", prompt: "secret server prompt", source: "server" }), "/review ");
  assert.equal(slashCommandInsertion({ name: "/goal", prompt: "ignored built-in prompt", source: "builtin" }), "/goal ");
  assert.equal(slashCommandInsertion({ name: "/local", prompt: "expanded local prompt", source: "local" }), "expanded local prompt");
  assert.deepEqual(mergeSlashCommands([
    { id: "summary-only", command: "/summary-only", description: "server summary", enabled: true, scanVerdict: "safe" },
  ], []), [
    { id: "server-summary-only", name: "/summary-only", description: "server summary", prompt: "", source: "server" },
  ]);
});

test("built-in commands stay first and reserve their names from skills and templates", () => {
  const external = mergeSlashCommands([
    { id: "server-goal", command: "/goal", description: "shadowing skill", enabled: true, scanVerdict: "safe" },
    { id: "server-review", command: "/review", description: "server review", enabled: true, scanVerdict: "safe" },
  ], [
    { id: "local-goal", name: "/Goal", prompt: "shadowing template", enabled: true },
    { id: "local-tests", name: "/write-tests", prompt: "write tests", enabled: true },
  ]);
  assert.deepEqual(mergeBuiltInSlashCommands([
    { id: "builtin-goal", name: "/Goal", description: "Add a protected task to the task list", prompt: "ignored" },
  ], external), [
    { id: "builtin-goal", name: "/goal", description: "Add a protected task to the task list", prompt: "", source: "builtin" },
    { id: "server-server-review", name: "/review", description: "server review", prompt: "", source: "server" },
    { id: "local-local-tests", name: "/write-tests", description: "", prompt: "write tests", source: "local" },
  ]);
});

test("user message rendering and copying prefer commandText", () => {
  assert.equal(visibleMessageText({ role: "user", contentText: "private expanded prompt", commandText: "/review src/main.go" }), "/review src/main.go");
  assert.equal(visibleMessageText({ role: "assistant", contentText: "answer", commandText: "/ignored" }), "answer");
  assert.equal(visibleMessageText({ role: "user", contentText: "ordinary", commandText: "" }), "ordinary");
});

test("normalizeSlashCommandName canonicalizes slashes, trim, and case", () => {
  assert.equal(normalizeSlashCommandName(" //Review "), "/review");
  assert.equal(normalizeSlashCommandName("write-tests"), "/write-tests");
  assert.equal(normalizeSlashCommandName("  /  "), "");
});

test("loadServerSkillsWithFallback preserves local-template compatibility on initial API failure", async () => {
  const result = await loadServerSkillsWithFallback(async () => { throw new Error("skills unavailable"); });
  assert.deepEqual(result.skills, []);
  assert.equal(result.error, "skills unavailable");
});

test("loadServerSkillsWithFallback retains last-known server policy on refresh failure", async () => {
  const previous = [{ id: "blocked", command: "/blocked", enabled: false, scanVerdict: "blocked" }];
  const result = await loadServerSkillsWithFallback(async () => { throw new Error("offline"); }, previous);
  assert.equal(result.skills, previous);
  assert.equal(result.status, "stale");
  assert.equal(result.error, "offline");
});

test("load state applies only the latest sequence and distinguishes ready/error/stale", async () => {
  const state = { serverSkillsLoadSeq: 2, serverSkills: [], serverSkillsStatus: "loading", serverSkillsError: "" };
  assert.equal(applyServerSkillsLoadResult(state, 1, { skills: [{ id: "old" }], status: "ready" }), false);
  assert.deepEqual(state.serverSkills, []);
  assert.equal(applyServerSkillsLoadResult(state, 2, { skills: [{ id: "new" }], status: "ready" }), true);
  assert.equal(state.serverSkillsStatus, "ready");
  const stale = await loadServerSkillsWithFallback(async () => { throw new Error("offline"); }, [], { hadServerData: true });
  assert.equal(stale.status, "stale");
  const failed = await loadServerSkillsWithFallback(async () => { throw new Error("offline"); });
  assert.equal(failed.status, "error");
});

test("only optimistic-lock 409 responses trigger conflict refresh", () => {
  assert.equal(isOptimisticSkillConflict({
    status: 409,
    body: { error: "conflict: skill was updated by another client" },
  }), true);
  assert.equal(isOptimisticSkillConflict({
    status: 409,
    body: { error: "conflict: skill command already exists" },
  }), false);
  assert.equal(isOptimisticSkillConflict({ status: 500, message: "skill was updated by another client" }), false);
});

test("summary hydration merges enabled details and fails closed on detail errors", async () => {
  const hydrated = await hydrateServerSkillSummaries([
    { id: "enabled", command: "/enabled", enabled: true, findingCount: 1 },
    { id: "disabled", command: "/disabled", enabled: false, findingCount: 0 },
    { id: "broken", command: "/broken", enabled: true, findingCount: 2 },
  ], async (id) => {
    if (id === "broken") throw new Error("detail unavailable");
    return { id, prompt: "safe prompt", scanFindings: [] };
  });
  assert.equal(hydrated[0].detailLoaded, true);
  assert.equal(hydrated[0].prompt, "safe prompt");
  assert.equal(hydrated[1].detailLoaded, false);
  assert.equal(hydrated[1].prompt, undefined);
  assert.equal(hydrated[2].detailLoaded, false);
  assert.equal(hydrated[2].prompt, undefined);
  assert.equal(hydrated[2].detailError, "detail unavailable");
});

test("effective owners reserve command names before browser-local fallbacks", () => {
  const commands = mergeEffectiveOwnerCommands({
    items: [
      { owner: { id: "workspace-owner", command: "/review", enabled: false, scanVerdict: "safe" } },
      { owner: { id: "global-owner", command: "/summarize", enabled: true, scanVerdict: "safe" } },
      { owner: { id: "blocked-owner", command: "/blocked", enabled: true, scanVerdict: "blocked" } },
    ],
  }, [
    { id: "local-review", name: "/review", prompt: "local review", enabled: true },
    { id: "local-blocked", name: "/blocked", prompt: "local blocked", enabled: true },
    { id: "local-other", name: "/other", prompt: "local other", enabled: true },
  ]);
  assert.deepEqual(commands, [
    { id: "server-global-owner", name: "/summarize", description: "", prompt: "", source: "server" },
    { id: "local-local-other", name: "/other", description: "", prompt: "local other", source: "local" },
  ]);
});

test("authoritative effective policy fails closed initially and permits stale last-known policy", () => {
  const local = [{ id: "local", name: "/local", prompt: "local prompt", enabled: true }];
  assert.deepEqual(mergeAuthoritativeEffectiveCommands({
    items: [], status: "error", hasAuthoritativeData: false,
  }, local), []);
  assert.deepEqual(mergeAuthoritativeEffectiveCommands({
    items: [], status: "stale", hasAuthoritativeData: true,
  }, local), [
    { id: "local-local", name: "/local", description: "", prompt: "local prompt", source: "local" },
  ]);
});

test("disabled project and workspace owners explicitly shadow enabled lower layers and local templates", () => {
  const commands = mergeAuthoritativeEffectiveCommands({
    hasAuthoritativeData: true,
    items: [
      { id: "project-owner", command: "/project-shadow", scope: "project", enabled: false, scanVerdict: "safe" },
      { id: "workspace-owner", command: "/workspace-shadow", scope: "workspace", enabled: false, scanVerdict: "safe" },
    ],
  }, [
    { id: "local-project", name: "/project-shadow", prompt: "local project", enabled: true },
    { id: "local-workspace", name: "/workspace-shadow", prompt: "local workspace", enabled: true },
    { id: "local-other", name: "/other", prompt: "local other", enabled: true },
  ]);
  assert.deepEqual(commands, [
    { id: "local-local-other", name: "/other", description: "", prompt: "local other", source: "local" },
  ]);
});
