import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

import { buildNavigationView, renderNavigationHTML } from "./conversation-navigation.mjs";

// Two conversations in one project, the second on a fork: the case the sidebar
// makes hardest to read, because the rows differ only in their meta line.
const payload = {
  projects: [{ id: "p1", name: "autoto", gitPath: "C:/work/autoto" }],
  conversations: [
    {
      projectId: "p1", projectName: "autoto", projectPath: "C:/work/autoto",
      worklineId: "w1", worklineTitle: "main", worklineBranch: "main", worklineRole: "primary",
      agentId: "a1", agentTitle: "檢查設定與上下文超限", agentType: "primary", agentStatus: "idle", model: "codex:gpt-5.6-luna",
    },
    {
      projectId: "p1", projectName: "autoto", projectPath: "C:/work/autoto",
      worklineId: "w2", worklineTitle: "fork", worklineBranch: "autoto/fork-of-main-dee16d25",
      agentId: "a2", agentTitle: "優化卡片並整合 Telegram", agentStatus: "idle", model: "ttapy:claude-opus-5",
    },
  ],
};

function render(activeAgentId) {
  const view = buildNavigationView(payload, { mode: "all" });
  return renderNavigationHTML(view, { activeProjectId: "p1", activeAgentId });
}

// Forks of one workline differ only by a branch name in the meta line, so the
// current row has to be identifiable by more than its text.
test("只有目前對話的列會標記 active 與 aria-current", () => {
  const html = render("a2");
  const rows = [...html.matchAll(/<div class="navigation-conversation-row[^"]*"[^>]*data-navigation-id="(a\d)"[^>]*>/g)];
  assert.ok(rows.length >= 2, "兩個對話都要繪製出來");

  const activeRows = rows.filter((match) => match[0].includes("active"));
  assert.equal(activeRows.length, 1, "同一時間只能有一列是 active");
  assert.equal(activeRows[0][1], "a2");
  assert.match(activeRows[0][0], /aria-current="true"/, "輔助技術需要 aria-current 才知道目前在哪一列");

  const inactive = rows.find((match) => match[1] === "a1");
  assert.doesNotMatch(inactive[0], /aria-current/, "非目前的列不該宣告 aria-current");
});

test("切換選取時 active 與 aria-current 會跟著移動", () => {
  const first = render("a1");
  const second = render("a2");
  assert.match(first, /data-navigation-id="a1"[^>]*aria-current="true"|aria-current="true"[^>]*data-navigation-id="a1"/);
  assert.doesNotMatch(second, /data-navigation-id="a1"[^>]*aria-current="true"/);
});

test("沒有選取任何對話時不會有 aria-current", () => {
  assert.doesNotMatch(render(""), /aria-current="true"/);
});

test("project 操作上下文仍把開啟中的對話列標成目前位置", () => {
  const html = renderNavigationHTML(buildNavigationView(payload, { mode: "all" }), {
    activeProjectId: "p1",
    activeAgentId: "a2",
    activeSelectionKind: "project",
  });
  assert.match(html, /data-navigation-id="a2"[^>]*aria-current="true"|aria-current="true"[^>]*data-navigation-id="a2"/);
  assert.doesNotMatch(html, /navigation-project-row[^"]*active/);
  const source = readFileSync(new URL("./conversation-navigation.mjs", import.meta.url), "utf8");
  assert.doesNotMatch(
    source,
    /const active = options\.activeSelectionKind !== "project" && conversation\.agentId === activeAgentId/,
    "selectionKind 管的是工作區 chrome，不能再擋住對話列的目前標記",
  );
});

// The accent must come from the theme variable: the cyber and cream presets
// redefine --ws-primary, and a hardcoded blue would clash there.
test("active 列的視覺標記取自主題變數", () => {
  const styles = readFileSync(new URL("../styles/white-shell.css", import.meta.url), "utf8");
  assert.match(
    styles,
    /body\.white-shell\.theme-light \.navigation-conversation-row\.active[\s\S]*?box-shadow:\s*none/,
  );
  assert.match(
    styles,
    /body\.white-shell\.theme-light \.navigation-conversation-row\.active \.navigation-title-text \{[\s\S]*?font-weight:\s*650/,
  );
});

// Re-entering an already-open conversation ran the whole load pipeline again,
// which reset live reasoning, the run summary and the plan state mid-turn.
test("重複選取已開啟的對話會走輕量的聚焦路徑", () => {
  const source = readFileSync(new URL("./app-main.mjs", import.meta.url), "utf8");
  assert.match(
    source,
    /if \(!options\.force && state\.agent\?\.id === agentId && state\.project\?\.id === projectId && state\.workline\?\.id === worklineId && !state\.chatHydrating\) \{\s*focusOpenConversation\(agentId\);\s*return;\s*\}/,
    "已開啟的對話必須在發出請求前就短路",
  );
  // The guard has to sit before the requests it exists to avoid.
  const guardIndex = source.indexOf("focusOpenConversation(agentId);");
  const worklinesFetch = source.indexOf("/api/projects/${encodeURIComponent(projectId)}/worklines");
  assert.ok(guardIndex > 0 && worklinesFetch > guardIndex, "守門要在 worklines 請求之前");
  // Focusing must not reload anything.
  const focusBody = source.slice(source.indexOf("function focusOpenConversation"), source.indexOf("async function selectNavigationConversation"));
  assert.doesNotMatch(focusBody, /\bapi\(|enterAgent\(|loadMessages\(/, "聚焦路徑不該重新載入對話");
});
