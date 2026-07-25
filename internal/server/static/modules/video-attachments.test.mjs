import test from "node:test";
import assert from "node:assert/strict";

import {
  ALLOWED_VIDEO_MIME_TYPES,
  VIDEO_ATTACHMENT_LIMITS,
  VIDEO_ATTACHMENT_TIMEOUTS,
  constrainVideoFrameSize,
  isSupportedVideoFile,
  openBrowserVideo,
  processVideoAttachment,
  uniformVideoFrameTimes,
} from "./video-attachments.mjs";

function createFile(parts, name, options = {}) {
  const blob = new Blob(parts, options);
  Object.defineProperties(blob, {
    name: { value: name, enumerable: true },
    lastModified: { value: options.lastModified || 0, enumerable: true },
  });
  return blob;
}

function sourceFile({ name = "clip.mp4", type = "video/mp4", size = 1024 } = {}) {
  return { name, type, size };
}

function createManualTimers() {
  let sequence = 0;
  const pending = new Map();
  return {
    setTimeout(callback, milliseconds) {
      const id = ++sequence;
      pending.set(id, { callback, milliseconds });
      return id;
    },
    clearTimeout(id) { pending.delete(id); },
    runNext() {
      const next = [...pending.entries()].sort((a, b) => a[1].milliseconds - b[1].milliseconds || a[0] - b[0])[0];
      if (!next) return false;
      pending.delete(next[0]);
      next[1].callback();
      return true;
    },
    get size() { return pending.size; },
  };
}

function createEventTargetVideo({ metadata = { duration: 2, width: 320, height: 180 }, loadMetadata = false } = {}) {
  const listeners = new Map();
  return {
    duration: metadata.duration,
    videoWidth: metadata.width,
    videoHeight: metadata.height,
    currentTime: 0,
    addEventListener(name, callback) {
      const values = listeners.get(name) || new Set();
      values.add(callback);
      listeners.set(name, values);
    },
    removeEventListener(name, callback) { listeners.get(name)?.delete(callback); },
    emit(name) { [...(listeners.get(name) || [])].forEach((callback) => callback()); },
    load() { if (loadMetadata) queueMicrotask(() => this.emit("loadedmetadata")); },
    pause() {},
    removeAttribute() {},
  };
}

test("video attachments use an explicit MP4/WebM allowlist", () => {
  assert.deepEqual(ALLOWED_VIDEO_MIME_TYPES, ["video/mp4", "video/webm"]);
  assert.equal(isSupportedVideoFile(sourceFile()), true);
  assert.equal(isSupportedVideoFile(sourceFile({ name: "clip.webm", type: "video/webm" })), true);
  assert.equal(isSupportedVideoFile(sourceFile({ name: "clip.mov", type: "video/quicktime" })), false);
  assert.equal(isSupportedVideoFile(sourceFile({ name: "clip.mp4", type: "" })), false);
});

test("frame times are uniform, interior, and capped at six", () => {
  assert.deepEqual(uniformVideoFrameTimes(0), []);
  assert.deepEqual(uniformVideoFrameTimes(0.4), [0.2]);
  assert.deepEqual(uniformVideoFrameTimes(3), [0.5, 1.5, 2.5]);
  assert.deepEqual(uniformVideoFrameTimes(60), [5, 15, 25, 35, 45, 55]);
  assert.deepEqual(uniformVideoFrameTimes(60, 3), [10, 30, 50]);
});

test("frame dimensions preserve aspect ratio and cap the longest edge", () => {
  assert.deepEqual(constrainVideoFrameSize(1920, 1080), { width: 1280, height: 720, scale: 2 / 3 });
  assert.deepEqual(constrainVideoFrameSize(720, 1280), { width: 720, height: 1280, scale: 1 });
  assert.deepEqual(constrainVideoFrameSize(0, 10), { width: 0, height: 0, scale: 0 });
});

test("processing creates frame Files plus a UTF-8 manifest without uploading the source", async () => {
  const captures = [];
  let closed = 0;
  const file = sourceFile({ name: "demo.mp4", size: 9 * 1024 * 1024 });
  const result = await processVideoAttachment(file, {
    createFile,
    openVideo: async () => ({
      metadata: { duration: 3.2, width: 1920, height: 1080 },
      async captureFrame(time, options) {
        captures.push({ time, options });
        return { blob: new Blob([new Uint8Array(100)], { type: "image/jpeg" }), width: 1280, height: 720, type: "image/jpeg" };
      },
      close() { closed += 1; },
    }),
  });

  assert.equal(closed, 1);
  assert.deepEqual(result.times, [0.4, 1.2, 2, 2.8]);
  assert.equal(result.originalIncluded, false);
  assert.equal(result.frameFiles.length, 4);
  assert.equal(result.files.includes(file), false);
  assert.equal(result.files[0], result.frameFiles[0]);
  assert.equal(result.files.at(-1), result.manifestFile);
  assert.equal(result.derivedImageBytes, 400);
  assert.equal(captures.every(({ options }) => options.dimensions.width === 1280 && options.dimensions.height === 720), true);
  assert.equal(captures.every(({ options }) => options.maxBytes <= VIDEO_ATTACHMENT_LIMITS.derivedImageBytes), true);
  const manifest = await result.manifestFile.text();
  assert.match(result.manifestFile.type, /^text\/plain/);
  assert.match(manifest, /Source: demo\.mp4/);
  assert.match(manifest, /Duration: 3\.200 seconds/);
  assert.match(manifest, /Dimensions: 1920x1080/);
  assert.match(manifest, /Sample times: 0\.400s, 1\.200s, 2\.000s, 2\.800s/);
  assert.match(manifest, /visual frames only; audio was not analyzed/);
});

