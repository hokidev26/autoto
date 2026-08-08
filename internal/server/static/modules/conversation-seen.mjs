// "Has the user actually looked at this reply yet?" is a per-browser question,
// not a server fact: the same account can read a conversation on a laptop and
// leave it unread on a phone. So the seen marks live in localStorage and never
// go to the API.
//
// Stored shape: { [agentId]: lastSeenActivityMillis }. Comparing timestamps
// rather than storing a boolean means a *new* reply after the user looked
// re-arms the unread mark by itself, with no explicit invalidation step.

const seenKey = "autoto.conversationSeen";
const maxEntries = 500;

function readStore(storage) {
  try {
    const raw = storage?.getItem?.(seenKey);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    return parsed;
  } catch {
    return {};
  }
}

function writeStore(storage, value) {
  try {
    storage?.setItem?.(seenKey, JSON.stringify(value));
  } catch {}
}

function activityMillis(value) {
  const parsed = Date.parse(value || "");
  return Number.isFinite(parsed) ? parsed : 0;
}

// Keep the newest marks when trimming. An unbounded map would grow with every
// conversation the user ever opened.
function trim(store) {
  const entries = Object.entries(store);
  if (entries.length <= maxEntries) return store;
  entries.sort((left, right) => Number(right[1] || 0) - Number(left[1] || 0));
  return Object.fromEntries(entries.slice(0, maxEntries));
}

export function readSeenMap(storage = globalThis.localStorage) {
  const store = readStore(storage);
  const seen = {};
  for (const [agentId, value] of Object.entries(store)) {
    const millis = Number(value);
    if (agentId && Number.isFinite(millis) && millis > 0) seen[agentId] = millis;
  }
  return seen;
}

// Called when a conversation is opened or is already on screen while a reply
// lands. Marks are monotonic so an out-of-order render cannot un-see a reply.
export function markConversationSeen(agentId, lastActivityAt, storage = globalThis.localStorage) {
  const id = String(agentId || "").trim();
  if (!id) return false;
  const millis = typeof lastActivityAt === "number" ? lastActivityAt : activityMillis(lastActivityAt);
  if (!millis) return false;
  const store = readStore(storage);
  if (Number(store[id] || 0) >= millis) return false;
  store[id] = millis;
  writeStore(storage, trim(store));
  return true;
}

// Unread means: the agent has produced something the user has not looked at.
// A running conversation is deliberately not "unread" -- it is still working,
// and it already signals that with its own running colour.
export function conversationUnread(conversation, seenMap = {}) {
  if (!conversation) return false;
  const status = String(conversation.agentStatus || "").toLocaleLowerCase();
  if (status === "running" || status === "pending" || status === "queued") return false;
  if (status === "error" || status === "failed") return false;
  const activity = activityMillis(conversation.lastActivityAt);
  if (!activity) return false;
  const seen = Number(seenMap?.[conversation.agentId] || 0);
  return activity > seen;
}

export const conversationSeenStorageKey = seenKey;
