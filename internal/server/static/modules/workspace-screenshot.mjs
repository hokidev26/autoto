// Screenshot-the-workspace-preview -> annotate -> add to chat.
//
// The preview panel hosts a cross-origin sandboxed iframe (no allow-same-origin),
// so its pixels cannot be read with a canvas drawImage/getImageData call. Instead
// we capture the whole browser tab via the Screen Capture API
// (getDisplayMedia({ preferCurrentTab: true })) and crop the captured frame down
// to the iframe's on-screen rectangle using plain CSS-pixel-to-captured-pixel
// scaling math (see computeCropRect, which is pure and unit-tested).

const DEFAULT_COLORS = ["#ff4757", "#ffa502", "#2ed573", "#3d7bff", "#ffffff", "#15161a"];

const FALLBACK_LABELS = {
  "workspace.explorer.annotatorPen": "Pen",
  "workspace.explorer.annotatorRect": "Rectangle",
  "workspace.explorer.annotatorArrow": "Arrow",
  "workspace.explorer.annotatorText": "Text",
  "workspace.explorer.annotatorUndo": "Undo",
  "workspace.explorer.annotatorClear": "Clear",
  "workspace.explorer.annotatorAdd": "Add to chat",
  "workspace.explorer.annotatorCancel": "Cancel",
};

function clampNumber(value, min, max) {
  if (!Number.isFinite(value)) return min;
  return Math.min(Math.max(value, min), max);
}

function finiteOr(value, fallback) {
  const num = Number(value);
  return Number.isFinite(num) ? num : fallback;
}

/**
 * Maps a CSS-pixel rectangle measured inside a `winW x winH` window (i.e. the
 * values you get from `element.getBoundingClientRect()` plus
 * `window.innerWidth/innerHeight`) onto the equivalent rectangle within a
 * captured video frame of `srcW x srcH` pixels (as produced by
 * getDisplayMedia, which may be scaled by devicePixelRatio or by the OS
 * picker). Pure math only -- no DOM, no canvas -- so it can be unit tested
 * without a browser.
 *
 * The result is always clamped to the captured frame's bounds and never
 * returns negative width/height.
 *
 * @param {{x?:number, y?:number, left?:number, top?:number, width?:number, height?:number}} rect
 * @param {number} srcW captured frame width in pixels
 * @param {number} srcH captured frame height in pixels
 * @param {number} winW CSS window width the rect was measured against
 * @param {number} winH CSS window height the rect was measured against
 * @returns {{sx:number, sy:number, sw:number, sh:number}}
 */
export function computeCropRect(rect, srcW, srcH, winW, winH) {
  const frameW = Number(srcW);
  const frameH = Number(srcH);
  const windowW = Number(winW);
  const windowH = Number(winH);
  if (!(frameW > 0) || !(frameH > 0) || !(windowW > 0) || !(windowH > 0)) {
    return { sx: 0, sy: 0, sw: 0, sh: 0 };
  }

  const source = rect || {};
  const rawX = finiteOr(source.x, finiteOr(source.left, 0));
  const rawY = finiteOr(source.y, finiteOr(source.top, 0));
  const rawW = Math.max(0, finiteOr(source.width, 0));
  const rawH = Math.max(0, finiteOr(source.height, 0));

  const scaleX = frameW / windowW;
  const scaleY = frameH / windowH;

  const left = clampNumber(rawX * scaleX, 0, frameW);
  const top = clampNumber(rawY * scaleY, 0, frameH);
  const right = clampNumber((rawX + rawW) * scaleX, 0, frameW);
  const bottom = clampNumber((rawY + rawH) * scaleY, 0, frameH);

  return {
    sx: left,
    sy: top,
    sw: Math.max(0, right - left),
    sh: Math.max(0, bottom - top),
  };
}

/**
 * Crops `sourceCanvas` down to `rect` (a CSS-pixel rect measured within a
 * `winW x winH` window) and returns a brand-new canvas containing only that
 * region. Browser-only (uses document.createElement + drawImage).
 */
