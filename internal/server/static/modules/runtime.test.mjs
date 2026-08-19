import test from "node:test";
import assert from "node:assert/strict";

import { api, apiDownload, isAccountSessionAuthorizationFailure, onAPIAuthorizationFailure } from "./runtime.mjs";

test("api synchronously reports 401 and 403 authorization failures", async () => {
  const previousFetch = globalThis.fetch;
  const observed = [];
  const unsubscribe = onAPIAuthorizationFailure((detail) => observed.push(detail));
  try {
    for (const status of [401, 403]) {
      globalThis.fetch = async () => ({
        ok: false,
        status,
        statusText: status === 401 ? "Unauthorized" : "Forbidden",
        text: async () => JSON.stringify({ error: `denied-${status}` }),
      });
      await assert.rejects(api(`/api/failure-${status}`), (error) => {
        assert.equal(error.status, status);
        assert.equal(error.path, `/api/failure-${status}`);
        return true;
      });
    }
  } finally {
    unsubscribe();
    if (previousFetch === undefined) delete globalThis.fetch;
    else globalThis.fetch = previousFetch;
  }

  assert.deepEqual(observed.map((item) => ({ status: item.status, path: item.path })), [
    { status: 401, path: "/api/failure-401" },
    { status: 403, path: "/api/failure-403" },
  ]);
});

test("api reports authorization failure before reading the response body", async () => {
  const previousFetch = globalThis.fetch;
  let releaseBody;
  let resolveObserved;
  const observed = new Promise((resolve) => { resolveObserved = resolve; });
  const unsubscribe = onAPIAuthorizationFailure(resolveObserved);
  globalThis.fetch = async () => ({
    ok: false,
    status: 401,
    statusText: "Unauthorized",
    text: () => new Promise((resolve) => { releaseBody = () => resolve(JSON.stringify({ error: "expired" })); }),
  });
  try {
    const request = api("/api/slow-unauthorized");
    const detail = await observed;
    assert.equal(detail.status, 401);
    assert.equal(detail.path, "/api/slow-unauthorized");
    assert.equal(typeof releaseBody, "function");
    releaseBody();
    await assert.rejects(request, /expired/);
  } finally {
    unsubscribe();
    if (previousFetch === undefined) delete globalThis.fetch;
    else globalThis.fetch = previousFetch;
  }
});

test("api does not report unrelated server failures as authorization changes", async () => {
  const previousFetch = globalThis.fetch;
  let calls = 0;
  const unsubscribe = onAPIAuthorizationFailure(() => { calls += 1; });
  globalThis.fetch = async () => ({
    ok: false,
    status: 500,
    statusText: "Internal Server Error",
    text: async () => JSON.stringify({ error: "boom" }),
  });
  try {
    await assert.rejects(api("/api/failure-500"), /boom/);
    assert.equal(calls, 0);
  } finally {
    unsubscribe();
    if (previousFetch === undefined) delete globalThis.fetch;
    else globalThis.fetch = previousFetch;
  }
});

test("apiDownload preserves the successful response body for Blob downloads", async () => {
  const previousFetch = globalThis.fetch;
  let request = null;
  let textCalls = 0;
  const response = {
    ok: true,
    status: 200,
    statusText: "OK",
    text: async () => { textCalls += 1; return "must-not-read"; },
    blob: async () => new Blob(["credential-json"], { type: "application/json" }),
  };
  globalThis.fetch = async (path, options) => {
    request = { path, options };
    return response;
  };
  try {
    const result = await apiDownload("/api/providers/oauth/codex/accounts/codex_1/export", {
      method: "GET",
      headers: { "X-Autoto-Confirm": "export-codex-account" },
    });
    assert.equal(result, response);
    assert.equal(textCalls, 0);
    assert.equal(request.path, "/api/providers/oauth/codex/accounts/codex_1/export");
    assert.equal(request.options.headers["X-Autoto-Confirm"], "export-codex-account");
  } finally {
    if (previousFetch === undefined) delete globalThis.fetch;
    else globalThis.fetch = previousFetch;
  }
});

test("peer authentication failures are not Autoto account-session failures", () => {
  assert.equal(isAccountSessionAuthorizationFailure({ status: 401, error: new Error("missing or invalid local API token") }), true);
  assert.equal(isAccountSessionAuthorizationFailure({ status: 401, error: new Error("peer authentication failed") }), false);
  assert.equal(isAccountSessionAuthorizationFailure({ status: 409, error: new Error("peer authentication failed") }), false);
  assert.equal(isAccountSessionAuthorizationFailure({ status: 403, error: new Error("forbidden") }), false);
});
