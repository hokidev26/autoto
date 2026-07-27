// Full-size viewer for protected image assets. Opening the raw /api/ URL in a
// new tab cannot work: the tab is a plain navigation with no X-Autoto-Token
// header, so it renders 401 JSON. The viewer therefore reuses the already
// hydrated blob URL and never navigates.
import { loadProtectedImageURL } from "./protected-images.mjs?v=protected-images-1";

const overlayId = "imageLightbox";

function documentRef(runtime = {}) {
  return runtime.documentImpl || globalThis.document || null;
}

export function closeImageLightbox(runtime = {}) {
  const doc = documentRef(runtime);
  const overlay = doc?.getElementById?.(overlayId);
  if (!overlay) return false;
  overlay.remove?.();
  // The scroll lock is applied on open; drop it even if several opens raced.
  if (doc?.body?.classList?.remove) doc.body.classList.remove("image-lightbox-open");
  return true;
}

// Builds the overlay through DOM APIs rather than innerHTML so the caption and
// filename, which come from model output and user uploads, cannot inject markup.
function buildOverlay(doc, { objectURL, caption, downloadName, labels }) {
  const overlay = doc.createElement("div");
  overlay.id = overlayId;
  overlay.className = "image-lightbox";
  overlay.setAttribute("role", "dialog");
  overlay.setAttribute("aria-modal", "true");
  if (caption) overlay.setAttribute("aria-label", caption);

  const frame = doc.createElement("div");
  frame.className = "image-lightbox-frame";

  const image = doc.createElement("img");
  image.className = "image-lightbox-image";
  image.src = objectURL;
  image.alt = caption || "";
  frame.appendChild(image);

  const bar = doc.createElement("div");
  bar.className = "image-lightbox-bar";

  if (caption) {
    const label = doc.createElement("span");
    label.className = "image-lightbox-caption";
    label.textContent = caption;
    label.title = caption;
    bar.appendChild(label);
  }

  const actions = doc.createElement("div");
  actions.className = "image-lightbox-actions";

  // Downloading from the blob URL keeps the save path authenticated-free: the
  // bytes are already in the page, so no second request can 401.
  const download = doc.createElement("a");
  download.className = "image-lightbox-download";
  download.href = objectURL;
  download.download = downloadName || "image";
  download.textContent = "⤓";
  download.setAttribute("aria-label", labels.download || "Download");
  actions.appendChild(download);

  const close = doc.createElement("button");
  close.type = "button";
  close.className = "image-lightbox-close";
  close.textContent = "✕";
  close.setAttribute("aria-label", labels.close || "Close");
  close.setAttribute("data-image-lightbox-close", "");
  actions.appendChild(close);

  bar.appendChild(actions);
  overlay.appendChild(bar);
  overlay.appendChild(frame);
  return { overlay, close };
}

export async function openImageLightbox({ url, caption = "", downloadName = "", labels = {} } = {}, runtime = {}) {
  const doc = documentRef(runtime);
  if (!doc?.createElement || !url) return false;
  let objectURL = "";
  try {
    objectURL = await loadProtectedImageURL(url, runtime);
  } catch {
    return false;
  }
  closeImageLightbox(runtime);
  const { overlay, close } = buildOverlay(doc, { objectURL, caption, downloadName, labels });

  // Clicking the backdrop closes; clicking the image itself must not, so the
  // frame swallows its own clicks.
  overlay.addEventListener("click", (event) => {
    if (event.target === overlay || event.target?.closest?.("[data-image-lightbox-close]")) {
      closeImageLightbox(runtime);
    }
  });
  const onKeyDown = (event) => {
    if (event.key !== "Escape") return;
    closeImageLightbox(runtime);
    doc.removeEventListener?.("keydown", onKeyDown);
  };
  doc.addEventListener?.("keydown", onKeyDown);

  (doc.body || doc.documentElement)?.appendChild?.(overlay);
  if (doc.body?.classList?.add) doc.body.classList.add("image-lightbox-open");
  close.focus?.();
  return true;
}