export function cropCanvas(sourceCanvas, rect, winW, winH) {
  const srcW = Number(sourceCanvas?.width) || 0;
  const srcH = Number(sourceCanvas?.height) || 0;
  const { sx, sy, sw, sh } = computeCropRect(rect, srcW, srcH, winW, winH);
  const outW = Math.max(1, Math.round(sw));
  const outH = Math.max(1, Math.round(sh));

  const canvas = globalThis.document.createElement("canvas");
  canvas.width = outW;
  canvas.height = outH;
  const ctx = canvas.getContext("2d");
  if (ctx && sourceCanvas && sw > 0 && sh > 0) {
    ctx.drawImage(sourceCanvas, sx, sy, sw, sh, 0, 0, outW, outH);
  }
  return canvas;
}

function waitForFirstFrame(video) {
  return new Promise((resolve) => {
    if (typeof video.requestVideoFrameCallback === "function") {
      video.requestVideoFrameCallback(() => resolve());
      return;
    }
    if (video.readyState >= 2 && video.videoWidth > 0) {
      resolve();
      return;
    }
    const onReady = () => resolve();
    video.addEventListener("loadeddata", onReady, { once: true });
    // Some browsers are slow to fire loadeddata for capture streams; don't
    // hang forever waiting for it.
    globalThis.setTimeout(onReady, 300);
  });
}

/**
 * Captures a single frame of the current browser tab using the Screen
 * Capture API, draws it onto a canvas, and immediately stops all tracks so
 * the "you are sharing your screen" browser indicator disappears as soon as
 * possible. Rejects (does not throw synchronously) if the API is unsupported
 * or the user denies the permission prompt -- callers should catch this.
 *
 * @returns {Promise<{canvas: HTMLCanvasElement, srcW: number, srcH: number}>}
 */
export async function captureViewportFrame() {
  const mediaDevices = globalThis.navigator?.mediaDevices;
  if (!mediaDevices?.getDisplayMedia) {
    throw new Error("Screen capture is not supported in this browser.");
  }

  const stream = await mediaDevices.getDisplayMedia({
    video: { frameRate: 5 },
    preferCurrentTab: true,
  });

  const doc = globalThis.document;
  const video = doc.createElement("video");
  video.muted = true;
  video.playsInline = true;
  video.style.position = "fixed";
  video.style.top = "-10000px";
  video.style.left = "-10000px";
  video.style.opacity = "0";
  video.srcObject = stream;
  doc.body?.appendChild(video);

  try {
    await video.play().catch(() => {});
    await waitForFirstFrame(video);
    const track = stream.getVideoTracks?.()[0];
    const settings = track?.getSettings?.() || {};
    const srcW = video.videoWidth || Number(settings.width) || 0;
    const srcH = video.videoHeight || Number(settings.height) || 0;

    const canvas = doc.createElement("canvas");
    canvas.width = Math.max(1, srcW);
    canvas.height = Math.max(1, srcH);
    const ctx = canvas.getContext("2d");
    ctx?.drawImage(video, 0, 0, canvas.width, canvas.height);

    return { canvas, srcW: canvas.width, srcH: canvas.height };
  } finally {
    stream.getTracks().forEach((trackItem) => {
      try {
        trackItem.stop();
      } catch {}
    });
    video.srcObject = null;
    video.remove();
  }
}

function drawArrowHead(ctx, x0, y0, x1, y1, lineWidth) {
  const angle = Math.atan2(y1 - y0, x1 - x0);
  const headLen = Math.max(12, lineWidth * 4);
  ctx.beginPath();
  ctx.moveTo(x1, y1);
  ctx.lineTo(x1 - headLen * Math.cos(angle - Math.PI / 6), y1 - headLen * Math.sin(angle - Math.PI / 6));
  ctx.lineTo(x1 - headLen * Math.cos(angle + Math.PI / 6), y1 - headLen * Math.sin(angle + Math.PI / 6));
  ctx.closePath();
  ctx.fill();
}

/**
 * Opens a full-screen modal annotator over `baseCanvas`: a base screenshot
 * with a transparent drawing canvas on top, plus a toolbar offering pen /
 * rectangle / arrow / text tools, a handful of color swatches, undo, clear,
 * "Add to chat" and "Cancel". Escape cancels. Browser-only.
 *
 * @param {HTMLCanvasElement} baseCanvas the screenshot to annotate
 * @param {{onAdd?: (file: File) => void, onCancel?: () => void, t?: (key: string) => string}} options
 * @returns {HTMLElement|null} the overlay root element (or null if it could not be built)
 */
