import { escapeAttr, escapeHtml } from "./dom.mjs";

export const UNCOMMITTED_HASH = "UNCOMMITTED";
export const GIT_TIMELINE_LANE_PITCH = 12;
export const GIT_TIMELINE_ROW_HEIGHT = 40;
export const GIT_LOG_LIMIT = 80;

const LANE_COLORS = Object.freeze([
  "#4f46e5",
  "#0284c7",
  "#16a34a",
  "#d97706",
  "#db2777",
  "#7c3aed",
  "#0f766e",
  "#b45309",
]);

export function gitTimelineLaneColor(lane) {
  const index = Math.max(0, Number(lane) || 0);
  return LANE_COLORS[index % LANE_COLORS.length];
}

export function timelineCommits(commits, { dirty = false, head = "" } = {}) {
  const list = Array.isArray(commits) ? commits.filter((commit) => commit && commit.hash) : [];
  const headHash = String(head || "").trim();
  if (!dirty || !headHash) return list;
  return [
    {
      hash: UNCOMMITTED_HASH,
      shortHash: "",
      subject: "",
      parents: [headHash],
      refs: [],
      uncommitted: true,
    },
    ...list,
  ];
}

export function layoutGitTimeline(commits) {
  const items = Array.isArray(commits) ? commits : [];
  const upcoming = new Set(items.map((commit) => commit.hash).filter(Boolean));
  const lanes = [];
  const rows = [];

  const findLane = (hash) => lanes.indexOf(hash);
  const allocLane = () => {
    const empty = lanes.indexOf(null);
    if (empty !== -1) return empty;
    lanes.push(null);
    return lanes.length - 1;
  };

  for (const commit of items) {
    const hash = String(commit?.hash || "");
    upcoming.delete(hash);
    let lane = findLane(hash);
    if (lane === -1) {
      lane = allocLane();
      lanes[lane] = hash;
    }
    const through = lanes
      .map((value, index) => (value ? index : -1))
      .filter((index) => index >= 0);
    const parents = Array.isArray(commit?.parents) ? commit.parents.filter(Boolean) : [];
    const links = [];
    lanes[lane] = null;

    parents.forEach((parent, index) => {
      if (!upcoming.has(parent)) {
        if (index === 0) links.push({ from: lane, to: lane, kind: "stub" });
        else links.push({ from: lane, to: lane, kind: "merge-stub" });
        return;
      }
      const existing = findLane(parent);
      if (index === 0) {
        if (existing === -1) {
          lanes[lane] = parent;
          links.push({ from: lane, to: lane, kind: "parent" });
          return;
        }
        links.push({ from: lane, to: existing, kind: "parent" });
        return;
      }
      let dest = existing;
      if (dest === -1) {
        dest = allocLane();
        lanes[dest] = parent;
      }
      links.push({ from: lane, to: dest, kind: "merge" });
    });

    while (lanes.length && lanes[lanes.length - 1] === null) lanes.pop();
    rows.push({
      hash,
      lane,
      through,
      links,
      laneCount: Math.max(lanes.length, lane + 1, 1),
      uncommitted: Boolean(commit?.uncommitted),
    });
  }

  const laneCount = rows.reduce((max, row) => Math.max(max, row.laneCount), 1);
  return { laneCount, rows };
}

function laneX(lane) {
  return 2 + Number(lane) * GIT_TIMELINE_LANE_PITCH + GIT_TIMELINE_LANE_PITCH / 2;
}

