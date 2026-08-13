import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import { resolveComposerActivityStatus } from "./agent-workspace-helpers.mjs";
import { appMainExtraMessages } from "./messages-app-main-extra.mjs";

// A first-token timeout is absorbed by the retry loop inside a single model turn,
// and that loop used to wait in silence: no event, so the composer showed an idle
// conversation for the whole backoff and a recovering run looked like a hung one.
  const appMain = readFileSync(new URL("./app-main-stream.mjs", import.meta.url), "utf8");
const runnerModel = readFileSync(new URL("../../../agent/runner_model.go", import.meta.url), "utf8");

function translate(key) {
  return { "chat.activity.retrying": "重試中" }[key] || key;
}

test("模型輪次內的重試迴圈會發出事件，不再靜默等待", () => {
  // The wait lives in runModelTurn's backoff. Publishing from there is what makes
  // it visible; the client handler for this event already existed.
  const loop = runnerModel.slice(runnerModel.indexOf("func (r *Runner) runModelTurn("));
  const body = loop.slice(0, loop.indexOf("\nfunc "));
  assert.match(body, /r\.publish\(Event\{Type: "agent\.provider_error_retry"/, "重試前必須發出事件");
  assert.match(body, /"attempt":\s+attempt \+ 1/);
  assert.match(body, /"backoffMs":\s+backoff\.Milliseconds\(\)/, "要帶上這次等待多久");
  assert.match(body, /"scope":\s+"model_turn"/, "要能和 segment 層的重試區分");
  // The announcement has to come before the sleep, or it arrives after the wait it
  // was meant to explain.
  assert.ok(
    body.indexOf(`"agent.provider_error_retry"`) < body.indexOf("case <-time.After(backoff):"),
    "事件要在等待之前發出",
  );
});

test("無上限重試時不會謊報分母", () => {
  // maxAttempts is 0 for the unlimited sentinel, and the client keeps that 0 rather
  // than coercing it to 1 -- which is what produced "重試中 5/1".
  const handler = appMain.slice(appMain.indexOf(`if (event.type === "agent.provider_error_retry")`));
  const body = handler.slice(0, handler.indexOf("\n  }\n"));
  assert.match(body, /maxAttempts: maxAttempts > 0 \? maxAttempts : 0/, "0 必須被保留下來");
  assert.match(body, /providerErrorRetryUnlimited/, "終端訊息要有不帶分母的版本");

  // And the status line drops the fraction when there is no total.
  assert.deepEqual(
    resolveComposerActivityStatus({ agent: { status: "running" }, providerRetry: { attempt: 5, maxAttempts: 0 } }, translate),
    { kind: "retrying", text: "重試中" },
  );
  // A real ceiling still counts.
  assert.deepEqual(
    resolveComposerActivityStatus({ agent: { status: "running" }, providerRetry: { attempt: 1, maxAttempts: 3 } }, translate),
    { kind: "retrying", text: "重試中 1/3" },
  );
});

test("重試提示在三種語言都有，且無上限版本不含分母", () => {
  for (const locale of ["zh-TW", "zh-CN", "en"]) {
    const bundle = appMainExtraMessages[locale]?.appMainExtra;
    assert.ok(bundle, `${locale} 缺少 appMainExtra 訊息`);
    assert.ok(bundle.providerErrorRetryUnlimited, `${locale} 缺少 providerErrorRetryUnlimited`);
    assert.match(bundle.providerErrorRetryUnlimited, /\{attempt\}/, `${locale} 要顯示第幾次`);
    assert.doesNotMatch(bundle.providerErrorRetryUnlimited, /\{maxAttempts\}/, `${locale} 不該有分母`);
    // The bounded variant keeps both numbers.
    assert.match(bundle.providerErrorRetry, /\{attempt\}/);
    assert.match(bundle.providerErrorRetry, /\{maxAttempts\}/);
  }
});

test("重試狀態在下一次嘗試有產出或 run 結束時被清掉", () => {
  // Otherwise the composer keeps claiming a retry that already resolved. Both
  // clears already existed; pinned here because the new publisher makes the state
  // reachable far more often than before.
  const started = appMain.slice(appMain.indexOf(`if (event.type === "model.started")`));
  assert.match(started.slice(0, started.indexOf("\n  }\n")), /state\.providerRetry = null/);
  const terminal = appMain.slice(appMain.indexOf("if (terminalAgentEvents.includes(event.type))"));
  assert.match(terminal.slice(0, terminal.indexOf("\n  }\n")), /state\.providerRetry = null/);
});
