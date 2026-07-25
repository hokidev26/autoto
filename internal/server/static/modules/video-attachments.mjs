export const VIDEO_ATTACHMENT_LIMITS = Object.freeze({
  sourceBytes: 50 * 1024 * 1024,
  durationSeconds: 60,
  maxFrames: 6,
  maxDimension: 1280,
  derivedImageBytes: 4 * 1024 * 1024,
  messageBytes: 25 * 1024 * 1024,
});

export const ALLOWED_VIDEO_MIME_TYPES = Object.freeze(["video/mp4", "video/webm"]);

export const VIDEO_ATTACHMENT_TIMEOUTS = Object.freeze({
  metadataMs: 10_000,
  seekMs: 8_000,
  totalMs: 30_000,
});

export class VideoAttachmentError extends Error {
  constructor(code, message) {
    super(message || code);
    this.name = "VideoAttachmentError";
    this.code = code;
  }
}

export function isSupportedVideoFile(file) {
  return ALLOWED_VIDEO_MIME_TYPES.includes(String(file?.type || "").trim().toLowerCase());
}

export function uniformVideoFrameTimes(durationSeconds, maxFrames = VIDEO_ATTACHMENT_LIMITS.maxFrames) {
  const duration = Number(durationSeconds);
  if (!Number.isFinite(duration) || duration <= 0) return [];
  const limit = Math.max(1, Math.floor(Number(maxFrames) || 1));
  const count = Math.min(limit, Math.max(1, Math.ceil(duration)));
  return Array.from({ length: count }, (_, index) => {
    const time = duration * ((index + 0.5) / count);
    return Number(Math.min(Math.max(time, 0), duration).toFixed(3));
  });
}

export function constrainVideoFrameSize(width, height, maxDimension = VIDEO_ATTACHMENT_LIMITS.maxDimension) {
  const sourceWidth = Number(width);
  const sourceHeight = Number(height);
  if (!Number.isFinite(sourceWidth) || sourceWidth <= 0 || !Number.isFinite(sourceHeight) || sourceHeight <= 0) {
    return { width: 0, height: 0, scale: 0 };
  }
  const longest = Math.max(sourceWidth, sourceHeight);
  const scale = Math.min(1, Math.max(1, Number(maxDimension) || 1) / longest);
  return {
    width: Math.max(1, Math.round(sourceWidth * scale)),
    height: Math.max(1, Math.round(sourceHeight * scale)),
    scale,
  };
}

function resolvedTimerFunctions(timers = {}) {
  return {
    setTimeout: timers.setTimeout || globalThis.setTimeout,
    clearTimeout: timers.clearTimeout || globalThis.clearTimeout,
  };
}

function cancellationError() {
  return new VideoAttachmentError("cancelled", "Video attachment processing was cancelled.");
}

function throwIfCancelled(signal) {
  if (signal?.aborted) throw cancellationError();
}

function throwIfDeadlineExceeded(deadlineAt, now = Date.now) {
  if (Number.isFinite(deadlineAt) && now() >= deadlineAt) {
    throw new VideoAttachmentError("processing-timeout", "Video attachment processing exceeded its overall deadline.");
  }
}

function timeoutPlan(timeoutMs, deadlineAt, now) {
  const requested = Math.max(1, Number(timeoutMs) || 1);
  const remaining = Number.isFinite(deadlineAt) ? Math.max(0, deadlineAt - now()) : requested;
  return {
    milliseconds: Math.max(0, Math.min(requested, remaining)),
    deadlineWins: remaining <= requested,
  };
}

function waitForOperation(operation, {
  timeoutMs,
  timeoutCode,
  timeoutMessage,
  deadlineAt,
  signal,
  timers,
  now = Date.now,
  onLateResolve,
} = {}) {
  const promise = Promise.resolve(operation);
  const clock = typeof now === "function" ? now : Date.now;
  const timerFunctions = resolvedTimerFunctions(timers);
  const plan = timeoutPlan(timeoutMs, deadlineAt, clock);
  return new Promise((resolve, reject) => {
    let settled = false;
    let timerId;
    const cleanup = () => {
      if (timerId !== undefined) timerFunctions.clearTimeout?.(timerId);
      signal?.removeEventListener?.("abort", onAbort);
    };
    const finish = (callback, value) => {
      if (settled) return false;
      settled = true;
      cleanup();
      callback(value);
      return true;
    };
    const onAbort = () => finish(reject, cancellationError());
    const onTimeout = () => finish(reject, new VideoAttachmentError(
      plan.deadlineWins ? "processing-timeout" : timeoutCode,
      plan.deadlineWins ? "Video attachment processing exceeded its overall deadline." : timeoutMessage,
    ));

    if (signal?.aborted) {
      onAbort();
    } else if (plan.milliseconds <= 0) {
      onTimeout();
    } else {
      signal?.addEventListener?.("abort", onAbort, { once: true });
      timerId = timerFunctions.setTimeout?.(onTimeout, plan.milliseconds);
    }

    promise.then((value) => {
      if (!finish(resolve, value)) onLateResolve?.(value);
    }, (error) => {
      finish(reject, error);
    });
  });
}

