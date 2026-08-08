// Audible completion signals, synthesized rather than shipped as audio files.
//
// A run that finishes while the user is looking at another window used to be
// silent: the only completion signal was a toast that removes itself after a few
// seconds. Synthesis keeps this to one small module with no binary assets, no
// build step, and no network fetch that could fail at the moment it matters.
//
// Browsers refuse to start an AudioContext until the user has interacted with
// the page, and a context created too early lands in "suspended" forever. So the
// context is created lazily on first playback and also resumed from a one-shot
// gesture listener, which is the only reliable way to have sound ready *before*
// the first thing worth announcing happens.

export const notificationToneDefaults = Object.freeze({
  // Two rising notes: unmistakably "finished", short enough not to be annoying
  // when a burst of subagents lands at once.
  success: Object.freeze({
    steps: Object.freeze([
      Object.freeze({ frequency: 660, duration: 0.1 }),
      Object.freeze({ frequency: 990, duration: 0.16 }),
    ]),
    gain: 0.16,
    type: "sine",
  }),
  // One low falling note. Distinct from success without being alarming.
  error: Object.freeze({
    steps: Object.freeze([
      Object.freeze({ frequency: 400, duration: 0.14 }),
      Object.freeze({ frequency: 260, duration: 0.24 }),
    ]),
    gain: 0.2,
    type: "triangle",
  }),
});

const toneAliases = Object.freeze({
  success: "success",
  completed: "success",
  done: "success",
  error: "error",
  failed: "error",
  failure: "error",
  interrupted: "error",
});

export function resolveToneName(value) {
  const token = String(value || "").trim().toLowerCase();
  return toneAliases[token] || "";
}

function contextConstructor(scope) {
  return scope?.AudioContext || scope?.webkitAudioContext || null;
}

export function createNotificationSound({
  scope = globalThis,
  tones = notificationToneDefaults,
  isEnabled = () => true,
  onError,
} = {}) {
  let context = null;
  let unlockBound = false;
  let failed = false;

  function ensureContext() {
    if (failed) return null;
    if (context) return context;
    const Ctor = contextConstructor(scope);
    if (!Ctor) {
      // No WebAudio at all. Fail quiet and permanently: a missing sound must
      // never break the notification path that also renders the toast.
      failed = true;
      return null;
    }
    try {
      context = new Ctor();
    } catch (error) {
      failed = true;
      onError?.(error);
      return null;
    }
    return context;
  }

  // Chrome and Safari start the context suspended until a gesture. Resuming from
  // the first click/keypress means the first completion after page load is
  // audible instead of silently swallowed.
  function unlock() {
    const ctx = ensureContext();
    if (!ctx) return false;
    if (ctx.state === "suspended") {
      try {
        const resumed = ctx.resume?.();
        if (resumed?.catch) resumed.catch((error) => onError?.(error));
      } catch (error) {
        onError?.(error);
        return false;
      }
    }
    return true;
  }

  function bindUnlockGesture(target = scope?.document) {
    if (unlockBound || !target?.addEventListener) return false;
    unlockBound = true;
    const handler = () => {
      unlock();
      target.removeEventListener("pointerdown", handler);
      target.removeEventListener("keydown", handler);
    };
    target.addEventListener("pointerdown", handler, { once: false, passive: true });
    target.addEventListener("keydown", handler, { once: false, passive: true });
    return true;
  }

  function scheduleTone(ctx, tone) {
    const spec = tones?.[tone];
    if (!spec?.steps?.length) return false;
    const now = ctx.currentTime;
    let offset = 0;
    for (const step of spec.steps) {
      const oscillator = ctx.createOscillator();
      const gain = ctx.createGain();
      oscillator.type = spec.type || "sine";
      oscillator.frequency.value = Number(step.frequency) || 660;
      const start = now + offset;
      const duration = Number(step.duration) || 0.12;
      const peak = Number(spec.gain) || 0.15;
      // Ramped envelope: a raw start/stop on a gain node clicks audibly.
      gain.gain.setValueAtTime(0.0001, start);
      gain.gain.exponentialRampToValueAtTime(peak, start + Math.min(0.02, duration / 3));
      gain.gain.exponentialRampToValueAtTime(0.0001, start + duration);
      oscillator.connect(gain);
      gain.connect(ctx.destination);
      oscillator.start(start);
      oscillator.stop(start + duration + 0.02);
      offset += duration;
    }
    return true;
  }

  // force bypasses the preference gate for an explicit "test this sound" action,
  // where checking whether sounds are enabled would be beside the point.
  function play(toneOrFamily, { force = false } = {}) {
    const tone = resolveToneName(toneOrFamily);
    if (!tone) return false;
    if (!force && !isEnabled(tone)) return false;
    const ctx = ensureContext();
    if (!ctx) return false;
    // A suspended context silently drops scheduled nodes, so try to resume and
    // still attempt playback: by the time a run finishes the user has almost
    // always interacted with the page.
    if (ctx.state === "suspended") unlock();
    try {
      return scheduleTone(ctx, tone);
    } catch (error) {
      onError?.(error);
      return false;
    }
  }

  return {
    play,
    unlock,
    bindUnlockGesture,
    available: () => Boolean(contextConstructor(scope)) && !failed,
    state: () => context?.state || "uninitialized",
  };
}
