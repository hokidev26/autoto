# Autoto Security Policy

Autoto is an experimental local-first MVP for trusted local development environments. Do not expose it directly to untrusted networks. The canonical executable is `./autoto`; `./autoto` remains only as a legacy compatibility shim.

## Reporting a vulnerability

If you find a security issue, please report it privately to the repository owner instead of opening a public issue with exploit details.

Include:

- A clear description of the issue
- Steps to reproduce
- Affected commit or version
- Expected and actual behavior
- Any relevant logs with secrets removed

## Secret handling

Do not commit:

- `.env` or `.env.*`
- Local config files containing credentials
- SQLite databases
- Provider API keys
- Agent Server backend API keys
- MCP server environment values or tokens
- Telegram bot tokens or Home Assistant access tokens
- Access tokens or private keys

Autoto stores its default config at `~/.autoto/config.json` and its database at `~/.autoto/autoto.db`. On first use, a legacy `~/.autoto/config.json` is copied into the canonical config location when it exists; review that copied configuration before sharing it. The repository `.gitignore` excludes common local secret and runtime files, but you should still inspect commits before pushing.

Telegram and Home Assistant connection metadata must reference secrets as `env:VARIABLE_NAME`; plaintext connection tokens are rejected. Public integration responses expose only whether a logical secret field is configured, never the environment-variable name or resolved value. If a Telegram bot token may have leaked, rotate it and pair again: token revision changes revoke stale pairings. Also revoke any affected pairing explicitly from the local UI/API. If a Home Assistant token may have leaked, rotate the referenced environment value and disable/delete or restart/retest the connection as appropriate; Home Assistant does not use channel pairings.

## Backup and restore boundary

A complete credential-capable backup is a matched set: `config.json`, a consistent SQLite snapshot of `autoto.db`, the entire `credentials/` directory (including Codex and Anthropic account files), and `secrets/provider-secrets.key`. The provider key file must come from the same backup generation as the SQLite provider-secret ciphertext; a missing or mismatched key fails closed and must not be replaced with a newly generated key.

Do not copy only the main `autoto.db` file while Autoto is running. Stop Autoto cleanly before copying the database, or use SQLite's online backup/snapshot facilities so the main file and any active `-wal` state are captured consistently. Copying an active main file without its required sidecar state can silently omit committed data. Protect backups like live credentials, and restore private permissions for credential directories/files and the provider key.

## Legacy compatibility and deprecation logs

Canonical Autoto inputs always take precedence when canonical and legacy forms are both present. Until removal, old Autoto names are accepted only for compatibility reads, one-time migration, or route/header/cookie aliases; new configuration, responses, examples, and clients must write canonical Autoto names.

Deprecation warnings may record that a successfully used legacy command, fallback, credential alias class, or its canonical replacement was selected, but they must never record token, password, cookie, Authorization header, API key, MCP environment value, or other secret values. Warnings are deduplicated once per process or compatibility key so repeated requests do not amplify sensitive operational metadata; invalid credentials and canonical-preferred paths do not emit legacy-use warnings.

The removal schedule and gates are defined only in `PROJECT_PLAN.md`: no runtime legacy surface is removed before v0.4.0 or before at least two tagged releases of migration runway.

## Current security boundaries