function waitForMediaEvent(target, successName, failureNames = ["error", "abort"], {
  timeoutMs,
  timeoutCode,
  timeoutMessage,
  deadlineAt,
  signal,
  timers,
  now = Date.now,
} = {}) {
  const clock = typeof now === "function" ? now : Date.now;
  const timerFunctions = resolvedTimerFunctions(timers);
  const plan = timeoutPlan(timeoutMs, deadlineAt, clock);
  return new Promise((resolve, reject) => {
    let settled = false;
    let timerId;
    const cleanup = () => {
      if (timerId !== undefined) timerFunctions.clearTimeout?.(timerId);
      target.removeEventListener?.(successName, onSuccess);
      failureNames.forEach((name) => target.removeEventListener?.(name, onFailure));
      signal?.removeEventListener?.("abort", onAbort);
    };
    const finish = (callback, value) => {
      if (settled) return;
      settled = true;
      cleanup();
      callback(value);
    };
    const onSuccess = () => finish(resolve);
    const onFailure = () => finish(reject, new VideoAttachmentError("decode-failed", "The browser could not decode this video."));
    const onAbort = () => finish(reject, cancellationError());
    const onTimeout = () => finish(reject, new VideoAttachmentError(
      plan.deadlineWins ? "processing-timeout" : timeoutCode,
      plan.deadlineWins ? "Video attachment processing exceeded its overall deadline." : timeoutMessage,
    ));

    target.addEventListener?.(successName, onSuccess, { once: true });
    failureNames.forEach((name) => target.addEventListener?.(name, onFailure, { once: true }));
    if (signal?.aborted) onAbort();
    else if (plan.milliseconds <= 0) onTimeout();
    else {
      signal?.addEventListener?.("abort", onAbort, { once: true });
      timerId = timerFunctions.setTimeout?.(onTimeout, plan.milliseconds);
    }
  });
}

function canvasToBlob(canvas, type, quality) {
  return new Promise((resolve) => {
    if (typeof canvas?.toBlob !== "function") {
      resolve(null);
      return;
    }
    canvas.toBlob(resolve, type, quality);
  });
}

async function encodeVideoFrame(video, time, dimensions, maxBytes, createCanvas, waitOptions = {}) {
  throwIfCancelled(waitOptions.signal);
  if (Math.abs(Number(video.currentTime || 0) - time) > 0.001) {
    const seeking = waitForMediaEvent(video, "seeked", ["error", "abort"], {
      ...waitOptions,
      timeoutMs: waitOptions.seekTimeoutMs,
      timeoutCode: "seek-timeout",
      timeoutMessage: "Timed out while seeking a video key frame.",
    });
    video.currentTime = time;
    await seeking;
  }
  throwIfCancelled(waitOptions.signal);

  const scales = [1, 0.85, 0.7, 0.55, 0.4];
  const jpegQualities = [0.86, 0.72, 0.58, 0.44, 0.32];
  let smallest = null;
  for (const scale of scales) {
    const canvas = createCanvas();
    canvas.width = Math.max(1, Math.round(dimensions.width * scale));
    canvas.height = Math.max(1, Math.round(dimensions.height * scale));
    const context = canvas.getContext?.("2d", { alpha: false });
    if (!context?.drawImage) throw new VideoAttachmentError("canvas-unavailable", "Canvas video capture is unavailable.");
    context.drawImage(video, 0, 0, canvas.width, canvas.height);

    for (const quality of jpegQualities) {
      const blob = await canvasToBlob(canvas, "image/jpeg", quality);
      if (!blob) continue;
      if (!smallest || blob.size < smallest.size) smallest = blob;
      if (blob.size <= maxBytes) return { blob, width: canvas.width, height: canvas.height, type: "image/jpeg" };
    }

    const png = await canvasToBlob(canvas, "image/png");
    if (png) {
      if (!smallest || png.size < smallest.size) smallest = png;
      if (png.size <= maxBytes) return { blob: png, width: canvas.width, height: canvas.height, type: "image/png" };
    }
  }
  throw new VideoAttachmentError("derived-budget-exceeded", `A key frame requires ${smallest?.size || 0} bytes, exceeding its derived-image budget.`);
}

