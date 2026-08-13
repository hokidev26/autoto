# Autoto Plugins

Autoto has a local plugin registry. A plugin is a local directory containing an `autoto.plugin.json` manifest and an executable that speaks the MCP (Model Context Protocol) stdio transport. Once installed and explicitly enabled, the tools the plugin declares become callable by the agent under the name `plugin__<slug>__<toolname>`.

This document covers authoring a plugin, the manifest format, the security model, the HTTP API, and a walkthrough using the bundled echo example at `examples/plugins/echo-plugin/`.

## What a plugin is

A plugin is:

- a **local directory** (the plugin root),
- containing an **`autoto.plugin.json` manifest** at its top level,
- and an **MCP stdio server**: an executable that reads JSON-RPC 2.0 messages from stdin and writes responses to stdout, implementing at least `initialize`, `tools/list`, and `tools/call`.

Autoto launches the executable as a short-lived child process: once during enable/discover/health to list tools, and once per tool call. There are no long-lived plugin sessions.

### How plugins differ from the MCP servers tab

Autoto also has a stdio MCP server registry (Settings → Skills). Both run local MCP processes, but the trust model is different:

| | MCP server registry | Plugin registry |
|---|---|---|
| Command | Any command, resolved from `PATH` | A regular file inside the plugin root; relative paths only, symlink escapes rejected |
| Working directory | Agent workspace | Plugin root |
| Environment | Inherits the full Autoto process environment plus stored values | Clean environment: manifest `env` + resolved `secretRefs` + a short list of essential variables only |
| Secrets | Stored env values (never returned by the API) | Logical `env:VARIABLE_NAME` references only; values are never stored |
| Change detection | None | Manifest is re-read and hash-checked before every launch; a changed manifest fails closed |
| Install state | — | Always installed disabled; enabling requires an explicit local-code confirmation |
| Audit | — | Every lifecycle action is recorded as an audit event |

## Manifest reference

The manifest must be a file named `autoto.plugin.json` at the top level of the plugin root. It must be a single JSON object, valid UTF-8 without NUL bytes, at most 64 KiB. Unknown fields are rejected, so a manifest written for a newer Autoto version does not silently degrade on an older one.

| Field | Required | Type | Constraints |
|---|---|---|---|
| `apiVersion` | yes | string | Must be exactly `autoto.dev/v1alpha1` |
| `transport` | yes | string | Must be exactly `stdio` |
| `slug` | yes | string | Normalized to lowercase kebab-case: `a-z` and `0-9` kept, every other run of characters becomes a single `-`. The result must be 1–63 characters and unique among installed plugins |
| `name` | yes | string | At most 120 bytes |
| `version` | yes | string | At most 64 bytes |
| `description` | no | string | At most 1000 bytes |
| `command` | yes | string | Relative path to the server executable inside the plugin root. At most 1024 characters. Absolute paths, drive/volume prefixes, and `..` traversal are rejected; after symlink resolution the file must still be inside the root and must be a regular file |
| `args` | no | string[] | At most 64 items, each at most 4096 bytes, at most 32 KiB total |
| `env` | no | object | Plain environment variables passed to the plugin process. `env` and `secretRefs` combined may have at most 128 keys. Names must match `[A-Za-z_][A-Za-z0-9_]*` and be at most 256 characters; values at most 4096 bytes. Keys that look sensitive are rejected (see below) |
| `secretRefs` | no | object | Secret environment variables as logical references. Values must be `env:VARIABLE_NAME` references; the same key cannot also appear in `env` |
| `timeoutSeconds` | no | integer | 1–600. Bounds every plugin operation: the whole enable/discover/health launch, and — for tool calls — process startup plus MCP initialization, and then each individual call. Absent or `0` means the default of 20 seconds. Values outside the range reject the manifest |

All string fields must be valid UTF-8 without NUL bytes.

A complete example:

```json
{
  "apiVersion": "autoto.dev/v1alpha1",
  "transport": "stdio",
  "slug": "github-search",
  "name": "GitHub Search",
  "version": "1.2.0",
  "description": "Searches GitHub issues from a local CLI.",
  "command": "bin/github-search.exe",
  "args": ["--stdio"],
  "env": { "LOG_LEVEL": "info" },
  "secretRefs": { "GITHUB_TOKEN": "env:AUTOTO_GITHUB_TOKEN" },
  "timeoutSeconds": 60
}
```

### Sensitive env keys

`env` holds plain values, so keys that look like secrets are rejected there and must be declared under `secretRefs` instead. A key is considered sensitive when, after lowercasing and stripping `_`, `-`, `.`, and spaces, it contains any of: `password`, `passwd`, `secret`, `token`, `apikey`, `credential`, `privatekey`, `accesskey`, `authorization`, `cookie`, `bearer`, `jwt`.

