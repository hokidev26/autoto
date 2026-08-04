// Opt-in diagnostic for transcript scroll position. Inert unless switched on.
//
// The transcript can end up somewhere other than the newest message after a
// turn, and reproducing that synthetically has not worked: the browser's own
// clamp rescues the fixture even when the anchor aimed at a height that was
// about to disappear. This records what actually happens in a real session --
// every scrollTop write with the code that made it, and every height change --
// so one reproduction on a real machine identifies the cause.
//
// Turn on:   localStorage.setItem("autoto.scrollTrace", "1")  then reload
// Turn off:  localStorage.removeItem("autoto.scrollTrace")    then reload
// Read:      autotoScrollTrace.table()   /  .dump()  /  .suspects()
//
// Records are capped and hold no message text: offsets, heights, and the
// module/function frames that wrote them.

const STORAGE_KEY = "autoto.scrollTrace";
const MAX_RECORDS = 4000;
const MAX_FRAMES = 4;

function enabled() {
  try {
    return String(globalThis.localStorage?.getItem?.(STORAGE_KEY) || "").trim() === "1";
  } catch {
    return false;
  }
}

// Turns a raw stack into the few frames that identify the writer, with the
// cache-busting query and absolute origin stripped so rows stay readable.
function callerFrames(skip) {
  const raw = String(new Error().stack || "");
  return raw
    .split("\n")
    .slice(skip)
    .map((line) => line
      .trim()
      .replace(/^at\s+/, "")
      .replace(/https?:\/\/[^/]+\/ui\/modules\//g, "")
      .replace(/\?v=[^):\s]*/g, "")
      .replace(/\s*\(([^)]*)\)$/, " $1"))
    .filter((line) => line && !line.includes("scroll-trace.mjs"))
    .slice(0, MAX_FRAMES);
}

export function installScrollTrace(getElement) {
  if (!enabled()) return null;
  const resolve = typeof getElement === "function"
    ? getElement
    : () => globalThis.document?.getElementById?.("messages");

  const records = [];
  let installedOn = null;
  let lastHeight = 0;
  let lastClient = 0;
  let sampling = true;

  const push = (record) => {
    records.push(record);
    if (records.length > MAX_RECORDS) records.splice(0, records.length - MAX_RECORDS);
  };

  // Read-only: callers that just want the numbers must not disturb the sampler's
  // baseline. Sharing one mutable baseline meant a write consumed the height
  // change before the per-frame sampler could notice it, so content growth was
  // never recorded at all.
  const geometry = (el) => {
    const height = Math.round(el.scrollHeight);
    const client = Math.round(el.clientHeight);
    return { height, client, max: Math.max(0, height - client) };
  };

  // The setter is on the prototype, so the descriptor has to be found by walking
  // up from the instance rather than read off it.
  const descriptorFor = (el) => {
    let proto = Object.getPrototypeOf(el);
    while (proto) {
      const found = Object.getOwnPropertyDescriptor(proto, "scrollTop");
      if (found?.set && found?.get) return found;
      proto = Object.getPrototypeOf(proto);
    }
    return null;
  };

  function attach() {
    const el = resolve();
    if (!el || el === installedOn) return el;
    const descriptor = descriptorFor(el);
    if (!descriptor) return el;

    Object.defineProperty(el, "scrollTop", {
      configurable: true,
      get() { return descriptor.get.call(this); },
      set(value) {
        const from = Math.round(descriptor.get.call(this));
        const to = Math.round(Number(value) || 0);
        const g = geometry(this);
        // A write is only interesting if it moves the reader or lands short of
        // the newest content. Skipping the rest keeps a long session readable.
        const lands = Math.min(Math.max(0, to), g.max);
        push({
          t: Math.round(performance.now()),
          kind: "write",
          from,
          asked: to,
          lands,
          clamped: lands !== to,
          height: g.height,
          max: g.max,
          shortOfBottom: g.max - lands,
          by: callerFrames(2),
        });
        descriptor.set.call(this, value);
      },
    });

    // Content height changing with no write is the other half of the story: the
    // anchor was correct when it ran and the content moved out from under it.
    //
    // ResizeObserver is the wrong tool here -- it watches the container's own
    // box, which is fixed by the layout, so it never fires when the content
    // inside grows or shrinks. Sampling scrollHeight per frame does catch it.
    if (typeof globalThis.requestAnimationFrame === "function") {
      const sample = () => {
        const current = resolve();
        if (current) {
          const g = geometry(current);
          if (g.height !== lastHeight || g.client !== lastClient) {
            const before = lastHeight;
            lastHeight = g.height;
            lastClient = g.client;
            const top = Math.round(current.scrollTop);
            push({
              t: Math.round(performance.now()),
              kind: "height",
              heightFrom: before,
              height: g.height,
              top,
              max: g.max,
              shortOfBottom: g.max - top,
            });
          }
        }
        if (sampling) globalThis.requestAnimationFrame(sample);
      };
      globalThis.requestAnimationFrame(sample);
    }

    el.addEventListener("scroll", () => {
      const current = resolve();
      if (!current) return;
      const g = geometry(current);
      const top = Math.round(current.scrollTop);
      push({ t: Math.round(performance.now()), kind: "scroll", top, height: g.height, max: g.max, shortOfBottom: g.max - top });
    }, { passive: true });

    installedOn = el;
    return el;
  }

  attach();
  // Every full render replaces the container's children; the node itself
  // survives, but re-checking keeps this correct if that ever changes.
  const rebind = globalThis.setInterval?.(attach, 1000);

  const api = {
    records,
    dump: () => records.slice(),
    clear: () => { records.length = 0; return "cleared"; },
    stop: () => {
      sampling = false;
      if (rebind) globalThis.clearInterval(rebind);
      return "stopped";
    },
    table: (limit = 60) => {
      const rows = records.slice(-limit).map((r) => r.kind === "write"
        ? { t: r.t, kind: "write", from: r.from, asked: r.asked, lands: r.lands, clamped: r.clamped, h: r.height, max: r.max, short: r.shortOfBottom, by: (r.by || [])[0] || "" }
        : { t: r.t, kind: r.kind, from: r.heightFrom ?? "", top: r.top, h: r.height, max: r.max, short: r.shortOfBottom, by: "" });
      console.table(rows);
      return `${rows.length} of ${records.length} records`;
    },
    // The rows worth looking at first: a write that was clamped, or a height
    // change that left the reader well short of the newest content.
    suspects: (threshold = 120) => {
      const hits = records.filter((r) => r.clamped || (r.shortOfBottom ?? 0) > threshold);
      console.table(hits.map((r) => ({
        t: r.t, kind: r.kind, asked: r.asked ?? "", lands: r.lands ?? r.top, h: r.height,
        max: r.max, short: r.shortOfBottom, clamped: !!r.clamped, by: (r.by || []).join(" <- "),
      })));
      return `${hits.length} suspect records (threshold ${threshold}px)`;
    },
  };

  globalThis.autotoScrollTrace = api;
  console.info("[autoto] scroll trace on. autotoScrollTrace.table() / .suspects(). Off: localStorage.removeItem(\"autoto.scrollTrace\")");
  return api;
}