export function openScreenshotAnnotator(baseCanvas, { onAdd, onCancel, t: translate } = {}) {
  const doc = globalThis.document;
  const tr = typeof translate === "function" ? translate : (key) => FALLBACK_LABELS[key] || key;

  if (!doc || !baseCanvas || !baseCanvas.width || !baseCanvas.height) {
    onCancel?.();
    return null;
  }

  const width = baseCanvas.width;
  const height = baseCanvas.height;

  const drawState = {
    tool: "pen",
    color: DEFAULT_COLORS[0],
    lineWidth: 4,
    drawing: false,
    startX: 0,
    startY: 0,
    strokeSnapshot: null,
    history: [],
    pointerId: null,
  };

  const overlay = doc.createElement("div");
  overlay.className = "workspace-screenshot-overlay";
  overlay.tabIndex = -1;

  const modal = doc.createElement("div");
  modal.className = "workspace-screenshot-modal";
  overlay.appendChild(modal);

  const toolbar = doc.createElement("div");
  toolbar.className = "workspace-screenshot-toolbar";
  modal.appendChild(toolbar);

  const toolGroup = doc.createElement("div");
  toolGroup.className = "workspace-screenshot-tool-group";
  const toolButtons = new Map();
  [
    ["pen", tr("workspace.explorer.annotatorPen")],
    ["rect", tr("workspace.explorer.annotatorRect")],
    ["arrow", tr("workspace.explorer.annotatorArrow")],
    ["text", tr("workspace.explorer.annotatorText")],
  ].forEach(([id, label]) => {
    const button = doc.createElement("button");
    button.type = "button";
    button.className = "workspace-screenshot-tool-btn";
    button.textContent = label;
    button.addEventListener("click", () => setTool(id));
    toolGroup.appendChild(button);
    toolButtons.set(id, button);
  });
  toolbar.appendChild(toolGroup);

  const colorGroup = doc.createElement("div");
  colorGroup.className = "workspace-screenshot-color-group";
  const colorButtons = [];
  DEFAULT_COLORS.forEach((color) => {
    const swatch = doc.createElement("button");
    swatch.type = "button";
    swatch.className = "workspace-screenshot-color-swatch";
    swatch.style.background = color;
    swatch.dataset.color = color;
    swatch.addEventListener("click", () => setColor(color));
    colorGroup.appendChild(swatch);
    colorButtons.push(swatch);
  });
  toolbar.appendChild(colorGroup);

  const actionGroup = doc.createElement("div");
  actionGroup.className = "workspace-screenshot-action-group";
  const undoBtn = doc.createElement("button");
  undoBtn.type = "button";
  undoBtn.className = "workspace-screenshot-secondary-btn";
  undoBtn.textContent = tr("workspace.explorer.annotatorUndo");
  undoBtn.addEventListener("click", undo);
  const clearBtn = doc.createElement("button");
  clearBtn.type = "button";
  clearBtn.className = "workspace-screenshot-secondary-btn";
  clearBtn.textContent = tr("workspace.explorer.annotatorClear");
  clearBtn.addEventListener("click", clearAll);
  actionGroup.appendChild(undoBtn);
  actionGroup.appendChild(clearBtn);
  toolbar.appendChild(actionGroup);

  const spacer = doc.createElement("div");
  spacer.className = "workspace-screenshot-spacer";
  toolbar.appendChild(spacer);

  const cancelBtn = doc.createElement("button");
  cancelBtn.type = "button";
  cancelBtn.className = "workspace-screenshot-cancel-btn";
  cancelBtn.textContent = tr("workspace.explorer.annotatorCancel");
  cancelBtn.addEventListener("click", () => close(true));
  const addBtn = doc.createElement("button");
  addBtn.type = "button";
  addBtn.className = "workspace-screenshot-add-btn";
  addBtn.textContent = tr("workspace.explorer.annotatorAdd");
  addBtn.addEventListener("click", finish);
  toolbar.appendChild(cancelBtn);
  toolbar.appendChild(addBtn);

  const canvasWrap = doc.createElement("div");
  canvasWrap.className = "workspace-screenshot-canvas-wrap";
  modal.appendChild(canvasWrap);

  baseCanvas.className = "workspace-screenshot-base-canvas";
  canvasWrap.appendChild(baseCanvas);

  const annotationCanvas = doc.createElement("canvas");
  annotationCanvas.width = width;
  annotationCanvas.height = height;
  annotationCanvas.className = "workspace-screenshot-annotation-canvas";
  canvasWrap.appendChild(annotationCanvas);
  const actx = annotationCanvas.getContext("2d");

  function setTool(id) {
    drawState.tool = id;
    toolButtons.forEach((button, key) => button.classList.toggle("active", key === id));
  }

  function setColor(color) {
    drawState.color = color;
    colorButtons.forEach((button) => button.classList.toggle("active", button.dataset.color === color));
  }

  function pointerPos(event) {
    const rect = annotationCanvas.getBoundingClientRect();
    const scaleX = rect.width > 0 ? width / rect.width : 1;
    const scaleY = rect.height > 0 ? height / rect.height : 1;
    return {
      x: (event.clientX - rect.left) * scaleX,
      y: (event.clientY - rect.top) * scaleY,
    };
  }

  function pushHistory() {
    if (!actx) return;
    drawState.history.push(actx.getImageData(0, 0, width, height));
    if (drawState.history.length > 30) drawState.history.shift();
  }

  function undo() {
    if (!actx || !drawState.history.length) return;
    const previous = drawState.history.pop();
    actx.putImageData(previous, 0, 0);
  }

  function clearAll() {
    if (!actx) return;
    pushHistory();
    actx.clearRect(0, 0, width, height);
  }

  function drawRect(x0, y0, x1, y1) {
    actx.lineWidth = drawState.lineWidth;
    actx.strokeStyle = drawState.color;
    actx.strokeRect(Math.min(x0, x1), Math.min(y0, y1), Math.abs(x1 - x0), Math.abs(y1 - y0));
  }

  function drawArrow(x0, y0, x1, y1) {
    actx.lineWidth = drawState.lineWidth;
    actx.strokeStyle = drawState.color;
    actx.fillStyle = drawState.color;
    actx.beginPath();
    actx.moveTo(x0, y0);
    actx.lineTo(x1, y1);
    actx.stroke();
    drawArrowHead(actx, x0, y0, x1, y1, drawState.lineWidth);
  }

  function startText(event) {
    const pos = pointerPos(event);
    const rect = annotationCanvas.getBoundingClientRect();
    const displayScaleX = width > 0 ? rect.width / width : 1;
    const displayScaleY = height > 0 ? rect.height / height : 1;

    const input = doc.createElement("input");
    input.type = "text";
    input.className = "workspace-screenshot-text-input";
    input.style.left = `${pos.x * displayScaleX}px`;
    input.style.top = `${pos.y * displayScaleY}px`;
    input.style.color = drawState.color;
    canvasWrap.appendChild(input);
    input.focus();

    let settled = false;
    function commit() {
      if (settled) return;
      settled = true;
      const value = input.value.trim();
      input.remove();
      if (!value || !actx) return;
      pushHistory();
      actx.fillStyle = drawState.color;
      actx.font = `${Math.max(18, drawState.lineWidth * 6)}px sans-serif`;
      actx.textBaseline = "top";
      actx.fillText(value, pos.x, pos.y);
    }
    function discard() {
      if (settled) return;
      settled = true;
      input.remove();
    }
    input.addEventListener("keydown", (event2) => {
      if (event2.key === "Enter") {
        event2.preventDefault();
        commit();
      } else if (event2.key === "Escape") {
        event2.preventDefault();
        discard();
      }
    });
    input.addEventListener("blur", commit);
  }

  function onPointerDown(event) {
    if (drawState.tool === "text") {
      startText(event);
      return;
    }
    if (!actx) return;
    event.preventDefault();
    annotationCanvas.setPointerCapture?.(event.pointerId);
    drawState.pointerId = event.pointerId;
    pushHistory();
    const pos = pointerPos(event);
    drawState.drawing = true;
    drawState.startX = pos.x;
    drawState.startY = pos.y;
    if (drawState.tool === "pen") {
      actx.beginPath();
      actx.moveTo(pos.x, pos.y);
      actx.lineCap = "round";
      actx.lineJoin = "round";
    } else {
      drawState.strokeSnapshot = actx.getImageData(0, 0, width, height);
    }
  }

  function onPointerMove(event) {
    if (!drawState.drawing || !actx) return;
    const pos = pointerPos(event);
    if (drawState.tool === "pen") {
      actx.lineWidth = drawState.lineWidth;
      actx.strokeStyle = drawState.color;
      actx.lineTo(pos.x, pos.y);
      actx.stroke();
    } else if (drawState.tool === "rect" && drawState.strokeSnapshot) {
      actx.putImageData(drawState.strokeSnapshot, 0, 0);
      drawRect(drawState.startX, drawState.startY, pos.x, pos.y);
    } else if (drawState.tool === "arrow" && drawState.strokeSnapshot) {
      actx.putImageData(drawState.strokeSnapshot, 0, 0);
      drawArrow(drawState.startX, drawState.startY, pos.x, pos.y);
    }
  }

  function onPointerUp(event) {
    if (!drawState.drawing) return;
    drawState.drawing = false;
    drawState.strokeSnapshot = null;
    if (drawState.pointerId != null) annotationCanvas.releasePointerCapture?.(drawState.pointerId);
    drawState.pointerId = null;
  }

  annotationCanvas.addEventListener("pointerdown", onPointerDown);
  annotationCanvas.addEventListener("pointermove", onPointerMove);
  annotationCanvas.addEventListener("pointerup", onPointerUp);
  annotationCanvas.addEventListener("pointercancel", onPointerUp);

  function onKeyDown(event) {
    if (event.key === "Escape") {
      event.preventDefault();
      close(true);
    }
  }
  doc.addEventListener("keydown", onKeyDown);

  overlay.addEventListener("mousedown", (event) => {
    if (event.target === overlay) close(true);
  });

  function finish() {
    const output = doc.createElement("canvas");
    output.width = width;
    output.height = height;
    const octx = output.getContext("2d");
    octx?.drawImage(baseCanvas, 0, 0, width, height);
    if (actx) octx?.drawImage(annotationCanvas, 0, 0, width, height);
    output.toBlob((blob) => {
      if (blob) {
        const file = new File([blob], "screenshot.png", { type: "image/png" });
        onAdd?.(file);
      }
      close(false);
    }, "image/png");
  }

  function close(triggerCancel) {
    doc.removeEventListener("keydown", onKeyDown);
    overlay.remove();
    if (triggerCancel) onCancel?.();
  }

  setTool("pen");
  setColor(DEFAULT_COLORS[0]);
  doc.body?.appendChild(overlay);
  overlay.focus?.();

  return overlay;
}