### Manifest hash

Autoto stores a SHA-256 hash of the normalized manifest at install time. Any manifest edit changes the hash, and a changed hash makes every launch fail closed until you update or reinstall (see the security model below). `timeoutSeconds` participates like any other field: adding or changing it changes the hash and requires an update/reinstall, while manifests without it keep their existing hash. Note that older Autoto versions reject manifests that contain `timeoutSeconds`, because unknown manifest fields are disallowed.

## Security model

Plugins are local code execution. The registry is designed so that nothing executes without an explicit decision, and so that what executes is exactly what was reviewed. It is **not** an operating-system sandbox: an enabled plugin runs with your user account's full privileges.

- **Install never executes.** Installation reads and validates the manifest, stores the record, and leaves the plugin disabled with no process started and no undeclared secret resolved.
- **Enable requires explicit confirmation.** The enable API requires `"confirmExecuteLocalCode": true`, and the UI shows a confirmation dialog. Enabling launches the plugin once to discover its tools.
- **Clean environment.** The plugin process does not inherit the Autoto process environment. It receives only the manifest `env`, the resolved `secretRefs`, and these essential variables from the parent: `PATH`, `HOME`, `TMPDIR`, `TEMP`, `TMP`, `LANG`, `LANGUAGE`, and variables prefixed with `LC_`. The working directory is the plugin root.
- **Secret references, never secret values.** `secretRefs` values are `env:VARIABLE_NAME` references resolved from the Autoto process environment at launch time. Resolved values are never stored and never returned by the API — plugin responses expose only whether each key is configured. Resolved values are redacted (replaced with `[REDACTED]`) from error messages and tool output, and discovery rejects a plugin whose tool names, descriptions, or schemas contain a configured secret value.
- **Command containment.** The command must be a relative path that stays inside the plugin root after cleaning and symlink resolution, and must be a regular file. The manifest itself is also rejected if it resolves outside the root through a symlink.
- **Fail-closed revalidation.** Before every launch — enable, discover, health, and each tool call — Autoto re-reads `autoto.plugin.json` from disk and compares its hash, slug, and command against the installed record. A mismatch fails with `plugin manifest changed; reinstall or update the plugin before enabling`. A tool call additionally re-checks, immediately before delegation, that the plugin is still enabled, its revision has not changed, and the discovered tool snapshot still contains the exact tool; otherwise the call fails with a `plugin tool unavailable: ...` error.
- **Approval invalidation.** Every plugin state change (install, enable, disable, discover, update, uninstall) invalidates pending tool approvals, so a call approved before the change cannot execute after it.
- **Audit trail.** Lifecycle actions are recorded as audit events: `plugin.install`, `plugin.enabled`, `plugin.disabled`, `plugin.discover`, `plugin.uninstall`, `plugin.update`, and `plugin.health`.

## Tool exposure

Tools discovered from an enabled plugin are exposed to the agent as:

```text
plugin__<slug>__<toolname>
```

where `<toolname>` is the remote tool name lowercased, with `a-z`, `0-9`, and `-` kept and every other run of characters collapsed into a single `_`. The full exposed name must be at most 192 characters and the tool component must not be empty; names that collide case-insensitively are rejected at discovery.

Every plugin tool call is classified as **exec risk**, so it goes through the same approval flow as other local code execution: depending on your permission mode, the agent must ask before calling it.

Limits enforced on plugins:

| Limit | Value |
|---|---|
| Tools per plugin | 64 |
| Remote tool name | 128 characters |
| Exposed tool name | 192 characters |
| Tool description | 2 KiB |
| Tool input schema (per tool, normalized) | 64 KiB |
| Tool input schemas (per plugin, total) | 256 KiB |
| Process timeout | 20 seconds default; per-plugin `timeoutSeconds` 1–600 |
| Captured stderr | 64 KiB |
| MCP response stream (stdout) | 1 MiB per ephemeral launch; a reused tool-call process gets a 64 MiB lifetime budget and is recycled after 62 calls, so every call keeps at least a full per-call budget of headroom |
| Tool output returned to the agent | truncated at 256 KiB with a `...[truncated]` marker |
| Manifest size | 64 KiB |

For enable, discover, and health check the timeout bounds the whole operation — process start, MCP `initialize`, and the `tools/list` round trip — and the process is killed when it expires. Tool calls run on a reused process (below), so there the timeout bounds process startup plus `initialize` when a fresh process is needed, and then each `tools/call` round trip individually. A timed-out call kills the process and fails with a context-deadline error.

### Process reuse

