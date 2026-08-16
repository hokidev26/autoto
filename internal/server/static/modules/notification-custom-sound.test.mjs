import assert from "node:assert/strict";
import test from "node:test";

import {
  createMemoryClipStorage,
  createNotificationCustomSoundStore,
  notificationCustomSoundErrorCodes,
  notificationCustomSoundMaxBytes,
  readNotificationSoundFile,
  sanitizeNotificationSoundFileName,
  sniffNotificationSoundType,
} from "./notification-custom-sound.mjs";

function wavBytes() {
  const bytes = new Uint8Array(12);
  bytes.set([0x52, 0x49, 0x46, 0x46], 0);
  bytes.set([0x57, 0x41, 0x56, 0x45], 8);
  return bytes.buffer;
}

function fakeFile({ name, type, bytes, size }) {
  const buffer = bytes instanceof ArrayBuffer ? bytes : bytes.buffer;
  return {
    name,
    type,
    size: size ?? buffer.byteLength,
    arrayBuffer: async () => buffer,
  };
}

test("sniff recognizes wav, mpeg, and ogg headers and rejects empty buffers", () => {
  assert.equal(sniffNotificationSoundType(wavBytes()), "audio/wav");
  const id3 = new Uint8Array([0x49, 0x44, 0x33, 0x04]);
  assert.equal(sniffNotificationSoundType(id3), "audio/mpeg");
  const ogg = new Uint8Array([0x4F, 0x67, 0x67, 0x53]);
  assert.equal(sniffNotificationSoundType(ogg), "audio/ogg");
  assert.equal(sniffNotificationSoundType(new Uint8Array([0x00, 0x00, 0x00, 0x00])), "");
  assert.equal(sniffNotificationSoundType(new Uint8Array()), "");
});

test("readNotificationSoundFile keeps a basename and rejects type or size mismatches", async () => {
  const clip = await readNotificationSoundFile(fakeFile({
    name: `C:\\Users\\Ray\\ding<script>.wav`,
    type: "audio/wav",
    bytes: wavBytes(),
  }));
  assert.equal(clip.name, "dingscript.wav");
  assert.equal(clip.type, "audio/wav");
  assert.equal(clip.bytes.byteLength, 12);

  await assert.rejects(
    () => readNotificationSoundFile(fakeFile({ name: "ding.exe", type: "application/octet-stream", bytes: wavBytes() })),
    (error) => error.code === notificationCustomSoundErrorCodes.unsupportedType,
  );
  await assert.rejects(
    () => readNotificationSoundFile(fakeFile({
      name: "ding.wav",
      type: "audio/wav",
      bytes: wavBytes(),
      size: notificationCustomSoundMaxBytes + 1,
    })),
    (error) => error.code === notificationCustomSoundErrorCodes.tooLarge,
  );
  await assert.rejects(
    () => readNotificationSoundFile(null),
    (error) => error.code === notificationCustomSoundErrorCodes.required,
  );
});

test("sanitizeNotificationSoundFileName strips paths and control characters", () => {
  assert.equal(sanitizeNotificationSoundFileName("../../etc/passwd"), "passwd");
  assert.equal(sanitizeNotificationSoundFileName(""), "sound");
});

test("the custom-sound store round-trips a clip without writing localStorage", async () => {
  const storage = createMemoryClipStorage();
  const store = createNotificationCustomSoundStore({ storage });
  assert.equal(store.current(), null);
  const imported = await store.importFile(fakeFile({ name: "done.wav", type: "audio/wav", bytes: wavBytes() }));
  assert.equal(imported.name, "done.wav");
  assert.equal(imported.size, 12);
  assert.equal(store.current().name, "done.wav");
  const reloaded = createNotificationCustomSoundStore({ storage });
  const loaded = await reloaded.load();
  assert.equal(loaded.name, "done.wav");
  assert.equal(loaded.type, "audio/wav");
  await reloaded.clear();
  assert.equal(reloaded.current(), null);
  assert.equal(await storage.get("custom"), null);
});
