import assert from "node:assert/strict";
import test from "node:test";

import { createNotificationSound, resolveToneName } from "./notification-sound.mjs";

function fakeAudioScope({ state = "running" } = {}) {
  const started = [];
  const listeners = new Map();
  let resumed = 0;
  class FakeContext {
    constructor() {
      this.currentTime = 0;
      this.state = state;
      this.destination = { id: "destination" };
    }
    resume() {
      resumed += 1;
      this.state = "running";
      return Promise.resolve();
    }
    createOscillator() {
      const node = {
        type: "",
        frequency: { value: 0 },
        connect() {},
        start(at) { started.push({ frequency: node.frequency.value, at, type: node.type }); },
        stop() {},
      };
      return node;
    }
    createGain() {
      return {
        gain: {
          setValueAtTime() {},
          exponentialRampToValueAtTime() {},
        },
        connect() {},
      };
    }
  }
  return {
    scope: {
      AudioContext: FakeContext,
      document: {
        addEventListener(type, handler) { listeners.set(type, handler); },
        removeEventListener(type) { listeners.delete(type); },
      },
    },
    started,
    listeners,
    resumeCount: () => resumed,
  };
}

test("resolveToneName maps run outcomes onto the audible tones", () => {
  assert.equal(resolveToneName("completed"), "success");
  assert.equal(resolveToneName("done"), "success");
  assert.equal(resolveToneName("failed"), "error");
  assert.equal(resolveToneName("interrupted"), "error");
  assert.equal(resolveToneName("approval_required"), "approval");
  assert.equal(resolveToneName(""), "");
});

test("play schedules the tone steps for an enabled tone", () => {
  const { scope, started } = fakeAudioScope();
  const sound = createNotificationSound({ scope });
  assert.equal(sound.play("completed"), true);
  assert.equal(started.length, 2);
  assert.ok(started[1].frequency > started[0].frequency, "success tone rises");
  assert.ok(started[1].at > started[0].at, "steps are sequenced, not stacked");
});

test("error tone falls and is distinct from success", () => {
  const { scope, started } = fakeAudioScope();
  const sound = createNotificationSound({ scope });
  assert.equal(sound.play("error"), true);
  assert.ok(started[1].frequency < started[0].frequency, "error tone falls");
});

test("a disabled preference silences playback but force overrides it", () => {
  const { scope, started } = fakeAudioScope();
  const sound = createNotificationSound({ scope, isEnabled: () => false });
  assert.equal(sound.play("completed"), false);
  assert.equal(started.length, 0);
  assert.equal(sound.play("completed", { force: true }), true);
  assert.equal(started.length, 2);
});

test("per-tone preferences are honoured independently", () => {
  const { scope, started } = fakeAudioScope();
  const sound = createNotificationSound({ scope, isEnabled: (tone) => tone === "error" });
  assert.equal(sound.play("completed"), false);
  assert.equal(sound.play("error"), true);
  assert.equal(started.length, 2);
});

test("a suspended context is resumed so the first completion is audible", () => {
  const { scope, resumeCount } = fakeAudioScope({ state: "suspended" });
  const sound = createNotificationSound({ scope });
  assert.equal(sound.play("completed"), true);
  assert.ok(resumeCount() >= 1, "suspended context was resumed");
});

test("the unlock gesture is bound once and detaches after firing", () => {
  const { scope, listeners, resumeCount } = fakeAudioScope({ state: "suspended" });
  const sound = createNotificationSound({ scope });
  assert.equal(sound.bindUnlockGesture(), true);
  assert.equal(sound.bindUnlockGesture(), false, "does not double-bind");
  assert.equal(listeners.size, 2);
  listeners.get("pointerdown")();
  assert.ok(resumeCount() >= 1);
  assert.equal(listeners.size, 0, "listeners detach once unlocked");
});

test("a browser without WebAudio fails quiet instead of throwing", () => {
  const sound = createNotificationSound({ scope: { document: null } });
  assert.equal(sound.available(), false);
  assert.equal(sound.play("completed"), false);
  assert.equal(sound.unlock(), false);
});

test("a throwing AudioContext is reported once and then stays inert", () => {
  const errors = [];
  const scope = {
    AudioContext: class {
      constructor() { throw new Error("blocked"); }
    },
  };
  const sound = createNotificationSound({ scope, onError: (error) => errors.push(error) });
  assert.equal(sound.play("completed"), false);
  assert.equal(sound.play("error"), false);
  assert.equal(errors.length, 1, "construction is not retried on every play");
  assert.equal(sound.available(), false);
});

test("an unknown family never plays a tone", () => {
  const { scope, started } = fakeAudioScope();
  const sound = createNotificationSound({ scope });
  assert.equal(sound.play("truncated"), false);
  assert.equal(sound.play(null), false);
  assert.equal(started.length, 0);
});

test("volume zero is mute and overlapping plays honour the concurrent cap", () => {
  const { scope, started } = fakeAudioScope();
  const muted = createNotificationSound({ scope, getVolume: () => 0 });
  assert.equal(muted.play("completed", { force: true }), false);
  assert.equal(started.length, 0);
  const capped = createNotificationSound({ scope, getMaxConcurrent: () => 1 });
  assert.equal(capped.play("completed", { force: true }), true);
  assert.equal(capped.play("error", { force: true }), false);
  assert.equal(started.length, 2);
});

test("approval is a distinct knock, not the success rise", () => {
  const { scope, started } = fakeAudioScope();
  const sound = createNotificationSound({ scope });
  assert.equal(sound.play("approval_required"), true);
  assert.equal(started.length, 2);
  assert.equal(started[0].frequency, started[1].frequency);
});

test("a custom clip plays through HTMLAudio and skips the synthesizer", () => {
  const { scope, started } = fakeAudioScope();
  const played = [];
  scope.setTimeout = () => 0;
  scope.URL = {
    createObjectURL(blob) { return `blob:${blob.size || 1}`; },
    revokeObjectURL() {},
  };
  scope.Audio = class {
    constructor(src) { this.src = src; this.volume = 1; }
    addEventListener() {}
    play() { played.push(this.src); return Promise.resolve(); }
  };
  const sound = createNotificationSound({
    scope,
    getSource: () => "custom",
    getCustomClip: () => ({ name: "ding.wav", blob: { size: 12 } }),
  });
  assert.equal(sound.play("completed", { force: true }), true);
  assert.equal(started.length, 0);
  assert.deepEqual(played, ["blob:12"]);
});

test("custom source without a clip falls back to the synthesized tone", () => {
  const { scope, started } = fakeAudioScope();
  const sound = createNotificationSound({
    scope,
    getSource: () => "custom",
    getCustomClip: () => null,
  });
  assert.equal(sound.play("completed"), true);
  assert.equal(started.length, 2);
});
