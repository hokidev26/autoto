import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

// Two bugs on the phone, both in the drawer's wiring rather than its styling.
//
// 1. The resize handler closed the drawer unconditionally. On a phone, resize fires
//    when the URL bar collapses and when the on-screen keyboard opens or closes, and
//    both happen just as a run starts streaming: the drawer opened on the first tap
//    and the next resize shut it, so it took two taps to stay open.
// 2. The hamburger switched the primary workbench on every tap, re-running the whole
//    layout, the sidebar render, and a task-workspace reload even when the view was
//    already the conversation list.
//
// Source assertions: both live in app-main.mjs top-level wiring against the live
// shell, so standing that up here would test the harness rather than the behaviour.
const appMain = readFileSync(new URL("app-main.mjs", import.meta.url), "utf8");

function handlerBody(pattern) {
  const start = appMain.search(pattern);
  assert.notEqual(start, -1, `expected app-main.mjs to still wire ${pattern}`);
  const open = appMain.indexOf("{", start);
  let depth = 0;
  for (let index = open; index < appMain.length; index += 1) {
    if (appMain[index] === "{") depth += 1;
    else if (appMain[index] === "}") {
      depth -= 1;
      if (depth === 0) return appMain.slice(open + 1, index);
    }
  }
  throw new Error("unbalanced braces");
}

test("a resize only closes the drawer when the viewport stops being a phone", () => {
  const body = handlerBody(/window\.addEventListener\("resize"/);
  assert.match(
    body,
    /if \(lastViewportWasMobile && !mobile\) closeMobileSidebar/,
    "the close is conditional on leaving the phone layout, not on any resize",
  );
  assert.doesNotMatch(
    body,
    /^\s*closeMobileSidebar\(\{ restoreFocus: false \}\);/m,
    "an unconditional close is the bug: the keyboard and URL bar both fire resize",
  );
  // The previous value has to be tracked outside the handler, or every resize looks
  // like a transition and the guard is decorative.
  assert.match(appMain, /let lastViewportWasMobile = isMobileAppViewport\(\);/);
  assert.match(body, /lastViewportWasMobile = mobile;/, "and it has to be updated");
});

test("the resize handler still does the rest of its work", () => {
  // The guard must not become an early return: these ran on every resize before and
  // still have to, or the terminal and composer stop tracking the viewport.
  const body = handlerBody(/window\.addEventListener\("resize"/);
  for (const call of [
    "leaveOverviewForMobile()",
    "syncSettingsViewportState()",
    "layoutSettingsShell()",
    "resizeTerminal()",
    "autoResizeMessageInput()",
    "refreshComposerActivityStatus()",
  ]) {
    assert.ok(body.includes(call), `${call} must still run on every resize`);
  }
});

test("the hamburger only switches the workbench when the view has to change", () => {
  const body = handlerBody(/\$\("mobileMenuBtn"\)\.addEventListener\("click"/);
  assert.match(
    body,
    /if \(state\.activeWorkbench !== "conversation" \|\| state\.overviewActive\) \{\s*switchPrimaryWorkbench\("conversation"\);/,
    "already on the conversation list, the tap should just open the drawer",
  );
  // Home counts as a view that has to change, or the drawer opens over the overview.
  assert.match(body, /state\.overviewActive/);
  assert.match(body, /openMobileSidebar\(\);/, "and the drawer still opens either way");
});

test("switching conversations does not queue the draft request ahead of the transcript", () => {
  // The draft is its own round trip and only fills the composer. Awaiting it before
  // loadMessages put a whole request in front of the thing the user is waiting to
  // see, which is the transcript.
  const enterAgent = handlerBody(/async function enterAgent\(\)/);
  const draftAt = enterAgent.indexOf("restoreCurrentChatDraft()");
  const messagesAt = enterAgent.indexOf("loadMessages(agentId)");
  assert.ok(draftAt !== -1 && messagesAt !== -1);
  assert.ok(draftAt < messagesAt, "the draft still starts early, so it overlaps the load");
  assert.doesNotMatch(
    enterAgent,
    /await restoreCurrentChatDraft\(\);/,
    "but it must not be awaited before the messages are requested",
  );
  assert.match(enterAgent, /const draftPromise = restoreCurrentChatDraft\(\)\.catch/);
  assert.match(
    enterAgent,
    /await Promise\.all\(\[effectiveSkillsPromise, draftPromise\]\)/,
    "and it is still awaited before the function returns, so failures are not dropped",
  );
});

test("a failed draft cannot stop the conversation from opening", () => {
  const enterAgent = handlerBody(/async function enterAgent\(\)/);
  const draftReport = enterAgent.indexOf("if (draftError)");
  const skillsThrow = enterAgent.indexOf("if (effectiveSkillsError) throw");
  assert.ok(draftReport !== -1, "a draft failure is reported");
  assert.ok(draftReport < skillsThrow, "and reported before the skills error can throw");
  assert.match(
    enterAgent.slice(draftReport, skillsThrow),
    /notifyTerminal/,
    "reported to the terminal rather than thrown: a stale composer beats a dead conversation",
  );
});