Tool calls do not pay process startup every time: Autoto keeps at most one warm plugin process per plugin and reuses it for subsequent calls. Reuse is pinned to the exact manifest revision, manifest hash, and resolved environment; any mismatch, any lifecycle change (enable, disable, discover, update, uninstall), any call error or timeout, five minutes of idleness, or 62 served calls closes the process and the next call starts a fresh one. Concurrent calls to the same plugin do not queue: the overflow call runs on its own short-lived process. Two practical consequences for plugin authors: your process may serve many `tools/call` requests in one MCP session (long-lived state is possible but must never be relied on), and the reused process keeps the environment it was launched with — changed secret values take effect on the next recycle, which Autoto triggers automatically because the resolved environment is fingerprinted per call.

## Lifecycle

1. **Install** — point Autoto at the plugin root (Settings → Skills → plugins tab, or `POST /api/plugins/install`). The manifest is validated and stored; the plugin is disabled and nothing has executed.
2. **Enable** — requires the local-code confirmation. Autoto launches the plugin, initializes MCP, lists its tools, validates them against the limits above, and stores the snapshot. The plugin becomes `enabled` with status `healthy`.
3. **Use** — the agent sees `plugin__<slug>__<tool>` tools and can call them, subject to exec-risk approval. Calls reuse a warm plugin process where safe (see “Process reuse” above) and fall back to a fresh process otherwise.
4. **Discover refresh** — `POST /api/plugins/{id}/discover` re-runs discovery for an enabled plugin and replaces the stored tool snapshot.
5. **Update** — after editing the manifest on disk, `POST /api/plugins/{id}/update` re-reads it from the stored root path and adopts it atomically: the discovered tool snapshot is cleared, the revision is bumped, and the plugin is **always left disabled**, so you must re-enable it and re-confirm local code execution.
6. **Health check** — `POST /api/plugins/{id}/health` launches an enabled plugin, initializes MCP, and lists tools without changing any state. For a disabled plugin it reports `healthy: false` without executing anything.
7. **Disable / uninstall** — disabling keeps the record but removes the tools from the agent; uninstalling deletes the record and its tool snapshot. Uninstall never deletes the plugin source directory or any files inside it.

## HTTP API

All endpoints are local API routes and follow the same authentication rules as the rest of the Autoto API.

Plugin records returned by the API contain: `id`, `slug`, `name`, `version`, `description`, `manifestVersion`, `rootPath`, `environment` (an array of `{key, configured}` — never values), `enabled`, `status`, `revision`, `lastCheckedAt`, `createdAt`, `updatedAt`.

### `GET /api/plugins`

Lists installed plugins. Returns `200` with an array of plugin records.

### `POST /api/plugins/install`

Installs a plugin from a local directory. The plugin is always installed disabled.

```json
{ "rootPath": "C:/Users/me/plugins/echo-plugin" }
```

Returns `201` with the plugin record. Errors: `400` missing `rootPath` or invalid manifest, `404` when the path does not exist, `409` when a plugin with the same slug is already installed.

### `GET /api/plugins/{id}`

Returns `200` with one plugin record, or `404`.

### `POST /api/plugins/{id}/enable`

Enables the plugin and runs tool discovery. Requires explicit confirmation:

```json
{ "confirmExecuteLocalCode": true }
```

Returns `200` with the plugin record (`enabled: true`, status `healthy`). Errors: `400` when the confirmation is missing or false, `404` unknown id, `502` when the plugin fails to launch or discovery fails — the plugin is then left disabled with status `error` and the (redacted) error stored on the record.

### `POST /api/plugins/{id}/disable`

Disables the plugin. No body. Returns `200` with the plugin record.

### `POST /api/plugins/{id}/discover`

Re-runs tool discovery for an enabled plugin and replaces the stored snapshot. No body. Returns `200`:

```json
{ "pluginId": "…", "tools": [ { "pluginId": "…", "remoteName": "echo", "exposedName": "plugin__echo-plugin__echo", "description": "…", "inputSchema": { "type": "object" }, "discoveredAt": "…" } ], "count": 1 }
```

Errors: `404` unknown id, `409` when the plugin is disabled (`plugin is disabled`), `502` when discovery fails.

### `POST /api/plugins/{id}/update`

Re-reads `autoto.plugin.json` from the stored root path and adopts the changed manifest atomically. The discovered tool snapshot is cleared, the revision is bumped, and the plugin is always left disabled so that re-enabling re-confirms local code execution. No body. Returns `200` with the updated plugin record. Errors: `404` unknown plugin or missing directory, `400` invalid manifest, `409` slug conflict with another installed plugin.

### `POST /api/plugins/{id}/health`

Runs a health check. For an enabled plugin, launches it, initializes MCP, and lists tools. For a disabled plugin, returns `healthy: false` with error `plugin is disabled` **without executing anything**. No body. Returns `200`:

