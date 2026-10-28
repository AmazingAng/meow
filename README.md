# Meow

A minimal notification app for AI agents. Meow V1 tells you when work finishes, fails, or needs your attention. Read it on your iPhone and continue in the agent on your computer. V1 has no phone replies or approval actions.

```
server/   Go API server: one process, one SQLite file (WAL), APNs delivery
cli/      the `meow` command
client/   Go client for the HTTP API (used by the CLI and the server tests)
ios/      SwiftUI app (iOS 17+) and its notification service extension
```

## Installation

The CLI is available as a ready-to-run binary for macOS and Linux, on Apple
Silicon/ARM64 and Intel/AMD64. You do not need Go or a GitHub account.

### macOS with Homebrew

```sh
brew install amazingang/tap/meow
meow setup --server https://YOUR_MEOW_SERVER
```

Upgrade with `brew upgrade meow`; uninstall with `brew uninstall meow`.

### Direct installation on macOS or Linux

Copy the **install and setup command** from the iPhone app. It includes your
server address, installs the CLI and starts setup in the terminal. For example:

```sh
curl -fsSL https://raw.githubusercontent.com/AmazingAng/meow/main/scripts/install.sh | sh -s -- --server https://YOUR_MEOW_SERVER
```

Paste your Device Key when setup asks for it. The copied command contains no key.
Setup detects Claude Code, Codex and Cursor, asks before editing their
configuration, and sends one test notification, including when no agent is
installed yet.

To install without starting setup:

```sh
curl -fsSL https://raw.githubusercontent.com/AmazingAng/meow/main/scripts/install.sh | sh
```

The installer verifies SHA-256 before replacing the binary, installs in
`~/.local/bin`, and does not require sudo or edit shell configuration. If this
directory is not on your PATH, it prints the line to add to your shell profile:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

The combined command starts setup using the installed binary's absolute path,
so it works immediately even before that PATH change. To pin a release or choose
a directory, download the script and pass its options:

```sh
curl -fsSLo /tmp/meow-install.sh https://raw.githubusercontent.com/AmazingAng/meow/main/scripts/install.sh
sh /tmp/meow-install.sh --version 0.1.2 --install-dir "$HOME/.local/bin"
```