test("processing omits a large original while keeping derived attachments within the message budget", async () => {
  const file = sourceFile({ name: "large.webm", type: "video/webm", size: 11 * 1024 * 1024 });
  const result = await processVideoAttachment(file, {
    currentMessageBytes: 20 * 1024 * 1024,
    createFile,
    openVideo: async () => ({
      metadata: { duration: 2, width: 640, height: 360 },
      captureFrame: async () => ({ blob: new Blob([new Uint8Array(128)], { type: "image/png" }), width: 640, height: 360, type: "image/png" }),
      close() {},
    }),
  });
  assert.equal(result.originalIncluded, false);
  assert.equal(result.files.includes(file), false);
  assert.equal(result.frameFiles.length, 2);
  assert.equal(result.frameFiles.every((frame) => frame.type === "image/png"), true);
  assert.equal(result.totalBytes < 5 * 1024 * 1024, true);
});

test("source and message byte limits fail before unreadable attachments are returned", async () => {
  let opened = 0;
  await assert.rejects(
    processVideoAttachment(sourceFile({ size: VIDEO_ATTACHMENT_LIMITS.sourceBytes + 1 }), {
      createFile,
      openVideo: async () => { opened += 1; return null; },
    }),
    (error) => error.code === "source-too-large",
  );
  assert.equal(opened, 0);

  let closed = 0;
  await assert.rejects(
    processVideoAttachment(sourceFile(), {
      currentMessageBytes: VIDEO_ATTACHMENT_LIMITS.messageBytes,
      createFile,
      openVideo: async () => ({
        metadata: { duration: 1, width: 320, height: 180 },
        captureFrame: async () => ({ blob: new Blob([new Uint8Array(1)], { type: "image/jpeg" }), width: 320, height: 180, type: "image/jpeg" }),
        close() { closed += 1; },
      }),
    }),
    (error) => error.code === "message-budget-exceeded",
  );
  assert.equal(closed, 1);
});

test("processing rejects limits without returning partial unreadable attachments and always closes", async () => {
  let closed = 0;
  await assert.rejects(
    processVideoAttachment(sourceFile(), {
      createFile,
      openVideo: async () => ({
        metadata: { duration: 61, width: 640, height: 360 },
        captureFrame: async () => { throw new Error("must not capture"); },
        close() { closed += 1; },
      }),
    }),
    (error) => error.code === "duration-too-long",
  );
  assert.equal(closed, 1);

  await assert.rejects(
    processVideoAttachment(sourceFile(), {
      createFile,
      openVideo: async () => ({
        metadata: { duration: 1, width: 640, height: 360 },
        captureFrame: async (_time, { maxBytes }) => ({
          blob: new Blob([new Uint8Array(maxBytes + 1)], { type: "image/jpeg" }),
          width: 640,
          height: 360,
          type: "image/jpeg",
        }),
        close() { closed += 1; },
      }),
    }),
    (error) => error.code === "derived-budget-exceeded",
  );
  assert.equal(closed, 2);
});

test("metadata timeout is injectable and revokes an object URL when media events never fire", async () => {
  const timers = createManualTimers();
  const revoked = [];
  const video = createEventTargetVideo();
  const opening = openBrowserVideo(sourceFile(), {
    createObjectURL: () => "blob:metadata-timeout",
    revokeObjectURL: (url) => revoked.push(url),
    createVideo: () => video,
    timers,
    now: () => 0,
    timeouts: { ...VIDEO_ATTACHMENT_TIMEOUTS, metadataMs: 5, totalMs: 50 },
  });
  assert.equal(timers.runNext(), true);
  await assert.rejects(opening, (error) => error.code === "metadata-timeout");
  assert.deepEqual(revoked, ["blob:metadata-timeout"]);
  assert.equal(timers.size, 0);
});

