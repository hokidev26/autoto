// Local custom notification clips. The file never leaves this device: the user
// picks it in the page, Autoto sniffs the bytes, and IndexedDB (or an injected
// store in tests) keeps the copy. There is no server upload and no desktop
// path-read API, because either would turn a sound picker into an arbitrary
// file-read.

export const notificationCustomSoundMaxBytes = 1024 * 1024;
export const notificationCustomSoundKey = "custom";
export const notificationCustomSoundDbName = "autoto.notification-sound.v1";

const allowedExtensions = Object.freeze({
  "audio/wav": Object.freeze(["wav"]),
  "audio/mpeg": Object.freeze(["mp3", "mpeg", "mpga"]),
  "audio/ogg": Object.freeze(["ogg", "oga", "opus"]),
  "audio/webm": Object.freeze(["webm"]),
  "audio/mp4": Object.freeze(["m4a", "mp4", "aac"]),
  "audio/flac": Object.freeze(["flac"]),
});

export const notificationCustomSoundAccept = ".mp3,.wav,.ogg,.oga,.opus,.webm,.m4a,.aac,.flac";

export const notificationCustomSoundErrorCodes = Object.freeze({
  required: "required",
  unsupportedType: "unsupported-type",
  tooLarge: "too-large",
  unreadable: "unreadable",
});

function customSoundError(code) {
  const error = new Error(code);
  error.code = code;
  return error;
}

function ascii(bytes, start, length) {
  return Array.from(bytes.slice(start, start + length), (value) => String.fromCharCode(value)).join("");
}

export function notificationSoundFileExtension(filename) {
  const name = String(filename || "").replace(/\\/g, "/").split("/").pop() || "";
  const dot = name.lastIndexOf(".");
  if (dot < 0 || dot === name.length - 1) return "";
  return name.slice(dot + 1).toLowerCase();
}