export async function openBrowserVideo(file, {
  createObjectURL = (value) => URL.createObjectURL(value),
  revokeObjectURL = (value) => URL.revokeObjectURL(value),
  createVideo = () => document.createElement("video"),
  createCanvas = () => document.createElement("canvas"),
  timeouts = VIDEO_ATTACHMENT_TIMEOUTS,
  timers,
  now = Date.now,
  signal,
  deadlineAt,
} = {}) {
  const resolvedTimeouts = { ...VIDEO_ATTACHMENT_TIMEOUTS, ...(timeouts || {}) };
  const clock = typeof now === "function" ? now : Date.now;
  const processingDeadline = Number.isFinite(deadlineAt) ? deadlineAt : clock() + resolvedTimeouts.totalMs;
  throwIfCancelled(signal);
  const objectUrl = createObjectURL(file);
  let video;
  let closed = false;
  try {
    video = createVideo();
    video.preload = "metadata";
    video.muted = true;
    video.playsInline = true;
    const loaded = waitForMediaEvent(video, "loadedmetadata", ["error", "abort"], {
      timeoutMs: resolvedTimeouts.metadataMs,
      timeoutCode: "metadata-timeout",
      timeoutMessage: "Timed out while reading video metadata.",
      deadlineAt: processingDeadline,
      signal,
      timers,
      now: clock,
    });
    video.src = objectUrl;
    video.load?.();
    await loaded;
    const metadata = {
      duration: Number(video.duration),
      width: Number(video.videoWidth),
      height: Number(video.videoHeight),
    };
    return {
      metadata,
      captureFrame: (time, options) => encodeVideoFrame(video, time, options.dimensions, options.maxBytes, createCanvas, {
        seekTimeoutMs: resolvedTimeouts.seekMs,
        deadlineAt: processingDeadline,
        signal,
        timers,
        now: clock,
      }),
      close() {
        if (closed) return;
        closed = true;
        video.pause?.();
        video.removeAttribute?.("src");
        video.load?.();
        revokeObjectURL(objectUrl);
      },
    };
  } catch (error) {
    if (!closed) {
      closed = true;
      video?.pause?.();
      video?.removeAttribute?.("src");
      video?.load?.();
      revokeObjectURL(objectUrl);
    }
    throw error;
  }
}

function defaultCreateFile(parts, name, options) {
  return new File(parts, name, options);
}

function sourceBaseName(name) {
  const value = String(name || "video").replace(/[\\/\0]/g, "_");
  return (value.replace(/\.[^.]+$/, "") || "video").slice(0, 120);
}

function frameExtension(type) {
  return type === "image/png" ? "png" : "jpg";
}

export function createVideoManifest({ sourceName, metadata, times, frames }) {
  const timeList = times.map((time) => `${time.toFixed(3)}s`).join(", ");
  const frameLines = frames.map((frame, index) => (
    `Frame ${index + 1}: ${times[index].toFixed(3)}s, ${frame.width}x${frame.height}, ${frame.file.name}`
  ));
  return [
    "Video key-frame manifest",
    `Source: ${sourceName}`,
    `Duration: ${metadata.duration.toFixed(3)} seconds`,
    `Dimensions: ${metadata.width}x${metadata.height}`,
    `Sample times: ${timeList}`,
    ...frameLines,
    "Analysis scope: visual frames only; audio was not analyzed.",
    "Encoding: UTF-8",
    "",
  ].join("\n");
}

