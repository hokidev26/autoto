// Audible completion signals. Built-in tones are synthesized so Autoto ships
// with no audio assets. A user-picked clip stays in this device's IndexedDB
// and is never uploaded; if that clip is missing, playback falls back to the
// synthesizer so a run still announces itself.
//
// Browsers refuse to start an AudioContext until the user has interacted with
// the page, and a context created too early lands in "suspended" forever. So the
// context is created lazily on first playback and also resumed from a one-shot
// gesture listener, which is the only reliable way to have sound ready *before*
// the first thing worth announcing happens.

export const notificationSoundPresets = Object.freeze(["soft", "clear", "low"]);
export const notificationSoundSources = Object.freeze(["preset", "custom"]);

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
  // Two identical knocks: "something is waiting", not finished and not failed.
  approval: Object.freeze({
    steps: Object.freeze([
      Object.freeze({ frequency: 520, duration: 0.08 }),
      Object.freeze({ frequency: 520, duration: 0.08 }),
    ]),
    gain: 0.14,
    type: "sine",
    gap: 0.06,
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
  approval: "approval",
  approval_required: "approval",
  pending_approval: "approval",
});

export function normalizeNotificationSoundPreset(value) {
  const token = String(value || "").trim().toLowerCase();
  return notificationSoundPresets.includes(token) ? token : "soft";
}

export function normalizeNotificationSoundSource(value) {
  const token = String(value || "").trim().toLowerCase();
  return notificationSoundSources.includes(token) ? token : "preset";
}

export function normalizeNotificationSoundCustomName(value) {
  const base = String(value || "").replace(/\\/g, "/").split("/").pop() || "";
  const cleaned = base.replace(/[<>:"|?*\u0000-\u001f]/g, "").trim();
  if (!cleaned || cleaned.toLowerCase().startsWith("data:")) return "";
  return cleaned.slice(0, 80);
}

export function normalizeNotificationSoundVolume(value, fallback = 100) {
  const number = Number(value);
  if (!Number.isFinite(number)) return fallback;
  return Math.max(0, Math.min(100, Math.round(number)));
}

export function normalizeNotificationSoundMaxConcurrent(value, fallback = 2) {
  const number = Number(value);
  if (!Number.isFinite(number)) return fallback;
  return Math.max(1, Math.min(4, Math.round(number)));
}

function applySoundPreset(spec, preset, volume) {
  const name = normalizeNotificationSoundPreset(preset);
  const gainScale = name === "clear" ? 1.28 : name === "low" ? 0.72 : 1;
  const freqScale = name === "clear" ? 1.06 : name === "low" ? 0.82 : 1;
  const type = name === "clear" ? "triangle" : (spec.type || "sine");
  const peak = Math.max(0.0001, (Number(spec.gain) || 0.15) * gainScale * (normalizeNotificationSoundVolume(volume) / 100));
  return {
    type,
    gap: Number(spec.gap) || 0,
    gain: peak,
    steps: (spec.steps || []).map((step) => ({
      frequency: Math.max(40, (Number(step.frequency) || 660) * freqScale),
      duration: Number(step.duration) || 0.12,
    })),
  };
}

export function resolveToneName(value) {
  const token = String(value || "").trim().toLowerCase();
  return toneAliases[token] || "";
}

function contextConstructor(scope) {
  return scope?.AudioContext || scope?.webkitAudioContext || null;
}

function audioConstructor(scope) {
  return scope?.Audio || globalThis.Audio || null;
}

function objectUrlAPI(scope) {
  return scope?.URL || globalThis.URL || null;
}

export function createNotificationSound({
  scope = globalThis,
  tones = notificationToneDefaults,
  isEnabled = () => true,
  getPreset = () => "soft",
  getSource = () => "preset",
  getCustomClip = () => null,
  getVolume = () => 100,
  getMaxConcurrent = () => 2,
  onError,
} = {}) {
  let context = null;
  let unlockBound = false;
  let failed = false;
  let playing = 0;

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

  function playCustomClip(clip, volume) {
    const AudioCtor = audioConstructor(scope);
    const urls = objectUrlAPI(scope);
    if (!AudioCtor || !urls?.createObjectURL || !clip?.blob) return false;
    let url = "";
    try {
      url = urls.createObjectURL(clip.blob);
      const audio = new AudioCtor(url);
      audio.volume = Math.max(0, Math.min(1, volume / 100));
      let finished = false;
      const done = () => {
        if (finished) return;
        finished = true;
        playing = Math.max(0, playing - 1);
        try { urls.revokeObjectURL?.(url); } catch {}
      };
      audio.addEventListener?.("ended", done);
      audio.addEventListener?.("error", done);
      const started = audio.play?.();
      if (started?.catch) started.catch((error) => { done(); onError?.(error); });
      // A 1 MB clip should end itself; this cap only exists so a stuck play()
      // cannot pin the concurrent slot forever.
      const later = scope?.setTimeout || globalThis.setTimeout;
      later?.(() => done(), 8000);
      playing += 1;
      return true;
    } catch (error) {
      if (url) {
        try { urls.revokeObjectURL?.(url); } catch {}
      }
      onError?.(error);
      return false;
    }
  }

  function scheduleTone(ctx, spec) {
    if (!spec?.steps?.length) return 0;
    const now = ctx.currentTime;
    let offset = 0;
    const gap = Number(spec.gap) || 0;
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
      offset += duration + gap;
    }
    return offset;
  }

  // force bypasses the preference gate for an explicit "test this sound" action,
  // where checking whether sounds are enabled would be beside the point.
  function play(toneOrFamily, { force = false } = {}) {
    const tone = resolveToneName(toneOrFamily);
    if (!tone) return false;
    if (!force && !isEnabled(tone)) return false;
    const volume = normalizeNotificationSoundVolume(getVolume?.());
    if (volume <= 0) return false;
    const cap = normalizeNotificationSoundMaxConcurrent(getMaxConcurrent?.());
    if (playing >= cap) return false;
    if (normalizeNotificationSoundSource(getSource?.()) === "custom") {
      const clip = getCustomClip?.();
      if (clip?.blob && playCustomClip(clip, volume)) return true;
      // Missing or unplayable custom clip must not silence the run: fall through
      // to the synthesized tone so the user still hears that something happened.
    }
    const ctx = ensureContext();
    if (!ctx) return false;
    // A suspended context silently drops scheduled nodes, so try to resume and
    // still attempt playback: by the time a run finishes the user has almost
    // always interacted with the page.
    if (ctx.state === "suspended") unlock();
    try {
      const source = tones?.[tone];
      if (!source) return false;
      const spec = applySoundPreset(source, getPreset?.(), volume);
      const duration = scheduleTone(ctx, spec);
      if (!duration) return false;
      playing += 1;
      const later = scope?.setTimeout || globalThis.setTimeout;
      later?.(() => { playing = Math.max(0, playing - 1); }, Math.ceil(duration * 1000) + 40);
      return true;
    } catch (error) {
      onError?.(error);
      return false;
    }
  }

  return {
    play,
    unlock,
    bindUnlockGesture,
    available: () => (Boolean(contextConstructor(scope)) && !failed) || Boolean(audioConstructor(scope)),
    state: () => context?.state || "uninitialized",
  };
}
