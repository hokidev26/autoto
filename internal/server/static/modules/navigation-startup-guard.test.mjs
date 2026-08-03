import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createNavigationStartupGuard } from "./navigation-startup-guard.mjs";

function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}

async function guardedStartup(guard, token, pending, writes) {
  if (!guard.isCurrent(token)) return false;
  await pending.promise;
  if (!guard.isCurrent(token)) return false;
  writes.push("startup");
  return true;
}

async function simulatedInit(guard, token, pending, events) {
  await pending.promise;
  events.push("profile", "model", "navigation-refresh");
  if (guard.isCurrent(token)) events.push("startup-navigation");
  events.push("deep-link-router");
}

async function simulatedProjectSelection(projectId, sequence, current, request, events) {
  const selection = ++sequence.value;
  current.id = projectId;
  const worklines = await request(`/projects/${projectId}/worklines`);
  if (selection !== sequence.value || current.id !== projectId) return;
  current.workline = worklines[0];
  const agents = await request(`/worklines/${current.workline.id}/agents`);
  if (selection !== sequence.value || current.id !== projectId) return;
  current.agent = agents.find((agent) => agent.type === "primary") || agents[0];
  await request(`/agents/${current.agent.id}/messages`);
  if (selection === sequence.value && current.id === projectId) events.push(projectId);
}

async function flushMicrotasks() {
  for (let index = 0; index < 8; index += 1) await Promise.resolve();
}

test("startup without user navigation keeps its token and completes", async () => {
  const guard = createNavigationStartupGuard();
  const token = guard.beginInit(1);
  const pending = deferred();
  const writes = [];
  const run = guardedStartup(guard, token, pending, writes);

  pending.resolve();
  assert.equal(await run, true);
  assert.deepEqual(writes, ["startup"]);
});

test("a project click invalidates a late startup write before it can restore the first project", async () => {
  const guard = createNavigationStartupGuard();
  const token = guard.beginInit(7);
  const pending = deferred();
  const writes = [];
  const run = guardedStartup(guard, token, pending, writes);

  guard.beginUserNavigation();
  pending.resolve();

  assert.equal(await run, false);
  assert.deepEqual(writes, []);
});

test("overview and recent-conversation startup work cannot overwrite a user navigation intent", async () => {
  const guard = createNavigationStartupGuard();
  const token = guard.beginInit(3);
  const overviewLoad = deferred();
  const recentLoad = deferred();
  const writes = [];
  const overview = guardedStartup(guard, token, overviewLoad, writes);
  const recent = guardedStartup(guard, token, recentLoad, writes);

  guard.beginUserNavigation();
  overviewLoad.resolve();
  recentLoad.resolve();

  assert.equal(await overview, false);
  assert.equal(await recent, false);
  assert.deepEqual(writes, []);
});

test("a new init generation invalidates an older startup even without a click", async () => {
  const guard = createNavigationStartupGuard();
  const oldToken = guard.beginInit(10);
  const pending = deferred();
  const writes = [];
  const run = guardedStartup(guard, oldToken, pending, writes);

  guard.beginInit(11);
  pending.resolve();

  assert.equal(await run, false);
  assert.deepEqual(writes, []);
});

test("a new init after user navigation cannot regain startup restore permission", async () => {
  const guard = createNavigationStartupGuard();
  guard.beginInit(20);
  guard.beginUserNavigation();
  const newToken = guard.beginInit(21);
  const pending = deferred();
  const writes = [];
  const run = guardedStartup(guard, newToken, pending, writes);

  pending.resolve();

  assert.equal(await run, false);
  assert.deepEqual(writes, []);
});

test("user navigation remains authoritative across a later init generation", () => {
  const guard = createNavigationStartupGuard();
  const firstStartup = guard.beginInit(1);

  guard.beginUserNavigation();
  const restartedStartup = guard.beginInit(2);

  assert.equal(guard.isCurrent(firstStartup), false);
  assert.equal(guard.isCurrent(restartedStartup), false);
  assert.equal(guard.snapshot().startupBlockedByUser, true);
});

test("a non-user invalidation still allows a later init to restore startup navigation", () => {
  const guard = createNavigationStartupGuard();
  const firstStartup = guard.beginInit(1);

  guard.invalidate();
  const restartedStartup = guard.beginInit(2);

  assert.equal(guard.isCurrent(firstStartup), false);
  assert.equal(guard.isCurrent(restartedStartup), true);
  assert.equal(guard.snapshot().startupBlockedByUser, false);
});

test("global rail clicks claim user navigation before switching the visible workbench", () => {
  const source = readFileSync(new URL("./app-main.mjs", import.meta.url), "utf8");
  const start = source.indexOf("function activateGlobalRailTarget(target)");
  const end = source.indexOf("\n}\n\nfunction ", start);
  const body = source.slice(start, end + 2);
  const claim = body.indexOf("navigationStartupGuard.beginUserNavigation()");
  const key = body.indexOf("const key =");
  const switchWorkbench = body.indexOf("switchPrimaryWorkbench(\"conversation\")");

  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  assert.ok(claim >= 0 && claim < key);
  assert.ok(claim < switchWorkbench);
  assert.match(body, /const openingConversationFromOverview = key === "conversation" && state\.overviewActive/);
  assert.match(body, /state\.startupWorkbenchIntent = state\.initializing \? key : ""/);
  assert.match(body, /!state\.initializing && \(openingConversationFromOverview \|\| !state\.agent\)/);
  assert.match(body, /openDefaultConversationTarget\(\{ preserveMessageState: true \}\)\.catch\(showError\)/);
});