test("seek timeout closes the browser session and revokes its object URL", async () => {
  const timers = createManualTimers();
  const revoked = [];
  const video = createEventTargetVideo();
  const processing = processVideoAttachment(sourceFile(), {
    createFile,
    timers,
    now: () => 0,
    timeouts: { ...VIDEO_ATTACHMENT_TIMEOUTS, metadataMs: 50, seekMs: 5, totalMs: 100 },
    openVideo: (file, options) => openBrowserVideo(file, {
      ...options,
      createObjectURL: () => "blob:seek-timeout",
      revokeObjectURL: (url) => revoked.push(url),
      createVideo: () => video,
      createCanvas: () => { throw new Error("canvas should not be reached"); },
    }),
  });
  video.emit("loadedmetadata");
  for (let index = 0; index < 10; index += 1) await Promise.resolve();
  assert.equal(timers.runNext(), true);
  await assert.rejects(processing, (error) => error.code === "seek-timeout");
  assert.deepEqual(revoked, ["blob:seek-timeout"]);
  assert.equal(timers.size, 0);
});

test("overall processing deadline closes a session whose frame capture never settles", async () => {
  const timers = createManualTimers();
  let closed = 0;
  const processing = processVideoAttachment(sourceFile(), {
    createFile,
    timers,
    now: () => 0,
    timeouts: { ...VIDEO_ATTACHMENT_TIMEOUTS, metadataMs: 100, seekMs: 100, totalMs: 5 },
    openVideo: async () => ({
      metadata: { duration: 1, width: 320, height: 180 },
      captureFrame: () => new Promise(() => {}),
      close() { closed += 1; },
    }),
  });
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(timers.runNext(), true);
  await assert.rejects(processing, (error) => error.code === "processing-timeout");
  assert.equal(closed, 1);
  assert.equal(timers.size, 0);
});

test("metadata cancellation revokes the source URL and clears its timer", async () => {
  const timers = createManualTimers();
  const controller = new AbortController();
  const revoked = [];
  const opening = openBrowserVideo(sourceFile(), {
    createObjectURL: () => "blob:metadata-cancelled",
    revokeObjectURL: (url) => revoked.push(url),
    createVideo: () => createEventTargetVideo(),
    signal: controller.signal,
    timers,
    now: () => 0,
  });
  controller.abort();
  await assert.rejects(opening, (error) => error.code === "cancelled");
  assert.deepEqual(revoked, ["blob:metadata-cancelled"]);
  assert.equal(timers.size, 0);
});

test("cancellation during frame capture closes the active session", async () => {
  const controller = new AbortController();
  let closed = 0;
  const processing = processVideoAttachment(sourceFile(), {
    createFile,
    signal: controller.signal,
    openVideo: async () => ({
      metadata: { duration: 1, width: 320, height: 180 },
      captureFrame: () => new Promise(() => {}),
      close() { closed += 1; },
    }),
  });
  await Promise.resolve();
  controller.abort();
  await assert.rejects(processing, (error) => error.code === "cancelled");
  assert.equal(closed, 1);
});

test("cancellation rejects promptly and closes a session that resolves after cancellation", async () => {
  const controller = new AbortController();
  let resolveOpen;
  let closed = 0;
  const opening = new Promise((resolve) => { resolveOpen = resolve; });
  const processing = processVideoAttachment(sourceFile(), {
    createFile,
    signal: controller.signal,
    openVideo: () => opening,
  });
  controller.abort();
  await assert.rejects(processing, (error) => error.code === "cancelled");
  resolveOpen({
    metadata: { duration: 1, width: 320, height: 180 },
    captureFrame() {},
    close() { closed += 1; },
  });
  await Promise.resolve();
  assert.equal(closed, 1);
});

test("browser video setup revokes its object URL when element creation fails", async () => {
  const revoked = [];
  await assert.rejects(
    openBrowserVideo(sourceFile(), {
      createObjectURL: () => "blob:failed-video",
      revokeObjectURL: (url) => revoked.push(url),
      createVideo: () => { throw new Error("video unavailable"); },
    }),
    /video unavailable/,
  );
  assert.deepEqual(revoked, ["blob:failed-video"]);
});

test("browser video sessions revoke their source object URL on close", async () => {
  const listeners = new Map();
  const revoked = [];
  const video = {
    duration: 2,
    videoWidth: 320,
    videoHeight: 180,
    addEventListener(name, callback) { listeners.set(name, callback); },
    removeEventListener(name) { listeners.delete(name); },
    load() { queueMicrotask(() => listeners.get("loadedmetadata")?.()); },
    pause() {},
    removeAttribute() {},
  };
  const session = await openBrowserVideo(sourceFile(), {
    createObjectURL: () => "blob:video-source",
    revokeObjectURL: (url) => revoked.push(url),
    createVideo: () => video,
  });
  assert.deepEqual(session.metadata, { duration: 2, width: 320, height: 180 });
  session.close();
  session.close();
  assert.deepEqual(revoked, ["blob:video-source"]);
});