For manual installation, download the matching archive and `checksums.txt` from
[GitHub Releases](https://github.com/AmazingAng/meow/releases/latest), verify its
checksum, extract `meow`, and put it in your PATH. Run `meow doctor` if setup or
push delivery needs attention.

## Run a local server from source

```sh
go build -o bin/meow-server ./server/cmd/meow-server
go build -o bin/meow ./cli/cmd/meow
MEOW_ADDR=:8080 bin/meow-server
```

Open the iOS app and enter this server's address. On a physical phone, use the
computer's LAN address, such as `http://192.168.31.235:8080`, while connected to
the same Wi-Fi. The app creates the device and Device Key automatically.

In another terminal, run the setup command copied from the app, or:

```sh
bin/meow setup --server http://127.0.0.1:8080
bin/meow "Build completed"
bin/meow send --alert --priority high --title "Needs your attention" "Deploy to production? Please respond on your computer."
```

Setup asks for the Device Key shown in the app. Respond to questions in the agent
on your computer. For push delivery, configure direct APNs or the
[wake gateway](docs/wake-gateway.md); without a provider, messages still appear
in the inbox and push is recorded as `skipped`.

## CLI

```
meow <message>                         info notification
meow send <message> --priority high    reads stdin when no message is given
meow login [--key meow_…] [--server URL]
meow whoami
meow setup [--server URL]
meow setup claude|codex|cursor [--uninstall] [--no-summary]
meow doctor
```

`meow ask` and `meow wait` are unavailable in the V1 CLI and return an explanatory error without creating a question. The server protocol and underlying question implementation remain for compatibility. Existing questions, options and historical answers remain readable in the app. Previously queued replies are preserved as copyable records and are not sent automatically.

The iPhone app suggests the official server, `https://api.meow.blue`, on first launch. You can edit the address for a self-hosted server; saved server choices are preserved. During setup, share the installation command with your computer and copy the Device Key separately when the terminal asks for it. The shared command does not include the key. The onboarding key preview shows only `meow_…` and the last 4 characters and cannot be selected; use **Copy Device Key** to copy the complete key, or reveal it for manual transfer. **Settings → Help** contains installation and HTTP API links plus an agent prompt that opens in a sheet for reading and copying. Settings shows the first and last 3 characters of the Device Key; tap the row to copy the complete key. A shareable setup command is available beside it. The old key-bearing login/curl examples are no longer shown in onboarding; `meow login` and the HTTP API remain supported.

Configuration lives in `~/.config/meow/config.json` (mode 0600); `MEOW_KEY`, `MEOW_SERVER` and `MEOW_SOURCE` override it. No server address is assumed: setup/login use the address supplied by the app or your existing configuration.

**Settings → Reset Device Key…** is a separate red row with a confirmation alert. It saves a replacement key and revokes the previous key without changing the iPhone's registration or message history. Copy the new key and run `meow login` on each sender. If old-key revocation fails, the app explains the failure; tapping reset again retries that revocation without creating another key.

## Local inbox and search

The iOS app saves synchronized messages in a local SQLite database. Search, grouping and display pagination use those saved messages
without network requests, including history beyond the first visible page.
Saved history remains searchable after restarting the app while offline.

Entering a session, project or Agent group marks the current matching snapshot read once. New arrivals remain unread. Swipe a message or group left to mark it read or delete it; group deletion confirms the matching message count and removes successful items from both server and iPhone. The header contains only view switching, deletion and search; read actions remain on groups and messages. Search and pagination never broaden these operations beyond the selected IDs.

On foreground entry or pull-to-refresh, the app loads saved messages first and
fetches every page of retained server history in the background. Only a complete
snapshot reconciles deletions; a failed page preserves the previous cache. This
uses the existing API and needs no server upgrade. The first synchronization can
take longer; empty results show progress while synchronization is outstanding.
The notification extension does not open this database: background notifications
enter the cache when the main app fetches them. Switching servers or re-registering
clears the old credential's cache.

Legacy question states remain visible as history. Reading a notification does not answer a question or approve an action.

## Notifications during Focus / Do Not Disturb

| Priority | iOS behavior |
| --- | --- |
| `low` | Passive; no sound or screen wake. |
| `normal` | Standard notification; respects Focus. Default for `meow send`. |
| `high` | Time Sensitive; can interrupt Focus when allowed. Use for failures or requests for attention. |

In iPhone Settings → Notifications → Meow, enable **Time Sensitive Notifications**.
Also enable Time Sensitive Notifications in Settings → Focus → your Focus → Apps.
Meow's **During Focus** button explains both steps and offers to open the iPhone notification settings. Meow cannot inspect or change each Focus's allowlist. From the CLI, use
`meow send --priority high "Your decision is needed"`.
Both direct APNs and the wake gateway use the same priority mapping.

Audible notifications use the bundled `meow.wav` cat sound. Tap
**Notification sound → Cat sound** in the app's Settings to preview it when changing sounds. Low-priority
notifications remain silent. The sound is bundled with both the app and its
notification extension; the extension also applies it to older servers' default
sounds. Existing iOS sound, silent-mode and Focus settings still apply.
The recording is `kitten10.wav` by Department64, available under CC0 from
[Freesound](https://freesound.org/people/Department64/sounds/64005/).

Time Sensitive notifications respect the Ring/Silent switch. Bark's stronger
Critical Alerts can also sound in silent mode, but require an Apple-approved
[Critical Alerts entitlement](https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.developer.usernotifications.critical-alerts)
and separate user consent. Meow does not currently claim this capability.

## Agent integrations

### Agent skill

The [`meow` skill](skills/meow/SKILL.md) teaches Claude Code, Codex and other
agents to install the CLI, guide setup, troubleshoot and send notifications.
Use Meow CLI 0.1.7 or later for all three supported agent integrations. Check `meow version` before setup; `meow doctor` does not enforce this minimum. Upgrade an existing Homebrew installation with `brew update && brew upgrade amazingang/tap/meow`, or re-run the direct installer without `--server`; saved credentials are preserved. Install the skill, then ask the agent to set up Meow:

```sh
npx skills add AmazingAng/meow -g          # Node.js 22.20+; asks which agents to install to
npx skills add AmazingAng/meow -g -a claude-code codex -y
npx skills update meow -g
npx skills remove meow -g
```

The agent can install the CLI and run `meow doctor`, but you enter the Device
Key yourself in a terminal, in `meow setup`. Without Node.js, copy
`skills/meow/` to `~/.claude/skills/meow` or `~/.agents/skills/meow`.
The skills CLI sends anonymous telemetry unless `DISABLE_TELEMETRY=1` is set.

### Hooks

```sh
# After installing the CLI using one of the methods above:
meow setup --server https://your-meow-server     # asks for the Device Key, connects every agent found
```

`meow setup` checks the server, asks for the Device Key from the app (it is never passed as a flag, so it stays out of shell history), connects Claude Code, Codex and Cursor if they are installed, and sends one CLI connection test notification. This test does not verify agent hook authorization or execution. `meow setup claude`, `meow setup codex` or `meow setup cursor` connects a single agent. `meow doctor` re-runs the checks without changing anything and says what to fix.

`meow setup` shows what it will change and asks first; each edited file is backed up as `<file>.bak.<timestamp>`. Re-running is safe, and `--uninstall` removes only what meow added.

| Agent | What is installed | You get notified when |
| --- | --- | --- |
| Claude Code | async `Stop`, `StopFailure`, and `Notification` hooks in `settings.json` (`$CLAUDE_CONFIG_DIR` or `~/.claude`) | a turn ends; an API error ends a turn; a permission prompt or MCP form needs attention |
| Codex | async `Stop` and `PermissionRequest` in `hooks.json` (`$CODEX_HOME` or `~/.codex`) | a turn ends; a permission request needs attention (automatic review filtered when identified) |
| Cursor | `afterAgentResponse` capture and `stop` command in `hooks.json` (`~/.cursor`) | a turn ends (`completed`) or the loop stops with `error`; aborted turns are ignored |

- Stop titles say **New result**, without implying success. Hook bodies retain newlines and up to 4000 Unicode scalars. Claude Stop events without a non-whitespace assistant reply are ignored before formatting; missing, null or blank replies do not create empty results. `--no-summary` still sends a reminder for a real reply while omitting its body. Set `MEOW_LANGUAGE=zh` in the agent's launch environment for Chinese titles; otherwise titles are English.
- Banners parse Markdown into plain text before limiting the preview to 180 Unicode scalars and three lines. Emphasis and link labels remain readable without Markdown delimiters; code blocks and complete JSON objects/arrays are omitted. Literal punctuation inside inline code or escaped text is preserved. Inbox rows have a separate rich-text summary; the detail screen retains the original body. Direct APNs and relay hydration follow shared preview fixtures. This requires the updated iOS notification extension for relay/legacy-direct previews, and the updated server for direct previews when iOS cannot run the extension.
- Replies and decisions stay in the agent on the computer. Permission hooks do not mirror every native question card. Codex has no equivalent of Claude's API-error `StopFailure` in this integration. Cursor has no passive permission event: its permission hooks block the action when the command prints nothing, so Meow does not install one. Cursor's `stop` payload has no assistant text. Meow captures `afterAgentResponse.text` by conversation and `generation_id`, then `stop` sends that generation's snapshot. It never reads transcript bodies. If the snapshot is missing, the notification has no body. Replays reuse the first frozen payload and delivery key. Private snapshots live beside the CLI config, are isolated by server and Device Key, and expire after 24 hours (the server idempotency window); expired files are cleaned on the next Cursor hook. Re-run `meow setup cursor` after upgrading to install both hooks. See the [Cursor hooks reference](https://cursor.com/docs/hooks).
- Setup backs up changed files and outdated generated skills before removal, preserving customized content. It never installs or edits the skills.sh copy.
- Codex setup uses lifecycle hooks exclusively. Run `meow setup codex`, start a new Codex session, authorize **both** Meow hooks in `/hooks`, then finish a normal task and confirm its result arrives on your iPhone. Without hook authorization, automatic Codex task notifications are unavailable; manual `meow send` still works. Use a Codex version with lifecycle hook support.
- Setup automatically removes an old Meow `notify` entry and restores any external handler it wrapped (including Computer Use), with a backup before each edit. Foreign notify entries and hooks are preserved. Repeated setup is safe; no separate migration command is required. Restart old Codex sessions to load the new configuration. Cached invocations of the retired `meow hook codex` no longer send Meow pushes, but still forward an already-configured original handler until restart.
- `meow doctor` reports an old Meow notify entry as needing cleanup, and distinguishes configured hooks from previously observed Stop execution. A setup test proves CLI delivery only; a real task notification verifies the agent path. Execution evidence is **not** a live trust or phone-delivery check; Meow does not read undocumented trust databases. A policy/feature toggle or revoked trust can still prevent hooks from running. See the [Codex hook documentation](https://learn.chatgpt.com/docs/hooks) and [Claude hook reference](https://code.claude.com/docs/en/hooks).
- Hooks retry transport failures, HTTP 429 and 5xx up to **three total attempts**, with 1s/2s backoff, 5 seconds per request and a 20-second overall budget. Quota exhaustion and other permanent errors are not retried. Each attempt reuses the same body and idempotency key.
- Hooks print nothing and exit 0; sanitized failures go to `hook.log`. Claude and Codex lifecycle hooks are marked async, but host shutdown can cancel them: this is best-effort delivery, not a persistent queue. Cursor's stop command runs in the foreground and is limited to 30 seconds. `MEOW_HOOK_DISABLED=1` silences Meow, while preserving a forwarded handler.
- Agent reminders carry optional session context. The inbox shows the session name (or a short ID), agent and workspace. Avatar colors are stable: agents have a color family, workspaces use nearby hues and sessions use related tones. Session/workspace IDs keep colors unchanged after a rename. Existing messages without context remain readable, but their original session cannot be reconstructed.
- Codex names come from the latest matching record in `$CODEX_HOME/session_index.jsonl` (default `~/.codex`). Claude names come from `custom-title` / `ai-title` records in the hook event's own `transcript_path`, preferring an explicit name. Cursor names come from the composer title for that conversation id in Cursor's state database; only the `name` field is read. Lookups inspect at most the last 2 MiB of a regular file; missing, unreadable or out-of-window metadata falls back to the session ID and never prevents a notification. Full local workspace paths are hashed before sending.
- Codex `PermissionRequest` fires **before** automatic review, so it does not prove that a person is waiting. Meow's notification policy suppresses per-tool permission reminders, **including MCP/Computer Use**, when the supplied transcript identifies the same session and its latest turn context explicitly identifies the current turn's `approvals_reviewer` as `auto_review` (Approve for me). This prevents repeated empty attention notifications during goal execution; it is not a goal-specific filter and also applies to ordinary auto-review turns. It does not approve any action or change Codex's permissions. A Computer Use app-level prompt can still require human confirmation on the computer; in auto-review turns it is not mirrored to the phone. Manual-review turns retain permission reminders. The local lookup reads the session header and scans backward to the latest turn context, even when a long turn or screenshot record exceeds 2 MiB, using 64 KiB blocks and a 64 MiB per-record memory guard. Rollout metadata is not a stable Codex API: missing, malformed, unreadable, oversized or mismatched metadata preserves the reminder. Global `config.toml`, waiting time and empty bodies are never used to guess whether a turn is automatic; Stop/result notifications are unchanged. Updating the CLI at the path used by existing hooks applies this fix without reinstalling hooks.
- Other agents/scripts can supply `meow send --source "Agent" --workspace "project" --workspace-id "project-1" --session "Deploy review" --session-id "session-1" "Finished"`. The corresponding `MEOW_WORKSPACE`, `MEOW_WORKSPACE_ID`, `MEOW_SESSION` and `MEOW_SESSION_ID` variables provide defaults. Names are display labels; provide IDs to distinguish identically named workspaces/sessions and retain identity across renames.
- Explicit `meow send` notifications and automatic Stop reminders are not deduplicated against each other in V1. No heuristic time window can silently suppress a result.
- Codex Stop skips temporary suggestion output only when `transcript_path` is explicitly `null` and the complete reply is a single `suggestion` (string/null) or `suggestions` (array of titled records) JSON envelope. Persistent chats, other JSON/results, missing metadata and permission requests still notify. Stop does not expose Desktop's `composer_predictions` source marker, so this is a conservative compatibility filter, not a general background-task classifier. An intentional ephemeral turn returning that exact envelope is also skipped; use `meow send` for such a result. The retired legacy `meow hook codex` entry sends no Meow notifications.

## Notification modules and integrations

Agents and ordinary services share the same V1 notification object. `message/` owns the protocol and validation, `adapters/` converts external events, and `client/` handles HTTP transport. Existing CLI commands and the `/v1` API remain compatible. Services can send a JSON object with `meow send --json --idempotency-key EVENT_ID < notification.json` or POST the same object directly.

See the [module boundaries and integration guide](docs/architecture.md), [JSON Schema](contracts/notification-v1.schema.json), and [cross-language fixtures](contracts/fixtures/notifications.json). Built-in adapters emit explicit `event` values (`result`, `attention`, `failure`), independently of titles, `type` and `priority`. Events without this optional field remain generic. Codex permission reminders include only the human explanation (`description` / `justification`); tool names, commands and other parameters stay on the computer. Without an explanation the notification still shows its attention title and session context. `--no-summary` omits the body.

For scripts, CI, backups and monitors, the [service integration guide](skills/meow/references/http.md#project-and-session-for-services) explains CLI/HTTP examples and each context field. `source` names the sending service, `context.workspace` is the project name, and `context.session` is the task/run/incident name. Stable `workspace_id` and `session_id` distinguish work across updates; they are separate from per-notification idempotency keys. No agent hooks, custom adapter or server change are needed.

## HTTP API

All requests use `Authorization: Bearer <token>`. There are two credentials:

- **Device Key** (`meow_…`): send-only, for agents and scripts. Scopes `notify` and `ask`. Revocable. It can never answer questions.
- **Device token** (`mdt_…`): held by the iOS app. Answers questions and manages keys.

Only SHA-256 hashes of both are stored.

| Endpoint | Credential | |
| --- | --- | --- |
| `POST /v1/notifications` | Device Key | JSON `{title, body, source, priority, type, event?, context?}` or a plain-text body |
| `POST /v1/questions` | Device Key | `{question, body, source, choices, timeout}` → `{id, status, expires_at, …}` |
| `GET /v1/questions/{id}` | Device Key with `ask`, or device token | current state |
| `GET /v1/questions/{id}/wait?timeout=30` | Device Key | long poll until resolved, at most 60s |
| `POST /v1/questions/{id}/cancel` | Device Key | |
| `POST /v1/questions/{id}/responses` | device token | `{answer}`; `Idempotency-Key` required |
| `POST /v1/devices` | none | register; returns the device token and the first Device Key once |
| `GET/PATCH /v1/device`, `GET/POST /v1/keys`, `DELETE /v1/keys/{id}` | device token | |
| `GET /v1/inbox`, `POST /v1/notifications/{id}/read`, `…/delivered` | device token | |
| `DELETE /v1/notifications/{id}` | device token | permanently delete one message; `204`, or `404` if missing |
| `DELETE /v1/inbox?from=…&before=…` | device token | delete this device's messages created in `[from, before)`; Unix milliseconds; returns `{deleted: N}` |

```sh
curl -H "Authorization: Bearer $MEOW_KEY" -d "Build completed" "$MEOW_SERVER/v1/notifications"
```

The optional notification `context` object accepts `workspace`, `workspace_id`, `session` and `session_id`. Display names allow 200 Unicode characters; IDs allow 128. Values are trimmed and must be single-line text without control characters. Context is returned by notification detail and inbox endpoints and included in creation idempotency. Omitting it preserves the previous request format. Update the server, CLI and iOS app for session context to appear end to end; server startup migrates SQLite to schema v3 while preserving history.

In the iOS inbox, swipe left and tap the trash icon to delete one message. The header trash menu clears a selected time range after confirmation. Time ranges use the iPhone's local calendar and apply to all messages, including those hidden by search. Deletion removes server records and the local cache; it cannot be undone. Update the server before using these controls; unsupported endpoints and network failures keep local messages and show an error. Bulk deletion requires both bounds (`0 <= from < before <= 253402300799999`); missing, duplicate and unknown query parameters are rejected. Deleting a legacy question message also removes its question and responses and wakes waiting clients with `404`; it never approves anything. Creation idempotency records keep their normal retention to prevent sender retries from recreating deleted messages. An APNs notification already accepted by Apple may remain in Notification Center.

Question lifecycle: `pending` → `answered` | `rejected` | `expired` | `cancelled`. Exactly one response is accepted per question. A retry with the same `Idempotency-Key` and answer returns the stored result. Any other second response gets `409`, and a response after expiry gets `410`. Expiry never approves anything.

`POST` requests that create resources accept an optional `Idempotency-Key`; replays return `200` with `Idempotent-Replayed: true`.

Push status is recorded per notification: `queued`, `accepted` (APNs accepted it, which is not proof it was displayed), `failed` or `skipped`. `delivered_at` and `read_at` are reported by the app.

## Server configuration

For a persistent Docker deployment with Caddy HTTPS, see [deploy/README.md](deploy/README.md).

| Variable | Default | |
| --- | --- | --- |
| `MEOW_ADDR` | `:8080` | |
| `MEOW_DB` | `meow.db` | keep it on a persistent local volume |
| `MEOW_RETENTION` | `720h` | server delivery-cache lifetime (minimum `1h`); startup and every 5 minutes prune expired content; downloaded iPhone history is independent |
| `MEOW_TRUST_PROXY` | | `true` to rate-limit by `X-Forwarded-For` behind a reverse proxy |
| `MEOW_APNS_KEY_FILE`, `MEOW_APNS_KEY_ID`, `MEOW_APNS_TEAM_ID`, `MEOW_APNS_TOPIC` | | APNs token auth (.p8); push is disabled when unset |

### Message cache and iPhone history

The server is a short-lived delivery cache, not the archive of the iPhone's inbox. During the client rollout it defaults to **720 hours (30 days)** of message content, then automatically deletes expired notifications and resolved legacy questions on startup and every **5 minutes**. Pending legacy questions remain until resolved/expired. After every iPhone has independent local-history support (build 17 or later), an operator may explicitly shorten the window, for example `MEOW_RETENTION=12h` (minimum `1h`). Disabling storage is unsupported: a gateway wake needs the message to remain available for the iPhone to fetch.

Messages already downloaded by the app stay in its local SQLite history and remain searchable/readable after server expiry, refresh and app restart. Server omission or a missing-detail `404` is not a local-delete instruction. Unread counts include local-only history; old messages can still be marked read and explicitly deleted on the iPhone. Deleting a message/range in the app removes the current iPhone's saved copy and any matching server content. Deletion/expiry on the server or another client does not erase other already-downloaded copies. A device that stays offline beyond the cache window may miss messages; a new installation cannot recover history that has already expired on the server.

Content-free per-device send receipts and creation-idempotency metadata are kept for **24 hours**, independently of body retention. Expiry or explicit deletion does not reset daily message limits or cause a replayed event to create another push. A retry whose body has expired returns `404 not_found` within the idempotency window; use a new event/key for a new notification.

**Upgrade the iOS app with independent local-history support before shortening server retention.** Older app versions replace their cache with the server's list and may lose downloaded history. The rollout default is `MEOW_RETENTION=720h`; keep it until every iPhone is on build 17 or later. Existing explicit settings such as `MEOW_RETENTION=12h` in `.env` or a service environment still override this default: change them to `720h` **before restarting** during rollout. Increasing retention cannot recover content already deleted; even 720h is finite and cannot preserve older clients' history indefinitely. Automatic server cleanup does not delete devices or rotate keys; inactive-device cleanup remains a separate opt-in setting.

TTL is logical deletion, not guaranteed forensic erasure of SQLite free pages, WAL files or existing backups. Configure encrypted storage and a separate backup retention policy when appropriate.

Back up the live database with `meow-server backup /path/to/backup.db` (a consistent `VACUUM INTO` snapshot). Do not copy the `.db` file while the server is running.

## iOS app

```sh
cd ios
xcodegen generate          # brew install xcodegen; creates Meow.xcodeproj from project.yml
open Meow.xcodeproj
```

- Bundle ID `blue.meow.app`, App Group `group.blue.meow.app`, team set in `project.yml`.
- `ios/scripts/make_assets.py <design/assets> ios/Meow/Resources/Assets.xcassets` regenerates the icon, logos and mascot from the design masters.
- Debug builds accept launch arguments `-meowServer http://127.0.0.1:8080 -meowAutoRegister YES` to skip onboarding against a local server.
- Simulator pushes: fill in the IDs in `ios/scripts/approval.apns` and run `xcrun simctl push booted blue.meow.app ios/scripts/approval.apns`.

Meow V1 delivers notifications without reply or approval actions, including on the lock screen. Existing questions, options and historical answers remain readable; continue in the agent on your computer to respond. Previously saved unsent replies are retained as copyable records in Settings and are not automatically submitted.

## Development

```sh
go test -race ./...
```

## Releasing

Push a version tag; GitHub Actions runs the tests and publishes prebuilt `meow` binaries with checksums to GitHub Releases:

```sh
git tag v0.1.0 && git push origin v0.1.0
```

The release workflow also tests the installer. After the release finishes,
update the Homebrew tap from the published checksums (no cross-repository token
is stored in Actions):

```sh
gh repo clone AmazingAng/homebrew-tap ../homebrew-tap
python3 scripts/update_homebrew.py 0.1.0 --output ../homebrew-tap/Formula/meow.rb
git -C ../homebrew-tap add Formula/meow.rb
git -C ../homebrew-tap commit -m "Update Meow to 0.1.0"
git -C ../homebrew-tap push
```

### Self-hosting without Apple credentials

The optional [wake gateway](docs/wake-gateway.md) keeps the APNs key on the gateway host. Set `MEOW_PUSH_GATEWAY_URL` on your server; the iOS app registers automatically and fetches message content directly from your server. No invitation, additional account or manual gateway setup is required. Direct APNs remains the default.
