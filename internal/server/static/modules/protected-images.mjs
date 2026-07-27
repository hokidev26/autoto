// Protected image assets live behind /api/, which the same-origin guard only
// admits with an X-Autoto-Token request header. A browser cannot attach headers
// to <img src>, so a direct src returns 401 JSON and the browser renders a
// broken-image icon. Every protected <img> is therefore hydrated here: fetch
// through apiDownload, then swap in a blob: URL.
import { apiDownload } from "./runtime.mjs";

// The transcript re-renders by replacing innerHTML, so <img> nodes are thrown
// away and rebuilt constantly. Caching by URL keeps one fetch per asset instead
// of one per render, and keeps the blob URL stable so the decoded image stays in
// the browser's cache and does not visibly re-flash.
const blobURLCache = new Map();
const inflight = new Map();
const cacheLimit = 64;

export const protectedImageAttribute = "data-protected-image";
// Anchors that must save a protected asset. A plain href navigates without the
// token header, so the href is hydrated to a blob: URL the same way.
export const protectedDownloadAttribute = "data-protected-download";

function defaultRuntime() {
  return {
    download: apiDownload,
    createObjectURL: (blob) => globalThis.URL?.createObjectURL?.(blob),
    revokeObjectURL: (url) => globalThis.URL?.revokeObjectURL?.(url),
  };
}

// Evicting the oldest entry bounds memory in a long conversation with many
// images. The revoke is why hydration must tolerate a cached URL going stale:
// callers re-fetch on error rather than trusting the cache forever.
function rememberBlobURL(url, objectURL, revokeObjectURL) {
  blobURLCache.set(url, objectURL);
  while (blobURLCache.size > cacheLimit) {
    const oldest = blobURLCache.keys().next();
    if (oldest.done || oldest.value === url) break;
    const stale = blobURLCache.get(oldest.value);
    blobURLCache.delete(oldest.value);
    if (stale) {
      try { revokeObjectURL(stale); } catch {}
    }
  }
}

export function cachedProtectedImageURL(url) {
  return blobURLCache.get(String(url || "")) || "";
}

export async function loadProtectedImageURL(url, runtime = {}) {
  const path = String(url || "");
  if (!path) throw new Error("missing protected image url");
  const { download, createObjectURL, revokeObjectURL } = { ...defaultRuntime(), ...runtime };
  const cached = blobURLCache.get(path);
  if (cached) return cached;
  const pending = inflight.get(path);
  if (pending) return pending;
  const task = (async () => {
    const response = await download(path);
    const blob = await response.blob();
    const objectURL = createObjectURL(blob);
    if (!objectURL) throw new Error("object URL unavailable");
    rememberBlobURL(path, objectURL, revokeObjectURL);
    return objectURL;
  })().finally(() => { inflight.delete(path); });
  inflight.set(path, task);
  return task;
}

// Drops every cached blob URL. Called when the whole conversation goes away, so
// switching between long image-heavy threads does not accumulate blobs.
export function releaseProtectedImageURLs(runtime = {}) {
  const { revokeObjectURL } = { ...defaultRuntime(), ...runtime };
  for (const objectURL of blobURLCache.values()) {
    try { revokeObjectURL(objectURL); } catch {}
  }
  blobURLCache.clear();
}

// Hydrates one <img>. The source path stays in a data attribute and never in
// src, so a failed hydration cannot fall back to an unauthenticated request that
// would paint the broken-image icon again.
export async function hydrateProtectedImage(image, runtime = {}) {
  if (!image?.getAttribute) return false;
  const path = image.getAttribute(protectedImageAttribute) || "";
  if (!path) return false;
  if (image.dataset?.protectedImageState === "loading") return false;
  const cached = blobURLCache.get(path);
  if (cached && image.getAttribute("src") === cached) {
    if (image.dataset) image.dataset.protectedImageState = "ready";
    return true;
  }
  if (image.dataset) image.dataset.protectedImageState = "loading";
  try {
    const objectURL = await loadProtectedImageURL(path, runtime);
    image.setAttribute("src", objectURL);
    if (image.dataset) image.dataset.protectedImageState = "ready";
    return true;
  } catch (error) {
    if (image.dataset) image.dataset.protectedImageState = "error";
    // A hydration failure is a real load failure. Dispatch the same "error"
    // event a native <img> would so existing placeholder handling still runs.
    try { image.dispatchEvent?.(new globalThis.Event("error")); } catch {}
    if (typeof runtime.onError === "function") runtime.onError(error, path);
    return false;
  }
}

export function hydrateProtectedImages(root, runtime = {}) {
  const nodes = root?.querySelectorAll?.(`img[${protectedImageAttribute}]`);
  if (!nodes) return [];
  return Array.from(nodes).map((image) => hydrateProtectedImage(image, runtime));
}

// Points a download anchor at the blob URL. Done lazily on first pointer/focus
// contact rather than at render time, so listing a conversation does not fetch
// the full-size bytes of every asset just to arm its download link.
export async function hydrateProtectedDownload(anchor, runtime = {}) {
  if (!anchor?.getAttribute) return false;
  const path = anchor.getAttribute(protectedDownloadAttribute) || "";
  if (!path) return false;
  const cached = blobURLCache.get(path);
  if (cached && anchor.getAttribute("href") === cached) return true;
  try {
    anchor.setAttribute("href", await loadProtectedImageURL(path, runtime));
    return true;
  } catch (error) {
    if (typeof runtime.onError === "function") runtime.onError(error, path);
    return false;
  }
}

export function bindProtectedDownloads(root, runtime = {}) {
  const nodes = root?.querySelectorAll?.(`a[${protectedDownloadAttribute}]`);
  if (!nodes) return 0;
  let bound = 0;
  for (const anchor of Array.from(nodes)) {
    if (anchor.dataset?.protectedDownloadBound === "true") continue;
    if (anchor.dataset) anchor.dataset.protectedDownloadBound = "true";
    const arm = () => { hydrateProtectedDownload(anchor, runtime); };
    anchor.addEventListener?.("pointerenter", arm, { once: true });
    anchor.addEventListener?.("focus", arm, { once: true });
    // A click before hydration finishes would follow the unauthenticated href.
    // Swallow that first click and let the hydration complete instead.
    anchor.addEventListener?.("click", async (event) => {
      if (anchor.getAttribute("href")?.startsWith("blob:")) return;
      event.preventDefault?.();
      if (await hydrateProtectedDownload(anchor, runtime)) anchor.click?.();
    });
    bound += 1;
  }
  return bound;
}