- Autoto is intended for local use. The embedded UI is served by the same local Go service.
- Telegram is the only inbound channel. Runtime ingress uses Bot API `getUpdates` long polling only; Autoto does not expose a Telegram webhook receiver. Slack and Discord are not implemented.
- Telegram accepts private-chat `/pair <code>`, `/status`, `/approve <toolCallId>` with fixed one-time `allow_once` semantics, and `/deny <toolCallId> [reason]`. There is no `/task`, free-form assistant chat, permission-mode command, or terminal command. The literal command is `/approve`; there is no separate `/approve-once` spelling.
- Pairing codes are short-lived and stored only as hashes. Active pairings bind a Telegram chat/user, Agent, connection, and bot-credential revision. Unpaired commands and failed pairing attempts produce no Telegram response; accepted updates and denials are persisted as channel events/audit records. Repeated pairing failures lock the pending pairing, and paired chats are rate-limited.
- Telegram update IDs and offsets are persisted transactionally as channel events/cursors, so restart replay is idempotent for the same connection. This channel durability is separate from Agent WebSocket replay, which remains process-local and in-memory.
- Telegram approval is never a privilege escalation: it can only apply a one-time decision to an existing pending tool call and cannot approve `danger` risk. It cannot change permission modes, enable `bypassPermissions`, open a terminal, create a task/run, or control a device.
- Browser-originated API calls must pass a per-process local token injected into the UI; cross-site `Origin` requests are rejected, and `Sec-Fetch-Site: cross-site` is rejected even when `Origin` is absent.
- `X-Autoto-Token` is the canonical browser API and WebSocket token header. `X-Autoto-Token` is accepted only for legacy-client compatibility; WebSocket clients may also provide the local token through the `token` query parameter.
- The local token remains browser cross-site protection. Optional local account sessions add per-user draft ownership and message attribution, but they do not isolate operating-system users or yet enforce project membership across every route.
- Requests from a non-loopback transport peer, requests whose `Host` is not loopback, and loopback-proxy requests carrying supported forwarding metadata such as `X-Forwarded-Proto`, `X-Forwarded-Host`, `Forwarded`, `X-Forwarded-For`, `CF-Connecting-IP`, or `Cf-Ray` enter the remote authentication boundary. `AUTOTO_EXPOSED=true` controls explicit exposed operation but does not turn a direct `localhost` request into remote authority; local administrator access still requires both a loopback peer and loopback Host.
- Remote authentication requires `AUTOTO_ACCESS_PASSWORD` when no locally stored password hash exists, or a locally stored password hash before serving the UI or API. The environment password seeds the initial credential; a host-local password rotation stores a hash and that hash becomes authoritative on subsequent restarts. New sessions use the host-local policy: `restricted` caps permission at `acceptEdits`, limits filesystem scope to configured projects, and disables the PTY terminal; an explicit `full` policy enables host filesystem scope, terminal access, and `bypassPermissions`. Remote clients cannot promote their own session mode.
- Remote access policy and password changes are host-local operations. Configure them from the Remote Access settings page on `localhost`, or use `AUTOTO_ALLOW_REMOTE_FULL_ACCESS`, `AUTOTO_DEFAULT_REMOTE_ACCESS_MODE`, and `AUTOTO_ALLOW_REMOTE_NATIVE_PICKER` at startup. When an environment password is active, localhost can generate or set a replacement password; the saved config hash then takes precedence. Treat `full` mode as equivalent to granting a remote shell on the Autoto host and require a trusted edge authentication layer first.
- One-click `cloudflared` installation is also host-local and requires the canonical local token. Autoto prefers an existing binary on `PATH`; otherwise it downloads only the fixed platform asset from Cloudflare's official GitHub release, enforces public-network and redirect host allowlists, bounds response sizes, verifies the release asset SHA-256 digest, and installs into `~/.autoto/bin` through a staged file. It does not invoke a system package manager, request administrator privileges, change `PATH`, or start a tunnel automatically.
- Remote access cookies are process-local, expire after 24 hours, become invalid on restart, and can be cleared from the UI logout action. Logout, password rotation, policy changes, session eviction, and expiry also cancel established remote Agent and terminal WebSockets. The canonical cookie is `autoto_remote_access`; the legacy `autoto_remote_access` cookie is accepted and cleared for compatibility. API clients may send `Authorization: Bearer $AUTOTO_ACCESS_PASSWORD` or the canonical `X-Autoto-Access: $AUTOTO_ACCESS_PASSWORD` while the environment credential is authoritative; password-header access remains restricted and `X-Autoto-Access` is a legacy alias.
- Schedule definitions accept only `readOnly` or `acceptEdits`. The selected mode is persisted as a run permission cap and cannot widen the Agent's current permission. If the Agent already has a pending/running manual run, the schedule is recorded as skipped; it does not cancel or replace that run.
- Session grants belong to attended Runs. A schedule dispatch, an internal submission, or any future Run source that is not `manual` or `conversation` neither spends nor creates a session grant, so a one-time human approval given in the UI cannot be reused later by an unattended Run. Unknown Run sources are classified as unattended, and the unattended check never consumes or invalidates a grant the interactive session still holds.
- Schedule prompts are rejected at create and update time when they contain a shell command that stops or restarts Autoto itself (`taskkill`/`sc`/`Stop-Process`/`pkill`/`killall`/`systemctl`/`launchctl` aimed at the Autoto process or unit). Matching is anchored on command shapes, not prose, so a schedule may still discuss restarts. This is defence in depth on top of the execution-time approval gate: it prevents a self-restart loop in which the schedule kills the server, a supervisor or autostart entry revives it, the interrupted Run resumes, and the same command fires again.
- Notification deliveries are persisted with deduplication, leases, bounded exponential backoff, attempt counts, latest failure metadata, and terminal `delivered`/`dead` states. Webhook/Telegram delivery failures do not block the Agent loop. Stored payloads and errors are bounded and redacted, but operators should still treat notification history as sensitive operational metadata.
- Home Assistant connections must use loopback, `.local`, link-local, or private-network hosts. Redirects are disabled so the bearer token cannot be forwarded to another host. State/entity listing is read-only and exposes only whitelisted scalar attributes.
- Home Assistant state changes are not a generic service-call proxy. Only the fixed action catalog is accepted, inputs reject unknown fields/templates/secret keys, and execution requires an unchanged canonical action. A request expires quickly, the Web UI asks for two confirmations, and final approval must come directly from loopback without forwarding headers.
- Unknown or critical device actions are hard-blocked before a request is created. This includes door unlock, alarm, script/automation trigger, shell command, camera snapshot, notify, and unlisted services. IM cannot create, approve, deny, or execute device actions. Generic IoT/camera control and cloud monitoring are not implemented.
- Git status/diff/log/commit and workline workflow APIs reject repositories outside the current project path, configured default project directory, or an Autoto-created workline worktree under `.autoto-worktrees`.
- Read/Edit/Write/Glob/Grep and filesystem browse/preview/mkdir resolve symlinks and reject paths whose physical location escapes the configured boundary; concurrent path replacement still remains an operating-system-level TOCTOU concern.
- `Read`, `Write`, `Edit`, `Glob`, and `Grep` also hard-block or omit sensitive paths such as `.env*`, credentials/secrets, common private keys, and `.git`.
- Attachment uploads are size-bounded, sanitize display filenames, clean multipart temporary files, and default downloads to attachment; only validated safe image formats are served inline with `nosniff`.
- Bash and stdio MCP are not covered by the sensitive-filename filter; they are permission-gated but remain powerful local code execution.
- Shell commands are classified by the grammar of the shell that actually runs them: cmd.exe/PowerShell on Windows, POSIX `sh` elsewhere. Classification produces three outcomes. `dangerous` labels are catastrophic and effectively irreversible, and are hard-denied in every permission mode including `bypassPermissions`. `sensitive` labels are serious but recoverable (force-push, package install, service or permission changes) and always require human approval, but stay approvable so normal development is still possible. A command that cannot be classified statically — a variable-expanded program name, an unbalanced quote, a base64 `-EncodedCommand` payload — is treated as needing approval, never as safe.
- The command review requirement sits above stored permission rules, workflow preferences, and permission modes. Neither a wildcard `allow` rule, nor `bypassPermissions`, nor disabling exec confirmation can turn a `sensitive` or unclassifiable command into a silent execution. An explicit per-command session approval from the user still applies.
- Danger reflection is an optional second gate that runs when a summary model is configured. Before an action that would otherwise execute with no human involvement, an isolated model call reviews the proposed action and returns proceed/confirm/block. It can only make the outcome stricter: a reflection can downgrade an allow into an approval prompt or a denial, but can never upgrade an approval or denial into an allow, so static policy remains the ceiling. The reviewed action is passed as fenced untrusted data and the reflector is instructed never to obey instructions inside it; it has no tools. Any timeout, provider error, malformed verdict, or attempted tool call fails closed to human approval rather than to execution. Note the disclosure boundary: reflection sends the proposed command or tool input to the configured summary-model provider, which persisted tool events deliberately do not contain. Reflection stays inert when no summary model is configured, and it is skipped for read-risk tools and for commands already on the built-in safe allowlist.
- MCP registry entries can launch local stdio processes; registry API responses expose environment variable names only, not stored values.
- Backend API keys are stored locally and are never returned by API responses; responses expose only whether a key is configured.
- The monitoring snapshot aggregates local runtime/database counters. It is not a hosted or cloud monitoring system and should remain available only to trusted local users.