test("a conversation rail click opens the top project after startup data arrives without restoring Home", () => {
  const source = readFileSync(new URL("./app-main.mjs", import.meta.url), "utf8");
  const intentBranch = source.indexOf('if (!state.agent && state.startupWorkbenchIntent === "conversation"');
  const startupBranch = source.indexOf("else if (!state.agent && startupTokenCurrent(startupToken))", intentBranch);
  const intentBody = source.slice(intentBranch, startupBranch);

  assert.ok(intentBranch >= 0 && intentBranch < startupBranch);
  assert.match(intentBody, /state\.startupWorkbenchIntent = ""/);
  assert.match(intentBody, /state\.activeWorkbench === "conversation" && !state\.overviewActive/);
  assert.match(intentBody, /await openDefaultConversationTarget\(\{ preserveMessageState: true \}\)/);
  assert.doesNotMatch(intentBody, /resolveInitialNavigationTarget|applyPrimaryWorkbench\("overview"\)|openOverviewDashboard/);
});

test("A to B to A keeps only the final user navigation current", () => {
  const guard = createNavigationStartupGuard();
  const startup = guard.beginInit(1);
  const a1 = guard.beginUserNavigation();
  const b = guard.beginUserNavigation();
  const a2 = guard.beginUserNavigation();

  assert.equal(guard.isCurrent(startup), false);
  assert.equal(a2 > b && b > a1, true);
  assert.equal(guard.snapshot().navigationIntentSeq, a2);
});

test("invalidating startup keeps profile/model/nav refresh and deep-link init finalization", async () => {
  const guard = createNavigationStartupGuard();
  const token = guard.beginInit(4);
  const pending = deferred();
  const events = [];
  const run = simulatedInit(guard, token, pending, events);

  guard.beginUserNavigation();
  pending.resolve();
  await run;

  assert.deepEqual(events, ["profile", "model", "navigation-refresh", "deep-link-router"]);
});

test("the first project click completes workline, primary agent, and messages exactly once", async () => {
  const sequence = { value: 0 };
  const current = { id: "" };
  const requests = [];
  const events = [];
  const request = (path) => {
    const pending = deferred();
    requests.push({ path, pending });
    return pending.promise;
  };
  const selection = simulatedProjectSelection("p1", sequence, current, request, events);

  assert.equal(requests[0].path, "/projects/p1/worklines");
  requests[0].pending.resolve([{ id: "w1" }]);
  await flushMicrotasks();
  assert.equal(requests[1].path, "/worklines/w1/agents");
  requests[1].pending.resolve([{ id: "a1", type: "primary" }]);
  await flushMicrotasks();
  assert.equal(requests[2].path, "/agents/a1/messages");
  requests[2].pending.resolve([]);
  await selection;

  assert.deepEqual(requests.map(({ path }) => path), [
    "/projects/p1/worklines",
    "/worklines/w1/agents",
    "/agents/a1/messages",
  ]);
  assert.deepEqual(events, ["p1"]);
});

test("auth invalidation can restart single-flight init after old cleanup", async () => {
  let initializing = false;
  let restartRequested = false;
  let runs = 0;
  const guard = createNavigationStartupGuard();
  const pending = deferred();

  async function init() {
    if (initializing) return;
    initializing = true;
    runs += 1;
    const token = guard.beginInit(runs);
    await pending.promise;
    if (!guard.isCurrent(token)) {
      // The old run still performs its non-navigation cleanup before restart.
    }
    initializing = false;
    if (restartRequested) {
      restartRequested = false;
      await init();
    }
  }

  const first = init();
  guard.invalidate();
  restartRequested = true;
  pending.resolve();
  await first;

  assert.equal(runs, 2);
  assert.equal(initializing, false);
});

test("projectSelectSeq-style A to B to A discards stale chains without duplicating final A", async () => {
  const sequence = { value: 0 };
  const current = { id: "" };
  const requests = [];
  const events = [];
  const request = (path) => {
    const pending = deferred();
    requests.push({ path, pending });
    return pending.promise;
  };

  const a1 = simulatedProjectSelection("a", sequence, current, request, events);
  const b = simulatedProjectSelection("b", sequence, current, request, events);
  requests[0].pending.resolve([{ id: "wa" }]);
  await flushMicrotasks();
  requests[1].pending.resolve([{ id: "wb" }]);
  await flushMicrotasks();

  const a2 = simulatedProjectSelection("a", sequence, current, request, events);
  requests[2].pending.resolve([{ id: "ab", type: "primary" }]);
  await flushMicrotasks();
  requests[3].pending.resolve([{ id: "wa2" }]);
  await flushMicrotasks();
  requests[4].pending.resolve([{ id: "aa", type: "primary" }]);
  await flushMicrotasks();
  requests[5].pending.resolve([]);
  await Promise.all([a1, a2, b]);

  assert.deepEqual(events, ["a"]);
  assert.deepEqual(requests.map(({ path }) => path), [
    "/projects/a/worklines",
    "/projects/b/worklines",
    "/worklines/wb/agents",
    "/projects/a/worklines",
    "/worklines/wa2/agents",
    "/agents/aa/messages",
  ]);
});
