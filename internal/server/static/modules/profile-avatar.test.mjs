import test from "node:test";
import assert from "node:assert/strict";

import {
  avatarDataUrlByteLength,
  compressProfileAvatar,
  isSupportedProfileAvatarFile,
  normalizeAvatarDataUrl,
  profileAvatarErrorCodes,
  profileAvatarHTML,
  profileAvatarMaxBytes,
} from "./profile-avatar.mjs";

const tinyJPEGDataUrl = "data:image/jpeg;base64,AAAA";

test("avatar data URLs accept only bounded JPEG base64 values", () => {
  assert.equal(normalizeAvatarDataUrl(tinyJPEGDataUrl), tinyJPEGDataUrl);
  assert.equal(avatarDataUrlByteLength(tinyJPEGDataUrl), 3);
  assert.equal(normalizeAvatarDataUrl("data:image/png;base64,AAAA"), "");
  assert.equal(normalizeAvatarDataUrl("data:image/jpeg;base64,not base64"), "");
  assert.equal(normalizeAvatarDataUrl(`data:image/jpeg;base64,${"A".repeat(140_000)}`), "");
  assert.equal(normalizeAvatarDataUrl(`data:image/jpeg;base64,${"A".repeat(Math.ceil((profileAvatarMaxBytes + 1) * 4 / 3))}`), "");
});

test("avatar compressor crops to a square and releases the decoded bitmap", async () => {
  const drawCalls = [];
  let closed = false;
  const bitmap = { width: 400, height: 200, close() { closed = true; } };
  const context = {
    clearRect(...args) { drawCalls.push(["clearRect", ...args]); },
    fillRect(...args) { drawCalls.push(["fillRect", ...args]); },
    drawImage(...args) { drawCalls.push(["drawImage", ...args]); },
    fillStyle: "",
  };
  const canvas = {
    width: 0,
    height: 0,
    getContext() { return context; },
    toDataURL(type) {
      assert.equal(type, "image/jpeg");
      return tinyJPEGDataUrl;
    },
  };
  const result = await compressProfileAvatar({ type: "image/png" }, {
    createImageBitmapImpl: async () => bitmap,
    documentImpl: { createElement: () => canvas },
  });

  assert.deepEqual(result, { dataUrl: tinyJPEGDataUrl, bytes: 3, width: 200, height: 200 });
  assert.deepEqual(drawCalls.find((call) => call[0] === "drawImage"), ["drawImage", bitmap, 100, 0, 200, 200, 0, 0, 200, 200]);
  assert.equal(closed, true);
});

test("avatar compressor rejects non-raster input before reading it", async () => {
  assert.equal(isSupportedProfileAvatarFile({ type: "image/png" }), true);
  assert.equal(isSupportedProfileAvatarFile({ type: "image/svg+xml" }), false);
  await assert.rejects(
    compressProfileAvatar({ type: "image/svg+xml" }, { documentImpl: { createElement: () => ({}) } }),
    (error) => error.code === profileAvatarErrorCodes.unsupportedType,
  );
});

test("profile avatar HTML prefers a data URL and escapes initials", () => {
  assert.equal(
    profileAvatarHTML({ avatarDataUrl: tinyJPEGDataUrl, avatarInitials: "XY" }),
    `<img class="message-avatar-image" data-user-profile-avatar-image src="${tinyJPEGDataUrl}" alt="" aria-hidden="true" />`,
  );
  assert.equal(profileAvatarHTML({ avatarInitials: "r<a" }), "r&lt;a");
  assert.equal(profileAvatarHTML({}), "AT");
});
