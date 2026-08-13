import { escapeAttr, escapeHtml } from "./dom.mjs";

export const profileAvatarMaxDimension = 256;
export const profileAvatarMaxBytes = 96 * 1024;
export const profileAvatarMaxDataUrlLength = 140_000;
export const profileAvatarSupportedInputTypes = Object.freeze(new Set([
  "image/gif",
  "image/jpeg",
  "image/jpg",
  "image/png",
  "image/webp",
]));

const avatarDataUrlPrefix = "data:image/jpeg;base64,";
const avatarDataUrlPattern = /^data:image\/jpeg;base64,([A-Za-z0-9+/]+={0,2})$/i;
const maxSourcePixels = 40_000_000;
const compressionScales = Object.freeze([1, 0.9, 0.78, 0.66, 0.56, 0.45, 0.35]);
const compressionQualities = Object.freeze([0.86, 0.78, 0.7, 0.62, 0.54]);

export const profileAvatarErrorCodes = Object.freeze({
  unsupportedType: "unsupported-type",
  unreadable: "unreadable",
  invalidDimensions: "invalid-dimensions",
  compressionFailed: "compression-failed",
  tooLarge: "too-large",
});

function profileAvatarError(code) {
  const error = new Error(code);
  error.code = code;
  return error;
}

export function avatarDataUrlByteLength(value = "") {
  const source = String(value || "");
  const comma = source.indexOf(",");
  if (comma < 0) return 0;
  const encoded = source.slice(comma + 1);
  if (!encoded) return 0;
  const padding = encoded.endsWith("==") ? 2 : (encoded.endsWith("=") ? 1 : 0);
  return Math.max(0, Math.floor(encoded.length * 3 / 4) - padding);
}

export function profileAvatarHTML(identity = {}) {
  return identity?.avatarDataUrl
    ? `<img class="message-avatar-image" data-user-profile-avatar-image src="${escapeAttr(identity.avatarDataUrl)}" alt="" aria-hidden="true" />`
    : escapeHtml(identity?.avatarInitials || "AT");
}

// The assistant's mark in the main transcript. Exported so the surfaces that
// mirror that transcript head - the background-task pane - show the same
// avatar instead of carrying their own copy of the artwork.
export const assistantAvatarSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false" style="width:14px;height:14px;display:block;"><circle cx="32" cy="32" r="25"/><path d="M21 35c3.2 4 6.8 6 11 6s7.8-2 11-6"/><path d="M23 25h.02M41 25h.02"/></svg>`;

export function normalizeAvatarDataUrl(value = "") {
  const source = String(value || "").trim();
  if (!source) return "";
  if (source.length > profileAvatarMaxDataUrlLength) return "";
  const match = source.match(avatarDataUrlPattern);
  if (!match) return "";
  const encoded = match[1];
  if (encoded.length % 4 !== 0 || avatarDataUrlByteLength(`${avatarDataUrlPrefix}${encoded}`) > profileAvatarMaxBytes) return "";
  if (typeof globalThis.atob === "function") {
    try {
      if (globalThis.atob(encoded).length > profileAvatarMaxBytes) return "";
    } catch {
      return "";
    }
  }
  return `${avatarDataUrlPrefix}${encoded}`;
}

export function isSupportedProfileAvatarFile(file) {
  return profileAvatarSupportedInputTypes.has(String(file?.type || "").toLowerCase());
}

function imageDimensions(source) {
  const width = Number(source?.naturalWidth || source?.width || 0);
  const height = Number(source?.naturalHeight || source?.height || 0);
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0 || width * height > maxSourcePixels) {
    throw profileAvatarError(profileAvatarErrorCodes.invalidDimensions);
  }
  return { width, height };
}

async function loadAvatarSource(file, {
  createImageBitmapImpl = globalThis.createImageBitmap,
  ImageImpl = globalThis.Image,
  URLImpl = globalThis.URL,
} = {}) {
  let objectURL = "";
  let bitmap = null;
  try {
    if (typeof createImageBitmapImpl === "function") {
      try {
        bitmap = await createImageBitmapImpl(file, { imageOrientation: "from-image" });
      } catch {
        bitmap = await createImageBitmapImpl(file);
      }
      return { source: bitmap, ...imageDimensions(bitmap), close: () => bitmap.close?.() };
    }
    if (typeof ImageImpl !== "function" || typeof URLImpl?.createObjectURL !== "function") {
      throw profileAvatarError(profileAvatarErrorCodes.unreadable);
    }
    objectURL = URLImpl.createObjectURL(file);
    const image = await new Promise((resolve, reject) => {
      const element = new ImageImpl();
      element.onload = () => resolve(element);
      element.onerror = () => reject(profileAvatarError(profileAvatarErrorCodes.unreadable));
      element.src = objectURL;
    });
    return { source: image, ...imageDimensions(image), close: () => {} };
  } catch (error) {
    bitmap?.close?.();
    if (error?.code) throw error;
    throw profileAvatarError(profileAvatarErrorCodes.unreadable);
  } finally {
    if (objectURL && typeof URLImpl?.revokeObjectURL === "function") URLImpl.revokeObjectURL(objectURL);
  }
}

function drawAvatarCrop(context, source, width, height, side) {
  const cropSide = Math.min(width, height);
  const sourceX = (width - cropSide) / 2;
  const sourceY = (height - cropSide) / 2;
  context.clearRect(0, 0, side, side);
  context.fillStyle = "#ffffff";
  context.fillRect(0, 0, side, side);
  context.drawImage(source, sourceX, sourceY, cropSide, cropSide, 0, 0, side, side);
}

export async function compressProfileAvatar(file, options = {}) {
  if (!isSupportedProfileAvatarFile(file)) throw profileAvatarError(profileAvatarErrorCodes.unsupportedType);
  const documentImpl = options.documentImpl || globalThis.document;
  if (typeof documentImpl?.createElement !== "function") throw profileAvatarError(profileAvatarErrorCodes.compressionFailed);
  const loaded = await loadAvatarSource(file, options);
  try {
    const canvas = documentImpl.createElement("canvas");
    const context = canvas?.getContext?.("2d", { alpha: false });
    if (!canvas || !context || typeof canvas.toDataURL !== "function") throw profileAvatarError(profileAvatarErrorCodes.compressionFailed);
    const cropSide = Math.min(loaded.width, loaded.height);
    let best = null;
    for (const scale of compressionScales) {
      const side = Math.max(1, Math.min(profileAvatarMaxDimension, Math.round(cropSide * scale)));
      canvas.width = side;
      canvas.height = side;
      drawAvatarCrop(context, loaded.source, loaded.width, loaded.height, side);
      for (const quality of compressionQualities) {
        const dataUrl = String(canvas.toDataURL("image/jpeg", quality) || "");
        const normalized = normalizeAvatarDataUrl(dataUrl);
        if (!normalized) continue;
        const candidate = { dataUrl: normalized, bytes: avatarDataUrlByteLength(normalized), width: side, height: side };
        best = candidate;
        if (candidate.bytes <= profileAvatarMaxBytes) return candidate;
      }
    }
    if (best) throw profileAvatarError(profileAvatarErrorCodes.tooLarge);
    throw profileAvatarError(profileAvatarErrorCodes.compressionFailed);
  } finally {
    loaded.close?.();
  }
}