export function renderGitTimelineLanes(row, laneCount) {
  const count = Math.max(1, Number(laneCount) || 1, Number(row?.laneCount) || 1);
  const width = 4 + count * GIT_TIMELINE_LANE_PITCH;
  const height = GIT_TIMELINE_ROW_HEIGHT;
  const midY = height / 2;
  const commitLane = Number(row?.lane) || 0;
  const parts = [];
  const through = Array.isArray(row?.through) ? row.through : [];
  for (const lane of through) {
    const x = laneX(lane);
    const color = gitTimelineLaneColor(lane);
    parts.push(`<line x1="${x}" y1="0" x2="${x}" y2="${height}" stroke="${color}" />`);
  }
  const links = Array.isArray(row?.links) ? row.links : [];
  for (const link of links) {
    if (link.from === link.to) continue;
    const x1 = laneX(link.from);
    const x2 = laneX(link.to);
    const color = gitTimelineLaneColor(link.kind === "merge" ? link.to : link.from);
    parts.push(`<path d="M ${x1} ${midY} C ${x1} ${height - 2}, ${x2} ${midY + 6}, ${x2} ${height}" stroke="${color}" />`);
  }
  const cx = laneX(commitLane);
  const color = gitTimelineLaneColor(commitLane);
  if (row?.uncommitted) {
    parts.push(`<circle cx="${cx}" cy="${midY}" r="4.5" fill="none" stroke="${color}" stroke-width="2" />`);
  } else {
    parts.push(`<circle cx="${cx}" cy="${midY}" r="4" fill="${color}" />`);
  }
  return `<svg class="git-timeline-lanes" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}" aria-hidden="true">${parts.join("")}</svg>`;
}

function translate(t, key, params) {
  return typeof t === "function" ? t(key, params) : key;
}

function formatWhen(formatTimestamp, value) {
  if (typeof formatTimestamp === "function") return formatTimestamp(value);
  return String(value || "");
}

function renderRefPills(refs) {
  if (!Array.isArray(refs) || !refs.length) return "";
  return `<span class="git-timeline-refs">${refs.map((ref) => {
    const kind = String(ref?.kind || "branch");
    const name = String(ref?.name || "");
    if (!name) return "";
    return `<span class="git-ref ${escapeAttr(kind)}">${escapeHtml(name)}</span>`;
  }).join("")}</span>`;
}

export function renderGitTimeline(commits, {
  dirty = false,
  head = "",
  truncated = false,
  openHash = "",
  t,
  formatTimestamp,
} = {}) {
  const items = timelineCommits(commits, { dirty, head });
  if (!items.length) {
    return `<div class="settings-empty-card compact">${escapeHtml(translate(t, "noHistory"))}</div>`;
  }
  const layout = layoutGitTimeline(items);
  const rowsByHash = new Map(layout.rows.map((row) => [row.hash, row]));
  const truncatedNote = truncated
    ? `<div class="git-timeline-truncated">${escapeHtml(translate(t, "historyTruncated", { count: String(items.filter((item) => !item.uncommitted).length) }))}</div>`
    : "";
  const rows = items.map((commit) => {
    const row = rowsByHash.get(commit.hash) || { hash: commit.hash, lane: 0, through: [0], links: [], laneCount: layout.laneCount, uncommitted: commit.uncommitted };
    const isOpen = openHash === commit.hash;
    const isHead = Array.isArray(commit.refs) && commit.refs.some((ref) => ref.kind === "head");
    const subject = commit.uncommitted ? translate(t, "uncommitted") : (commit.subject || "");
    const hashLabel = commit.uncommitted ? "•" : (commit.shortHash || commit.hash.slice(0, 8));
    const classes = [
      "git-timeline-row",
      commit.uncommitted ? "is-uncommitted" : "",
      isHead ? "is-head" : "",
      isOpen ? "is-open" : "",
    ].filter(Boolean).join(" ");
    const details = !commit.uncommitted && isOpen
      ? `<small class="git-timeline-author">${escapeHtml([commit.authorName, commit.authorEmail].filter(Boolean).join(" · "))}</small>`
      : "";
    return `
      <button type="button" class="${classes}" data-git-commit="${escapeAttr(commit.hash)}" aria-expanded="${isOpen ? "true" : "false"}">
        ${renderGitTimelineLanes(row, layout.laneCount)}
        <span class="git-timeline-meta">
          <span class="git-timeline-headline">
            <strong>${escapeHtml(hashLabel)}</strong>
            ${renderRefPills(commit.refs)}
          </span>
          <span class="git-timeline-subject">${escapeHtml(subject)}</span>
          ${commit.uncommitted ? "" : `<small>${escapeHtml(formatWhen(formatTimestamp, commit.date))}</small>`}
          ${details}
        </span>
      </button>
    `;
  }).join("");
  return `${truncatedNote}<div class="git-timeline" role="list">${rows}</div>`;
}