export async function processVideoAttachment(file, {
  currentMessageBytes = 0,
  limits = VIDEO_ATTACHMENT_LIMITS,
  timeouts = VIDEO_ATTACHMENT_TIMEOUTS,
  timers,
  now = Date.now,
  signal,
  openVideo = openBrowserVideo,
  createFile = defaultCreateFile,
  openVideoOptions,
} = {}) {
  const resolvedLimits = { ...VIDEO_ATTACHMENT_LIMITS, ...(limits || {}) };
  const resolvedTimeouts = { ...VIDEO_ATTACHMENT_TIMEOUTS, ...(timeouts || {}) };
  const clock = typeof now === "function" ? now : Date.now;
  const deadlineAt = clock() + resolvedTimeouts.totalMs;
  throwIfCancelled(signal);
  if (!isSupportedVideoFile(file)) {
    throw new VideoAttachmentError("unsupported-type", "Only video/mp4 and video/webm files are supported.");
  }
  if (Number(file?.size || 0) > resolvedLimits.sourceBytes) {
    throw new VideoAttachmentError("source-too-large", "The source video exceeds 50 MiB.");
  }

  let session;
  try {
    const openOptions = {
      ...(openVideoOptions || {}),
      timeouts: resolvedTimeouts,
      timers,
      now: clock,
      signal,
      deadlineAt,
    };
    session = await waitForOperation(openVideo(file, openOptions), {
      timeoutMs: resolvedTimeouts.metadataMs,
      timeoutCode: "metadata-timeout",
      timeoutMessage: "Timed out while reading video metadata.",
      deadlineAt,
      signal,
      timers,
      now: clock,
      onLateResolve: (lateSession) => lateSession?.close?.(),
    });
    throwIfCancelled(signal);
    throwIfDeadlineExceeded(deadlineAt, clock);
    const metadata = {
      duration: Number(session?.metadata?.duration),
      width: Math.round(Number(session?.metadata?.width)),
      height: Math.round(Number(session?.metadata?.height)),
    };
    if (!Number.isFinite(metadata.duration) || metadata.duration <= 0 || metadata.width <= 0 || metadata.height <= 0) {
      throw new VideoAttachmentError("invalid-metadata", "The video metadata is incomplete or unreadable.");
    }
    if (metadata.duration > resolvedLimits.durationSeconds) {
      throw new VideoAttachmentError("duration-too-long", "The video exceeds 60 seconds.");
    }

    const dimensions = constrainVideoFrameSize(metadata.width, metadata.height, resolvedLimits.maxDimension);
    const times = uniformVideoFrameTimes(metadata.duration, resolvedLimits.maxFrames);
    const frames = [];
    let remainingImageBytes = resolvedLimits.derivedImageBytes;
    const baseName = sourceBaseName(file?.name);
    for (let index = 0; index < times.length; index += 1) {
      const remainingFrames = times.length - index;
      throwIfCancelled(signal);
      const maxBytes = Math.max(1, Math.floor(remainingImageBytes / remainingFrames));
      const captured = await waitForOperation(
        session.captureFrame(times[index], { dimensions, maxBytes, index, signal, deadlineAt }),
        {
          timeoutMs: resolvedTimeouts.seekMs,
          timeoutCode: "seek-timeout",
          timeoutMessage: "Timed out while seeking or encoding a video key frame.",
          deadlineAt,
          signal,
          timers,
          now: clock,
        },
      );
      throwIfCancelled(signal);
      throwIfDeadlineExceeded(deadlineAt, clock);
      if (!captured?.blob || !Number.isFinite(captured.blob.size) || captured.blob.size <= 0 || captured.blob.size > maxBytes) {
        throw new VideoAttachmentError("derived-budget-exceeded", "A generated key frame is empty or exceeds the derived-image budget.");
      }
      const type = captured.type === "image/png" || captured.blob.type === "image/png" ? "image/png" : "image/jpeg";
      const timestampMs = Math.round(times[index] * 1000).toString().padStart(5, "0");
      const frameFile = createFile(
        [captured.blob],
        `${baseName}.frame-${String(index + 1).padStart(2, "0")}-${timestampMs}ms.${frameExtension(type)}`,
        { type, lastModified: Date.now() },
      );
      frames.push({ file: frameFile, width: captured.width || dimensions.width, height: captured.height || dimensions.height });
      remainingImageBytes -= frameFile.size;
    }

    throwIfCancelled(signal);
    throwIfDeadlineExceeded(deadlineAt, clock);
    const manifestText = createVideoManifest({ sourceName: file.name || "video", metadata, times, frames });
    const manifestFile = createFile(
      [manifestText],
      `${baseName}.keyframes.txt`,
      { type: "text/plain;charset=utf-8", lastModified: Date.now() },
    );
    const derivedFiles = [...frames.map((frame) => frame.file), manifestFile];
    const derivedBytes = derivedFiles.reduce((sum, item) => sum + Number(item.size || 0), 0);
    const occupiedBytes = Math.max(0, Number(currentMessageBytes) || 0);
    if (occupiedBytes + derivedBytes > resolvedLimits.messageBytes) {
      throw new VideoAttachmentError("message-budget-exceeded", "The derived video attachments exceed the 25 MiB message budget.");
    }
    // The server accepts only image/text attachments. Keep the browser-decoded
    // source local and upload only bounded key frames plus their manifest.
    return {
      files: derivedFiles,
      frameFiles: frames.map((frame) => frame.file),
      manifestFile,
      originalIncluded: false,
      metadata,
      times,
      derivedImageBytes: resolvedLimits.derivedImageBytes - remainingImageBytes,
      totalBytes: derivedBytes,
    };
  } finally {
    session?.close?.();
  }
}
