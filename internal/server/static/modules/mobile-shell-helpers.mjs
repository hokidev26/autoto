// Phone chrome helpers. The conversation tools live in the desktop chat
// header; on a phone they dock into the top bar so hamburger, title, and
// tools share one row.

export function conversationToolsDockTarget({
  mobile = false,
  conversationVisible = false,
  group = null,
  home = null,
  dock = null,
} = {}) {
  if (!group || !home || !dock) return null;
  return mobile && conversationVisible ? dock : home;
}
