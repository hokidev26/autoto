import test from "node:test";
import assert from "node:assert/strict";

import {
  bindProtectedDownloads,
  cachedProtectedImageURL,
  hydrateProtectedImage,
  hydrateProtectedImages,
  loadProtectedImageURL,
  protectedDownloadAttribute,
  protectedImageAttribute,
  releaseProtectedImageURLs,
} from "./protected-images.mjs";

function fakeImage(path, attribute = protectedImageAttribute) {
  const attributes = { [attribute]: path };
  const events = {};
  return {
    dataset: {},
    attributes,
    dispatched: [],
    getAttribute: (name) => attributes[name] ?? null,
    setAttribute: (name, value) => { attributes[name] = value; },
    addEventListener: (name, handler) => { events[name] = handler; },
    dispatchEvent: (event) => { events[event?.type]?.(event); return true; },
  };
}

function runtimeFor(bodies, { failures = new Set() } = {}) {
  const calls = [];
  const revoked = [];
  let counter = 0;
  return {
    calls,
    revoked,
    runtime: {
      download: async (path) => {
        calls.push(path);
        if (failures.has(path)) throw new Error("401 Unauthorized");
        return { blob: async () => ({ body: bodies[path] ?? path }) };
      },
      createObjectURL: (blob) => `blob:${blob.body}#${++counter}`,
      revokeObjectURL: (url) => revoked.push(url),
    },
  };
}

test("a protected image is fetched with the API token and swapped to a blob URL, never a bare src", async () => {
  releaseProtectedImageURLs({ revokeObjectURL: () => {} });
  const path = "/api/agents/a/messages/m/attachments/at";
  const { runtime, calls } = runtimeFor({ [path]: "png-bytes" });
  const image = fakeImage(path);

  assert.equal(image.getAttribute("src"), null, "must not carry an unauthenticated src before hydration");
  assert.equal(await hydrateProtectedImage(image, runtime), true);
  assert.equal(image.getAttribute("src"), "blob:png-bytes#1");
  assert.equal(image.dataset.protectedImageState, "ready");
  assert.deepEqual(calls, [path]);
});

test("re-rendering the transcript reuses the cached blob instead of refetching per node", async () => {
  releaseProtectedImageURLs({ revokeObjectURL: () => {} });
  const path = "/api/agents/a/messages/m/attachments/cached";
  const { runtime, calls } = runtimeFor({ [path]: "cached-bytes" });

  await hydrateProtectedImage(fakeImage(path), runtime);
  const rerendered = fakeImage(path);
  await hydrateProtectedImage(rerendered, runtime);

  assert.deepEqual(calls, [path], "one fetch per asset, not one per render");
  assert.equal(rerendered.getAttribute("src"), "blob:cached-bytes#1");
  assert.equal(cachedProtectedImageURL(path), "blob:cached-bytes#1");
});

test("concurrent hydration of the same asset shares a single in-flight request", async () => {
  releaseProtectedImageURLs({ revokeObjectURL: () => {} });
  const path = "/api/agents/a/messages/m/attachments/race";
  const { runtime, calls } = runtimeFor({ [path]: "race-bytes" });

  const results = await Promise.all([
    loadProtectedImageURL(path, runtime),
    loadProtectedImageURL(path, runtime),
    loadProtectedImageURL(path, runtime),
  ]);

  assert.deepEqual(calls, [path]);
  assert.deepEqual(new Set(results), new Set(["blob:race-bytes#1"]));
});

test("a failed fetch marks the node and raises the same error event a native img would", async () => {
  releaseProtectedImageURLs({ revokeObjectURL: () => {} });
  const path = "/api/agents/a/messages/m/attachments/denied";
  const { runtime } = runtimeFor({}, { failures: new Set([path]) });
  const image = fakeImage(path);
  let errored = false;
  image.addEventListener("error", () => { errored = true; });

  assert.equal(await hydrateProtectedImage(image, runtime), false);
  assert.equal(image.dataset.protectedImageState, "error");
  assert.equal(errored, true, "placeholder handling relies on the error event");
  assert.equal(image.getAttribute("src"), null, "a failure must not fall back to the 401 URL");
});

test("releasing the cache revokes every blob so switching conversations does not leak", async () => {
  releaseProtectedImageURLs({ revokeObjectURL: () => {} });
  const first = "/api/agents/a/messages/m/attachments/one";
  const second = "/api/agents/a/messages/m/attachments/two";
  const { runtime, revoked } = runtimeFor({ [first]: "one", [second]: "two" });
  await loadProtectedImageURL(first, runtime);
  await loadProtectedImageURL(second, runtime);

  releaseProtectedImageURLs(runtime);

  assert.deepEqual(revoked.sort(), ["blob:one#1", "blob:two#2"]);
  assert.equal(cachedProtectedImageURL(first), "");
});

test("hydrating a subtree covers every protected image in it", async () => {
  releaseProtectedImageURLs({ revokeObjectURL: () => {} });
  const paths = ["/api/x/1", "/api/x/2"];
  const { runtime, calls } = runtimeFor({ "/api/x/1": "a", "/api/x/2": "b" });
  const images = paths.map((path) => fakeImage(path));
  const root = { querySelectorAll: (selector) => (selector.includes(protectedImageAttribute) ? images : []) };

  await Promise.all(hydrateProtectedImages(root, runtime));

  assert.deepEqual(calls.sort(), paths);
  assert.equal(images[0].getAttribute("src").startsWith("blob:"), true);
});

test("a download link is armed lazily and a click before hydration is not allowed to follow the 401 URL", async () => {
  releaseProtectedImageURLs({ revokeObjectURL: () => {} });
  const path = "/api/agents/a/messages/m/generated-images/g?download=1";
  const { runtime, calls } = runtimeFor({ [path]: "download-bytes" });
  const listeners = {};
  let prevented = 0;
  let clicked = 0;
  const anchor = {
    dataset: {},
    attributes: { [protectedDownloadAttribute]: path },
    getAttribute(name) { return this.attributes[name] ?? null; },
    setAttribute(name, value) { this.attributes[name] = value; },
    addEventListener: (name, handler) => { listeners[name] = handler; },
    click: () => { clicked += 1; },
  };
  const root = { querySelectorAll: (selector) => (selector.includes(protectedDownloadAttribute) ? [anchor] : []) };

  assert.equal(bindProtectedDownloads(root, runtime), 1);
  assert.deepEqual(calls, [], "binding must not fetch full-size bytes up front");

  await listeners.click({ preventDefault: () => { prevented += 1; } });
  assert.equal(prevented, 1, "the pre-hydration click must be swallowed");
  assert.equal(anchor.getAttribute("href"), "blob:download-bytes#1");
  assert.equal(clicked, 1, "the download is retried once the blob URL is in place");
});

test("blob and data URLs are already displayable and skip the token fetch", async () => {
  releaseProtectedImageURLs({ revokeObjectURL: () => {} });
  const { runtime, calls } = runtimeFor({});
  assert.equal(await loadProtectedImageURL("blob:http://localhost/preview", runtime), "blob:http://localhost/preview");
  assert.equal(await loadProtectedImageURL("data:image/png;base64,AAAA", runtime), "data:image/png;base64,AAAA");
  assert.deepEqual(calls, []);
});