export function sanitizeNotificationSoundFileName(filename) {
  const base = String(filename || "").replace(/\\/g, "/").split("/").pop() || "";
  const cleaned = base.replace(/[<>:"|?*\u0000-\u001f]/g, "").trim();
  return cleaned.slice(0, 80) || "sound";
}

export function sniffNotificationSoundType(buffer) {
  const bytes = buffer instanceof Uint8Array ? buffer : new Uint8Array(buffer || []);
  if (bytes.length < 4) return "";
  if (bytes.length >= 12 && ascii(bytes, 0, 4) === "RIFF" && ascii(bytes, 8, 4) === "WAVE") return "audio/wav";
  if (ascii(bytes, 0, 4) === "OggS") return "audio/ogg";
  if (ascii(bytes, 0, 4) === "fLaC") return "audio/flac";
  if (ascii(bytes, 0, 3) === "ID3") return "audio/mpeg";
  if (bytes[0] === 0xFF && (bytes[1] & 0xE0) === 0xE0) return "audio/mpeg";
  if (bytes[0] === 0x1A && bytes[1] === 0x45 && bytes[2] === 0xDF && bytes[3] === 0xA3) return "audio/webm";
  if (bytes.length >= 12 && ascii(bytes, 4, 4) === "ftyp") {
    const brand = ascii(bytes, 8, 4);
    if (["M4A ", "M4B ", "mp41", "mp42", "isom", "iso2"].includes(brand)) return "audio/mp4";
  }
  return "";
}

export async function readNotificationSoundFile(file, { maxBytes = notificationCustomSoundMaxBytes } = {}) {
  if (!file) throw customSoundError(notificationCustomSoundErrorCodes.required);
  const size = Number(file.size);
  if (Number.isFinite(size) && size > maxBytes) throw customSoundError(notificationCustomSoundErrorCodes.tooLarge);
  let buffer;
  try {
    buffer = await file.arrayBuffer();
  } catch (error) {
    const failed = customSoundError(notificationCustomSoundErrorCodes.unreadable);
    failed.cause = error;
    throw failed;
  }
  if (!(buffer instanceof ArrayBuffer) || buffer.byteLength <= 0) {
    throw customSoundError(notificationCustomSoundErrorCodes.unreadable);
  }
  if (buffer.byteLength > maxBytes) throw customSoundError(notificationCustomSoundErrorCodes.tooLarge);
  const sniffed = sniffNotificationSoundType(buffer);
  const extension = notificationSoundFileExtension(file.name);
  const allowed = sniffed ? allowedExtensions[sniffed] : null;
  if (!sniffed || !allowed || !allowed.includes(extension)) {
    throw customSoundError(notificationCustomSoundErrorCodes.unsupportedType);
  }
  return {
    name: sanitizeNotificationSoundFileName(file.name),
    type: sniffed,
    bytes: buffer,
  };
}

function recordToClip(record) {
  if (!record?.bytes) return null;
  const bytes = record.bytes instanceof ArrayBuffer ? record.bytes : null;
  if (!bytes || bytes.byteLength <= 0) return null;
  const type = String(record.type || "").trim() || "application/octet-stream";
  const name = sanitizeNotificationSoundFileName(record.name);
  return {
    name,
    type,
    size: bytes.byteLength,
    blob: new Blob([bytes], { type }),
    bytes,
  };
}

export function createMemoryClipStorage(initial = []) {
  const values = new Map(initial);
  return {
    async get(key) {
      return values.has(key) ? values.get(key) : null;
    },
    async set(key, value) {
      values.set(key, value);
    },
    async remove(key) {
      values.delete(key);
    },
  };
}

function createIndexedDbClipStorage(scope) {
  const indexedDB = scope?.indexedDB;
  if (!indexedDB?.open) return null;

  function open() {
    return new Promise((resolve, reject) => {
      let request;
      try {
        request = indexedDB.open(notificationCustomSoundDbName, 1);
      } catch (error) {
        reject(error);
        return;
      }
      request.onupgradeneeded = () => {
        const db = request.result;
        if (!db.objectStoreNames.contains("clips")) db.createObjectStore("clips");
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
  }

  function requestValue(request) {
    return new Promise((resolve, reject) => {
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
  }

  async function withStore(mode, work) {
    const db = await open();
    try {
      const tx = db.transaction("clips", mode);
      const done = new Promise((resolve, reject) => {
        tx.oncomplete = () => resolve();
        tx.onerror = () => reject(tx.error);
        tx.onabort = () => reject(tx.error);
      });
      const result = await work(tx.objectStore("clips"));
      await done;
      return result;
    } finally {
      try { db.close?.(); } catch {}
    }
  }

  return {
    async get(key) {
      return withStore("readonly", async (store) => (await requestValue(store.get(key))) || null);
    },
    async set(key, value) {
      return withStore("readwrite", (store) => requestValue(store.put(value, key)));
    },
    async remove(key) {
      return withStore("readwrite", (store) => requestValue(store.delete(key)));
    },
  };
}

export function createNotificationCustomSoundStore({
  scope = globalThis,
  storage,
  maxBytes = notificationCustomSoundMaxBytes,
} = {}) {
  const persist = storage || createIndexedDbClipStorage(scope) || createMemoryClipStorage();
  let current = null;

  async function load() {
    try {
      current = recordToClip(await persist.get(notificationCustomSoundKey));
    } catch {
      current = null;
    }
    return current;
  }

  async function importFile(file) {
    const record = await readNotificationSoundFile(file, { maxBytes });
    current = recordToClip(record);
    try {
      await persist.set(notificationCustomSoundKey, record);
    } catch {
      // IndexedDB can fail in private mode. Keep the clip for this session so
      // the picker still does something audible instead of looking broken.
    }
    return current;
  }

  async function clear() {
    current = null;
    try {
      await persist.remove(notificationCustomSoundKey);
    } catch {}
    return true;
  }

  return {
    load,
    importFile,
    clear,
    current: () => current,
  };
}