/**
 * Orchestrates the whole flow: capture the tab, crop it down to the preview
 * iframe's rectangle, then open the annotator. Never throws -- any failure
 * (unsupported API, denied permission, missing iframe, ...) is routed to
 * `onError` and otherwise ignored so the rest of the page keeps working.
 *
 * @param {{iframeEl?: Element, onFile?: (file: File) => void, t?: (key: string) => string, onError?: (error: unknown) => void}} options
 */
export async function runPreviewScreenshot({ iframeEl, onFile, t: translate, onError } = {}) {
  try {
    if (!iframeEl || typeof iframeEl.getBoundingClientRect !== "function") {
      throw new Error("Preview iframe is not available.");
    }
    const rect = iframeEl.getBoundingClientRect();
    if (!(rect.width > 0) || !(rect.height > 0)) {
      throw new Error("Preview iframe has no visible area.");
    }

    const { canvas } = await captureViewportFrame();
    const winW = globalThis.innerWidth || globalThis.document?.documentElement?.clientWidth || rect.width;
    const winH = globalThis.innerHeight || globalThis.document?.documentElement?.clientHeight || rect.height;
    const cropped = cropCanvas(canvas, rect, winW, winH);

    openScreenshotAnnotator(cropped, {
      onAdd: (file) => onFile?.(file),
      onCancel: () => {},
      t: translate,
    });
  } catch (error) {
    onError?.(error);
  }
}