```json
{ "pluginId": "…", "healthy": true, "toolCount": 1, "checkedAt": "2026-08-13T12:00:00Z" }
```

An unhealthy result carries an `error` field instead. Errors: `404` unknown id.

### `DELETE /api/plugins/{id}`

Uninstalls the plugin: unregisters it and deletes its tool snapshot. The source directory is not touched.

```json
{ "ok": true, "sourceDeleted": false }
```

## Windows notes

- The manifest `command` must point at a **regular file inside the plugin root**. Interpreters are not resolved from the manifest command, so `"command": "python"` or `"command": "node server.js"` does not work. Point it at a built `.exe` (the usual case for Go plugins), or at a `.cmd` wrapper inside the root that invokes the runtime.
- `PATH` is one of the essential variables inherited even in the clean environment, so a `.cmd` wrapper can find runtimes installed on the machine (for example `node` or `python`) even though the rest of the parent environment is stripped.
- Forward slashes are fine in `command` (for example `bin/server.exe`); paths are normalized per platform.

## Troubleshooting

- **`plugin manifest changed; reinstall or update the plugin before enabling`** — the on-disk `autoto.plugin.json` no longer matches the hash, slug, or command recorded at install time. This is the fail-closed protection against silent manifest swaps. Call `POST /api/plugins/{id}/update` to adopt the change (the plugin stays disabled until you re-enable it), or uninstall and reinstall.
- **`plugin transport must be stdio`** — the manifest `transport` field is missing or not `stdio`. Only the stdio transport is supported.
- **`sensitive plugin env key "API_TOKEN" must use secretRefs with an env:VARIABLE_NAME reference`** — a key under `env` matched the sensitive-key markers. Move it to `secretRefs` with a value like `env:MY_VARIABLE`, and set the actual value in the Autoto process environment.
- **`plugin is disabled`** — you called discover (or a tool) on a disabled plugin, or a health check reported it. Enable the plugin first; enabling requires the local-code confirmation.
- **`plugin tool unavailable: plugin is disabled` / `plugin tool unavailable: plugin revision changed` / `plugin tool unavailable: tool snapshot changed`** — the plugin's state changed between tool discovery and the call, so the call failed closed. Re-run discovery and try again.
- **`plugin command must be a relative path inside the plugin root` / `plugin command escapes plugin root through a symlink` / `plugin command must be a regular file`** — the manifest `command` is absolute, traverses out of the root, resolves outside the root through a symlink, or is not a plain file (for example a directory). Ship the executable inside the plugin directory and reference it relatively.
- **`context deadline exceeded`** — the launch, `initialize`, `tools/list`, or `tools/call` did not complete within the timeout (20 seconds by default). Slow-starting or long-running plugins should set `timeoutSeconds` in the manifest (1–600); remember that changing the manifest requires an update and re-enable.
- **Anything unclear** — `POST /api/plugins/{id}/health` runs the launch → initialize → list-tools sequence for an enabled plugin and returns the failure text (with secrets redacted), which is usually the fastest way to see why a plugin misbehaves.

## Writing your first plugin

The repository ships a complete minimal plugin at `examples/plugins/echo-plugin/`: a single-file, stdlib-only Go MCP stdio server that exposes one `echo` tool, plus a manifest and build instructions.

1. Build the server inside the example directory so the binary sits in the plugin root:

   ```bash
   cd examples/plugins/echo-plugin
   go build -o echo-plugin.exe .        # Windows
   go build -o echo-plugin .            # macOS / Linux; also change "command" in the manifest
   ```

2. The manifest next to it declares the executable and a 30-second timeout:

   ```json
   {
     "apiVersion": "autoto.dev/v1alpha1",
     "transport": "stdio",
     "slug": "echo-plugin",
     "name": "Echo Plugin",
     "version": "0.1.0",
     "description": "Minimal example plugin that echoes text back.",
     "command": "echo-plugin.exe",
     "args": [],
     "timeoutSeconds": 30
   }
   ```

3. In Autoto, open Settings → Skills → plugins tab, enter the absolute path of the example directory, and install. The plugin appears disabled; nothing has run.
4. Enable it and confirm the local-code dialog. Discovery runs and one tool appears: `plugin__echo-plugin__echo`.
5. Ask the agent to call it. The call is exec-risk, so you approve it like any other command. The first call starts the plugin process; follow-up calls reuse it while it stays warm.

From there, a real plugin is the same shape: keep the executable inside the directory, declare configuration under `env`, route anything secret through `secretRefs`, and bump `version` (and run update + re-enable) when you change the manifest. See `examples/plugins/echo-plugin/main.go` for the minimal JSON-RPC loop to copy.
