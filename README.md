# Meow

Push notifications and quick replies for AI agents. An agent sends a notification or asks a question, your iPhone shows it, you answer from the lock screen, and the waiting CLI gets the answer.

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
Setup detects Claude Code and Codex, asks before editing their configuration,
and sends one test notification, including when neither agent is installed yet.

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
sh /tmp/meow-install.sh --version 0.1.0 --install-dir "$HOME/.local/bin"
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
bin/meow ask "Deploy to production?" --choices approve,reject
```

Setup asks for the Device Key shown in the app. Answer questions in the iOS app
or the notification. For push delivery, configure direct APNs or the
[wake gateway](docs/wake-gateway.md); without a provider, messages still appear
in the inbox and push is recorded as `skipped`.

## CLI

```
meow <message>                         info notification
meow send <message> --priority high    reads stdin when no message is given
meow ask <question> [--choices a,b] [--timeout 300] [--no-wait] [--json]
meow wait <question-id>
meow login [--key meow_…] [--server URL]
meow whoami
meow setup [--server URL]
meow setup claude|codex [--uninstall] [--no-summary] [--no-skill]
meow doctor
```

`meow ask` prints the answer to stdout. Exit codes: `0` answered, `3` rejected, `4` expired, `5` cancelled, `130` interrupted (Ctrl-C cancels the question). It keeps waiting through network failures and server restarts.

Configuration lives in `~/.config/meow/config.json` (mode 0600); `MEOW_KEY`, `MEOW_SERVER` and `MEOW_SOURCE` override it. No server address is assumed: setup/login use the address supplied by the app or your existing configuration.

## Local inbox and search

The iOS app saves synchronized messages in a local SQLite database. Search,
All / Unread / Pending filters, and display pagination use those saved messages
without network requests, including history beyond the first visible page.
Saved history remains searchable after restarting the app while offline.

On foreground entry or pull-to-refresh, the app loads saved messages first and
fetches every page of retained server history in the background. Only a complete
snapshot reconciles deletions; a failed page preserves the previous cache. This
uses the existing API and needs no server upgrade. The first synchronization can
take longer; empty results show progress while synchronization is outstanding.
The notification extension does not open this database: background notifications
enter the cache when the main app fetches them. Switching servers or re-registering
clears the old credential's cache.

Question expiry, authorization, idempotency, and acceptance of replies are still
enforced by the server. Cached question state does not grant permission to approve.

## Notifications during Focus / Do Not Disturb

| Priority | iOS behavior |
| --- | --- |
| `low` | Passive; no sound or screen wake. |
| `normal` | Standard notification; respects Focus. Default for `meow send`. |
| `high` | Time Sensitive; can interrupt Focus when allowed. Default for `meow ask`. |

In iPhone Settings → Notifications → Meow, enable **Time Sensitive Notifications**.
Also enable Time Sensitive Notifications in Settings → Focus → your Focus → Apps.
Meow's Settings shows the app's permission and has a time-sensitive test button;
it cannot inspect or change each Focus's allowlist. From the CLI, use
`meow send --priority high "Your decision is needed"`.
Both direct APNs and the wake gateway use the same priority mapping.

Audible notifications use the bundled `meow.wav` cat sound. Tap
**Notification sound → Meow** in the app's Settings to preview it. Low-priority
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

```sh
# After installing the CLI using one of the methods above:
meow setup --server https://your-meow-server     # asks for the Device Key, connects every agent found
```

`meow setup` checks the server, asks for the Device Key from the app (it is never passed as a flag, so it stays out of shell history), connects Claude Code and Codex if they are installed, and sends one test notification. `meow setup claude` or `meow setup codex` connects a single agent. `meow doctor` re-runs the checks without changing anything and says what to fix.

`meow setup` shows what it will change and asks first; each edited file is backed up as `<file>.bak.<timestamp>`. Re-running is safe, and `--uninstall` removes only what meow added.

| Agent | What is installed | You get notified when |
| --- | --- | --- |
| Claude Code | async `Stop` and `Notification` hooks in `settings.json` (`$CLAUDE_CONFIG_DIR` or `~/.claude`), skill in `skills/meow/` | a task finishes; a permission prompt or MCP form has been waiting about 6 seconds |
| Codex | top-level `notify` in `config.toml` (`$CODEX_HOME` or `~/.codex`), skill in `~/.agents/skills/meow/` | a turn finishes |

- Completion notifications include the first 200 characters of the agent's last message; pass `--no-summary` to leave it out.
- The skill teaches the agent to ask you with `meow ask` when it needs a decision.
- Codex runs one `notify` command. Setup preserves an existing handler (including
  Computer Use), running it alongside Meow with the original JSON argument.
  Re-running setup does not duplicate the handler; uninstall restores it.
  `--force` explicitly replaces it instead. Each handler runs independently;
  Meow has a five-second HTTP timeout and the original command has a separate
  thirty-second timeout. Output remains silent and failures go to `hook.log`.
- The hook command (`meow hook …`) always exits 0 and prints nothing, so it never blocks or disturbs the agent. Problems are logged to `hook.log` next to the meow config. Set `MEOW_HOOK_DISABLED=1` to silence it.

## HTTP API

All requests use `Authorization: Bearer <token>`. There are two credentials:

- **Device Key** (`meow_…`): send-only, for agents and scripts. Scopes `notify` and `ask`. Revocable. It can never answer questions.
- **Device token** (`mdt_…`): held by the iOS app. Answers questions and manages keys.

Only SHA-256 hashes of both are stored.

| Endpoint | Credential | |
| --- | --- | --- |
| `POST /v1/notifications` | Device Key | JSON `{title, body, source, priority, type}` or a plain-text body |
| `POST /v1/questions` | Device Key | `{question, body, source, choices, timeout}` → `{id, status, expires_at, …}` |
| `GET /v1/questions/{id}` | Device Key with `ask`, or device token | current state |
| `GET /v1/questions/{id}/wait?timeout=30` | Device Key | long poll until resolved, at most 60s |
| `POST /v1/questions/{id}/cancel` | Device Key | |
| `POST /v1/questions/{id}/responses` | device token | `{answer}`; `Idempotency-Key` required |
| `POST /v1/devices` | none | register; returns the device token and the first Device Key once |
| `GET/PATCH /v1/device`, `GET/POST /v1/keys`, `DELETE /v1/keys/{id}` | device token | |
| `GET /v1/inbox`, `POST /v1/notifications/{id}/read`, `…/delivered` | device token | |

```sh
curl -H "Authorization: Bearer $MEOW_KEY" -d "Build completed" "$MEOW_SERVER/v1/notifications"
```

Question lifecycle: `pending` → `answered` | `rejected` | `expired` | `cancelled`. Exactly one response is accepted per question. A retry with the same `Idempotency-Key` and answer returns the stored result. Any other second response gets `409`, and a response after expiry gets `410`. Expiry never approves anything.

`POST` requests that create resources accept an optional `Idempotency-Key`; replays return `200` with `Idempotent-Replayed: true`.

Push status is recorded per notification: `queued`, `accepted` (APNs accepted it, which is not proof it was displayed), `failed` or `skipped`. `delivered_at` and `read_at` are reported by the app.

## Server configuration

For a persistent Docker deployment with Caddy HTTPS, see [deploy/README.md](deploy/README.md).

| Variable | Default | |
| --- | --- | --- |
| `MEOW_ADDR` | `:8080` | |
| `MEOW_DB` | `meow.db` | keep it on a persistent local volume |
| `MEOW_RETENTION` | `720h` | resolved questions and notifications older than this are pruned |
| `MEOW_TRUST_PROXY` | | `true` to rate-limit by `X-Forwarded-For` behind a reverse proxy |
| `MEOW_APNS_KEY_FILE`, `MEOW_APNS_KEY_ID`, `MEOW_APNS_TEAM_ID`, `MEOW_APNS_TOPIC` | | APNs token auth (.p8); push is disabled when unset |

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

Notification actions: Approve requires unlocking the phone, Reject is destructive, and free-text questions get a reply field. Questions with other choices are answered in the app. Answers are saved before sending and retried with the same `Idempotency-Key`, so an answer given offline or from the lock screen is applied at most once.

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
