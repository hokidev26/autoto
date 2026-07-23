import test from "node:test";
import assert from "node:assert/strict";

import { computeCropRect } from "./workspace-screenshot.mjs";

test("identical window/frame sizes map 1:1", () => {
  const rect = { x: 10, y: 20, width: 100, height: 50 };
  assert.deepEqual(computeCropRect(rect, 800, 600, 800, 600), { sx: 10, sy: 20, sw: 100, sh: 50 });
});

test("uniformly scaled frame doubles every coordinate", () => {
  const rect = { x: 10, y: 20, width: 100, height: 50 };
  assert.deepEqual(computeCropRect(rect, 1600, 1200, 800, 600), { sx: 20, sy: 40, sw: 200, sh: 100 });
});

test("independent horizontal/vertical scale factors are applied separately", () => {
  // Frame is 2x wider but 1x as tall as the window (e.g. an odd capture aspect ratio).
  const rect = { x: 10, y: 20, width: 100, height: 50 };
  assert.deepEqual(computeCropRect(rect, 1600, 600, 800, 600), { sx: 20, sy: 20, sw: 200, sh: 50 });
});

test("rect overflowing the right/bottom edge of the frame is clamped", () => {
  const rect = { x: 750, y: 570, width: 100, height: 100 };
  assert.deepEqual(computeCropRect(rect, 800, 600, 800, 600), { sx: 750, sy: 570, sw: 50, sh: 30 });
});

test("rect starting off-screen to the left/top is clamped and shrinks accordingly", () => {
  const rect = { x: -30, y: -10, width: 100, height: 40 };
  assert.deepEqual(computeCropRect(rect, 800, 600, 800, 600), { sx: 0, sy: 0, sw: 70, sh: 30 });
});

test("rect entirely outside the frame on one axis collapses that axis to zero width/height", () => {
  const rect = { x: 900, y: 0, width: 50, height: 50 };
  assert.deepEqual(computeCropRect(rect, 800, 600, 800, 600), { sx: 800, sy: 0, sw: 0, sh: 50 });
});

test("supports DOMRect-style objects that only expose left/top instead of x/y", () => {
  const rect = { left: 10, top: 20, width: 100, height: 50 };
  assert.deepEqual(computeCropRect(rect, 800, 600, 800, 600), { sx: 10, sy: 20, sw: 100, sh: 50 });
});

test("negative width/height on the input rect are treated as zero, not mirrored", () => {
  const rect = { x: 10, y: 10, width: -50, height: -20 };
  assert.deepEqual(computeCropRect(rect, 800, 600, 800, 600), { sx: 10, sy: 10, sw: 0, sh: 0 });
});

test("zero or negative frame/window dimensions guard to an all-zero rect", () => {
  const rect = { x: 10, y: 20, width: 100, height: 50 };
  assert.deepEqual(computeCropRect(rect, 0, 600, 800, 600), { sx: 0, sy: 0, sw: 0, sh: 0 });
  assert.deepEqual(computeCropRect(rect, 800, -1, 800, 600), { sx: 0, sy: 0, sw: 0, sh: 0 });
  assert.deepEqual(computeCropRect(rect, 800, 600, 0, 600), { sx: 0, sy: 0, sw: 0, sh: 0 });
  assert.deepEqual(computeCropRect(rect, 800, 600, 800, -10), { sx: 0, sy: 0, sw: 0, sh: 0 });
});

test("missing/undefined rect and non-finite fields degrade to zero instead of throwing", () => {
  assert.deepEqual(computeCropRect(undefined, 800, 600, 800, 600), { sx: 0, sy: 0, sw: 0, sh: 0 });
  assert.deepEqual(computeCropRect({}, 800, 600, 800, 600), { sx: 0, sy: 0, sw: 0, sh: 0 });
  const rect = { x: Number.NaN, y: 20, width: 100, height: 50 };
  assert.deepEqual(computeCropRect(rect, 800, 600, 800, 600), { sx: 0, sy: 20, sw: 100, sh: 50 });
});