## Remote tunnel checklist

Do not expose a raw `localhost:16888` URL to the public Internet without an authentication layer.

Recommended personal setup:

1. Prefer a named Cloudflare Tunnel plus Zero Trust Access for anything beyond short personal use. Temporary `trycloudflare.com` links are fine for quick phone follow-up, but the public URL changes and has no edge identity gate.
2. In Cloudflare Zero Trust (named tunnel), create a self-hosted Access application for the tunnel hostname.
3. Add an Access policy that only allows your email, identity provider group, or one-time PIN identity.
4. Start Autoto with a second local password gate when exposed beyond loopback:

   ```sh
   AUTOTO_EXPOSED=true AUTOTO_ACCESS_PASSWORD='use-a-long-random-password' ./autoto
   ```

5. **Phone follow-up (chat + agent approvals):** on the host, open Settings → Remote access and set an access password. If `cloudflared` is missing, use the host-local one-click install action; installation stays inside Autoto's home directory and does not start the tunnel. Then start the temporary tunnel (or use your named tunnel URL), scan the on-page QR code (or copy the URL), and sign in on the phone. Default **restricted** mode is enough to follow projects, message the agent, and approve/deny tools; it does not open the host terminal or native desktop dialogs.
6. Only if you explicitly need a remote shell, open Remote Access settings from `localhost`, allow full access, and set the default remote mode to `full` after confirming Cloudflare Access or another edge authentication layer is active. Existing remote sessions and WebSockets are revoked whenever this policy changes, so sign in again after saving.

Cloudflare's current dashboard flow is documented in their official Zero Trust docs for creating a remote tunnel and adding a self-hosted Access application:

- https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/create-remote-tunnel/
- https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/self-hosted-public-app/

## Known hardening work

Planned security improvements include:

- Complete project membership enforcement, real login sessions, per-route/per-user authorization scopes, and broader audit trails before exposing Autoto to other users or networks
- Optional encryption for locally stored backend API keys and MCP environment values
- Broader audit/search/retention controls for automation, channel, and notification history
- More granular tool approval and stronger shell/MCP containment; sensitive-path filtering alone does not sandbox executable tools
- Broader test coverage for filesystem, shell, integration-network, and restart-recovery boundaries
- Security review before adding `/task`, Slack/Discord, any generic IoT adapter, camera actions, door unlock, or cloud monitoring; these remain unimplemented
