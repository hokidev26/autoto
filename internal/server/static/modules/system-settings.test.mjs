import assert from "node:assert/strict";
import test from "node:test";

import { formatBytes } from "./formatters.mjs";
import { t } from "./i18n.mjs";
import { createSystemSettingsController } from "./system-settings.mjs";

function controller(state = {}) {
  return createSystemSettingsController({
    state,
    localPreferencesBackupSummary: () => ({ count: 0, bytes: 0, labels: [] }),
    localPreferencesBackupText: () => "{}",
  });
}

test("server system page uses the listen address as the hero and escapes a runtime error", () => {
  // The hero used to repeat the panel help copy. The error string is server-supplied
  // and must not be able to inject markup into the alert.
  const markup = controller({
    runtimeSummary: {
      version: "1.2.3",
      server: { address: "127.0.0.1:16888", host: "127.0.0.1", port: 16888 },
      process: { pid: 42, uptimeSeconds: 90, startedAt: "2026-08-01T00:00:00Z", executable: "/usr/bin/autoto" },
      go: { version: "go1.26", cpus: 8, os: "linux", arch: "amd64" },
      providers: { total: 3, configured: 2 },
      backends: { configured: 1, active: 1 },
      security: { remoteAccessRequired: false, bypassPermissionsAllowed: true, maxPermissionMode: "bypassPermissions", remoteTerminalAllowed: true, accessPasswordConfigured: false },
      generatedAt: "2026-08-01T00:01:00Z",
    },
    runtimeError: `<img src=x onerror=alert(1)>`,
  }).renderServerSystemSettingsContent();

  assert.match(markup, /class="settings-hero-title">127\.0\.0\.1:16888</);
  assert.match(markup, />1\.2\.3</);
  assert.match(markup, />#42</);
  assert.match(markup, /1 分 30 秒|1 min 30 s/);
  assert.match(markup, /&lt;img src=x onerror=alert\(1\)&gt;/);
  assert.doesNotMatch(markup, /<img src=x/);
  assert.match(markup, /usage-metric-value is-identifier/);
});

test("server system page falls back to settings host:port and shows a loading card", () => {
  // Without a summary the page still has to name the process, and a missing
  // address used to render a blank hero.
  const markup = controller({
    settings: { server: { host: "localhost", port: "16888" }, version: "0.1.0-dev" },
  }).renderServerSystemSettingsContent();
  assert.match(markup, /class="settings-hero-title">localhost:16888</);
  assert.match(markup, /settings-empty-card/);
});

test("about update presentation maps each status to a distinct tone and label", () => {
  // A missing status, a failed check, and "already on this build" used to share
  // the idle copy, so an available update looked like "not checked yet".
  const available = controller({
    updateStatus: { status: "update_available", currentVersion: "1.0.0", targetVersion: "1.1.0" },
    licenseSummary: { modules: [] },
  }).renderAboutSettingsContent();
  assert.match(available, /settings-badge available/);
  assert.match(available, />1\.0\.0</);
  assert.match(available, />1\.1\.0</);

  const current = controller({
    updateStatus: { status: "up_to_date", currentVersion: "1.0.0", targetVersion: "1.0.0" },
    licenseSummary: { modules: [] },
  }).renderAboutSettingsContent();
  assert.match(current, /settings-badge current/);

  const development = controller({
    updateStatus: { status: "development_build", currentVersion: "0.1.0-dev" },
    licenseSummary: { modules: [] },
  }).renderAboutSettingsContent();
  assert.match(development, /settings-badge idle/);

  const failed = controller({
    updateError: "network down",
    settings: { version: "1.0.0" },
    licenseSummary: { modules: [] },
  }).renderAboutSettingsContent();
  assert.match(failed, /settings-badge error/);
  assert.match(failed, /network down/);
});

test("about license groups put unknown first, open that group, and escape module paths", () => {
  // Unknown licenses are the ones that need a look; burying them under MIT made
  // the audit list look clean. Paths are remote-ish strings and must be escaped.
  const markup = controller({
    updateStatus: { status: "up_to_date", currentVersion: "1.0.0", targetVersion: "1.0.0" },
    licenseSummary: {
      modules: [
        { path: "github.com/safe/mit", license: "MIT", relation: "indirect", version: "v1.0.0" },
        { path: `<img src=x onerror=pwn>`, license: "unknown", relation: "direct", version: "v0" },
        { path: "github.com/also/unknown", license: "", relation: "indirect", version: "v2" },
      ],
    },
  }).renderAboutSettingsContent();

  const unknownAt = markup.indexOf("license-accordion warn");
  const mitAt = markup.indexOf(">MIT<");
  assert.ok(unknownAt !== -1 && mitAt !== -1 && unknownAt < mitAt, "unknown group must sort before named licenses");
  assert.match(markup, /<details class="license-accordion warn" open>/);
  assert.match(markup, /&lt;img src=x onerror=pwn&gt;/);
  assert.doesNotMatch(markup, /<img src=x onerror=pwn>/);
  assert.match(markup, /license-relation-badge direct/);
});

test("storage page reports known usage, escapes entry paths, and loads an empty scan as a card", () => {
  // Paths come from the host filesystem. An error string in the same card is
  // likewise untrusted and used to render as HTML.
  const loaded = controller({
    storageSummary: {
      totalKnownBytes: 2048,
      generatedAt: "2026-08-01T00:00:00Z",
      scanLimit: 1000,
      entries: [
        { key: "database", path: `/tmp/<script>x</script>.db`, sizeBytes: 1024, fileCount: 1, directoryCount: 0, exists: true, truncated: false, entriesScanned: 1 },
        { key: "projects", path: "/work", sizeBytes: 1024, fileCount: 2, directoryCount: 1, exists: true, truncated: true, entriesScanned: 3, error: `<img src=x>` },
      ],
    },
  }).renderStorageSettingsContent();
  assert.match(loaded, new RegExp(formatBytes(2048).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.match(loaded, /\/tmp\/&lt;script&gt;x&lt;\/script&gt;\.db/);
  assert.match(loaded, /&lt;img src=x&gt;/);
  assert.doesNotMatch(loaded, /<script>x<\/script>/);
  assert.doesNotMatch(loaded, /<img src=x>/);

  const loading = controller({}).renderStorageSettingsContent();
  assert.match(loading, /settings-empty-card/);
});

test("usage metric cards mark identifiers, coerce empty values to 0, and escape copy", () => {
  // localhost:16888 wrapped mid-port inside a 24px headline. Identifier values
  // keep a distinct class so they shrink instead of splitting. Empty used to
  // render a blank card that looked like a failed fetch.
  const { renderUsageMetricCard } = controller({});
  const identifier = renderUsageMetricCard("<b>addr</b>", "127.0.0.1:16888", "config.json", "identifier");
  assert.match(identifier, /usage-metric-value is-identifier/);
  assert.match(identifier, /&lt;b&gt;addr&lt;\/b&gt;/);
  assert.doesNotMatch(identifier, /<b>addr<\/b>/);
  const empty = renderUsageMetricCard("n", "", "");
  assert.match(empty, /usage-metric-value">0</);
  const numeric = renderUsageMetricCard("n", 1200, "hint");
  assert.match(numeric, /1[,.]?200|1 200|1200/);
});

test("runtime page still renders when the summary is missing rather than throwing", () => {
  // Opening Runtime before the first poll used to leave the panel blank because
  // the hero read memory.allocBytes off undefined.
  const markup = controller({}).renderRuntimeSettingsContent();
  assert.match(markup, /runtime-page/);
  assert.match(markup, /settings-empty-card/);
  assert.match(markup, /id="refreshRuntimeSummaryBtn"/);
});
